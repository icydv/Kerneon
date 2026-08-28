package core

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"
)

func TestRateCalculationUsesElapsedTime(t *testing.T) {
	var r RateTracker
	t0 := time.Unix(0, 0)
	r.Add(100, t0)
	got := r.Add(600, t0.Add(250*time.Millisecond))
	if !got.Valid || got.Delta != 500 || got.Rate != 2000 {
		t.Fatalf("unexpected result: %+v", got)
	}
}

func TestRateCounterReset(t *testing.T) {
	var r RateTracker
	t0 := time.Now()
	r.Add(500, t0)
	got := r.Add(10, t0.Add(time.Second))
	if !got.Reset || got.Valid || got.Rate != 0 {
		t.Fatalf("reset created a rate: %+v", got)
	}
}

func TestRateCounterWraparound(t *testing.T) {
	var r RateTracker
	t0 := time.Now()
	r.Add(math.MaxUint64-4, t0)
	got := r.Add(5, t0.Add(time.Second))
	if !got.Valid || got.Delta != 10 {
		t.Fatalf("wrap result: %+v", got)
	}
}

func TestRateRejectsZeroAndTinyElapsed(t *testing.T) {
	var r RateTracker
	t0 := time.Now()
	r.Add(1, t0)
	if r.Add(2, t0).Valid {
		t.Fatal("zero elapsed was accepted")
	}
	if r.Add(3, t0.Add(time.Nanosecond)).Valid {
		t.Fatal("tiny elapsed was accepted")
	}
}

func TestEMAIsTimeAware(t *testing.T) {
	e := NewEMA(time.Second)
	e.Update(0, time.Second)
	a := e.Update(100, 100*time.Millisecond)
	if a <= 0 || a >= 20 {
		t.Fatalf("unexpected EMA step: %f", a)
	}
	b := e.Update(100, 2*time.Second)
	if b <= 80 || b >= 100 {
		t.Fatalf("unexpected delayed EMA: %f", b)
	}
}

func TestRollingAverage(t *testing.T) {
	r := NewRollingAverage(3)
	for _, v := range []float64{1, 2, 3, 7} {
		r.Add(v)
	}
	if r.Value() != 4 {
		t.Fatalf("got %f", r.Value())
	}
}

func TestRingBufferOrderAndCapacity(t *testing.T) {
	r := NewRing[int](3)
	for i := 1; i <= 5; i++ {
		r.Add(i)
	}
	got := r.Values(nil)
	want := []int{3, 4, 5}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v", got)
		}
	}
}

func TestUnitConversionAndPrecision(t *testing.T) {
	f := RateFormatter{Mode: UnitBits}
	now := time.Now()
	if got := f.Format(1_250_000, now); got != "10.0 Mbps" {
		t.Fatalf("got %q", got)
	}
	f = RateFormatter{Mode: UnitBytes}
	if got := f.Format(1_250_000, now); got != "1.25 MB/s" {
		t.Fatalf("got %q", got)
	}
}

func TestRateFormatterStartsAtTheCorrectMagnitude(t *testing.T) {
	now := time.Now()
	f := RateFormatter{Mode: UnitBytes}
	if got := f.Format(3_688_487, now); got != "3.69 MB/s" {
		t.Fatalf("initial value leaked raw bytes: %q", got)
	}
	if got := FormatRate(3_688_487, UnitBits); got != "29.5 Mbps" {
		t.Fatalf("stateless rate formatter got %q", got)
	}
}

func TestPeakFormattingDoesNotChangeLiveUnit(t *testing.T) {
	now := time.Now()
	f := RateFormatter{Mode: UnitBytes}
	if got := f.Format(24_000, now); got != "24.0 KB/s" {
		t.Fatalf("live rate got %q", got)
	}
	_ = FormatRate(900_000_000, UnitBytes)
	if got := f.Format(25_000, now.Add(100*time.Millisecond)); got != "25.0 KB/s" {
		t.Fatalf("peak formatting changed live scale: %q", got)
	}
}

func TestUnitHysteresisPreventsBoundaryFlicker(t *testing.T) {
	f := RateFormatter{Mode: UnitAuto}
	now := time.Now()
	for i := 0; i < 10; i++ {
		f.Format(140_000, now.Add(time.Duration(i)*100*time.Millisecond))
	}
	if got := f.Format(124_000, now.Add(time.Second)); !strings.Contains(got, "Mbps") {
		t.Fatalf("unit fell too early: %q", got)
	}
}

func TestGraphScaleHoldsAfterSpike(t *testing.T) {
	var g GraphScaler
	now := time.Now()
	high := g.Update(900, now)
	low := g.Update(20, now.Add(time.Second))
	if low != high {
		t.Fatalf("scale bounced: %f to %f", high, low)
	}
	if later := g.Update(20, now.Add(6*time.Second)); later >= high {
		t.Fatalf("scale did not decay: %f", later)
	}
}

func TestGraphTimeMapping(t *testing.T) {
	start := time.Unix(0, 0)
	end := start.Add(10 * time.Second)
	if got := MapTime(start.Add(5*time.Second), start, end, 100); got != 50 {
		t.Fatalf("got %f", got)
	}
	if got := MapTime(end.Add(time.Second), start, end, 100); got != 100 {
		t.Fatalf("clamp got %f", got)
	}
}

func TestAggregation(t *testing.T) {
	start := time.Unix(0, 0)
	got := Aggregate([]Point{{start, 2}, {start.Add(time.Second), 4}, {start.Add(6 * time.Second), 10}}, 5*time.Second)
	if len(got) != 2 || got[0].Avg != 3 || got[0].Min != 2 || got[0].Max != 4 {
		t.Fatalf("got %+v", got)
	}
}

func TestMalformedConfigReturnsDefaults(t *testing.T) {
	c, err := ParseConfig([]byte("{"))
	if err == nil || c.Version != ConfigVersion || c.Sampling.NetworkHz != 60 {
		t.Fatalf("config=%+v err=%v", c, err)
	}
}

func TestConfigValidation(t *testing.T) {
	c := DefaultConfig()
	c.Sampling.NetworkHz = 999
	c.Network.PingTarget = ""
	c.Window.Width = 10
	b, _ := json.Marshal(c)
	got, err := ParseConfig(b)
	if err != nil || got.Sampling.NetworkHz != 60 || got.Window.Width != 960 || got.Network.PingTarget != "1.1.1.1" {
		t.Fatalf("got %+v err=%v", got, err)
	}
}

func TestLegacyConfigMigration(t *testing.T) {
	got, err := ParseConfig([]byte(`{"polling_hz":20,"graph_seconds":120,"units":"bytes","ping_target":"9.9.9.9","ping_interval_sec":5}`))
	if err != nil || got.Version != 2 || got.Sampling.NetworkHz != 20 || got.Network.Units != UnitBytes {
		t.Fatalf("got %+v err=%v", got, err)
	}
}

func TestAlertHysteresisAndCooldown(t *testing.T) {
	a := AlertState{AboveFor: 2 * time.Second, Cooldown: 10 * time.Second}
	now := time.Now()
	if a.Update(96, 95, 90, now) || a.Update(96, 95, 90, now.Add(time.Second)) {
		t.Fatal("alert fired early")
	}
	if !a.Update(96, 95, 90, now.Add(3*time.Second)) {
		t.Fatal("alert did not fire")
	}
	if a.Update(80, 95, 90, now.Add(4*time.Second)) || a.Update(99, 95, 90, now.Add(5*time.Second)) || a.Update(99, 95, 90, now.Add(8*time.Second)) {
		t.Fatal("cooldown failed")
	}
}

func TestProviderFailureIsolation(t *testing.T) {
	var p ProviderState
	p.Failure(assertErr("one"), 3)
	p.Failure(assertErr("two"), 3)
	if p.Disabled {
		t.Fatal("provider disabled too early")
	}
	p.Failure(assertErr("three"), 3)
	if !p.Disabled {
		t.Fatal("provider not disabled")
	}
	p.Success()
	if p.Disabled || p.Failures != 0 {
		t.Fatal("success did not recover")
	}
}

type assertErr string

func (e assertErr) Error() string { return string(e) }

func TestProcessRecordLifetime(t *testing.T) {
	var l ProcessLedger
	now := time.Now()
	l.Update(now, map[uint32]string{42: "game.exe"})
	l.Update(now.Add(time.Second), map[uint32]string{})
	if l.Records[42].ExitedAt.IsZero() {
		t.Fatal("exit not recorded")
	}
	l.Prune(now.Add(2 * time.Second))
	if len(l.Records) != 0 {
		t.Fatal("record not pruned")
	}
}

func TestPressureExplainer(t *testing.T) {
	p := ExplainPressure(PressureInput{GPU: 99, CPU: 40, Memory: 50})
	if p.Key != "gpu" || !strings.Contains(p.Title, "Likely") {
		t.Fatalf("got %+v", p)
	}
}

func TestOptimizerOffersMeasuredPowerExperimentOnlyWhenCPUBound(t *testing.T) {
	got := AnalyzeOptimizations(OptimizationInput{CPU: 92, GPU: 54, Memory: 62, CPUFrequencyMHz: 3100, CPUMaxMHz: 4200, GameFocus: true, PowerPlan: "Balanced"})
	found := false
	for _, item := range got {
		if item.Key == "power-plan" && item.Actionable {
			found = true
		}
	}
	if !found {
		t.Fatal("CPU-bound workload did not produce a power-profile experiment")
	}
	got = AnalyzeOptimizations(OptimizationInput{CPU: 45, GPU: 99, Memory: 62, GameFocus: true, PowerPlan: "Balanced"})
	for _, item := range got {
		if item.Key == "power-plan" {
			t.Fatal("GPU-bound workload should not recommend a CPU power plan")
		}
	}
	got = AnalyzeOptimizations(OptimizationInput{CPU: 94, GPU: 50, GameFocus: true, PowerPlan: "Revision - Ultra Performance"})
	for _, item := range got {
		if item.Key == "power-plan" {
			t.Fatal("performance-oriented custom plan should not be replaced with standard High performance")
		}
	}
}

func TestBenchmarkComparisonRejectsWeakOrDifferentEvidence(t *testing.T) {
	if got := CompareBenchmarks(Benchmark{Samples: 4}, Benchmark{Samples: 20}); got.Verdict != "Not enough evidence" {
		t.Fatalf("weak sample verdict: %+v", got)
	}
	if got := CompareBenchmarks(Benchmark{Samples: 20, CPU: 20, GPU: 15}, Benchmark{Samples: 20, CPU: 80, GPU: 85}); got.Verdict != "Workloads were not comparable" {
		t.Fatalf("different workload verdict: %+v", got)
	}
	if got := CompareBenchmarks(Benchmark{Samples: 30, CPU: 85, GPU: 60, CPUFrequencyMHz: 3000}, Benchmark{Samples: 30, CPU: 86, GPU: 61, CPUFrequencyMHz: 3210}); got.Verdict != "Measured improvement" {
		t.Fatalf("measured gain verdict: %+v", got)
	}
}
