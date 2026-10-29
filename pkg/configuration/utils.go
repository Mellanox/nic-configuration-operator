/*
2024 NVIDIA CORPORATION & AFFILIATES
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

package configuration

import (
	"fmt"
	"net"

	"github.com/Mellanox/nic-configuration-operator/pkg/consts"
	"github.com/vishvananda/netlink"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// ConfigurationUtils observes NCO-specific link policy before generic configuration.
type ConfigurationUtils interface {
	GetLinkType(string) string
	HasNoCarrier(string) (bool, error)
}
type configurationUtils struct{}

func (d *configurationUtils) GetLinkType(name string) string {
	log.Log.Info("ConfigurationUtils.GetLinkType()", "name", name)
	link, err := netlink.LinkByName(name)
	if err != nil {
		log.Log.Error(err, "GetLinkType(): failed to get link", "device", name)
		return ""
	}
	return encapTypeToLinkType(link.Attrs().EncapType)
}

// HasNoCarrier returns whether the net device is administratively up without a running carrier.
// This matches the condition rendered as NO-CARRIER by iproute2.
func (d *configurationUtils) HasNoCarrier(name string) (bool, error) {
	log.Log.Info("ConfigurationUtils.HasNoCarrier()", "name", name)
	link, err := netlink.LinkByName(name)
	if err != nil {
		return false, fmt.Errorf("failed to get link state for network interface %s: %w", name, err)
	}

	return hasNoCarrier(link.Attrs().Flags), nil
}

func hasNoCarrier(flags net.Flags) bool {
	return flags&net.FlagUp != 0 && flags&net.FlagRunning == 0
}

func encapTypeToLinkType(encapType string) string {
	switch encapType {
	case "ether":
		return consts.Ethernet
	case "infiniband":
		return consts.Infiniband
	default:
		return ""
	}
}

func newConfigurationUtils() ConfigurationUtils { return &configurationUtils{} }

// mergeParams copies src into dst, with src winning on key collisions. Callers merge layers in
// increasing priority order so the highest-priority layer is applied last. nv param keys are always
// concrete (e.g. "MODULE_SPLIT_M0[2]"); rawNvConfig range syntax like "NAME[0..3]" is rejected by CEL
// validation on the CRD, and Spectrum-X/template params only ever emit concrete keys.
func mergeParams(dst, src map[string]string) {
	for k, v := range src {
		dst[k] = v
	}
}
