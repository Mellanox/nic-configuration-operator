# NIC Configuration Library Reference

The `pkg/` packages of the NIC Configuration Operator serve as a reusable Go library for managing NVIDIA NIC firmware, NV configuration, runtime settings, and Spectrum-X optimizations programmatically — without running the full Kubernetes operator.

**Go module:** `github.com/Mellanox/nic-configuration-operator`

```go
import (
    "github.com/Mellanox/nic-configuration-operator/api/v1alpha1"
    "github.com/Mellanox/nic-configuration-operator/pkg/firmware"
    "github.com/Mellanox/nic-configuration-operator/pkg/configuration"
    libfirmware "github.com/Mellanox/dms-lib/pkg/firmware"
    libconfiguration "github.com/Mellanox/dms-lib/pkg/configuration"
    "github.com/Mellanox/nic-configuration-operator/pkg/nvconfig"
    "github.com/Mellanox/nic-configuration-operator/pkg/spectrumx"
    "github.com/Mellanox/nic-configuration-operator/pkg/types"
    "github.com/Mellanox/nic-configuration-operator/pkg/consts"
)
```

---

## Quick Start

Firmware and runtime operations use local dms-lib backends. NCO no longer starts
`dmsd` or exposes `pkg/dms` clients for remote servers.

```go
fwBackend, err := libfirmware.NewManager(libfirmware.Options{})
if err != nil { return err }
runtimeBackend, err := libconfiguration.NewManager(libconfiguration.Options{})
if err != nil { return err }

fwMgr := firmware.NewFirmwareManager(nil, fwBackend, "")
rebootRequired, err := fwMgr.InstallFirmware(ctx, device, &types.FirmwareInstallOptions{
    FwFilePath: "/path/to/fw.bin",
})
// Handle err and rebootRequired according to the application's maintenance policy.

nvUtils := nvconfig.NewNVConfigUtils()
cfgMgr := configuration.NewConfigurationManager(nvUtils, runtimeBackend, fwBackend)
result, err := cfgMgr.ApplyNVConfiguration(ctx, device, &types.ConfigurationOptions{})
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
    // returns bool: reboot required after a failed reset; SkipReset retains false
    // returns error: firmware install errors
    InstallFirmware(ctx context.Context, device *v1alpha1.NicDevice, options *types.FirmwareInstallOptions) (bool, error)

    // GetFirmwareVersionsFromDevice retrieves burned and running FW versions
    // returns: burned version, running version, error
    GetFirmwareVersionsFromDevice(device *v1alpha1.NicDevice) (string, string, error)
}
```

**Constructor:**
```go
func NewFirmwareManager(client client.Client, backend libfirmware.Manager, namespace string) FirmwareManager
```
- `client`: K8s client for operator mode (can be `nil` in library mode when using `FwFilePath`)
- `backend`: shared library firmware inspection, install and reset implementation
- `namespace`: K8s namespace for firmware source lookup (operator mode only)

#### InstallFirmware Flow (Dual-Mode)

1. **Version extraction** — if `options.Version == ""` and `options.FwFilePath != ""`, version is extracted from the firmware file using `flint` (ConnectX) or `mlx-mkbfb` (BlueField)
2. **Idempotency check** — queries current burned version before resolving a cache path; skips install and reset if already matching. A query error retains the existing behavior of proceeding to install.
3. **Path resolution**:
   - **Library mode** (`FwFilePath` set): uses path directly
   - **Operator mode** (`FwFilePath` empty): resolves from K8s cache via `device.Spec.Firmware.NicFirmwareSourceRef`
4. **Install**: delegates the resolved local artifact and observed ASIC/PFs to dms-lib. The library owns image verification, ConnectX burning and BlueField installation.
5. **Reset**: delegates to dms-lib unless `SkipReset=true`. Firmware-wrapper reset passes `SKIP_VM_CHECK`; configuration reset retains its separate default. On reset failure → `(true, nil)`. Skipping reset retains the historical `(false, nil)` result and leaves activation to the caller.

#### FirmwareUtils Interface

Source: `pkg/firmware/utils.go`

```go
type FirmwareUtils interface {
    DownloadFile(url, destPath string) error
    UnzipFiles(zipPath, destDir string) ([]string, error)
    GetFirmwareVersionAndPSIDFromFWBinary(firmwareBinaryPath string) (version string, psid string, err error)
    GetFWVersionsFromBFB(bfbPath string) (map[string]string, error)
    GetDocaSpcXCCVersion(docaSpcXCCPath string) (string, error)
    CleanupDirectory(root string, allowedSet map[string]struct{}) error
    InstallDebPackage(debPath string) error
    GetInstalledDebPackageVersion(packageName string) string
}
```

The utility implementation is private. BIN/BFB metadata methods delegate to dms-lib; download, archive/cache operations and CC package handling remain in NCO.

```go
func NewFirmwareProvisioner(backend libfirmware.Manager) FirmwareProvisioner
```

The provisioner retains URL-to-file metadata, cache validation and cleanup, version/PSID organization, and BFB/CC package source handling. Kubernetes source readiness and maintenance remain consumer policy.

---

### pkg/configuration/ — Configuration Management

Source: `pkg/configuration/manager.go`

#### ConfigurationManager Interface

```go
type ConfigurationManager interface {
    // ValidateDeviceNvSpec validates device's NV spec against host configuration
    // returns: update required, reboot required, unsupported parameters, error
    ValidateDeviceNvSpec(ctx context.Context, device *v1alpha1.NicDevice) (bool, bool, []string, error)

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
    nvConfigUtils nvconfig.NVConfigUtils,
    runtimeBackend libconfiguration.Manager,
    firmwareBackend libfirmware.Manager,
) ConfigurationManager
```

#### Configuration execution

The daemon delegates plan-backed and ordinary execution to dms-lib, translating
ordinary template intent into explicit NV/runtime overrides. NCO retains its
maintenance, firmware activation, status and CC process lifecycle.

The compatibility ConfigurationManager keeps ordinary Force, WithDefault,
NetworkBay and reset behavior. It no longer compiles or executes Spectrum-X
plans. Its runtime method now delegates to dms-lib using observed endpoint relationships; no separate DMS QoS engine remains. Library consumers should use dms-lib directly for generic plan configuration.

#### ResetToDefault Behavior

1. Reset NV config via `mlxconfig reset` for each PF
2. Re-enable `ADVANCED_PCI_SETTINGS=1`
3. Preserve BlueField operation mode (`INTERNAL_CPU_OFFLOAD_ENGINE`) after reset
4. Requires reboot after reset

---

### pkg/dmscli/ — Agentless DMS CLI API

Sources: `pkg/dmscli/nvconfig.go`, `pkg/dmscli/blueprints.go`, `pkg/dmscli/xpath.go`

`pkg/dmscli` wraps the local, agentless `dms-cli` executable with package-level
functions; there is no client object or stored runtime state. It supports bulk
NVConfig apply, Blueprints plan generation, and generic typed XPath GET and SET.
The same typed operation is shared by ordinary DMS SET and NVConfig apply so
other projects can reuse the API without depending on NCO's Spectrum-X policy.

```go
type XPathOperation struct {
	Path        string
	Values      map[string]any
	Scope       string
	TargetClass string
}

// Compatibility alias used by ApplyNVConfigRequest.
type NVConfigXPathOperation = XPathOperation

type XPathQuery struct {
    Path   string
    Leaves []string
}

type NVConfigParam struct {
    Param string
    Value any // JSON string, boolean, or number
}

type ApplyNVConfigRequest struct {
    Target      string
    Ports       []int
    Typed       []NVConfigXPathOperation
    Raw         []NVConfigParam
    WithDefault bool
    Force       bool
}

func ApplyNVConfig(
    ctx context.Context,
    execInterface execUtils.Interface,
    request ApplyNVConfigRequest,
) (*ApplyNVConfigResult, error)

type BlueprintPlanRequest struct {
	BlueprintsStateDir string
	Profile            string
	Name               string
	Stage              string
	TargetMapFile      string
	Params             []string // key=value; values must not contain commas
}

func GenerateBlueprintPlan(
    ctx context.Context,
    execInterface execUtils.Interface,
    request BlueprintPlanRequest,
) (*BlueprintPlanResult, error)

func QueryXPaths(
    ctx context.Context,
    execInterface execUtils.Interface,
    target string,
    queries []XPathQuery,
) (*QueryXPathsResult, error)

func SetXPaths(
    ctx context.Context,
    execInterface execUtils.Interface,
    target string,
    operations []XPathOperation,
) (*SetXPathsResult, error)
```

The caller supplies the repository-standard `k8s.io/utils/exec.Interface`; this
reuses the existing command abstraction and supports
`k8s.io/utils/exec/testing.FakeExec` in tests. Command output is logged at V(2)
through a `logr.Logger` carried by the context. The command shape is:

```text
dms-cli --json -t pci/<BDF> --input <payload-json> /nvidia/nvconfig/apply
```

The payload supports `ports`, `typed`, `raw`, `with-default`, and `force`.
`Target` is carried by `-t` and is not serialized. For Spectrum-X, the target
is each discovered PCI function in turn, `ports` contains its local port (`[1]`),
and the existing template-derived native parameters and doSPCX prepare operations are
sent in one action. `with-default` and `force` apply to that combined action;
both send the complete breakout plus post-breakout intent once the breakout
barrier is complete, and `force` does so immediately.

Each DMS apply action accepts one BDF. The configuration manager invokes it
separately for every discovered function, preserving native overrides within each
batch. Pending state on every function is verified after all writes before permitting reboot.

Typed GET and SET preserve query/group order and use `;` as a literal argument
between DMS containers:

```text
dms-cli --json -t pci/<BDF> /nvidia/link/ipg admin \
  \; /nvidia/link/physical admin-status
dms-cli --json -t pci/<BDF> /nvidia/link/physical admin-status=down \
  \; /nvidia/link/physical admin-status=up
```

SET value keys are sorted for deterministic command lines. Scalar leaf-list
values use repeated assignments (`lanes=0 lanes=1 ...`), avoiding the current
DMS compact-list parsing limitation. GET decodes the public `dms-cli --json`
contract: one container returns a flat values object and a batch returns objects
keyed by container path. SET success returns `{ "status": "ok" }`. For partial
GETs, successful leaves are merged into `Values`; SET results retain both the
`successes` and `failures` maps. Both operations retain `status`, `error_msg`,
and `error_code` while also returning the command error.

The daemon compiles plans through dms-lib. The independent low-level dms-cli
Blueprint wrapper remains available, but NCO no longer owns the duplicate
planner pipeline, tarball installer or plan cache.

---

### pkg/spectrumx/ — CC Process Lifecycle

Generic planning and configuration live in dms-lib. This package retains only
NCO's existing CC process startup, liveness checks and termination notifications.
The tarball installer, plan cache and duplicate doSPCX compiler are removed.

```go
type SpectrumXManager interface {
    RunDocaSpcXCC(port v1alpha1.NicDevicePortSpec) error
    IsDocaSpcXCCRunning(rdma string) bool
    GetCCTerminationChannel() <-chan string
}

func NewSpectrumXConfigManager() SpectrumXManager
```

#### CC Process Lifecycle

`RunDocaSpcXCC()` launches the preinstalled
`/opt/mellanox/doca/tools/doca_spcx_cc` executable as a background process per
RDMA interface. A 3-second startup check distinguishes startup failures
(returned as errors) from runtime crashes (notified via
`GetCCTerminationChannel()`). The deprecated
`NicFirmwareSource.spec.docaSpcXCCUrlSource` field does not install or select
this executable; custom daemon images must provide it themselves.

---

### pkg/nvconfig/ — NV Config (mlxconfig) Utilities

Source: `pkg/nvconfig/utils.go`

#### NVConfigUtils Interface

```go
type NVConfigUtils interface {
    // QueryNvConfig returns default, current, and next-boot configs
    // parameters: optional specific parameters (e.g., "ESWITCH_HAIRPIN_DESCRIPTORS[0..7]")
    QueryNvConfig(ctx context.Context, port v1alpha1.NicDevicePortSpec, parameters []string) (types.NvConfigQuery, error)

    // SetNvConfigParameter sets a single NV config parameter
    SetNvConfigParameter(port v1alpha1.NicDevicePortSpec, paramName string, paramValue string) error

    // SetNvConfigParametersBatch sets multiple parameters in one DMS NVConfig action
    // params: map of paramName → paramValue
    // withDefault: request --with_default from DMS
    // force: request --force from DMS
    SetNvConfigParametersBatch(port v1alpha1.NicDevicePortSpec, params map[string]string, withDefault bool, force bool) (types.ApplyStatus, error)

    // ResetNvConfig resets NIC's NV config to defaults
    ResetNvConfig(port v1alpha1.NicDevicePortSpec) error

    // SetSystemConf applies a ConnectX-9 Network Bay system configuration for a single ASIC via
    // `mlxconfig -d <device> -y [--force] set_system_conf <conf>[<asic>]` (persistent, reboot-required)
    SetSystemConf(ctx context.Context, port v1alpha1.NicDevicePortSpec, conf string, asic int, force bool) error

    // ValidateSystemConf runs `mlxconfig -d <device> -y validate_system_conf <conf>[<asic>]` and returns
    // the overall match bit plus the names of the mismatched params (the MISMATCH rows), so callers can
    // decide which mismatches are intentional overrides rather than drift.
    ValidateSystemConf(ctx context.Context, port v1alpha1.NicDevicePortSpec, conf string, asic int) (bool, []string, error)
}
```

The built-in implementation also exposes an optional profile-resolution capability:

```go
type SystemConfParamsProvider interface {
    GetSystemConfParams(ctx context.Context, port v1alpha1.NicDevicePortSpec, conf string, asic int) (map[string]string, error)
}
```

`ConfigurationManager` requires this capability only for a device with a selected Network Bay
profile. Keeping it separate preserves compatibility for custom `NVConfigUtils` implementations
that do not manage Network Bay devices.

**Constructor:**
```go
func NewNVConfigUtils() NVConfigUtils
```

The built-in implementation also provides
`SetNvConfigParametersBatchWithContext(ctx, port, params, withDefault, force) (types.ApplyStatus, error)`.
`ConfigurationManager` uses that optional extension so reconcile cancellation reaches
`dms-cli`, while retaining the public `NVConfigUtils` interface for existing library
implementations. Implementations without the extension continue through the legacy method.

The built-in implementation also supports doSPCX XPath validation and combined
native/typed apply. It validates each discovered PCI function through
`pci/<BDF>?port=1` and uses `ports: [1]` in each function's combined apply payload.
Spectrum-X configuration fails closed when a custom utility does not provide
these optional methods.

**Batch command format:**
```
dms-cli --json -t pci/<BDF> --input <payload-json> /nvidia/nvconfig/apply
```
Parameter names are sorted before they are encoded as raw DMS entries. The
apply target is always `pci/<port.PCI>`; the discovered `FwctlDevice` value is
not passed to the NVConfig apply action. Raw query, single-parameter set, reset,
and Network Bay profile resolution continue to invoke `mlxconfig` directly;
profile application uses the ordinary DMS raw-parameter batch, while doSPCX
XPath validation queries through `dms-cli`.

**System profile command format (ConnectX-9 Network Bay):**
```
mlxconfig -d <device> show_system_conf
```
`GetSystemConfParams` parses the textual output, selects `<conf>` and the detected ASIC, expands
ranged parameter names, and merges the result into the ordinary native desired map. The older
`SetSystemConf` and `ValidateSystemConf` methods remain on `NVConfigUtils` for library compatibility,
but the configuration manager no longer uses them during reconciliation.

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
    AllParameters  bool                // True only for an unfiltered native query
}

type VPD struct {
    PartNumber   string
    SerialNumber string
    ModelName    string
}
```

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

`MultiplaneModeNone` (`"none"`), `MultiplaneModeSwplb` (`"swplb"`), `MultiplaneModeHwplb` (`"hwplb"`)

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
    SerialNumber    string              // Serial number (e.g., "MT2116X09299"). Not guaranteed unique — embedded NICs on HGX B300 share a flashed SN; identify NICs by the PCI device address of Ports[0] instead.
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
    FwctlDevice      string // fwctl character device path (e.g., "/dev/fwctl/fwctl0")
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

### DMSPlanReference

Spectrum-X configuration uses `NicConfigurationTemplate.spec.planRef` and
`NicDevice.spec.configuration.planRef`, both containing required `namespace` and
`name` strings identifying DMSPlanParams. Ordinary template fields become explicit
overrides. The obsolete SpectrumXOptimizedSpec and NCO planner APIs are removed;
external consumers must migrate to dms-lib.
See [DMS plan configuration](dms-plan-configuration.md).

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

External commands normally use `cmd.CombinedOutput()` and log at `log.Log.V(2)`.
JSON protocol commands that can emit DMS diagnostics on stderr capture the
streams separately and parse stdout only. Diagnostic content is bounded in both
logs and returned errors. Output is not duplicated in error-level logs.

### Batch Operations

- **NVConfig apply**: the compatibility path keeps one sorted raw batch per PCI target. Plan-backed execution and ordinary intent delegated by the daemon use dms-lib.

### Channel-Based Notifications

The CC process manager (`pkg/spectrumx/`) communicates with controllers via plain Go channels. The controller bridges these to Kubernetes watch events using `source.Channel`.

---

## Recent Changes

| Commit | Summary | Library Impact |
|---|---|---|
| `6dc1e5b` — Expose pkg/ as a reusable Go library | Renamed APIs (`BurnNicFirmware` → `InstallFirmware`, etc.), added option/result types, dual-mode firmware install, `SetNvConfigParametersBatch`, `ResetNicFirmware` | New public API surface; breaking changes from old method names |
| `852ced3` — Fix CX8 reboot blocked by mlxfwreset | Treat `mlxfwreset` failure as non-fatal in controller | No library API change; behavioral fix for CX8 SuperNIC devices |

The CC process manager reports whether a tracked process is running without starting
it during observation. Process startup remains in the apply phase. Custom
implementations of `SpectrumXManager` must keep that distinction.
