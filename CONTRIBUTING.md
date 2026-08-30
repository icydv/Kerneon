# Contributing to Kerneon

Thank you for helping Kerneon understand more PCs without inventing performance claims.

## Before opening a pull request

1. Search existing issues and describe the user-visible problem before proposing a broad rewrite.
2. Keep hardware mutation capability-gated, reversible and disabled by default.
3. Never add injection, game-memory access, arbitrary remote commands, undocumented register writes or silent dependency installation.
4. Add tests for new parsing, scoring, proof, security or rollback behaviour.
5. Run the full test suite on Windows:

   ```powershell
   $env:KERNEON_GO = 'C:\Program Files\Go\bin\go.exe'
   .\scripts\test.ps1
   ```

6. Explain what was measured, what remains an inference and how every mutation returns to the captured state.

## Performance reports

Useful performance evidence alternates equivalent Surge-off and Surge-on windows, records the exact game scene and settings, and includes average FPS, 1% low, p99 frame time and hitch rate. A single uncontrolled run is useful diagnostic context but not proof of a gain.

## Style

- Prefer plain language in user-facing copy.
- Keep provider failures isolated and visible.
- Preserve the local-first, no-telemetry default.
- Use `gofmt` and keep the Win32 render path allocation-conscious.
- Avoid hardware- or title-specific assumptions in the universal Surge path.

By contributing, you agree that your contribution is provided under GPL-3.0-only and that you have the right to submit it.
