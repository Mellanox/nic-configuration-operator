/*
2025 NVIDIA CORPORATION & AFFILIATES
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

package host

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	execUtils "k8s.io/utils/exec"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/Mellanox/nic-configuration-operator/pkg/consts"
	"github.com/Mellanox/nic-configuration-operator/pkg/utils"
)

// HostUtils is an interface that contains util functions that perform operations on the actual host
type HostUtils interface {
	// DiscoverOfedVersion retrieves installed OFED version
	// returns string - installed OFED version
	// returns empty string - OFED isn't installed or version couldn't be determined
	DiscoverOfedVersion() string

	// ScheduleReboot schedules reboot on the host
	ScheduleReboot() error
	// GetHostUptimeSeconds returns the host uptime in seconds
	GetHostUptimeSeconds() (time.Duration, error)
}

type hostUtils struct {
	execInterface execUtils.Interface
}

func (h *hostUtils) ScheduleReboot() error {
	log.Log.Info("HostUtils.ScheduleReboot()")
	root, err := os.Open("/")
	if err != nil {
		log.Log.Error(err, "ScheduleReboot(): Failed to os.Open")
		return err
	}

	if err := syscall.Chroot(consts.HostPath); err != nil {
		err := root.Close()
		if err != nil {
			log.Log.Error(err, "ScheduleReboot(): Failed to syscall.Chroot")
			return err
		}
		return err
	}

	defer func() {
		if err := root.Close(); err != nil {
			log.Log.Error(err, "ScheduleReboot(): Failed to os.Close")
			return
		}
		if err := root.Chdir(); err != nil {
			log.Log.Error(err, "ScheduleReboot(): Failed to os.Chdir")
			return
		}
		if err = syscall.Chroot("."); err != nil {
			log.Log.Error(err, "ScheduleReboot(): Failed to syscall.Chroot")
		}
	}()

	cmd := h.execInterface.Command("shutdown", "-r", "now")
	_, err = utils.RunCommand(cmd)
	if err != nil {
		log.Log.Error(err, "ScheduleReboot(): Failed to run shutdown -r now")
		return err
	}
	return nil
}

// DiscoverOfedVersion retrieves installed OFED version
// returns string - installed OFED version
// returns empty string - OFED isn't installed or version couldn't be determined
func (h *hostUtils) DiscoverOfedVersion() string {
	log.Log.Info("HostUtils.DiscoverOfedVersion()")
	versionBytes, err := os.ReadFile(filepath.Join(consts.HostPath, consts.Mlx5ModuleVersionPath))
	if err != nil {
		log.Log.Error(err, "DiscoverOfedVersion(): failed to read mlx5_core version file, OFED isn't installed")
		return ""
	}
	version := strings.TrimSuffix(string(versionBytes), "\n")
	log.Log.Info("HostUtils.DiscoverOfedVersion(): OFED version", "version", version)
	return version
}

// GetHostUptimeSeconds returns the host uptime in seconds
func (h *hostUtils) GetHostUptimeSeconds() (time.Duration, error) {
	log.Log.V(2).Info("HostUtils.GetHostUptimeSeconds()")
	output, err := os.ReadFile("/proc/uptime")
	if err != nil {
		log.Log.Error(err, "HostUtils.GetHostUptimeSeconds(): failed to read the system's uptime")
		return 0, err
	}
	uptimeStr := strings.Split(string(output), " ")[0]
	uptimeSeconds, _ := strconv.ParseFloat(uptimeStr, 64)

	return time.Duration(uptimeSeconds) * time.Second, nil
}

func NewHostUtils() HostUtils {
	return &hostUtils{execInterface: execUtils.New()}
}
