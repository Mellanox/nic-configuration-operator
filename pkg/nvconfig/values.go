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

package nvconfig

import (
	"math/big"
	"strings"
)

// ValueMatches compares a native value with its symbolic and numeric mlxconfig representations.
func ValueMatches(values []string, desired string) bool {
	for _, value := range values {
		if mlxConfigValuesEqual(value, desired) {
			return true
		}
	}
	return false
}

func mlxConfigValuesEqual(actual, desired string) bool {
	actual = strings.TrimSpace(actual)
	desired = strings.TrimSpace(desired)
	if strings.EqualFold(actual, desired) {
		return true
	}

	actualNumber, actualIsNumber := parseMlxConfigNumber(actual)
	desiredNumber, desiredIsNumber := parseMlxConfigNumber(desired)
	return actualIsNumber && desiredIsNumber && actualNumber.Cmp(desiredNumber) == 0
}

func parseMlxConfigNumber(value string) (*big.Int, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, false
	}

	base := 10
	digits := value
	if strings.HasPrefix(strings.ToLower(digits), "0x") {
		base = 16
		digits = digits[2:]
	} else if strings.ContainsAny(digits, "abcdefABCDEF") {
		// System configuration profiles use bare hexadecimal values such as FF.
		base = 16
	}

	number, ok := new(big.Int).SetString(digits, base)
	return number, ok
}
