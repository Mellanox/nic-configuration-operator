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

package helper

import (
	"context"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/Mellanox/nic-configuration-operator/pkg/consts"
)

// NicFirmwareMap contains supported mapping of NIC firmware with each in the format of:
// NIC ID, Firmware version
var NicFirmwareMap = []string{}

func InitNicFwMapFromConfigMap(client kubernetes.Interface, namespace string) error {
	cm, err := client.CoreV1().ConfigMaps(namespace).Get(
		context.Background(),
		consts.SupportedNicFirmwareConfigmap,
		metav1.GetOptions{},
	)
	// if the configmap does not exist, return false
	if err != nil {
		return err
	}
	for _, v := range cm.Data {
		NicFirmwareMap = append(NicFirmwareMap, v)
	}

	return nil
}

func GetRecommendedFwVersion(deviceId, ofed string) string {
	for _, n := range NicFirmwareMap {
		fw := strings.Split(n, " ")
		if len(fw) < 3 {
			log.Log.Info("incorrect NicFirmwareMap value", "fw", fw)
			return ""
		}
		if deviceId == fw[0] && ofed == fw[1] {
			return fw[2]
		}
	}
	return ""
}
