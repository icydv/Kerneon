//go:build windows

package main

import (
	"encoding/json"
	"fmt"
	"math"
	"math/bits"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
	"unsafe"
)

const (
	processMemoryPriority           = 0
	processPowerThrottling          = 4
	processPowerThrottlingVersion   = 1
	processPowerThrottlingExecution = 0x1
	surgeBaselineDuration           = 30 * time.Second
	surgeProofSettleDuration        = 3 * time.Second
	surgeProofWindowDuration        = 20 * time.Second
	surgeProofWindowStride          = 24 * time.Second
)

var (
	procGetSystemPowerStatus   = kernel32.NewProc("GetSystemPowerStatus")
	procGetProcessAffinityMask = kernel32.NewProc("GetProcessAffinityMask")
	procSetProcessAffinityMask = kernel32.NewProc("SetProcessAffinityMask")
)

type processPowerThrottlingState struct {
	Version, ControlMask, StateMask uint32
}

type processMemoryPriorityState struct {
	MemoryPriority uint32
}

type systemPowerStatus struct {
	ACLineStatus, BatteryFlag, BatteryLifePercent, SystemStatusFlag byte
	BatteryLifeTime, BatteryFullLifeTime                            uint32
}

type RemediationRecord struct {
	At        time.Time `json:"at"`
	ID        string    `json:"id"`
	Status    string    `json:"status"`
	Painpoint string    `json:"painpoint"`
	Evidence  string    `json:"evidence"`
	Previous  string    `json:"previous"`
	Action    string    `json:"action"`
	Rollback  string    `json:"rollback"`
}

type autopilotSessionSnapshot struct {
	PID           uint32                      `json:"pid"`
	Name          string                      `json:"name"`
	Path          string                      `json:"path"`
	Started       time.Time                   `json:"started"`
	Priority      uint32                      `json:"priority"`
	PowerQoS      processPowerThrottlingState `json:"power_qos"`
	Memory        processMemoryPriorityState  `json:"memory"`
	Affinity      uint64                      `json:"affinity"`
	PriorityValid bool                        `json:"priority_valid"`
	PowerQoSValid bool                        `json:"power_qos_valid"`
	MemoryValid   bool                        `json:"memory_valid"`
	AffinityValid bool                        `json:"affinity_valid"`
	Background    []backgroundProcessSnapshot `json:"background,omitempty"`
	NVIDIA        []NVAPISettingSnapshot      `json:"nvidia,omitempty"`
	CapturedAt    time.Time                   `json:"captured_at"`
}

type AutopilotState struct {
	mu                  sync.RWMutex
	Records             []RemediationRecord
	Status              string
	GamePID             uint32
	GameName            string
	PriorityBefore      uint32
	PowerQoSBefore      processPowerThrottlingState
	MemoryBefore        processMemoryPriorityState
	AffinityBefore      uintptr
	SystemAffinity      uintptr
	PriorityChanged     bool
	PowerQoSChanged     bool
	MemoryChanged       bool
	AffinityChanged     bool
	PlanChanged         bool
	PreviousPlanGUID    string
	PreviousPlanName    string
	TemporaryPlanGUID   string
	LastEvaluation      time.Time
	SessionStarted      time.Time
	AppliedAt           time.Time
	ActionsApplied      bool
	FrameBaseline       FrameStats
	FrameProofDone      bool
	FrameProofChecks    int
	FrameComparable     int
	FrameRegressions    int
	FrameImprovements   int
	FrameBenefitCounts  map[string]int
	FrameProofWindows   []FrameStats
	ExperimentalChanges bool
	RejectedPID         uint32
	CPUCapabilityLogged bool
	PerformanceChecked  bool
	AntiCheatDetected   bool
	AntiCheatProvider   string
	Background          []backgroundProcessSnapshot
	NVIDIA              []NVAPISettingSnapshot
	NVIDIAChanged       bool
	HardwareBaseline    surgeHardwareBaseline
}

type surgeMetricBaseline struct {
	Value   float64
	Samples int
}

type surgeHardwareBaseline struct {
	CPUClockMHz, GPUClockMHz, VRAMClockMHz surgeMetricBaseline
	GPUPowerLimitW, GPUTemperatureC        surgeMetricBaseline
}

func addSurgeBaselineSample(metric surgeMetricBaseline, value float64) surgeMetricBaseline {
	if value <= 0 || math.IsNaN(value) || math.IsInf(value, 0) {
		return metric
	}
	metric.Samples++
	metric.Value += (value - metric.Value) / float64(metric.Samples)
	return metric
}

var powerACSettingPattern = regexp.MustCompile(`(?i)Current AC Power Setting Index:\s*0x([0-9a-f]+)`)

type autopilotView struct {
	Records             []RemediationRecord
	Status              string
	GamePID             uint32
	GameName            string
	Active              bool
	ActionsApplied      bool
	FrameProofDone      bool
	FrameProofChecks    int
	FrameComparable     int
	FrameImprovements   int
	ExperimentalChanges bool
	FrameBaseline       FrameStats
	FrameAfter          FrameStats
	FrameBenefitCounts  map[string]int
	HardwareBaseline    surgeHardwareBaseline
}

func (a *App) autopilotSnapshot() autopilotView {
	a.autopilot.mu.RLock()
	defer a.autopilot.mu.RUnlock()
	benefits := make(map[string]int, len(a.autopilot.FrameBenefitCounts))
	for metric, count := range a.autopilot.FrameBenefitCounts {
		benefits[metric] = count
	}
	return autopilotView{
		Records: append([]RemediationRecord(nil), a.autopilot.Records...), Status: a.autopilot.Status,
		GamePID: a.autopilot.GamePID, GameName: a.autopilot.GameName, Active: a.autopilot.GamePID != 0,
		ActionsApplied: a.autopilot.ActionsApplied, FrameProofDone: a.autopilot.FrameProofDone,
		FrameProofChecks: a.autopilot.FrameProofChecks, FrameComparable: a.autopilot.FrameComparable,
		FrameImprovements: a.autopilot.FrameImprovements, ExperimentalChanges: a.autopilot.ExperimentalChanges,
		FrameBaseline: a.autopilot.FrameBaseline, FrameAfter: averageFrameStats(a.autopilot.FrameProofWindows),
		FrameBenefitCounts: benefits, HardwareBaseline: a.autopilot.HardwareBaseline,
	}
}

func (a *App) sampleAutopilotHardwareBaseline() {
	snapshot := a.engine.Snapshot()
	gpu := a.surgeStack.Snapshot().GPU
	if !gpu.Ready {
		gpu = a.surgeStack.nvml.Snapshot()
	}
	a.autopilot.mu.Lock()
	defer a.autopilot.mu.Unlock()
	if a.autopilot.GamePID == 0 || a.autopilot.ActionsApplied {
		return
	}
	baseline := &a.autopilot.HardwareBaseline
	baseline.CPUClockMHz = addSurgeBaselineSample(baseline.CPUClockMHz, snapshot.CPU.FrequencyMHz)
	if gpu.Ready {
		baseline.GPUClockMHz = addSurgeBaselineSample(baseline.GPUClockMHz, float64(gpu.GraphicsClockMHz))
		baseline.VRAMClockMHz = addSurgeBaselineSample(baseline.VRAMClockMHz, float64(gpu.MemoryClockMHz))
		baseline.GPUPowerLimitW = addSurgeBaselineSample(baseline.GPUPowerLimitW, gpu.PowerLimitW)
		baseline.GPUTemperatureC = addSurgeBaselineSample(baseline.GPUTemperatureC, gpu.TemperatureC)
	}
}

func (a *App) autopilotMutationApplied() bool {
	a.autopilot.mu.RLock()
	defer a.autopilot.mu.RUnlock()
	if a.autopilot.PriorityChanged || a.autopilot.PowerQoSChanged || a.autopilot.MemoryChanged || a.autopilot.AffinityChanged || a.autopilot.PlanChanged || a.autopilot.NVIDIAChanged {
		return true
	}
	for _, background := range a.autopilot.Background {
		if background.PriorityChanged || background.PowerQoSChanged {
			return true
		}
	}
	return false
}

func (a *App) remediationDir() string {
	return filepath.Join(a.logger.Dir(), "remediation")
}

func (a *App) autopilotSnapshotPath() string {
	return filepath.Join(a.remediationDir(), "active-session.json")
}

func queryProcessStarted(pid uint32) time.Time {
	handle, _, _ := procOpenProcess.Call(PROCESS_QUERY_LIMITED_INFORMATION, 0, uintptr(pid))
	if handle == 0 {
		return time.Time{}
	}
	defer procCloseHandle.Call(handle)
	var create, exit, kernel, user filetime
	if ok, _, _ := procGetProcessTimes.Call(handle, uintptr(unsafe.Pointer(&create)), uintptr(unsafe.Pointer(&exit)), uintptr(unsafe.Pointer(&kernel)), uintptr(unsafe.Pointer(&user))); ok == 0 {
		return time.Time{}
	}
	return filetimeToTime(create.Uint64())
}

func (a *App) persistAutopilotSnapshot(snapshot autopilotSessionSnapshot) error {
	if snapshot.Path == "" || snapshot.Started.IsZero() {
		return fmt.Errorf("process identity could not be captured safely")
	}
	if err := os.MkdirAll(a.remediationDir(), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return err
	}
	temporary := a.autopilotSnapshotPath() + ".tmp"
	file, err := os.OpenFile(temporary, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err = file.Write(data); err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(temporary)
		return err
	}
	return os.Rename(temporary, a.autopilotSnapshotPath())
}

func (a *App) restoreInterruptedAutopilotSnapshot() {
	data, err := os.ReadFile(a.autopilotSnapshotPath())
	if os.IsNotExist(err) {
		return
	}
	if err != nil {
		a.logger.Error("autopilot", "read active-session snapshot", err)
		return
	}
	var snapshot autopilotSessionSnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		a.logger.Error("autopilot", "decode active-session snapshot", err)
		return
	}
	errors := make([]string, 0, 4+len(snapshot.Background)+len(snapshot.NVIDIA))
	// Driver profile changes outlive a process crash, so restore them even if
	// the original game has already exited.
	for _, setting := range snapshot.NVIDIA {
		if err := a.surgeStack.nvapi.restoreApplicationDWORD(setting); err != nil {
			errors = append(errors, fmt.Sprintf("NVIDIA setting 0x%08X: %v", setting.SettingID, err))
		}
	}
	path, started := queryProcessPath(snapshot.PID), queryProcessStarted(snapshot.PID)
	identityMatches := path != "" && !started.IsZero() && strings.EqualFold(path, snapshot.Path) && started.Sub(snapshot.Started) <= time.Second && snapshot.Started.Sub(started) <= time.Second
	if identityMatches {
		if snapshot.PowerQoSValid {
			if err := setProcessPowerQoS(snapshot.PID, snapshot.PowerQoS); err != nil {
				errors = append(errors, "power QoS: "+err.Error())
			}
		}
		if snapshot.PriorityValid {
			if err := setProcessPriority(snapshot.PID, snapshot.Priority); err != nil {
				errors = append(errors, "priority: "+err.Error())
			}
		}
		if snapshot.MemoryValid {
			if err := setProcessMemoryPriorityState(snapshot.PID, snapshot.Memory); err != nil {
				errors = append(errors, "memory: "+err.Error())
			}
		}
		if snapshot.AffinityValid {
			if err := setProcessAffinityState(snapshot.PID, uintptr(snapshot.Affinity)); err != nil {
				errors = append(errors, "affinity: "+err.Error())
			}
		}
	} else if path != "" {
		// A PID may have been recycled. Persistent state still restores, but a
		// different process never receives the captured process fields.
		a.logger.Error("autopilot", "refused process snapshot restore", fmt.Errorf("PID %d no longer matches the captured game identity", snapshot.PID))
	}
	for _, background := range snapshot.Background {
		if !backgroundIdentityMatches(background) {
			continue
		}
		if background.PowerQoSValid {
			if err := setProcessPowerQoS(background.PID, background.PowerQoS); err != nil {
				errors = append(errors, background.Name+" power QoS: "+err.Error())
			}
		}
		if background.PriorityValid {
			if err := setProcessPriority(background.PID, background.Priority); err != nil {
				errors = append(errors, background.Name+" priority: "+err.Error())
			}
		}
	}
	if len(errors) > 0 {
		a.logger.Error("autopilot", "restore active-session snapshot", fmt.Errorf("%s", strings.Join(errors, "; ")))
		return
	}
	_ = os.Remove(a.autopilotSnapshotPath())
}

func (a *App) recordAutopilot(id, status, painpoint, evidence, previous, action, rollback string) bool {
	record := RemediationRecord{At: time.Now(), ID: id, Status: status, Painpoint: painpoint, Evidence: evidence, Previous: previous, Action: action, Rollback: rollback}
	dir := a.remediationDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		a.logger.Error("autopilot", "create remediation log directory", err)
		return false
	}
	data, err := json.Marshal(record)
	if err != nil {
		return false
	}
	path := filepath.Join(dir, "autopilot-"+record.At.Format("20060102")+".jsonl")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		a.logger.Error("autopilot", "open remediation log", err)
		return false
	}
	_, writeErr := file.Write(append(data, '\n'))
	if writeErr == nil {
		writeErr = file.Sync()
	}
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		a.logger.Error("autopilot", "persist remediation record", fmt.Errorf("write: %v; close: %v", writeErr, closeErr))
		return false
	}
	a.autopilot.mu.Lock()
	a.autopilot.Records = append([]RemediationRecord{record}, a.autopilot.Records...)
	if len(a.autopilot.Records) > 40 {
		a.autopilot.Records = a.autopilot.Records[:40]
	}
	a.autopilot.mu.Unlock()
	return true
}

func (a *App) processByPID(pid uint32) (ProcessMetric, bool) {
	for _, process := range a.snapshot.Processes {
		if process.PID == pid {
			return process, true
		}
	}
	return ProcessMetric{}, false
}

func (a *App) lockedGameProcess() (ProcessMetric, bool) {
	name := strings.TrimSpace(a.config.Gaming.LockedProcessName)
	if name == "" {
		return ProcessMetric{}, false
	}
	var best ProcessMetric
	found := false
	for _, process := range a.snapshot.Processes {
		if strings.EqualFold(process.Name, name) && (!found || process.CPU > best.CPU) {
			best, found = process, true
		}
	}
	return best, found
}

func foregroundProcessID() uint32 {
	window, _, _ := procGetForegroundWindow.Call()
	if window == 0 {
		return 0
	}
	var pid uint32
	procGetWindowThreadProcessId.Call(window, uintptr(unsafe.Pointer(&pid)))
	return pid
}

func processPowerQoS(pid uint32) (processPowerThrottlingState, error) {
	var state processPowerThrottlingState
	state.Version = processPowerThrottlingVersion
	handle, _, err := procOpenProcess.Call(PROCESS_QUERY_LIMITED_INFORMATION, 0, uintptr(pid))
	if handle == 0 {
		return state, err
	}
	defer procCloseHandle.Call(handle)
	ok, _, err := procGetProcessInformation.Call(handle, processPowerThrottling, uintptr(unsafe.Pointer(&state)), unsafe.Sizeof(state))
	if ok == 0 {
		return state, err
	}
	return state, nil
}

func setProcessPowerQoS(pid uint32, state processPowerThrottlingState) error {
	handle, _, err := procOpenProcess.Call(PROCESS_SET_INFORMATION|PROCESS_QUERY_LIMITED_INFORMATION, 0, uintptr(pid))
	if handle == 0 {
		return err
	}
	defer procCloseHandle.Call(handle)
	ok, _, err := procSetProcessInformation.Call(handle, processPowerThrottling, uintptr(unsafe.Pointer(&state)), unsafe.Sizeof(state))
	if ok == 0 {
		return err
	}
	return nil
}

func processMemoryPriorityStateFor(pid uint32) (processMemoryPriorityState, error) {
	var state processMemoryPriorityState
	handle, _, err := procOpenProcess.Call(PROCESS_QUERY_LIMITED_INFORMATION, 0, uintptr(pid))
	if handle == 0 {
		return state, err
	}
	defer procCloseHandle.Call(handle)
	ok, _, err := procGetProcessInformation.Call(handle, processMemoryPriority, uintptr(unsafe.Pointer(&state)), unsafe.Sizeof(state))
	if ok == 0 {
		return state, err
	}
	return state, nil
}

func setProcessMemoryPriorityState(pid uint32, state processMemoryPriorityState) error {
	handle, _, err := procOpenProcess.Call(PROCESS_SET_INFORMATION|PROCESS_QUERY_LIMITED_INFORMATION, 0, uintptr(pid))
	if handle == 0 {
		return err
	}
	defer procCloseHandle.Call(handle)
	ok, _, err := procSetProcessInformation.Call(handle, processMemoryPriority, uintptr(unsafe.Pointer(&state)), unsafe.Sizeof(state))
	if ok == 0 {
		return err
	}
	return nil
}

func processAffinityState(pid uint32) (uintptr, uintptr, error) {
	handle, _, err := procOpenProcess.Call(PROCESS_QUERY_LIMITED_INFORMATION, 0, uintptr(pid))
	if handle == 0 {
		return 0, 0, err
	}
	defer procCloseHandle.Call(handle)
	var processMask, systemMask uintptr
	ok, _, err := procGetProcessAffinityMask.Call(handle, uintptr(unsafe.Pointer(&processMask)), uintptr(unsafe.Pointer(&systemMask)))
	if ok == 0 {
		return 0, 0, err
	}
	return processMask, systemMask, nil
}

func setProcessAffinityState(pid uint32, mask uintptr) error {
	handle, _, err := procOpenProcess.Call(PROCESS_SET_INFORMATION|PROCESS_QUERY_LIMITED_INFORMATION, 0, uintptr(pid))
	if handle == 0 {
		return err
	}
	defer procCloseHandle.Call(handle)
	ok, _, err := procSetProcessAffinityMask.Call(handle, mask)
	if ok == 0 {
		return err
	}
	return nil
}

func onACPower() bool {
	var status systemPowerStatus
	ok, _, _ := procGetSystemPowerStatus.Call(uintptr(unsafe.Pointer(&status)))
	return ok != 0 && status.ACLineStatus == 1
}

func activeACProcessorSetting(alias string) (uint64, error) {
	output, err := runPowercfg("/qh", "scheme_current", "SUB_PROCESSOR", alias)
	if err != nil {
		return 0, err
	}
	match := powerACSettingPattern.FindSubmatch(output)
	if len(match) != 2 {
		return 0, fmt.Errorf("Windows did not report the AC value for %s", alias)
	}
	value, err := strconv.ParseUint(string(match[1]), 16, 64)
	if err != nil {
		return 0, fmt.Errorf("parse %s AC value: %w", alias, err)
	}
	return value, nil
}

func (a *App) autopilotTick() {
	if !a.surgeIsEnabled() {
		return
	}
	game, running := a.lockedGameProcess()
	if !running {
		a.restoreAutopilotSession("Locked game is not running")
		a.autopilot.mu.Lock()
		a.autopilot.Status = "Watching for " + fallback(a.config.Gaming.LockedProcessName, "a locked game")
		a.autopilot.LastEvaluation = time.Now()
		a.autopilot.mu.Unlock()
		return
	}
	if foregroundProcessID() != game.PID {
		a.restoreAutopilotSession("Game left the foreground")
		a.autopilot.mu.Lock()
		if a.autopilot.RejectedPID == game.PID {
			a.autopilot.RejectedPID = 0
		}
		a.autopilot.Status = game.Name + " is running; changes are paused until it is foreground"
		a.autopilot.LastEvaluation = time.Now()
		a.autopilot.mu.Unlock()
		return
	}

	view := a.autopilotSnapshot()
	a.autopilot.mu.RLock()
	rejected := a.autopilot.RejectedPID == game.PID
	a.autopilot.mu.RUnlock()
	if rejected {
		a.autopilot.mu.Lock()
		a.autopilot.Status = "Measured frame regression was rolled back; observing until the game leaves focus"
		a.autopilot.LastEvaluation = time.Now()
		a.autopilot.mu.Unlock()
		return
	}
	if view.GamePID != game.PID {
		if view.Active {
			a.restoreAutopilotSession("Game process restarted")
		}
		a.startAutopilotSession(game)
	}
	a.autopilot.mu.RLock()
	started, applied := a.autopilot.SessionStarted, a.autopilot.ActionsApplied
	a.autopilot.mu.RUnlock()
	if !applied {
		remaining := surgeBaselineDuration - time.Since(started)
		if remaining > 0 {
			a.sampleAutopilotHardwareBaseline()
			a.autopilot.mu.Lock()
			a.autopilot.Status = fmt.Sprintf("Measuring a clean baseline for %s · %ds remaining", game.Name, int(remaining.Seconds())+1)
			a.autopilot.LastEvaluation = time.Now()
			a.autopilot.mu.Unlock()
			return
		}
		a.applyAutopilotSession(game)
	}
	noTreatment := !a.autopilotMutationApplied()
	a.autopilot.mu.Lock()
	if a.autopilot.FrameProofDone && noTreatment {
		a.autopilot.Status = "Observation-only · no eligible treatment changed this PC"
	} else if a.autopilot.AntiCheatDetected {
		if a.antiCheatGuardIsEnabled() {
			a.autopilot.Status = a.autopilot.AntiCheatProvider + " active · external-only protection for " + game.Name
		} else {
			a.autopilot.Status = a.autopilot.AntiCheatProvider + " detected · guardrails off · external controls active for " + game.Name
		}
	} else {
		a.autopilot.Status = "Actively protecting " + game.Name + " while it is foreground"
	}
	a.autopilot.LastEvaluation = time.Now()
	a.autopilot.mu.Unlock()
	a.tryAutopilotPowerPlan()
	a.tryAutopilotFrameProof()
	a.maybeStartHardwareTuning()
}

func (a *App) startAutopilotSession(game ProcessMetric) {
	now := time.Now()
	cpuProfile := currentSurgeCPUProfile()
	a.autopilot.mu.Lock()
	a.autopilot.GamePID, a.autopilot.GameName = game.PID, game.Name
	a.autopilot.SessionStarted = now
	a.autopilot.ActionsApplied, a.autopilot.FrameProofDone, a.autopilot.CPUCapabilityLogged = false, false, false
	a.autopilot.FrameProofChecks, a.autopilot.FrameComparable, a.autopilot.FrameRegressions, a.autopilot.FrameImprovements = 0, 0, 0, 0
	a.autopilot.FrameBenefitCounts = make(map[string]int)
	a.autopilot.FrameProofWindows = nil
	a.autopilot.ExperimentalChanges = false
	a.autopilot.PerformanceChecked = false
	a.autopilot.AntiCheatDetected, a.autopilot.AntiCheatProvider = false, ""
	a.autopilot.Background, a.autopilot.NVIDIA, a.autopilot.NVIDIAChanged = nil, nil, false
	a.autopilot.HardwareBaseline = surgeHardwareBaseline{}
	a.autopilot.Status = "Measuring a clean baseline for " + game.Name + " on " + cpuProfile.Vendor
	a.autopilot.mu.Unlock()
}

func (a *App) applyAutopilotSession(game ProcessMetric) {
	cpuProfile := currentSurgeCPUProfile()
	antiCheat := detectAntiCheat(a.snapshot.Processes)
	if antiCheat.Detected && a.antiCheatGuardIsEnabled() {
		now := time.Now()
		baseline := a.frames.SnapshotWindow(a.autopilot.SessionStarted, now)
		if !surgeFrameBaselineReady(baseline) {
			a.autopilot.mu.Lock()
			a.autopilot.Status = "Waiting for a complete live frame baseline before Surge decides"
			a.autopilot.LastEvaluation = now
			a.autopilot.mu.Unlock()
			return
		}
		route := determineSurgeRoute(baseline, a.surgeStack.Snapshot().GPU, a.display.CPU, a.display.GPU, a.displayRefreshHz)
		contention := analyzeSurgeContention(a.snapshot.Processes, game.PID)
		stutter := diagnoseSurgeStutter(baseline, a.snapshot.Latency, a.snapshot.Disk, a.snapshot.Memory, contention)
		treatments := planSurgeTreatments(SurgeTreatmentContext{
			Mode: a.config.Tuning.Mode, AntiCheat: antiCheat, AntiCheatGuard: true,
			Route: route, Stutter: stutter, Frame: baseline, Contention: contention,
			GPU: a.surgeStack.Snapshot().GPU, DisplayRefreshHz: a.displayRefreshHz,
		})
		backgroundSnapshots := []backgroundProcessSnapshot(nil)
		if treatment, ok := treatmentByID(treatments, "background-isolation"); ok && treatment.Eligible {
			backgroundSnapshots = captureBackgroundSnapshots(surgeContentionCandidates(a.snapshot.Processes, game.PID))
		}
		if len(backgroundSnapshots) > 0 {
			snapshot := autopilotSessionSnapshot{PID: game.PID, Name: game.Name, Background: backgroundSnapshots, CapturedAt: now}
			if err := a.persistAutopilotSnapshot(snapshot); err != nil {
				a.autopilot.mu.Lock()
				a.autopilot.ActionsApplied, a.autopilot.FrameProofDone = true, true
				a.autopilot.Status = "Surge refused guarded background isolation because rollback could not be secured"
				a.autopilot.mu.Unlock()
				a.recordAutopilot("session-snapshot", "failed", "No guarded intervention was allowed without a crash-recovery snapshot", err.Error(), "System unchanged", "No change applied", "None required")
				return
			}
		}
		a.autopilot.mu.Lock()
		a.autopilot.FrameBaseline = baseline
		a.autopilot.AppliedAt, a.autopilot.ActionsApplied = now, true
		a.autopilot.AntiCheatDetected, a.autopilot.AntiCheatProvider = true, antiCheat.Provider
		a.autopilot.Background = append([]backgroundProcessSnapshot(nil), backgroundSnapshots...)
		a.autopilot.Status = antiCheat.Provider + " detected · external-only Surge lane"
		a.autopilot.mu.Unlock()
		a.recordAutopilot("anti-cheat-boundary", "guarded", antiCheat.Provider+" compatibility boundary is active", antiCheat.Evidence, "Game process untouched", "Use passive ETW measurement, Windows system policy and qualified non-protected background isolation only", "Every external change is separately journaled and reversible")
		a.recordAutopilot("strategy-route", "observed", route.Title, route.Evidence+" · confidence "+route.Confidence, "No process control path selected", route.Explanation, "Re-evaluate continuously as the measured route changes")
		if contention.Detected {
			a.recordAutopilot("contention-map", "observed", contention.Title, contention.Detail, "Protected and system processes excluded", "Evaluate only qualified same-session user processes without touching the game or anti-cheat", "Any eligible background isolation has an exact rollback snapshot")
		}
		a.applyAdvancedSurgeTreatments(game, treatments)
		return
	}
	if antiCheat.Detected {
		a.autopilot.mu.Lock()
		a.autopilot.AntiCheatDetected, a.autopilot.AntiCheatProvider = true, antiCheat.Provider
		a.autopilot.mu.Unlock()
		a.recordAutopilot("anti-cheat-guardrails", "risk-accepted", antiCheat.Provider+" detected while guardrails are disabled", antiCheat.Evidence, "Strict external-only compatibility lane", "Allow Kerneon's normal external Windows controls; anti-cheat processes remain excluded", "Re-enable the guard to restore the strict lane")
	}
	priority := queryProcessPriority(game.PID)
	qos, qosErr := processPowerQoS(game.PID)
	memory, memoryErr := processMemoryPriorityStateFor(game.PID)
	processMask, systemMask, affinityErr := processAffinityState(game.PID)
	now := time.Now()
	path, started := game.Path, game.Started
	if path == "" {
		path = queryProcessPath(game.PID)
	}
	if started.IsZero() {
		started = queryProcessStarted(game.PID)
	}
	baseline := a.frames.SnapshotWindow(a.autopilot.SessionStarted, now)
	if !surgeFrameBaselineReady(baseline) {
		a.autopilot.mu.Lock()
		a.autopilot.Status = "Waiting for a complete live frame baseline before Surge decides"
		a.autopilot.LastEvaluation = now
		a.autopilot.mu.Unlock()
		return
	}
	stack := a.surgeStack.Snapshot()
	route := determineSurgeRoute(baseline, stack.GPU, a.display.CPU, a.display.GPU, a.displayRefreshHz)
	contention := analyzeSurgeContention(a.snapshot.Processes, game.PID)
	stutter := diagnoseSurgeStutter(baseline, a.snapshot.Latency, a.snapshot.Disk, a.snapshot.Memory, contention)
	treatments := planSurgeTreatments(SurgeTreatmentContext{
		Mode: a.config.Tuning.Mode, AntiCheat: antiCheat, AntiCheatGuard: a.antiCheatGuardIsEnabled(),
		GamePriority: priority, Route: route, Stutter: stutter, Frame: baseline, Contention: contention, GPU: stack.GPU,
		NVIDIA: stack.GameProfile.NVIDIA, DisplayRefreshHz: a.displayRefreshHz,
	})
	backgroundSnapshots := []backgroundProcessSnapshot(nil)
	if treatment, ok := treatmentByID(treatments, "background-isolation"); ok && treatment.Eligible {
		backgroundSnapshots = captureBackgroundSnapshots(surgeContentionCandidates(a.snapshot.Processes, game.PID))
	}
	nvidiaSnapshots := []NVAPISettingSnapshot(nil)
	if treatment, ok := treatmentByID(treatments, "nvidia-maximum-performance"); ok && treatment.Eligible {
		setting, snapshotErr := a.surgeStack.nvapi.applicationDWORDSnapshot(path, nvidiaPreferredPStateID)
		if snapshotErr != nil {
			a.recordAutopilot("nvidia-profile", "failed", "The NVIDIA application profile could not be captured safely", snapshotErr.Error(), stack.GameProfile.NVIDIA.PowerPolicyName, "No driver setting was changed", "None required")
		} else {
			nvidiaSnapshots = append(nvidiaSnapshots, setting)
		}
	}
	if treatment, ok := treatmentByID(treatments, "refresh-aware-frame-cap"); ok && treatment.Eligible {
		setting, snapshotErr := a.surgeStack.nvapi.applicationDWORDSnapshot(path, nvidiaFrameRateLimitID)
		if snapshotErr != nil {
			a.recordAutopilot("frame-cap-experiment", "failed", "The NVIDIA application frame-limit value could not be captured safely", snapshotErr.Error(), "Application frame policy unchanged", "No cap was written", "None required")
		} else {
			nvidiaSnapshots = append(nvidiaSnapshots, setting)
		}
	}
	sessionSnapshot := autopilotSessionSnapshot{
		PID: game.PID, Name: game.Name, Path: path, Started: started, Priority: priority,
		PowerQoS: qos, Memory: memory, Affinity: uint64(processMask), PriorityValid: priority != 0,
		PowerQoSValid: qosErr == nil, MemoryValid: memoryErr == nil, AffinityValid: affinityErr == nil && processMask != 0,
		Background: backgroundSnapshots, NVIDIA: nvidiaSnapshots, CapturedAt: now,
	}
	if err := a.persistAutopilotSnapshot(sessionSnapshot); err != nil {
		a.autopilot.mu.Lock()
		a.autopilot.ActionsApplied, a.autopilot.FrameProofDone = true, true
		a.autopilot.Status = "Surge refused to act because the rollback snapshot could not be secured"
		a.autopilot.mu.Unlock()
		a.recordAutopilot("session-snapshot", "failed", "No intervention was allowed without a crash-recovery snapshot", err.Error(), "System unchanged", "No change applied", "None required")
		return
	}
	a.autopilot.mu.Lock()
	a.autopilot.PriorityBefore, a.autopilot.PowerQoSBefore = priority, qos
	a.autopilot.MemoryBefore = memory
	a.autopilot.AffinityBefore, a.autopilot.SystemAffinity = processMask, systemMask
	a.autopilot.FrameBaseline = baseline
	a.autopilot.Background = append([]backgroundProcessSnapshot(nil), backgroundSnapshots...)
	a.autopilot.NVIDIA = append([]NVAPISettingSnapshot(nil), nvidiaSnapshots...)
	a.autopilot.AppliedAt, a.autopilot.ActionsApplied = now, true
	a.autopilot.mu.Unlock()
	a.recordAutopilot("strategy-route", "observed", route.Title, route.Evidence+" · confidence "+route.Confidence, "No control path selected", route.Explanation, "Re-evaluate continuously as the measured route changes")
	if contention.Detected {
		a.recordAutopilot("contention-map", "observed", contention.Title, contention.Detail, "Background processes unchanged", "Keep protected/system processes outside automatic control; require repeatable frame evidence before any isolation experiment", "No rollback required")
	}

	if qosErr == nil && qos.ControlMask&processPowerThrottlingExecution != 0 && qos.StateMask&processPowerThrottlingExecution != 0 {
		painpoint := "Windows execution-speed throttling is active on the locked game"
		evidence := fmt.Sprintf("Windows reports execution throttling enabled for %s (PID %d)", game.Name, game.PID)
		action := "Remove execution-speed throttling for this foreground session"
		if a.recordAutopilot("game-power-qos", "prepared", painpoint, evidence, fmt.Sprintf("control=%d state=%d", qos.ControlMask, qos.StateMask), action, "Restore the exact previous power-throttling state") {
			highQoS := processPowerThrottlingState{Version: processPowerThrottlingVersion, ControlMask: processPowerThrottlingExecution, StateMask: 0}
			if err := setProcessPowerQoS(game.PID, highQoS); err != nil {
				a.recordAutopilot("game-power-qos", "failed", "Could not change the game's Windows power QoS", err.Error(), "Unchanged", "No change applied", "None required")
			} else {
				a.autopilot.mu.Lock()
				a.autopilot.PowerQoSChanged = true
				a.autopilot.mu.Unlock()
				a.recordAutopilot("game-power-qos", "applied", "Full-speed execution guardrail is active for this game session", "Windows accepted an explicit full execution QoS request; this is a scheduling guardrail, not a claimed FPS gain", fmt.Sprintf("control=%d state=%d", qos.ControlMask, qos.StateMask), "Execution-speed power throttling is explicitly disabled while foreground", "Exact previous state will return when the session ends")
			}
		}
	}
	if priority == priorityBelowNormal {
		if a.recordAutopilot("game-scheduling", "prepared", "The foreground game has a reduced Windows scheduling priority", fmt.Sprintf("%s uses %s priority; %s", game.Name, priorityLabel(priority), cpuProfile.Evidence), priorityLabel(priority), "Repair to Normal; High and Realtime are prohibited", "Restore "+priorityLabel(priority)) {
			if err := setProcessPriority(game.PID, priorityNormal); err != nil {
				a.recordAutopilot("game-scheduling", "failed", "Windows refused the scheduling preference", err.Error(), priorityLabel(priority), "No change applied", "None required")
			} else {
				a.autopilot.mu.Lock()
				a.autopilot.PriorityChanged = true
				a.autopilot.mu.Unlock()
				a.recordAutopilot("game-scheduling", "applied", "Reduced CPU scheduling priority repaired for the active game", "The locked foreground process was measurably below Windows' normal scheduling class; "+cpuProfile.PolicyNote, priorityLabel(priority), "Normal priority applied", "Original priority will return when focus leaves the game")
			}
		}
	}
	if scheduling, ok := treatmentByID(treatments, "cpu-scheduling-trial"); ok && scheduling.Eligible {
		if a.recordAutopilot("cpu-scheduling-trial", "prepared", "The game is waiting on CPU/engine frame production", scheduling.Reason+" "+cpuProfile.Evidence, priorityLabel(priority), "Temporarily give the game Above Normal scheduling priority", "Restore exact "+priorityLabel(priority)+" priority if proof is neutral, regresses, loses focus or Surge stops") {
			if err := setProcessPriority(game.PID, priorityAboveNormal); err != nil {
				a.recordAutopilot("cpu-scheduling-trial", "failed", "Windows refused the bounded CPU scheduling experiment", err.Error(), priorityLabel(priority), "No priority change retained", "None required")
			} else {
				a.autopilot.mu.Lock()
				a.autopilot.PriorityChanged = true
				a.autopilot.ExperimentalChanges = true
				a.autopilot.mu.Unlock()
				a.recordAutopilot("cpu-scheduling-trial", "applied", "The CPU-limited game now gets first call on contested user-process CPU time", "Windows accepted Above Normal for the locked foreground game; High and Realtime remain prohibited", priorityLabel(priority), "Above Normal for this measured foreground session", "Exact prior priority is journaled and automatically restored unless repeated frame evidence earns retention")
			}
		}
	}
	if memoryErr == nil && memory.MemoryPriority > 0 && memory.MemoryPriority < 5 {
		if a.recordAutopilot("game-memory-priority", "prepared", "The foreground game has a reduced Windows memory priority", fmt.Sprintf("Windows reports memory priority %d of 5", memory.MemoryPriority), fmt.Sprintf("priority=%d", memory.MemoryPriority), "Restore normal memory priority for this session", fmt.Sprintf("Restore priority %d", memory.MemoryPriority)) {
			if err := setProcessMemoryPriorityState(game.PID, processMemoryPriorityState{MemoryPriority: 5}); err != nil {
				a.recordAutopilot("game-memory-priority", "failed", "Windows refused the memory-priority repair", err.Error(), fmt.Sprintf("priority=%d", memory.MemoryPriority), "No change applied", "None required")
			} else {
				a.autopilot.mu.Lock()
				a.autopilot.MemoryChanged = true
				a.autopilot.mu.Unlock()
				a.recordAutopilot("game-memory-priority", "applied", "Reduced memory priority repaired for the active game", "The game was foreground with a below-normal memory priority", fmt.Sprintf("priority=%d", memory.MemoryPriority), "Normal memory priority applied", "Exact previous priority will return when the session ends")
			}
		}
	}
	if affinityErr == nil && runtime.NumCPU() <= 64 && processMask != 0 && systemMask != 0 && processMask != systemMask {
		processCPUs := bits.OnesCount64(uint64(processMask))
		systemCPUs := bits.OnesCount64(uint64(systemMask))
		if processCPUs*2 <= systemCPUs {
			if a.recordAutopilot("game-affinity-repair", "prepared", "The game is restricted to a small subset of available logical processors", fmt.Sprintf("Affinity exposes %d of %d available processors", processCPUs, systemCPUs), fmt.Sprintf("mask=0x%X", processMask), "Restore the process to the current system affinity mask", fmt.Sprintf("Restore mask 0x%X", processMask)) {
				if err := setProcessAffinityState(game.PID, systemMask); err != nil {
					a.recordAutopilot("game-affinity-repair", "failed", "Windows refused the affinity repair", err.Error(), fmt.Sprintf("mask=0x%X", processMask), "No change applied", "None required")
				} else {
					a.autopilot.mu.Lock()
					a.autopilot.AffinityChanged = true
					a.autopilot.mu.Unlock()
					a.recordAutopilot("game-affinity-repair", "applied", "Severe CPU-affinity restriction removed for this session", fmt.Sprintf("The game could use only %d of %d available processors", processCPUs, systemCPUs), fmt.Sprintf("mask=0x%X", processMask), fmt.Sprintf("mask=0x%X", systemMask), "Exact previous mask will return when the session ends")
				}
			}
		}
	}
	a.applyAdvancedSurgeTreatments(game, treatments)
}

func surgeFrameBaselineReady(baseline FrameStats) bool {
	return baseline.Available && baseline.Error == "" && baseline.Samples >= 120 && baseline.FPS > 0 && baseline.OnePercentLow > 0
}

func (a *App) tryAutopilotPowerPlan() {
	view := a.autopilotSnapshot()
	if !view.Active {
		return
	}
	a.autopilot.mu.RLock()
	alreadyChanged, performanceChecked := a.autopilot.PlanChanged, a.autopilot.PerformanceChecked
	a.autopilot.mu.RUnlock()
	if alreadyChanged || (a.config.Tuning.Mode == "performance" && performanceChecked) || !onACPower() {
		return
	}
	cpuProfile := currentSurgeCPUProfile()
	if !cpuProfile.PerformanceAllowed {
		a.autopilot.mu.Lock()
		if a.autopilot.CPUCapabilityLogged {
			a.autopilot.mu.Unlock()
			return
		}
		a.autopilot.CPUCapabilityLogged = true
		a.autopilot.Status = cpuProfile.PolicyNote
		a.autopilot.mu.Unlock()
		a.recordAutopilot("cpu-capability-boundary", "observed", "Host clock control is not available from this CPU environment", cpuProfile.Evidence, "Windows CPU policy unchanged", "Continue process-level Guarded controls only", "No rollback required")
		return
	}
	if a.config.Tuning.Mode == "performance" {
		a.tryAutopilotPerformancePolicy()
		return
	}
	benchmark := benchmarkFromSnapshot(a.snapshot, time.Now().Add(-30*time.Second))
	power := a.optimizerSnapshot()
	plan := strings.ToLower(power.PowerPlan)
	if benchmark.Samples < 10 || benchmark.CPU < 85 || benchmark.GPU >= 90 || !power.HighPerformanceExists || strings.Contains(plan, "performance") || strings.Contains(plan, "ultimate") || strings.Contains(plan, "ultra") {
		return
	}
	if !a.recordAutopilot("session-power-plan", "prepared", "Sustained CPU pressure may be waiting on conservative clock ramping", fmt.Sprintf("%d samples: CPU %.0f%%, GPU %.0f%%; AC power connected", benchmark.Samples, benchmark.CPU, benchmark.GPU), power.PowerPlan, "Temporarily select Windows High performance", "Restore "+power.PowerPlan+" when the game session ends") {
		return
	}
	a.config.Tuning.PendingPowerPlan = power.PowerGUID
	if err := a.store.Save(a.configSnapshot()); err != nil {
		a.config.Tuning.PendingPowerPlan = ""
		a.recordAutopilot("session-power-plan", "failed", "Rollback journal could not be written", err.Error(), power.PowerPlan, "No change applied", "None required")
		return
	}
	if _, err := runPowercfg("/setactive", highPerformanceGUID); err != nil {
		a.config.Tuning.PendingPowerPlan = ""
		_ = a.store.Save(a.configSnapshot())
		a.recordAutopilot("session-power-plan", "failed", "Windows refused the temporary power plan", err.Error(), power.PowerPlan, "No change applied", "None required")
		return
	}
	a.autopilot.mu.Lock()
	a.autopilot.PlanChanged = true
	a.autopilot.ExperimentalChanges = true
	a.autopilot.PreviousPlanGUID, a.autopilot.PreviousPlanName = power.PowerGUID, power.PowerPlan
	a.autopilot.mu.Unlock()
	a.recordAutopilot("session-power-plan", "applied", "CPU-heavy game session moved to a performance-oriented power policy", fmt.Sprintf("CPU %.0f%%, GPU %.0f%% on AC power", benchmark.CPU, benchmark.GPU), power.PowerPlan, "High performance active for this session", "Automatic restore when focus leaves the game")
}

func (a *App) tryAutopilotPerformancePolicy() {
	a.autopilot.mu.Lock()
	a.autopilot.PerformanceChecked = true
	a.autopilot.mu.Unlock()
	previousGUID, previousName, err := activePowerPlan()
	if err != nil || previousGUID == "" {
		a.recordAutopilot("performance-policy", "failed", "The active Windows power policy could not be captured", fmt.Sprint(err), "Unknown", "No change applied", "None required")
		return
	}
	current := make(map[string]uint64, 4)
	currentKnown := true
	for _, alias := range []string{"PERFEPP", "PROCTHROTTLEMIN", "PROCTHROTTLEMAX", "PERFBOOSTMODE"} {
		value, settingErr := activeACProcessorSetting(alias)
		if settingErr != nil {
			currentKnown = false
			break
		}
		current[alias] = value
	}
	if currentKnown && current["PERFEPP"] == 0 && current["PROCTHROTTLEMIN"] == 100 && current["PROCTHROTTLEMAX"] == 100 && current["PERFBOOSTMODE"] == 2 {
		a.recordAutopilot("performance-policy", "observed", "The active Windows CPU policy already matches Surge Aggressive", "EPP 0; processor state 100–100%; aggressive boost; AC power", previousName, "No duplicate policy or redundant mutation was created", "No rollback required")
		return
	}
	if !a.recordAutopilot("performance-policy", "prepared", "Aggressive policy was explicitly selected for this game session", "AC power connected; clean baseline captured; exact prior scheme identified", previousName, "Create an isolated temporary scheme with maximum CPU performance preference", "Reactivate the prior scheme and delete the temporary scheme") {
		return
	}
	output, err := runPowercfg("/duplicatescheme", previousGUID)
	if err != nil {
		a.recordAutopilot("performance-policy", "failed", "Windows could not create an isolated session policy", err.Error(), previousName, "No change applied", "None required")
		return
	}
	temporaryGUID := powerGUIDPattern.FindString(string(output))
	if temporaryGUID == "" || strings.EqualFold(temporaryGUID, previousGUID) {
		a.recordAutopilot("performance-policy", "failed", "Windows did not return a safe temporary policy identity", strings.TrimSpace(string(output)), previousName, "No change applied", "None required")
		return
	}
	cleanup := func() { _, _ = runPowercfg("/delete", temporaryGUID) }
	_, _ = runPowercfg("/changename", temporaryGUID, "Kerneon Surge Session", "Temporary; restored and removed when the session ends")
	settings := [][2]string{
		{"PERFEPP", "0"},
		{"PROCTHROTTLEMIN", "100"},
		{"PROCTHROTTLEMAX", "100"},
		{"PERFBOOSTMODE", "2"},
	}
	for _, setting := range settings {
		if _, err := runPowercfg("/setacvalueindex", temporaryGUID, "SUB_PROCESSOR", setting[0], setting[1]); err != nil {
			cleanup()
			a.recordAutopilot("performance-policy", "failed", "This PC does not expose every required bounded CPU policy", setting[0]+": "+err.Error(), previousName, "Temporary policy deleted without activation", "None required")
			return
		}
	}
	a.config.Tuning.PendingPowerPlan = previousGUID
	a.config.Tuning.PendingTemporaryPlan = temporaryGUID
	if err := a.store.Save(a.configSnapshot()); err != nil {
		a.config.Tuning.PendingPowerPlan, a.config.Tuning.PendingTemporaryPlan = "", ""
		cleanup()
		a.recordAutopilot("performance-policy", "failed", "Crash rollback could not be journaled", err.Error(), previousName, "Temporary policy deleted without activation", "None required")
		return
	}
	if _, err := runPowercfg("/setactive", temporaryGUID); err != nil {
		a.config.Tuning.PendingPowerPlan, a.config.Tuning.PendingTemporaryPlan = "", ""
		_ = a.store.Save(a.configSnapshot())
		cleanup()
		a.recordAutopilot("performance-policy", "failed", "Windows refused to activate the temporary Aggressive policy", err.Error(), previousName, "Temporary policy deleted", "None required")
		return
	}
	a.autopilot.mu.Lock()
	a.autopilot.PlanChanged = true
	a.autopilot.ExperimentalChanges = true
	a.autopilot.PreviousPlanGUID, a.autopilot.PreviousPlanName = previousGUID, previousName
	a.autopilot.TemporaryPlanGUID = temporaryGUID
	a.autopilot.mu.Unlock()
	a.recordAutopilot("performance-policy", "applied", "Windows CPU clock governance moved to the explicit Aggressive policy", "EPP 0; minimum and maximum processor state 100%; aggressive boost request; AC session only", previousName, "Temporary Kerneon Surge Session policy active", "Exact prior scheme will reactivate and this temporary scheme will be deleted")
}

func (a *App) tryAutopilotFrameProof() {
	a.autopilot.mu.RLock()
	pid, appliedAt := a.autopilot.GamePID, a.autopilot.AppliedAt
	baseline, done, check := a.autopilot.FrameBaseline, a.autopilot.FrameProofDone, a.autopilot.FrameProofChecks
	a.autopilot.mu.RUnlock()
	if pid == 0 || done || appliedAt.IsZero() || check >= 3 {
		return
	}
	if !a.autopilotMutationApplied() {
		a.autopilot.mu.Lock()
		if a.autopilot.FrameProofDone {
			a.autopilot.mu.Unlock()
			return
		}
		a.autopilot.FrameProofDone = true
		a.autopilot.Status = "Observation-only · no eligible treatment changed this PC"
		a.autopilot.mu.Unlock()
		a.recordAutopilot("frame-proof", "observed", "No performance claim was created because no treatment was applied", "The measured route produced no eligible mutation; A/B differences would be ordinary gameplay variation", "System unchanged", "Continue monitoring and explain why Surge did not act", "No rollback required")
		return
	}
	windowStart := appliedAt.Add(surgeProofSettleDuration + time.Duration(check)*surgeProofWindowStride)
	windowEnd := windowStart.Add(surgeProofWindowDuration)
	if time.Now().Before(windowEnd) {
		return
	}
	after := a.frames.SnapshotWindow(windowStart, windowEnd)
	a.autopilot.mu.Lock()
	a.autopilot.FrameProofChecks++
	checks := a.autopilot.FrameProofChecks
	a.autopilot.mu.Unlock()
	if !after.Available || baseline.Error != "" || after.Error != "" || baseline.Samples < 60 || after.Samples < 120 || baseline.OnePercentLow <= 0 || after.OnePercentLow <= 0 {
		if checks >= 3 {
			a.autopilot.mu.Lock()
			a.autopilot.FrameProofDone = true
			a.autopilot.mu.Unlock()
			a.recordAutopilot("frame-proof", "observed", "Frame proof remained inconclusive", fmt.Sprintf("baseline samples=%d; final window samples=%d; provider available=%t; diagnostics=%s", baseline.Samples, after.Samples, after.Available, fallback(after.Error, baseline.Error)), "No causal verdict", "Continue passive measurement without claiming a gain", "All session changes still restore at the session boundary")
		}
		return
	}
	comparable, comparison := comparableFrameWorkload(baseline, after)
	if !comparable {
		a.autopilot.mu.Lock()
		a.autopilot.FrameRegressions = 0
		if checks >= 3 {
			a.autopilot.FrameProofDone = true
		}
		a.autopilot.mu.Unlock()
		a.recordAutopilot("frame-proof-window", "observed", fmt.Sprintf("Verification window %d was not comparable", checks), comparison, "30-second pre-treatment baseline", "Exclude this scene from the performance result", "Continue with the remaining scheduled window")
		if checks >= 3 {
			a.recordAutopilot("frame-proof", "observed", "Gameplay changed too much for an honest causal comparison", comparison, "Unlike gameplay windows", "Report the measurements without attributing them to Surge", "All session changes still restore at the session boundary")
		}
		return
	}
	benefitMetrics := frameBenefitMetrics(baseline, after)
	a.autopilot.mu.Lock()
	a.autopilot.FrameComparable++
	a.autopilot.FrameProofWindows = append(a.autopilot.FrameProofWindows, after)
	if len(benefitMetrics) > 0 {
		a.autopilot.FrameImprovements++
		if a.autopilot.FrameBenefitCounts == nil {
			a.autopilot.FrameBenefitCounts = make(map[string]int)
		}
		for _, metric := range benefitMetrics {
			a.autopilot.FrameBenefitCounts[metric]++
		}
	}
	a.autopilot.mu.Unlock()
	benefitEvidence := "no benefit threshold crossed"
	if len(benefitMetrics) > 0 {
		benefitEvidence = "benefit thresholds: " + strings.Join(benefitMetrics, ", ")
	}
	a.recordAutopilot("frame-proof-window", "measured", fmt.Sprintf("Comparable verification window %d of 3", checks), fmt.Sprintf("average %.1f FPS · 1%% low %.1f FPS · p99 %.1f ms · hitches %.1f/min; %s; %s", after.FPS, after.OnePercentLow, after.P99FrameMs, after.HitchesPerMinute, comparison, benefitEvidence), "30-second pre-treatment baseline", "Include this window in the aggregate A/B result", "Exact rollback remains available")
	lowRegression := after.OnePercentLow < baseline.OnePercentLow*0.88
	delayRegression := baseline.P95DisplayMs > 0 && after.P95DisplayMs > baseline.P95DisplayMs*1.15
	dropRegression := after.DroppedPercent > baseline.DroppedPercent+2
	if lowRegression && (delayRegression || dropRegression) {
		evidence := fmt.Sprintf("1%% low %.1f→%.1f FPS; p95 display %.1f→%.1f ms; dropped %.1f→%.1f%%", baseline.OnePercentLow, after.OnePercentLow, baseline.P95DisplayMs, after.P95DisplayMs, baseline.DroppedPercent, after.DroppedPercent)
		a.autopilot.mu.Lock()
		a.autopilot.FrameRegressions++
		regressions := a.autopilot.FrameRegressions
		a.autopilot.mu.Unlock()
		if regressions >= 2 {
			a.recordAutopilot("frame-proof-rollback", "prepared", "Frame delivery regressed in repeated comparable windows", evidence+"; "+comparison, "Current Surge session bundle", "Roll back every applied session change", "Observation-only until the game leaves focus")
			a.restoreAutopilotSession("Repeated comparable frame regression")
			a.autopilot.mu.Lock()
			a.autopilot.RejectedPID = pid
			a.autopilot.FrameProofDone = true
			a.autopilot.Status = "Repeatable frame regression rolled back; observation-only until the game leaves focus"
			a.autopilot.mu.Unlock()
			a.recordAutopilot("frame-proof-rollback", "restored", "Surge withdrew a session after repeatable regression", evidence, "Session bundle active", "Exact recorded state restored", "Complete")
			return
		}
		a.recordAutopilot("frame-proof", "observed", "One comparable window regressed; Surge is waiting for confirmation", evidence+"; "+comparison, "Single window only", "Do not claim causality or roll back yet", "A second comparable regression triggers exact rollback")
	} else {
		a.autopilot.mu.Lock()
		a.autopilot.FrameRegressions = 0
		a.autopilot.mu.Unlock()
	}
	if checks >= 3 {
		a.autopilot.mu.Lock()
		comparableChecks, improvements := a.autopilot.FrameComparable, a.autopilot.FrameImprovements
		experimental := a.autopilot.ExperimentalChanges
		proofWindows := append([]FrameStats(nil), a.autopilot.FrameProofWindows...)
		benefitCounts := make(map[string]int, len(a.autopilot.FrameBenefitCounts))
		for metric, count := range a.autopilot.FrameBenefitCounts {
			benefitCounts[metric] = count
		}
		a.autopilot.FrameProofDone = true
		a.autopilot.mu.Unlock()
		postMean := averageFrameStats(proofWindows)
		proofEarned, repeatedMetric, repeatedCount := experimentalFrameProofEarned(comparableChecks, benefitCounts)
		if experimental && !proofEarned {
			evidence := fmt.Sprintf("%d comparable windows; average %.1f→%.1f FPS; 1%% low %.1f→%.1f FPS; p99 %.1f→%.1f ms; hitches %.1f→%.1f/min", comparableChecks, baseline.FPS, postMean.FPS, baseline.OnePercentLow, postMean.OnePercentLow, baseline.P99FrameMs, postMean.P99FrameMs, baseline.HitchesPerMinute, postMean.HitchesPerMinute)
			a.recordAutopilot("frame-proof-neutral", "prepared", "The experimental treatment did not earn repeatable causal proof", evidence, "Current experimental session controls", "Withdraw the treatment instead of retaining an unproven tweak", "Exact captured state will be restored")
			a.restoreAutopilotSession("Fewer than two comparable windows proved a frame-delivery benefit")
			a.autopilot.mu.Lock()
			a.autopilot.RejectedPID = pid
			a.autopilot.FrameProofDone = true
			a.autopilot.Status = "Unproven treatment withdrawn; observation-only until the game leaves focus"
			a.autopilot.mu.Unlock()
			a.recordAutopilot("frame-proof-neutral", "restored", "Surge withdrew a treatment that did not earn its place", evidence, "Experimental session controls", "Exact recorded state restored", "Complete")
			return
		}
		status, conclusion := "verified", "No repeatable material regression was measured"
		action := "Retain measured repairs for the rest of this foreground session"
		if experimental && proofEarned {
			status, conclusion = "proved", fmt.Sprintf("The treatment repeated its %s benefit in %d comparable windows", repeatedMetric, repeatedCount)
			action = "Retain the treatment because repeated comparable evidence crossed its benefit threshold"
		}
		a.recordAutopilot("frame-proof", status, conclusion, fmt.Sprintf("%d of 3 windows were workload-comparable; %d crossed a benefit threshold; average %.1f→%.1f FPS; 1%% low %.1f→%.1f FPS; p99 %.1f→%.1f ms; hitches %.1f→%.1f/min", comparableChecks, improvements, baseline.FPS, postMean.FPS, baseline.OnePercentLow, postMean.OnePercentLow, baseline.P99FrameMs, postMean.P99FrameMs, baseline.HitchesPerMinute, postMean.HitchesPerMinute), "30-second pre-treatment baseline plus three non-overlapping 20-second verification windows", action, "Exact rollback remains available")
	}
}

func experimentalFrameProofEarned(comparableChecks int, benefitCounts map[string]int) (bool, string, int) {
	if comparableChecks < 2 {
		return false, "", 0
	}
	metric, count := repeatedFrameBenefitMetric(benefitCounts)
	return count >= 2, metric, count
}

func repeatedFrameBenefitMetric(counts map[string]int) (string, int) {
	bestMetric, bestCount := "", 0
	for _, metric := range []string{"average FPS", "1% low", "p99 frame time", "hitch rate", "display latency"} {
		if counts[metric] > bestCount {
			bestMetric, bestCount = metric, counts[metric]
		}
	}
	return bestMetric, bestCount
}

func averageFrameStats(windows []FrameStats) FrameStats {
	if len(windows) == 0 {
		return FrameStats{}
	}
	result := FrameStats{Available: true}
	for _, window := range windows {
		result.Available = result.Available && window.Available
		result.FPS += window.FPS
		result.OnePercentLow += window.OnePercentLow
		result.ZeroPointOnePercentLow += window.ZeroPointOnePercentLow
		result.P99FrameMs += window.P99FrameMs
		result.WorstFrameMs += window.WorstFrameMs
		result.HitchesPerMinute += window.HitchesPerMinute
		result.P95DisplayMs += window.P95DisplayMs
		result.DroppedPercent += window.DroppedPercent
		result.AverageRenderMs += window.AverageRenderMs
		result.AverageGPUActiveMs += window.AverageGPUActiveMs
		result.FramePacingCV += window.FramePacingCV
		result.Samples += window.Samples
		result.HitchCount += window.HitchCount
	}
	count := float64(len(windows))
	result.FPS /= count
	result.OnePercentLow /= count
	result.ZeroPointOnePercentLow /= count
	result.P99FrameMs /= count
	result.WorstFrameMs /= count
	result.HitchesPerMinute /= count
	result.P95DisplayMs /= count
	result.DroppedPercent /= count
	result.AverageRenderMs /= count
	result.AverageGPUActiveMs /= count
	result.FramePacingCV /= count
	return result
}

func comparableFrameWorkload(baseline, after FrameStats) (bool, string) {
	if baseline.AverageRenderMs <= 0 || after.AverageRenderMs <= 0 {
		return false, "render-completion workload signatures were unavailable"
	}
	delta := math.Abs(after.AverageRenderMs - baseline.AverageRenderMs)
	tolerance := math.Max(0.75, math.Max(after.AverageRenderMs, baseline.AverageRenderMs)*0.30)
	evidence := fmt.Sprintf("render completion %.2f→%.2f ms (allowed delta %.2f ms)", baseline.AverageRenderMs, after.AverageRenderMs, tolerance)
	return delta <= tolerance, evidence
}

func frameBenefitThresholdCrossed(baseline, after FrameStats) bool {
	return len(frameBenefitMetrics(baseline, after)) > 0
}

func frameBenefitMetrics(baseline, after FrameStats) []string {
	averageImproved := baseline.FPS > 0 && after.FPS >= baseline.FPS*1.03
	lowImproved := baseline.OnePercentLow > 0 && after.OnePercentLow >= baseline.OnePercentLow*1.04
	tailImproved := baseline.P99FrameMs > 0 && after.P99FrameMs > 0 && after.P99FrameMs <= baseline.P99FrameMs*0.92
	hitchesImproved := baseline.HitchesPerMinute >= 4 && after.HitchesPerMinute <= baseline.HitchesPerMinute*0.70
	displayImproved := baseline.P95DisplayMs > 0 && after.P95DisplayMs > 0 && after.P95DisplayMs <= baseline.P95DisplayMs*0.92
	metrics := make([]string, 0, 5)
	if averageImproved {
		metrics = append(metrics, "average FPS")
	}
	if lowImproved {
		metrics = append(metrics, "1% low")
	}
	if tailImproved {
		metrics = append(metrics, "p99 frame time")
	}
	if hitchesImproved {
		metrics = append(metrics, "hitch rate")
	}
	if displayImproved {
		metrics = append(metrics, "display latency")
	}
	return metrics
}

func (a *App) restoreAutopilotSession(reason string) {
	a.autopilot.mu.RLock()
	pid, name := a.autopilot.GamePID, a.autopilot.GameName
	priority, priorityChanged := a.autopilot.PriorityBefore, a.autopilot.PriorityChanged
	qos, qosChanged := a.autopilot.PowerQoSBefore, a.autopilot.PowerQoSChanged
	memory, memoryChanged := a.autopilot.MemoryBefore, a.autopilot.MemoryChanged
	affinity, affinityChanged := a.autopilot.AffinityBefore, a.autopilot.AffinityChanged
	planGUID, planName, temporaryPlan, planChanged := a.autopilot.PreviousPlanGUID, a.autopilot.PreviousPlanName, a.autopilot.TemporaryPlanGUID, a.autopilot.PlanChanged
	background := append([]backgroundProcessSnapshot(nil), a.autopilot.Background...)
	nvidia := append([]NVAPISettingSnapshot(nil), a.autopilot.NVIDIA...)
	nvidiaChanged := a.autopilot.NVIDIAChanged
	a.autopilot.mu.RUnlock()
	if pid == 0 && !planChanged && len(background) == 0 && !nvidiaChanged {
		return
	}
	for _, target := range background {
		if !backgroundIdentityMatches(target) {
			continue
		}
		if target.PowerQoSChanged && target.PowerQoSValid {
			if err := setProcessPowerQoS(target.PID, target.PowerQoS); err != nil {
				a.recordAutopilot("background-isolation-rollback", "failed", "Could not restore "+target.Name+" power QoS", err.Error(), "EcoQoS session", "Crash journal retained for another restore attempt", "Exact prior state remains recorded")
			}
		}
		if target.PriorityChanged && target.PriorityValid {
			if err := setProcessPriority(target.PID, target.Priority); err != nil {
				a.recordAutopilot("background-isolation-rollback", "failed", "Could not restore "+target.Name+" priority", err.Error(), "Below Normal session", "Crash journal retained for another restore attempt", "Exact prior state remains recorded")
			} else {
				a.recordAutopilot("background-isolation-rollback", "restored", reason, target.Name, "Temporary contention isolation", priorityLabel(target.Priority)+" and the recorded QoS state restored", "Complete")
			}
		}
	}
	if nvidiaChanged {
		for _, setting := range nvidia {
			if err := a.surgeStack.nvapi.restoreApplicationDWORD(setting); err != nil {
				a.recordAutopilot("nvidia-profile-rollback", "failed", "NVIDIA application profile rollback needs attention", err.Error(), "Prefer maximum performance", "Crash journal retained for another restore attempt", "Exact prior setting remains recorded")
			} else {
				a.recordAutopilot("nvidia-profile-rollback", "restored", reason, setting.ExecutablePath, "Prefer maximum performance", "Captured NVIDIA application value restored", "Complete")
			}
		}
	}
	if qosChanged {
		if a.recordAutopilot("game-power-qos-rollback", "prepared", reason, "Session boundary reached", "HighQoS session override", "Restore exact power QoS state", fmt.Sprintf("control=%d state=%d", qos.ControlMask, qos.StateMask)) {
			if err := setProcessPowerQoS(pid, qos); err != nil {
				a.recordAutopilot("game-power-qos-rollback", "failed", "Power QoS rollback could not be completed", err.Error(), "HighQoS override", "Windows process may already have exited", "No persistent system setting was changed")
			} else {
				a.recordAutopilot("game-power-qos-rollback", "restored", reason, name, "HighQoS override", "Previous power QoS restored", "Complete")
			}
		}
	}
	if priorityChanged {
		if a.recordAutopilot("game-scheduling-rollback", "prepared", reason, name, "Normal priority repair", "Restore "+priorityLabel(priority), "Return to the original priority") {
			if err := setProcessPriority(pid, priority); err != nil {
				a.recordAutopilot("game-scheduling-rollback", "failed", "Scheduling rollback could not be completed", err.Error(), "Normal priority repair", "Process may already have exited", "No persistent setting remains")
			} else {
				a.recordAutopilot("game-scheduling-rollback", "restored", reason, name, "Normal priority repair", priorityLabel(priority)+" restored", "Complete")
			}
		}
	}
	if memoryChanged {
		if a.recordAutopilot("game-memory-priority-rollback", "prepared", reason, name, "Normal memory priority session repair", fmt.Sprintf("Restore priority %d", memory.MemoryPriority), "Return to the exact previous memory priority") {
			if err := setProcessMemoryPriorityState(pid, memory); err != nil {
				a.recordAutopilot("game-memory-priority-rollback", "failed", "Memory-priority rollback could not be completed", err.Error(), "Normal memory priority", "Process may already have exited", "No persistent system setting was changed")
			} else {
				a.recordAutopilot("game-memory-priority-rollback", "restored", reason, name, "Normal memory priority", fmt.Sprintf("priority %d restored", memory.MemoryPriority), "Complete")
			}
		}
	}
	if affinityChanged {
		if a.recordAutopilot("game-affinity-rollback", "prepared", reason, name, fmt.Sprintf("Expanded affinity mask 0x%X", a.autopilot.SystemAffinity), fmt.Sprintf("Restore mask 0x%X", affinity), "Return to the exact previous affinity") {
			if err := setProcessAffinityState(pid, affinity); err != nil {
				a.recordAutopilot("game-affinity-rollback", "failed", "CPU-affinity rollback could not be completed", err.Error(), "Expanded session affinity", "Process may already have exited", "No persistent system setting was changed")
			} else {
				a.recordAutopilot("game-affinity-rollback", "restored", reason, name, "Expanded session affinity", fmt.Sprintf("mask 0x%X restored", affinity), "Complete")
			}
		}
	}
	if planChanged && planGUID != "" {
		if a.recordAutopilot("session-power-plan-rollback", "prepared", reason, name, "High performance", "Restore "+planName, "Return to the pre-session power plan") {
			if _, err := runPowercfg("/setactive", planGUID); err != nil {
				a.recordAutopilot("session-power-plan-rollback", "failed", "Power-plan rollback needs attention", err.Error(), "High performance", "Rollback failed", "The saved GUID remains in settings for next-launch recovery")
			} else {
				a.config.Tuning.PendingPowerPlan, a.config.Tuning.PendingTemporaryPlan = "", ""
				_ = a.store.Save(a.configSnapshot())
				if temporaryPlan != "" {
					_, _ = runPowercfg("/delete", temporaryPlan)
				}
				a.recordAutopilot("session-power-plan-rollback", "restored", reason, name, "High performance", planName+" restored", "Complete")
			}
		}
	}
	a.autopilot.mu.Lock()
	a.autopilot.GamePID, a.autopilot.GameName = 0, ""
	a.autopilot.PriorityChanged, a.autopilot.PowerQoSChanged, a.autopilot.MemoryChanged, a.autopilot.AffinityChanged, a.autopilot.PlanChanged = false, false, false, false, false
	a.autopilot.PreviousPlanGUID, a.autopilot.PreviousPlanName, a.autopilot.TemporaryPlanGUID = "", "", ""
	a.autopilot.SessionStarted, a.autopilot.AppliedAt = time.Time{}, time.Time{}
	a.autopilot.ActionsApplied, a.autopilot.FrameProofDone, a.autopilot.CPUCapabilityLogged = false, false, false
	a.autopilot.FrameProofChecks, a.autopilot.FrameComparable, a.autopilot.FrameRegressions, a.autopilot.FrameImprovements = 0, 0, 0, 0
	a.autopilot.FrameBenefitCounts = nil
	a.autopilot.FrameProofWindows = nil
	a.autopilot.ExperimentalChanges = false
	a.autopilot.PerformanceChecked = false
	a.autopilot.AntiCheatDetected, a.autopilot.AntiCheatProvider = false, ""
	a.autopilot.Background, a.autopilot.NVIDIA, a.autopilot.NVIDIAChanged = nil, nil, false
	a.autopilot.FrameBaseline = FrameStats{}
	a.autopilot.HardwareBaseline = surgeHardwareBaseline{}
	a.autopilot.mu.Unlock()
	a.restoreInterruptedAutopilotSnapshot()
}
