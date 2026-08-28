# Kerneon 1.0.0 release report

Release candidate built and verified on 28 August 2026.

## Release identity

| Item | Value |
|---|---|
| Product | Kerneon 1.0.0 portable x64 |
| Executable | `Kerneon.exe` |
| Size | 7,843,840 bytes (7.48 MiB) |
| SHA-256 | `7493B2318F22BBB9A97C6E0169C9ED76274520279289DAAABE968FDB08018AA0` |
| Toolchain | Go 1.27.0, `CGO_ENABLED=0`, `GOARCH=amd64`, trimmed paths and symbols |
| Runtime dependencies | Windows system DLLs only; no third-party Go modules. Optional AI uses Go's standard HTTPS client. |
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

- 30/30 named tests passed.
- 23 deterministic core tests passed.
- Seven Windows/application integration tests passed.
- `go vet ./...` completed with no warnings.
- A real 1.5-second 120 Hz network run remained within the practical timer tolerance.
- Enabling adaptive low-power state reduced that collector toward 10 Hz.
- CPU/memory/topology, volume, interface, process, disk/GPU PDH, settings atomicity, and malformed settings recovery checks passed.
- Provider failure isolation, alert hysteresis/cooldown, counter resets/wraps, zero/tiny elapsed time, immediate rate magnitude, peak/live formatting isolation, unit hysteresis, graph time mapping/scaling, aggregation, history lifetime, optimizer gating, workload comparability, and proof verdict checks passed.
- The AI tests verify the telemetry digest excludes process/network identity and that the request is stateless with strict Structured Outputs. They use a local mock server; no live API request or billing occurred.

The build command executes tests and static analysis before producing the executable. The final binary reports only the local `kerneon` module and standard toolchain metadata.

## External performance measurements

CPU is external process CPU normalized across 16 logical processors. Memory and object counts are read from the Windows process object. Each table is a separate process run.

| Scenario | Duration / samples | Responsive | Average CPU | Max CPU | Average working set | Average private | Handle range | GDI range |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| Visible Overview, 60 FPS | 1 min / 29 | 29/29 | 0.796% | 0.969% | 33.15 MB | 62.32 MB | 400–429 | 37–38 |
| Minimized, adaptive | 1 min / 29 | 29/29 | 0.025% | 0.146% | 32.63 MB | 62.19 MB | 392–410 | 37–37 |

The minimized run's perfectly flat GDI count and bounded handle range do not indicate resource leakage over the observed minute. The larger private virtual allocation versus the earlier build comes from including Go's standard TLS/HTTP stack for optional AI; working set remained near 33 MB. These short runs cannot establish long-term leak behavior.

The included soak harness defaults to two hours. Only the one-minute final-build runs above and earlier short pre-feature runs were performed here; they must not be represented as completion of the full two-hour soak.

## PulseNet baseline comparison

The supplied PulseNet 1.0 binary was rebuilt and measured on the same PC before replacement.

| State | PulseNet CPU | Kerneon CPU | PulseNet working set | Kerneon working set | Interpretation |
|---|---:|---:|---:|---:|---|
| Visible | 1.169% | 0.796% | 23.62 MB | 33.15 MB | Kerneon used about 32% less CPU at a 60 FPS default while collecting substantially more telemetry; working set increased by 9.53 MB. |
| Minimized | 0.370% | 0.025% | 23.45 MB | 32.63 MB | Adaptive collection and 1 FPS rendering reduced background CPU by about 93%; working set increased by 9.18 MB. |

PulseNet was 3,046,912 bytes; Kerneon is 7,843,840 bytes. The increase covers native system/process/storage/GPU providers, hardened settings/logging, diagnostics, full navigation, histories, process detail, tray/compact lifecycle, optimizer transaction safety, standard-library TLS/HTTP support, and embedded product resources.

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

Automated Win32 input plus off-screen `PrintWindow` capture was used to inspect Overview, Processes list, process detail, Gaming, Insights, Optimize, and Settings without relying on desktop screenshots. The inspected default window was 1240 × 800 at the development monitor's active 100% scale.

Verified behavior includes page navigation, stable card/grid alignment, the pressure-arc Kerneon monogram and embedded icon, Overview live signature, readable empty/unavailable values, process filter/list/map/detail interactions, process copy/open-location action placement, Insights privacy disclosure, Optimize's evidence-closed state on an existing Ultra Performance plan, 60-second incident capture, compact mode, material/opacity settings, resize constraints, and dark-title-bar integration.

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
- AI is explicit, stateless, identity-minimized, key-protected by Windows Credential Manager, and advisory only; it has no system tools.
- Optimize rejects performance-oriented current plans, weak/non-CPU baselines, inadequate samples, and non-comparable repeats. A pending test has a persisted rollback GUID and is restored on exit or next startup.

## Honest release boundaries

Frame presentation, temperatures/power/fans, process GPU/network attribution, privileged process mutations, persistent long-term history, custom Windows notifications, a bundled commercial AI service, broad automatic system tuning, and a production anti-cheat-reviewed overlay are not implemented and are not simulated. See `docs/KNOWN_LIMITATIONS.md` for the complete list.

## Release disposition

The portable x64 build is suitable as a local Kerneon 1.0.0 release candidate. Automated tests, static analysis, short soak, startup, tray lifecycle, and primary visual paths passed. A wider hardware/DPI matrix, code signing, physical sleep/resume, and the included two-hour soak remain appropriate before broad public distribution.
