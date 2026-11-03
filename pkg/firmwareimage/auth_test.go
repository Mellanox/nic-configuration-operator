// Copyright 2026 NVIDIA CORPORATION & AFFILIATES
// SPDX-License-Identifier: Apache-2.0

package firmwareimage

import (
	"archive/tar"
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"

	"github.com/google/go-containerregistry/pkg/name"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func transportAuthDirectory(registry, password string) string {
	root := GinkgoT().TempDir()
	directory := filepath.Join(root, "firmware-secret")
	ExpectWithOffset(1, os.Mkdir(directory, 0700)).To(Succeed())
	writeTransportAuth(directory, registry, password)
	return root
}

func writeTransportAuth(directory, registry, password string) {
	content, err := json.Marshal(map[string]any{"auths": map[string]any{
		registry: map[string]string{"auth": base64.StdEncoding.EncodeToString([]byte("test:" + password))}}})
	ExpectWithOffset(1, err).NotTo(HaveOccurred())
	ExpectWithOffset(1, os.WriteFile(filepath.Join(directory, ".dockerconfigjson"), content, 0600)).To(Succeed())
}

var _ = Describe("Explicit firmware registry credentials", func() {
	It("rotates mounted credentials per attempt and redacts authentication errors", func() {
		blob := transportGzip(transportArchive([]transportArchiveEntry{{name: "firmware/firmware.bin", contents: "firmware",
			link: "", kind: tar.TypeReg, format: tar.FormatUnknown, pax: nil}}))
		manifest := transportManifest(blob, "application/vnd.oci.image.layer.v1.tar+gzip", 1)
		var expected atomic.Value
		expected.Store("first-password")
		server := transportRegistry(manifest, blob, &expected)
		reader := transportForServer(server)
		registry := strings.TrimPrefix(server.URL, "https://")
		reader.authDir = transportAuthDirectory(registry, "first-password")
		ref := transportReference(server, manifest)
		Expect(reader.Fetch(context.Background(), ref, GinkgoT().TempDir())).To(Succeed())
		expected.Store("second-password")
		err := reader.Fetch(context.Background(), ref, GinkgoT().TempDir())
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).NotTo(ContainSubstring("first-password"))
		Expect(err.Error()).NotTo(ContainSubstring("second-password"))
		writeTransportAuth(filepath.Join(reader.authDir, "firmware-secret"), registry, "second-password")
		Expect(reader.Fetch(context.Background(), ref, GinkgoT().TempDir())).To(Succeed())
	})

	It("supports standard Secret keys and repository-prefix credential precedence", func() {
		root := GinkgoT().TempDir()
		Expect(os.Mkdir(filepath.Join(root, "general"), 0700)).To(Succeed())
		Expect(os.Mkdir(filepath.Join(root, "scoped"), 0700)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(root, "general", ".dockercfg"),
			[]byte(`{"https://registry.example/v1/":{"username":"general","password":"general-password"}}`), 0600)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(root, "scoped", ".dockerconfigjson"),
			[]byte(`{"auths":{"registry.example/team":{"username":"scoped","password":"scoped-password"}}}`), 0600)).To(Succeed())
		keychain, err := loadRegistryAuth(root)
		Expect(err).NotTo(HaveOccurred())
		for repository, username := range map[string]string{
			"registry.example/team/firmware": "scoped", "registry.example/team": "scoped",
			"registry.example/teammate/firmware": "general", "registry.example/other": "general",
		} {
			ref, parseErr := name.NewRepository(repository, name.StrictValidation)
			Expect(parseErr).NotTo(HaveOccurred())
			authenticator, resolveErr := keychain.Resolve(ref)
			Expect(resolveErr).NotTo(HaveOccurred())
			config, authErr := authenticator.Authorization()
			Expect(authErr).NotTo(HaveOccurred())
			Expect(config.Username).To(Equal(username))
		}
	})

	DescribeTable("rejects unsupported or inconsistent credentials without exposing their values", func(content string) {
		_, err := parseRegistryAuth([]byte(content), false)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).NotTo(ContainSubstring("secret-value"))
	},
		Entry("helper store", `{"credsStore":"secret-value","auths":{"registry.example":{}}}`),
		Entry("per-registry helper", `{"credHelpers":{"registry.example":"secret-value"}}`),
		Entry("invalid JSON", `{"auths":"secret-value"}`),
		Entry("duplicate JSON fields", `{"auths":{"registry.example":{"password":"one","password":"secret-value"}}}`),
		Entry("invalid encoded auth", `{"auths":{"registry.example":{"auth":"secret-value"}}}`),
		Entry("inconsistent auth", `{"auths":{"registry.example":{"auth":"dGVzdDpwYXNz","password":"secret-value"}}}`),
		Entry("wildcard host", `{"auths":{"*.example":{"password":"secret-value"}}}`),
		Entry("wildcard path", `{"auths":{"registry.example/team/*":{"password":"secret-value"}}}`),
		Entry("HTTP credentials", `{"auths":{"http://registry.example":{"password":"secret-value"}}}`),
		Entry("URL credentials", `{"auths":{"https://user:secret-value@registry.example":{"password":"secret-value"}}}`),
		Entry("path traversal", `{"auths":{"registry.example/team/../other":{"password":"secret-value"}}}`),
		Entry("empty auth map", `{"auths":{}}`),
		Entry("normalized collision", `{"auths":{"registry.example":{"password":"secret-value"},"https://registry.example/v1/":{}}}`))

	It("fails configured missing/empty Secrets instead of silently falling back to anonymous", func() {
		keychain, err := loadRegistryAuth("")
		Expect(err).NotTo(HaveOccurred())
		Expect(keychain).NotTo(BeNil())
		_, err = loadRegistryAuth(filepath.Join(GinkgoT().TempDir(), "missing"))
		Expect(err).To(HaveOccurred())
		root := GinkgoT().TempDir()
		_, err = loadRegistryAuth(root)
		Expect(err).To(HaveOccurred())
		Expect(os.Mkdir(filepath.Join(root, "missing-secret"), 0700)).To(Succeed())
		_, err = loadRegistryAuth(root)
		Expect(err).To(HaveOccurred())
	})

	It("rereads kubelet-style projected symlink updates", func() {
		root := GinkgoT().TempDir()
		secret := filepath.Join(root, "secret")
		Expect(os.Mkdir(secret, 0700)).To(Succeed())
		Expect(os.Mkdir(filepath.Join(secret, "..first"), 0700)).To(Succeed())
		Expect(os.Mkdir(filepath.Join(secret, "..second"), 0700)).To(Succeed())
		writeTransportAuth(filepath.Join(secret, "..first"), "registry.example", "first-password")
		writeTransportAuth(filepath.Join(secret, "..second"), "registry.example", "second-password")
		Expect(os.Symlink("..first", filepath.Join(secret, "..data"))).To(Succeed())
		Expect(os.Symlink("..data/.dockerconfigjson", filepath.Join(secret, ".dockerconfigjson"))).To(Succeed())
		keychain, err := loadRegistryAuth(root)
		Expect(err).NotTo(HaveOccurred())
		ref, err := name.NewRepository("registry.example/firmware", name.StrictValidation)
		Expect(err).NotTo(HaveOccurred())
		config, err := keychain.Resolve(ref)
		Expect(err).NotTo(HaveOccurred())
		auth, err := config.Authorization()
		Expect(err).NotTo(HaveOccurred())
		Expect(auth.Password).To(Equal("first-password"))
		Expect(os.Remove(filepath.Join(secret, "..data"))).To(Succeed())
		Expect(os.Symlink("..second", filepath.Join(secret, "..data"))).To(Succeed())
		keychain, err = loadRegistryAuth(root)
		Expect(err).NotTo(HaveOccurred())
		config, err = keychain.Resolve(ref)
		Expect(err).NotTo(HaveOccurred())
		auth, err = config.Authorization()
		Expect(err).NotTo(HaveOccurred())
		Expect(auth.Password).To(Equal("second-password"))
	})
})
