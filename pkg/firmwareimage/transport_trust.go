// Copyright 2026 NVIDIA CORPORATION & AFFILIATES
// SPDX-License-Identifier: Apache-2.0

package firmwareimage

import (
	"crypto/x509"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

const maxTrustBytes int64 = 8 << 20

// These are the Unix/Linux certificate locations used by Go and the supported
// daemon images. Environment overrides retain ordinary file-plus-directory
// behavior, but are loaded fresh instead of using SystemCertPool's cached roots.
var unixCertificateFiles = []string{
	"/etc/ssl/certs/ca-certificates.crt", "/etc/pki/tls/certs/ca-bundle.crt", "/etc/ssl/ca-bundle.pem",
	"/etc/pki/tls/cacert.pem", "/etc/pki/ca-trust/extracted/pem/tls-ca-bundle.pem", "/etc/ssl/cert.pem",
}

var unixCertificateDirectories = []string{"/etc/ssl/certs", "/etc/pki/tls/certs"}

func registryCertPool() (*x509.CertPool, error) {
	pool, err := registryBaseCertPool()
	if err != nil {
		return nil, err
	}
	if file := os.Getenv("FIRMWARE_IMAGE_CA_FILE"); file != "" {
		content, err := readBoundedFile(file, maxTrustBytes)
		if err != nil || !pool.AppendCertsFromPEM(content) {
			return nil, errors.New("configured additional firmware registry CA bundle is unavailable or invalid")
		}
	}
	return pool, nil
}

func registryBaseCertPool() (*x509.CertPool, error) {
	file, directoryOverride := os.Getenv("SSL_CERT_FILE"), os.Getenv("SSL_CERT_DIR")
	if file == "" && directoryOverride == "" {
		pool, err := x509.SystemCertPool()
		if err != nil {
			return nil, errors.New("cannot load system registry CA certificates")
		}
		return pool, nil
	}
	pool := x509.NewCertPool()
	loaded := false
	if file != "" {
		content, err := readBoundedFile(file, maxTrustBytes)
		if err != nil || !pool.AppendCertsFromPEM(content) {
			return nil, errors.New("configured registry CA bundle is unavailable or invalid")
		}
		loaded = true
	} else {
		for _, candidate := range unixCertificateFiles {
			content, err := readBoundedFile(candidate, maxTrustBytes)
			if err == nil && pool.AppendCertsFromPEM(content) {
				loaded = true
				break
			}
		}
	}
	directories := unixCertificateDirectories
	if directoryOverride != "" {
		directories = filepath.SplitList(directoryOverride)
	}
	found, err := appendCertDirectories(pool, directories, directoryOverride != "")
	if err != nil {
		return nil, err
	}
	if !loaded && !found {
		return nil, errors.New("configured registry trust has no valid certificates")
	}
	return pool, nil
}

func appendCertDirectories(pool *x509.CertPool, directories []string, required bool) (bool, error) {
	loaded, count := false, 0
	var total int64
	for _, directory := range directories {
		entries, err := os.ReadDir(directory)
		if err != nil {
			if required {
				return false, errors.New("configured registry CA directory is unavailable")
			}
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
				continue
			}
			count++
			if count > 1024 || total >= maxTrustBytes {
				return false, errors.New("registry CA directories exceed their byte or entry limits")
			}
			content, err := readBoundedFile(filepath.Join(directory, entry.Name()), maxTrustBytes-total)
			if err != nil {
				if required {
					return false, errors.New("configured registry CA file is unavailable or exceeds its limit")
				}
				continue
			}
			total += int64(len(content))
			loaded = pool.AppendCertsFromPEM(content) || loaded
		}
	}
	return loaded, nil
}
