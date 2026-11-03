// Copyright 2026 NVIDIA CORPORATION & AFFILIATES
// SPDX-License-Identifier: Apache-2.0

package firmwareimage

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/types"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type transportArchiveEntry struct {
	name, contents, link string
	kind                 byte
	format               tar.Format
	pax                  map[string]string
}

func transportArchive(entries []transportArchiveEntry) []byte {
	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	for _, entry := range entries {
		size := int64(0)
		if entry.kind == tar.TypeReg {
			size = int64(len(entry.contents))
		}
		ExpectWithOffset(1, writer.WriteHeader(&tar.Header{Name: entry.name, Typeflag: entry.kind,
			Mode: 0644, Size: size, Linkname: entry.link, Format: entry.format, PAXRecords: entry.pax})).To(Succeed())
		if size != 0 {
			_, err := writer.Write([]byte(entry.contents))
			ExpectWithOffset(1, err).NotTo(HaveOccurred())
		}
	}
	ExpectWithOffset(1, writer.Close()).To(Succeed())
	return buffer.Bytes()
}

func transportGzip(content []byte) []byte {
	var buffer bytes.Buffer
	writer := gzip.NewWriter(&buffer)
	_, err := writer.Write(content)
	ExpectWithOffset(1, err).NotTo(HaveOccurred())
	ExpectWithOffset(1, writer.Close()).To(Succeed())
	return buffer.Bytes()
}

func transportManifest(blob []byte, media types.MediaType, layers int) []byte {
	hash, err := v1.NewHash(sha256Digest(blob))
	ExpectWithOffset(1, err).NotTo(HaveOccurred())
	configHash, err := v1.NewHash(sha256Digest([]byte("{}")))
	ExpectWithOffset(1, err).NotTo(HaveOccurred())
	manifest := v1.Manifest{SchemaVersion: 2, MediaType: types.OCIManifestSchema1,
		Config: v1.Descriptor{MediaType: types.OCIConfigJSON, Size: 2, Digest: configHash}}
	for index := 0; index < layers; index++ {
		manifest.Layers = append(manifest.Layers, v1.Descriptor{MediaType: media, Size: int64(len(blob)), Digest: hash})
	}
	content, err := json.Marshal(manifest)
	ExpectWithOffset(1, err).NotTo(HaveOccurred())
	return content
}

func transportRegistry(manifest, blob []byte, password *atomic.Value) *httptest.Server {
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if password != nil {
			username, provided, ok := request.BasicAuth()
			if !ok || username != "test" || provided != password.Load().(string) {
				writer.Header().Set("WWW-Authenticate", `Basic realm="firmware"`)
				writer.WriteHeader(http.StatusUnauthorized)
				return
			}
		}
		transportRegistryResponse(writer, request, manifest, blob)
	}))
	DeferCleanup(server.Close)
	return server
}

func transportRegistryResponse(writer http.ResponseWriter, request *http.Request, manifest, blob []byte) {
	switch {
	case request.URL.Path == "/v2/":
		writer.WriteHeader(http.StatusOK)
	case strings.Contains(request.URL.Path, "/manifests/"):
		writer.Header().Set("Content-Type", string(types.OCIManifestSchema1))
		_, _ = writer.Write(manifest)
	case strings.Contains(request.URL.Path, "/blobs/"):
		writer.Header().Set("Content-Type", "application/octet-stream")
		_, _ = writer.Write(blob)
	default:
		http.NotFound(writer, request)
	}
}

func transportReference(server *httptest.Server, manifest []byte) string {
	return strings.TrimPrefix(server.URL, "https://") + "/test/firmware@" + sha256Digest(manifest)
}

func transportForServer(server *httptest.Server) *Transport {
	reader, err := NewTransport(TransportOptions{AuthDir: ""})
	ExpectWithOffset(1, err).NotTo(HaveOccurred())
	reader.roundTripper = func() (http.RoundTripper, error) { return server.Client().Transport, nil }
	return reader
}

var _ = Describe("Firmware image transport", func() {
	var payload []transportArchiveEntry
	BeforeEach(func() {
		payload = []transportArchiveEntry{
			{name: "firmware/", contents: "", link: "", kind: tar.TypeDir, format: tar.FormatUnknown, pax: nil},
			{name: "firmware/firmware.bin", contents: "test firmware", link: "", kind: tar.TypeReg, format: tar.FormatUnknown, pax: nil},
		}
	})

	DescribeTable("validates complete registry content before exposing a payload", func(scenario string) {
		blob := transportGzip(transportArchive(payload))
		served := blob
		media := types.OCILayer
		layers := 1
		valid := false
		limits := defaultTransportLimits()
		switch scenario {
		case "gzip":
			valid = true
		case "plain":
			blob, media, valid = transportArchive(payload), types.OCIUncompressedLayer, true
		case "dot prefix":
			payload[1].name = "./firmware/firmware.bin"
			blob, valid = transportGzip(transportArchive(payload)), true
		case "manifest digest":
		case "corrupt tail":
			served = append([]byte(nil), blob...)
			served[len(served)-1] ^= 1
		case "truncated response":
			served = blob[:len(blob)-5]
		case "truncated gzip":
			blob = blob[:len(blob)-4]
		case "compressed descriptor":
			limits.compressed = 16
		case "actual compressed overflow":
			limits.compressed = int64(len(blob) + 8)
			served = append(append([]byte(nil), blob...), make([]byte, 20)...)
		case "expanded limit":
			limits.expanded = 1024
		case "payload limit":
			limits.payload = 4
		case "symlink", "hardlink":
			payload[1].kind, payload[1].link = tar.TypeSymlink, "/etc/passwd"
			if scenario == "hardlink" {
				payload[1].kind = tar.TypeLink
			}
			blob = transportGzip(transportArchive(payload))
		case "duplicate", "normalized duplicate":
			duplicate := payload[1]
			if scenario == "normalized duplicate" {
				duplicate.name = "./firmware/firmware.bin"
			}
			blob = transportGzip(transportArchive(append(payload, duplicate)))
		case "traversal", "absolute", "backslash":
			paths := map[string]string{"traversal": "firmware/../firmware/firmware.bin", "absolute": "/firmware/firmware.bin", "backslash": `firmware\firmware.bin`}
			payload[1].name = paths[scenario]
			blob = transportGzip(transportArchive(payload))
		case "extra file":
			blob = transportGzip(transportArchive(append(payload, transportArchiveEntry{
				name: "extra", contents: "x", link: "", kind: tar.TypeReg, format: tar.FormatUnknown, pax: nil})))
		case "missing":
			blob = transportGzip(transportArchive(payload[:1]))
		case "empty":
			payload[1].contents = ""
			blob = transportGzip(transportArchive(payload))
		case "zero layers":
			layers = 0
		case "two layers":
			layers = 2
		case "zstd":
			media = types.OCILayerZStd
		case "logical entries":
			limits.entries = 1
		case "nonzero trailer":
			blob = transportGzip(append(transportArchive(payload), []byte("invalid trailer")...))
		case "PAX metadata":
			payload[1].format, payload[1].pax = tar.FormatPAX, map[string]string{"comment": "unsupported"}
			blob = transportGzip(transportArchive(payload))
		case "GNU format":
			payload[1].format = tar.FormatGNU
			blob = transportGzip(transportArchive(payload))
		}
		if scenario != "corrupt tail" && scenario != "truncated response" && scenario != "actual compressed overflow" {
			served = blob
		}
		manifest := transportManifest(blob, media, layers)
		server := transportRegistry(manifest, served, nil)
		reference := transportReference(server, manifest)
		if scenario == "manifest digest" {
			reference = strings.Split(reference, "@")[0] + "@sha256:" + strings.Repeat("0", 64)
		}
		reader := transportForServer(server)
		reader.limits = limits
		directory := GinkgoT().TempDir()
		err := reader.Fetch(context.Background(), reference, directory)
		if valid {
			Expect(err).NotTo(HaveOccurred())
			content, readErr := os.ReadFile(filepath.Join(directory, PayloadName))
			Expect(readErr).NotTo(HaveOccurred())
			Expect(string(content)).To(Equal("test firmware"))
			info, statErr := os.Lstat(filepath.Join(directory, PayloadName))
			Expect(statErr).NotTo(HaveOccurred())
			Expect(info.Mode().IsRegular()).To(BeTrue())
			Expect(info.Mode().Perm()).To(Equal(os.FileMode(0444)))
		} else {
			Expect(err).To(HaveOccurred())
			files, readErr := os.ReadDir(directory)
			Expect(readErr).NotTo(HaveOccurred())
			Expect(files).To(BeEmpty())
		}
	},
		Entry("gzip", "gzip"), Entry("plain", "plain"), Entry("dot prefix", "dot prefix"),
		Entry("requested manifest digest", "manifest digest"), Entry("corrupted complete blob", "corrupt tail"),
		Entry("truncated HTTP response", "truncated response"), Entry("truncated gzip", "truncated gzip"),
		Entry("compressed descriptor limit", "compressed descriptor"), Entry("actual compressed limit", "actual compressed overflow"),
		Entry("expanded limit", "expanded limit"), Entry("payload limit", "payload limit"),
		Entry("symlink", "symlink"), Entry("hardlink", "hardlink"), Entry("duplicate", "duplicate"),
		Entry("normalized duplicate", "normalized duplicate"), Entry("traversal", "traversal"), Entry("absolute", "absolute"),
		Entry("backslash", "backslash"), Entry("extra file", "extra file"), Entry("missing", "missing"), Entry("empty", "empty"),
		Entry("zero layers", "zero layers"), Entry("two layers", "two layers"), Entry("unsupported compression", "zstd"),
		Entry("logical entry limit", "logical entries"), Entry("nonzero trailer", "nonzero trailer"),
		Entry("PAX metadata", "PAX metadata"), Entry("GNU format", "GNU format"))

	It("bounds manifest responses before allocation", func() {
		blob := transportGzip(transportArchive(payload))
		manifest := append(transportManifest(blob, types.OCILayer, 1), bytes.Repeat([]byte(" "), 4096)...)
		server := transportRegistry(manifest, blob, nil)
		reader := transportForServer(server)
		reader.limits.metadata = 1024
		Expect(reader.Fetch(context.Background(), transportReference(server, manifest), GinkgoT().TempDir())).To(HaveOccurred())
	})

	DescribeTable("rejects unsupported manifest/config descriptors", func(change func(*v1.Manifest)) {
		blob := transportGzip(transportArchive(payload))
		var manifest v1.Manifest
		Expect(json.Unmarshal(transportManifest(blob, types.OCILayer, 1), &manifest)).To(Succeed())
		change(&manifest)
		raw, err := json.Marshal(manifest)
		Expect(err).NotTo(HaveOccurred())
		server := transportRegistry(raw, blob, nil)
		Expect(transportForServer(server).Fetch(context.Background(), transportReference(server, raw), GinkgoT().TempDir())).To(HaveOccurred())
	},
		Entry("index", func(m *v1.Manifest) { m.MediaType = types.OCIImageIndex }),
		Entry("invalid schema", func(m *v1.Manifest) { m.SchemaVersion = 1 }),
		Entry("unsupported config type", func(m *v1.Manifest) { m.Config.MediaType = types.OCILayer }),
		Entry("empty config", func(m *v1.Manifest) { m.Config.Size = 0 }),
		Entry("oversized config", func(m *v1.Manifest) { m.Config.Size = MaxMetadataBytes + 1 }),
		Entry("external config", func(m *v1.Manifest) { m.Config.URLs = []string{"https://elsewhere.invalid"} }),
		Entry("external layer", func(m *v1.Manifest) { m.Layers[0].URLs = []string{"https://elsewhere.invalid"} }),
		Entry("embedded layer", func(m *v1.Manifest) { m.Layers[0].Data = []byte("unsupported") }))

	It("rejects HTTP downgrade redirects without contacting the HTTP endpoint", func() {
		var contacted atomic.Bool
		plain := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { contacted.Store(true) }))
		DeferCleanup(plain.Close)
		blob := transportGzip(transportArchive(payload))
		manifest := transportManifest(blob, types.OCILayer, 1)
		server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			if strings.Contains(request.URL.Path, "/manifests/") {
				http.Redirect(writer, request, plain.URL+"/manifest", http.StatusFound)
				return
			}
			transportRegistryResponse(writer, request, manifest, blob)
		}))
		DeferCleanup(server.Close)
		Expect(transportForServer(server).Fetch(context.Background(), transportReference(server, manifest), GinkgoT().TempDir())).To(HaveOccurred())
		Expect(contacted.Load()).To(BeFalse())
	})

	It("preserves bounded blob reads across HTTPS redirects and strips registry credentials", func() {
		blob := transportArchive([]transportArchiveEntry{{name: "firmware/firmware.bin", contents: strings.Repeat("x", 100<<10),
			link: "", kind: tar.TypeReg, format: tar.FormatUnknown, pax: nil}})
		manifest := transportManifest(blob, types.OCIUncompressedLayer, 1)
		var authorization atomic.Value
		authorization.Store("not contacted")
		cdn := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			authorization.Store(request.Header.Get("Authorization"))
			_, _ = writer.Write(blob)
		}))
		DeferCleanup(cdn.Close)
		server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			username, password, _ := request.BasicAuth()
			if username != "test" || password != "registry-password" {
				writer.Header().Set("WWW-Authenticate", `Basic realm="firmware"`)
				writer.WriteHeader(http.StatusUnauthorized)
				return
			}
			if strings.Contains(request.URL.Path, "/blobs/") {
				http.Redirect(writer, request, cdn.URL+"/download", http.StatusFound)
				return
			}
			transportRegistryResponse(writer, request, manifest, blob)
		}))
		DeferCleanup(server.Close)
		reader := transportForServer(server)
		reader.limits.metadata = 1024
		reader.authDir = transportAuthDirectory(strings.TrimPrefix(server.URL, "https://"), "registry-password")
		Expect(reader.Fetch(context.Background(), transportReference(server, manifest), GinkgoT().TempDir())).To(Succeed())
		Expect(authorization.Load()).To(Equal(""))
	})

	DescribeTable("rejects cross-origin OAuth token body redirects", func(status int) {
		certificate, _ := transportCertificate()
		var contacted atomic.Bool
		target := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			contacted.Store(true)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"token":"test-token"}`))
		}))
		target.TLS = &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12}
		target.StartTLS()
		DeferCleanup(target.Close)
		var registry *httptest.Server
		registry = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/token" {
				// Use a DNS name so the dependency's IP-literal auth guard does
				// not mask the cross-origin redirect behavior under test.
				destination := strings.Replace(target.URL, "127.0.0.1", "localhost", 1)
				http.Redirect(w, r, destination+"/token", status)
				return
			}
			w.Header().Set("WWW-Authenticate", `Bearer realm="`+registry.URL+`/token",service="test"`)
			w.WriteHeader(http.StatusUnauthorized)
		}))
		registry.TLS = &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12}
		registry.StartTLS()
		DeferCleanup(registry.Close)
		reader := transportForServer(registry)
		reader.authDir = GinkgoT().TempDir()
		secretDir := filepath.Join(reader.authDir, "firmware-registry")
		Expect(os.Mkdir(secretDir, 0700)).To(Succeed())
		host := strings.TrimPrefix(registry.URL, "https://")
		credentials := `{"auths":{"` + host + `":{"identitytoken":"TEST_ONLY_IDENTITY_SECRET"}}}`
		Expect(os.WriteFile(filepath.Join(secretDir, ".dockerconfigjson"), []byte(credentials), 0600)).To(Succeed())
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		err := reader.Fetch(ctx, host+"/firmware@sha256:"+strings.Repeat("a", 64), GinkgoT().TempDir())
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).NotTo(ContainSubstring("TEST_ONLY_IDENTITY_SECRET"))
		Expect(contacted.Load()).To(BeFalse(), "OAuth bodies must never reach another authority")
	}, Entry("307", http.StatusTemporaryRedirect), Entry("308", http.StatusPermanentRedirect))

	It("stops local parsing after cancellation and enforces the exact expanded limit", func() {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		err := extractPayload(ctx, bytes.NewReader(transportArchive(payload)), io.Discard, defaultTransportLimits())
		Expect(err).To(MatchError(ContainSubstring("context canceled")))
		for _, input := range []string{"1234", "12345"} {
			_, err := io.ReadAll(&expandedReader{reader: strings.NewReader(input), remaining: 4})
			Expect(err == nil).To(Equal(len(input) == 4))
		}
	})

	It("validates public input pointers and private staging ownership", func() {
		_, err := NewTransport(TransportOptions{AuthDir: "relative"})
		Expect(err).To(HaveOccurred())
		reader, err := NewTransport(TransportOptions{AuthDir: ""})
		Expect(err).NotTo(HaveOccurred())
		var nilContext context.Context
		Expect(reader.Fetch(nilContext, "invalid", "missing")).To(HaveOccurred())
		Expect((*Transport)(nil).Fetch(context.Background(), "invalid", "missing")).To(HaveOccurred())
		Expect(reader.Fetch(context.Background(), "invalid", "missing")).To(HaveOccurred())
		ref := "registry.example/firmware@sha256:" + strings.Repeat("1", 64)
		Expect(reader.Fetch(context.Background(), ref, "missing")).To(HaveOccurred())
	})
})

func transportCertificate() (tls.Certificate, []byte) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	ExpectWithOffset(1, err).NotTo(HaveOccurred())
	template := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: "firmware-test"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames: []string{"localhost"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	ExpectWithOffset(1, err).NotTo(HaveOccurred())
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

var _ = Describe("Firmware registry CA rotation", func() {
	DescribeTable("rereads configured trust on every Fetch", func(trustSource string) {
		payload := []transportArchiveEntry{{name: "firmware/firmware.bin", contents: "firmware", link: "", kind: tar.TypeReg, format: tar.FormatUnknown, pax: nil}}
		blob := transportGzip(transportArchive(payload))
		manifest := transportManifest(blob, types.OCILayer, 1)
		start := func(certificate tls.Certificate) *httptest.Server {
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				transportRegistryResponse(w, r, manifest, blob)
			}))
			server.Config.ErrorLog = log.New(io.Discard, "", 0)
			server.TLS = &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12}
			server.StartTLS()
			DeferCleanup(server.Close)
			return server
		}
		first, firstPEM := transportCertificate()
		second, secondPEM := transportCertificate()
		firstServer, secondServer := start(first), start(second)
		trustDir := GinkgoT().TempDir()
		bundle := filepath.Join(trustDir, "bundle.pem")
		GinkgoT().Setenv("SSL_CERT_FILE", bundle)
		GinkgoT().Setenv("SSL_CERT_DIR", "")
		GinkgoT().Setenv("FIRMWARE_IMAGE_CA_FILE", "")
		if trustSource == "SSL_CERT_DIR" {
			GinkgoT().Setenv("SSL_CERT_FILE", "")
			GinkgoT().Setenv("SSL_CERT_DIR", trustDir)
		}
		if trustSource == "FIRMWARE_IMAGE_CA_FILE" {
			GinkgoT().Setenv("SSL_CERT_FILE", "")
			GinkgoT().Setenv("FIRMWARE_IMAGE_CA_FILE", bundle)
		}
		Expect(os.WriteFile(bundle, firstPEM, 0600)).To(Succeed())
		if trustSource == "FIRMWARE_IMAGE_CA_FILE" {
			base, loadErr := registryBaseCertPool()
			Expect(loadErr).NotTo(HaveOccurred())
			expected := base.Clone()
			Expect(expected.AppendCertsFromPEM(firstPEM)).To(BeTrue())
			actual, loadErr := registryCertPool()
			Expect(loadErr).NotTo(HaveOccurred())
			Expect(actual.Equal(expected)).To(BeTrue(), "additional trust must retain all default system roots")
		}
		reader, err := NewTransport(TransportOptions{AuthDir: ""})
		Expect(err).NotTo(HaveOccurred())
		Expect(reader.Fetch(context.Background(), transportReference(firstServer, manifest), GinkgoT().TempDir())).To(Succeed())
		Expect(os.WriteFile(bundle, secondPEM, 0600)).To(Succeed())
		Expect(reader.Fetch(context.Background(), transportReference(secondServer, manifest), GinkgoT().TempDir())).To(Succeed())
		Expect(reader.Fetch(context.Background(), transportReference(firstServer, manifest), GinkgoT().TempDir())).To(HaveOccurred())
	}, Entry("SSL_CERT_FILE", "SSL_CERT_FILE"), Entry("SSL_CERT_DIR", "SSL_CERT_DIR"),
		Entry("additional FIRMWARE_IMAGE_CA_FILE", "FIRMWARE_IMAGE_CA_FILE"))
})

func FuzzStrictFirmwareArchive(f *testing.F) {
	f.Add([]byte("malformed"))
	f.Add(make([]byte, 1024))
	f.Fuzz(func(t *testing.T, input []byte) {
		limits := transportLimits{metadata: 4096, compressed: 4096, expanded: 4096, payload: 1024, entries: 4}
		_ = extractPayload(context.Background(), bytes.NewReader(input), io.Discard, limits)
	})
}
