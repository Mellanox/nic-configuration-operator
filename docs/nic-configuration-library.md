# NIC Configuration Library Reference

The `pkg/` packages of the NIC Configuration Operator serve as a reusable Go library for managing NVIDIA NIC firmware, NV configuration, runtime settings, and Spectrum-X optimizations programmatically — without running the full Kubernetes operator.

**Go module:** `github.com/Mellanox/nic-configuration-operator`

```go
import (
    "github.com/Mellanox/nic-configuration-operator/api/v1alpha1"
    "github.com/Mellanox/nic-configuration-operator/pkg/firmware"
    "github.com/Mellanox/nic-configuration-operator/pkg/configuration"
    "github.com/Mellanox/nic-configuration-operator/pkg/dms"
    "github.com/Mellanox/nic-configuration-operator/pkg/nvconfig"
    "github.com/Mellanox/nic-configuration-operator/pkg/spectrumx"
    "github.com/Mellanox/nic-configuration-operator/pkg/types"
    "github.com/Mellanox/nic-configuration-operator/pkg/consts"
)
```

---

## Quick Start

### With local DMS server (operator mode)

```go
// 1. Start local DMS server for devices
dmsSrv := dms.NewDMSServer()
err := dmsSrv.StartDMSServer(devices)
defer dmsSrv.StopDMSServer()

// 2. Install firmware (library mode — direct file path)
fwMgr := firmware.NewFirmwareManager(nil, dmsSrv, "")
rebootRequired, err := fwMgr.InstallFirmware(ctx, device, &types.FirmwareInstallOptions{
    FwFilePath: "/path/to/fw.bin",
})

// 3. Apply NV configuration
nvUtils := nvconfig.NewNVConfigUtils()
spectrumXMgr := spectrumx.NewSpectrumXConfigManager(dmsSrv, configs)
cfgMgr := configuration.NewConfigurationManager(nil, dmsSrv, nvUtils, spectrumXMgr)
result, err := cfgMgr.ApplyNVConfiguration(ctx, device, &types.ConfigurationOptions{})
```

### With external DMS server (library mode)

```go
// Connect to an already-running DMS server with TLS auth
dmsMgr := dms.NewExternalDMSManager(devices, "remotehost:9339",
    []string{"--tls-ca", "/path/ca.pem", "--tls-cert", "/path/cert.pem", "--tls-key", "/path/key.pem"})

// Use dmsMgr with any consumer — firmware, configuration, spectrumx
fwMgr := firmware.NewFirmwareManager(nil, dmsMgr, "")
```

---

## Packages

### pkg/firmware/ — Firmware Management

Source: `pkg/firmware/manager.go`

#### FirmwareManager Interface

```go
type FirmwareManager interface {
    // ValidateRequestedFirmwareSource validates the NicFirmwareSource object
    // returns string: requested firmware version
    // returns error: firmware source not ready or invalid
    ValidateRequestedFirmwareSource(ctx context.Context, device *v1alpha1.NicDevice) (string, error)

    // InstallFirmware updates device firmware to requested version
    // returns bool: reboot required (when fw reset fails or SkipReset=true)
    // returns error: firmware install errors
    InstallFirmware(ctx context.Context, device *v1alpha1.NicDevice, options *types.FirmwareInstallOptions) (bool, error)

    // InstallDocaSpcXCC validates and installs DOCA SPC-X CC package
    // No-op if already installed; error if version mismatch
    InstallDocaSpcXCC(ctx context.Context, device *v1alpha1.NicDevice, targetVersion string) error

    // GetFirmwareVersionsFromDevice retrieves burned and running FW versions
    // returns: burned version, running version, error
    GetFirmwareVersionsFromDevice(device *v1alpha1.NicDevice) (string, string, error)
}
```

**Constructor:**
```go
func NewFirmwareManager(client client.Client, dmsManager dms.DMSManager, namespace string) FirmwareManager
```
- `client`: K8s client for operator mode (can be `nil` in library mode when using `FwFilePath`)
- `dmsManager`: required for BlueField BFB installs via `DMSClient.InstallBFB()`
- `namespace`: K8s namespace for firmware source lookup (operator mode only)

#### InstallFirmware Flow (Dual-Mode)

1. **Version extraction** — if `options.Version == ""` and `options.FwFilePath != ""`, version is extracted from the firmware file using `flint` (ConnectX) or `mlx-mkbfb` (BlueField)
2. **Idempotency check** — queries current burned version; skips install if already matching
3. **Path resolution**:
   - **Library mode** (`FwFilePath` set): uses path directly
   - **Operator mode** (`FwFilePath` empty): resolves from K8s cache via `device.Spec.Firmware.NicFirmwareSourceRef`
4. **Install**: BlueField → DMS `InstallBFB()`; ConnectX → validate version/PSID + verify bootable + `BurnNicFirmware()`
5. **Reset**: `mlxfwreset` unless `SkipReset=true`. On reset failure → `(true, nil)` (reboot required, no error)

#### FirmwareUtils Interface

Source: `pkg/firmware/utils.go`

```go
type FirmwareUtils interface {
    DownloadFile(url, destPath string) error
    UnzipFiles(zipPath, destDir string) ([]string, error)
    GetFirmwareVersionsFromDevice(pciAddress string) (burned string, running string, err error)
    GetFirmwareVersionAndPSIDFromFWBinary(firmwareBinaryPath string) (version string, psid string, err error)
    GetFWVersionsFromBFB(bfbPath string) (map[string]string, error)
    GetDocaSpcXCCVersion(docaSpcXCCPath string) (string, error)
    VerifyImageBootable(firmwareBinaryPath string) error
    CleanupDirectory(root string, allowedSet map[string]struct{}) error
    BurnNicFirmware(ctx context.Context, pciAddress, fwPath string) error
    ResetNicFirmware(ctx context.Context, pciAddress string) error
    InstallDebPackage(debPath string) error
    GetInstalledDebPackageVersion(packageName string) string
}
```

Note: `FirmwareUtils` is unexported via `newFirmwareUtils()` — created internally by `NewFirmwareManager`.

---

### pkg/configuration/ — Configuration Management

Source: `pkg/configuration/manager.go`

#### ConfigurationManager Interface

```go
type ConfigurationManager interface {
    // ValidateDeviceNvSpec validates device's NV spec against host configuration
    // returns: nv config update required, reboot required, error
    ValidateDeviceNvSpec(ctx context.Context, device *v1alpha1.NicDevice) (bool, bool, error)

    // ApplyNVConfiguration calculates and applies missing NV spec configuration
    ApplyNVConfiguration(ctx context.Context, device *v1alpha1.NicDevice, options *types.ConfigurationOptions) (*types.ConfigurationApplyResult, error)

    // ApplyRuntimeConfiguration calculates and applies missing runtime spec configuration
    ApplyRuntimeConfiguration(ctx context.Context, device *v1alpha1.NicDevice) (*types.RuntimeConfigurationApplyResult, error)

    // ResetNicFirmware resets NIC's firmware via mlxfwreset
    ResetNicFirmware(ctx context.Context, device *v1alpha1.NicDevice) error
}
```

**Constructor:**
```go
func NewConfigurationManager(
    eventRecorder record.EventRecorder,
    dmsManager dms.DMSManager,
    nvConfigUtils nvconfig.NVConfigUtils,
    spectrumXConfigManager spectrumx.SpectrumXManager,
) ConfigurationManager
```

#### NV Configuration Flow

1. **Query** current NV config via `nvConfigUtils.QueryNvConfig()`
2. **Diff** desired vs current parameters
3. **ADVANCED_PCI_SETTINGS unlock** — automatically enabled before applying other configs (reboot may be required to unlock additional parameters)
4. **Batch set** via `nvConfigUtils.SetNvConfigParametersBatch()` — single `mlxconfig set` call
5. **Optional reset** — `mlxfwreset` unless `ConfigurationOptions.SkipReset=true`

#### Runtime Configuration Flow

1. **PCI MaxReadRequest** — applied via sysfs
2. **QoS settings** — trust mode and PFC applied via `DMSClient.SetQoSSettings()`

#### ResetToDefault Behavior

1. Reset NV config via `mlxconfig reset` for each PF
2. Re-enable `ADVANCED_PCI_SETTINGS=1`
3. Preserve BlueField operation mode (`INTERNAL_CPU_OFFLOAD_ENGINE`) after reset
4. Requires reboot after reset

---

### pkg/dms/ — Device Management Service

Source: `pkg/dms/dms.go`, `pkg/dms/client.go`

#### DMSManager Interface

Provides per-device DMS client lookup. This is the interface used by all consuming packages (firmware, configuration, spectrumx).

```go
type DMSManager interface {
    // GetDMSClientBySerialNumber returns DMS client for a device
    GetDMSClientBySerialNumber(serialNumber string) (DMSClient, error)
}
```

#### DMSServer Interface

Extends DMSManager with server lifecycle management. Used only by the daemon entry point.

```go
type DMSServer interface {
    DMSManager
    // StartDMSServer starts a single DMS server for all given NIC devices
    StartDMSServer(devices []v1alpha1.NicDeviceStatus) error
    // StopDMSServer stops the running DMS server (SIGTERM)
    StopDMSServer() error
    // IsRunning returns whether the DMS server process is alive
    IsRunning() bool
}
```

**Constructors:**
```go
// NewDMSServer creates a local DMS server manager (starts/stops dmsd process)
// Clients are created with authParams: ["--insecure"]
func NewDMSServer() DMSServer

// NewExternalDMSManager connects to an already-running external DMS server
// authParams: authentication/security flags for dmsc (e.g. ["--insecure"],
// ["--tls-ca", "/ca.pem", "--tls-cert", "/cert.pem", "--tls-key", "/key.pem"],
// ["-u", "user", "-p", "pass"])
func NewExternalDMSManager(devices []v1alpha1.NicDeviceStatus, address string, authParams []string) DMSManager
```

#### DMSClient Interface

```go
type DMSClient interface {
    // GetQoSSettings returns current QoS settings (trust mode, PFC, ToS)
    GetQoSSettings(interfaceName string) (*v1alpha1.QosSpec, error)
    // SetQoSSettings sets QoS on all ports of the device
    SetQoSSettings(spec *v1alpha1.QosSpec) error
    // GetParameters returns current values for given DMS paths
    GetParameters(params []types.ConfigurationParameter) (map[string]string, error)
    // SetParameters batches all updates into single dmsc command
    SetParameters(params []types.ConfigurationParameter) error
    // InstallBFB installs BFB firmware on BlueField device
    InstallBFB(ctx context.Context, version string, bfbPath string) error
}
```

#### Architecture

A single `dmsd` server process manages all NIC devices. Started with comma-separated PCI addresses via `--target_pci`. Port discovery starts from 9339, incrementing on conflict.

Per-device `dmsClient` instances hold configurable `authParams` (e.g. `["--insecure"]` for local server, TLS/credential flags for external server) and use `--target <pci>` to address individual NICs:
```
dmsc -a <address> <authParams...> --target 0000:08:00.0 get --path /path
dmsc -a <address> <authParams...> --target 0000:08:00.0 set --update "path:::type:::value"
```

**Supported authentication flags** (passed via `authParams`):
- `--insecure` — disable TLS (default for local server)
- `-u <user> -p <pass>` — username/password authentication
- `--tls-ca <path>` — server CA certificate
- `--tls-cert <path>` — client TLS certificate
- `--tls-key <path>` — client TLS private key
- `--skip-verify` — skip server certificate verification

#### Batch SetParameters

`SetParameters` collects all update entries via `collectSetUpdates()` (expanding interface/port/priority combinations into individual `path:::type:::value` strings using `formatSetUpdate()`), then passes them to a single `dmsc set` command with multiple `--update` flags and `--timeout 5m`:

```
dmsc -a localhost:9339 --insecure --target 0000:08:00.0 set --timeout 5m \
  --update "/interfaces/interface[name=p0]/nvidia/qos/config/trust-mode:::string:::dscp" \
  --update "/interfaces/interface[name=p0]/nvidia/cc/config/priority[id=0]/np_enabled:::bool:::1" \
  --update "/interfaces/interface[name=p0]/nvidia/cc/config/priority[id=1]/np_enabled:::bool:::1"
```

**Parameter expansion rules:**
- Path contains `interface` → expanded per device port (with `[name=<iface>]` filter)
- Path contains `priority` → additionally expanded for IDs 0–7 (with `[id=<n>]` filter)
- Otherwise → single update entry with unfiltered path

**IgnoreError semantics:** if the batch command fails and ALL params have `IgnoreError=true`, the error is suppressed.

---

### pkg/spectrumx/ — Spectrum-X Configuration

Source: `pkg/spectrumx/spectrumx.go`

#### SpectrumXManager Interface

```go
type SpectrumXManager interface {
    // Breakout configuration (phase 1 — requires reboot)
    BreakoutConfigApplied(ctx context.Context, device *v1alpha1.NicDevice) (bool, error)
    ApplyBreakoutConfig(ctx context.Context, device *v1alpha1.NicDevice) (*types.ConfigurationApplyResult, error)

    // NV configuration (phase 2 — applied after breakout reboot)
    NvConfigApplied(ctx context.Context, device *v1alpha1.NicDevice) (bool, error)
    ApplyNvConfig(ctx context.Context, device *v1alpha1.NicDevice) (*types.ConfigurationApplyResult, error)

    // Runtime configuration (phase 3 — no reboot)
    RuntimeConfigApplied(device *v1alpha1.NicDevice) (bool, error)
    ApplyRuntimeConfig(device *v1alpha1.NicDevice) (*types.RuntimeConfigurationApplyResult, error)

    // DOCA Congestion Control
    GetDocaCCTargetVersion(device *v1alpha1.NicDevice) (string, error)
    RunDocaSpcXCC(port v1alpha1.NicDevicePortSpec) error
    GetCCTerminationChannel() <-chan string
}
```

**Constructor:**
```go
func NewSpectrumXConfigManager(
    dmsManager dms.DMSManager,
    spectrumXConfigs map[string]*types.SpectrumXConfig,
) SpectrumXManager
```
- `spectrumXConfigs`: keyed by version string (e.g., `"RA1.3"`, `"RA2.0"`, `"RA2.1"`), loaded from YAML via `types.LoadSpectrumXConfig()`

#### Two-Phase Configuration Flow

1. **Breakout** → select params by multiplane mode and plane count → apply via mlxconfig batch → reboot
2. **NV config** → filter by `DeviceId`, `Breakout`, `Multiplane` → split into MLXConfig params (`SetNvConfigParametersBatch`) and DMS params (`SetParameters`) → apply in batch → reboot
3. **Runtime** → RoCE, Adaptive Routing, Congestion Control, InterPacketGap settings applied via DMS and sysfs — no reboot

**Parameter filtering:** each `ConfigurationParameter` can be filtered by `DeviceId` (e.g., `"1023"` for CX8, `"a2dc"` for BF3), `Breakout` (plane count), and `Multiplane` mode.

#### CC Process Lifecycle

`RunDocaSpcXCC()` launches a `doca_spcx_cc` background process per RDMA interface. A 3-second startup check distinguishes startup failures (returned as errors) from runtime crashes (notified via `GetCCTerminationChannel()`).

---

### pkg/nvconfig/ — NV Config (mlxconfig) Utilities

Source: `pkg/nvconfig/utils.go`

#### NVConfigUtils Interface

```go
type NVConfigUtils interface {
    // QueryNvConfig returns default, current, and next-boot configs
    // additionalParameter: optional specific parameter (e.g., "ESWITCH_HAIRPIN_DESCRIPTORS[0..7]")
    QueryNvConfig(ctx context.Context, pciAddr string, additionalParameter string) (types.NvConfigQuery, error)

    // SetNvConfigParameter sets a single NV config parameter
    SetNvConfigParameter(pciAddr string, paramName string, paramValue string) error

    // SetNvConfigParametersBatch sets multiple parameters in single mlxconfig call
    // params: map of paramName → paramValue
    // withDefault: add --with_default flag
    SetNvConfigParametersBatch(pciAddr string, params map[string]string, withDefault bool) error

    // ResetNvConfig resets NIC's NV config to defaults
    ResetNvConfig(pciAddr string) error
}
```

**Constructor:**
```go
func NewNVConfigUtils() NVConfigUtils
```

**Batch command format:**
```
mlxconfig -d <pci> --yes [--with_default] set PARAM1=VAL1 PARAM2=VAL2 ...
```
Parameter names are sorted for deterministic command generation.

---

### pkg/types/ — Shared Types

Source: `pkg/types/types.go`, `pkg/types/spectrumx.go`

#### Option Types

```go
// FirmwareInstallOptions — options for InstallFirmware
type FirmwareInstallOptions struct {
    Version    string // FW version (for K8s cache lookup in operator mode)
    FwFilePath string // Direct path to .bin/.bfb file (library mode)
    SkipReset  bool   // Skip mlxfwreset after burning
}

// ConfigurationOptions — options for ApplyNVConfiguration
type ConfigurationOptions struct {
    SkipReset   bool // Skip mlxfwreset after NV config apply
    WithDefault bool // Add --with_default flag to mlxconfig set
}
```

#### Result Types

```go
type ApplyStatus int

const (
    ApplyStatusNothingToDo      ApplyStatus = iota // No changes needed
    ApplyStatusSuccess                              // All changes applied
    ApplyStatusFailed                               // Apply failed
    ApplyStatusPartiallyApplied                     // Some changes applied
)

type ConfigurationApplyResult struct {
    Status         ApplyStatus
    RebootRequired bool
    Error          error
}

type RuntimeConfigurationApplyResult struct {
    Status ApplyStatus
    Error  error
}
```

#### Query Types

```go
type NvConfigQuery struct {
    DefaultConfig  map[string][]string // Default NV config values
    CurrentConfig  map[string][]string // Currently applied values
    NextBootConfig map[string][]string // Values effective after next boot
}

type VPD struct {
    PartNumber   string
    SerialNumber string
    ModelName    string
}
```

#### ConfigurationParameter

Used by DMS client and Spectrum-X config. Loaded from YAML files in `bindata/spectrum-x/`.

```go
type ConfigurationParameter struct {
    Name             string // Human-readable name
    MlxConfig        string // mlxconfig parameter name (e.g., "LINK_TYPE_P1")
    Value            string // Value to set
    ValueType        string // DMS value type: "string", "bool", "int"
    DMSPath          string // DMS path (e.g., "/interfaces/interface/nvidia/qos/config/trust-mode")
    AlternativeValue string // Alternative value representation
    DeviceId         string // Device ID filter (e.g., "1023", "a2dc")
    Breakout         int    // Plane count filter (1, 2, 4)
    Multiplane       string // Multiplane mode filter (none, swplb, hwplb, uniplane)
    IgnoreError      bool   // Suppress errors for this parameter in batch operations
}
```

A parameter with `MlxConfig` set is applied via `nvconfig.SetNvConfigParametersBatch()`; a parameter with `DMSPath` set is applied via `dms.DMSClient.SetParameters()`.

#### Spectrum-X Config Types

```go
type SpectrumXConfig struct {
    BreakoutConfig         SpectrumXBreakoutConfig
    NVConfig               []ConfigurationParameter
    RuntimeConfig          SpectrumXRuntimeConfig
    UseSoftwareCCAlgorithm bool   // Use software congestion control
    DocaCCVersion          string // Required DOCA CC version
}

type SpectrumXBreakoutConfig struct {
    Swplb    map[int][]ConfigurationParameter // keyed by plane count
    Hwplb    map[int][]ConfigurationParameter
    Uniplane map[int][]ConfigurationParameter
    None     map[int][]ConfigurationParameter
}

type SpectrumXRuntimeConfig struct {
    Roce              []ConfigurationParameter
    AdaptiveRouting   []ConfigurationParameter
    CongestionControl []ConfigurationParameter
    InterPacketGap    InterPacketGapConfig
}

type InterPacketGapConfig struct {
    PureL3 []ConfigurationParameter // Pure L3 overlay mode
    L3EVPN []ConfigurationParameter // L3 EVPN overlay mode
}
```

**Loading:** `func LoadSpectrumXConfig(configPath string) (*SpectrumXConfig, error)` — reads YAML file.

#### Error Helpers

```go
// Incorrect spec (invalid device configuration)
func IncorrectSpecError(msg string) error
func IsIncorrectSpecError(err error) bool

// Firmware source not ready
func FirmwareSourceNotReadyError(firmwareSourceName, msg string) error
func IsFirmwareSourceNotReadyError(err error) bool

// Values mismatch across ports/priorities
func ValuesDoNotMatchError(param ConfigurationParameter, value string) error
func IsValuesDoNotMatchError(err error) bool
```

---

### pkg/consts/ — Constants

Source: `pkg/consts/consts.go`

#### Device Identifiers

| Constant | Value | Device |
|---|---|---|
| `MellanoxVendor` | `"15b3"` | Mellanox vendor ID |
| `BlueField2DeviceID` | `"a2d6"` | BlueField-2 |
| `BlueField3DeviceID` | `"a2dc"` | BlueField-3 |
| `BlueField3LxDeviceID` | `"a2d9"` | BlueField-3 Lx |
| `BlueField4DeviceID` | `"a2df"` | BlueField-4 |

#### NV Config Parameter Names

| Constant | mlxconfig name |
|---|---|
| `SriovEnabledParam` | `SRIOV_EN` |
| `SriovNumOfVfsParam` | `NUM_OF_VFS` |
| `LinkTypeP1Param` / `LinkTypeP2Param` | `LINK_TYPE_P1` / `LINK_TYPE_P2` |
| `MaxAccOutReadParam` | `MAX_ACC_OUT_READ` |
| `RoceCcPrioMaskP1Param` / `RoceCcPrioMaskP2Param` | `ROCE_CC_PRIO_MASK_P1` / `_P2` |
| `CnpDscpP1Param` / `CnpDscpP2Param` | `CNP_DSCP_P1` / `_P2` |
| `AtsEnabledParam` | `ATS_ENABLED` |
| `AdvancedPCISettingsParam` | `ADVANCED_PCI_SETTINGS` |
| `BF3OperationModeParam` | `INTERNAL_CPU_OFFLOAD_ENGINE` |

#### NV Param Values

| Constant | Value | Meaning |
|---|---|---|
| `NvParamTrue` | `"1"` | Enabled |
| `NvParamFalse` | `"0"` | Disabled |
| `NvParamLinkTypeEthernet` | `"2"` | Ethernet link |
| `NvParamLinkTypeInfiniband` | `"1"` | InfiniBand link |
| `NvParamBF3DpuMode` | `"0"` | BlueField DPU mode |
| `NvParamBF3NicMode` | `"1"` | BlueField NIC mode |

#### Multiplane Modes

`MultiplaneModeNone` (`"none"`), `MultiplaneModeSwplb` (`"swplb"`), `MultiplaneModeHwplb` (`"hwplb"`), `MultiplaneModeUniplane` (`"uniplane"`)

#### Trust Modes

`TrustModeDscp` (`"dscp"`), `TrustModePfc` (`"pfc"`)

#### Firmware Storage Paths

| Constant | Value |
|---|---|
| `NicFirmwareStorage` | `/nic-firmware` |
| `NicFirmwareBinariesFolder` | `firmware-binaries` |
| `BFBFolder` | `bfb` |
| `DocaSpcXCCFolder` | `doca-spc-x-cc` |

---

## API Types (CRD)

Source: `api/v1alpha1/`

These Kubernetes CRD types are used as parameters to library methods.

### NicDevice

```go
type NicDevice struct {
    Spec   NicDeviceSpec
    Status NicDeviceStatus
}
```

### NicDeviceStatus

```go
type NicDeviceStatus struct {
    Node            string              // Node where device is located
    Type            string              // Device type (e.g., "ConnectX7")
    SerialNumber    string              // Serial number (e.g., "MT2116X09299")
    PartNumber      string              // Part number (e.g., "MCX713106AEHEA_QP1")
    PSID            string              // Product Serial ID (e.g., "MT_0000000221")
    FirmwareVersion string              // Installed firmware version (e.g., "22.31.1014")
    DPU             bool                // BlueField in DPU mode
    ModelName       string              // Model name (e.g., "ConnectX-6", "BlueField-3")
    SuperNIC        bool                // SuperNIC device
    Ports           []NicDevicePortSpec // Device ports
    Conditions      []metav1.Condition
}
```

### NicDevicePortSpec

```go
type NicDevicePortSpec struct {
    PCI              string // PCI address (e.g., "0000:3b:00.0")
    NetworkInterface string // Network interface name (e.g., "eth1")
    RdmaInterface    string // RDMA interface name (e.g., "mlx5_1")
}
```

### NicDeviceSpec

```go
type NicDeviceSpec struct {
    Configuration       *NicDeviceConfigurationSpec
    Firmware            *FirmwareTemplateSpec
    InterfaceNameTemplate *NicDeviceInterfaceNameSpec
}
```

### ConfigurationTemplateSpec

```go
type ConfigurationTemplateSpec struct {
    NumVfs                  int
    LinkType                LinkTypeEnum                 // "Ethernet" or "Infiniband"
    PciPerformanceOptimized *PciPerformanceOptimizedSpec
    RoceOptimized           *RoceOptimizedSpec
    GpuDirectOptimized      *GpuDirectOptimizedSpec
    SpectrumXOptimized      *SpectrumXOptimizedSpec
    RawNvConfig             []NvConfigParam              // Arbitrary NV config params
}
```

### QosSpec

```go
type QosSpec struct {
    Trust string // Trust mode (e.g., "dscp", "pfc")
    PFC   string // Priority-based Flow Control (e.g., "0,0,0,1,0,0,0,0")
    ToS   int    // 8-bit Type of Service value
}
```

### SpectrumXOptimizedSpec

```go
type SpectrumXOptimizedSpec struct {
    Enabled        bool
    Version        string // "RA1.3", "RA2.0", "RA2.1"
    Overlay        string // "l3" or "none"
    MultiplaneMode string // "none", "swplb", "hwplb", "uniplane"
    NumberOfPlanes int    // 1, 2, or 4
}
```

### FirmwareTemplateSpec

```go
type FirmwareTemplateSpec struct {
    NicFirmwareSourceRef string // Reference to NicFirmwareSource CR
    UpdatePolicy         string // "Validate" or "Update"
}
```

---

## Design Patterns

### Dual-Mode (Operator vs Library)

Methods support two modes via option structs:
- **Operator mode**: resolves firmware/config from K8s cache. Example: `InstallFirmware` with `FwFilePath=""` looks up cache via `device.Spec.Firmware.NicFirmwareSourceRef`.
- **Library mode**: caller provides paths directly. Example: `InstallFirmware` with `FwFilePath="/path/to/fw.bin"` skips K8s entirely.

### Command Execution Logging

All external commands use `cmd.CombinedOutput()` (not `cmd.Output()`) and log unconditionally at `log.Log.V(2)`. Output is not duplicated in error-level logs.

### Batch Operations

- **mlxconfig**: `SetNvConfigParametersBatch()` applies multiple params in single `mlxconfig set` call
- **DMS**: `SetParameters()` sends all `--update` entries in single `dmsc set` command with `--timeout 5m`
- **IgnoreError**: per-parameter flag; in batch mode, errors are suppressed only if ALL params have `IgnoreError=true`

### Channel-Based Notifications

K8s-free packages (`pkg/spectrumx/`, `pkg/dms/`) communicate with controllers via plain Go channels. The controller bridges these to Kubernetes watch events using `source.Channel`.

---

## Recent Changes

| Commit | Summary | Library Impact |
|---|---|---|
| `6dc1e5b` — Expose pkg/ as a reusable Go library | Renamed APIs (`BurnNicFirmware` → `InstallFirmware`, etc.), added option/result types, dual-mode firmware install, `SetNvConfigParametersBatch`, `ResetNicFirmware` | New public API surface; breaking changes from old method names |
| `a819489` — Refactor DMS to single daemon | Single `dmsd` process for all devices, `dmsClient` with `--target`, `IsRunning()` on manager | `DMSManager.StartDMSServer` replaces `StartDMSInstances`; `DMSClient.IsRunning()` removed |
| `852ced3` — Fix CX8 reboot blocked by mlxfwreset | Treat `mlxfwreset` failure as non-fatal in controller | No library API change; behavioral fix for CX8 SuperNIC devices |
| `3bf35c5` — Batch SetParameters | `collectSetUpdates` + single `dmsc set` with multiple `--update` flags + `--timeout 5m` | `SetParameters` now executes one command instead of N; `formatSetUpdate` shared helper |
