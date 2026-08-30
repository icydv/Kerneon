# Testing

Kerneon is created and published by **Ryan Horth**.

Run all checks from a PowerShell prompt:

```powershell
.\scripts\test.ps1
```

The suite currently contains 93 named tests: 28 deterministic core tests and 65 Windows/application tests.

Core coverage includes elapsed-time rates, zero/tiny intervals, counter reset/wrap behavior, time-aware EMA, rolling average, ring ordering/capacity, immediate rate-unit initialization, live/peak formatter isolation, unit conversion and hysteresis, graph scale decay, timestamp mapping, aggregation, malformed/versioned settings, validation, PulseNet migration, alert hysteresis/cooldown, provider failure isolation, process lifetime, human pressure explanation, optimizer eligibility, workload comparability, and proof verdicts.

Windows/application coverage validates native collectors, PDH fail-soft behavior, a real 120 Hz network run, atomic/FIFO settings, AI identity removal and request structure, 1%/0.1% frame-tail math, refresh-aware hitch detection and benefit thresholds, event classification/significance, graph-series geometry, modal child/backdrop hit ordering, CPU vendor classification, the universal unknown-game pipeline, provider readiness gating, default/optional anti-cheat policy, NVML telemetry against an installed NVIDIA runtime, NVML tuning bounds/step generation/support-selective rollback, NVAPI DRS global/application-profile reads and public-structure encoding, CPU/engine versus GPU power-route classification, typed treatment eligibility, refresh-aware cap selection, DPC-first stutter routing, stable-window refusal, protected-process contention filtering, compact high-entropy QR credentials, fragment-to-session exchange, scan-to-dashboard pairing without code entry, the bounded camera-preview/browser handoff and its expiry, credential rotation, New-pairing session revocation, strict cookies, Host validation, same-origin/local-grant enforcement, indefinite-grant revocation, arbitrary-action refusal, real Windows JPEG screen capture, separate screen/input grants, input validation, and an end-to-end Pion WebRTC data-channel negotiation. Tests never call the live OpenAI API, inject test input, mutate a live hardware tuning control, write an NVIDIA profile, or change the Windows power plan. `go vet ./...` must complete without warnings.

## Manual UI checklist

- Launch, resize, minimum size, restore, and close-to-tray lifecycle.
- Navigate every page and return with Escape.
- Confirm Overview labels and values remain aligned at minimum and default sizes.
- Hover graphs for the exact nearest series only. Click a line to pause chart playback and pin a stable timestamp/value/contribution card; verify live telemetry and Surge safety evaluation continue; use the obvious Resume live control or Escape. Drag a real distance to focus and double-click/reset to clear the range.
- Type, erase, and clear a process filter; switch list/map; open and close a process detail; copy PID/path; open location only on an accessible process.
- Toggle compact view with the button and `M`.
- Capture 60 seconds and inspect History.
- Cycle each settings choice, restart, and confirm persistence.
- Corrupt a disposable settings copy and confirm preservation/default recovery.
- Confirm GPU/disk/latency unavailable states are honest with the provider disabled or disconnected.
- Open Surge's Performance Stack. Confirm each required dependency shows its purpose, readiness and official source; on a disposable machine without PresentMon, verify Surge is blocked and the explicit Install action invokes the exact `Intel.PresentMon` winget package. Confirm an NVIDIA system displays live NVML temperature, power, clocks and limiter reasons plus the cached DRS policy without a periodic UI/game-loop hitch.
- Lock an executable with no title adapter and confirm it still receives the universal frame/provider/routing/contention/rollback pipeline. A verified adapter may add evidence, but removing it must not disable Surge compatibility.
- With each supported anti-cheat lab fixture, confirm the default guard records `anti-cheat-boundary`, skips game/profile mutations and hides the HUD. Confirm disabling the guard requires two clicks, shows the account-risk warning, never targets the anti-cheat process, and still loads no module into the game. Re-enable it and verify the active session restores immediately. Re-run against publisher-supported protected test environments before every public release; never use a production account for mutation testing.
- Confirm Insights remains useful without AI; connect a disposable copied API key, generate, verify source/confidence/evidence labels, then disconnect and confirm the key is absent from `settings.json`.
- With a disposable game and background process, verify Surge writes `active-session.json` before mutation, leaves healthy Normal priority/default QoS untouched, isolates only a qualifying same-session contender, and restores process/QoS state on every boundary. Using a disposable NVIDIA application profile, verify explicit and inherited `PREFERRED_PSTATE`/`FRL_FPS` values apply only for their matching routes and restore exactly after a simulated game exit. On a disposable power plan, verify Guarded gating and Aggressive duplicate/activate/restore/delete behavior. Do not run mutation tests on production workloads or profiles.
- On a disposable, adequately cooled machine with current backups, open Tuning Lab and verify ordinary Surge consent does not arm it. Read and complete the separate two-click hardware-risk acknowledgement, confirm Auto with Surge still starts off, and verify an unsupported control remains visibly unavailable. In an explicit elevated session, confirm Remote Link remains available for paired monitoring while unpaired requests, cross-origin commands and commands without a local control lease remain rejected. Capture the provider's exact original values, test Conservative/Balanced/Enthusiast target bounds without crossing the driver range, and simulate insufficient PresentMon data, unlike workload, regression, temperature ceiling, NVIDIA safeguard, focus loss, game exit, Surge stop, process exit and application restart. Every case must restore the exact snapshot and leave an auditable prepared/outcome record. Hardware mutation testing must never run on a production account or irreplaceable system.
- Verify opening Tuning Lab with Surge off always takes a fresh stock estimate. With Surge already on, verify a completed stock estimate is reused unchanged and a missing estimate remains unavailable until the user explicitly pauses Surge; boosted clocks, temperatures and power must never become a replacement baseline.
- Lock a borderless game, enable Frame HUD, and confirm the native card appears only while that exact process is foreground, remains click-through, shows PresentMon FPS/1% low, follows DPI/monitor changes, and disappears immediately on unlock/disable. Verify PresentMon collection remains per-frame while the desktop counter repaints at only 5 Hz, without injecting a module into the game or changing presentation mode.
- Feed comparable and deliberately unlike frame windows into the proof tests. Confirm one regression never rolls back, two repeated comparable material regressions do, a neutral experimental treatment is withdrawn after the proof cycle, a threshold-crossing treatment is retained, different render-completion workloads remain inconclusive, and an already matching Aggressive CPU policy creates no duplicate scheme.
- Enable Remote Link on an isolated private network; scan the branded QR from the rendered application and verify it opens the paired dashboard without code entry. Confirm the integrated logo does not compromise scanning at practical angles/distances. With Surge off, verify the remote dials continue to update clocks, power and temperature and the result card explicitly has no active claim. Start a measured Surge session and verify each cyan continuation begins at its frozen pre-treatment baseline; natural readings below baseline remain neutral. Confirm the result card stays at “No claim yet” until repeated comparable PresentMon windows earn proof, and that unlike or incomplete windows never become a performance claim. Repeat the token exchange with separate preview and browser cookie jars inside 45 seconds; both must receive independent sessions and land on the dashboard, while the same token must fail after grace expiry. Pair two disposable browsers and verify the displayed QR rotates. Select New pairing and confirm both existing browsers immediately receive 401/lose their WebRTC peers. Verify manual-code Reveal/Hide, five-attempt throttling, eight-hour device authentication, Off/15-minute/Until-revoked general control, verified-game allowlist, audit records, Host/Origin rejection, and the absence of file/shell endpoints.
- On phone, tablet, and desktop browsers, connect Kerneon Session; verify display selection, view-only mode, touch/mouse coordinates, physical keyboard, mobile text entry, separate input grant, global sharing pill/tray indicator, immediate revoke, peer teardown on New pairing, and refusal to capture UAC secure desktop. Repeat at minimum/default app sizes and portrait/landscape browser layouts.
- At 60 Hz and a high-refresh monitor, inspect hover, press, page transition, and Surge activation cadence. Confirm Reduced Motion removes decorative motion while controls retain immediate state feedback.
- Close to tray, restore from the tray icon, and exit from the tray icon.

## Soak test

`scripts\soak.ps1` records process CPU, working/private memory, handles, threads, GDI objects, responsiveness, and window visibility to CSV. The default is two hours. It terminates only the Kerneon process it started.

```powershell
.\scripts\soak.ps1 -DurationMinutes 120 -IntervalSeconds 5
```

Review for monotonic handle/GDI/memory growth, unresponsiveness, or CPU regression. A shorter release-candidate run may validate the harness, but it is not represented as a two-hour soak.
