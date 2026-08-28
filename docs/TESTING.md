# Testing

Run all checks from a PowerShell prompt:

```powershell
.\scripts\test.ps1
```

The suite currently contains 24 named tests: 19 deterministic core tests and five Windows/application integration tests.

Core coverage includes elapsed-time rates, zero/tiny intervals, counter reset/wrap behavior, time-aware EMA, rolling average, ring ordering/capacity, unit conversion and hysteresis, graph scale decay, timestamp mapping, aggregation, malformed/versioned settings, validation, PulseNet migration, alert hysteresis/cooldown, provider failure isolation, process lifetime, and human pressure explanation.

Windows/application integration coverage validates native memory/system counts, CPU topology, fixed volumes, interface enumeration, process enumeration, PDH fail-soft behavior, a real 120 Hz network run, adaptive reduction toward 10 Hz, atomic settings round-trip, and malformed-file preservation. `go vet ./...` must complete without warnings.

## Manual UI checklist

- Launch, resize, minimum size, restore, and close-to-tray lifecycle.
- Navigate every page and return with Escape.
- Confirm Overview labels and values remain aligned at minimum and default sizes.
- Hover graphs for a synchronized timestamp/value crosshair.
- Type, erase, and clear a process filter; switch list/map; open and close a process detail; copy PID/path; open location only on an accessible process.
- Toggle compact view with the button and `M`.
- Capture 60 seconds and inspect History.
- Cycle each settings choice, restart, and confirm persistence.
- Corrupt a disposable settings copy and confirm preservation/default recovery.
- Confirm GPU/disk/latency unavailable states are honest with the provider disabled or disconnected.
- Close to tray, restore from the tray icon, and exit from the tray icon.

## Soak test

`scripts\soak.ps1` records process CPU, working/private memory, handles, threads, GDI objects, responsiveness, and window visibility to CSV. The default is two hours. It terminates only the Kerneon process it started.

```powershell
.\scripts\soak.ps1 -DurationMinutes 120 -IntervalSeconds 5
```

Review for monotonic handle/GDI/memory growth, unresponsiveness, or CPU regression. A shorter release-candidate run may validate the harness, but it is not represented as a two-hour soak.
