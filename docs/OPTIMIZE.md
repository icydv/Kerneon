# Optimize safely

Optimize is a controlled experiment, not a boost button. Kerneon currently supports one Windows mutation: temporarily selecting the standard High performance power plan when the measured workload is CPU-heavy and the current plan is not already performance-oriented.

## Workflow

1. Play or run a repeatable CPU-heavy scene.
2. Choose **Capture baseline**. Kerneon requires at least ten synchronized samples.
3. If the evidence gate opens, choose **Apply + test**. Kerneon durably records the exact active-plan GUID before making the change.
4. Repeat the same scene, then choose **Compare** after at least ten new samples.
5. Choose **Keep verified change** only if Kerneon reports a measured improvement, or **Rollback now** to restore the previous plan.

Kerneon rejects the comparison when CPU/GPU demand changed too much between runs. It reports **Improvement not proven** when the repeat does not show a material CPU-clock improvement under comparable load. No FPS increase is claimed because this version does not include PresentMon frame-time capture.

## Recovery and trade-offs

A pending experiment rolls back on normal exit. If the app or PC is interrupted, Kerneon restores the journaled plan on its next startup. If the rollback journal cannot be written, no change is made.

High performance can use more energy, create more heat/noise, and may make no measurable difference—especially for GPU-bound games. Kerneon does not replace a current plan whose name already contains Performance, Ultimate, or Ultra. It deliberately omits RAM cleaners, generic registry tweaks, blanket service disabling, process termination, and automatic driver changes.

