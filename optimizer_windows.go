//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"kerneon/core"
)

const highPerformanceGUID = "8c5e7fda-e8bf-4a96-9a85-a6e23a8c635c"

var powerGUIDPattern = regexp.MustCompile(`(?i)[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)

type OptimizerState struct {
	mu                    sync.RWMutex
	PowerPlan, PowerGUID  string
	HighPerformanceExists bool
	Refreshing            bool
	Error                 string
	Baseline              core.Benchmark
	BaselineAt            time.Time
	AppliedAt             time.Time
	PreviousGUID          string
	PreviousPlan          string
	Applied               bool
	Comparison            core.BenchmarkDelta
}

type optimizerView struct {
	PowerPlan, PowerGUID  string
	HighPerformanceExists bool
	Refreshing            bool
	Error                 string
	Baseline              core.Benchmark
	BaselineAt            time.Time
	AppliedAt             time.Time
	PreviousPlan          string
	Applied               bool
	Comparison            core.BenchmarkDelta
}

func (a *App) optimizerSnapshot() optimizerView {
	a.optimizer.mu.RLock()
	defer a.optimizer.mu.RUnlock()
	return optimizerView{PowerPlan: a.optimizer.PowerPlan, PowerGUID: a.optimizer.PowerGUID, HighPerformanceExists: a.optimizer.HighPerformanceExists, Refreshing: a.optimizer.Refreshing, Error: a.optimizer.Error, Baseline: a.optimizer.Baseline, BaselineAt: a.optimizer.BaselineAt, AppliedAt: a.optimizer.AppliedAt, PreviousPlan: a.optimizer.PreviousPlan, Applied: a.optimizer.Applied, Comparison: a.optimizer.Comparison}
}

func (a *App) refreshPowerPlan() {
	a.optimizer.mu.Lock()
	if a.optimizer.Refreshing {
		a.optimizer.mu.Unlock()
		return
	}
	a.optimizer.Refreshing = true
	a.optimizer.mu.Unlock()
	go func() {
		guid, name, err := activePowerPlan()
		exists := powerPlanExists(highPerformanceGUID)
		a.optimizer.mu.Lock()
		a.optimizer.Refreshing = false
		if err != nil {
			a.optimizer.Error = err.Error()
		} else {
			a.optimizer.PowerGUID, a.optimizer.PowerPlan, a.optimizer.HighPerformanceExists, a.optimizer.Error = guid, name, exists, ""
		}
		a.optimizer.mu.Unlock()
		if a.hwnd != 0 {
			procPostMessageW.Call(a.hwnd, WM_APP_RENDER, 0, 0)
		}
	}()
}

func runPowercfg(args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "powercfg.exe", args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return out, ctx.Err()
	}
	if err != nil {
		return out, fmt.Errorf("powercfg %s: %s", strings.Join(args, " "), strings.TrimSpace(string(out)))
	}
	return out, nil
}

func activePowerPlan() (guid, name string, err error) {
	out, err := runPowercfg("/getactivescheme")
	if err != nil {
		return "", "", err
	}
	guid = strings.ToLower(powerGUIDPattern.FindString(string(out)))
	if guid == "" {
		return "", "", errors.New("Windows did not report an active power plan")
	}
	name = "Active Windows plan"
	text := string(out)
	if left, right := strings.LastIndex(text, "("), strings.LastIndex(text, ")"); left >= 0 && right > left {
		name = strings.TrimSpace(text[left+1 : right])
	}
	return guid, name, nil
}

func powerPlanExists(guid string) bool {
	out, err := runPowercfg("/list")
	return err == nil && strings.Contains(strings.ToLower(string(out)), strings.ToLower(guid))
}

func (a *App) captureOptimizationBaseline() {
	b := benchmarkFromSnapshot(a.engine.Snapshot(), time.Now().Add(-60*time.Second))
	a.optimizer.mu.Lock()
	a.optimizer.Baseline, a.optimizer.BaselineAt = b, time.Now()
	a.optimizer.Comparison = core.BenchmarkDelta{}
	a.optimizer.Error = ""
	a.optimizer.mu.Unlock()
	a.engine.AddUserEvent("optimize", "Optimization baseline captured", fmt.Sprintf("%d synchronized samples recorded before any system change.", b.Samples), 0)
}

func benchmarkFromSnapshot(s Snapshot, since time.Time) core.Benchmark {
	var b core.Benchmark
	for _, h := range s.History {
		if h.At.Before(since) {
			continue
		}
		b.Samples++
		b.CPU += h.CPU
		b.GPU += h.GPU
		b.Memory += h.Memory
		b.Disk += h.Disk
		b.Latency += h.Latency
	}
	if n := float64(b.Samples); n > 0 {
		b.CPU /= n
		b.GPU /= n
		b.Memory /= n
		b.Disk /= n
		b.Latency /= n
	}
	b.CPUFrequencyMHz = s.CPU.FrequencyMHz
	return b
}

func (a *App) applyPerformancePlan() {
	view := a.optimizerSnapshot()
	if view.Baseline.Samples < 10 {
		a.optimizer.mu.Lock()
		a.optimizer.Error = "Capture at least 10 baseline samples before changing the power plan."
		a.optimizer.mu.Unlock()
		return
	}
	if !view.HighPerformanceExists {
		a.optimizer.mu.Lock()
		a.optimizer.Error = "High performance is not exposed on this Windows device; no change was made."
		a.optimizer.mu.Unlock()
		return
	}
	planName := strings.ToLower(view.PowerPlan)
	if strings.Contains(planName, "performance") || strings.Contains(planName, "ultimate") || strings.Contains(planName, "ultra") {
		a.optimizer.mu.Lock()
		a.optimizer.Error = "The active plan is already performance-oriented; Kerneon will not replace it with a generic plan."
		a.optimizer.mu.Unlock()
		return
	}
	if view.Baseline.CPU < 70 || (view.Baseline.GPU >= 90 && view.Baseline.GPU >= view.Baseline.CPU-8) {
		a.optimizer.mu.Lock()
		a.optimizer.Error = "The baseline does not show a CPU-heavy limit, so a CPU power-plan change is not justified."
		a.optimizer.mu.Unlock()
		return
	}
	if strings.EqualFold(view.PowerGUID, highPerformanceGUID) {
		a.optimizer.mu.Lock()
		a.optimizer.Error = "High performance is already active; there is no plan change to test."
		a.optimizer.mu.Unlock()
		return
	}
	a.config.Tuning.PendingPowerPlan = view.PowerGUID
	if err := a.store.Save(a.config); err != nil {
		a.config.Tuning.PendingPowerPlan = ""
		a.optimizer.mu.Lock()
		a.optimizer.Error = "Rollback journal could not be saved; no power-plan change was made: " + err.Error()
		a.optimizer.mu.Unlock()
		return
	}
	if _, err := runPowercfg("/setactive", highPerformanceGUID); err != nil {
		a.config.Tuning.PendingPowerPlan = ""
		_ = a.store.Save(a.config)
		a.optimizer.mu.Lock()
		a.optimizer.Error = err.Error()
		a.optimizer.mu.Unlock()
		return
	}
	a.optimizer.mu.Lock()
	a.optimizer.PreviousGUID, a.optimizer.PreviousPlan = view.PowerGUID, view.PowerPlan
	a.optimizer.PowerGUID, a.optimizer.PowerPlan = highPerformanceGUID, "High performance"
	a.optimizer.Applied, a.optimizer.AppliedAt = true, time.Now()
	a.optimizer.Comparison, a.optimizer.Error = core.BenchmarkDelta{}, ""
	a.optimizer.mu.Unlock()
	a.engine.AddUserEvent("optimize", "High performance applied for verification", "The previous power plan was preserved for one-click rollback.", 1)
}

func (a *App) compareOptimizationRun() {
	view := a.optimizerSnapshot()
	if !view.Applied || view.AppliedAt.IsZero() {
		return
	}
	after := benchmarkFromSnapshot(a.engine.Snapshot(), view.AppliedAt)
	comparison := core.CompareBenchmarks(view.Baseline, after)
	a.optimizer.mu.Lock()
	a.optimizer.Comparison, a.optimizer.Error = comparison, ""
	a.optimizer.mu.Unlock()
	a.engine.AddUserEvent("optimize", comparison.Verdict, comparison.Detail, func() int {
		if comparison.Verdict == "Measured improvement" {
			return 0
		}
		return 1
	}())
}

func (a *App) rollbackOptimization() {
	view := a.optimizerSnapshot()
	if !view.Applied || view.PowerGUID == "" {
		return
	}
	a.optimizer.mu.RLock()
	previousGUID := a.optimizer.PreviousGUID
	a.optimizer.mu.RUnlock()
	if previousGUID == "" {
		return
	}
	if _, err := runPowercfg("/setactive", previousGUID); err != nil {
		a.optimizer.mu.Lock()
		a.optimizer.Error = err.Error()
		a.optimizer.mu.Unlock()
		return
	}
	a.optimizer.mu.Lock()
	a.optimizer.PowerGUID, a.optimizer.PowerPlan = previousGUID, a.optimizer.PreviousPlan
	a.optimizer.Applied, a.optimizer.AppliedAt = false, time.Time{}
	a.optimizer.Error = ""
	a.optimizer.mu.Unlock()
	a.config.Tuning.PendingPowerPlan = ""
	a.saveAndRestart(false)
	a.engine.AddUserEvent("optimize", "Power plan rolled back", "The pre-test Windows power plan was restored.", 0)
}

func (a *App) keepOptimization() {
	view := a.optimizerSnapshot()
	if !view.Applied || view.Comparison.Verdict != "Measured improvement" {
		a.optimizer.mu.Lock()
		a.optimizer.Error = "Kerneon will only mark this change as kept after a measured improvement."
		a.optimizer.mu.Unlock()
		return
	}
	a.optimizer.mu.Lock()
	a.optimizer.Applied, a.optimizer.PreviousGUID, a.optimizer.PreviousPlan = false, "", ""
	a.optimizer.mu.Unlock()
	a.config.Tuning.PendingPowerPlan = ""
	a.saveAndRestart(false)
	a.engine.AddUserEvent("optimize", "Verified power plan kept", view.Comparison.Detail, 0)
}

func (a *App) restoreInterruptedOptimization() {
	guid := strings.TrimSpace(a.config.Tuning.PendingPowerPlan)
	if guid == "" || !powerGUIDPattern.MatchString(guid) {
		return
	}
	if _, err := runPowercfg("/setactive", guid); err != nil {
		a.logger.Error("optimize", "restore interrupted power-plan experiment", err)
		return
	}
	a.config.Tuning.PendingPowerPlan = ""
	if err := a.store.Save(a.config); err != nil {
		a.logger.Error("optimize", "clear rollback journal", err)
	}
	a.logger.Info("optimize", "restored power plan from interrupted experiment")
}
