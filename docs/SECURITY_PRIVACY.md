# Security and privacy

Kerneon is a local read-only monitor.

- Runs as the current user with an `asInvoker` manifest.
- Does not install a service, scheduled task, driver, certificate, browser extension, firewall rule, or startup entry.
- Does not inject code, hook games, read game memory, or use undocumented timer/HPET modifications.
- Does not terminate or reprioritize processes in 1.0.0.
- Does not upload telemetry or require an account.
- Does not download code or data at runtime.
- Uses Windows ICMP only when latency monitoring is enabled and only for the configured target.
- Reads local process, registry, performance-counter, network-interface, volume, and OS information through documented Windows APIs.
- Stores settings, logs, and user-requested exports in the current user's profile.

Diagnostic exports are created only by explicit user action. Exported text replaces the username, home path, and IPv4-like strings. The export contains a system summary, provider status, settings, and sanitized logs; it does not contain a process list or executable paths.

The process-detail page intentionally offers only read-only actions. Mutation controls described in the wider product concept remain out of 1.0.0 until critical-process protection, confirmation flows, privilege boundaries, and undo/error behavior can be tested properly.
