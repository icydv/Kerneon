//go:build windows

package main

import "strings"

type SurgeTreatment struct {
	ID                string
	Title             string
	Eligible          bool
	Reason            string
	ExpectedMetric    string
	MinimumEvidence   string
	Risk              string
	RequiresElevation bool
	RequiresRelaunch  bool
	AntiCheatSafe     bool
}

type SurgeTreatmentContext struct {
	Mode             string
	AntiCheat        AntiCheatState
	AntiCheatGuard   bool
	GamePriority     uint32
	Route            SurgeRoute
	Stutter          SurgeStutterDiagnosis
	Frame            FrameStats
	Contention       SurgeContention
	GPU              SurgeGPUTelemetry
	NVIDIA           SurgeNVIDIAProfile
	DisplayRefreshHz int
}

// planSurgeTreatments is the policy boundary between evidence and mutation.
// Every control path has a named outcome and a falsifiable eligibility gate;
// callers must never interpret an ineligible treatment as permission to act.
func planSurgeTreatments(context SurgeTreatmentContext) []SurgeTreatment {
	strictAntiCheat := context.AntiCheat.Detected && context.AntiCheatGuard
	aggressive := context.Mode == "performance"
	hasFrameBaseline := context.Frame.Available && context.Frame.Error == "" && context.Frame.Samples >= 120

	process := SurgeTreatment{
		ID: "game-session-repair", Title: "Repair game scheduling state",
		Eligible: !strictAntiCheat, AntiCheatSafe: false,
		ExpectedMetric:  "remove an observed Windows scheduling restriction",
		MinimumEvidence: "locked foreground process with a captured rollback snapshot",
		Risk:            "low; Above Normal is the ceiling and every field is restored",
	}
	if process.Eligible {
		process.Reason = "The locked game may receive bounded process-state repairs when Windows reports a degraded state."
	} else {
		process.Reason = "The anti-cheat guard keeps the game process untouched."
	}

	scheduling := SurgeTreatment{
		ID: "cpu-scheduling-trial", Title: "Give the game first call on contested CPU time",
		AntiCheatSafe: false, ExpectedMetric: "improve average FPS, the 1% low or the slow frame-time tail on a CPU-limited route",
		MinimumEvidence: "30-second stock baseline plus at least two comparable post-treatment windows",
		Risk:            "low; Windows Above Normal only, never High or Realtime, with exact priority rollback",
	}
	scheduling.Eligible = aggressive && hasFrameBaseline && !strictAntiCheat && (context.Route.Key == "cpu-engine" || context.Route.Key == "mixed") && context.GamePriority == priorityNormal
	switch {
	case !aggressive:
		scheduling.Reason = "The scheduling experiment is reserved for the explicitly confirmed Aggressive policy."
	case strictAntiCheat:
		scheduling.Reason = "The anti-cheat guard keeps the protected game process untouched."
	case !hasFrameBaseline:
		scheduling.Reason = "A sufficiently large frame baseline has not been captured."
	case context.Route.Key != "cpu-engine" && context.Route.Key != "mixed":
		scheduling.Reason = "The measured frame route is not limited by CPU or engine scheduling."
	case context.GamePriority != priorityNormal:
		scheduling.Reason = "The game is not at Normal priority, so this exact A/B scheduling trial is not applicable."
	default:
		scheduling.Reason = "The game is CPU/engine limited at Normal priority, so a bounded Above Normal A/B trial is justified."
	}

	background := SurgeTreatment{
		ID: "background-isolation", Title: "Isolate measured background contention",
		AntiCheatSafe: true, ExpectedMetric: "lower p99 frame time and hitch rate without reducing average FPS",
		MinimumEvidence: "120 presented frames, actionable same-session contention and a contention stutter route",
		Risk:            "medium; at most two user-space contenders are temporarily moved to Below Normal + EcoQoS",
	}
	background.Eligible = aggressive && hasFrameBaseline && context.Contention.Actionable && context.Stutter.Key == "contention" && (context.Route.Key == "cpu-engine" || context.Route.Key == "mixed")
	switch {
	case !aggressive:
		background.Reason = "Background isolation is reserved for the explicitly confirmed Aggressive policy."
	case !hasFrameBaseline:
		background.Reason = "A sufficiently large frame baseline has not been captured."
	case !context.Contention.Actionable:
		background.Reason = "No eligible same-session user process is consuming enough CPU to justify isolation."
	case context.Stutter.Key != "contention":
		background.Reason = "The hitch signature points to " + context.Stutter.Key + ", not background CPU contention."
	case context.Route.Key != "cpu-engine" && context.Route.Key != "mixed":
		background.Reason = "The measured frame route is not CPU/contention sensitive."
	default:
		background.Reason = "A reversible A/B isolation window is justified by the measured frame and process evidence."
	}

	nvidia := SurgeTreatment{
		ID: "nvidia-maximum-performance", Title: "Hold NVIDIA application clocks ready",
		AntiCheatSafe: true, ExpectedMetric: "reduce clock-ramp stalls and improve the slow frame-time tail",
		MinimumEvidence: "NVIDIA application profile plus a GPU-throughput route and native limiter telemetry",
		Risk:            "medium; higher idle/game power and temperature while the session is active",
	}
	nvidiaProvider := context.NVIDIA.Ready && context.NVIDIA.Found && strings.Contains(strings.ToLower(context.GPU.Provider), "nvidia")
	nvidia.Eligible = aggressive && hasFrameBaseline && !strictAntiCheat && nvidiaProvider && (context.Route.Key == "gpu-throughput" || context.Route.Key == "gpu-power") && context.NVIDIA.PowerPolicy != 1
	switch {
	case !aggressive:
		nvidia.Reason = "The vendor power-profile transaction is reserved for Aggressive mode."
	case strictAntiCheat:
		nvidia.Reason = "The anti-cheat guard keeps persistent application-profile changes out of this session."
	case !hasFrameBaseline:
		nvidia.Reason = "Frame evidence is not large enough to justify a driver-profile mutation."
	case !nvidiaProvider:
		nvidia.Reason = "A writable NVIDIA application profile is not available for this executable."
	case context.Route.Key != "gpu-throughput" && context.Route.Key != "gpu-power":
		nvidia.Reason = "The active bottleneck is not NVIDIA GPU throughput."
	case context.NVIDIA.PowerPolicy == 1:
		nvidia.Reason = "The NVIDIA application profile already prefers maximum performance."
	default:
		nvidia.Reason = "GPU-bound frame evidence makes a temporary documented NVAPI profile transaction eligible."
	}

	frameCap := SurgeTreatment{
		ID: "refresh-aware-frame-cap", Title: "Stabilize the display ceiling",
		AntiCheatSafe: true, RequiresRelaunch: true,
		ExpectedMetric:  "lower p99 frame time, queueing and VRR-ceiling oscillation",
		MinimumEvidence: "refresh known, FPS at the display boundary and unstable long-frame tail",
		Risk:            "medium; a poor cap can reduce responsiveness, so it remains a separate A/B experiment",
	}
	nearRefresh := context.DisplayRefreshHz >= 60 && context.Frame.FPS >= float64(context.DisplayRefreshHz)*0.92
	unstableTail := context.Frame.HitchesPerMinute >= 4 || context.Frame.FramePacingCV >= 0.16
	frameCap.Eligible = aggressive && hasFrameBaseline && !strictAntiCheat && context.NVIDIA.Ready && context.NVIDIA.Found && context.Route.Key == "ceiling" && nearRefresh && unstableTail
	if frameCap.Eligible {
		frameCap.Reason = "Frame delivery is oscillating at the measured display boundary; a separate cap experiment is eligible after relaunch."
	} else {
		frameCap.Reason = "The display-ceiling, refresh and unstable-tail gates are not all satisfied."
	}

	trace := SurgeTreatment{
		ID: "driver-latency-trace", Title: "Capture the driver latency culprit",
		Eligible: context.Stutter.Key == "driver-latency", AntiCheatSafe: true,
		ExpectedMetric:  "attribute DPC/ISR spikes to a concrete driver before changing configuration",
		MinimumEvidence: "repeated long frames aligned with elevated DPC or interrupt service time",
		Risk:            "low; short bounded ETW capture, no game mutation",
	}
	if trace.Eligible {
		trace.Reason = "The current hitch window aligns with elevated DPC/ISR activity."
	} else {
		trace.Reason = "No driver-latency correlation is present in the current window."
	}

	return []SurgeTreatment{process, scheduling, background, nvidia, frameCap, trace}
}

func treatmentByID(treatments []SurgeTreatment, id string) (SurgeTreatment, bool) {
	for _, treatment := range treatments {
		if treatment.ID == id {
			return treatment, true
		}
	}
	return SurgeTreatment{}, false
}

func refreshAwareFrameCap(refreshHz int) int {
	if refreshHz < 60 {
		return 0
	}
	margin := (refreshHz + 49) / 50 // approximately 2%, rounded up
	if margin < 2 {
		margin = 2
	}
	target := refreshHz - margin
	if target < 30 || target > 1023 {
		return 0
	}
	return target
}
