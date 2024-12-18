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

package consts

const (
	MellanoxVendor = "15b3"

	Ethernet   = "Ethernet"
	Infiniband = "Infiniband"

	ConfigUpdateInProgressCondition     = "ConfigUpdateInProgress"
	FirmwareConfigMatchCondition        = "FirmwareConfigMatch"
	IncorrectSpecReason                 = "IncorrectSpec"
	UpdateStartedReason                 = "UpdateStarted"
	PendingRebootReason                 = "PendingReboot"
	NonVolatileConfigUpdateFailedReason = "NonVolatileConfigUpdateFailed"
	RuntimeConfigUpdateFailedReason     = "RuntimeConfigUpdateFailed"
	UpdateSuccessfulReason              = "UpdateSuccessful"
	SpecValidationFailed                = "SpecValidationFailed"
	FirmwareError                       = "FirmwareError"

	DeviceConfigSpecEmptyReason = "DeviceConfigSpecEmpty"
	DeviceFwMatchReason         = "DeviceFirmwareConfigMatch"
	DeviceFwMismatchReason      = "DeviceFirmwareConfigMismatch"

	PartNumberPrefix      = "pn:"
	SerialNumberPrefix    = "sn:"
	FirmwareVersionPrefix = "fw version:"
	PSIDPrefix            = "psid:"
	LinkStatsPrefix       = "lnksta"
	MaxReadReqPrefix      = "maxreadreq"
	TrustStatePrefix      = "priority trust state:"
	PfcEnabledPrefix      = "enabled"

	NetClass = 0x02

	LastAppliedStateAnnotation = "lastAppliedState"

	NvParamFalse              = "0"
	NvParamTrue               = "1"
	NvParamLinkTypeInfiniband = "1"
	NvParamLinkTypeEthernet   = "2"
	NvParamZero               = "0"

	SriovEnabledParam        = "SRIOV_EN"
	SriovNumOfVfsParam       = "NUM_OF_VFS"
	LinkTypeP1Param          = "LINK_TYPE_P1"
	LinkTypeP2Param          = "LINK_TYPE_P2"
	MaxAccOutReadParam       = "MAX_ACC_OUT_READ"
	RoceCcPrioMaskP1Param    = "ROCE_CC_PRIO_MASK_P1"
	RoceCcPrioMaskP2Param    = "ROCE_CC_PRIO_MASK_P2"
	CnpDscpP1Param           = "CNP_DSCP_P1"
	CnpDscpP2Param           = "CNP_DSCP_P2"
	Cnp802pPrioP1Param       = "CNP_802P_PRIO_P1"
	Cnp802pPrioP2Param       = "CNP_802P_PRIO_P2"
	AtsEnabledParam          = "ATS_ENABLED"
	AdvancedPCISettingsParam = "ADVANCED_PCI_SETTINGS"

	SecondPortPrefix = "P2"

	EnvBaremetal = "Baremetal"

	MaintenanceRequestor   = "configuration.nic.mellanox.com"
	MaintenanceRequestName = "nic-configuration-operator-maintenance"

	HostPath = "/host"

	SupportedNicFirmwareConfigmap = "supported-nic-firmware"
	Mlx5ModuleVersionPath         = "/sys/bus/pci/drivers/mlx5_core/module/version"

	FwConfigNotAppliedAfterRebootErrorMsg = "firmware configuration failed to apply after reboot"

	ConfigDaemonManifestsPath = "./bindata/manifests/daemon"

	OperatorConfigMapName = "nic-configuration-operator-config"
)
