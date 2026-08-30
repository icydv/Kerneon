# PulseNet 1.0 audit

Kerneon is created and published by **Ryan Horth**.

The supplied archive contained a 50 KB `main.go`, `go.mod`, and a short readme. It was a compact Go/Win32/GDI network monitor built around `GetIfTable2` and Windows ICMP.

## Baseline findings

- Sampling, ping, state mutation, window lifecycle, hit testing, and all painting were concentrated in one source file.
- Counter collection and full-window rendering were coupled to a 60 Hz UI-thread timer. Expensive work could stall message handling, and a captured baseline frame briefly presented as not responding.
- Adapter enumeration was vulnerable to choosing duplicate/filter interfaces, which could misidentify or double-count traffic.
- There was no CPU, GPU, memory, storage, process, system, alert, history, or diagnostic architecture to extend.
- Settings did not have the hardened schema/migration/atomic-write path now used by Kerneon.
- There were no automated tests, version resources, product icon, release scripts, dependency notices, or measured release report.

## Measured baseline on the development PC

The original binary was built with the same portable toolchain and measured externally after stabilization.

| State | CPU | Working set | Private memory | Handles | Threads |
|---|---:|---:|---:|---:|---:|
| Visible | 1.169% | 23.62 MB | 21.05 MB | 293 | 16 |
| Minimized | 0.370% | 23.45 MB | 20.61 MB | 301 | 17 |

The baseline executable was 3,046,912 bytes. These values are point measurements on one machine, not universal performance claims.

## Replacement decision

Incrementally extending the monolith would preserve its coupled scheduling and failure domain. Kerneon therefore retains the lightweight native Go/Win32 deployment model but replaces the internal architecture, telemetry engine, visual system, settings, diagnostics, tests, and lifecycle behavior.
