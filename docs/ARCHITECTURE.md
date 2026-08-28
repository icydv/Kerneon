# Architecture

## Process model

Kerneon is a single, non-elevated Windows GUI process. The Win32 message loop is locked to its OS thread. Collection runs on five bounded background loops; painting consumes immutable snapshot copies. There is no service, kernel driver, resident helper, browser runtime, or injected component. A user-approved power-plan experiment invokes the signed Windows `powercfg.exe` command and waits for it to exit. Optional AI analysis makes one foreground HTTPS request directly to the OpenAI Responses API.

## Data flow

1. Provider loops read Windows counters at their own cadence.
2. Each provider validates and writes its portion of a mutex-protected snapshot.
3. CPU sampling appends a synchronized history point containing the latest resource values.
4. The render loop obtains a copy, applies time-aware display smoothing, and posts a paint request.
5. The UI renders into one reusable compatible bitmap and blits it to the window in a single pass.

Collector panics are isolated at the goroutine boundary and converted into local log entries and visible provider events. A failed GPU or disk provider therefore does not stop CPU, memory, network, process, or UI operation.

## Sampling classes

| Class | Default | Source | Purpose |
|---|---:|---|---|
| Network counters | 60 Hz | `GetIfEntry2` | Smooth throughput and packet-rate deltas |
| CPU and memory | 500 ms | `GetSystemTimes`, `GlobalMemoryStatusEx`, `GetPerformanceInfo` | System load, memory, synchronized history |
| Processes, disk, GPU | 1.5 s | Toolhelp/PSAPI, PDH | More expensive enumeration and providers |
| Latency | 2 s | Windows ICMP API | Ping, jitter, loss window |
| System metadata | 30 s | Registry/native topology and volumes | Slowly changing facts |
| Visible rendering | 60 FPS | Win32/GDI | Independent presentation cadence |

Configured network rates are 10, 20, 30, 60, 90, or 120 Hz. Graph rates are 30, 60, or 120 FPS. Rates use measured elapsed time; configured timers do not appear in rate equations.

Adaptive mode reduces work while minimized or hidden to the tray: rendering 1 FPS, network 10 Hz, CPU 1 Hz, processes 0.2 Hz, and system metadata every two minutes. Game Focus detects a full-monitor foreground application, pauses Kerneon's invisible high-rate rendering, and defers metadata scans. It does not inject, hook, modify, or inspect game memory.

## Telemetry correctness

- 64-bit interface, IO, memory, and time counters are retained as 64-bit values.
- Rate calculations divide counter deltas by real monotonic elapsed time.
- Counter regression resets the baseline instead of producing a spike.
- Network auto-selection rejects loopback, disconnected, hardware-interface filtered, virtual-filter, tunnel, and duplicate WFP-style rows; an explicit adapter selection remains supported by configuration.
- Network deltas are read from one selected adapter, not summed across duplicate representations.
- Rate formatting initializes at the correct magnitude on its first sample, uses stateless formatting for peaks/tooltips, and retains boundary hysteresis for the live value so KB/MB labels do not flicker.
- Graph scaling decays slowly after peaks and maps timestamps rather than sample indices.
- Ping loss is withheld until ten samples exist.

## Retention

Live history is an in-memory ring of 36,000 synchronized samples and events are a 200-entry ring. With the default 500 ms CPU cadence, the raw capacity is roughly five hours, although views select shorter windows. Incident capture copies a recent slice so subsequent samples cannot mutate it. No telemetry database is written in 1.0.0.

## Settings and diagnostics

Settings use a versioned JSON schema, validation, legacy migration, temp-file write, flush, and atomic `MoveFileEx` replacement. Logs rotate at 1 MB with three backups. Diagnostic export is user initiated; usernames, home paths, and IPv4-like values are sanitized before packaging. Process executable paths are not exported.

## AI insight boundary

AI is optional and inactive until the user explicitly connects a copied API key and requests analysis. The key is persisted as a generic Windows Credential Manager credential; it is never serialized into Kerneon's JSON configuration or diagnostics. The request digest contains current and 60-second aggregate resource values plus up to five anonymous process metric rows (`process_1`, etc.). It omits names, executable paths, hostnames, Windows usernames, adapter identity, and IP addresses.

The Responses API request uses `store: false`, a strict JSON schema, a bounded response size, and no tools. Generated items must contain an explanation, measured evidence, next test, and confidence label. The model has no path to `runAction`, `powercfg`, process APIs, the clipboard, or other system mutation. Local deterministic insights remain available offline.

## Optimization transaction

Optimize derives findings locally. A Windows power-profile experiment is offered only when the measured signal is CPU-heavy, the baseline contains at least ten synchronized samples, High performance exists, and the active plan is not already performance/ultimate/ultra oriented. Before applying, Kerneon records the active plan GUID both in memory and in an atomic rollback journal inside settings.

The repeat run must also contain at least ten samples. Materially different CPU/GPU demand is rejected as non-comparable. A “measured improvement” requires a material reported CPU-clock increase under broadly comparable load; otherwise the result is explicitly “not proven.” Pending tests expose rollback, roll back on normal exit, and restore the prior GUID at next startup after an interrupted run. Choosing Keep clears the journal only after a measured-improvement verdict.

## Privilege model

Kerneon requests `asInvoker` and is useful without administrator rights. Protected processes may expose only their Toolhelp name/PID/thread count. Unavailable fields are zero or labelled unavailable. The only system mutation is an explicit active power-plan change through standard Windows tooling; unsupported or permission-denied devices fail closed and display the error.
