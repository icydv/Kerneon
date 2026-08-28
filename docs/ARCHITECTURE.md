# Architecture

## Process model

Kerneon is a single, non-elevated Windows GUI process. The Win32 message loop is locked to its OS thread. Collection runs on five bounded background loops; painting consumes immutable snapshot copies. There is no service, kernel driver, helper process, browser runtime, injected component, or network backend.

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
| Visible rendering | 30 FPS | Win32/GDI | Independent presentation cadence |

Configured network rates are 10, 20, 30, 60, 90, or 120 Hz. Graph rates are 30, 60, or 120 FPS. Rates use measured elapsed time; configured timers do not appear in rate equations.

Adaptive mode reduces work while minimized or hidden to the tray: rendering 1 FPS, network 10 Hz, CPU 1 Hz, processes 0.2 Hz, and system metadata every two minutes. Game Focus detects a full-monitor foreground application, pauses Kerneon's invisible high-rate rendering, and defers metadata scans. It does not inject, hook, modify, or inspect game memory.

## Telemetry correctness

- 64-bit interface, IO, memory, and time counters are retained as 64-bit values.
- Rate calculations divide counter deltas by real monotonic elapsed time.
- Counter regression resets the baseline instead of producing a spike.
- Network auto-selection rejects loopback, disconnected, hardware-interface filtered, virtual-filter, tunnel, and duplicate WFP-style rows; an explicit adapter selection remains supported by configuration.
- Network deltas are read from one selected adapter, not summed across duplicate representations.
- Unit formatting has boundary hysteresis to avoid KB/MB label flicker.
- Graph scaling decays slowly after peaks and maps timestamps rather than sample indices.
- Ping loss is withheld until ten samples exist.

## Retention

Live history is an in-memory ring of 36,000 synchronized samples and events are a 200-entry ring. With the default 500 ms CPU cadence, the raw capacity is roughly five hours, although views select shorter windows. Incident capture copies a recent slice so subsequent samples cannot mutate it. No telemetry database is written in 1.0.0.

## Settings and diagnostics

Settings use a versioned JSON schema, validation, legacy migration, temp-file write, flush, and atomic `MoveFileEx` replacement. Logs rotate at 1 MB with three backups. Diagnostic export is user initiated; usernames, home paths, and IPv4-like values are sanitized before packaging. Process executable paths are not exported.

## Privilege model

Kerneon requests `asInvoker` and is useful without administrator rights. Protected processes may expose only their Toolhelp name/PID/thread count. Unavailable fields are zero or labelled unavailable. The application performs no privileged mutations.
