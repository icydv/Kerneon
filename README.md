# Kerneon 1.0.0

Kerneon is a portable, native Windows performance monitor designed to explain what a PC is doing without turning the answer into a wall of counters. It is a ground-up replacement for the PulseNet 1.0 prototype.

The application presents synchronized CPU, GPU, memory, storage, network, process, latency, system, event, and recent-history views. It is local-first: no account, installer, background service, driver, code injection, or administrator access is required. Optional AI insights are generated only on request with a user-supplied OpenAI API key.

## Run

Kerneon supports 64-bit Windows 10 and Windows 11. Launch `Kerneon.exe`. Closing the main window hides it to the notification area by default; right-click its tray icon to exit.

Settings are stored atomically under `%APPDATA%\Kerneon\settings.json`. Logs and user-requested diagnostic exports are stored under `%LOCALAPPDATA%\Kerneon`. A malformed settings file is preserved with a `.corrupt-<timestamp>` suffix before defaults are restored. Existing PulseNet settings are migrated once when possible.

## Highlights

- Native CPU, memory, network, volume, process, Windows-version, and topology collectors.
- Windows PDH providers for GPU engines/memory and physical-disk telemetry, with visible unavailable states.
- Counter rates calculated from actual elapsed time, reset/wrap protection, time-aware smoothing, stable units, and bounded histories.
- 10–120 Hz network counter sampling independent from the 30–120 FPS graph renderer.
- Adaptive minimized/tray behavior: 1 FPS rendering, 10 Hz network sampling, 1 s CPU sampling, and 5 s process sampling.
- Game Focus detects a true full-monitor foreground window, suspends invisible high-rate rendering, and defers metadata scans without injecting into the game.
- Overview pressure explanation, synchronized graph crosshairs, 60-second incident capture, process list/map/detail, compact view, alerts/events, system summary, and sanitized diagnostics.
- Evidence-grounded local Insights plus optional GPT-5.4 Mini analysis of a stateless, identity-free telemetry digest.
- An Optimize workspace that captures a baseline, gates changes on a measured CPU limit, applies one reversible Windows power-plan experiment, compares a repeat run, and keeps a change only after a measured improvement.
- Per-monitor-v2 DPI awareness, a Windows 11 Mica backdrop, selectable restrained window opacity, 60 FPS display smoothing, rounded native surfaces, double-buffered GDI rendering, tray lifecycle, and embedded version/icon resources.

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

The build has no third-party runtime Go modules. See `THIRD_PARTY_NOTICES.md`, `docs\ARCHITECTURE.md`, `docs\TESTING.md`, and `docs\KNOWN_LIMITATIONS.md`.

## Keyboard shortcuts

- `Esc`: leave a process detail or return to Overview.
- `M`: toggle compact view.
- `R`: reset network session counters.
- Type on Processes: filter by process name; Backspace edits the filter.

## Safety and privacy

Telemetry stays local by default. AI analysis is opt-in per request; Kerneon sends resource measurements and anonymized process labels, never process names, paths, hostnames, adapter names, or IP addresses. Requests use the OpenAI Responses API with `store: false`. The optional API key is stored by Windows Credential Manager, not in Kerneon's settings file.

Kerneon does not terminate processes, change priority or affinity, modify services, clear memory, install drivers, inject overlays, or apply registry tweaks. Optimize can change the active Windows power plan only after an explicit click and eligible baseline. It journals the previous plan, exposes immediate rollback, restores an interrupted experiment at next startup, and automatically rolls back a pending experiment on normal exit. A result is retained only when the repeat run passes the evidence checks.

## Scope

Kerneon 1.0.0 deliberately labels unavailable data instead of inventing it. Frame-presentation statistics, temperatures, fan/power sensors, persistent history, per-process network/GPU attribution, custom notifications, and privileged process actions are not claimed. AI insight generation requires API connectivity and separate OpenAI API billing; a bundled commercial AI service is not included in this portable build. The detailed list is in `docs\KNOWN_LIMITATIONS.md`.
