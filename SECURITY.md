# Security policy

Kerneon handles elevated Windows controls, local-network pairing and optional remote screen input. Security reports are treated as engineering issues, not public debugging exercises.

## Supported version

Only the newest technical-preview release and the current `main` branch receive security fixes. Preview builds are not production-hardened and must not be exposed through router port forwarding or used on an untrusted LAN.

## Report a vulnerability

Use GitHub’s **Report a vulnerability** option in the repository Security tab to open a private report. Include:

- the affected commit or release;
- the exact boundary involved (pairing, control lease, screen session, tuning, rollback or local storage);
- reproducible steps or a minimal proof of concept;
- expected impact and any known mitigations.

Do not include real pairing credentials, Windows passwords, API keys, private diagnostic exports or another person’s data. Please do not open a public issue for an unpatched vulnerability.

The project will acknowledge a complete report as soon as practical, reproduce it, develop a fix privately where necessary, and publish a clear advisory when users need to take action. No bounty programme is currently offered.

## Security boundaries

Kerneon intentionally provides no arbitrary remote shell, file-transfer endpoint, game-memory access, DLL injection, anti-cheat bypass, firmware writer or generic kernel-driver interface. A proposed change that weakens one of those boundaries needs an explicit threat-model review before merge.
