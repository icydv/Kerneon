//go:build windows

package main

import (
	"strings"
	"testing"
)

func TestSurgeRouteRejectsGPUTuningForEngineLimitedFrame(t *testing.T) {
	frame := FrameStats{Samples: 800, FPS: 80, AverageRenderMs: 4}
	route := determineSurgeRoute(frame, SurgeGPUTelemetry{Ready: true, GPUUtil: 38, PowerW: 80, PowerLimitW: 250}, 37, 38, 144)
	if route.Key != "cpu-engine" || strings.HasPrefix(route.ControlPath, "vendor-gpu") {
		t.Fatalf("route=%+v", route)
	}
}

func TestSurgeRouteIdentifiesPowerConstrainedGPU(t *testing.T) {
	frame := FrameStats{Samples: 900, FPS: 70, AverageRenderMs: 13}
	gpu := SurgeGPUTelemetry{Ready: true, GPUUtil: 99, PowerW: 242, PowerLimitW: 250, ThrottleReasons: nvmlReasonPowerCap, ThrottleSummary: "power cap"}
	route := determineSurgeRoute(frame, gpu, 44, 99, 144)
	if route.Key != "gpu-power" || route.ControlPath != "vendor-gpu-power" {
		t.Fatalf("route=%+v", route)
	}
}

func TestSurgeContentionExcludesGameAndProtectedProcesses(t *testing.T) {
	processes := []ProcessMetric{
		{PID: 10, Name: "game.exe", CPU: 45},
		{PID: 11, Name: "dwm.exe", CPU: 8},
		{PID: 12, Name: "sync-tool.exe", CPU: 7},
		{PID: 13, Name: "browser.exe", CPU: 4},
	}
	finding := analyzeSurgeContention(processes, 10)
	if !finding.Actionable || finding.TopName != "sync-tool.exe" || finding.CombinedCPU != 11 {
		t.Fatalf("finding=%+v", finding)
	}
}

func TestSurgeContentionNeverTargetsAntiCheatOrKerneon(t *testing.T) {
	processes := []ProcessMetric{
		{PID: 10, Name: "game.exe", CPU: 45},
		{PID: 11, Name: "EasyAntiCheat_EOS.exe", CPU: 20},
		{PID: 12, Name: "Kerneon-Surge-Preview.exe", CPU: 15},
		{PID: 13, Name: "capture-tool.exe", CPU: 7},
	}
	finding := analyzeSurgeContention(processes, 10)
	if finding.TopName != "capture-tool.exe" || finding.CombinedCPU != 7 {
		t.Fatalf("protected process entered contention candidates: %+v", finding)
	}
}

func TestDiagnoseSurgeStutterRoutesDPCBeforeGenericTweaks(t *testing.T) {
	frame := FrameStats{Samples: 900, FPS: 120, P99FrameMs: 28, WorstFrameMs: 55, HitchesPerMinute: 8, FramePacingCV: .24}
	latency := LatencyData{Available: true, DPCTimePercent: 2.1, InterruptTimePercent: .4, InterruptsPerSec: 18000}
	diagnosis := diagnoseSurgeStutter(frame, latency, DiskData{}, MemoryData{}, SurgeContention{})
	if diagnosis.Key != "driver-latency" || diagnosis.Confidence != "medium" {
		t.Fatalf("unexpected stutter route: %+v", diagnosis)
	}
}

func TestDiagnoseSurgeStutterKeepsStableWindowObservationOnly(t *testing.T) {
	frame := FrameStats{Samples: 900, FPS: 144, P99FrameMs: 7.4, WorstFrameMs: 9, HitchesPerMinute: 0, FramePacingCV: .04}
	diagnosis := diagnoseSurgeStutter(frame, LatencyData{Available: true}, DiskData{}, MemoryData{}, SurgeContention{})
	if diagnosis.Key != "stable" {
		t.Fatalf("stable pacing was misclassified: %+v", diagnosis)
	}
}

func TestSurgeLocalPressureCannotBeOverriddenByNetworkQuality(t *testing.T) {
	pressure := surgeLocalPressure(25, 30, 45, 4)
	if pressure.Key == "network" {
		t.Fatalf("network diagnosis leaked into Surge: %+v", pressure)
	}
	if pressure.Key != "comfortable" {
		t.Fatalf("unexpected local pressure: %+v", pressure)
	}
}
