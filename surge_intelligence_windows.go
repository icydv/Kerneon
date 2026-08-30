//go:build windows

package main

import (
	"fmt"
	"sort"

	"kerneon/core"
)

type SurgeRoute struct {
	Key, Title, Explanation, Evidence string
	ControlPath                       string
	Confidence                        string
}

type SurgeContention struct {
	Detected, Actionable bool
	Title, Detail        string
	TopName              string
	TopCPU, CombinedCPU  float64
}

type SurgeStutterDiagnosis struct {
	Key, Title, Explanation, Evidence, Confidence string
}

// surgeLocalPressure intentionally excludes internet latency, jitter and
// packet loss. Those signals matter on the Network page, but they do not
// identify a local frame-production bottleneck that Surge can remediate.
func surgeLocalPressure(cpu, gpu, memory, disk float64) core.Pressure {
	return core.ExplainPressure(core.PressureInput{CPU: cpu, GPU: gpu, Memory: memory, Disk: disk})
}

func diagnoseSurgeStutter(frame FrameStats, latency LatencyData, disk DiskData, memory MemoryData, contention SurgeContention) SurgeStutterDiagnosis {
	evidence := fmt.Sprintf("p99 %.1f ms · worst %.1f ms · %.1f hitches/min · pacing CV %.2f", frame.P99FrameMs, frame.WorstFrameMs, frame.HitchesPerMinute, frame.FramePacingCV)
	if frame.Samples < 120 || frame.FPS <= 0 {
		return SurgeStutterDiagnosis{Key: "collecting", Title: "Building the long-frame baseline", Explanation: "Surge needs enough presented frames before a stutter pattern can be separated from a scene transition or loading screen.", Evidence: evidence, Confidence: "low"}
	}
	if frame.HitchesPerMinute < 2 && frame.FramePacingCV < 0.12 && frame.DroppedPercent < 0.5 {
		return SurgeStutterDiagnosis{Key: "stable", Title: "Frame pacing is presently stable", Explanation: "The slow tail is close to the normal frame rhythm; no stutter remediation is eligible from this window.", Evidence: evidence, Confidence: "high"}
	}
	if latency.Available && (latency.DPCTimePercent >= 1.5 || latency.InterruptTimePercent >= 1.5) {
		return SurgeStutterDiagnosis{Key: "driver-latency", Title: "Driver or interrupt latency aligns with the hitch window", Explanation: "Elevated DPC/interrupt service can pause time-sensitive game and audio work. Surge should preserve an ETW trace and identify the driver before recommending a change.", Evidence: evidence + fmt.Sprintf(" · DPC %.2f%% · ISR %.2f%% · %.0f interrupts/s", latency.DPCTimePercent, latency.InterruptTimePercent, latency.InterruptsPerSec), Confidence: "medium"}
	}
	if disk.LatencyMs >= 20 || disk.Queue >= 2.0 {
		return SurgeStutterDiagnosis{Key: "storage", Title: "Storage delivery aligns with long frames", Explanation: "Asset reads, decompression or paging may be making frame production wait. Surge should correlate the locked executable and top I/O process before acting.", Evidence: evidence + fmt.Sprintf(" · disk %.1f ms · queue %.1f · page reads %.0f/s", disk.LatencyMs, disk.Queue, latency.PageReadsPerSec), Confidence: "medium"}
	}
	if memory.UsagePercent >= 92 || memory.CommitPercent >= 90 || (latency.Available && latency.PageReadsPerSec >= 50) {
		return SurgeStutterDiagnosis{Key: "memory", Title: "Memory pressure aligns with long frames", Explanation: "Commit or page-in pressure can create intermittent stalls. Clearing caches is not a remedy; Surge should identify the actual consumer and preserve useful cache when possible.", Evidence: evidence + fmt.Sprintf(" · memory %.0f%% · commit %.0f%% · page reads %.0f/s", memory.UsagePercent, memory.CommitPercent, latency.PageReadsPerSec), Confidence: "medium"}
	}
	if contention.Actionable {
		return SurgeStutterDiagnosis{Key: "contention", Title: "Background contention aligns with frame instability", Explanation: "A repeatable A/B isolation window is eligible for user-space contenders; protected and system processes remain untouched.", Evidence: evidence + " · " + contention.Detail, Confidence: "medium"}
	}
	return SurgeStutterDiagnosis{Key: "engine-driver", Title: "Long frames are real; the cause is not yet isolated", Explanation: "Shader compilation, asset streaming, engine locks, driver queues and scene changes remain candidates. Surge will trace rather than apply a generic tweak.", Evidence: evidence, Confidence: "low"}
}

func determineSurgeRoute(frame FrameStats, gpu SurgeGPUTelemetry, totalCPU, fallbackGPU float64, refreshHz int) SurgeRoute {
	gpuUtil := fallbackGPU
	if gpu.Ready {
		gpuUtil = gpu.GPUUtil
	}
	evidence := fmt.Sprintf("CPU %.0f%% · GPU %.0f%%", totalCPU, gpuUtil)
	if frame.Error == "" && frame.Samples >= 30 && frame.FPS > 0 && frame.AverageRenderMs > 0 {
		frameTime := 1000 / frame.FPS
		renderShare := frame.AverageRenderMs / frameTime
		evidence = fmt.Sprintf("%.1f FPS · render %.2f/%.2f ms · GPU %.0f%%", frame.FPS, frame.AverageRenderMs, frameTime, gpuUtil)
		if refreshHz > 0 && frame.FPS >= float64(refreshHz)*0.97 && renderShare < 0.82 {
			return SurgeRoute{Key: "ceiling", Title: "Display or frame ceiling reached", Explanation: "The renderer has headroom while delivery is already at the display boundary. More clocks are not an eligible action.", Evidence: evidence, ControlPath: "presentation", Confidence: "high"}
		}
		if renderShare >= 0.82 || gpuUtil >= 92 {
			powerRatio := 0.0
			if gpu.PowerLimitW > 0 {
				powerRatio = gpu.PowerW / gpu.PowerLimitW
			}
			if gpu.ThrottleReasons&(nvmlReasonSWThermal|nvmlReasonHWthermal) != 0 || gpu.TemperatureC >= 84 {
				return SurgeRoute{Key: "gpu-thermal", Title: "GPU throughput is thermally constrained", Explanation: "Cooling and the vendor thermal envelope are the relevant path; CPU scheduling changes are not.", Evidence: evidence + fmt.Sprintf(" · %.0f°C · %s", gpu.TemperatureC, gpu.ThrottleSummary), ControlPath: "vendor-gpu-thermal", Confidence: "high"}
			}
			if gpu.ThrottleReasons&nvmlReasonPowerCap != 0 && powerRatio >= 0.90 {
				return SurgeRoute{Key: "gpu-power", Title: "GPU throughput is meeting its power envelope", Explanation: "A supported vendor power/clock experiment may be eligible only inside reported device limits and with privileged rollback.", Evidence: evidence + fmt.Sprintf(" · %.0f/%.0f W", gpu.PowerW, gpu.PowerLimitW), ControlPath: "vendor-gpu-power", Confidence: "high"}
			}
			return SurgeRoute{Key: "gpu-throughput", Title: "GPU throughput is the active route", Explanation: "Native vendor controls and graphics workload are relevant; generic CPU priority changes are not the answer.", Evidence: evidence, ControlPath: "vendor-gpu", Confidence: "high"}
		}
		if renderShare <= 0.67 {
			return SurgeRoute{Key: "cpu-engine", Title: "CPU, engine or frame-production route", Explanation: "The GPU finishes well before the next frame. Surge should investigate the main thread, background contention, game settings and caps—not add GPU heat.", Evidence: evidence, ControlPath: "cpu-engine-contention", Confidence: "high"}
		}
		return SurgeRoute{Key: "mixed", Title: "Mixed frame-production route", Explanation: "No single control path dominates yet. Surge should collect a longer comparable window before mutating hardware.", Evidence: evidence, ControlPath: "observe", Confidence: "medium"}
	}
	if gpuUtil >= 92 {
		return SurgeRoute{Key: "gpu-provisional", Title: "Provisional GPU pressure", Explanation: "GPU utilization is high, but frame proof is required before a vendor tuning path can unlock.", Evidence: evidence, ControlPath: "observe", Confidence: "low"}
	}
	if totalCPU >= 85 && gpuUtil < 85 {
		return SurgeRoute{Key: "cpu-provisional", Title: "Provisional CPU pressure", Explanation: "System CPU pressure is high while GPU pressure is lower; frame proof will decide whether this affects delivery.", Evidence: evidence, ControlPath: "observe", Confidence: "low"}
	}
	return SurgeRoute{Key: "waiting", Title: "Waiting for a comparable game window", Explanation: "Surge has not collected enough frame evidence to choose a control path honestly.", Evidence: evidence, ControlPath: "observe", Confidence: "low"}
}

func analyzeSurgeContention(processes []ProcessMetric, gamePID uint32) SurgeContention {
	type candidate struct {
		name string
		cpu  float64
	}
	candidates := make([]candidate, 0, len(processes))
	combined := 0.0
	for _, process := range processes {
		if process.PID == 0 || process.PID == gamePID || surgeProcessProtected(process.Name) || process.CPU < 2.5 {
			continue
		}
		candidates = append(candidates, candidate{name: process.Name, cpu: process.CPU})
		combined += process.CPU
	}
	if len(candidates) == 0 {
		return SurgeContention{Title: "No material user-space contention", Detail: "No non-game process currently exceeds the diagnostic threshold."}
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].cpu > candidates[j].cpu })
	top := candidates[0]
	finding := SurgeContention{Detected: true, TopName: top.name, TopCPU: top.cpu, CombinedCPU: combined}
	finding.Actionable = top.cpu >= 5 || combined >= 10
	if finding.Actionable {
		finding.Title = "Background contention is worth an A/B isolation test"
		finding.Detail = fmt.Sprintf("%s %.1f%% · eligible user-space contenders %.1f%% combined", top.name, top.cpu, combined)
	} else {
		finding.Title = "Background work is visible but unlikely to be decisive"
		finding.Detail = fmt.Sprintf("%s %.1f%% · %.1f%% combined", top.name, top.cpu, combined)
	}
	return finding
}
