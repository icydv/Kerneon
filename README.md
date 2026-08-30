<p align="center">
  <img src="assets/kerneon-icon.png" width="112" alt="Kerneon logo">
</p>

<h1 align="center">Kerneon</h1>

<p align="center">Understand the PC. Measure the problem. Keep only what helps.</p>

<p align="center">
  <a href="https://github.com/icydv/Kerneon/actions/workflows/ci.yml"><img alt="Build" src="https://github.com/icydv/Kerneon/actions/workflows/ci.yml/badge.svg"></a>
  <a href="https://github.com/icydv/Kerneon/releases"><img alt="Technical preview" src="https://img.shields.io/badge/status-technical_preview-79dce5"></a>
  <a href="LICENSE.txt"><img alt="GPL-3.0-only" src="https://img.shields.io/badge/license-GPL--3.0--only-a7b0b5"></a>
  <img alt="Windows 10 and 11 x64" src="https://img.shields.io/badge/platform-Windows_10%2F11_x64-20282d">
</p>

Kerneon is created and published by **Ryan Horth**.

> [!WARNING]
> **Kerneon 0.1.0 is a technical preview.** Monitoring is usable, but Surge, hardware tuning, Remote Link and remote screen control still need testing across a much wider hardware and browser matrix. Do not use experimental tuning on an irreplaceable system, and do not treat an observed clock change as proof of an FPS gain.

Kerneon is a native Windows performance monitor and measured session regulator designed to explain what a PC is doing without turning the answer into a wall of counters. It is a ground-up replacement for the PulseNet 1.0 prototype.

The application presents synchronized CPU, GPU, memory, storage, network, process, latency, system, event, and recent-history views. It is local-first: no account, background service, bundled driver, code injection, or administrator access is required for monitoring or ordinary Surge. Optional AI insights are generated only on request with a user-supplied OpenAI API key. The separately consented Tuning Lab requires an explicit elevated session when a supported hardware control is armed.

## The Surge promise

Surge finds the limiting factor, tests a reversible response, and keeps it only when frame data proves that it helped.

- **Smoother frames, not vanity FPS:** Surge targets 1% lows, p99 frame time and hitch rate as well as the average frame rate.
- **The real limiter first:** CPU, GPU, display-ceiling, driver-latency, paging, storage and background-contention evidence choose the control path.
- **Performance without guesswork:** a treatment is allowed only when its matching bottleneck, minimum baseline and rollback snapshot are present.
- **Placebo automatically rejected:** unlike gameplay is excluded; neutral experiments and repeated regressions are withdrawn.
- **Exact rollback:** every session change is journalled before application and restored when Surge stops, the game leaves focus, or Kerneon exits.
- **Hardware headroom must earn its place:** optional Tuning Lab steps only through driver-reported clock and power ranges, watches thermal/hardware faults, and retains a value only after comparable frame proof.

In technical terms, Surge is a closed-loop, typed treatment controller over documented Windows and vendor APIs. In everyday terms, it changes less, measures more, and can show exactly why a change deserved to stay. Results still depend on the game and the available hardware headroom; an already well-tuned or hard GPU-limited PC may correctly produce no eligible treatment.

## Install and run

Kerneon supports 64-bit Windows 10 and Windows 11. The x64 MSI installs Kerneon for all users in `C:\Program Files\Kerneon` and adds shared Start menu and public desktop shortcuts. Installation and removal require a Windows administrator approval. The executable can also be run directly as a portable preview.

Download the current preview from [GitHub Releases](https://github.com/icydv/Kerneon/releases/tag/v0.1.0-preview.1). Both the MSI and portable executable are intentionally unsigned; Windows may identify them as an unknown publisher. Download only from this repository and verify `SHA256SUMS.txt` before running a release artifact.

Launch `Kerneon.exe`. Kerneon permits one desktop instance per Windows session; launching it again restores and focuses the existing window. Closing the main window hides it to the notification area by default; right-click its tray icon to exit. See `docs\INSTALLATION.md` for installation, upgrade, removal, signing, and user-data details.

Settings are stored atomically under `%APPDATA%\Kerneon\settings.json`. Logs and user-requested diagnostic exports are stored under `%LOCALAPPDATA%\Kerneon`. A malformed settings file is preserved with a `.corrupt-<timestamp>` suffix before defaults are restored. Existing PulseNet settings are migrated once when possible.

## Highlights

- Native CPU, memory, network, volume, process, Windows-version, and topology collectors.
- Windows PDH providers for GPU engines/memory and physical-disk telemetry, with visible unavailable states.
- Counter rates calculated from actual elapsed time, reset/wrap protection, time-aware smoothing, stable units, and bounded histories.
- 10–120 Hz network counter sampling independent from the 30–120 FPS graph renderer.
- Adaptive minimized/tray behavior: 1 FPS rendering, 10 Hz network sampling, 1 s CPU sampling, and 5 s process sampling.
- Game Focus detects a true full-monitor foreground window, suspends invisible high-rate rendering, and defers metadata scans without injecting into the game.
- Overview pressure explanation, draggable graph focus ranges, click-to-pause sample contribution cards with an explicit return to live data, total/top-five application breakdowns, 60-second incident capture, per-process history and safe scheduling controls, compact view, alerts/events, and sanitized diagnostics.
- Evidence-grounded local Insights plus optional GPT-5.4 Mini analysis of a stateless, identity-free telemetry digest.
- Kerneon Surge: a universal locked-executable engine rather than a GTA-specific profile. Every game receives the same provider preflight, native hardware evidence, bottleneck routing, frame laboratory, process/power transaction, and rollback pipeline. Optional verified title adapters may add game-owned settings without becoming a requirement for unknown games.
- A visible Performance Stack preflight identifies required providers, explains why each is needed, links only to official vendor sources, and blocks Surge rather than tuning blind. Intel PresentMon can be installed on explicit request through the verified Windows Package Manager package.
- Mandatory-for-Surge Intel PresentMon ETW frame proof: mean FPS, 1% and 0.1% lows, p99/worst frame time, adaptive hitch rate, pacing variation, p95 display delay, dropped presents, render-completion time, and an honest GPU-versus-CPU/engine/cap limiter signal. The rest of Kerneon remains usable without it.
- A built-in Windows stutter and latency core correlates the long-frame tail with DPC/interrupt load, scheduler queueing, page reads, physical-disk latency, memory pressure and user-space contention. It labels correlation honestly and reserves driver attribution for a preserved ETW trace.
- Native NVIDIA NVML telemetry reads GPU power, enforced/default/minimum/maximum power limits, temperature, current/maximum graphics and memory clocks, P-state, utilization, and driver clock-limiter reasons without shelling out to `nvidia-smi`. The separately armed Tuning Lab can transact only the power/clock controls and ranges actually exposed by that driver session; unsupported controls stay unavailable.
- Optional Surge Tuning Lab uses separate versioned consent, Conservative/Balanced/Enthusiast envelopes, an on-disk exact rollback snapshot, staged PresentMon trials, temperature and NVIDIA hardware-safeguard aborts, WHEA/display-reset monitoring, and live session restoration. It is deliberately GPU/VRAM-only; processor and system-memory overclocking are outside Kerneon's scope.
- Public NVIDIA NVAPI DRS integration audits the effective application profile and can transactionally apply the documented per-game maximum-performance or frame-limit value only when the typed treatment planner proves that route is relevant. The exact explicit/inherited state is crash-journaled and restored; no undocumented Profile Inspector IDs are used.
- A native top-left Frame HUD for the locked foreground game. PresentMon collects one event per presented frame; the click-through, non-injected HUD composes its readable number at only 5 Hz so the counter does not become a frame-pacing workload. It is suppressed in recognised anti-cheat sessions while the default guard is enabled.
- Remote Link: responsive phone/tablet/desktop telemetry, Event Lens, Hardware Passport, verified Steam/Epic launches, game-process locking, and a small audited control allowlist over the LAN. Its Surge view continuously reports CPU/GPU/VRAM clocks, GPU power ceiling/draw and temperature even while Surge is off; an active session adds a cyan above-baseline dial segment, while FPS/1% low/p99/hitch improvement is labelled proved only after repeated comparable PresentMon windows. Scanning the branded QR exchanges its fragment-held credential in the actual browser and opens the paired dashboard directly; the six-digit code is only a manual fallback. The public QR rotates immediately, with a 45-second camera-preview/browser handoff grace for phones that separate their cookie stores. New pairing revokes all old device sessions. Control can be granted for 15 minutes or until explicitly revoked.
- Kerneon Session: native multi-monitor Windows capture and responsive browser rendering over WebRTC DTLS data channels, with separate local view/input grants, touch/mouse/keyboard/text control, a global sharing indicator, and immediate revocation. No Quick Assist or cloud relay is used.
- Per-monitor-v2 DPI awareness, a Windows 11 Mica backdrop, selectable restrained window opacity, monitor-refresh-rate UI motion, rounded native surfaces, double-buffered GDI/GDI+ rendering, tray lifecycle, and embedded version/icon resources.

## Build and test

Requirements: Windows 10/11 x64 and Go 1.27 or newer.

```powershell
.\scripts\test.ps1
.\scripts\build.ps1
```

`KERNEON_GO` may point to a specific `go.exe`. The checked-in `.syso` contains the application manifest, icon, and version resources, so `go-winres` is not needed for a normal build. To regenerate those resources after changing the icon, install `github.com/tc-hib/go-winres@v0.3.3` and run `scripts\regenerate-resources.ps1`.

For an extended runtime check:

```powershell
.\scripts\soak.ps1 -DurationMinutes 120 -IntervalSeconds 5
```

The QR encoder is a small linked Go module. Frame proof detects the separately installed official Intel PresentMon provider; Surge's explicit dependency action can ask Windows Package Manager to install the official `Intel.PresentMon` package. See `THIRD_PARTY_NOTICES.md`, `docs\AI_INSIGHTS.md`, `docs\OPTIMIZE.md`, `docs\PERFORMANCE_STACK.md`, `docs\SURGE_SAFETY.md`, `docs\ARCHITECTURE.md`, `docs\TESTING.md`, and `docs\KNOWN_LIMITATIONS.md`.

## Keyboard shortcuts

- `Esc`: leave a process detail or return to Overview.
- `M`: toggle compact view.
- `R`: reset network session counters.
- Type on Processes: filter by process name; Backspace edits the filter.

## Safety and privacy

Telemetry stays local by default. AI analysis is opt-in per request; Kerneon sends resource measurements and anonymized process labels, never process names, paths, hostnames, adapter names, or IP addresses. Requests use the OpenAI Responses API with `store: false`. The optional API key is stored by Windows Credential Manager, not in Kerneon's settings file.

Kerneon does not terminate processes, modify services, clear memory, silently install drivers, inject code/overlays into games, or apply registry tweak packs. Its optional Frame HUD is a separate Windows-composited, click-through window and never hooks the renderer or reads game memory. The locked executable is never opened with `PROCESS_VM_READ`. By default, a recognised BattlEye, Easy Anti-Cheat, Vanguard, EA AntiCheat, FACEIT, PunkBuster, EQU8, ACE, or GameGuard signal forces an external-only session and suppresses the HUD and Tuning Lab. An advanced user can disable that additional guard, accepting that compatibility and account safety cannot be guaranteed; this only restores Kerneon's ordinary external Windows/documented vendor controls, never injection, hooks, game-memory access, anti-cheat manipulation, or a kernel component. The dependency center performs no installation until the user explicitly chooses it. Aggressive and hardware-tuning modes may increase heat, power use and instability. NVIDIA transactions use only documented per-application DRS values or NVML controls that return a current value and explicit driver range; they do not write firmware, unsupported offsets, undocumented voltage/register values, or global profiles. Exact rollback data is recorded before mutation and restored automatically. Read `docs\SURGE_SAFETY.md` before distributing or using the stronger modes.

## Scope

Kerneon 0.1.0 deliberately labels unavailable data instead of inventing it. NVIDIA telemetry, documented application-profile transactions, and capability-gated NVML power/core/VRAM tuning are native when the installed device and driver expose those public surfaces. Radeon ADLX manual/automatic core and VRAM routes are capability-gated per card. Processor and system-memory overclocking are outside Kerneon's scope. Persistent multi-day history, per-process network/GPU attribution, privileged process actions, and high-frame-rate video/audio/file/clipboard remote transport also remain scoped out. Frame statistics—and therefore Surge activation and tuning proof—require the official PresentMon provider. AI insight generation requires API connectivity and separate OpenAI API billing. The detailed list is in `docs\KNOWN_LIMITATIONS.md`.

## Open source

Kerneon is free software licensed under **GPL-3.0-only**. You may use, study, modify and redistribute it under the terms in [`LICENSE.txt`](LICENSE.txt). Contributions are welcome through issues and pull requests; read [`CONTRIBUTING.md`](CONTRIBUTING.md) and report security-sensitive findings through [`SECURITY.md`](SECURITY.md).

Copyright © 2026 Ryan Horth.
