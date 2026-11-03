// Copyright 2026 NVIDIA CORPORATION & AFFILIATES
// SPDX-License-Identifier: Apache-2.0

package firmwareimage

import (
	"compress/gzip"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	registrytransport "github.com/google/go-containerregistry/pkg/v1/remote/transport"
	"github.com/google/go-containerregistry/pkg/v1/types"
)

// TransportOptions configures explicit in-process registry access. AuthDir is a
// root containing one mounted directory per named authentication Secret.
type TransportOptions struct {
	AuthDir string
}

// Transport downloads immutable firmware data images. It does not own cache
// membership/publication and never executes an image or a credential helper.
type Transport struct {
	authDir      string
	limits       transportLimits
	roundTripper func() (http.RoundTripper, error)
}

// NewTransport creates a registry reader. Credentials and configured CA bundles
// are loaded per Fetch so projected file rotation does not require Pod restarts.
func NewTransport(options TransportOptions) (*Transport, error) {
	if options.AuthDir != "" && !filepath.IsAbs(options.AuthDir) {
		return nil, errors.New("firmware registry authentication directory must be absolute")
	}
	return &Transport{authDir: options.AuthDir, limits: defaultTransportLimits(), roundTripper: newRegistryRoundTripper}, nil
}

// Fetch writes destinationDir/firmware.bin only after authenticating the complete
// compressed layer. The caller owns this private staging directory and atomically
// publishes it after success. Temporary/partial files are removed on failure.
func (t *Transport) Fetch(ctx context.Context, reference, destinationDir string) error {
	if t == nil || ctx == nil {
		return errors.New("firmware image transport and context are required")
	}
	if _, err := ImageKey(reference); err != nil {
		return err
	}
	ref, err := name.NewDigest(reference, name.StrictValidation)
	if err != nil {
		return errors.New("firmware image requires a concrete SHA256 digest reference")
	}
	info, err := os.Lstat(destinationDir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("firmware destination must be an existing private staging directory")
	}
	keychain, err := loadRegistryAuth(t.authDir)
	if err != nil {
		return err
	}
	base, err := t.roundTripper()
	if err != nil {
		return err
	}
	if closer, ok := base.(interface{ CloseIdleConnections() }); ok {
		defer closer.CloseIdleConnections()
	}
	bounded := &registryRoundTripper{base: base, limits: t.limits, registry: ref.Context().RegistryStr(),
		manifestPath: "/v2/" + ref.Context().RepositoryStr() + "/manifests/" + ref.DigestStr()}
	opts := []remote.Option{remote.WithContext(ctx), remote.WithTransport(bounded), remote.WithAuthFromKeychain(keychain)}
	descriptor, err := remote.Get(ref, opts...)
	if err != nil {
		return registryRequestError(ctx, "read firmware manifest", err)
	}
	// The library verifies the digest before its internal metadata parsing, and
	// we independently verify exact bytes before interpreting the package format.
	layer, err := payloadDescriptor(descriptor.Manifest, ref.DigestStr(), t.limits)
	if err != nil {
		return err
	}
	bounded.blobPath = "/v2/" + ref.Context().RepositoryStr() + "/blobs/" + layer.Digest.String()
	blob, err := os.CreateTemp(destinationDir, ".compressed-*")
	if err != nil {
		return fmt.Errorf("create compressed firmware staging file: %w", err)
	}
	defer func() { _ = blob.Close() }()
	defer func() { _ = os.Remove(blob.Name()) }()
	if err := t.downloadLayer(ctx, ref, layer, blob, opts); err != nil {
		return err
	}
	return t.writePayload(ctx, blob, layer.MediaType, destinationDir)
}

func (t *Transport) downloadLayer(ctx context.Context, ref name.Digest, expected v1.Descriptor,
	output *os.File, opts []remote.Option) error {
	layer, err := remote.Layer(ref.Context().Digest(expected.Digest.String()), opts...)
	if err != nil {
		return registryRequestError(ctx, "open firmware layer", err)
	}
	input, err := layer.Compressed()
	if err != nil {
		return registryRequestError(ctx, "read firmware layer", err)
	}
	hash := sha256.New()
	written, readErr := io.Copy(io.MultiWriter(output, hash), io.LimitReader(input, t.limits.compressed+1))
	closeErr := input.Close()
	if readErr != nil || closeErr != nil {
		return registryRequestError(ctx, "read complete firmware layer", errors.Join(readErr, closeErr))
	}
	if written != expected.Size || written > t.limits.compressed ||
		"sha256:"+hex.EncodeToString(hash.Sum(nil)) != expected.Digest.String() {
		return errors.New("firmware layer does not match its complete compressed size and SHA256 digest")
	}
	if _, err := output.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("rewind verified firmware layer: %w", err)
	}
	return nil
}

func (t *Transport) writePayload(ctx context.Context, blob *os.File, mediaType types.MediaType, directory string) (err error) {
	var expanded io.Reader = blob
	if mediaType != types.OCIUncompressedLayer {
		decoder, decodeErr := gzip.NewReader(blob)
		if decodeErr != nil {
			return errors.New("firmware payload has an invalid gzip header")
		}
		defer func() { _ = decoder.Close() }()
		expanded = decoder
	}
	file := filepath.Join(directory, PayloadName)
	output, err := os.OpenFile(file, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("create firmware payload: %w", err)
	}
	defer func() {
		_ = output.Close()
		if err != nil {
			_ = os.Remove(file)
		}
	}()
	if err = extractPayload(ctx, expanded, output, t.limits); err != nil {
		return err
	}
	if err = output.Chmod(0444); err != nil {
		return fmt.Errorf("make firmware payload read-only: %w", err)
	}
	if err = output.Sync(); err != nil {
		return fmt.Errorf("sync firmware payload: %w", err)
	}
	if err = output.Close(); err != nil {
		return fmt.Errorf("close firmware payload: %w", err)
	}
	return nil
}

// registryRequestError deliberately excludes server bodies, token URLs and
// authentication headers from returned errors that become Kubernetes status.
func registryRequestError(ctx context.Context, operation string, err error) error {
	if ctx.Err() != nil {
		return fmt.Errorf("%s: %w", operation, ctx.Err())
	}
	var response *registrytransport.Error
	if errors.As(err, &response) {
		return fmt.Errorf("%s failed with registry HTTP status %d", operation, response.StatusCode)
	}
	return fmt.Errorf("%s failed: registry authentication, TLS, network or content verification error", operation)
}

type registryRoundTripper struct {
	base                   http.RoundTripper
	limits                 transportLimits
	registry, manifestPath string
	blobPath               string
}

func (t *registryRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.URL.Scheme != "https" {
		return nil, errors.New("firmware registry requests and redirects require HTTPS")
	}
	if request.Response != nil && request.Response.Request != nil {
		previous := request.Response.Request.URL
		if previous.Scheme != request.URL.Scheme || previous.Host != request.URL.Host {
			// OAuth refresh tokens travel in POST bodies, not only headers. A
			// 307/308 redirect can replay that body at an unrelated authority.
			if (request.Method != http.MethodGet && request.Method != http.MethodHead) ||
				(request.Body != nil && request.Body != http.NoBody) || request.ContentLength != 0 {
				return nil, errors.New("firmware registry redirects cannot forward request bodies across origins")
			}
		}
	}
	if request.Response != nil && request.URL.Host != t.registry {
		// Go's default redirect policy treats a different port on the same host
		// as credential-compatible. Registry credentials are host:port scoped.
		request = request.Clone(request.Context())
		request.Header.Del("Authorization")
		request.Header.Del("Cookie")
	}
	response, err := t.base.RoundTrip(request)
	if err != nil {
		return nil, err
	}
	maximum := t.limits.metadata + 1
	if response.StatusCode == http.StatusOK && t.matchesOriginalRequest(request, t.blobPath) {
		maximum = t.limits.compressed + 1
	}
	if response.StatusCode == http.StatusOK && t.matchesOriginalRequest(request, t.manifestPath) {
		mediaType, _, parseErr := mime.ParseMediaType(response.Header.Get("Content-Type"))
		if parseErr != nil || (types.MediaType(mediaType) != types.OCIManifestSchema1 &&
			types.MediaType(mediaType) != types.DockerManifestSchema2) {
			_ = response.Body.Close()
			return nil, errors.New("firmware registry must return a supported concrete image manifest")
		}
	}
	response.Body = &httpBodyLimit{ReadCloser: response.Body, remaining: maximum}
	return response, nil
}

func (t *registryRoundTripper) matchesOriginalRequest(request *http.Request, expectedPath string) bool {
	for depth := 0; request != nil && depth < 10; depth++ {
		if expectedPath != "" && request.URL.Host == t.registry && request.URL.Path == expectedPath {
			return true
		}
		if request.Response == nil {
			return false
		}
		request = request.Response.Request
	}
	return false
}

type httpBodyLimit struct {
	io.ReadCloser
	remaining int64
}

func (r *httpBodyLimit) Read(buffer []byte) (int, error) {
	if r.remaining <= 0 {
		return 0, errors.New("firmware registry HTTP response exceeds its byte limit")
	}
	if int64(len(buffer)) > r.remaining {
		buffer = buffer[:r.remaining]
	}
	count, err := r.ReadCloser.Read(buffer)
	r.remaining -= int64(count)
	return count, err
}

func newRegistryRoundTripper() (http.RoundTripper, error) {
	roots, err := registryCertPool()
	if err != nil {
		return nil, err
	}
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, errors.New("default registry HTTP transport is unsupported")
	}
	transport := base.Clone()
	transport.Proxy = http.ProxyFromEnvironment
	transport.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	return transport, nil
}
