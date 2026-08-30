# Architecture

Kerneon is created and published by **Ryan Horth**.

## Process model

Kerneon is normally a single, non-elevated Windows GUI process. The separately acknowledged Tuning Lab preview can explicitly restart that one instance elevated; the session is visibly labelled and Remote Link remains available as its paired monitoring surface. The Win32 message loop is locked to its OS thread. Collection runs on bounded background loops; painting consumes snapshot copies. There is no Kerneon service, kernel driver, browser runtime, or injected component. Surge frame proof launches the separately installed Intel PresentMon console against the locked PID and parses its ETW stream. On NVIDIA hardware, the Performance Stack dynamically calls the driver-supplied NVML runtime in-process for telemetry and capability-gated Tuning Lab controls; it does not poll by spawning `nvidia-smi`. Public NVAPI DRS application-profile reads run on a background worker and are cached by executable. Evidence-gated writes use only documented per-application values on the control loop after the prior explicit/inherited setting is flushed to the crash journal. The optional Frame HUD is a separate topmost, no-activate, click-through Win32 surface; it never enters the game process and is suppressed for recognised anti-cheat sessions while the default guard is enabled. Remote Link uses the Go HTTP server only while enabled; pairing, same-origin enforcement, local control leases and the command allowlist remain enforced during tuning. Optional AI analysis makes one HTTPS request directly to the OpenAI Responses API.

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
| Processes, disk, GPU, stutter context | 1.5 s | Toolhelp/PSAPI, PDH | Process contention, storage/GPU load, DPC/ISR, processor queue, context switches and page reads |
| Latency | 2 s | Windows ICMP API | Ping, jitter, loss window |
| System metadata | 30 s | Registry/native topology and volumes | Slowly changing facts |
| Visible rendering | 60 FPS | Win32/GDI/GDI+ | Independent graph cadence; UI motion temporarily follows monitor refresh |

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

Live history is an in-memory ring of 36,000 synchronized samples and events are a 200-entry ring. With the default 500 ms CPU cadence, the raw capacity is roughly five hours, although views select shorter windows. Incident capture copies a recent slice so subsequent samples cannot mutate it. The 0.1 technical preview writes no telemetry database.

## Settings and diagnostics

Settings use a versioned JSON schema, validation, legacy migration, temp-file write, flush, and atomic `MoveFileEx` replacement. Logs rotate at 1 MB with three backups. Diagnostic export is user initiated; usernames, home paths, and IPv4-like values are sanitized before packaging. Process executable paths are not exported.

## AI insight boundary

AI is optional and inactive until the user explicitly connects a copied API key and requests analysis. The key is persisted as a generic Windows Credential Manager credential; it is never serialized into Kerneon's JSON configuration or diagnostics. The request digest contains current and 60-second aggregate resource values plus up to five anonymous process metric rows (`process_1`, etc.). It omits names, executable paths, hostnames, Windows usernames, adapter identity, and IP addresses.

The Responses API request uses `store: false`, a strict JSON schema, a bounded response size, and no tools. Generated items must contain an explanation, measured evidence, next test, and confidence label. The model has no path to `runAction`, `powercfg`, process APIs, the clipboard, or other system mutation. Local deterministic insights remain available offline.

## Surge transaction

Surge evaluates any locked foreground executable every two seconds. The universal controller does not require a per-title entry. Optional game adapters can add verified game-owned evidence without changing the transaction semantics. Before activation, the Performance Stack requires PresentMon, the built-in Windows latency core and the native provider selected for detected hardware; missing software is shown with its purpose and official source, and activation fails closed.

The locked executable is always enumerated with limited-query access and never with `PROCESS_VM_READ`. With the default guard enabled, a positive anti-cheat process/service signal switches the session to an external-only branch before any process state is queried for mutation. That branch removes game QoS/priority/memory/affinity mutation, driver-profile writes and the desktop HUD. The user may explicitly disable this additional guard; only ordinary external controls return, while anti-cheat processes remain excluded. Kerneon globally excludes DLL injection, graphics hooks, game-memory access, anti-cheat manipulation and a kernel tuning driver in both states.

A new session observes an eight-second baseline before mutation. A typed treatment planner maps frame route, stutter diagnosis, hardware provider, mode and anti-cheat preference to explicit eligibility. The catalog repairs only detected game throttling/reduced priority/memory/affinity, can isolate at most two identity-bound same-session contenders, can create a guarded/disposable CPU policy, and can transact documented NVIDIA per-game maximum-performance or refresh-aware cap values. Healthy Normal scheduling and default unthrottled QoS are left alone. Each mutation is preceded by a flushed JSONL `prepared` record containing evidence, prior state, intended action, and rollback. Outcomes are appended separately.

Process, background contender and NVIDIA application-profile changes are restored when foreground ownership ends, the process exits/restarts, the lock changes, Surge switches off, or Kerneon closes. The crash-recovery file binds every process PID to executable path and creation time and records whether a driver value was explicit or inherited. Persistent driver state restores even after the game exits. Guarded power changes require AC power, sustained CPU pressure, lower GPU pressure, and a non-performance current plan. Aggressive first reads the effective AC EPP, processor bounds, and boost policy; if they already match, no redundant mutation occurs. Otherwise it duplicates the current scheme, adjusts only the duplicate, stores both GUIDs, restores the original, and deletes the duplicate. PresentMon compares three fixed post-change windows with the baseline and gates them by render-completion similarity. Repeated material regression withdraws early; an experimental treatment with no predeclared material benefit is also withdrawn after the proof cycle. Unlike scenes are explicitly inconclusive, and stability is never marketed as an FPS gain.

Tuning Lab is a second, separate GPU transaction. Provider discovery builds an independent capability set for GPU core, video-memory, power and curve controls; processor and system-memory tuning are out of scope. A control is direct only when the provider returns its current value, allowed range and documented write/reset path. Before the first write, `active-hardware-tuning.json` is atomically flushed with the exact original values and per-control support flags. NVML and ADLX paths revalidate every request against the same live provider and roll earlier sub-writes back if a later write fails. Discovery takes a stock PresentMon baseline, advances eligible domains through profile-bounded steps, and rejects temperature ceilings, hardware safeguards, WHEA/display reset events, unlike workloads, regression, and non-material results. Retained values remain session-only and are monitored every two seconds. Focus/process/Surge/guard/shutdown boundaries restore the snapshot; startup performs an elevated interrupted-transaction recovery check before monitoring begins.

## Motion presentation

Telemetry cadence and interaction cadence are separate. Graph sampling remains user-configured. Pointer movement, button press/release, navigation, and the Surge activation moment extend a short motion deadline; while active, repaint cadence uses the monitor refresh value reported by Windows, then returns to the configured graph rate. The Frame HUD receives per-present measurement events but composes its static numeric surface at only 5 Hz. It has no refresh-rate animation: repeatedly repainting a layered desktop window at 144/240 Hz can itself damage the independent-flip pacing being measured. Reduced Motion bypasses decorative transitions. Press feedback begins on `WM_LBUTTONDOWN`, not after the action completes.

## Privilege model

Kerneon requests `asInvoker` and is useful without administrator rights. Protected processes may expose only their Toolhelp name/PID/thread count. Unavailable fields are zero or labelled unavailable. Process QoS/scheduling mutations use standard Windows APIs, and power policy uses Windows tooling; unsupported or permission-denied actions fail closed and are logged. Tuning Lab elevation occurs only after a user clicks Restart elevated; Kerneon cannot and does not accept its own UAC consent prompt.

The current Tuning Lab preview uses the explicitly elevated one-instance process and keeps Remote Link available as a paired monitoring surface. The production hardware-control boundary should be an on-demand signed elevated broker rather than a permanently elevated UI and LAN listener. It must accept only authenticated, typed, allowlisted transactions, capture exact device/profile state before mutation, reject arbitrary shell/path/register access, and expose rollback independently of the GUI. The preview enables only documented NVML controls that expose live bounds; broader vendor clock/voltage/fan/memory mutation remains disabled until its adapter and broker path are implemented and adversarially tested.
