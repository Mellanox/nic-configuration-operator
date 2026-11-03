// Copyright 2026 NVIDIA CORPORATION & AFFILIATES
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
// http://www.apache.org/licenses/LICENSE-2.0

// Package firmwareimage defines the immutable firmware container payload contract.
package firmwareimage

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
)

const (
	RootDir     = "/nic-firmware-images"
	PayloadPath = "/firmware/firmware.bin"
	PayloadName = "firmware.bin"
)

// OCI references are names, not HTTP URLs. A digest pins both tagged and untagged names.
var referencePattern = regexp.MustCompile(`^(?:[a-z0-9][a-z0-9.-]*(?::[0-9]+)?/)[a-z0-9]+(?:(?:[._]|__|[-]+)[a-z0-9]+)*(?:/[a-z0-9]+(?:(?:[._]|__|[-]+)[a-z0-9]+)*)*(?::[A-Za-z0-9_][A-Za-z0-9_.-]{0,127})?@sha256:[a-f0-9]{64}$`)

// ImageKey gives each validated reference its own cache identity.
func ImageKey(ref string) (string, error) {
	if len(ref) > 512 || !referencePattern.MatchString(ref) {
		return "", fmt.Errorf("firmware image must be an OCI reference pinned with @sha256:<64 lowercase hex digits>: %q", ref)
	}
	sum := sha256.Sum256([]byte(ref))
	return hex.EncodeToString(sum[:]), nil
}
