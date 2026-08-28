package core

import (
	"fmt"
	"math"
	"strings"
)

type OptimizationInput struct {
	CPU, GPU, Memory, Disk, Latency, PacketLoss float64
	CPUFrequencyMHz, CPUMaxMHz                  float64
	TopProcess                                  string
	TopProcessCPU                               float64
	GameFocus                                   bool
	PowerPlan                                   string
}

type OptimizationFinding struct {
	Key, Title, Detail, Evidence, Recommendation string
	Severity                                     int
	Actionable                                   bool
}

func AnalyzeOptimizations(v OptimizationInput) []OptimizationFinding {
	findings := make([]OptimizationFinding, 0, 5)
	pressure := ExplainPressure(PressureInput{CPU: v.CPU, GPU: v.GPU, Memory: v.Memory, Disk: v.Disk, Latency: v.Latency, PacketLoss: v.PacketLoss})
	findings = append(findings, OptimizationFinding{
		Key: "bottleneck", Title: pressure.Title, Detail: pressure.Explanation,
		Evidence: fmt.Sprintf("CPU %.0f%% · GPU %.0f%% · memory %.0f%% · disk %.0f%%", v.CPU, v.GPU, v.Memory, v.Disk), Severity: pressure.Severity,
	})

	plan := v.PowerPlan
	if plan == "" {
		plan = "Unknown"
	}
	cpuBound := v.CPU >= 75 && (v.GPU < 90 || v.CPU > v.GPU+8)
	planAlreadyPerformance := strings.Contains(strings.ToLower(plan), "performance") || strings.Contains(strings.ToLower(plan), "ultimate") || strings.Contains(strings.ToLower(plan), "ultra")
	if cpuBound && !planAlreadyPerformance {
		detail := "A performance-oriented power profile can reduce CPU ramp-up and down-clock latency on some systems. It cannot create extra cores or fix a GPU limit."
		if v.CPUMaxMHz > 0 && v.CPUFrequencyMHz > 0 {
			detail += fmt.Sprintf(" Current clock is %.0f MHz of a %.0f MHz reported maximum.", v.CPUFrequencyMHz, v.CPUMaxMHz)
		}
		findings = append(findings, OptimizationFinding{
			Key: "power-plan", Title: "Run a power-profile experiment", Detail: detail,
			Evidence:       "Current plan: " + plan + "; CPU is carrying the measured load.",
			Recommendation: "Capture a repeatable gameplay segment, apply High performance, repeat it, then keep the change only if the telemetry improves.", Actionable: true, Severity: 1,
		})
	}

	if !v.GameFocus {
		findings = append(findings, OptimizationFinding{
			Key: "game-focus", Title: "Reduce Kerneon's own game-time work",
			Detail:   "Game Focus drops nonessential Kerneon refresh work while another fullscreen application is active.",
			Evidence: "Game Focus is currently off.", Recommendation: "Enable Game Focus and verify Kerneon's process CPU during the same scene.", Actionable: true,
		})
	}

	if v.TopProcess != "" && v.TopProcessCPU >= 12 {
		findings = append(findings, OptimizationFinding{
			Key: "contention", Title: "Background CPU contention is measurable",
			Detail:   "Kerneon will identify the process but will not terminate or deprioritize it automatically; doing so can corrupt work or destabilize software.",
			Evidence: fmt.Sprintf("%s is using %.1f%% CPU.", v.TopProcess, v.TopProcessCPU), Recommendation: "Inspect the process, confirm it is nonessential, then close it normally before repeating the benchmark.", Severity: 1,
		})
	}

	if v.Memory >= 88 {
		findings = append(findings, OptimizationFinding{
			Key: "memory", Title: "Memory pressure needs capacity, not a cleaner",
			Detail:   "Standby-list purges and generic RAM cleaners usually discard useful cache. Kerneon deliberately does not offer them.",
			Evidence: fmt.Sprintf("Physical memory is %.0f%% used.", v.Memory), Recommendation: "Close a verified high-memory workload or reduce the game's texture/cache setting, then compare paging and frame consistency.", Severity: 2,
		})
	}

	if v.PacketLoss >= 2 || v.Latency >= 100 {
		findings = append(findings, OptimizationFinding{
			Key: "network", Title: "Connection quality is the limiting signal",
			Detail:   "A registry 'network boost' cannot repair loss or congestion outside this PC, so Kerneon will not apply one.",
			Evidence: fmt.Sprintf("Latency %.0f ms · packet loss %.0f%%.", v.Latency, v.PacketLoss), Recommendation: "Compare Ethernet versus Wi-Fi and repeat the same target test before changing drivers or router settings.", Severity: 2,
		})
	}

	return findings
}

type Benchmark struct {
	Samples                         int
	CPU, GPU, Memory, Disk, Latency float64
	CPUFrequencyMHz                 float64
}

type BenchmarkDelta struct {
	Verdict, Detail                 string
	CPU, GPU, Memory, Disk, Latency float64
}

func CompareBenchmarks(before, after Benchmark) BenchmarkDelta {
	d := BenchmarkDelta{
		CPU: after.CPU - before.CPU, GPU: after.GPU - before.GPU,
		Memory: after.Memory - before.Memory, Disk: after.Disk - before.Disk,
		Latency: after.Latency - before.Latency,
	}
	if before.Samples < 10 || after.Samples < 10 {
		d.Verdict = "Not enough evidence"
		d.Detail = "Each side needs at least 10 synchronized samples from a comparable workload."
		return d
	}
	workloadGap := math.Abs(after.GPU-before.GPU) + math.Abs(after.CPU-before.CPU)
	if workloadGap > 35 {
		d.Verdict = "Workloads were not comparable"
		d.Detail = "CPU and GPU demand changed too much to attribute the difference to the tuning change."
		return d
	}
	clockGain := 0.0
	if before.CPUFrequencyMHz > 0 {
		clockGain = (after.CPUFrequencyMHz - before.CPUFrequencyMHz) / before.CPUFrequencyMHz * 100
	}
	if clockGain >= 3 && after.CPU >= before.CPU-8 {
		d.Verdict = "Measured improvement"
		d.Detail = fmt.Sprintf("CPU clock increased %.1f%% under a broadly comparable measured load.", clockGain)
	} else {
		d.Verdict = "Improvement not proven"
		d.Detail = "The repeat run did not show a material CPU-clock improvement. Rollback is the honest default."
	}
	return d
}
