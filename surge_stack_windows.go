//go:build windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/windows/registry"
)

const (
	presentMonOfficialURL = "https://github.com/GameTechDev/PresentMon/releases/latest"
	windowsETWOfficialURL = "https://learn.microsoft.com/en-us/windows/win32/etw/about-event-tracing"
	nvidiaDriverURL       = "https://www.nvidia.com/Download/index.aspx"
	amdDriverURL          = "https://www.amd.com/en/support/download/drivers.html"
	intelDriverURL        = "https://www.intel.com/content/www/us/en/download-center/home.html"
)

type SurgeDependency struct {
	ID, Name, Provider, Purpose string
	Required, Ready, BuiltIn    bool
	Direct                      bool
	DetectedPath, Detail        string
	Scope                       string
	OfficialURL                 string
}

type SurgeGameProfile struct {
	Detected                               bool
	Universal                              bool
	Game, Executable, ExecutablePath, Path string
	Provider, Adapter                      string
	WindowsGPUPreference                   string
	ReflexMode                             string
	RefreshRate, WindowMode                string
	VSync, FrameLimit                      string
	LatencyOpportunity, Evidence           string
	NVIDIA                                 SurgeNVIDIAProfile
}

type SurgeStackSnapshot struct {
	Dependencies                 []SurgeDependency
	GPU                          SurgeGPUTelemetry
	GameProfile                  SurgeGameProfile
	RequiredReady, RequiredTotal int
	CoreReady                    bool
	Installing                   bool
	Message                      string
	Refreshed                    time.Time
}

type SurgeStackState struct {
	mu              sync.RWMutex
	snapshot        SurgeStackSnapshot
	staticRefreshed time.Time
	nvml            NVMLProvider
	nvapi           NVAPIProvider
	adlx            ADLXProvider
	nvapiQuerying   bool
	nvapiQueryPath  string
}

func (state *SurgeStackState) Snapshot() SurgeStackSnapshot {
	state.mu.RLock()
	defer state.mu.RUnlock()
	snapshot := state.snapshot
	snapshot.Dependencies = append([]SurgeDependency(nil), state.snapshot.Dependencies...)
	return snapshot
}

func windowsSystemDirectory() string {
	root := strings.TrimSpace(os.Getenv("WINDIR"))
	if root == "" {
		root = `C:\Windows`
	}
	return filepath.Join(root, "System32")
}

func existingFile(paths ...string) string {
	for _, path := range paths {
		if path == "" {
			continue
		}
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return path
		}
	}
	return ""
}

func detectSurgeDependencies(gpuModel string) []SurgeDependency {
	system := windowsSystemDirectory()
	presentMon := presentMonExecutable()
	dependencies := []SurgeDependency{{
		ID: "presentmon", Name: "Frame Proof", Provider: "Intel PresentMon ETW", Purpose: "Measures frame time, 1% lows and regression so Surge can prove or withdraw a change.",
		Required: true, Ready: presentMon != "", DetectedPath: presentMon, OfficialURL: presentMonOfficialURL,
	}, {
		ID: "windows-latency", Name: "Stutter and latency core", Provider: "Windows PDH + ETW", Purpose: "Correlates long frames with DPC, interrupt, scheduler, paging and storage pressure without touching game memory.",
		Required: true, Ready: existingFile(filepath.Join(system, "pdh.dll")) != "" && existingFile(filepath.Join(system, "advapi32.dll")) != "" && existingFile(filepath.Join(system, "wpr.exe")) != "", BuiltIn: true,
		DetectedPath: existingFile(filepath.Join(system, "wpr.exe")), Detail: "Built into Windows; no third-party service or kernel driver is installed.", OfficialURL: windowsETWOfficialURL,
	}}
	model := strings.ToLower(gpuModel)
	nvapi := existingFile(filepath.Join(system, "nvapi64.dll"))
	nvml := existingFile(filepath.Join(system, "nvml.dll"))
	if strings.Contains(model, "nvidia") || nvapi != "" || nvml != "" {
		ready := nvapi != "" && nvml != ""
		detail := "Requires both the NVIDIA profile and management runtimes supplied by the display driver."
		if ready {
			detail = "Native NVIDIA profile and telemetry runtimes detected; no separate Kerneon download is required."
		}
		dependencies = append(dependencies, SurgeDependency{ID: "nvidia", Name: "NVIDIA native control", Provider: "NVAPI + NVML", Purpose: "Measures GPU headroom and applies only documented per-game power/cap values behind proof and exact rollback.", Required: strings.Contains(model, "nvidia"), Ready: ready, DetectedPath: fallback(nvml, nvapi), Detail: detail, OfficialURL: nvidiaDriverURL})
	}
	adlx := existingFile(filepath.Join(system, "amdadlx64.dll"))
	if strings.Contains(model, "amd") || strings.Contains(model, "radeon") || adlx != "" {
		dependencies = append(dependencies, SurgeDependency{ID: "amd", Name: "AMD native control", Provider: "AMD ADLX", Purpose: "Provides supported Radeon telemetry, tuning ranges and graphics controls.", Required: strings.Contains(model, "amd") || strings.Contains(model, "radeon"), Ready: adlx != "", DetectedPath: adlx, Detail: "Supplied by a compatible AMD Software installation.", OfficialURL: amdDriverURL})
	}
	igcl := existingFile(filepath.Join(system, "igcl.dll"), filepath.Join(system, "igcl64.dll"))
	if strings.Contains(model, "intel") || igcl != "" {
		dependencies = append(dependencies, SurgeDependency{ID: "intel", Name: "Intel native control", Provider: "Intel IGCL", Purpose: "Provides supported Intel graphics telemetry and tuning controls.", Required: strings.Contains(model, "intel"), Ready: igcl != "", DetectedPath: igcl, Detail: "Supplied by a compatible Intel graphics driver installation.", OfficialURL: intelDriverURL})
	}
	return dependencies
}

var profileValuePattern = regexp.MustCompile(`(?is)<%s\s+value="([^"]*)"\s*/?>`)

func xmlProfileValue(data []byte, element string) string {
	pattern := regexp.MustCompile(fmt.Sprintf(profileValuePattern.String(), regexp.QuoteMeta(element)))
	match := pattern.FindSubmatch(data)
	if len(match) != 2 {
		return ""
	}
	return strings.TrimSpace(string(match[1]))
}

type surgeGameAdapter interface {
	Matches(executable string) bool
	Inspect(profile SurgeGameProfile) SurgeGameProfile
}

type rockstarGTAEnhancedAdapter struct{}

func (rockstarGTAEnhancedAdapter) Matches(executable string) bool {
	return strings.EqualFold(executable, "GTA5_Enhanced.exe")
}

func (rockstarGTAEnhancedAdapter) Inspect(profile SurgeGameProfile) SurgeGameProfile {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return profile
	}
	candidates := []string{
		filepath.Join(home, "Documents", "Rockstar Games", "GTAV Enhanced", "settings.xml"),
		filepath.Join(home, "Documents", "Rockstar Games", "GTA V Enhanced", "settings.xml"),
		filepath.Join(home, "OneDrive", "Documents", "Rockstar Games", "GTAV Enhanced", "settings.xml"),
	}
	path := existingFile(candidates...)
	if path == "" {
		return profile
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return profile
	}
	profile.Game, profile.Path = "Grand Theft Auto V Enhanced", path
	profile.Adapter = "Rockstar profile adapter"
	profile.Provider = "Universal engine + Rockstar settings adapter"
	profile.ReflexMode, profile.RefreshRate = xmlProfileValue(data, "ReflexMode"), xmlProfileValue(data, "RefreshRate")
	profile.WindowMode, profile.VSync, profile.FrameLimit = xmlProfileValue(data, "Windowed"), xmlProfileValue(data, "VSync"), xmlProfileValue(data, "FrameLimit")
	if profile.ReflexMode == "0" {
		profile.LatencyOpportunity = "The game profile reports NVIDIA Reflex disabled"
		profile.Evidence = "ReflexMode=0. Kerneon will not guess undocumented values; a validated game adapter or the in-game control is required."
	}
	return profile
}

var surgeGameAdapters = []surgeGameAdapter{
	rockstarGTAEnhancedAdapter{},
}

func windowsGPUPreference(executablePath string) string {
	if strings.TrimSpace(executablePath) == "" {
		return "Unavailable until the game is running"
	}
	key, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\DirectX\UserGpuPreferences`, registry.QUERY_VALUE)
	if err != nil {
		return "Windows default"
	}
	defer key.Close()
	value, _, err := key.GetStringValue(executablePath)
	if err != nil {
		return "Windows default"
	}
	value = strings.ToLower(value)
	switch {
	case strings.Contains(value, "gpupreference=2"):
		return "High performance"
	case strings.Contains(value, "gpupreference=1"):
		return "Power saving"
	default:
		return "Windows default"
	}
}

func detectSurgeGameProfile(lockedProcess, executablePath string) SurgeGameProfile {
	executable := strings.TrimSpace(lockedProcess)
	if executable == "" {
		return SurgeGameProfile{}
	}
	profile := SurgeGameProfile{
		Detected: true, Universal: true, Executable: executable,
		ExecutablePath: executablePath,
		Game:           strings.TrimSuffix(executable, filepath.Ext(executable)),
		Provider:       "Universal executable engine", Adapter: "Universal",
		WindowsGPUPreference: windowsGPUPreference(executablePath),
		LatencyOpportunity:   "Universal frame, driver, scheduling and latency audit active",
		Evidence:             "Every locked executable receives the same provider, proof and rollback pipeline; verified title adapters only add game-owned settings.",
	}
	for _, adapter := range surgeGameAdapters {
		if adapter.Matches(executable) {
			profile = adapter.Inspect(profile)
			break
		}
	}
	return profile
}

func summarizeSurgeReadiness(dependencies []SurgeDependency) (ready, total int, coreReady bool) {
	coreReady = true
	for _, dependency := range dependencies {
		if !dependency.Required {
			continue
		}
		total++
		if dependency.Ready {
			ready++
		} else {
			coreReady = false
		}
	}
	return ready, total, coreReady && total > 0
}

func (a *App) refreshSurgeStack(force bool) {
	current := a.surgeStack.Snapshot()
	now := time.Now()
	dependencies := current.Dependencies
	a.surgeStack.mu.RLock()
	staticRefreshed := a.surgeStack.staticRefreshed
	a.surgeStack.mu.RUnlock()
	staticChanged := force || len(dependencies) == 0 || now.Sub(staticRefreshed) >= 30*time.Second
	if staticChanged {
		dependencies = detectSurgeDependencies(a.snapshot.GPU.Model)
	}
	gpu := current.GPU
	for index, dependency := range dependencies {
		if dependency.ID == "nvidia" && dependency.Ready {
			gpu = a.surgeStack.nvml.Snapshot()
			// NVML returning a physical device is stronger evidence than a model
			// string that may not have arrived from the slower PDH collector yet.
			// Make that provider part of preflight immediately on cold startup.
			if gpu.Ready {
				dependencies[index].Required = true
			}
			break
		}
	}
	gamePath := ""
	if game, ok := a.lockedGameProcess(); ok {
		gamePath = game.Path
		if gamePath == "" {
			gamePath = queryProcessPath(game.PID)
		}
	}
	profile := detectSurgeGameProfile(a.config.Gaming.LockedProcessName, gamePath)
	if gpu.Ready {
		if strings.EqualFold(current.GameProfile.Executable, profile.Executable) && strings.EqualFold(current.GameProfile.ExecutablePath, profile.ExecutablePath) {
			profile.NVIDIA = current.GameProfile.NVIDIA
			if profile.NVIDIA.Ready && profile.NVIDIA.PowerPolicyName != "" {
				profile.Evidence += " NVIDIA power policy: " + profile.NVIDIA.PowerPolicyName + " (" + profile.NVIDIA.Location + ")."
			}
		}
	}
	ready, total, coreReady := summarizeSurgeReadiness(dependencies)
	a.surgeStack.mu.Lock()
	if staticChanged {
		a.surgeStack.staticRefreshed = now
	}
	a.surgeStack.snapshot.Dependencies = dependencies
	a.surgeStack.snapshot.GPU = gpu
	a.surgeStack.snapshot.GameProfile = profile
	a.surgeStack.snapshot.RequiredReady, a.surgeStack.snapshot.RequiredTotal = ready, total
	a.surgeStack.snapshot.CoreReady = coreReady
	a.surgeStack.snapshot.Refreshed = now
	a.surgeStack.mu.Unlock()
	if gpu.Ready && profile.NVIDIA.Provider == "" {
		a.queueNVAPIProfileQuery(profile)
	}
}

func (a *App) queueNVAPIProfileQuery(profile SurgeGameProfile) {
	queryPath := profile.ExecutablePath
	a.surgeStack.mu.Lock()
	if a.surgeStack.nvapiQuerying {
		a.surgeStack.mu.Unlock()
		return
	}
	a.surgeStack.nvapiQuerying, a.surgeStack.nvapiQueryPath = true, queryPath
	a.surgeStack.mu.Unlock()
	go func(expectedExecutable, expectedPath string) {
		result := a.surgeStack.nvapi.QueryPowerPolicy(expectedPath)
		a.surgeStack.mu.Lock()
		if strings.EqualFold(a.surgeStack.snapshot.GameProfile.Executable, expectedExecutable) && strings.EqualFold(a.surgeStack.snapshot.GameProfile.ExecutablePath, expectedPath) {
			a.surgeStack.snapshot.GameProfile.NVIDIA = result
			if result.Ready && result.PowerPolicyName != "" {
				a.surgeStack.snapshot.GameProfile.Evidence += " NVIDIA power policy: " + result.PowerPolicyName + " (" + result.Location + ")."
			}
		}
		a.surgeStack.nvapiQuerying = false
		a.surgeStack.mu.Unlock()
		if a.hwnd != 0 {
			procPostMessageW.Call(a.hwnd, WM_APP_RENDER, 0, 0)
		}
	}(profile.Executable, queryPath)
}

func (a *App) setSurgeDependencyMessage(message string, installing bool) {
	a.surgeStack.mu.Lock()
	a.surgeStack.snapshot.Message, a.surgeStack.snapshot.Installing = message, installing
	a.surgeStack.mu.Unlock()
	if a.hwnd != 0 {
		procPostMessageW.Call(a.hwnd, WM_APP_RENDER, 0, 0)
	}
}

func (a *App) installPresentMonDependency() {
	stack := a.surgeStack.Snapshot()
	for _, dependency := range stack.Dependencies {
		if dependency.ID == "presentmon" && dependency.Ready {
			a.setSurgeDependencyMessage("PresentMon is already ready.", false)
			return
		}
	}
	if stack.Installing {
		return
	}
	winget, err := exec.LookPath("winget.exe")
	if err != nil {
		shellOpen(presentMonOfficialURL)
		a.setSurgeDependencyMessage("Windows Package Manager is unavailable; the official download page was opened.", false)
		return
	}
	a.setSurgeDependencyMessage("Installing Intel PresentMon from the verified winget source…", true)
	go func() {
		command := exec.Command(winget, "install", "--id", "Intel.PresentMon", "--exact", "--source", "winget", "--accept-source-agreements", "--accept-package-agreements", "--disable-interactivity")
		command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
		output, installErr := command.CombinedOutput()
		if installErr != nil {
			detail := strings.TrimSpace(string(output))
			if len(detail) > 180 {
				detail = detail[len(detail)-180:]
			}
			a.setSurgeDependencyMessage("PresentMon installation failed: "+fallback(detail, installErr.Error()), false)
			return
		}
		a.refreshSurgeStack(true)
		a.setSurgeDependencyMessage("PresentMon installed. Frame Proof is ready.", false)
	}()
}

func (a *App) closeSurgeProviders() {
	a.surgeStack.nvml.Close()
	a.surgeStack.nvapi.Close()
	a.surgeStack.adlx.Close()
}
