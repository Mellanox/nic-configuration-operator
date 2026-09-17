/*
2026 NVIDIA CORPORATION & AFFILIATES
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

package devicediscovery

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/jaypipes/ghw/pkg/pci"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/Mellanox/nic-configuration-operator/pkg/types"
)

// GetVPD retrieves the PCI VPD identifier string, part number, and serial
// number through the kernel's sysfs interface. The kernel transparently routes
// VPD access to function 0 for devices whose PCI functions share VPD storage.
func (d *deviceDiscoveryUtils) GetVPD(pciAddr string) (*types.VPD, error) {
	log.Log.Info("HostUtils.GetVPD()", "pciAddr", pciAddr)

	parsed, err := (&pci.Device{Address: pciAddr}).VPD(context.Background())
	if err != nil {
		return nil, fmt.Errorf("reading PCI VPD for %s: %w", pciAddr, err)
	}
	vpd, err := mapPCIVPD(parsed)
	if err != nil {
		return nil, fmt.Errorf("parsing PCI VPD for %s: %w", pciAddr, err)
	}
	return vpd, nil
}

// mapPCIVPD maps ghw's generic PCI VPD representation to the fields required
// by NicDevice discovery, validating required fields along the way.
func mapPCIVPD(parsed *pci.VPD) (*types.VPD, error) {
	if parsed == nil {
		return nil, fmt.Errorf("parsed VPD is nil")
	}

	modelName, err := parseVPDText("identifier string", parsed.Identifier)
	if err != nil {
		return nil, err
	}
	partNumber, err := parseVPDText("PN", parsed.ReadOnly["PN"])
	if err != nil {
		return nil, err
	}
	serialNumber, err := parseVPDText("SN", parsed.ReadOnly["SN"])
	if err != nil {
		return nil, err
	}

	missing := make([]string, 0, 2)
	if partNumber == "" {
		missing = append(missing, "PN")
	}
	if serialNumber == "" {
		missing = append(missing, "SN")
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("VPD read-only data is missing required keyword(s): %s", strings.Join(missing, ", "))
	}

	return &types.VPD{
		PartNumber:   partNumber,
		SerialNumber: serialNumber,
		ModelName:    modelName,
	}, nil
}

func parseVPDText(field, value string) (string, error) {
	value = strings.Trim(value, " \t\r\n\x00")
	if !utf8.ValidString(value) {
		return "", fmt.Errorf("VPD field %s contains invalid UTF-8", field)
	}
	return value, nil
}
