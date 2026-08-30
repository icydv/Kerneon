# Kerneon Surge

Kerneon is created and published by **Ryan Horth**.

Surge is a continuously evaluated, allowlisted session controller. It is not a generic boost button and it does not treat every busy resource as a defect.

## Product language

For any audience: Surge finds what is holding a game back, applies only a reversible response that matches the evidence, and keeps it only when the resulting frame data proves a worthwhile improvement.

For a technical audience: Surge is a closed-loop controller with typed eligibility gates, workload-comparable PresentMon proof windows, predeclared 1% low/p99/hitch thresholds, write-ahead rollback journalling, and documented Windows or vendor control surfaces.

The supporting statements used in-product are intentionally outcome-led without guaranteeing an FPS gain:

- Smoother frame delivery—not a vanity average.
- The real limiter, found before anything changes.
- Background noise contained only when it is proven relevant.
- Native CPU and GPU headroom used selectively, within supported limits.
- Placebo automatically rejected by comparable A/B evidence.
- Regressions stop themselves.
- Every session returns to its exact previous state.

## Universal game boundary

GTA V Enhanced was used as one live measurement workload during development; it is not hard-coded as Surge's supported-game boundary. Any locked executable enters the universal pipeline: dependency preflight, PresentMon frame proof, vendor telemetry, CPU/GPU/engine/latency routing, process and power-policy transaction, comparable A/B windows, and exact rollback. A game Kerneon has never seen still receives this complete path.

Verified title adapters are optional additions. They may read a documented game-owned setting or provide a guided in-game action, but the universal engine never depends on one. The first observed Rockstar profile adapter reports its existing Reflex value without guessing or writing undocumented values. New adapters register behind the same interface rather than adding game names to the controller.

## Performance Stack preflight

Surge activation requires every provider marked Required for the detected hardware. The page shows the provider, purpose, readiness and official source before activation. PresentMon is required because a paid performance controller must be able to measure regression; Kerneon monitoring remains available without it. The Windows PDH/ETW latency core is built in and adds DPC/ISR, processor queue, context-switch, paging and storage evidence. On explicit request, Kerneon uses the verified `Intel.PresentMon` winget package. NVIDIA NVAPI/NVML are supplied by the display driver; AMD ADLX and Intel IGCL are selected only on matching hardware. The official ADLX and IGCL SDKs are pinned as build references; installing a Radeon or Intel SDK on an NVIDIA user's PC would add no runtime performance.

The NVIDIA adapter reads NVML directly in-process. It exposes temperature, utilization, current/default/minimum/maximum power limits, current/maximum clocks, P-state, driver clock-limiter reasons, and a capability audit for writable clock-offset and power-limit ranges. A second provider uses public NVAPI DRS for the executable-specific profile. Its normal audit is asynchronous and cached. Ordinary Aggressive mode writes only the documented `PREFERRED_PSTATE` or `FRL_FPS` application value after a matching route and exact rollback snapshot. The separately consented Tuning Lab can step through whichever NVML controls expose a current value and explicit range in that driver session. It never writes global profiles, undocumented IDs, firmware, unreported clock offsets, or undocumented voltage/register values, and never spawns `nvidia-smi`.

Kerneon deliberately does not depend on a cache cleaner, global timer-resolution utility, generic debloat script, MSI-mode toggler, renderer injector, or undocumented ring-0 overclocking driver. Those products either duplicate documented Windows/vendor surfaces, cannot establish a causal win, add a resident workload, or create unacceptable compatibility and anti-cheat risk. RTSS-class frame limiting is not silently installed or redistributed.

## Anti-cheat boundary

The universal architecture is non-injected for every title. Kerneon does not hook DirectX/Vulkan/OpenGL, load a DLL into the game, read or write game memory, patch files, spoof input, or install a kernel driver. The locked executable is sampled with limited-query process rights rather than `PROCESS_VM_READ`.

If a recognised anti-cheat process or active Windows service is detected, the default guard records the provider and forces an external-only lane. Game priority, process QoS, memory priority, affinity repair, driver-profile writes and Kerneon's desktop HUD are suppressed. An advanced preference can disable that extra containment after the user accepts the compatibility/account risk. Doing so permits only Kerneon's normal external Windows and documented vendor controls; anti-cheat processes remain blacklisted and injection, hooking, game-memory access, file patching and kernel components remain impossible. Neither mode can guarantee third-party anti-cheat compatibility.

## Session playbooks

When Surge is enabled, a game executable is locked, and that process owns the foreground, Kerneon first records an eight-second baseline and an identity-bound crash-recovery snapshot. It can then:

1. remove execution-speed throttling only when Windows reports it active on the locked game;
2. repair a game found below Normal scheduling priority—never force High or Realtime and never raise a healthy Normal process just to appear busy;
3. repair a reduced process-memory priority;
4. repair a severe affinity restriction that exposes no more than half of the system's available logical processors;
5. reduce Kerneon's own nonessential sampling and invisible painting;
6. in Guarded mode, temporarily select High performance only after sustained CPU-heavy rather than GPU-heavy evidence on AC;
7. in twice-confirmed Aggressive mode, duplicate the current plan into an isolated session scheme and set AC CPU EPP to 0, processor minimum/maximum state to 100%, and boost mode to Aggressive. Firmware thermal, electrical, and frequency ceilings still apply;
8. temporarily move at most two stable, same-interactive-session user-space contenders to Below Normal plus EcoQoS only when long-frame evidence selects the contention route; protected, shell, security, Kerneon and anti-cheat processes are excluded;
9. on NVIDIA, transactionally request the documented per-game maximum-performance policy only for a GPU-throughput route, or a roughly 2%-below-refresh application cap only for an unstable display-ceiling route. These are separate experiments and a relaunch may be required by the driver;
10. compare predeclared 1% low, p99, hitch-rate and display-delay benefit thresholds over three workload-comparable windows. Repeated regression rolls back immediately; an experimental treatment with no repeatable material benefit is also withdrawn rather than retained as snake oil;
11. when the separately acknowledged Tuning Lab is armed, audit manufacturer-supported graphics providers. It can transact NVIDIA core/VRAM/power controls returned by NVML and Radeon manual or automatic core/VRAM clocks returned by ADLX. It baselines stock, tries profile-bounded steps, watches temperature, hardware safeguards and Windows hardware events, and keeps only a workload-comparable material frame-delivery gain. Processor and system-memory overclocking are out of scope.

Every mutation has a `prepared` JSONL record written and flushed before execution. The record includes the pain point, evidence, prior state, intended action, and rollback route. Applied, failed, verified, and restored outcomes are appended separately. Logs live under `%LOCALAPPDATA%\Kerneon\remediation` and are readable from the Surge page.

Process QoS, scheduling, affinity, memory-priority, background contender, and NVIDIA application-profile state are restored when the game leaves the foreground, exits, changes PID, is unlocked, Surge is switched off, or Kerneon closes. The active recovery file records path and process creation time as well as PID, so a recycled PID is never modified. Persistent driver values restore even if the original game process has already exited. The prior power-plan GUID and temporary scheme GUID are kept in atomic settings so interrupted runs restore the original and delete the temporary scheme at next launch.

The page changes its “What Surge can regulate and prove” catalog with the selected mode. Guarded describes evidence-gated process repair and conservative policy selection. Aggressive explicitly identifies its isolated plan, EPP 0, full processor-state request, aggressive boost request, HighQoS, scheduling ceiling, frame-proof kill switch, and exact exit restoration. CPU vendor, topology, virtualization, boost, CPPC, SMT/hybrid, and AVX2 capability evidence is shown when exposed by the processor.

Tuning Lab uses three envelopes rather than a single reckless maximum. Conservative uses the smallest steps and a 78°C discovery ceiling; Balanced uses moderate driver headroom and an 82°C ceiling; Enthusiast searches a wider—but still hard-capped and driver-bounded—range with an 84°C ceiling. “Auto with Surge” begins only after the locked game owns the foreground and only in an explicitly elevated session. A successful result remains session-only and is continuously watched; a thermal/hardware safeguard restores the captured values immediately. The module never describes “stable” as “faster,” and it never keeps a faster clock or higher power ceiling unless frame proof crosses the same predeclared material-benefit threshold.

Read [SURGE_SAFETY.md](SURGE_SAFETY.md) before using or distributing Aggressive mode.

## Proof

With the official Intel PresentMon provider, the Gaming page measures mean FPS, 1%/0.1% low, p99 and worst frame time, refresh-aware hitch rate, pacing variation, p95 present-to-display delay, dropped presents, render-completion time, and GPU-active time through ETW. The stutter classifier correlates that slow tail with Windows DPC/ISR, processor queue, page reads, physical-disk latency, memory/commit pressure and eligible background contention. A correlation is not labelled a causal driver verdict until a deeper ETW trace supports it. Render completion as a share of the whole frame distinguishes a likely GPU throughput limit from CPU/engine/frame-cap headroom without pretending to identify a specific game thread. Proof uses three fixed post-change windows and compares only windows whose render-completion workload is similar to baseline. One anomalous scene cannot trigger rollback or be called a gain; repeated material regression triggers exact rollback. An experimental treatment must cross a predeclared benefit threshold in a comparable window or it is withdrawn after the proof cycle. Kerneon still refuses to promise an FPS increase: a GPU-bound or already well-tuned system may correctly show no eligible treatment or no benefit.

PresentMon documents reduced accuracy for GPU execution timing when Hardware-Accelerated GPU Scheduling is active. Kerneon therefore treats the limiter as a signal, not a verdict, and suppresses causal proof if PresentMon reports lost ETW events or another capture warning.

The Gaming page can enable a top-left Frame HUD after a game executable is locked. It is a separate click-through Win32 overlay—not renderer injection—and is visible only while that locked process owns the foreground. PresentMon collection is per presented frame; the static HUD surface updates at 5 Hz for legibility and to avoid creating frame-pacing pressure of its own.

AI can explain telemetry but cannot add commands to the remediation allowlist or invoke a Windows mutation.
