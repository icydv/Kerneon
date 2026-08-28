# Kerneon 1.0.0

Kerneon is a portable, native Windows performance monitor designed to explain what a PC is doing without turning the answer into a wall of counters. It is a ground-up replacement for the PulseNet 1.0 prototype.

The application presents synchronized CPU, GPU, memory, storage, network, process, latency, system, event, and recent-history views. It is local-first: no account, cloud service, installer, background service, driver, code injection, or administrator access is required.

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
- Per-monitor-v2 DPI awareness, dark title bar, rounded Windows 11 frame, double-buffered GDI rendering, tray lifecycle, and embedded version/icon resources.

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

Kerneon is read-only in 1.0.0. It does not terminate processes, change priority or affinity, modify services, alter power plans, clear memory, install drivers, inject overlays, or apply registry tweaks. It makes ICMP echo requests only to the configured ping target when latency monitoring is enabled. No telemetry is uploaded.

## Scope

Kerneon 1.0.0 deliberately labels unavailable data instead of inventing it. Frame-presentation statistics, temperatures, fan/power sensors, persistent history, per-process network/GPU attribution, custom notifications, and privileged process actions are not claimed. The detailed list is in `docs\KNOWN_LIMITATIONS.md`.
