# Surge safety notice

Kerneon is created and published by **Ryan Horth**.

Surge is a measured, reversible Windows session controller. It is not an overclocking guarantee, a substitute for adequate cooling, or a promise that every PC or game will gain frames.

Surge applies its universal pipeline to any locked executable; game-specific adapters are optional and never define compatibility. Before activation, the Performance Stack identifies required external providers and their official source. It performs no installation without an explicit user action. Missing proof or hardware providers block the affected control path rather than causing a blind fallback.

## What the modes do

Guarded can repair documented process-level conditions such as execution-speed throttling, reduced memory priority, a conservative scheduling preference, or a severe accidental affinity restriction. A Windows power-plan change is allowed only on AC power after a clean baseline indicates a sustained CPU-side wait rather than a GPU limit.

Aggressive is an explicit, twice-confirmed mode. It may create and activate a temporary copy of the current Windows power scheme, request AC EPP 0, request 100% minimum and maximum processor state, isolate up to two measured same-session user-space contenders, and use documented NVIDIA per-application maximum-performance or frame-limit values when the measured route qualifies. It does not raise a healthy Normal-priority game merely to appear active. Ordinary Aggressive mode never requests High or Realtime priority and does not write firmware, voltage, clock offsets, fan curves, global driver profiles, or undocumented registers.

The exact controls exposed depend on the processor and Windows environment. Kerneon records AMD/Intel/other vendor identity, topology, virtualization, and available CPU capabilities. A virtual machine is restricted to process-level controls because guest software cannot safely control host clocks.

## Optional Tuning Lab

Tuning Lab is a separate hardware-risk boundary inside Surge. It is off by default, has its own versioned and timestamped two-step acknowledgement, requires Aggressive mode, a locked foreground game, sufficient PresentMon evidence, and an explicit administrator restart. “Auto with Surge” is an additional opt-in; accepting the notice does not arm it. The default anti-cheat guard blocks hardware discovery when a recognised provider is present.

Kerneon audits GPU and video-memory providers independently. A control can arm only when the current vendor provider returns its present value, an explicit allowed range, a documented write operation and a way to restore the captured value. The NVIDIA path can independently expose core offset, video-memory offset and power ceiling through the installed driver's NVML runtime. A device that exposes only one of them receives only that one control. Voltage or a voltage/frequency curve remains unavailable unless a manufacturer-supported provider exposes bounded control and reset; Kerneon never substitutes undocumented NVAPI, SMU, register, firmware or I2C writes.

The discovery controller records an atomic recovery snapshot before the first write, measures a stock baseline, then tries small profile-bounded steps. Every step is checked against the live driver range, GPU temperature, hardware/thermal safeguard flags, WHEA events, display resets, game identity, foreground ownership and comparable 1% low/p99/hitch evidence. A regression, insufficient evidence, no material benefit, focus loss, game exit, Surge stop, Kerneon exit or guard trip restores the exact captured controls. A successful setting is session-only and continues to be watched while retained.

Processor and system-memory overclocking are not offered by Kerneon. Compatible Radeon ADLX auto-tune/undervolt surfaces remain unavailable until the adapter validates the installed driver and exact factory reset path.

## Anti-cheat compatibility

Kerneon has no renderer injection, API hook, game-memory read/write, file patch, synthetic game input, or kernel tuning driver. A locked game is never opened with `PROCESS_VM_READ`. PresentMon frame evidence is consumed from Windows ETW outside the game process.

When Kerneon sees a recognised anti-cheat process or active service, the default-on guard forces an external-only session and logs the reason. It refuses game-process QoS, priority, memory-priority, affinity and driver-profile mutation and hides its own HUD. An advanced user can disable that guard, which permits Kerneon's ordinary external Windows/documented vendor controls but never injection, hooks, game-memory access, anti-cheat process manipulation or a kernel component. Disabling it explicitly accepts compatibility and account risk; it is not a bypass and cannot guarantee that an anti-cheat vendor will permit the software. Release testing must include each publisher-supported compatibility route, and unknown providers should be treated conservatively.

## Risks and expectations

Aggressive mode can increase power use, temperature, fan noise, battery drain, or instability. Tuning Lab additionally risks crashes, freezes, data loss or corruption, overheating, reduced component life, warranty or support consequences, and permanent hardware damage. Manufacturer tools, driver-reported limits, temperature aborts and rollback reduce risk; they do not make tuning safe or guarantee recovery. Firmware, silicon, cooling, drivers, Windows policy, game design, and an existing GPU bottleneck can prevent a measurable improvement. Maintain current backups, adequate ventilation and cooling, and manufacturer-supported limits. Stop Surge if temperature, stability, fan behaviour, or power draw becomes unacceptable.

The current Tuning Lab preview restarts the one-instance application into a clearly labelled elevated hardware session. Remote Link remains available so a paired device can monitor the hardware and measured result while tuning runs; pairing, local grants, same-origin checks and the fixed command allowlist continue to apply. Production hardening should move the typed hardware-control boundary into an on-demand signed broker so the ordinary UI and LAN listener never share a permanently elevated process. The broker must authenticate its client, expose a fixed command allowlist, reject arbitrary paths/registers/commands, journal the exact previous state before execution, and restore after crash/session end. Elevation is a capability boundary, not permission to bypass anti-cheat or hardware limits.

Kerneon captures previous game/background process state, power policy and explicit/inherited NVIDIA application settings before mutation, writes a flushed JSONL decision record, restores session changes when the game loses focus or exits, and attempts interrupted-session recovery on the next launch. Experimental treatments that do not show a repeatable material benefit are withdrawn after the proof cycle. Restoration can still be prevented by external events—for example, Windows or a driver refusing an operation, files becoming unavailable, or hardware/firmware failure. Review `%LOCALAPPDATA%\Kerneon\remediation` when investigating an unexpected result.

## Commercial distribution

Present the ordinary Surge notice before a user first selects Aggressive mode. Present the separate full hardware-risk acknowledgement before Tuning Lab can be armed, and keep both available from the Surge page. Describe capabilities and limitations plainly and prominently. Do not market Surge as guaranteed FPS, universal hardware overclocking, or risk-free optimization.

This notice explains product behaviour and foreseeable risk. It is not a waiver of statutory consumer rights, does not attempt to exclude liability that cannot lawfully be excluded, and is not legal advice. Obtain jurisdiction-specific legal review before selling or broadly distributing the application.
