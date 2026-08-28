# Security and privacy

Kerneon is a local-first monitor with two explicit opt-in boundaries: AI analysis and a reversible Windows power-plan experiment.

- Runs as the current user with an `asInvoker` manifest.
- Does not install a service, scheduled task, driver, certificate, browser extension, firewall rule, or startup entry.
- Does not inject code, hook games, read game memory, or use undocumented timer/HPET modifications.
- Does not terminate or reprioritize processes in 1.0.0.
- Does not upload telemetry by default or require an account. An AI request occurs only after the user connects an API key and clicks Generate.
- AI sends current/aggregate resource measurements and anonymous process metric labels; it excludes process names, executable paths, hostnames, adapter names, usernames, and IP addresses.
- Uses a stateless OpenAI Responses API request with strict structured output and no model tools. Generated advice cannot invoke application actions.
- Stores the optional API key in Windows Credential Manager. It is not written to settings, logs, exports, or source files.
- Does not download or execute code at runtime. AI responses are bounded JSON text rendered as advisory cards.
- Uses Windows ICMP only when latency monitoring is enabled and only for the configured target.
- Reads local process, registry, performance-counter, network-interface, volume, and OS information through documented Windows APIs.
- Stores settings, logs, and user-requested exports in the current user's profile.

Diagnostic exports are created only by explicit user action. Exported text replaces the username, home path, and IPv4-like strings. The export contains a system summary, provider status, settings, and sanitized logs; it does not contain a process list or executable paths.

The process-detail page remains read-only. Kerneon does not expose termination, priority, affinity, service, registry, driver, memory-cleaner, or game-file mutations.

Optimize can invoke Windows `powercfg.exe /setactive` as the current user only after an explicit action, an eligible CPU-heavy baseline, and a durable rollback journal. It never replaces a plan whose name is already performance-, ultimate-, or ultra-oriented. The exact previous GUID is restored on Rollback, on normal exit while a test is pending, or at next startup after an interrupted experiment. If the journal cannot be saved, the change is refused. Keep is available only after comparable before/after samples produce the measured-improvement verdict.
