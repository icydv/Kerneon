//go:build windows

package main

import (
	"math"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestPresentMonCaptureUsesDedicatedSessionAndBurstBuffer(t *testing.T) {
	args := presentMonCaptureArgs(24092, "KerneonFrames-24092")
	want := []string{
		"--process_id", "24092",
		"--set_circular_buffer_size", "16384",
		"--session_name", "KerneonFrames-24092",
		"--stop_existing_session",
		"--terminate_on_proc_exit",
	}
	for index := 0; index < len(want); {
		flag := want[index]
		if !slices.Contains(args, flag) {
			t.Fatalf("capture arguments omit %q: %v", flag, args)
		}
		index++
		if strings.HasPrefix(flag, "--") && index < len(want) && !strings.HasPrefix(want[index], "--") {
			value := want[index]
			found := false
			for position := 0; position+1 < len(args); position++ {
				if args[position] == flag && args[position+1] == value {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("capture arguments omit %s %s: %v", flag, value, args)
			}
			index++
		}
	}
	if slices.Contains(args, "--terminate_existing_session") {
		t.Fatalf("capture must not terminate unrelated sessions: %v", args)
	}
}

func TestFrameStatsUsesTailLatencyForOnePercentLow(t *testing.T) {
	monitor := FrameMonitor{}
	now := time.Now()
	for i := 0; i < 99; i++ {
		monitor.samples = append(monitor.samples, FrameSample{At: now, FrameTimeMs: 10, DisplayDelayMs: 12, RenderMs: 6, GPUActiveMs: 6})
	}
	monitor.samples = append(monitor.samples, FrameSample{At: now, FrameTimeMs: 40, DisplayDelayMs: 50, RenderMs: 8, GPUActiveMs: 8})
	stats := monitor.Snapshot()
	if stats.Samples != 100 || math.Abs(stats.FPS-97.087) > 0.1 {
		t.Fatalf("unexpected aggregate: %+v", stats)
	}
	if math.Abs(stats.OnePercentLow-25) > 0.01 {
		t.Fatalf("unexpected 1%% low: %.3f", stats.OnePercentLow)
	}
	if stats.P95DisplayMs != 12 {
		t.Fatalf("unexpected p95 display delay: %.3f", stats.P95DisplayMs)
	}
	if stats.HitchCount != 1 || stats.P99FrameMs != 10 || stats.WorstFrameMs != 40 {
		t.Fatalf("unexpected long-frame analysis: %+v", stats)
	}
}

func TestFrameStatsCountsHighRefreshHitchesWithoutUsingFixedThirtyThreeMsCutoff(t *testing.T) {
	monitor := FrameMonitor{}
	now := time.Now()
	for i := 0; i < 998; i++ {
		monitor.samples = append(monitor.samples, FrameSample{At: now, FrameTimeMs: 6.9, RenderMs: 4})
	}
	monitor.samples = append(monitor.samples,
		FrameSample{At: now, FrameTimeMs: 15.5, RenderMs: 4},
		FrameSample{At: now, FrameTimeMs: 42, RenderMs: 4},
	)
	stats := monitor.Snapshot()
	if stats.HitchCount != 2 || stats.ZeroPointOnePercentLow <= 0 || stats.WorstFrameMs != 42 {
		t.Fatalf("high-refresh hitches were not preserved: %+v", stats)
	}
}

func TestPercentileBounds(t *testing.T) {
	values := []float64{1, 2, 3, 4, 5}
	if percentile(values, -1) != 1 || percentile(values, 2) != 5 || percentile(nil, .5) != 0 {
		t.Fatal("percentile did not clamp")
	}
}

func TestFrameLimiterSeparatesGPUAndEngineHeadroom(t *testing.T) {
	gpu := FrameStats{Samples: 500, FPS: 100, AverageRenderMs: 9.2}
	if got := frameLimiterLabel(gpu, 144); got != "GPU throughput · render path 92% of frame" {
		t.Fatalf("unexpected GPU limiter: %q", got)
	}
	engine := FrameStats{Samples: 500, FPS: 75, AverageRenderMs: 4.8}
	if got := frameLimiterLabel(engine, 144); got != "CPU / engine / frame cap · render path 36% of frame" {
		t.Fatalf("unexpected engine limiter: %q", got)
	}
}

func TestComparableFrameWorkloadRejectsSceneChange(t *testing.T) {
	baseline := FrameStats{AverageRenderMs: 4.0}
	if ok, _ := comparableFrameWorkload(baseline, FrameStats{AverageRenderMs: 4.8}); !ok {
		t.Fatal("small GPU-work movement should remain comparable")
	}
	if ok, _ := comparableFrameWorkload(baseline, FrameStats{AverageRenderMs: 6.2}); ok {
		t.Fatal("large GPU-work movement should be treated as a different scene")
	}
}

func TestFrameBenefitThresholdsRejectNoiseAndAcceptTailImprovement(t *testing.T) {
	baseline := FrameStats{OnePercentLow: 60, P99FrameMs: 24, HitchesPerMinute: 10, P95DisplayMs: 20}
	noise := FrameStats{OnePercentLow: 61, P99FrameMs: 23, HitchesPerMinute: 9, P95DisplayMs: 19.5}
	if frameBenefitThresholdCrossed(baseline, noise) {
		t.Fatal("ordinary measurement noise crossed a benefit threshold")
	}
	improved := noise
	improved.P99FrameMs = 20
	if !frameBenefitThresholdCrossed(baseline, improved) {
		t.Fatal("material p99 improvement was not recognized")
	}
}

func TestFrameBenefitThresholdRecognizesRepeatableAverageFPSGain(t *testing.T) {
	baseline := FrameStats{FPS: 100, OnePercentLow: 70, P99FrameMs: 15}
	if frameBenefitThresholdCrossed(baseline, FrameStats{FPS: 102.9, OnePercentLow: 70, P99FrameMs: 15}) {
		t.Fatal("sub-threshold average FPS noise was classified as a benefit")
	}
	if !frameBenefitThresholdCrossed(baseline, FrameStats{FPS: 103, OnePercentLow: 70, P99FrameMs: 15}) {
		t.Fatal("a three-percent average FPS gain was not recognized")
	}
}
