// Copyright 2026 NVIDIA CORPORATION & AFFILIATES
// SPDX-License-Identifier: Apache-2.0

package firmwareimage

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/types"
	strictjson "sigs.k8s.io/json"
)

// Firmware data image v1 is a concrete image manifest with one plain/gzip tar
// layer, containing only the fixed regular payload and optional parent entries.
const (
	MaxPayloadBytes    int64 = 512 << 20
	MaxCompressedBytes int64 = 520 << 20
	MaxExpandedBytes   int64 = 513 << 20
	MaxMetadataBytes   int64 = 64 << 10
	maxArchiveEntries        = 16
)

type transportLimits struct {
	metadata, compressed, expanded, payload int64
	entries                                 int
}

func defaultTransportLimits() transportLimits {
	return transportLimits{MaxMetadataBytes, MaxCompressedBytes, MaxExpandedBytes, MaxPayloadBytes, maxArchiveEntries}
}

func sha256Digest(content []byte) string {
	hash := sha256.Sum256(content)
	return "sha256:" + hex.EncodeToString(hash[:])
}

func validSHA256(hash v1.Hash) bool {
	if hash.Algorithm != "sha256" || len(hash.Hex) != 64 || strings.ToLower(hash.Hex) != hash.Hex {
		return false
	}
	_, err := hex.DecodeString(hash.Hex)
	return err == nil
}

func payloadDescriptor(raw []byte, requestedDigest string, limits transportLimits) (v1.Descriptor, error) {
	if int64(len(raw)) > limits.metadata || sha256Digest(raw) != requestedDigest {
		return v1.Descriptor{}, errors.New("firmware image manifest exceeds its limit or does not match the requested digest")
	}
	var manifest v1.Manifest
	strictErrors, err := strictjson.UnmarshalStrict(raw, &manifest, strictjson.DisallowDuplicateFields)
	if err != nil || len(strictErrors) != 0 {
		return v1.Descriptor{}, errors.New("firmware image manifest is invalid JSON")
	}
	if manifest.SchemaVersion != 2 || !manifest.MediaType.IsImage() {
		return v1.Descriptor{}, errors.New("firmware data image v1 requires a concrete OCI/Docker v2 image manifest; indexes are unsupported")
	}
	config := manifest.Config
	if !config.MediaType.IsConfig() || !validSHA256(config.Digest) || config.Size <= 0 ||
		config.Size > limits.metadata || len(config.URLs) != 0 || config.Data != nil {
		return v1.Descriptor{}, errors.New("firmware image config descriptor must identify bounded OCI/Docker config with a SHA256 digest")
	}
	if len(manifest.Layers) != 1 {
		return v1.Descriptor{}, errors.New("firmware data image v1 requires exactly one payload layer")
	}
	layer := manifest.Layers[0]
	if !validSHA256(layer.Digest) || layer.Size <= 0 || layer.Size > limits.compressed ||
		len(layer.URLs) != 0 || layer.Data != nil {
		return v1.Descriptor{}, errors.New("firmware payload descriptor is invalid, external, embedded or exceeds the compressed limit")
	}
	switch layer.MediaType {
	case types.OCIUncompressedLayer, types.OCILayer, types.DockerLayer:
		return layer, nil
	default:
		return v1.Descriptor{}, errors.New("firmware data image v1 supports only plain or gzip tar layers")
	}
}

// extractPayload never writes archive-provided paths. Entry count is the number
// of logical entries returned by archive/tar; the byte limit also bounds hidden
// extension headers, all padding, and decompressor trailers.
func extractPayload(ctx context.Context, expanded io.Reader, output io.Writer, limits transportLimits) error {
	bounded := &expandedReader{reader: contextReader{ctx: ctx, reader: expanded}, remaining: limits.expanded}
	archive := tar.NewReader(bounded)
	found := false
	for count := 0; ; count++ {
		header, err := archive.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("read firmware archive: %w", err)
		}
		if count >= limits.entries {
			return errors.New("firmware archive exceeds its logical entry limit")
		}
		if err := validateArchiveHeader(header); err != nil {
			return err
		}
		if header.Typeflag == tar.TypeDir {
			continue
		}
		if found {
			return errors.New("firmware archive contains duplicate payloads")
		}
		found = true
		if header.Size <= 0 || header.Size > limits.payload {
			return errors.New("firmware payload must be nonempty and within the payload size limit")
		}
		size, err := io.Copy(output, io.LimitReader(archive, limits.payload+1))
		if err != nil {
			return fmt.Errorf("copy firmware payload: %w", err)
		}
		if size != header.Size {
			return errors.New("firmware archive payload was truncated")
		}
	}
	// Tar EOF is not compressed-stream EOF. Drain to check CRC/truncation and
	// expanded bounds; only ordinary zero tar padding may follow the terminator.
	if _, err := io.Copy(zeroPaddingWriter{}, bounded); err != nil {
		return fmt.Errorf("read firmware archive trailer: %w", err)
	}
	if !found {
		return errors.New("firmware archive does not contain the fixed payload")
	}
	return nil
}

func validateArchiveHeader(header *tar.Header) error {
	if len(header.PAXRecords) != 0 || header.Format == tar.FormatGNU {
		return errors.New("firmware data image v1 does not support PAX metadata or GNU/sparse archive formats")
	}
	if strings.HasPrefix(header.Name, "/") || strings.Contains(header.Name, "\\") {
		return errors.New("firmware archive contains an absolute or unsupported path")
	}
	for _, component := range strings.Split(header.Name, "/") {
		if component == ".." {
			return errors.New("firmware archive contains path traversal")
		}
	}
	name := path.Clean(header.Name)
	if header.Typeflag == tar.TypeDir && (name == "." || name == "firmware") {
		return nil
	}
	if name != strings.TrimPrefix(PayloadPath, "/") ||
		header.Typeflag != tar.TypeReg {
		return errors.New("firmware archive permits only the fixed regular payload and its parent directories")
	}
	return nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(buffer []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(buffer)
}

type expandedReader struct {
	reader    io.Reader
	remaining int64
}

func (r *expandedReader) Read(buffer []byte) (int, error) {
	if r.remaining <= 0 {
		var extra [1]byte
		count, err := r.reader.Read(extra[:])
		if count != 0 {
			return 0, errors.New("expanded firmware layer exceeds its byte limit")
		}
		return 0, err
	}
	if int64(len(buffer)) > r.remaining {
		buffer = buffer[:r.remaining]
	}
	count, err := r.reader.Read(buffer)
	r.remaining -= int64(count)
	return count, err
}

type zeroPaddingWriter struct{}

func (zeroPaddingWriter) Write(buffer []byte) (int, error) {
	for _, value := range buffer {
		if value != 0 {
			return 0, errors.New("nonzero content follows firmware tar terminator")
		}
	}
	return len(buffer), nil
}
