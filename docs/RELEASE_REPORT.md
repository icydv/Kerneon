# Kerneon 1.0.0 release report

Release candidate built and verified on 28 August 2026.

## Release identity

| Item | Value |
|---|---|
| Product | Kerneon 1.0.0 portable x64 |
| Executable | `Kerneon.exe` |
| Size | 4,180,992 bytes (3.99 MiB) |
| SHA-256 | `86E05830733F6C55BD4BE5050394C6B758D1D10FC793F1FC5437195F6C352ACC` |
| Toolchain | Go 1.27.0, `CGO_ENABLED=0`, `GOARCH=amd64`, trimmed paths and symbols |
| Runtime dependencies | Windows system DLLs only; no third-party Go modules |
| Privilege | `asInvoker`; no administrator requirement |
| Signature | Unsigned; no Authenticode certificate was supplied |

The executable contains its GUI manifest, application icon, file/product version 1.0.0, product name, description, copyright, and original filename.

## Verification environment

- Windows 11 Pro 10.0.26200 (build 26200), x64.
- AMD Ryzen 7 5800X: 8 physical cores, 16 logical processors.
- 31.9 GB installed memory.
- NVIDIA GeForce RTX 2080 SUPER, driver 32.0.15.9159.
- Connected Intel Wi-Fi adapter selected automatically; WFP/filter duplicates excluded.
- Default Kerneon settings unless a test explicitly states 120 Hz or minimized mode.

Results describe this machine and build. They are not universal performance guarantees.

## Automated verification

`scripts/test.ps1` completed successfully:

- 24/24 named tests passed.
- 19 deterministic core tests passed.
- Five Windows/application integration tests passed.
- `go vet ./...` completed with no warnings.
- A real 1.5-second 120 Hz network run remained within the practical timer tolerance.
- Enabling adaptive low-power state reduced that collector toward 10 Hz.
- CPU/memory/topology, volume, interface, process, disk/GPU PDH, settings atomicity, and malformed settings recovery checks passed.
- Provider failure isolation, alert hysteresis/cooldown, counter resets/wraps, zero/tiny elapsed time, unit hysteresis, graph time mapping/scaling, aggregation, and history lifetime checks passed.

The build command executes tests and static analysis before producing the executable. The final binary reports only the local `kerneon` module and standard toolchain metadata.

## External performance measurements

CPU is external process CPU normalized across 16 logical processors. Memory and object counts are read from the Windows process object. Each table is a separate process run.

| Scenario | Duration / samples | Responsive | Average CPU | Max CPU | Average working set | Average private | Handle range | GDI range |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| Visible Overview | 2 min / 59 | 59/59 | 0.671% | 1.016% | 34.24 MB | 29.55 MB | 421–444 | 55–59 |
| Minimized, cold/warm-up | 2 min / 59 | 59/59 | 0.207% | 0.825% | 32.12 MB | 29.31 MB | 389–444 | 55–59 |
| Minimized, adaptive | 5 min / 60 | 60/60 | 0.032% | 0.117% | 31.67 MB | 29.08 MB | 395–426 | 55–55 |

During the five-minute run, average handle count by minute was 405.5, 415.3, 420.8, 421.4, and 421.2. This plateau and the perfectly flat GDI count do not indicate monotonic leakage over the observed period. Average private memory by minute was 28.54, 28.75, 29.08, 29.38, and 29.63 MB; the run is too short to make a long-term leak claim.

The included soak harness defaults to two hours. Only the two- and five-minute release-candidate runs above were performed here; they must not be represented as completion of the full two-hour soak.

## PulseNet baseline comparison

The supplied PulseNet 1.0 binary was rebuilt and measured on the same PC before replacement.

| State | PulseNet CPU | Kerneon CPU | PulseNet working set | Kerneon working set | Interpretation |
|---|---:|---:|---:|---:|---|
| Visible | 1.169% | 0.671% | 23.62 MB | 34.24 MB | Kerneon used about 43% less CPU while collecting substantially more telemetry; memory increased by 10.62 MB. |
| Minimized | 0.370% | 0.207% warm-up / 0.032% settled | 23.45 MB | 32.12 / 31.67 MB | Adaptive collection and 1 FPS rendering reduced background CPU; memory increased by roughly 8–9 MB. |

PulseNet was 3,046,912 bytes; Kerneon is 4,180,992 bytes. The increase covers native system/process/storage/GPU providers, hardened settings/logging, diagnostics, full navigation, histories, process detail, tray/compact lifecycle, and embedded product resources.

## Startup and lifecycle

Five launches reached a non-zero responding main window in 78.1, 30.1, 30.5, 30.8, and 30.6 ms. The warm median was 30.6 ms; the first measured launch was 78.1 ms.

The release lifecycle check verified:

- the main window was responding before close;
- Close to tray kept the process and hidden window alive;
- tray left-click restored a visible window;
- tray right-click exited the process;
- settings persisted across a normal shutdown.
- Compact changed the outer frame from 1240 × 800 to 430 × 270 and Expand restored exactly 1240 × 800 without changing the saved normal size.

## Visual and interaction QA

Automated Win32 input plus off-screen `PrintWindow` capture was used to inspect Overview, Processes list, process detail, Gaming, and Settings without relying on desktop screenshots. The inspected default window was 1240 × 800 at the development monitor's active 100% scale.

Verified behavior includes page navigation, stable card/grid alignment, native brand/icon presence, readable empty/unavailable values, process filter/list/map/detail interactions, process copy/open-location action placement, Overview graph population, settings layout, compact toggle, 60-second incident capture path, resize constraints, and dark-title-bar integration.

The application is per-monitor-v2 DPI aware and responds to `WM_DPICHANGED`, but additional physical DPI/multi-monitor combinations were not available for this release run. Physical sleep/resume was likewise not automated; the baseline reset path is implemented and unit behavior around resets is covered.

## Correctness and safety notes

- Counter differences use actual elapsed time and reject invalid intervals.
- Counter regression or resume resets a baseline instead of emitting a false spike.
- Interface auto-selection monitors one physical connected adapter and rejects common duplicate/filter/tunnel rows.
- Ping loss is not displayed as meaningful before ten observations.
- GPU/disk provider errors are visible and isolated from other collectors.
- Settings are versioned, validated, migrated, flushed, and atomically replaced.
- Diagnostic export is user initiated and sanitizes username, home path, and IPv4-like values.
- Process actions in 1.0.0 are read-only. No critical-process termination or scheduling mutation surface exists.
- Game Focus uses full-monitor foreground-window detection only; it performs no injection or game-memory access.

## Honest release boundaries

Frame presentation, temperatures/power/fans, process GPU/network attribution, privileged process mutations, persistent long-term history, custom Windows notifications, and a production anti-cheat-reviewed overlay are not implemented and are not simulated. See `docs/KNOWN_LIMITATIONS.md` for the complete list.

## Release disposition

The portable x64 build is suitable as a local Kerneon 1.0.0 release candidate. Automated tests, static analysis, short soak, startup, tray lifecycle, and primary visual paths passed. A wider hardware/DPI matrix, code signing, physical sleep/resume, and the included two-hour soak remain appropriate before broad public distribution.
