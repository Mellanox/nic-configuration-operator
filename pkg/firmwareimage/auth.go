// Copyright 2026 NVIDIA CORPORATION & AFFILIATES
// SPDX-License-Identifier: Apache-2.0

package firmwareimage

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/go-containerregistry/pkg/authn"
	strictjson "sigs.k8s.io/json"
)

const maxAuthBytes int64 = 1 << 20

type registryKeychain map[string]authn.AuthConfig

// Avoid AuthConfig.UnmarshalJSON, which silently replaces explicit credentials
// when an encoded auth field is also present.
type projectedAuthConfig authn.AuthConfig

func (k registryKeychain) Resolve(resource authn.Resource) (authn.Authenticator, error) {
	registry, err := registryHost(resource.RegistryStr())
	if err != nil {
		return nil, err
	}
	repository := registry + strings.TrimPrefix(resource.String(), resource.RegistryStr())
	selected := ""
	for identity := range k {
		if (repository == identity || strings.HasPrefix(repository, identity+"/")) && len(identity) > len(selected) {
			selected = identity
		}
	}
	if selected != "" {
		return authn.FromConfig(k[selected]), nil
	}
	return authn.Anonymous, nil
}

// loadRegistryAuth rereads projected files per attempt, including kubelet's
// symlink-based rotation. Only explicitly named volume directories are read;
// Docker helpers, home directories and ServiceAccount secrets are never used.
func loadRegistryAuth(root string) (authn.Keychain, error) {
	keychain := registryKeychain{}
	if root == "" {
		return keychain, nil
	}
	directories, err := os.ReadDir(root)
	if err != nil {
		return nil, errors.New("configured firmware registry auth directory is unavailable")
	}
	loaded := 0
	for _, directory := range directories {
		if strings.HasPrefix(directory.Name(), ".") {
			continue
		}
		if !directory.IsDir() {
			return nil, errors.New("firmware registry auth root must contain named Secret directories")
		}
		if loaded >= 32 {
			return nil, errors.New("too many firmware registry authentication Secrets")
		}
		if err := loadSecretDirectory(filepath.Join(root, directory.Name()), keychain); err != nil {
			return nil, fmt.Errorf("firmware registry Secret %s: %w", directory.Name(), err)
		}
		loaded++
	}
	if loaded == 0 {
		return nil, errors.New("configured firmware registry auth directory has no available Secrets")
	}
	return keychain, nil
}

func loadSecretDirectory(directory string, keychain registryKeychain) error {
	for _, file := range []string{".dockerconfigjson", ".dockercfg"} {
		content, err := readBoundedFile(filepath.Join(directory, file), maxAuthBytes)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return errors.New("cannot read bounded registry credentials")
		}
		entries, err := parseRegistryAuth(content, file == ".dockercfg")
		if err != nil {
			return err
		}
		for registry, config := range entries {
			if _, exists := keychain[registry]; exists {
				return errors.New("multiple credential entries target the same registry")
			}
			keychain[registry] = config
		}
		return nil
	}
	return errors.New("neither .dockerconfigjson nor .dockercfg is available")
}

func readBoundedFile(file string, maximum int64) ([]byte, error) {
	input, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer func() { _ = input.Close() }()
	info, err := input.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > maximum {
		return nil, errors.New("file is not a bounded regular file")
	}
	content, err := io.ReadAll(io.LimitReader(input, maximum+1))
	if err != nil || int64(len(content)) > maximum {
		return nil, errors.New("file exceeds its read limit")
	}
	return content, nil
}

func parseRegistryAuth(content []byte, legacy bool) (registryKeychain, error) {
	entries := map[string]projectedAuthConfig{}
	if legacy {
		strictErrors, err := strictjson.UnmarshalStrict(content, &entries, strictjson.DisallowDuplicateFields)
		if err != nil || len(strictErrors) != 0 {
			return nil, errors.New("invalid registry credential JSON")
		}
	} else {
		var config struct {
			Auths      map[string]projectedAuthConfig `json:"auths"`
			CredsStore string                         `json:"credsStore"`
			Helpers    map[string]string              `json:"credHelpers"`
		}
		strictErrors, err := strictjson.UnmarshalStrict(content, &config, strictjson.DisallowDuplicateFields)
		if err != nil || len(strictErrors) != 0 {
			return nil, errors.New("invalid registry credential JSON")
		}
		if config.CredsStore != "" || len(config.Helpers) != 0 {
			return nil, errors.New("firmware registry credential helpers are unsupported")
		}
		entries = config.Auths
	}
	result := registryKeychain{}
	if len(entries) == 0 {
		return nil, errors.New("configured registry Secret contains no authentication entries")
	}
	for registry, entry := range entries {
		config := authn.AuthConfig(entry)
		host, err := registryHost(registry)
		if err != nil {
			return nil, err
		}
		if config.Auth != "" {
			encoding := base64.RawStdEncoding
			if strings.HasSuffix(config.Auth, "=") {
				encoding = base64.StdEncoding
			}
			decoded, err := encoding.DecodeString(config.Auth)
			if err != nil {
				return nil, errors.New("invalid encoded registry credentials")
			}
			username, password, found := strings.Cut(string(decoded), ":")
			if !found {
				return nil, errors.New("invalid encoded registry credentials")
			}
			if (config.Username != "" && config.Username != username) || (config.Password != "" && config.Password != password) {
				return nil, errors.New("registry credentials contain inconsistent encoded and explicit username/password")
			}
			config.Username, config.Password, config.Auth = username, password, ""
		}
		if _, exists := result[host]; exists {
			return nil, errors.New("duplicate registry authentication identity")
		}
		result[host] = config
	}
	return result, nil
}

func registryHost(registry string) (string, error) {
	if !strings.Contains(registry, "://") {
		registry = "https://" + registry
	}
	parsed, err := url.Parse(registry)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil ||
		parsed.RawQuery != "" || parsed.Fragment != "" || strings.ContainsAny(parsed.Host, "* ") ||
		strings.ContainsAny(parsed.Path, "*\\ ") {
		return "", errors.New("firmware credentials require exact HTTPS registry hosts and optional repository prefixes; wildcards are unsupported")
	}
	host := strings.ToLower(parsed.Host)
	if host == "index.docker.io" || host == "registry-1.docker.io" {
		host = "index.docker.io"
	}
	scope := strings.Trim(parsed.Path, "/")
	if scope == "" || scope == "v1" {
		return host, nil
	}
	for _, component := range strings.Split(scope, "/") {
		if component == "" || component == "." || component == ".." {
			return "", errors.New("registry credential repository prefix is invalid")
		}
	}
	return host + "/" + scope, nil
}
