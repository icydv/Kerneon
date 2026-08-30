# Kerneon 0.1.0-preview.1 release report

Kerneon is created and published by **Ryan Horth** and released under
`GPL-3.0-only`.

This build is a public technical preview, not a production release. It is
unsigned because the project does not currently have an Authenticode
certificate. Windows may therefore show a reputation warning.

## Release identity

| Item | Value |
|---|---|
| Version | `0.1.0-preview.1` |
| Platform | Windows x64 |
| Portable executable | `Kerneon-0.1.0-preview.1-portable.exe` |
| Installer | `Kerneon-0.1.0-preview.1-x64.msi` |
| Toolchain | Go 1.27, `CGO_ENABLED=0`, trimmed paths and symbols |
| Runtime privilege | Starts as the current user; protected operations require explicit Windows elevation |
| Telemetry | None |
| Network service | Remote Link is off by default and listens only when the user enables it |

Release downloads include `SHA256SUMS.txt`. Verify a download in PowerShell
with:

```powershell
Get-FileHash .\Kerneon-0.1.0-preview.1-portable.exe -Algorithm SHA256
```

## Verification performed

The tagged source must pass the following gates:

- `go mod verify`;
- `go test -count=1 ./...`;
- `go vet -unsafeptr=false ./...`;
- a clean Windows GUI portable build;
- MSI creation and Windows Installer database validation;
- embedded icon, product name, version, copyright, and GPL metadata checks;
- SHA-256 checksums generated after the final build; and
- the Windows GitHub Actions workflow on the published commit.

The `unsafeptr` vet analyser is disabled because Kerneon's optional ADLX FFI
bridge reconstructs opaque vendor handles returned as `uintptr`. All other
standard vet analysers remain enabled. This is an explicit review item, not a
claim that unsafe code is risk-free.

## Performance evidence

Kerneon reports measurements and preserves a rollback trail for system changes.
It does **not** promise a universal FPS uplift. Performance results depend on the
game, bottleneck, hardware, drivers, temperatures, power limits, and background
workload.

Early live testing was conducted on one Ryzen 7 5800X / GeForce RTX 2080 SUPER
system. That testing exercised game-process selection, frame-time observation,
Surge lifecycle and restoration, Remote Link monitoring, and the stutter guard.
It is useful engineering evidence but not a statistically controlled product
benchmark. No percentage uplift from that session is advertised.

A defensible comparison requires the same game route, graphics settings,
resolution, driver state, temperature band, and background workload across
multiple alternating baseline and Surge runs. Contributors can use Kerneon's
capture and frame-proof tools to produce that evidence.

## Safety and security boundaries

- Surge snapshots reversible state before applying a change and restores that
  state when the session ends or after interrupted recovery.
- Hardware tuning is capability-gated. Unsupported controls are not simulated.
- GPU offset control depends on a compatible vendor driver/API and remains
  opt-in. CPU voltage or firmware overclocking is not implemented.
- Anti-cheat guardrails are enabled by default. Disabling them does not guarantee
  compatibility and Kerneon must never inject into or patch a game process.
- Remote Link pairing, control, screen view, and input are separate grants.
  Rotating pairing credentials invalidates existing sessions.
- Remote Link pairing/signalling currently uses HTTP on the private LAN. Use it
  only on a trusted network; internet exposure is unsupported.
- The screen-control preview cannot cross UAC secure desktop or Windows integrity
  boundaries and is not a replacement for a security-audited remote-management
  product.
- AI insights are optional, advisory, stateless per request, and have no direct
  system-control tools.

## Hardware and compatibility scope

Hardware-specific functionality is discovered at runtime. Vendor name alone is
not treated as proof that a clock, power, thermal, or fan control is available.
The first preview has not been validated across every AMD, Intel, NVIDIA, laptop,
OEM, firmware, driver, display-scale, anti-cheat, or Windows configuration.

See [KNOWN_LIMITATIONS.md](KNOWN_LIMITATIONS.md),
[SURGE_SAFETY.md](SURGE_SAFETY.md), and the repository security policy before
testing elevated or remote-control functionality.

## Release disposition

`0.1.0-preview.1` is suitable for opt-in public testing by users who understand
the limitations above. It is intended to gather reproducible compatibility and
performance evidence before a stable release. Report defects through GitHub
Issues and security-sensitive findings through GitHub private vulnerability
reporting.
