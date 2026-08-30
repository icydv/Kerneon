//go:build windows

package main

import "testing"

func treatment(t *testing.T, context SurgeTreatmentContext, id string) SurgeTreatment {
	t.Helper()
	result, ok := treatmentByID(planSurgeTreatments(context), id)
	if !ok {
		t.Fatalf("treatment %q is missing", id)
	}
	return result
}

func TestTreatmentPlannerSelectsContentionIsolationOnlyForMeasuredAggressiveRoute(t *testing.T) {
	context := SurgeTreatmentContext{
		Mode: "performance", Route: SurgeRoute{Key: "cpu-engine"},
		Stutter:    SurgeStutterDiagnosis{Key: "contention"},
		Frame:      FrameStats{Available: true, Samples: 900, FPS: 90},
		Contention: SurgeContention{Actionable: true},
	}
	if got := treatment(t, context, "background-isolation"); !got.Eligible {
		t.Fatalf("expected isolation to be eligible: %+v", got)
	}
	context.Route.Key = "gpu-throughput"
	if got := treatment(t, context, "background-isolation"); got.Eligible {
		t.Fatalf("GPU route must reject CPU isolation: %+v", got)
	}
}

func TestTreatmentPlannerRequiresGPUProofForNVIDIAProfile(t *testing.T) {
	context := SurgeTreatmentContext{
		Mode: "performance", Route: SurgeRoute{Key: "gpu-throughput"},
		Frame:  FrameStats{Available: true, Samples: 900, FPS: 90},
		GPU:    SurgeGPUTelemetry{Ready: true, Provider: "NVIDIA NVML"},
		NVIDIA: SurgeNVIDIAProfile{Ready: true, Found: true, PowerPolicy: 5},
	}
	if got := treatment(t, context, "nvidia-maximum-performance"); !got.Eligible {
		t.Fatalf("expected NVIDIA transaction to be eligible: %+v", got)
	}
	context.Route.Key = "cpu-engine"
	if got := treatment(t, context, "nvidia-maximum-performance"); got.Eligible {
		t.Fatalf("CPU route must reject NVIDIA clocks: %+v", got)
	}
}

func TestTreatmentPlannerHonoursOptionalAntiCheatGuard(t *testing.T) {
	context := SurgeTreatmentContext{
		Mode: "performance", AntiCheat: AntiCheatState{Detected: true, Provider: "Example"}, AntiCheatGuard: true,
		Route: SurgeRoute{Key: "gpu-throughput"}, Frame: FrameStats{Available: true, Samples: 900, FPS: 90},
		GPU: SurgeGPUTelemetry{Ready: true, Provider: "NVIDIA NVML"}, NVIDIA: SurgeNVIDIAProfile{Ready: true, Found: true, PowerPolicy: 5},
	}
	if got := treatment(t, context, "game-session-repair"); got.Eligible {
		t.Fatalf("strict guard must keep game process untouched: %+v", got)
	}
	context.AntiCheatGuard = false
	if got := treatment(t, context, "game-session-repair"); !got.Eligible {
		t.Fatalf("disabled guard should allow ordinary external controls: %+v", got)
	}
}

func TestAggressiveCPURouteAllowsOnlyBoundedSchedulingTrial(t *testing.T) {
	context := SurgeTreatmentContext{
		Mode: "performance", GamePriority: priorityNormal, Route: SurgeRoute{Key: "cpu-engine"},
		Frame: FrameStats{Available: true, Samples: 900, FPS: 90},
	}
	if got := treatment(t, context, "cpu-scheduling-trial"); !got.Eligible {
		t.Fatalf("expected CPU scheduling trial to be eligible: %+v", got)
	}
	context.Route.Key = "gpu-throughput"
	if got := treatment(t, context, "cpu-scheduling-trial"); got.Eligible {
		t.Fatalf("GPU route must reject the CPU scheduling trial: %+v", got)
	}
	context.Route.Key = "cpu-engine"
	context.GamePriority = priorityAboveNormal
	if got := treatment(t, context, "cpu-scheduling-trial"); got.Eligible {
		t.Fatalf("an already modified priority must not start a Normal-to-Above-Normal trial: %+v", got)
	}
}

func TestStrictAntiCheatStillAllowsQualifiedBackgroundIsolation(t *testing.T) {
	context := SurgeTreatmentContext{
		Mode: "performance", AntiCheat: AntiCheatState{Detected: true, Provider: "Example"}, AntiCheatGuard: true,
		Route: SurgeRoute{Key: "cpu-engine"}, Stutter: SurgeStutterDiagnosis{Key: "contention"},
		Frame: FrameStats{Available: true, Samples: 900, FPS: 90}, Contention: SurgeContention{Actionable: true},
	}
	if got := treatment(t, context, "game-session-repair"); got.Eligible {
		t.Fatalf("strict guard must keep the protected game untouched: %+v", got)
	}
	if got := treatment(t, context, "background-isolation"); !got.Eligible || !got.AntiCheatSafe {
		t.Fatalf("qualified non-protected background isolation should remain available: %+v", got)
	}
}

func TestTreatmentPlannerDoesNotMislabelDriverLatencyAsContention(t *testing.T) {
	context := SurgeTreatmentContext{
		Mode: "performance", Route: SurgeRoute{Key: "cpu-engine"}, Stutter: SurgeStutterDiagnosis{Key: "driver-latency"},
		Frame: FrameStats{Available: true, Samples: 900, FPS: 90}, Contention: SurgeContention{Actionable: true},
	}
	if got := treatment(t, context, "background-isolation"); got.Eligible {
		t.Fatalf("driver latency must not trigger process throttling: %+v", got)
	}
	if got := treatment(t, context, "driver-latency-trace"); !got.Eligible {
		t.Fatalf("driver trace should be selected: %+v", got)
	}
}

func TestRefreshAwareFrameCapUsesSmallBoundedMargin(t *testing.T) {
	for refresh, want := range map[int]int{60: 58, 144: 141, 240: 235} {
		if got := refreshAwareFrameCap(refresh); got != want {
			t.Fatalf("refresh %d: got %d want %d", refresh, got, want)
		}
	}
	if got := refreshAwareFrameCap(30); got != 0 {
		t.Fatalf("unsupported refresh should not produce a cap: %d", got)
	}
}
