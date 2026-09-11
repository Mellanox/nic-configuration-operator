# doSPCX and Legacy Spectrum-X Parameter Mapping

Copyright 2026 NVIDIA CORPORATION & AFFILIATES

This document compares the effective parameters in the current doSPCX plan with
the legacy Spectrum-X profile supplied as
`ra2.3-spectrum-x-k8s-profile-configmap.yaml`.

## Comparison scope

The comparison selects the configuration used by the current test deployment:

- ConnectX-8 (`1023`)
- legacy `hwplb`, corresponding to doSPCX HWMP
- two planes/PFs
- no overlay, corresponding to the legacy `pureL3` IPG selection

The doSPCX inputs are represented by:

- `pkg/spectrumx/dospcx/testdata/semantic/prepare-plan.json`
- `pkg/spectrumx/dospcx/testdata/semantic/configure-plan.json`

Only effective configuration parameters are compared. Profile and plan schema
versions are intentionally outside the scope of this document.

## NVConfig mapping

All selected legacy NVConfig parameters have an equivalent in the doSPCX
prepare plan.

| Legacy mlxconfig parameter | doSPCX XPath and value |
| --- | --- |
| `NUM_OF_PF=2` | `/nvidia/pci num-pfs=2` |
| `NUM_OF_PLANES_P1=2` | `/nvidia/multiplane num-planes=2` |
| `LAG_RESOURCE_ALLOCATION=1` | `/nvidia/lag resource-allocation=pre-allocation` |
| `MODULE_SPLIT_M0[0..7]=0x1` | `/nvidia/link/breakout/module/[0]/port/[1] lanes=[0..7]` |
| `MODULE_SPLIT_M0[8..15]=0xff` | `/nvidia/link/breakout/module/[0]/port/[255] lanes=[8..15]` |
| `LINK_TYPE_P1=2` | `/nvidia/link/type value=ETH` |
| `SRIOV_EN=1` | `/nvidia/pci/sriov enabled=true` |
| `NUM_OF_VFS=1` | `/nvidia/pci/sriov max-vfs=1` |
| `ROCE_ADAPTIVE_ROUTING_EN=1` | `/nvidia/roce adaptive-routing=true` |
| `USER_PROGRAMMABLE_CC=1` | `/nvidia/cc/config user-programmable=true` |
| `TX_SCHEDULER_LOCALITY_MODE=2` | `/nvidia/roce tx-sched-locality-mode=accumulative` |
| `ROCE_CC_STEERING_EXT=2` | `/nvidia/roce cc-steering-ext=enabled` |
| `ROCE_RTT_RESP_DSCP_P1=48` | `/nvidia/roce/rtt dscp=48` |
| `ROCE_RTT_RESP_DSCP_MODE_P1=1` | `/nvidia/roce/rtt dscp-mode=fixed` |
| `FLEX_PARSER_PROFILE_ENABLE=10` | `/nvidia/misc/parser flex-parser-profile=10` |
| `RDE_DISABLE=1` | `/nvidia/pci rde-disable=true` |
| `VF_LOG_BAR_SIZE=5` | `/nvidia/pci/sriov vf-log-bar-size=5` |
| `KEEP_ETH_LINK_UP_P1=0` | `/nvidia/link/policy keep-eth-link-up=false` |

The doSPCX prepare plan also sets:

```text
/nvidia/multiplane load-balance-mode=transport
```

This is equivalent to `LOAD_BALANCE_MODE_P1=2`, which is not present in the
selected legacy profile.

The legacy selection contains 32 raw mlxconfig entries when the 16 individual
`MODULE_SPLIT_M0` elements are counted. All 32 are represented semantically in
the doSPCX plan. The two lane-list operations compact the 16 legacy array
entries.

There is a structural difference: the current doSPCX prepare plan places all
of these NVConfig operations in its `breakout` semantic group. Its
`post-breakout` group is empty. This does not change the parameter coverage,
but it does differ from the two sections in the legacy profile.

## Equivalent runtime parameters

The following runtime configuration is equivalent:

| Legacy setting | doSPCX XPath and value |
| --- | --- |
| Trust mode `dscp` | `/nvidia/qos trust-mode=dscp` |
| PFC bitmap `00010000` | `/nvidia/qos/pfc enabled-priorities=3` |
| Pure-L3 IPG `25` | `/nvidia/link/ipg admin=25` |
| Adaptive retransmission `true` | `/nvidia/roce/accl adaptive-retransmission=true` |
| TX window `true` | `/nvidia/roce/accl tx-window=true` |
| Slow restart `false` | `/nvidia/roce/accl slow-restart=false` |
| Slow restart idle `false` | `/nvidia/roce/accl slow-restart-idle=false` |
| Adaptive routing force `true` | `/nvidia/roce/accl adaptive-routing-force=true` |
| RP enabled | `/nvidia/cc/global-status/[rp,0..7] enabled=true` |
| NP enabled | `/nvidia/cc/global-status/[np,0..7] enabled=true` |
| Slot 0 enabled | `/nvidia/cc/algo/slot/[0] enabled=true` |
| Slot 0 counters enabled | `/nvidia/cc/algo/slot/[0] counters-enabled=true` |
| Slot 15 disabled | `/nvidia/cc/algo/slot/[15] enabled=false` |

The PFC representations are equivalent: the legacy eight-position bitmap has
priority 3 enabled, while doSPCX represents the enabled priority directly.

The following slot 0 CC parameters have the same values:

```text
param[0]  = 400
param[1]  = 6553
param[2]  = 63570
param[3]  = 69468
param[8]  = 250000
param[9]  = 524288
param[10] = 0
param[11] = 1
param[13] = 0
param[14] = 2097152
param[16] = 1
param[17] = 0
param[18] = 0
param[22] = 1
param[23] = 3
param[24] = 1
```

## Runtime tuning differences

The doSPCX plan currently used by NCO differs from the legacy profile for six
slot 0 CC parameters:

| CC parameter | Legacy value | Current doSPCX plan value |
| --- | ---: | ---: |
| `param[4]`, additive increase step size | 96 | 36 |
| `param[5]`, high additive increase step size | 1700 | 1200 |
| `param[6]`, high additive increase interval | 200000 | 7000000 |
| `param[7]`, congestion delay threshold | 13000 | 15000 |
| `param[12]`, transmit-rate decrement step | 1 | 0 |
| `param[15]`, topology awareness | 1 | 2 |

These are actual tuning differences rather than different encodings of the
same values.

## Legacy runtime settings absent from the current plan artifact

The current NCO plan artifact does not contain equivalents for:

- `ROCE_ACCL.cc_per_plane_en=1`
- `ROCE_ACCL.cc_probe_mp_mode=1`

The current source in the `dospcx-data` repository now includes
`cc-probe-mp-mode=discard-on-plane-mismatch`, which is the typed equivalent of
the legacy `cc_probe_mp_mode=1`. It also defines `param[15]=1`. Regenerating a
plan from that source should therefore restore CC probe mode and make
`param[15]` match the legacy profile.

The current `dospcx-data` source still does not contain a `cc-per-plane`
operation, so `cc_per_plane_en=1` remains an actual coverage gap.

Relevant blueprint sources:

- `dospcx-data/data/features/common/runtime/spcx-base-linkup.yaml`
- `dospcx-data/data/features/spcx/ra22/common/ra22-cc-runtime.yaml`

## Additional doSPCX runtime parameters

The doSPCX configure plan contains settings that are not present in the legacy
Spectrum-X profile:

| doSPCX setting | Notes |
| --- | --- |
| `/nvidia/link/netdev mtu=9216` | Explicit PF MTU configuration |
| `/nvidia/link/physical admin-status=down` | Link transition before IPG/runtime changes |
| `/nvidia/link/physical admin-status=up` | Link transition after IPG/runtime changes |
| `/nvidia/cc/np cnp-dscp=48` | Runtime CNP DSCP configuration |
| `/nvidia/roce/tos traffic-class=96` | Runtime RoCE traffic-class configuration |

The runtime CNP DSCP setting is not a replacement for
`ROCE_RTT_RESP_DSCP_P1=48`. They configure separate runtime and NVConfig
knobs, even though both use the value 48.

The generated doSPCX plan also contains an `eswitch` group and a
`vf-lifecycle` data-direct operation. NCO intentionally skips those groups, so
they are not part of the effective NCO configuration.

## Parameters outside this comparison

The ordinary NCO-generated raw configuration may also contain parameters such
as:

- `ATS_ENABLED`
- `CNP_802P_PRIO_P1`
- `CNP_DSCP_P1`
- `ROCE_CC_PRIO_MASK_P1`

Those parameters do not come from the referenced legacy Spectrum-X profile.
They remain separate NCO configuration inputs and are not counted as legacy
doSPCX mapping gaps here.

## Summary

- NVConfig has complete semantic coverage of the selected legacy profile, plus
  `load-balance-mode=transport`.
- Most runtime behavior maps directly to typed doSPCX XPaths.
- The current plan artifact changes six CC tuning values.
- The current plan artifact omits CC-per-plane and CC-probe mode.
- Regenerating from the current `dospcx-data` source should restore CC-probe
  mode and make topology awareness match; CC-per-plane remains missing.
- doSPCX adds explicit MTU, link-transition, CNP DSCP, and RoCE traffic-class
  operations.
