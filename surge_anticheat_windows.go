//go:build windows

package main

import (
	"fmt"
	"strings"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

// AntiCheatState is intentionally conservative. Kerneon has no injection,
// renderer hook, game-memory reader, anti-cheat manipulator, or kernel driver.
// By default a positive signal also removes game-process mutations and the
// desktop HUD. A user may disable that additional guard, but doing so only
// restores Kerneon's ordinary documented external controls.
type AntiCheatState struct {
	Detected bool
	Provider string
	Evidence string
	Policy   string
}

var antiCheatProcessNames = map[string]string{
	"easyanticheat.exe":                   "Easy Anti-Cheat",
	"easyanticheat_eos.exe":               "Easy Anti-Cheat EOS",
	"easyanticheat_eos_setup.exe":         "Easy Anti-Cheat EOS",
	"beservice.exe":                       "BattlEye",
	"beservice_x64.exe":                   "BattlEye",
	"beserver.exe":                        "BattlEye",
	"vgc.exe":                             "Riot Vanguard",
	"vgtray.exe":                          "Riot Vanguard",
	"faceitservice.exe":                   "FACEIT Anti-cheat",
	"faceitclient.exe":                    "FACEIT Anti-cheat",
	"eaanticheat.gameservice.exe":         "EA AntiCheat",
	"eaanticheat.gameservicelauncher.exe": "EA AntiCheat",
	"eaanticheat.installer.exe":           "EA AntiCheat",
	"pnkbstra.exe":                        "PunkBuster",
	"pnkbstrb.exe":                        "PunkBuster",
	"equ8_server.exe":                     "EQU8",
	"sguard64.exe":                        "Anti-Cheat Expert",
	"anticheatexpertservice.exe":          "Anti-Cheat Expert",
	"gamemon.des":                         "nProtect GameGuard",
}

var antiCheatServiceNames = map[string]string{
	"BEService":          "BattlEye",
	"EasyAntiCheat":      "Easy Anti-Cheat",
	"EasyAntiCheat_EOS":  "Easy Anti-Cheat EOS",
	"vgc":                "Riot Vanguard",
	"FACEITService":      "FACEIT Anti-cheat",
	"EAAntiCheatService": "EA AntiCheat",
	"PnkBstrA":           "PunkBuster",
}

func antiCheatProcessEvidence(processes []ProcessMetric) AntiCheatState {
	for _, process := range processes {
		if provider := antiCheatProcessNames[strings.ToLower(strings.TrimSpace(process.Name))]; provider != "" {
			return AntiCheatState{
				Detected: true,
				Provider: provider,
				Evidence: fmt.Sprintf("%s is active as PID %d", process.Name, process.PID),
				Policy:   "External-only: no game-process mutation and no Kerneon HUD",
			}
		}
	}
	return AntiCheatState{Policy: "Non-injected: public Windows and vendor APIs only"}
}

func detectAntiCheat(processes []ProcessMetric) AntiCheatState {
	if state := antiCheatProcessEvidence(processes); state.Detected {
		return state
	}
	manager, err := mgr.Connect()
	if err != nil {
		return AntiCheatState{Policy: "Non-injected: public Windows and vendor APIs only", Evidence: "Service scan unavailable; invasive paths remain absent"}
	}
	defer manager.Disconnect()
	for name, provider := range antiCheatServiceNames {
		service, openErr := manager.OpenService(name)
		if openErr != nil {
			continue
		}
		status, queryErr := service.Query()
		service.Close()
		if queryErr == nil && status.State != svc.Stopped {
			return AntiCheatState{
				Detected: true,
				Provider: provider,
				Evidence: fmt.Sprintf("Windows service %s is %s", name, serviceStateLabel(status.State)),
				Policy:   "External-only: no game-process mutation and no Kerneon HUD",
			}
		}
	}
	return AntiCheatState{Policy: "Non-injected: public Windows and vendor APIs only"}
}

func serviceStateLabel(state svc.State) string {
	switch state {
	case svc.Running:
		return "running"
	case svc.StartPending, svc.ContinuePending:
		return "starting"
	case svc.Paused, svc.PausePending:
		return "paused"
	case svc.StopPending:
		return "stopping"
	default:
		return "active"
	}
}
