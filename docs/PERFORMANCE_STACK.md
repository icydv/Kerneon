# Kerneon Performance Stack

Kerneon is created and published by **Ryan Horth**.

Surge selects capabilities by hardware and evidence. It does not install every available utility on every PC.

| Capability | Provider | Runtime status | What it adds |
|---|---|---|---|
| Frame delivery and latency proof | Intel PresentMon | Required for Surge; explicit official winget install | Per-present FPS, slow-frame tail, display/render timing and regression proof across DirectX, OpenGL and Vulkan |
| Stutter context | Windows PDH and ETW/WPR | Built into Windows | DPC/ISR, processor queue, context switches, page reads, storage latency and bounded deep-trace foundation |
| NVIDIA hardware/profile path | Driver-supplied NVML and NVAPI | Required only on detected NVIDIA hardware | Power, clock, thermal and limiter evidence plus transactional documented per-game power/cap values |
| AMD hardware/profile path | AMD ADLX | Required only on supported Radeon hardware; direct adapter | Live telemetry, manual GPU/VRAM clock ranges where exposed, per-silicon automatic GPU/VRAM clock discovery, and Reset to Factory |
| Intel hardware/profile path | Intel IGCL | Required only on supported Intel graphics; adapter in development | Telemetry, frequency, temperature, power and supported control ranges exposed by the official driver API |
| Privileged mutation | Kerneon elevated broker | Planned; not enabled in this build | Authenticated allowlisted transaction, exact snapshot, watchdog and independent rollback |

The current build pins the official SDK references at NVIDIA NVAPI `cd6918f60b3c9a0476fdfe7e89bb32330602049d`, AMD ADLX `d9f04a9bba022d6cf6333f005dd540b4ad19fb63`, and Intel IGCL `b6c462933502e13d1537dd5024949a51be30e63d`.

## Deliberate exclusions

- Cache/standby-list cleaners are excluded: reclaiming useful cache can create more I/O and does not establish a durable frame-time win.
- Global timer-resolution tools are excluded: modern games can request their own timer characteristics, while a permanent system-wide request has power and scheduler trade-offs.
- Generic debloat and registry packs are excluded: hardware, Windows versions and user workloads differ, and rollback/causal proof is usually inadequate.
- MSI-mode togglers and undocumented register tools are excluded: device/driver compatibility is not universal and a mistaken write can destabilise or disable hardware.
- Renderer injectors and hidden overlays are excluded: they add anti-cheat and compatibility exposure. RTSS-class limiting remains a possible explicit, licensed optional integration only after a title/provider compatibility policy exists.
- Processor overclocking and third-party ring-0/register tools are excluded from Kerneon's tuning surface.

## Responsiveness model

Average FPS is not the objective function. Surge separately evaluates 1% and short-window 0.1% lows, p99/worst frame time, refresh-relative hitches, frame-time variation, present-to-display delay, render completion, dropped presents, DPC/ISR pressure, scheduling queue, paging, storage latency and background contention. Experimental changes must cross a predeclared 1% low, p99, hitch-rate or display-delay benefit threshold in workload-comparable windows. Repeated regression rolls back early; a neutral experimental result is also withdrawn. Stable or inconclusive evidence is never marketed as a gain.

## Anti-cheat model

All games use a non-injected base architecture. Kerneon does not hook graphics APIs, load into the game, read/write game memory, patch files, spoof input or install a kernel tuning driver. By default a recognised anti-cheat signal forces an external-only session and suppresses the HUD, game-process mutations and driver-profile writes. The user may explicitly disable that extra guard and accept the compatibility/account risk, but only Kerneon's ordinary external controls return and anti-cheat processes stay excluded. This is a containment preference, not a guarantee about third-party anti-cheat behaviour; release testing must follow each game publisher's supported route.
