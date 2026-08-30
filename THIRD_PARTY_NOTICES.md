# Third-party notices

Kerneon is created and published by **Ryan Horth**.

Kerneon's shipped executable links the Go modules listed below. It never silently downloads an executable component. The visible Surge dependency center can, after an explicit user action, invoke Windows Package Manager for the separately licensed official PresentMon package or open an official vendor driver page.

## go-qrcode

`github.com/skip2/go-qrcode` is used to generate the high-error-correction Remote Link pairing matrix. It is licensed under the MIT license.

## Intel PresentMon

Frame proof detects the separately installed official Intel PresentMon service/console and consumes its ETW CSV stream. PresentMon is not embedded in the Kerneon executable or release ZIP. If it is missing, Kerneon can invoke `winget install --id Intel.PresentMon --exact` only after the user selects Install. Its license and third-party notices remain in the Intel installation directory.

## NVIDIA NVML and NVAPI

On an NVIDIA system Kerneon dynamically detects `nvml.dll` and `nvapi64.dll` supplied by the installed NVIDIA display driver. Kerneon does not redistribute those proprietary libraries. The NVML adapter performs read-only telemetry and Tuning Lab capability calls for utilization, temperature, power, clocks, P-state, control limits, limiter reasons, clock-offset ranges and power-limit ranges; merely opening Surge never writes a driver setting. After separate risk consent, explicit arming and elevation, Tuning Lab can call only the public NVML clock-offset and power-limit setters supported by that device/driver, with exact session rollback. It does not redistribute or use an undocumented NVIDIA overclocking library.

The public NVIDIA NVAPI SDK headers from the MIT-licensed NVIDIA `nvapi` repository were used as the authoritative build reference for DRS interface identifiers, structures, versions, and the `PREFERRED_PSTATE_ID` enum. The validated reference revision is `cd6918f60b3c9a0476fdfe7e89bb32330602049d`; those headers and import libraries are not embedded in the Kerneon executable. Kerneon queries the driver-supplied runtime dynamically.

## AMD ADLX

The official AMD ADLX SDK repository is pinned as a build/reference dependency at revision `d9f04a9bba022d6cf6333f005dd540b4ad19fb63`. It documents the Radeon performance-monitoring, manual GPU/VRAM clock-range, per-silicon automatic GPU/VRAM tuning, auto-undervolt and Reset to Factory interfaces used by Kerneon's in-process adapter. Kerneon dynamically loads the `amdadlx64.dll` supplied by a compatible AMD display driver and does not redistribute that proprietary runtime or install it on non-Radeon PCs. The SDK source/reference is governed by AMD's `ADLX SDK License Agreement.pdf`.

## Intel Graphics Control Library

The official Intel Graphics Control Library repository is pinned as a build/reference dependency at revision `b6c462933502e13d1537dd5024949a51be30e63d`. Its headers, wrapper and samples document Intel telemetry, frequency, power, temperature and supported overclocking surfaces. IGCL runtime binaries are supplied by compatible Intel graphics drivers; Kerneon does not install them on unrelated hardware. The current executable does not yet link or redistribute IGCL.

## Windows Performance facilities

The stutter/latency core reads the PDH and Event Tracing for Windows facilities included with Windows. `wpr.exe` is detected as the official deep-trace foundation. The Windows ADK/Performance Analyzer is a developer diagnostic tool, not a runtime performance dependency, so Kerneon does not install the full ADK on customer machines.

## klauspost/cpuid

`github.com/klauspost/cpuid/v2` is used for native processor identity and feature evidence in Hardware Passport. It is licensed under the MIT license.

## Pion WebRTC

`github.com/pion/webrtc/v4` version 4.2.19 and its Pion datachannel, DTLS, ICE, interceptor, logging, mDNS, randutil, RTCP, RTP, SCTP, SDP, SRTP, STUN, transport, and TURN modules provide the Kerneon Session peer connection and encrypted DTLS data channels. The linked Pion modules are licensed under the MIT license.

Pion's linked support modules include `github.com/google/uuid` (BSD-3-Clause), `github.com/wlynxg/anet` (BSD-3-Clause), and the `golang.org/x/crypto`, `x/net`, `x/sys`, and `x/time` modules (BSD-style Go project licenses).

## Go

The Go toolchain and standard library are copyright The Go Authors and distributed under a BSD-style license. The toolchain is used to build Kerneon; its full source and license are available from the Go project.

## go-winres

`github.com/tc-hib/go-winres` version 0.3.3 is an optional build-time utility used to compile the Windows manifest, icon, and version metadata into `rsrc_windows_amd64.syso`. It is licensed under the Zero-Clause BSD license. The utility and its own executable are not embedded in or distributed with Kerneon.

## WiX Toolset

WiX Toolset 3.14 is used at build time to create the Windows Installer package. The WiX command-line tools are not installed by the Kerneon MSI. Kerneon's installer tables and branded artwork contain no background updater, service, driver, analytics client, or cloud component.

## Visual asset provenance

The final product icon is reproducibly rendered from `cmd/icon/main.go` using the Go standard library. An earlier AI-generated concept was rejected and is not part of the product or release package.
