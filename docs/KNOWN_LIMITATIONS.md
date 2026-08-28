# Known limitations in 1.0.0

These are explicit scope boundaries, not hidden or simulated features.

- No PresentMon integration, FPS, frame time, 1%/0.1% lows, or frame bottleneck classification. The Gaming page says this rather than fabricating frame data.
- No temperature, fan speed, voltage, clock, or power telemetry. Windows exposes no consistent supported provider for these across vendors; no third-party driver is bundled.
- GPU engine and memory values depend on Windows PDH counters and the installed display driver. Unsupported systems show an unavailable state.
- Process detail is read-only and does not yet include per-process GPU/network attribution, command line, signature/publisher, connections, parent/child tree, or per-process history. Priority, affinity, Efficiency Mode, and termination actions are deliberately omitted.
- Compact mode is a small always-on-top window, not a production game overlay: it has no global hotkey, click-through lock, opacity control, metric picker, session recorder, or anti-cheat certification.
- History and captured incidents are memory-only. Persistent/downsampled 6-hour, 24-hour, and 7-day storage is not implemented; the retention setting is reserved for that future store.
- Alerts use tested hysteresis/cooldown primitives and display local events, but 1.0.0 has no custom rule editor, Windows toast delivery, sound, or per-process alert configuration.
- Insights are conservative current-session observations. They do not claim long-term baselines or causal diagnosis.
- Network rates are for one selected adapter. Per-process network attribution and connection ownership are unavailable. Virtual/filter interfaces are excluded during automatic selection to avoid double counting.
- Administrator-only/protected process fields may be unavailable. Kerneon does not prompt for elevation.
- Per-monitor-v2 scaling is implemented, but the release candidate was visually captured only at the development machine's active scale. Additional physical multi-monitor/DPI matrices remain to be validated.
- Sleep/resume baseline reset is implemented from `WM_POWERBROADCAST`; the release candidate did not automate a physical sleep cycle.
- The portable executable is not Authenticode-signed because no publisher certificate was provided. Windows reputation prompts may therefore appear on another PC.
