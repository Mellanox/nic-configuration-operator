/*
Copyright 2026 NVIDIA CORPORATION & AFFILIATES
Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package spectrumx

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type blueprintsArchiveEntry struct {
	name     string
	typeFlag byte
	mode     int64
	content  string
}

type zeroReader struct{}

func (zeroReader) Read(data []byte) (int, error) {
	clear(data)
	return len(data), nil
}

func blueprintsArchive(entries ...blueprintsArchiveEntry) []byte {
	var output bytes.Buffer
	gzipWriter := gzip.NewWriter(&output)
	tarWriter := tar.NewWriter(gzipWriter)
	for _, entry := range entries {
		header := &tar.Header{
			Name:     entry.name,
			Typeflag: entry.typeFlag,
			Mode:     entry.mode,
			Size:     int64(len(entry.content)),
		}
		Expect(tarWriter.WriteHeader(header)).To(Succeed())
		if entry.content != "" {
			_, err := tarWriter.Write([]byte(entry.content))
			Expect(err).NotTo(HaveOccurred())
		}
	}
	Expect(tarWriter.Close()).To(Succeed())
	Expect(gzipWriter.Close()).To(Succeed())
	return output.Bytes()
}

func validBlueprintsArchive(extra ...blueprintsArchiveEntry) []byte {
	entries := make([]blueprintsArchiveEntry, 0, 10+len(extra))
	entries = append(entries, []blueprintsArchiveEntry{
		{name: "data/", typeFlag: tar.TypeDir, mode: 0o755},
		{name: "data/features/", typeFlag: tar.TypeDir, mode: 0o755},
		{name: "data/profiles/", typeFlag: tar.TypeDir, mode: 0o755},
		{name: "data/templates/", typeFlag: tar.TypeDir, mode: 0o755},
		{name: "data/execution-groups.yaml", typeFlag: tar.TypeReg, mode: 0o644, content: "groups: {}\n"},
		{name: "data/formulas.yaml", typeFlag: tar.TypeReg, mode: 0o644, content: "formulas: {}\n"},
		{name: "data/hca-types.yaml", typeFlag: tar.TypeReg, mode: 0o644, content: "hca_types: {}\n"},
		{name: "data/phase-config.yaml", typeFlag: tar.TypeReg, mode: 0o644, content: "phases: {}\n"},
		{name: "data/platform-types.yaml", typeFlag: tar.TypeReg, mode: 0o644, content: "platform_types: {}\n"},
		{name: "data/temp.sh", typeFlag: tar.TypeReg, mode: 0o755, content: "#!/bin/sh\n"},
	}...)
	return blueprintsArchive(append(entries, extra...)...)
}

func newBlueprintsDataManager(dataRoot string) *spectrumXConfigManager {
	return &spectrumXConfigManager{
		spectrumXConfigs:   nil,
		dmsManager:         nil,
		execInterface:      nil,
		blueprintsRoot:     "",
		blueprintsStateDir: "",
		dospcxDataRoot:     dataRoot,
		dospcxDataDigest:   "",
		ccProcesses:        nil,
		ccTerminationChan:  nil,
	}
}

var _ = Describe("doSPCX data archive installation", func() {
	It("restores the data tree and replaces obsolete files", func() {
		dataRoot := filepath.Join(GinkgoT().TempDir(), "doSpcx", "data")
		Expect(os.MkdirAll(dataRoot, 0o755)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(dataRoot, "obsolete"), []byte("old"), 0o644)).To(Succeed())
		archive := validBlueprintsArchive()
		manager := newBlueprintsDataManager(dataRoot)

		Expect(manager.InstallBlueprintsData(archive)).To(Succeed())

		content, err := os.ReadFile(filepath.Join(dataRoot, "execution-groups.yaml"))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(content)).To(Equal("groups: {}\n"))
		_, err = os.Stat(filepath.Join(dataRoot, "obsolete"))
		Expect(os.IsNotExist(err)).To(BeTrue())
		info, err := os.Stat(filepath.Join(dataRoot, "temp.sh"))
		Expect(err).NotTo(HaveOccurred())
		Expect(info.Mode().Perm()).To(Equal(os.FileMode(0o755)))
		Expect(manager.dospcxDataDigest).To(Equal(sha256Digest(archive)))
	})

	It("keeps the active tree when the archive is malformed", func() {
		dataRoot := filepath.Join(GinkgoT().TempDir(), "doSpcx", "data")
		Expect(os.MkdirAll(dataRoot, 0o755)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(dataRoot, "active"), []byte("keep"), 0o644)).To(Succeed())
		manager := newBlueprintsDataManager(dataRoot)

		Expect(manager.InstallBlueprintsData([]byte("not gzip"))).NotTo(Succeed())

		content, err := os.ReadFile(filepath.Join(dataRoot, "active"))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(content)).To(Equal("keep"))
		Expect(manager.dospcxDataDigest).To(BeEmpty())
	})

	It("rejects an archive with an invalid gzip checksum", func() {
		dataRoot := filepath.Join(GinkgoT().TempDir(), "doSpcx", "data")
		archive := validBlueprintsArchive()
		archive[len(archive)-1] ^= 0xff
		manager := newBlueprintsDataManager(dataRoot)

		Expect(manager.InstallBlueprintsData(archive)).NotTo(Succeed())
		Expect(dataRoot).NotTo(BeADirectory())
		Expect(manager.dospcxDataDigest).To(BeEmpty())
	})

	It("bounds decompression after the tar end marker", func() {
		archive := validBlueprintsArchive()
		var trailing bytes.Buffer
		gzipWriter := gzip.NewWriter(&trailing)
		_, err := io.CopyN(gzipWriter, zeroReader{}, maxBlueprintsDecompressedBytes+1)
		Expect(err).NotTo(HaveOccurred())
		Expect(gzipWriter.Close()).To(Succeed())
		archive = append(archive, trailing.Bytes()...)
		Expect(len(archive)).To(BeNumerically("<", maxBlueprintsArchiveBytes))
		manager := newBlueprintsDataManager(filepath.Join(GinkgoT().TempDir(), "data"))

		Expect(manager.InstallBlueprintsData(archive)).To(
			MatchError(ContainSubstring("decompressed size limit")))
		Expect(manager.dospcxDataDigest).To(BeEmpty())
	})

	It("rejects paths outside the data directory without changing the active tree", func() {
		dataRoot := filepath.Join(GinkgoT().TempDir(), "doSpcx", "data")
		Expect(os.MkdirAll(dataRoot, 0o755)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(dataRoot, "active"), []byte("keep"), 0o644)).To(Succeed())
		archive := validBlueprintsArchive(blueprintsArchiveEntry{
			name: "../outside", typeFlag: tar.TypeReg, mode: 0o644, content: "unsafe",
		})
		manager := newBlueprintsDataManager(dataRoot)

		err := manager.InstallBlueprintsData(archive)
		Expect(err).To(MatchError(ContainSubstring("unsafe path")))
		Expect(filepath.Join(filepath.Dir(dataRoot), "outside")).NotTo(BeAnExistingFile())
		Expect(filepath.Join(dataRoot, "active")).To(BeAnExistingFile())
	})

	It("rejects links and other non-file archive entries", func() {
		dataRoot := filepath.Join(GinkgoT().TempDir(), "doSpcx", "data")
		archive := validBlueprintsArchive(blueprintsArchiveEntry{
			name: "data/link", typeFlag: tar.TypeSymlink, mode: 0o777,
		})
		manager := newBlueprintsDataManager(dataRoot)

		Expect(manager.InstallBlueprintsData(archive)).To(MatchError(ContainSubstring("unsupported tar entry type")))
		Expect(dataRoot).NotTo(BeADirectory())
	})

	It("rejects archives with an incomplete data layout", func() {
		dataRoot := filepath.Join(GinkgoT().TempDir(), "doSpcx", "data")
		archive := blueprintsArchive(
			blueprintsArchiveEntry{name: "data/", typeFlag: tar.TypeDir, mode: 0o755},
			blueprintsArchiveEntry{name: "data/profiles/", typeFlag: tar.TypeDir, mode: 0o755},
		)
		manager := newBlueprintsDataManager(dataRoot)

		Expect(manager.InstallBlueprintsData(archive)).To(MatchError(ContainSubstring("missing required path")))
		Expect(dataRoot).NotTo(BeADirectory())
	})

	It("rejects archives larger than the compressed size limit", func() {
		manager := newBlueprintsDataManager(filepath.Join(GinkgoT().TempDir(), "data"))

		Expect(manager.InstallBlueprintsData(make([]byte, maxBlueprintsArchiveBytes+1))).To(
			MatchError(ContainSubstring("compressed size limit")))
	})

	It("requires an absolute installation path", func() {
		manager := newBlueprintsDataManager("relative/data")

		Expect(manager.InstallBlueprintsData(validBlueprintsArchive())).To(
			MatchError(ContainSubstring("non-empty absolute path")))
	})

	It("keeps the active data and digest consistent when backup cleanup fails", func() {
		dataRoot := filepath.Join(GinkgoT().TempDir(), "doSpcx", "data")
		manager := newBlueprintsDataManager(dataRoot)
		Expect(manager.InstallBlueprintsData(validBlueprintsArchive())).To(Succeed())
		updatedArchive := validBlueprintsArchive(blueprintsArchiveEntry{
			name: "data/new-profile-marker", typeFlag: tar.TypeReg, mode: 0o644, content: "new",
		})

		originalRemove := removeBlueprintsDataBackup
		DeferCleanup(func() { removeBlueprintsDataBackup = originalRemove })
		cleanupAttempted := false
		removeBlueprintsDataBackup = func(string) error {
			cleanupAttempted = true
			return errors.New("injected cleanup failure")
		}

		Expect(manager.InstallBlueprintsData(updatedArchive)).To(Succeed())
		Expect(cleanupAttempted).To(BeTrue())
		Expect(filepath.Join(dataRoot, "new-profile-marker")).To(BeAnExistingFile())
		Expect(manager.dospcxDataDigest).To(Equal(sha256Digest(updatedArchive)))
	})

	It("removes data installed from a ConfigMap and clears its digest", func() {
		dataRoot := filepath.Join(GinkgoT().TempDir(), "doSpcx", "data")
		manager := newBlueprintsDataManager(dataRoot)
		Expect(manager.InstallBlueprintsData(validBlueprintsArchive())).To(Succeed())

		Expect(manager.RemoveBlueprintsData()).To(Succeed())
		Expect(dataRoot).NotTo(BeADirectory())
		Expect(manager.dospcxDataDigest).To(BeEmpty())
	})

	It("keeps the active digest when removing installed data fails", func() {
		dataRoot := filepath.Join(GinkgoT().TempDir(), "doSpcx", "data")
		manager := newBlueprintsDataManager(dataRoot)
		archive := validBlueprintsArchive()
		Expect(manager.InstallBlueprintsData(archive)).To(Succeed())

		originalRemove := removeBlueprintsDataDirectory
		DeferCleanup(func() { removeBlueprintsDataDirectory = originalRemove })
		removeBlueprintsDataDirectory = func(string) error {
			return errors.New("injected removal failure")
		}

		Expect(manager.RemoveBlueprintsData()).To(MatchError(ContainSubstring("injected removal failure")))
		Expect(dataRoot).To(BeADirectory())
		Expect(manager.dospcxDataDigest).To(Equal(sha256Digest(archive)))
	})

	It("does not remove image-provided data when no ConfigMap bundle was installed", func() {
		dataRoot := filepath.Join(GinkgoT().TempDir(), "doSpcx", "data")
		Expect(os.MkdirAll(dataRoot, 0o755)).To(Succeed())
		imageData := filepath.Join(dataRoot, "image-data")
		Expect(os.WriteFile(imageData, []byte("keep"), 0o644)).To(Succeed())
		manager := newBlueprintsDataManager(dataRoot)

		Expect(manager.RemoveBlueprintsData()).To(Succeed())
		Expect(imageData).To(BeAnExistingFile())
		Expect(manager.dospcxDataDigest).To(BeEmpty())
	})
})
