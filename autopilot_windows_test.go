//go:build windows

package main

import (
	"strings"
	"testing"
	"time"
)

func TestSurgeProofWindowsAreLongEnoughForRealGameplay(t *testing.T) {
	if surgeBaselineDuration < 30*time.Second {
		t.Fatalf("baseline is too short for a defensible A/B claim: %s", surgeBaselineDuration)
	}
	if surgeProofWindowDuration < 20*time.Second || surgeProofWindowStride < surgeProofWindowDuration {
		t.Fatalf("proof windows are too short or overlap: duration=%s stride=%s", surgeProofWindowDuration, surgeProofWindowStride)
	}
}

func TestSurgeWaitsForUsableFrameEvidenceBeforeActing(t *testing.T) {
	if surgeFrameBaselineReady(FrameStats{Available: true, Samples: 119, FPS: 90, OnePercentLow: 60}) {
		t.Fatal("an undersized baseline was accepted")
	}
	if surgeFrameBaselineReady(FrameStats{Available: true, Samples: 900, FPS: 90}) {
		t.Fatal("a baseline without low-percentile evidence was accepted")
	}
	if !surgeFrameBaselineReady(FrameStats{Available: true, Samples: 900, FPS: 90, OnePercentLow: 60}) {
		t.Fatal("a complete baseline was rejected")
	}
}

func TestTuningFrameComparisonContainsMarketableABMetrics(t *testing.T) {
	text := tuningFrameComparison(
		FrameStats{FPS: 80, OnePercentLow: 55, P99FrameMs: 18, HitchesPerMinute: 6},
		FrameStats{FPS: 88, OnePercentLow: 63, P99FrameMs: 15, HitchesPerMinute: 2},
	)
	for _, expected := range []string{"average 80.0→88.0 FPS", "1% low 55.0→63.0 FPS", "p99 18.0→15.0 ms", "hitches 6.0→2.0/min"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("comparison omitted %q: %s", expected, text)
		}
	}
}

func TestSurgeProofRequiresAnActualMutation(t *testing.T) {
	a := &App{}
	if a.autopilotMutationApplied() {
		t.Fatal("an untouched session was classified as a treatment")
	}
	a.autopilot.PriorityChanged = true
	if !a.autopilotMutationApplied() {
		t.Fatal("a game scheduling repair was not classified as a treatment")
	}
	a.autopilot.PriorityChanged = false
	a.autopilot.Background = []backgroundProcessSnapshot{{PriorityChanged: true}}
	if !a.autopilotMutationApplied() {
		t.Fatal("guarded background isolation was not classified as a treatment")
	}
}

func TestAverageFrameStatsSupportsDirectABClaims(t *testing.T) {
	mean := averageFrameStats([]FrameStats{
		{FPS: 90, OnePercentLow: 60, P99FrameMs: 16, HitchesPerMinute: 4, Samples: 1800},
		{FPS: 100, OnePercentLow: 70, P99FrameMs: 14, HitchesPerMinute: 2, Samples: 2000},
	})
	if mean.FPS != 95 || mean.OnePercentLow != 65 || mean.P99FrameMs != 15 || mean.HitchesPerMinute != 3 || mean.Samples != 3800 {
		t.Fatalf("unexpected aggregate: %+v", mean)
	}
}

func TestExperimentalFrameProofRequiresRepeatedComparableBenefit(t *testing.T) {
	for _, test := range []struct {
		comparable int
		benefits   map[string]int
		want       bool
	}{
		{3, map[string]int{"average FPS": 1, "1% low": 1}, false},
		{1, map[string]int{"average FPS": 1}, false},
		{2, map[string]int{"average FPS": 2}, true},
		{3, map[string]int{"p99 frame time": 2, "average FPS": 1}, true},
	} {
		got, _, _ := experimentalFrameProofEarned(test.comparable, test.benefits)
		if got != test.want {
			t.Fatalf("comparable=%d benefits=%v: got %t want %t", test.comparable, test.benefits, got, test.want)
		}
	}
}

func TestFrameBenefitMetricsNameEveryCrossedThreshold(t *testing.T) {
	baseline := FrameStats{FPS: 100, OnePercentLow: 70, P99FrameMs: 15, HitchesPerMinute: 6, P95DisplayMs: 20}
	after := FrameStats{FPS: 104, OnePercentLow: 74, P99FrameMs: 13, HitchesPerMinute: 3, P95DisplayMs: 18}
	metrics := strings.Join(frameBenefitMetrics(baseline, after), ",")
	for _, expected := range []string{"average FPS", "1% low", "p99 frame time", "hitch rate", "display latency"} {
		if !strings.Contains(metrics, expected) {
			t.Fatalf("benefit explanation omitted %q: %s", expected, metrics)
		}
	}
}

func TestGPUClockProofRequiresAGPUThroughputRoute(t *testing.T) {
	if gpuTuningRouteEligible(SurgeRoute{Key: "cpu-engine"}) {
		t.Fatal("CPU/engine-limited gameplay was allowed to prove a GPU clock")
	}
	if gpuTuningRouteEligible(SurgeRoute{Key: "mixed"}) {
		t.Fatal("an unresolved mixed route was allowed to prove a GPU clock")
	}
	if !gpuTuningRouteEligible(SurgeRoute{Key: "gpu-throughput"}) || !gpuTuningRouteEligible(SurgeRoute{Key: "gpu-power"}) {
		t.Fatal("a measured GPU throughput route was not eligible for direct tuning proof")
	}
}

func confirmationStats(fps, low, p99, render float64) FrameStats {
	return FrameStats{
		Available: true, Samples: 1200, FPS: fps, OnePercentLow: low,
		P99FrameMs: p99, AverageRenderMs: render,
	}
}

func TestTuningConfirmationRequiresBothAlternatingPairs(t *testing.T) {
	stock := []FrameStats{
		confirmationStats(100, 70, 15, 8.0),
		confirmationStats(101, 71, 14.8, 8.1),
	}
	tuned := []FrameStats{
		confirmationStats(104, 74, 13.6, 8.1),
		confirmationStats(105, 75, 13.4, 8.2),
	}
	proved, stockMean, tunedMean, reason := evaluateTuningConfirmation(stock, tuned)
	if !proved {
		t.Fatalf("repeatable A-B-B-A benefit was rejected: %s", reason)
	}
	if stockMean.FPS != 100.5 || tunedMean.FPS != 104.5 {
		t.Fatalf("unexpected confirmation means: stock=%+v tuned=%+v", stockMean, tunedMean)
	}

	tuned[1] = confirmationStats(101.5, 71.2, 14.7, 8.2)
	proved, _, _, reason = evaluateTuningConfirmation(stock, tuned)
	if proved || !strings.Contains(reason, "only 1 of 2") {
		t.Fatalf("a one-off discovery win escaped the repeatability gate: proved=%t reason=%q", proved, reason)
	}
}

func TestSurgeHardwareBaselineUsesAStableRunningAverage(t *testing.T) {
	baseline := surgeMetricBaseline{}
	for _, value := range []float64{1800, 2200, 2000} {
		baseline = addSurgeBaselineSample(baseline, value)
	}
	baseline = addSurgeBaselineSample(baseline, 0)
	if baseline.Samples != 3 || baseline.Value != 2000 {
		t.Fatalf("unexpected baseline: %+v", baseline)
	}
}
