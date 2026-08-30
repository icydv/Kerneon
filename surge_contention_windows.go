//go:build windows

package main

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
	"unsafe"
)

var procProcessIDToSessionID = kernel32.NewProc("ProcessIdToSessionId")

type backgroundProcessSnapshot struct {
	PID             uint32                      `json:"pid"`
	Name            string                      `json:"name"`
	Path            string                      `json:"path"`
	Started         time.Time                   `json:"started"`
	Priority        uint32                      `json:"priority"`
	PowerQoS        processPowerThrottlingState `json:"power_qos"`
	PriorityValid   bool                        `json:"priority_valid"`
	PowerQoSValid   bool                        `json:"power_qos_valid"`
	PriorityChanged bool                        `json:"priority_changed,omitempty"`
	PowerQoSChanged bool                        `json:"power_qos_changed,omitempty"`
}

var surgeProtectedProcessNames = map[string]bool{
	"system": true, "system idle process": true, "registry": true,
	"dwm.exe": true, "audiodg.exe": true, "csrss.exe": true, "lsass.exe": true,
	"services.exe": true, "svchost.exe": true, "wininit.exe": true, "winlogon.exe": true,
	"explorer.exe": true, "sihost.exe": true, "fontdrvhost.exe": true,
	"startmenuexperiencehost.exe": true, "shellexperiencehost.exe": true,
	"searchhost.exe": true, "textinputhost.exe": true, "securityhealthservice.exe": true,
	"msmpeng.exe": true, "nissrv.exe": true, "taskmgr.exe": true,
}

func surgeProcessProtected(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	if surgeProtectedProcessNames[name] || strings.HasPrefix(name, "kerneon") {
		return true
	}
	_, antiCheat := antiCheatProcessNames[name]
	return antiCheat
}

func processSessionID(pid uint32) (uint32, bool) {
	var session uint32
	ok, _, _ := procProcessIDToSessionID.Call(uintptr(pid), uintptr(unsafe.Pointer(&session)))
	return session, ok != 0
}

func sameInteractiveSession(pid uint32) bool {
	current, currentOK := processSessionID(uint32(os.Getpid()))
	target, targetOK := processSessionID(pid)
	return currentOK && targetOK && current == target
}

// surgeContentionCandidates returns at most two reversible user-space targets.
// It is stricter than the diagnostic contention map: mutation requires a real
// path, a matching interactive session, stable identity and material CPU use.
func surgeContentionCandidates(processes []ProcessMetric, gamePID uint32) []ProcessMetric {
	candidates := make([]ProcessMetric, 0, 2)
	for _, process := range processes {
		if process.PID == 0 || process.PID == gamePID || process.CPU < 5 || process.Path == "" || surgeProcessProtected(process.Name) || !sameInteractiveSession(process.PID) {
			continue
		}
		if process.Started.IsZero() {
			process.Started = queryProcessStarted(process.PID)
		}
		if process.Started.IsZero() {
			continue
		}
		candidates = append(candidates, process)
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].CPU > candidates[j].CPU })
	if len(candidates) > 2 {
		candidates = candidates[:2]
	}
	return candidates
}

func captureBackgroundSnapshots(processes []ProcessMetric) []backgroundProcessSnapshot {
	result := make([]backgroundProcessSnapshot, 0, len(processes))
	for _, process := range processes {
		priority := queryProcessPriority(process.PID)
		qos, qosErr := processPowerQoS(process.PID)
		if priority == 0 && qosErr != nil {
			continue
		}
		result = append(result, backgroundProcessSnapshot{
			PID: process.PID, Name: process.Name, Path: process.Path, Started: process.Started,
			Priority: priority, PowerQoS: qos, PriorityValid: priority != 0, PowerQoSValid: qosErr == nil,
		})
	}
	return result
}

func backgroundIdentityMatches(snapshot backgroundProcessSnapshot) bool {
	path, started := queryProcessPath(snapshot.PID), queryProcessStarted(snapshot.PID)
	if path == "" || started.IsZero() || !strings.EqualFold(path, snapshot.Path) {
		return false
	}
	delta := started.Sub(snapshot.Started)
	return delta <= time.Second && delta >= -time.Second
}

func (a *App) applyAdvancedSurgeTreatments(game ProcessMetric, treatments []SurgeTreatment) {
	if treatment, ok := treatmentByID(treatments, "background-isolation"); ok && treatment.Eligible {
		a.autopilot.mu.RLock()
		targets := append([]backgroundProcessSnapshot(nil), a.autopilot.Background...)
		a.autopilot.mu.RUnlock()
		if len(targets) == 0 {
			a.recordAutopilot("background-isolation", "observed", "Contention passed the metric gate but no process passed the mutation safety gate", "Candidates must be in the same interactive session with a stable path and identity", "Background processes unchanged", "Continue measuring", "No rollback required")
		}
		for index, target := range targets {
			if !backgroundIdentityMatches(target) {
				continue
			}
			priorityChanged, qosChanged := false, false
			if target.PriorityValid && target.Priority != priorityBelowNormal {
				if err := setProcessPriority(target.PID, priorityBelowNormal); err != nil {
					a.recordAutopilot("background-isolation", "failed", "Windows refused the scheduling isolation for "+target.Name, err.Error(), priorityLabel(target.Priority), "No priority change retained", "Original state remains in the crash journal")
				} else {
					priorityChanged = true
				}
			}
			if target.PowerQoSValid {
				eco := processPowerThrottlingState{Version: processPowerThrottlingVersion, ControlMask: processPowerThrottlingExecution, StateMask: processPowerThrottlingExecution}
				if err := setProcessPowerQoS(target.PID, eco); err != nil {
					a.recordAutopilot("background-isolation", "failed", "Windows refused EcoQoS isolation for "+target.Name, err.Error(), "Original power QoS retained or journaled", "No untracked change", "Exact state remains in the crash journal")
				} else {
					qosChanged = true
				}
			}
			if priorityChanged || qosChanged {
				a.autopilot.mu.Lock()
				a.autopilot.ExperimentalChanges = true
				if index < len(a.autopilot.Background) {
					a.autopilot.Background[index].PriorityChanged = priorityChanged
					a.autopilot.Background[index].PowerQoSChanged = qosChanged
				}
				a.autopilot.mu.Unlock()
				a.recordAutopilot("background-isolation", "applied", "A measured user-space contender is competing with "+game.Name, treatment.Reason+" Target: "+target.Name+".", priorityLabel(target.Priority), "Temporarily use Below Normal scheduling and EcoQoS for "+target.Name, "Restore its exact priority and power QoS when the game loses focus or proof regresses")
			}
		}
	}

	if treatment, ok := treatmentByID(treatments, "nvidia-maximum-performance"); ok && treatment.Eligible {
		a.autopilot.mu.RLock()
		settings := append([]NVAPISettingSnapshot(nil), a.autopilot.NVIDIA...)
		a.autopilot.mu.RUnlock()
		for _, setting := range settings {
			if setting.SettingID != nvidiaPreferredPStateID {
				continue
			}
			if err := a.surgeStack.nvapi.applyApplicationDWORD(setting, nvidiaPreferMaximum); err != nil {
				a.recordAutopilot("nvidia-profile", "failed", "NVIDIA refused the documented application-profile transaction", err.Error(), nvidiaPowerPolicyName(setting.PreviousValue), "No untracked driver change is retained", "The crash journal remains able to restore the captured setting")
				continue
			}
			a.autopilot.mu.Lock()
			a.autopilot.NVIDIAChanged, a.autopilot.ExperimentalChanges = true, true
			a.autopilot.mu.Unlock()
			a.recordAutopilot("nvidia-profile", "applied", "GPU-bound frame delivery is eligible for an application clock-readiness policy", treatment.Reason, nvidiaPowerPolicyName(setting.PreviousValue), "Set the documented NVIDIA PREFERRED_PSTATE application value to Prefer maximum performance; this does not exceed firmware clock or power limits", "Restore the exact explicit value, or remove Kerneon's override if the value was inherited")
		}
	}

	if treatment, ok := treatmentByID(treatments, "refresh-aware-frame-cap"); ok && treatment.Eligible {
		target := refreshAwareFrameCap(a.displayRefreshHz)
		a.autopilot.mu.RLock()
		settings := append([]NVAPISettingSnapshot(nil), a.autopilot.NVIDIA...)
		a.autopilot.mu.RUnlock()
		for _, setting := range settings {
			if setting.SettingID != nvidiaFrameRateLimitID || target == 0 {
				continue
			}
			if err := a.surgeStack.nvapi.applyApplicationDWORD(setting, uint32(target)); err != nil {
				a.recordAutopilot("frame-cap-experiment", "failed", "NVIDIA refused the documented application frame-limit transaction", err.Error(), fmtFrameLimit(setting.PreviousValue), "No untracked cap is retained", "The crash journal remains able to restore the captured setting")
				continue
			}
			a.autopilot.mu.Lock()
			a.autopilot.NVIDIAChanged, a.autopilot.ExperimentalChanges = true, true
			a.autopilot.mu.Unlock()
			a.recordAutopilot("frame-cap-experiment", "applied", "Frame delivery is oscillating at the display ceiling", treatment.Reason, fmtFrameLimit(setting.PreviousValue), "Set the documented NVIDIA application frame-rate limit to "+fmt.Sprint(target)+" FPS (about 2% below "+fmt.Sprint(a.displayRefreshHz)+" Hz); a relaunch may be required by the driver", "Restore the exact explicit cap, or remove Kerneon's override if the value was inherited")
		}
	}
	if treatment, ok := treatmentByID(treatments, "driver-latency-trace"); ok && treatment.Eligible {
		a.recordAutopilot("driver-latency-trace", "prepared", "Driver/interrupt latency is the selected stutter route", treatment.Reason, "No driver configuration changed", "Preserve a bounded ETW evidence window before recommending a concrete driver action", "Trace collection is observational and expires automatically")
	}
}

func fmtFrameLimit(value uint32) string {
	if value == 0 {
		return "Disabled or inherited"
	}
	return fmt.Sprint(value) + " FPS"
}
