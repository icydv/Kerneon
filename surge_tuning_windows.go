//go:build windows

package main

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	tuningLabDisclaimerVersion = 2
	tuningConfirmationSettle   = 3 * time.Second
	tuningConfirmationWindow   = 20 * time.Second
	tuningConfirmationPairs    = 2
	amdADLXOfficialURL         = "https://gpuopen.com/adlx/"
	msiAfterburnerOfficialURL  = "https://www.msi.com/Landing/afterburner/graphics-cardsspeed"
)

const tuningLabDisclaimer = "Hardware tuning operates outside factory settings and may cause crashes, freezes, data loss or corruption, overheating, higher power use or noise, reduced component life, warranty or support consequences, and permanent damage. Vendor limits, thermal aborts and rollback reduce risk; they do not guarantee safety or recovery. By enabling this optional feature, you knowingly accept the risks inherent in non-stock operation and are responsible for backups, cooling, power delivery and checking warranty terms. Kerneon, Ryan Horth and the publisher do not guarantee stability, a performance gain, warranty coverage or successful recovery. Nothing in this notice excludes or limits statutory consumer rights or any liability that cannot lawfully be excluded."

type TuningDomainCapability struct {
	Name, Device, Provider, State, Detail string
	Controls                              string
	Supported, Direct, Guided             bool
	RequiresElevation, RequiresRestart    bool
	OfficialURL, InstalledPath            string
}

type TuningLabCapability struct {
	GPU, Memory TuningDomainCapability
	GPUOffsets  NVMLClockCapability
	AMD         ADLXGPUCapability
	Estimate    TuningHardwareEstimate
	Refreshed   time.Time
}

type TuningHardwareEstimate struct {
	Ready                                 bool
	Samples                               int
	Duration                              time.Duration
	AverageTemperatureC, PeakTemperatureC float64
	AveragePowerW, PeakPowerW             float64
	ClockLimitReason                      string
}

type TuningTrial struct {
	At                  time.Time
	Domain, Result      string
	CoreMHz, MemoryMHz  int32
	PowerLimitW         float64
	FPS                 float64
	OnePercentLow       float64
	P99FrameMs, Hitches float64
	TemperatureC        float64
	Comparable          bool
	Comparison          string
}

type TuningLabState struct {
	mu           sync.RWMutex
	Capability   TuningLabCapability
	Running      bool
	Estimating   bool
	Applied      bool
	Recovery     bool
	Stage        string
	Status       string
	Progress     float64
	CoreMHz      int32
	MemoryMHz    int32
	PowerLimitW  float64
	Temperature  float64
	Trials       []TuningTrial
	AttemptedPID uint32
	stop         chan struct{}
	done         chan struct{}
	stopOnce     sync.Once
	original     hardwareTuningSnapshot
	estimateRun  uint64
}

type TuningLabView struct {
	Capability          TuningLabCapability
	Running, Estimating bool
	Applied, Recovery   bool
	Stage, Status       string
	Progress            float64
	CoreMHz, MemoryMHz  int32
	PowerLimitW         float64
	Temperature         float64
	Trials              []TuningTrial
}

type hardwareTuningSnapshot struct {
	Version              int       `json:"version"`
	Provider             string    `json:"provider"`
	Device               string    `json:"device"`
	CoreSupported        bool      `json:"core_supported"`
	MemorySupported      bool      `json:"memory_supported"`
	PowerSupported       bool      `json:"power_supported"`
	CoreOffset           int32     `json:"core_offset_mhz"`
	MemoryOffset         int32     `json:"memory_offset_mhz"`
	PowerLimitMilliwatts uint32    `json:"power_limit_milliwatts"`
	AMDFactoryOriginal   bool      `json:"amd_factory_original,omitempty"`
	CapturedAt           time.Time `json:"captured_at"`
}

type tuningEnvelope struct {
	CoreStep, MemoryStep         int32
	PowerStepW                   uint32
	CoreFraction, MemoryFraction float64
	PowerFraction                float64
	CoreHardCap, MemoryHardCap   int32
	PowerHardCapW                uint32
	TemperatureLimit             float64
}

type tuningProfileExplanation struct {
	Title, BestFor, MayTest, Search, ThisPC, Stops string
	Color                                          uint32
	Technical                                      bool
}

func tuningProfileCapabilityText(capability TuningLabCapability, technical bool) string {
	if capability.AMD.Ready {
		controls := make([]string, 0, 3)
		if capability.AMD.GPUClock {
			controls = append(controls, "GPU core auto-overclock")
		}
		if capability.AMD.VRAMClock {
			controls = append(controls, "VRAM auto-overclock")
		}
		if capability.AMD.Undervolt {
			controls = append(controls, "GPU auto-undervolt")
		}
		if !technical {
			return "AMD's driver confirms that this exact card supports " + strings.Join(controls, ", ") + ". The final GPU curve is calculated for the individual card."
		}
		return fmt.Sprintf("AMD ADLX IADLXGPUAutoTuning: %s. Factory state before tuning: %t. Device: %s. ADLX ResetToFactory is the GPU rollback boundary.", strings.Join(controls, ", "), capability.AMD.AtFactory, fallback(capability.AMD.Device, capability.GPU.Device))
	}
	available := make([]string, 0, 3)
	if capability.GPUOffsets.Power.Supported {
		available = append(available, "GPU power allowance")
	}
	if capability.GPUOffsets.Core.Supported {
		available = append(available, "GPU core speed")
	}
	if capability.GPUOffsets.Memory.Supported {
		available = append(available, "graphics-memory speed")
	}
	if len(available) == 0 {
		if technical {
			return "No direct GPU write domain passed capability discovery."
		}
		return "Kerneon cannot safely change this card's clocks from the current driver. It will not pretend to tune them."
	}
	if !technical {
		detail := "Kerneon can directly tune this PC's " + strings.Join(available, ", ") + "."
		if !capability.GPUOffsets.Core.Supported || !capability.GPUOffsets.Memory.Supported {
			detail += " Any clock control the card has locked will be skipped, not guessed."
		}
		return detail
	}
	parts := make([]string, 0, 5)
	if domain := capability.GPUOffsets.Core; domain.Supported {
		parts = append(parts, fmt.Sprintf("GPU core P%d: %+d MHz now, range %+d..%+d MHz via %s", domain.PState, domain.Current, domain.Min, domain.Max, fallback(domain.API, "NVML")))
	}
	if domain := capability.GPUOffsets.Memory; domain.Supported {
		parts = append(parts, fmt.Sprintf("VRAM P%d: %+d MHz now, range %+d..%+d MHz via %s", domain.PState, domain.Current, domain.Min, domain.Max, fallback(domain.API, "NVML")))
	}
	if domain := capability.GPUOffsets.Power; domain.Supported {
		parts = append(parts, fmt.Sprintf("GPU power: %.0f W now, %.0f W factory, %.0f..%.0f W driver range", float64(domain.Current)/1000, float64(domain.Default)/1000, float64(domain.Min)/1000, float64(domain.Max)/1000))
	}
	if estimate := capability.Estimate; estimate.Ready {
		parts = append(parts, fmt.Sprintf("estimate: %d samples over %.1f s; %.1f°C average / %.1f°C peak; %.1f W average / %.1f W peak; clock limit: %s", estimate.Samples, estimate.Duration.Seconds(), estimate.AverageTemperatureC, estimate.PeakTemperatureC, estimate.AveragePowerW, estimate.PeakPowerW, fallback(estimate.ClockLimitReason, "none reported")))
	}
	return strings.Join(parts, ". ") + ". Voltage is not armed without a bounded vendor curve and exact reset path."
}

func profileEnabledControlText(capability TuningLabCapability, envelope tuningEnvelope, technical bool) string {
	if capability.AMD.Ready {
		controls := make([]string, 0, 2)
		if capability.AMD.GPUClock {
			controls = append(controls, "AMD's per-card GPU core auto-overclock")
		}
		if capability.AMD.VRAMClock && envelope.CoreFraction >= 0.30 {
			controls = append(controls, "AMD's per-card VRAM auto-overclock")
		}
		if len(controls) == 0 && capability.AMD.VRAMClock {
			controls = append(controls, "AMD's per-card VRAM auto-overclock")
		}
		if !capability.Estimate.Ready {
			return "Kerneon is checking which AMD clock engines this card exposes. Nothing is being changed during this three-second estimate."
		}
		if technical {
			return "Armed ADLX domains: " + strings.Join(controls, "; ") + ". ADLX performs silicon-specific curve discovery asynchronously; Kerneon supplies the proof/rollback boundary and never substitutes a fixed cross-card MHz value."
		}
		return "On this PC it will run " + strings.Join(controls, " and ") + ". AMD calculates the actual clock curve for this individual card; Kerneon then measures the game and keeps it only if it helps."
	}
	coreDelta, memoryDelta, powerDeltaW := estimatedTuningDeltas(capability, envelope)
	controls := make([]string, 0, 3)
	if capability.GPUOffsets.Power.Supported {
		if technical {
			controls = append(controls, fmt.Sprintf("power estimate +%d W (%d W steps; hard cap +%d W; %.0f%% of unused driver range)", powerDeltaW, envelope.PowerStepW, envelope.PowerHardCapW, envelope.PowerFraction*100))
		} else {
			controls = append(controls, fmt.Sprintf("give the GPU roughly %d W more power room when power is holding it back", powerDeltaW))
		}
	}
	if capability.GPUOffsets.Core.Supported {
		if technical {
			controls = append(controls, fmt.Sprintf("P0 core estimate +%d MHz (%d MHz steps; hard cap +%d MHz; %.0f%% of unused range)", coreDelta, envelope.CoreStep, envelope.CoreHardCap, envelope.CoreFraction*100))
		} else {
			controls = append(controls, fmt.Sprintf("explore roughly %d MHz of extra GPU core speed in small steps", coreDelta))
		}
	}
	if capability.GPUOffsets.Memory.Supported {
		if technical {
			controls = append(controls, fmt.Sprintf("P0 VRAM estimate +%d MHz (%d MHz steps; hard cap +%d MHz; %.0f%% of unused range)", memoryDelta, envelope.MemoryStep, envelope.MemoryHardCap, envelope.MemoryFraction*100))
		} else {
			controls = append(controls, fmt.Sprintf("explore roughly %d MHz of extra graphics-memory speed", memoryDelta))
		}
	}
	if len(controls) == 0 {
		return "No GPU overclocking control is armed on this PC. Kerneon will direct you to the detected manufacturer route instead of running an empty test."
	}
	if !capability.Estimate.Ready {
		return "Kerneon is still estimating this PC's usable search envelope. No settings are being changed during this three-second check."
	}
	prefix := "On this PC it will test: "
	if technical {
		prefix = "Armed domains: "
	}
	return prefix + strings.Join(controls, "; ") + ". Controls not reported as writable are excluded from the run."
}

func estimatedTuningDeltas(capability TuningLabCapability, envelope tuningEnvelope) (coreMHz, memoryMHz int32, powerW uint32) {
	factor := 0.72
	if capability.Estimate.Ready {
		headroom := envelope.TemperatureLimit - capability.Estimate.PeakTemperatureC
		factor = clampFloat(headroom/50, 0.25, 1)
	}
	if domain := capability.GPUOffsets.Core; domain.Supported {
		target := tuningTarget(domain.Current, domain.Max, envelope.CoreHardCap, envelope.CoreFraction)
		coreMHz = int32(math.Round(float64(target-domain.Current) * factor))
		if coreMHz < 1 && target > domain.Current {
			coreMHz = 1
		}
	}
	if domain := capability.GPUOffsets.Memory; domain.Supported {
		target := tuningTarget(domain.Current, domain.Max, envelope.MemoryHardCap, envelope.MemoryFraction)
		memoryMHz = int32(math.Round(float64(target-domain.Current) * factor))
		if memoryMHz < 1 && target > domain.Current {
			memoryMHz = 1
		}
	}
	if domain := capability.GPUOffsets.Power; domain.Supported {
		target := tuningPowerTarget(domain.Current, domain.Max, envelope.PowerHardCapW, envelope.PowerFraction)
		powerW = uint32(math.Round(float64(target-domain.Current) / 1000 * factor))
		if powerW < 1 && target > domain.Current {
			powerW = 1
		}
	}
	return
}

// estimatedProfileTemperatureLimit turns the live thermal sample into a
// profile-specific operating ceiling. The values in tuningProfileEnvelope are
// absolute safety maxima, not estimates, and must never be presented as though
// they came from hardware that has not been sampled yet.
func estimatedProfileTemperatureLimit(profile string, estimate TuningHardwareEstimate) (float64, bool) {
	if !estimate.Ready || estimate.Samples < 1 || estimate.PeakTemperatureC <= 0 || math.IsNaN(estimate.PeakTemperatureC) || math.IsInf(estimate.PeakTemperatureC, 0) {
		return 0, false
	}
	envelope := tuningProfileEnvelope(profile)
	riseBudget := 31.0
	switch profile {
	case "conservative":
		riseBudget = 24
	case "enthusiast":
		riseBudget = 37
	}
	limit := math.Min(envelope.TemperatureLimit, estimate.PeakTemperatureC+riseBudget)
	return math.Round(limit), true
}

func estimatedTuningEnvelope(profile string, estimate TuningHardwareEstimate) (tuningEnvelope, bool) {
	envelope := tuningProfileEnvelope(profile)
	limit, ready := estimatedProfileTemperatureLimit(profile, estimate)
	if !ready {
		envelope.TemperatureLimit = 0
		return envelope, false
	}
	envelope.TemperatureLimit = limit
	return envelope, true
}

func tuningProfileCardDetail(profile string, capability TuningLabCapability, technical bool) string {
	limit, ready := estimatedProfileTemperatureLimit(profile, capability.Estimate)
	if !ready {
		return "Estimating live envelope…"
	}
	if technical {
		envelope := tuningProfileEnvelope(profile)
		return fmt.Sprintf("%d W · %d/%d MHz · %.0f°C", envelope.PowerStepW, envelope.CoreStep, envelope.MemoryStep, limit)
	}
	label := "Measured"
	switch profile {
	case "conservative":
		label = "Cautious"
	case "enthusiast":
		label = "Wider"
	}
	return fmt.Sprintf("%s · %.0f°C", label, limit)
}

func explainTuningProfile(profile string, capability TuningLabCapability, technical bool) tuningProfileExplanation {
	envelope, thermalReady := estimatedTuningEnvelope(profile, capability.Estimate)
	explanation := tuningProfileExplanation{
		ThisPC:    tuningProfileCapabilityText(capability, technical),
		Technical: technical,
	}
	switch profile {
	case "conservative":
		explanation.Title = "Conservative · smallest useful search"
		explanation.BestFor = "Best when you want Kerneon to look for easy headroom with the least extra heat, power and trial time."
		explanation.Color = palette.Green
	case "enthusiast":
		explanation.Title = "Enthusiast · widest measured search"
		explanation.BestFor = "Best for well-cooled desktops where extra heat, fan noise, power draw and a longer discovery run are acceptable. It is still bounded—not an unlimited maximum."
		explanation.Color = palette.Violet
	default:
		explanation.Title = "Balanced · useful headroom without chasing the edge"
		explanation.BestFor = "Best for most gaming PCs. It searches meaningfully beyond stock while keeping more thermal and electrical margin than Enthusiast."
		explanation.Color = palette.Cyan
	}
	explanation.MayTest = profileEnabledControlText(capability, envelope, technical)
	if !thermalReady {
		if technical {
			explanation.Search = "Thermal ceiling unarmed. Kerneon must finish a valid live telemetry sample before it calculates or displays this profile's operating ceiling; no tuning transaction can start before then."
			explanation.Stops = "No clock or power write is permitted while the live estimate is pending or unavailable. Once armed, the normal frame-regression, hardware-fault, temperature, focus, Surge-stop and write-ahead rollback gates apply."
		} else {
			explanation.Search = "Kerneon is measuring this PC before it decides how much thermal room this profile may use. No temperature target is shown or armed until that live estimate is complete."
			explanation.Stops = "Nothing is changed during the estimate. If valid temperature telemetry cannot be read, discovery remains unavailable rather than guessing a limit."
		}
	} else if technical {
		explanation.Search = fmt.Sprintf("20 s stock baseline; each candidate gets an ~18 s workload-comparable PresentMon window with at least 120 samples. Acceptance requires ≥4%% 1%%-low gain, ≥8%% p99/display-tail reduction, or ≥30%% hitch reduction. Thermal ceiling: %.0f°C.", envelope.TemperatureLimit)
		explanation.Stops = "Immediate rollback on ≥10% 1%-low loss, ≥12% p99 regression, ≥15% display-tail regression, +2 percentage points dropped frames, WHEA, Display 4101, Kernel-Power 41, NVIDIA hardware safeguard, temperature ceiling, focus/process loss, Surge stop, or shutdown. The write-ahead snapshot restores every originally supported control."
	} else {
		explanation.Search = fmt.Sprintf("Kerneon first watches the game at stock settings for 20 seconds. It then changes one available control at a time, watches for about 18 seconds, and keeps a result only if smoother frame delivery—not merely a higher clock—is clearly measured. It stops at %.0f°C.", envelope.TemperatureLimit)
		explanation.Stops = "If the game becomes less smooth, the temperature limit is reached, Windows reports a hardware or display problem, the game loses focus, or Surge stops, Kerneon puts every changed value back exactly as it was. A stable overclock is not kept unless it also improves the game."
	}
	return explanation
}

func processIsElevated() bool {
	return windows.GetCurrentProcessToken().IsElevated()
}

func (a *App) restartElevatedTuningLab() error {
	if processIsElevated() {
		return nil
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	if err := a.store.Save(a.configSnapshot()); err != nil {
		return fmt.Errorf("save settings before elevation: %w", err)
	}
	verb, file, parameters := utf16Ptr("runas"), utf16Ptr(executable), utf16Ptr("--surge-tuning-elevated")
	result, _, callErr := procShellExecuteW.Call(0, uintptr(unsafe.Pointer(verb)), uintptr(unsafe.Pointer(file)), uintptr(unsafe.Pointer(parameters)), 0, SW_SHOW)
	if result <= 32 {
		return fmt.Errorf("Windows declined the explicit administrator launch: %v", callErr)
	}
	// The elevated copy waits for the single-instance mutex. Release every
	// session mutation and network listener before allowing it to continue.
	a.shutdown()
	procDestroyWindow.Call(a.hwnd)
	return nil
}

func tuningProfileEnvelope(profile string) tuningEnvelope {
	switch profile {
	case "conservative":
		return tuningEnvelope{CoreStep: 15, MemoryStep: 50, PowerStepW: 3, CoreFraction: 0.15, MemoryFraction: 0.12, PowerFraction: 0.18, CoreHardCap: 75, MemoryHardCap: 250, PowerHardCapW: 8, TemperatureLimit: 78}
	case "enthusiast":
		return tuningEnvelope{CoreStep: 25, MemoryStep: 100, PowerStepW: 5, CoreFraction: 0.50, MemoryFraction: 0.40, PowerFraction: 0.65, CoreHardCap: 180, MemoryHardCap: 800, PowerHardCapW: 30, TemperatureLimit: 84}
	default:
		return tuningEnvelope{CoreStep: 15, MemoryStep: 75, PowerStepW: 5, CoreFraction: 0.30, MemoryFraction: 0.25, PowerFraction: 0.35, CoreHardCap: 120, MemoryHardCap: 500, PowerHardCapW: 15, TemperatureLimit: 82}
	}
}

func tuningPowerTarget(current, maximum, hardCapW uint32, fraction float64) uint32 {
	if maximum <= current || hardCapW == 0 || fraction <= 0 {
		return current
	}
	headroom := maximum - current
	delta := uint32(math.Round(float64(headroom) * fraction))
	if delta < 1000 {
		delta = 1000
	}
	hardCap := hardCapW * 1000
	if delta > hardCap {
		delta = hardCap
	}
	if current+delta < current || current+delta > maximum {
		return maximum
	}
	return current + delta
}

func tuningPowerSteps(current, target, stepW uint32) []uint32 {
	step := stepW * 1000
	if step == 0 || target <= current {
		return nil
	}
	values := make([]uint32, 0, int((target-current)/step)+1)
	for value := current + step; value < target; value += step {
		values = append(values, value)
	}
	return append(values, target)
}

func tuningTarget(current, maximum, hardCap int32, fraction float64) int32 {
	if maximum <= current || hardCap <= 0 || fraction <= 0 {
		return current
	}
	headroom := maximum - current
	delta := int32(math.Round(float64(headroom) * fraction))
	if delta < 1 {
		delta = 1
	}
	if delta > hardCap {
		delta = hardCap
	}
	if current+delta > maximum {
		return maximum
	}
	return current + delta
}

func tuningSteps(current, target, step int32) []int32 {
	if step <= 0 || target <= current {
		return nil
	}
	values := make([]int32, 0, int((target-current)/step)+1)
	for value := current + step; value < target; value += step {
		values = append(values, value)
	}
	return append(values, target)
}

func findTuningProvider(paths ...string) string {
	return existingFile(paths...)
}

func (a *App) detectTuningLabCapability() TuningLabCapability {
	capability := TuningLabCapability{Refreshed: time.Now()}
	gpuModel := fallback(a.surgeStack.Snapshot().GPU.Name, a.snapshot.GPU.Model)
	capability.GPU = TuningDomainCapability{Name: "GPU + VRAM", Device: fallback(gpuModel, "Detecting graphics hardware"), RequiresElevation: true}
	modelLower := strings.ToLower(gpuModel)
	switch {
	case strings.Contains(modelLower, "nvidia") || strings.Contains(modelLower, "geforce"):
		capability.GPUOffsets = a.surgeStack.nvml.ClockCapability()
		capability.GPU.Provider = "NVIDIA driver (NVML)"
		capability.GPU.Supported = true
		capability.GPU.Direct = capability.GPUOffsets.Ready
		capability.GPU.OfficialURL = nvidiaDriverURL
		if capability.GPU.Direct {
			capability.GPU.State = "DIRECT DRIVER CONTROL"
			controls := make([]string, 0, 4)
			if capability.GPUOffsets.Core.Supported {
				controls = append(controls, "core clock")
			}
			if capability.GPUOffsets.Memory.Supported {
				controls = append(controls, "VRAM clock")
			}
			if capability.GPUOffsets.Power.Supported {
				controls = append(controls, "power ceiling")
			}
			capability.GPU.Controls = "Direct: " + strings.Join(controls, " + ") + " · voltage curve: not exposed"
			capability.GPU.Detail = "The driver exposes an explicit range for each listed control. Kerneon can run a stepped, session-only discovery with exact rollback."
		} else {
			afterburner := findTuningProvider(
				`C:\Program Files (x86)\MSI Afterburner\MSIAfterburner.exe`,
				`C:\Program Files\MSI Afterburner\MSIAfterburner.exe`,
			)
			capability.GPU.Provider = "MSI Afterburner OC Scanner"
			capability.GPU.Guided, capability.GPU.InstalledPath = afterburner != "", afterburner
			capability.GPU.OfficialURL = msiAfterburnerOfficialURL
			capability.GPU.State = "OC SCANNER REQUIRED"
			capability.GPU.Controls = "Clock/voltage curve: provider-owned OC Scanner · proof: Kerneon"
			capability.GPU.Detail = "This GeForce driver exposes telemetry but not public clock-offset writes on this device. Kerneon refuses undocumented NVAPI calls; MSI's official OC Scanner is the supported fallback."
			if afterburner != "" {
				capability.GPU.State = "GUIDED OC SCANNER"
			}
		}
	case strings.Contains(modelLower, "amd") || strings.Contains(modelLower, "radeon"):
		capability.AMD = a.surgeStack.adlx.Capability()
		if domain := capability.AMD.Core; domain.Supported {
			capability.GPUOffsets.Core = NVMLClockDomainCapability{Supported: true, Current: domain.Current, Min: domain.Min, Max: domain.Max, API: domain.API}
		}
		if domain := capability.AMD.VRAM; domain.Supported {
			capability.GPUOffsets.Memory = NVMLClockDomainCapability{Supported: true, Current: domain.Current, Min: domain.Min, Max: domain.Max, API: domain.API}
		}
		capability.GPUOffsets.Ready = capability.GPUOffsets.Core.Supported || capability.GPUOffsets.Memory.Supported
		capability.GPU.Provider, capability.GPU.OfficialURL = "AMD ADLX Auto Tuning", amdADLXOfficialURL
		capability.GPU.Supported = true
		capability.GPU.Direct = capability.AMD.Ready
		if capability.AMD.Ready {
			capability.GPU.Device = fallback(capability.AMD.Device, capability.GPU.Device)
			capability.GPU.State = "DIRECT AMD AUTO CLOCKS"
			controls := make([]string, 0, 3)
			if capability.AMD.GPUClock {
				controls = append(controls, "GPU core auto-overclock")
			}
			if capability.AMD.VRAMClock {
				controls = append(controls, "VRAM auto-overclock")
			}
			if capability.AMD.Undervolt {
				controls = append(controls, "GPU auto-undervolt")
			}
			capability.GPU.Controls = strings.Join(controls, " + ")
			capability.GPU.Detail = "AMD ADLX validated these controls against this exact card and exposes Reset to Factory. A non-factory existing profile is never overwritten without an exact recovery route."
		} else {
			capability.GPU.State = "ADLX CLOCKS LOCKED"
			capability.GPU.Controls = "GPU + VRAM auto clock support queried directly from AMD ADLX"
			capability.GPU.Detail = fallback(capability.AMD.Error, "The installed Radeon driver did not expose a writable automatic clock domain for this card.")
		}
	default:
		capability.GPU.State = "NOT SUPPORTED"
		capability.GPU.Detail = "No manufacturer-supported graphics tuning provider was detected."
	}

	capability.Memory = TuningDomainCapability{Name: "System memory", State: "OUT OF SCOPE", Detail: "RAM tuning is intentionally not part of Surge Tuning Lab."}
	return capability
}

func (a *App) refreshTuningLabCapability() {
	a.tuningLab.mu.RLock()
	estimate := a.tuningLab.Capability.Estimate
	a.tuningLab.mu.RUnlock()
	capability := a.detectTuningLabCapability()
	capability.Estimate = estimate
	a.tuningLab.mu.Lock()
	a.tuningLab.Capability = capability
	if a.tuningLab.Status == "" {
		a.tuningLab.Status = "Capability audit complete. Nothing has been changed."
	}
	a.tuningLab.mu.Unlock()
}

func (a *App) holdTuningEstimateWhileSurgeActive() {
	a.tuningLab.mu.Lock()
	a.tuningLab.estimateRun++
	a.tuningLab.Estimating = false
	a.tuningLab.Progress = 0
	if a.tuningLab.Capability.Estimate.Ready {
		a.tuningLab.Stage = "STOCK ESTIMATE LOCKED"
		a.tuningLab.Status = "Surge is active. Kerneon is reusing the completed stock estimate and will not recalculate temperatures, power headroom or search ranges while performance changes are in progress."
	} else {
		a.tuningLab.Stage = "STOCK ESTIMATE REQUIRED"
		a.tuningLab.Status = "Surge is active and no stock estimate exists in this session. Pause Surge to measure an unmodified baseline; Kerneon will not estimate from boosted hardware."
	}
	a.tuningLab.mu.Unlock()
}

func (a *App) estimateTuningLabCapability() {
	if a.surgeIsEnabled() {
		a.refreshTuningLabCapability()
		a.holdTuningEstimateWhileSurgeActive()
		return
	}
	a.tuningLab.mu.RLock()
	previousEstimate := a.tuningLab.Capability.Estimate
	a.tuningLab.mu.RUnlock()
	capability := a.detectTuningLabCapability()
	a.tuningLab.mu.Lock()
	a.tuningLab.estimateRun++
	run := a.tuningLab.estimateRun
	a.tuningLab.Capability = capability
	a.tuningLab.Estimating = true
	a.tuningLab.Progress = 0
	a.tuningLab.Stage = "ESTIMATING HARDWARE"
	a.tuningLab.Status = "Reading live driver ranges, temperatures, power headroom and clock-limit reasons. Nothing is being changed."
	a.tuningLab.mu.Unlock()
	go func() {
		started := time.Now()
		const samples = 7
		var validSamples int
		var temperatureTotal, powerTotal, peakTemperature, peakPower float64
		limiter := ""
		for index := 0; index < samples; index++ {
			if a.surgeIsEnabled() {
				capability.Estimate = previousEstimate
				a.tuningLab.mu.Lock()
				if a.tuningLab.estimateRun == run {
					a.tuningLab.Capability = capability
				}
				a.tuningLab.mu.Unlock()
				a.holdTuningEstimateWhileSurgeActive()
				if a.hwnd != 0 {
					procPostMessageW.Call(a.hwnd, WM_APP_RENDER, 0, 0)
				}
				return
			}
			if capability.AMD.Ready {
				telemetry := a.surgeStack.adlx.Snapshot()
				if telemetry.Ready && math.Max(telemetry.TemperatureC, telemetry.HotspotC) > 0 {
					temperature := math.Max(telemetry.TemperatureC, telemetry.HotspotC)
					validSamples++
					temperatureTotal += temperature
					powerTotal += telemetry.PowerW
					peakTemperature = math.Max(peakTemperature, temperature)
					peakPower = math.Max(peakPower, telemetry.PowerW)
					limiter = "AMD telemetry did not report an active hardware fault"
				}
			} else {
				telemetry := a.surgeStack.nvml.Snapshot()
				if telemetry.Ready && telemetry.TemperatureC > 0 {
					validSamples++
					temperatureTotal += telemetry.TemperatureC
					powerTotal += telemetry.PowerW
					peakTemperature = math.Max(peakTemperature, telemetry.TemperatureC)
					peakPower = math.Max(peakPower, telemetry.PowerW)
					limiter = telemetry.ThrottleSummary
				}
			}
			a.tuningLab.mu.Lock()
			if a.tuningLab.estimateRun != run {
				a.tuningLab.mu.Unlock()
				return
			}
			a.tuningLab.Progress = float64(index+1) / samples
			a.tuningLab.mu.Unlock()
			if a.hwnd != 0 {
				procPostMessageW.Call(a.hwnd, WM_APP_RENDER, 0, 0)
			}
			if index+1 < samples {
				time.Sleep(500 * time.Millisecond)
			}
		}
		if a.surgeIsEnabled() {
			capability.Estimate = previousEstimate
			a.tuningLab.mu.Lock()
			if a.tuningLab.estimateRun == run {
				a.tuningLab.Capability = capability
			}
			a.tuningLab.mu.Unlock()
			a.holdTuningEstimateWhileSurgeActive()
			return
		}
		capability.Estimate = TuningHardwareEstimate{Samples: validSamples, Duration: time.Since(started)}
		if validSamples > 0 {
			capability.Estimate.Ready = true
			capability.Estimate.AverageTemperatureC = temperatureTotal / float64(validSamples)
			capability.Estimate.PeakTemperatureC = peakTemperature
			capability.Estimate.AveragePowerW = powerTotal / float64(validSamples)
			capability.Estimate.PeakPowerW = peakPower
			capability.Estimate.ClockLimitReason = limiter
		}
		capability.Refreshed = time.Now()
		a.tuningLab.mu.Lock()
		if a.tuningLab.estimateRun == run {
			a.tuningLab.Capability = capability
			a.tuningLab.Estimating = false
			a.tuningLab.Progress = 0
			if capability.Estimate.Ready {
				a.tuningLab.Stage = "HARDWARE ESTIMATE READY"
				a.tuningLab.Status = "Three hardware-specific search envelopes are ready. Their thermal ceilings were calculated from the live sample; only measured, stable improvements may be retained."
			} else {
				a.tuningLab.Stage = "THERMAL ESTIMATE UNAVAILABLE"
				a.tuningLab.Status = "Kerneon could not read valid live temperature telemetry. No thermal ceiling was guessed or armed, so hardware discovery remains unavailable."
			}
		}
		a.tuningLab.mu.Unlock()
		if a.hwnd != 0 {
			procPostMessageW.Call(a.hwnd, WM_APP_RENDER, 0, 0)
		}
	}()
}

func (a *App) tuningLabSnapshot() TuningLabView {
	a.tuningLab.mu.RLock()
	defer a.tuningLab.mu.RUnlock()
	return TuningLabView{
		Capability: a.tuningLab.Capability, Running: a.tuningLab.Running, Estimating: a.tuningLab.Estimating, Applied: a.tuningLab.Applied, Recovery: a.tuningLab.Recovery,
		Stage: a.tuningLab.Stage, Status: a.tuningLab.Status, Progress: a.tuningLab.Progress,
		CoreMHz: a.tuningLab.CoreMHz, MemoryMHz: a.tuningLab.MemoryMHz, PowerLimitW: a.tuningLab.PowerLimitW, Temperature: a.tuningLab.Temperature,
		Trials: append([]TuningTrial(nil), a.tuningLab.Trials...),
	}
}

func (a *App) tuningSnapshotPath() string {
	return filepath.Join(a.remediationDir(), "active-hardware-tuning.json")
}

func (a *App) persistHardwareTuningSnapshot(snapshot hardwareTuningSnapshot) error {
	if err := os.MkdirAll(a.remediationDir(), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(a.remediationDir(), "hardware-tuning-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err = tmp.Write(data); err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return replaceFile(name, a.tuningSnapshotPath())
}

func (a *App) restoreInterruptedHardwareTuning() {
	data, err := os.ReadFile(a.tuningSnapshotPath())
	if os.IsNotExist(err) {
		return
	}
	if err != nil {
		a.logger.Error("tuning-lab", "read recovery snapshot", err)
		return
	}
	var snapshot hardwareTuningSnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		a.logger.Error("tuning-lab", "decode recovery snapshot", err)
		return
	}
	if snapshot.Version == 1 {
		// Version 1 always represented both NVIDIA offset domains.
		snapshot.CoreSupported, snapshot.MemorySupported = true, true
	}
	a.tuningLab.mu.Lock()
	a.tuningLab.original = snapshot
	a.tuningLab.Recovery = true
	a.tuningLab.Status = "A previous hardware-tuning transaction needs an elevated rollback check."
	a.tuningLab.mu.Unlock()
	if !processIsElevated() {
		return
	}
	if snapshot.Provider == "AMD ADLX" {
		if !snapshot.AMDFactoryOriginal {
			a.logger.Error("tuning-lab", "restore interrupted AMD tuning", fmt.Errorf("snapshot did not begin at AMD factory state"))
			return
		}
		if err := a.surgeStack.adlx.RestoreFactory(); err != nil {
			a.logger.Error("tuning-lab", "restore interrupted AMD clocks", err)
			return
		}
		_ = os.Remove(a.tuningSnapshotPath())
		a.tuningLab.mu.Lock()
		a.tuningLab.Recovery = false
		a.tuningLab.Status = "The interrupted AMD clock transaction was restored to its captured factory state."
		a.tuningLab.mu.Unlock()
		return
	}
	if snapshot.CoreSupported || snapshot.MemorySupported || snapshot.PowerSupported {
		core, memory, power := snapshotControlPointers(snapshot, snapshot.CoreOffset, snapshot.MemoryOffset, snapshot.PowerLimitMilliwatts)
		if err := a.surgeStack.nvml.SetControls(core, memory, power); err != nil {
			a.logger.Error("tuning-lab", "restore interrupted NVIDIA offsets", err)
			return
		}
	}
	_ = os.Remove(a.tuningSnapshotPath())
	a.tuningLab.mu.Lock()
	a.tuningLab.Recovery = false
	a.tuningLab.Status = "The interrupted GPU clock transaction was restored before monitoring started."
	a.tuningLab.mu.Unlock()
}

func tuningFrameScore(stats FrameStats) float64 {
	if !stats.Available || stats.OnePercentLow <= 0 {
		return -1e9
	}
	return stats.OnePercentLow*2 - stats.P99FrameMs*0.8 - stats.HitchesPerMinute*0.35 - stats.P95DisplayMs*0.15
}

func gpuTuningRouteEligible(route SurgeRoute) bool {
	return route.Key == "gpu-power" || route.Key == "gpu-throughput"
}

func (a *App) directTuningRouteTelemetry(provider string) SurgeGPUTelemetry {
	if provider == "AMD ADLX" {
		telemetry := a.surgeStack.adlx.Snapshot()
		return SurgeGPUTelemetry{
			Ready:        telemetry.Ready,
			GPUUtil:      telemetry.Usage,
			TemperatureC: math.Max(telemetry.TemperatureC, telemetry.HotspotC),
			PowerW:       telemetry.PowerW,
		}
	}
	return a.surgeStack.nvml.Snapshot()
}

func evaluateTuningConfirmation(stockWindows, tunedWindows []FrameStats) (bool, FrameStats, FrameStats, string) {
	if len(stockWindows) < tuningConfirmationPairs || len(tunedWindows) != len(stockWindows) {
		return false, FrameStats{}, FrameStats{}, "two complete stock/tuned pairs were not captured"
	}
	stockMean, tunedMean := averageFrameStats(stockWindows), averageFrameStats(tunedWindows)
	passed := 0
	for index := range stockWindows {
		stock, tuned := stockWindows[index], tunedWindows[index]
		if !stock.Available || !tuned.Available || stock.Samples < 120 || tuned.Samples < 120 {
			return false, stockMean, tunedMean, fmt.Sprintf("confirmation pair %d did not contain enough presented frames", index+1)
		}
		if comparable, reason := comparableFrameWorkload(stock, tuned); !comparable {
			return false, stockMean, tunedMean, fmt.Sprintf("confirmation pair %d was not workload-comparable: %s", index+1, reason)
		}
		if tuningFrameRegression(stock, tuned) {
			return false, stockMean, tunedMean, fmt.Sprintf("confirmation pair %d regressed frame delivery", index+1)
		}
		if frameBenefitThresholdCrossed(stock, tuned) {
			passed++
		}
	}
	if passed != len(stockWindows) {
		return false, stockMean, tunedMean, fmt.Sprintf("only %d of %d alternating pairs crossed a benefit threshold", passed, len(stockWindows))
	}
	if !frameBenefitThresholdCrossed(stockMean, tunedMean) || tuningFrameRegression(stockMean, tunedMean) {
		return false, stockMean, tunedMean, "the aggregate stock/tuned result did not preserve a material benefit"
	}
	return true, stockMean, tunedMean, fmt.Sprintf("%d of %d alternating pairs crossed a benefit threshold", passed, len(stockWindows))
}

func tuningFrameRegression(baseline, after FrameStats) bool {
	if !after.Available || after.Samples < 120 || after.OnePercentLow <= 0 {
		return true
	}
	low := after.OnePercentLow < baseline.OnePercentLow*0.90
	tail := baseline.P99FrameMs > 0 && after.P99FrameMs > baseline.P99FrameMs*1.12
	delay := baseline.P95DisplayMs > 0 && after.P95DisplayMs > baseline.P95DisplayMs*1.15
	return low || tail || delay || after.DroppedPercent > baseline.DroppedPercent+2
}

func tuningFaultSince(at time.Time) string {
	rows, err := queryWindowsEventLog("System")
	if err != nil {
		return ""
	}
	for _, row := range rows {
		when, _ := time.Parse(time.RFC3339Nano, row.Time)
		if when.Before(at.UTC()) {
			continue
		}
		source := strings.ToLower(row.Provider)
		if strings.Contains(source, "whea") || (strings.Contains(source, "display") && row.ID == 4101) || (strings.Contains(source, "kernel-power") && row.ID == 41) {
			return fmt.Sprintf("%s Event %d appeared during the trial", row.Provider, row.ID)
		}
	}
	return ""
}

func (a *App) setTuningProgress(stage, status string, progress float64, core, memory int32, powerLimitMilliwatts uint32, temperature float64) {
	a.tuningLab.mu.Lock()
	a.tuningLab.Stage, a.tuningLab.Status, a.tuningLab.Progress = stage, status, progress
	a.tuningLab.CoreMHz, a.tuningLab.MemoryMHz, a.tuningLab.PowerLimitW, a.tuningLab.Temperature = core, memory, float64(powerLimitMilliwatts)/1000, temperature
	a.tuningLab.mu.Unlock()
	if a.hwnd != 0 {
		procPostMessageW.Call(a.hwnd, WM_APP_RENDER, 0, 0)
	}
}

func tuningFrameComparison(baseline, after FrameStats) string {
	return fmt.Sprintf("average %.1f→%.1f FPS; 1%% low %.1f→%.1f FPS; p99 %.1f→%.1f ms; hitches %.1f→%.1f/min", baseline.FPS, after.FPS, baseline.OnePercentLow, after.OnePercentLow, baseline.P99FrameMs, after.P99FrameMs, baseline.HitchesPerMinute, after.HitchesPerMinute)
}

func (a *App) appendTuningTrial(baseline FrameStats, trial TuningTrial, stats FrameStats, fault string) {
	trial.FPS, trial.OnePercentLow = stats.FPS, stats.OnePercentLow
	trial.P99FrameMs, trial.Hitches = stats.P99FrameMs, stats.HitchesPerMinute
	trial.Comparable, trial.Comparison = comparableFrameWorkload(baseline, stats)
	if fault != "" {
		trial.Comparable = false
		trial.Comparison = fault
	}
	a.tuningLab.mu.Lock()
	a.tuningLab.Trials = append(a.tuningLab.Trials, trial)
	a.tuningLab.mu.Unlock()
	comparison := tuningFrameComparison(baseline, stats)
	if trial.Comparison != "" {
		comparison += "; " + trial.Comparison
	}
	status := "measured"
	if trial.Result == "rejected" {
		status = "rejected"
	}
	action := tuningControlSummary(trial.CoreMHz, trial.MemoryMHz, uint32(math.Round(trial.PowerLimitW*1000)))
	a.recordAutopilot("hardware-tuning-trial", status, trial.Domain+" · "+trial.Result, comparison+fmt.Sprintf("; peak %.0f°C", trial.TemperatureC), "Stock hardware frame window", action, "Original controls remain available in the rollback snapshot")
}

func (a *App) applyConfirmationState(snapshot hardwareTuningSnapshot, core, memory int32, power uint32, stage string) error {
	return a.applyTuningControls(snapshot, core, memory, power, stage+" · GPU")
}

func (a *App) confirmationWindow(stop <-chan struct{}, game ProcessMetric, duration, settle time.Duration, temperatureLimit float64) (FrameStats, float64, string) {
	peak := 0.0
	if settle > 0 {
		_, settlePeak, problem := a.waitTuningWindow(stop, game, settle, temperatureLimit)
		peak = math.Max(peak, settlePeak)
		if problem != "" {
			return FrameStats{}, peak, problem
		}
	}
	stats, measuredPeak, problem := a.waitTuningWindow(stop, game, duration, temperatureLimit)
	return stats, math.Max(peak, measuredPeak), problem
}

// confirmDirectTuningCandidate uses an A-B-B-A reversal sequence. Measuring
// stock on both sides of the tuned windows reduces warm-up, thermal and scene
// drift; both independently paired comparisons must pass before retention.
func (a *App) confirmDirectTuningCandidate(stop <-chan struct{}, game ProcessMetric, snapshot hardwareTuningSnapshot, core, memory int32, power uint32, temperatureLimit float64) (bool, FrameStats, FrameStats, float64, string) {
	stockWindows := make([]FrameStats, 0, tuningConfirmationPairs)
	tunedWindows := make([]FrameStats, 0, tuningConfirmationPairs)
	peak := 0.0
	measure := func(label string, progress float64) (FrameStats, bool, string) {
		a.setTuningProgress("REVERSAL CONFIRMATION", label, progress, core, memory, power, peak)
		stats, trialPeak, problem := a.confirmationWindow(stop, game, tuningConfirmationWindow, tuningConfirmationSettle, temperatureLimit)
		peak = math.Max(peak, trialPeak)
		if problem != "" || !stats.Available || stats.Samples < 120 {
			return stats, false, fallback(problem, "PresentMon did not provide a complete confirmation window")
		}
		return stats, true, ""
	}

	if err := a.applyConfirmationState(snapshot, snapshot.CoreOffset, snapshot.MemoryOffset, snapshot.PowerLimitMilliwatts, "Confirmation A1 · exact stock"); err != nil {
		return false, FrameStats{}, FrameStats{}, peak, err.Error()
	}
	stock1, ok, problem := measure("Stock A1/2 · measuring the exact captured controls.", 0.88)
	if !ok {
		return false, FrameStats{}, FrameStats{}, peak, problem
	}
	stockWindows = append(stockWindows, stock1)

	if err := a.applyConfirmationState(snapshot, core, memory, power, "Confirmation B1 · candidate"); err != nil {
		return false, FrameStats{}, FrameStats{}, peak, err.Error()
	}
	tuned1, ok, problem := measure("Tuned B1/2 · measuring the selected candidate.", 0.91)
	if !ok {
		return false, FrameStats{}, FrameStats{}, peak, problem
	}
	tunedWindows = append(tunedWindows, tuned1)

	tuned2, ok, problem := measure("Tuned B2/2 · repeating the candidate before reversal.", 0.94)
	if !ok {
		return false, FrameStats{}, FrameStats{}, peak, problem
	}
	tunedWindows = append(tunedWindows, tuned2)

	if err := a.applyConfirmationState(snapshot, snapshot.CoreOffset, snapshot.MemoryOffset, snapshot.PowerLimitMilliwatts, "Confirmation A2 · exact stock"); err != nil {
		return false, FrameStats{}, FrameStats{}, peak, err.Error()
	}
	stock2, ok, problem := measure("Stock A2/2 · checking the result against a second stock window.", 0.97)
	if !ok {
		return false, FrameStats{}, FrameStats{}, peak, problem
	}
	stockWindows = append(stockWindows, stock2)

	for index := range stockWindows {
		comparable, comparison := comparableFrameWorkload(stockWindows[index], tunedWindows[index])
		status := "observed"
		if comparable && !tuningFrameRegression(stockWindows[index], tunedWindows[index]) && frameBenefitThresholdCrossed(stockWindows[index], tunedWindows[index]) {
			status = "passed"
		}
		a.recordAutopilot("hardware-tuning-confirmation", status, fmt.Sprintf("Alternating stock/tuned pair %d", index+1), tuningFrameComparison(stockWindows[index], tunedWindows[index])+"; "+comparison, "Exact captured stock controls", "Reapply and remeasure the selected candidate", "The sequence ends at exact stock")
	}
	proved, stockMean, tunedMean, reason := evaluateTuningConfirmation(stockWindows, tunedWindows)
	return proved, stockMean, tunedMean, peak, reason
}

func (a *App) waitTuningWindow(stop <-chan struct{}, game ProcessMetric, duration time.Duration, temperatureLimit float64) (FrameStats, float64, string) {
	start := time.Now()
	peak := 0.0
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for time.Since(start) < duration {
		select {
		case <-stop:
			return FrameStats{}, peak, "Tuning stopped"
		case <-ticker.C:
			current, ok := a.lockedGameProcess()
			if !ok || current.PID != game.PID {
				return FrameStats{}, peak, "The locked game exited or restarted"
			}
			if foregroundProcessID() != game.PID {
				return FrameStats{}, peak, "The game left the foreground"
			}
			a.tuningLab.mu.RLock()
			amd := a.tuningLab.original.Provider == "AMD ADLX"
			a.tuningLab.mu.RUnlock()
			if amd {
				telemetry := a.surgeStack.adlx.Snapshot()
				temperature := math.Max(telemetry.TemperatureC, telemetry.HotspotC)
				if temperature > peak {
					peak = temperature
				}
				if peak >= temperatureLimit {
					return FrameStats{}, peak, fmt.Sprintf("GPU reached the %.0f°C Tuning Lab ceiling", temperatureLimit)
				}
				continue
			}
			telemetry := a.surgeStack.nvml.Snapshot()
			if telemetry.TemperatureC > peak {
				peak = telemetry.TemperatureC
			}
			if peak >= temperatureLimit {
				return FrameStats{}, peak, fmt.Sprintf("GPU reached the %.0f°C Tuning Lab ceiling", temperatureLimit)
			}
			if telemetry.ThrottleReasons&(nvmlReasonHWSlowdown|nvmlReasonHWthermal|nvmlReasonPowerBrake) != 0 {
				return FrameStats{}, peak, "The NVIDIA hardware safeguard or thermal limiter activated"
			}
		}
	}
	if fault := tuningFaultSince(start); fault != "" {
		return FrameStats{}, peak, fault
	}
	return a.frames.SnapshotWindow(start, time.Now()), peak, ""
}

func snapshotControlPointers(snapshot hardwareTuningSnapshot, coreValue, memoryValue int32, powerValue uint32) (core, memory *int32, power *uint32) {
	if snapshot.CoreSupported {
		core = &coreValue
	}
	if snapshot.MemorySupported {
		memory = &memoryValue
	}
	if snapshot.PowerSupported {
		power = &powerValue
	}
	return core, memory, power
}

func tuningControlSummary(core, memory int32, power uint32) string {
	parts := make([]string, 0, 3)
	parts = append(parts, fmt.Sprintf("graphics %+d MHz", core), fmt.Sprintf("VRAM %+d MHz", memory))
	if power > 0 {
		parts = append(parts, fmt.Sprintf("power %.0f W", float64(power)/1000))
	}
	return strings.Join(parts, " · ")
}

type hardwareControlValidationTarget struct {
	CoreMHz, MemoryMHz int32
	PowerMilliwatts    uint32
	Changed            bool
}

// boundedHardwareControlValidationTarget chooses a deliberately small control
// transaction. It proves that the installed driver accepts clock and power
// writes; it is not a stability tune or evidence of a performance benefit.
func boundedHardwareControlValidationTarget(capability NVMLClockCapability) hardwareControlValidationTarget {
	target := hardwareControlValidationTarget{
		CoreMHz: capability.Core.Current, MemoryMHz: capability.Memory.Current,
		PowerMilliwatts: capability.Power.Current,
	}
	if capability.Core.Supported && capability.Core.Current < capability.Core.Max {
		target.CoreMHz = min(capability.Core.Current+25, capability.Core.Max)
	}
	if capability.Memory.Supported && capability.Memory.Current < capability.Memory.Max {
		target.MemoryMHz = min(capability.Memory.Current+100, capability.Memory.Max)
	}
	if capability.Power.Supported && capability.Power.Current < capability.Power.Max {
		target.PowerMilliwatts = min(capability.Power.Current+1000, capability.Power.Max)
	}
	target.Changed = target.CoreMHz != capability.Core.Current || target.MemoryMHz != capability.Memory.Current || target.PowerMilliwatts != capability.Power.Current
	return target
}

func verifyHardwareControlReadback(capability NVMLClockCapability, core, memory int32, power uint32) error {
	problems := make([]string, 0, 3)
	if capability.Core.Supported && capability.Core.Current != core {
		problems = append(problems, fmt.Sprintf("graphics read-back %+d MHz, requested %+d MHz", capability.Core.Current, core))
	}
	if capability.Memory.Supported && capability.Memory.Current != memory {
		problems = append(problems, fmt.Sprintf("VRAM read-back %+d MHz, requested %+d MHz", capability.Memory.Current, memory))
	}
	if capability.Power.Supported && capability.Power.Current != power {
		problems = append(problems, fmt.Sprintf("power read-back %.0f W, requested %.0f W", float64(capability.Power.Current)/1000, float64(power)/1000))
	}
	if len(problems) > 0 {
		return fmt.Errorf("%s", strings.Join(problems, "; "))
	}
	return nil
}

// startHardwareControlValidation is an explicit diagnostic separate from the
// measured tuning route. It may bypass the workload-relevance gate only; exact
// journaling, manufacturer ranges, thermal/fault checks and rollback remain
// mandatory. No tested value is retained.
func (a *App) startHardwareControlValidation() {
	if a.config.Tuning.LabDisclaimerVersion != tuningLabDisclaimerVersion {
		a.setTuningProgress("ACKNOWLEDGEMENT REQUIRED", "Accept the hardware-risk acknowledgement before testing driver controls.", 0, 0, 0, 0, 0)
		return
	}
	if !processIsElevated() {
		a.setTuningProgress("ADMINISTRATOR REQUIRED", "Restart elevated before testing GPU clock controls.", 0, 0, 0, 0, 0)
		return
	}
	if a.surgeIsEnabled() {
		a.setTuningProgress("PAUSE SURGE FIRST", "The control test requires an unmodified session so it can capture and restore exact starting values.", 0, 0, 0, 0, 0)
		return
	}
	a.tuningLab.mu.Lock()
	if a.tuningLab.Running || a.tuningLab.Applied || a.tuningLab.Recovery {
		a.tuningLab.mu.Unlock()
		return
	}
	a.tuningLab.Running = true
	a.tuningLab.Stage = "PREPARING CONTROL TEST"
	a.tuningLab.Status = "Reading exact NVIDIA clock and power controls before a small temporary transaction."
	a.tuningLab.Progress = 0.03
	stop, done := make(chan struct{}), make(chan struct{})
	a.tuningLab.stopOnce = sync.Once{}
	a.tuningLab.stop, a.tuningLab.done = stop, done
	a.tuningLab.mu.Unlock()
	go a.hardwareControlValidationLoop(stop, done)
}

func (a *App) hardwareControlValidationLoop(stop <-chan struct{}, done chan<- struct{}) {
	defer close(done)
	defer func() {
		a.tuningLab.mu.Lock()
		a.tuningLab.Running = false
		a.tuningLab.mu.Unlock()
		if a.hwnd != 0 {
			procPostMessageW.Call(a.hwnd, WM_APP_RENDER, 0, 0)
		}
	}()

	capability := a.surgeStack.nvml.ClockCapability()
	if !capability.Ready || (!capability.Core.Supported && !capability.Memory.Supported) {
		a.setTuningProgress("CONTROL TEST UNAVAILABLE", "This NVIDIA driver did not expose a writable core or VRAM offset. Nothing changed.", 0, 0, 0, 0, 0)
		return
	}
	target := boundedHardwareControlValidationTarget(capability)
	if !target.Changed {
		a.setTuningProgress("NO TEST HEADROOM", "Every exposed control is already at its driver-reported maximum. Nothing changed.", 0, capability.Core.Current, capability.Memory.Current, capability.Power.Current, 0)
		return
	}
	snapshot := hardwareTuningSnapshot{
		Version: 3, Provider: "NVIDIA NVML", Device: capability.Device,
		CoreSupported: capability.Core.Supported, MemorySupported: capability.Memory.Supported, PowerSupported: capability.Power.Supported,
		CoreOffset: capability.Core.Current, MemoryOffset: capability.Memory.Current, PowerLimitMilliwatts: capability.Power.Current,
		CapturedAt: time.Now(),
	}
	telemetry := a.surgeStack.nvml.Snapshot()
	if !telemetry.Ready || telemetry.TemperatureC <= 0 || telemetry.TemperatureC >= 80 {
		a.setTuningProgress("CONTROL TEST REFUSED", "Valid GPU temperature below 80°C is required before a forced control check. Nothing changed.", 0, snapshot.CoreOffset, snapshot.MemoryOffset, snapshot.PowerLimitMilliwatts, telemetry.TemperatureC)
		return
	}
	if err := a.persistHardwareTuningSnapshot(snapshot); err != nil {
		a.setTuningProgress("CONTROL TEST REFUSED", "The exact rollback snapshot could not be secured: "+err.Error(), 0, snapshot.CoreOffset, snapshot.MemoryOffset, snapshot.PowerLimitMilliwatts, telemetry.TemperatureC)
		return
	}
	a.tuningLab.mu.Lock()
	a.tuningLab.original, a.tuningLab.Recovery = snapshot, true
	a.tuningLab.mu.Unlock()

	testStarted := time.Now()
	a.setTuningProgress("APPLYING BOUNDED TEST", "Temporarily requesting "+tuningControlSummary(target.CoreMHz, target.MemoryMHz, target.PowerMilliwatts)+". The exact starting values are already journaled.", 0.24, snapshot.CoreOffset, snapshot.MemoryOffset, snapshot.PowerLimitMilliwatts, telemetry.TemperatureC)
	if err := a.applyTuningControls(snapshot, target.CoreMHz, target.MemoryMHz, target.PowerMilliwatts, "Forced driver-control validation"); err != nil {
		_ = a.restoreHardwareTuning("The forced driver-control request failed")
		a.setTuningProgress("CONTROL TEST FAILED · STOCK RESTORED", err.Error(), 1, snapshot.CoreOffset, snapshot.MemoryOffset, snapshot.PowerLimitMilliwatts, telemetry.TemperatureC)
		return
	}
	readback := a.surgeStack.nvml.ClockCapability()
	if err := verifyHardwareControlReadback(readback, target.CoreMHz, target.MemoryMHz, target.PowerMilliwatts); err != nil {
		_ = a.restoreHardwareTuning("Driver read-back did not match the forced control request")
		a.setTuningProgress("CONTROL TEST FAILED · STOCK RESTORED", "The driver call returned but its read-back did not match: "+err.Error(), 1, snapshot.CoreOffset, snapshot.MemoryOffset, snapshot.PowerLimitMilliwatts, telemetry.TemperatureC)
		return
	}

	peak := telemetry.TemperatureC
	a.setTuningProgress("VERIFYING LIVE CONTROL", "Driver read-back matches the requested core, VRAM and power values. Watching temperature and hardware faults for 6 seconds before rollback.", 0.50, target.CoreMHz, target.MemoryMHz, target.PowerMilliwatts, peak)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(6 * time.Second)
	defer deadline.Stop()
	problem := ""
watch:
	for {
		select {
		case <-stop:
			problem = "Stopped by the user"
			break watch
		case <-ticker.C:
			live := a.surgeStack.nvml.Snapshot()
			if !live.Ready {
				problem = "NVIDIA telemetry disappeared during the control test"
				break watch
			}
			peak = math.Max(peak, live.TemperatureC)
			if live.TemperatureC >= 84 {
				problem = fmt.Sprintf("GPU temperature reached %.0f°C", live.TemperatureC)
				break watch
			}
			a.setTuningProgress("VERIFYING LIVE CONTROL", "Driver read-back matches. Watching temperature and hardware faults before automatic rollback.", 0.50+0.30*math.Min(time.Since(testStarted).Seconds()/6, 1), target.CoreMHz, target.MemoryMHz, target.PowerMilliwatts, peak)
		case <-deadline.C:
			break watch
		}
	}
	if problem == "" {
		problem = tuningFaultSince(testStarted)
	}
	if err := a.restoreHardwareTuning("Forced driver-control validation completed"); err != nil {
		a.setTuningProgress("ROLLBACK NEEDS ATTENTION", "The control test ended but exact rollback failed: "+err.Error(), 1, target.CoreMHz, target.MemoryMHz, target.PowerMilliwatts, peak)
		return
	}
	restored := a.surgeStack.nvml.ClockCapability()
	if err := verifyHardwareControlReadback(restored, snapshot.CoreOffset, snapshot.MemoryOffset, snapshot.PowerLimitMilliwatts); err != nil {
		a.setTuningProgress("ROLLBACK READ-BACK FAILED", "Rollback returned an unexpected driver read-back: "+err.Error(), 1, snapshot.CoreOffset, snapshot.MemoryOffset, snapshot.PowerLimitMilliwatts, peak)
		return
	}
	if problem != "" {
		a.recordAutopilot("hardware-control-validation", "rejected", problem, "The driver accepted and re-reported the bounded request", tuningControlSummary(snapshot.CoreOffset, snapshot.MemoryOffset, snapshot.PowerLimitMilliwatts), "Temporary "+tuningControlSummary(target.CoreMHz, target.MemoryMHz, target.PowerMilliwatts), "Exact captured controls restored and verified")
		a.setTuningProgress("CONTROL TEST REJECTED · STOCK RESTORED", problem+". The original controls were restored and verified.", 1, snapshot.CoreOffset, snapshot.MemoryOffset, snapshot.PowerLimitMilliwatts, peak)
		return
	}
	a.recordAutopilot("hardware-control-validation", "passed", "NVIDIA clock-control path verified", "Requested values matched driver read-back; no WHEA, display-reset or Kernel-Power fault appeared during the bounded hold", tuningControlSummary(snapshot.CoreOffset, snapshot.MemoryOffset, snapshot.PowerLimitMilliwatts), "Temporary "+tuningControlSummary(target.CoreMHz, target.MemoryMHz, target.PowerMilliwatts), "Exact captured controls restored and verified")
	a.setTuningProgress("CONTROL TEST PASSED · STOCK RESTORED", "NVIDIA accepted and re-reported the temporary core, VRAM and power controls. No watched fault appeared; exact starting values were restored and read back. This proves control works, not that these offsets improve FPS.", 1, snapshot.CoreOffset, snapshot.MemoryOffset, snapshot.PowerLimitMilliwatts, peak)
}

func (a *App) applyTuningControls(snapshot hardwareTuningSnapshot, core, memory int32, power uint32, stage string) error {
	if !snapshot.CoreSupported && !snapshot.MemorySupported && !snapshot.PowerSupported {
		return nil
	}
	if !a.recordAutopilot("hardware-tuning-control", "prepared", stage, tuningControlSummary(core, memory, power), tuningControlSummary(snapshot.CoreOffset, snapshot.MemoryOffset, snapshot.PowerLimitMilliwatts), "Apply only driver-validated controls for one measured trial", "Restore the exact captured controls") {
		return fmt.Errorf("the write-ahead journal refused the hardware transaction")
	}
	coreControl, memoryControl, powerControl := snapshotControlPointers(snapshot, core, memory, power)
	var applyErr error
	if snapshot.Provider == "AMD ADLX" {
		applyErr = a.surgeStack.adlx.SetManualClocks(coreControl, memoryControl)
	} else {
		applyErr = a.surgeStack.nvml.SetControls(coreControl, memoryControl, powerControl)
	}
	if applyErr != nil {
		a.recordAutopilot("hardware-tuning-control", "failed", stage, applyErr.Error(), "Original controls retained or restored", "No untracked tuning change", "Exact snapshot remains on disk")
		return applyErr
	}
	a.tuningLab.mu.Lock()
	a.tuningLab.Applied = core != snapshot.CoreOffset || memory != snapshot.MemoryOffset || power != snapshot.PowerLimitMilliwatts
	a.tuningLab.CoreMHz, a.tuningLab.MemoryMHz = core, memory
	a.tuningLab.PowerLimitW = float64(power) / 1000
	a.tuningLab.mu.Unlock()
	providerLabel := "NVIDIA"
	if snapshot.Provider == "AMD ADLX" {
		providerLabel = "AMD"
	}
	a.recordAutopilot("hardware-tuning-control", "applied", stage, "The "+providerLabel+" driver accepted every requested value inside its reported range", tuningControlSummary(snapshot.CoreOffset, snapshot.MemoryOffset, snapshot.PowerLimitMilliwatts), tuningControlSummary(core, memory, power), "Exact captured controls restore when the session ends or a guard trips")
	return nil
}

func (a *App) restoreHardwareTuning(reason string) error {
	a.tuningLab.mu.RLock()
	snapshot := a.tuningLab.original
	a.tuningLab.mu.RUnlock()
	if snapshot.CapturedAt.IsZero() {
		return nil
	}
	if snapshot.Provider == "AMD ADLX" {
		core, memory, _ := snapshotControlPointers(snapshot, snapshot.CoreOffset, snapshot.MemoryOffset, 0)
		if err := a.surgeStack.adlx.SetManualClocks(core, memory); err != nil {
			if snapshot.AMDFactoryOriginal {
				err = a.surgeStack.adlx.RestoreFactory()
			}
			if err != nil {
				a.recordAutopilot("hardware-tuning-rollback", "failed", reason, err.Error(), "AMD clock profile may remain active", "Recovery snapshot retained", "Retry from an elevated Tuning Lab")
				return err
			}
		}
		_ = os.Remove(a.tuningSnapshotPath())
		a.tuningLab.mu.Lock()
		a.tuningLab.Applied, a.tuningLab.Recovery = false, false
		a.tuningLab.original = hardwareTuningSnapshot{}
		a.tuningLab.mu.Unlock()
		a.recordAutopilot("hardware-tuning-rollback", "restored", reason, snapshot.Device, "AMD session clock tuning active", "Captured AMD clock state restored", "Complete")
		return nil
	}
	if snapshot.CoreSupported || snapshot.MemorySupported || snapshot.PowerSupported {
		core, memory, power := snapshotControlPointers(snapshot, snapshot.CoreOffset, snapshot.MemoryOffset, snapshot.PowerLimitMilliwatts)
		if err := a.surgeStack.nvml.SetControls(core, memory, power); err != nil {
			a.recordAutopilot("hardware-tuning-rollback", "failed", reason, err.Error(), "Tuned offsets may remain until driver reload or restart", "Recovery snapshot retained", "Retry from an elevated Tuning Lab")
			return err
		}
	}
	_ = os.Remove(a.tuningSnapshotPath())
	a.tuningLab.mu.Lock()
	a.tuningLab.Applied, a.tuningLab.Recovery = false, false
	a.tuningLab.CoreMHz, a.tuningLab.MemoryMHz = snapshot.CoreOffset, snapshot.MemoryOffset
	a.tuningLab.PowerLimitW = float64(snapshot.PowerLimitMilliwatts) / 1000
	a.tuningLab.original = hardwareTuningSnapshot{}
	a.tuningLab.mu.Unlock()
	a.recordAutopilot("hardware-tuning-rollback", "restored", reason, snapshot.Device, "Session tuning active", tuningControlSummary(snapshot.CoreOffset, snapshot.MemoryOffset, snapshot.PowerLimitMilliwatts)+" restored", "Complete")
	return nil
}

func (a *App) startHardwareTuning() {
	if a.config.Tuning.LabDisclaimerVersion != tuningLabDisclaimerVersion {
		a.tuningLab.mu.Lock()
		a.tuningLab.Status = "Accept the separate hardware-risk acknowledgement before arming Tuning Lab."
		a.tuningLab.mu.Unlock()
		return
	}
	if !processIsElevated() {
		a.tuningLab.mu.Lock()
		a.tuningLab.Status = "An explicit administrator restart is required for driver clock control."
		a.tuningLab.mu.Unlock()
		return
	}
	if !a.surgeIsEnabled() || a.config.Tuning.Mode != "performance" {
		a.tuningLab.mu.Lock()
		a.tuningLab.Status = "Tuning Lab requires Surge in Aggressive mode. Ordinary Surge remains voltage and clock neutral."
		a.tuningLab.mu.Unlock()
		return
	}
	if antiCheat := detectAntiCheat(a.snapshot.Processes); antiCheat.Detected && a.antiCheatGuardIsEnabled() {
		a.tuningLab.mu.Lock()
		a.tuningLab.Status = antiCheat.Provider + " detected. Hardware discovery is blocked while anti-cheat guardrails are on."
		a.tuningLab.mu.Unlock()
		return
	}
	view := a.tuningLabSnapshot()
	if view.Estimating {
		return
	}
	a.refreshTuningLabCapability()
	capability := a.tuningLabSnapshot().Capability
	if !capability.GPU.Direct {
		a.tuningLab.mu.Lock()
		a.tuningLab.Status = "No direct manufacturer-reported GPU clock range is writable on this PC. Nothing was changed."
		a.tuningLab.mu.Unlock()
		return
	}
	if !capability.Estimate.Ready {
		a.tuningLab.mu.Lock()
		a.tuningLab.Stage = "STOCK ESTIMATE REQUIRED"
		a.tuningLab.Status = "Hardware discovery cannot begin because Surge is active without a completed stock estimate. Pause Surge and estimate the unmodified PC first."
		a.tuningLab.mu.Unlock()
		return
	}
	game, ok := a.lockedGameProcess()
	if !ok {
		a.tuningLab.mu.Lock()
		a.tuningLab.Status = "Start and lock a game before beginning a measured discovery run."
		a.tuningLab.mu.Unlock()
		return
	}
	a.tuningLab.mu.Lock()
	if a.tuningLab.Running {
		a.tuningLab.mu.Unlock()
		return
	}
	stop, done := make(chan struct{}), make(chan struct{})
	a.tuningLab.stopOnce = sync.Once{}
	a.tuningLab.stop, a.tuningLab.done = stop, done
	a.tuningLab.Running, a.tuningLab.Stage = true, "WAITING FOR GAME"
	a.tuningLab.AttemptedPID = game.PID
	a.tuningLab.Status = "Switch to the locked game. Discovery begins only while it owns the foreground."
	a.tuningLab.Progress, a.tuningLab.Trials = 0, nil
	a.tuningLab.mu.Unlock()
	go a.hardwareTuningLoop(game, capability, stop, done)
}

func (a *App) maybeStartHardwareTuning() {
	if !a.config.Tuning.LabAutoWithSurge || !a.surgeIsEnabled() || a.config.Tuning.Mode != "performance" || a.config.Tuning.LabDisclaimerVersion != tuningLabDisclaimerVersion || !processIsElevated() {
		return
	}
	game, ok := a.lockedGameProcess()
	if !ok || foregroundProcessID() != game.PID {
		a.tuningLab.mu.Lock()
		if !a.tuningLab.Running && !a.tuningLab.Applied {
			a.tuningLab.AttemptedPID = 0
		}
		a.tuningLab.mu.Unlock()
		return
	}
	a.tuningLab.mu.RLock()
	busy := a.tuningLab.Running || a.tuningLab.Estimating || a.tuningLab.Applied || a.tuningLab.Recovery || a.tuningLab.AttemptedPID == game.PID
	a.tuningLab.mu.RUnlock()
	if !busy {
		a.startHardwareTuning()
	}
}

func (a *App) hardwareTuningLoop(game ProcessMetric, capability TuningLabCapability, stop <-chan struct{}, done chan<- struct{}) {
	defer close(done)
	defer func() {
		a.tuningLab.mu.Lock()
		a.tuningLab.Running = false
		a.tuningLab.mu.Unlock()
	}()
	deadline := time.Now().Add(2 * time.Minute)
	for foregroundProcessID() != game.PID {
		select {
		case <-stop:
			return
		case <-time.After(500 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			a.setTuningProgress("NOT STARTED", "The game did not become foreground within two minutes. Nothing changed.", 0, 0, 0, 0, 0)
			return
		}
	}
	if capability.AMD.Ready && !capability.GPUOffsets.Ready {
		a.amdHardwareTuningLoop(game, capability, stop)
		return
	}
	coreDomain, memoryDomain, powerDomain := capability.GPUOffsets.Core, capability.GPUOffsets.Memory, capability.GPUOffsets.Power
	providerName := "NVIDIA NVML"
	if capability.AMD.Ready {
		providerName = "AMD ADLX"
	}
	snapshot := hardwareTuningSnapshot{
		Version: 3, Provider: providerName, Device: capability.GPU.Device,
		CoreSupported: coreDomain.Supported, MemorySupported: memoryDomain.Supported, PowerSupported: powerDomain.Supported,
		CoreOffset: coreDomain.Current, MemoryOffset: memoryDomain.Current, PowerLimitMilliwatts: powerDomain.Current,
		AMDFactoryOriginal: capability.AMD.AtFactory, CapturedAt: time.Now(),
	}
	if err := a.persistHardwareTuningSnapshot(snapshot); err != nil {
		a.setTuningProgress("REFUSED", "Rollback snapshot could not be secured: "+err.Error(), 0, snapshot.CoreOffset, snapshot.MemoryOffset, snapshot.PowerLimitMilliwatts, 0)
		return
	}
	a.tuningLab.mu.Lock()
	a.tuningLab.original = snapshot
	a.tuningLab.Recovery = true
	a.tuningLab.mu.Unlock()
	envelope, thermalReady := estimatedTuningEnvelope(a.config.Tuning.LabProfile, capability.Estimate)
	if !thermalReady {
		_ = a.restoreHardwareTuning("Live thermal estimate was unavailable")
		a.setTuningProgress("REFUSED", "A valid live thermal estimate is required before clocks can change. Nothing was tuned.", 0, snapshot.CoreOffset, snapshot.MemoryOffset, snapshot.PowerLimitMilliwatts, 0)
		return
	}
	a.setTuningProgress("BASELINE", "Measuring stock frame delivery and temperature for 20 seconds.", 0.04, snapshot.CoreOffset, snapshot.MemoryOffset, snapshot.PowerLimitMilliwatts, 0)
	baseline, peak, problem := a.waitTuningWindow(stop, game, 20*time.Second, envelope.TemperatureLimit)
	if problem != "" || !baseline.Available || baseline.Samples < 120 {
		_ = a.restoreHardwareTuning("Baseline could not be validated")
		a.setTuningProgress("STOPPED", fallback(problem, "PresentMon did not provide a sufficient baseline. Nothing was tuned."), 0, snapshot.CoreOffset, snapshot.MemoryOffset, snapshot.PowerLimitMilliwatts, peak)
		return
	}
	bestCore, bestMemory, bestStats := snapshot.CoreOffset, snapshot.MemoryOffset, baseline
	bestPower := snapshot.PowerLimitMilliwatts
	bestScore := tuningFrameScore(baseline)
	directGPUAvailable := coreDomain.Supported || memoryDomain.Supported || powerDomain.Supported
	if directGPUAvailable {
		route := determineSurgeRoute(baseline, a.directTuningRouteTelemetry(snapshot.Provider), 0, 0, 0)
		if !gpuTuningRouteEligible(route) {
			a.recordAutopilot("hardware-tuning-route", "observed", "GPU clock discovery was skipped because the GPU was not the measured frame ceiling", route.Evidence, "Exact stock hardware controls", "Keep monitoring for a genuinely GPU-limited window", "No GPU tuning transaction was started")
			_ = a.restoreHardwareTuning("GPU tuning was not relevant to the measured frame route")
			a.setTuningProgress("NOT APPLICABLE", "The GPU is finishing before the frame deadline, so extra GPU clock and power would add heat without addressing this game's current limit. Stock controls remain active.", 1, snapshot.CoreOffset, snapshot.MemoryOffset, snapshot.PowerLimitMilliwatts, peak)
			return
		}
	}
	estimatedCore, estimatedMemory, estimatedPowerW := estimatedTuningDeltas(capability, envelope)
	coreTarget := snapshot.CoreOffset
	if coreDomain.Supported {
		coreTarget = snapshot.CoreOffset + estimatedCore
	}
	memoryTarget := snapshot.MemoryOffset
	if memoryDomain.Supported {
		memoryTarget = snapshot.MemoryOffset + estimatedMemory
	}
	powerTarget := snapshot.PowerLimitMilliwatts
	if powerDomain.Supported {
		powerTarget = snapshot.PowerLimitMilliwatts + estimatedPowerW*1000
	}
	coreSteps := tuningSteps(snapshot.CoreOffset, coreTarget, envelope.CoreStep)
	memorySteps := tuningSteps(snapshot.MemoryOffset, memoryTarget, envelope.MemoryStep)
	powerSteps := tuningPowerSteps(snapshot.PowerLimitMilliwatts, powerTarget, envelope.PowerStepW)
	total := len(powerSteps) + len(coreSteps) + len(memorySteps)
	if total == 0 {
		_ = a.restoreHardwareTuning("The driver exposed no positive offset headroom")
		a.setTuningProgress("NOT AVAILABLE", "The driver reported no positive clock or power headroom inside this profile. Nothing changed.", 0, snapshot.CoreOffset, snapshot.MemoryOffset, snapshot.PowerLimitMilliwatts, peak)
		return
	}
	completed := 0
	for _, value := range powerSteps {
		stage := fmt.Sprintf("Power discovery %.0f W", float64(value)/1000)
		if err := a.applyTuningControls(snapshot, bestCore, bestMemory, value, stage); err != nil {
			break
		}
		stats, trialPeak, fault := a.waitTuningWindow(stop, game, 18*time.Second, envelope.TemperatureLimit)
		completed++
		a.setTuningProgress("POWER DISCOVERY", fallback(fault, "Testing whether extra driver-approved power headroom actually improves frame delivery."), 0.08+0.78*float64(completed)/float64(total), bestCore, bestMemory, value, trialPeak)
		result := "stable"
		if fault != "" || tuningFrameRegression(baseline, stats) {
			result = "rejected"
			_ = a.applyTuningControls(snapshot, bestCore, bestMemory, bestPower, "Reject unstable or regressive power step")
			a.appendTuningTrial(baseline, TuningTrial{At: time.Now(), Domain: "GPU power", Result: result, CoreMHz: bestCore, MemoryMHz: bestMemory, PowerLimitW: float64(value) / 1000, TemperatureC: trialPeak}, stats, fault)
			break
		}
		if comparable, _ := comparableFrameWorkload(baseline, stats); comparable && tuningFrameScore(stats) > bestScore {
			bestPower, bestStats, bestScore = value, stats, tuningFrameScore(stats)
			result = "best so far"
		}
		a.appendTuningTrial(baseline, TuningTrial{At: time.Now(), Domain: "GPU power", Result: result, CoreMHz: bestCore, MemoryMHz: bestMemory, PowerLimitW: float64(value) / 1000, TemperatureC: trialPeak}, stats, fault)
	}
	_ = a.applyTuningControls(snapshot, bestCore, bestMemory, bestPower, "Return to the best measured power step")
	for _, value := range coreSteps {
		stage := fmt.Sprintf("Core discovery %+d MHz", value)
		if err := a.applyTuningControls(snapshot, value, bestMemory, bestPower, stage); err != nil {
			break
		}
		stats, trialPeak, fault := a.waitTuningWindow(stop, game, 18*time.Second, envelope.TemperatureLimit)
		completed++
		a.setTuningProgress("CORE DISCOVERY", fallback(fault, "Comparing the clock step with the stock workload."), 0.08+0.78*float64(completed)/float64(total), value, bestMemory, bestPower, trialPeak)
		result := "stable"
		if fault != "" || tuningFrameRegression(baseline, stats) {
			result = "rejected"
			_ = a.applyTuningControls(snapshot, bestCore, bestMemory, bestPower, "Reject unstable or regressive core step")
			a.appendTuningTrial(baseline, TuningTrial{At: time.Now(), Domain: "GPU core", Result: result, CoreMHz: value, MemoryMHz: bestMemory, PowerLimitW: float64(bestPower) / 1000, TemperatureC: trialPeak}, stats, fault)
			break
		}
		if comparable, _ := comparableFrameWorkload(baseline, stats); comparable && tuningFrameScore(stats) > bestScore {
			bestCore, bestStats, bestScore = value, stats, tuningFrameScore(stats)
			result = "best so far"
		}
		a.appendTuningTrial(baseline, TuningTrial{At: time.Now(), Domain: "GPU core", Result: result, CoreMHz: value, MemoryMHz: bestMemory, PowerLimitW: float64(bestPower) / 1000, TemperatureC: trialPeak}, stats, fault)
	}
	_ = a.applyTuningControls(snapshot, bestCore, bestMemory, bestPower, "Return to the best measured core step")
	for _, value := range memorySteps {
		stage := fmt.Sprintf("VRAM discovery %+d MHz", value)
		if err := a.applyTuningControls(snapshot, bestCore, value, bestPower, stage); err != nil {
			break
		}
		stats, trialPeak, fault := a.waitTuningWindow(stop, game, 18*time.Second, envelope.TemperatureLimit)
		completed++
		a.setTuningProgress("VRAM DISCOVERY", fallback(fault, "Checking frame delivery, temperature and Windows hardware events."), 0.08+0.78*float64(completed)/float64(total), bestCore, value, bestPower, trialPeak)
		result := "stable"
		if fault != "" || tuningFrameRegression(baseline, stats) {
			result = "rejected"
			_ = a.applyTuningControls(snapshot, bestCore, bestMemory, bestPower, "Reject unstable or regressive VRAM step")
			a.appendTuningTrial(baseline, TuningTrial{At: time.Now(), Domain: "GPU VRAM", Result: result, CoreMHz: bestCore, MemoryMHz: value, PowerLimitW: float64(bestPower) / 1000, TemperatureC: trialPeak}, stats, fault)
			break
		}
		if comparable, _ := comparableFrameWorkload(baseline, stats); comparable && tuningFrameScore(stats) > bestScore {
			bestMemory, bestStats, bestScore = value, stats, tuningFrameScore(stats)
			result = "best so far"
		}
		a.appendTuningTrial(baseline, TuningTrial{At: time.Now(), Domain: "GPU VRAM", Result: result, CoreMHz: bestCore, MemoryMHz: value, PowerLimitW: float64(bestPower) / 1000, TemperatureC: trialPeak}, stats, fault)
	}
	if !frameBenefitThresholdCrossed(baseline, bestStats) {
		_, comparison := comparableFrameWorkload(baseline, bestStats)
		a.recordAutopilot("hardware-tuning-proof", "observed", "Hardware tuning was withdrawn because it did not produce a material frame-delivery gain", tuningFrameComparison(baseline, bestStats)+"; "+comparison+"; best candidate "+tuningControlSummary(bestCore, bestMemory, bestPower), "Stock hardware controls", "Restore the original controls instead of retaining an unproved clock", "Exact captured values restored")
		_ = a.restoreHardwareTuning("No material performance benefit was proved")
		a.setTuningProgress("WITHDRAWN", "No workload-comparable benefit crossed Kerneon's minimum threshold. Original hardware controls were restored.", 1, snapshot.CoreOffset, snapshot.MemoryOffset, snapshot.PowerLimitMilliwatts, peak)
		return
	}
	gpuChanged := bestCore != snapshot.CoreOffset || bestMemory != snapshot.MemoryOffset || bestPower != snapshot.PowerLimitMilliwatts
	if gpuChanged {
		route := determineSurgeRoute(baseline, a.directTuningRouteTelemetry(snapshot.Provider), 0, 0, 0)
		if !gpuTuningRouteEligible(route) {
			a.recordAutopilot("hardware-tuning-proof", "observed", "GPU tuning was withdrawn because the GPU was not the measured frame ceiling", route.Evidence+"; "+tuningFrameComparison(baseline, bestStats)+"; candidate "+tuningControlSummary(bestCore, bestMemory, bestPower), "Stock hardware controls", "Do not assign changing gameplay to a GPU clock when the GPU finishes early", "Exact captured values restored")
			_ = a.restoreHardwareTuning("The GPU was not the measured frame ceiling")
			a.setTuningProgress("WITHDRAWN", "The selected clock was stable, but this game window was limited outside GPU throughput. Stock controls were restored instead of making a false performance claim.", 1, snapshot.CoreOffset, snapshot.MemoryOffset, snapshot.PowerLimitMilliwatts, peak)
			return
		}
	}
	proved, stockMean, tunedMean, confirmationPeak, confirmation := a.confirmDirectTuningCandidate(stop, game, snapshot, bestCore, bestMemory, bestPower, envelope.TemperatureLimit)
	peak = math.Max(peak, confirmationPeak)
	if !proved {
		a.recordAutopilot("hardware-tuning-proof", "observed", "Hardware tuning was withdrawn because alternating stock/tuned confirmation did not reproduce the gain", tuningFrameComparison(stockMean, tunedMean)+"; "+confirmation+"; candidate "+tuningControlSummary(bestCore, bestMemory, bestPower), "A-B-B-A reversal confirmation", "Restore the original controls instead of retaining an unproved clock", "Exact captured values restored")
		_ = a.restoreHardwareTuning("Alternating confirmation did not reproduce a material benefit")
		a.setTuningProgress("WITHDRAWN", "The discovery candidate did not reproduce its benefit across both stock/tuned pairs. Original controls were restored.", 1, snapshot.CoreOffset, snapshot.MemoryOffset, snapshot.PowerLimitMilliwatts, peak)
		return
	}
	if err := a.applyTuningControls(snapshot, bestCore, bestMemory, bestPower, "Retain the best proved session controls"); err != nil {
		_ = a.restoreHardwareTuning("The best measured offsets could not be retained")
		return
	}
	a.recordAutopilot("hardware-tuning-proof", "proved", "Hardware tuning reproduced a material benefit in alternating stock/tuned confirmation", tuningFrameComparison(stockMean, tunedMean)+"; "+confirmation+"; retained "+tuningControlSummary(bestCore, bestMemory, bestPower), "Two exact-stock and two candidate windows in A-B-B-A order", "Retain the repeatedly measured candidate for this foreground session", "Exact captured values restore automatically")
	a.setTuningProgress("PROVED · SESSION ONLY", "Best measured controls retained while the game stays foreground: "+tuningControlSummary(bestCore, bestMemory, bestPower)+". Original values return automatically.", 1, bestCore, bestMemory, bestPower, peak)
	monitor := time.NewTicker(2 * time.Second)
	defer monitor.Stop()
	lastFaultCheck := time.Now()
	for {
		select {
		case <-stop:
			_ = a.restoreHardwareTuning("Tuning Lab stopped")
			return
		case <-monitor.C:
			current, ok := a.lockedGameProcess()
			if !ok || current.PID != game.PID || foregroundProcessID() != game.PID {
				_ = a.restoreHardwareTuning("The tuned game left the foreground")
				return
			}
			if snapshot.CoreSupported || snapshot.MemorySupported || snapshot.PowerSupported {
				telemetry := a.surgeStack.nvml.Snapshot()
				if telemetry.TemperatureC >= envelope.TemperatureLimit || telemetry.ThrottleReasons&(nvmlReasonHWSlowdown|nvmlReasonHWthermal|nvmlReasonPowerBrake) != 0 {
					_ = a.restoreHardwareTuning("A live thermal or hardware safeguard tripped")
					a.setTuningProgress("SAFETY ROLLBACK", "A thermal or hardware safeguard activated after discovery. Original controls were restored.", 1, snapshot.CoreOffset, snapshot.MemoryOffset, snapshot.PowerLimitMilliwatts, telemetry.TemperatureC)
					return
				}
			}
			if fault := tuningFaultSince(lastFaultCheck); fault != "" {
				_ = a.restoreHardwareTuning("A Windows hardware fault appeared after discovery")
				a.setTuningProgress("SAFETY ROLLBACK", fault+". Original controls were restored.", 1, snapshot.CoreOffset, snapshot.MemoryOffset, snapshot.PowerLimitMilliwatts, 0)
				return
			}
			lastFaultCheck = time.Now()
		}
	}
}

// amdHardwareTuningLoop is the fallback for Radeon cards whose driver exposes
// AMD's silicon-specific auto-clock engines but not numeric manual ranges. The
// AMD driver owns clock discovery; Kerneon owns the stock baseline, frame proof,
// fault gates and factory rollback boundary.
func (a *App) amdHardwareTuningLoop(game ProcessMetric, capability TuningLabCapability, stop <-chan struct{}) {
	if !capability.AMD.AtFactory {
		a.setTuningProgress("REFUSED", "AMD reports an existing non-factory tuning profile. Automatic discovery cannot replace it because ADLX cannot return its exact private curve.", 0, 0, 0, 0, 0)
		return
	}
	snapshot := hardwareTuningSnapshot{
		Version: 3, Provider: "AMD ADLX", Device: capability.GPU.Device,
		AMDFactoryOriginal: true, CapturedAt: time.Now(),
	}
	if err := a.persistHardwareTuningSnapshot(snapshot); err != nil {
		a.setTuningProgress("REFUSED", "Rollback snapshot could not be secured: "+err.Error(), 0, 0, 0, 0, 0)
		return
	}
	a.tuningLab.mu.Lock()
	a.tuningLab.original = snapshot
	a.tuningLab.Recovery = true
	a.tuningLab.mu.Unlock()
	envelope, thermalReady := estimatedTuningEnvelope(a.config.Tuning.LabProfile, capability.Estimate)
	if !thermalReady {
		_ = a.restoreHardwareTuning("Live thermal estimate was unavailable")
		a.setTuningProgress("REFUSED", "A valid live thermal estimate is required before clocks can change. Nothing was tuned.", 0, 0, 0, 0, 0)
		return
	}
	a.setTuningProgress("BASELINE", "Measuring stock frame delivery and temperature for 20 seconds.", 0.04, 0, 0, 0, 0)
	baseline, peak, problem := a.waitTuningWindow(stop, game, 20*time.Second, envelope.TemperatureLimit)
	if problem != "" || !baseline.Available || baseline.Samples < 120 {
		_ = a.restoreHardwareTuning("AMD baseline could not be validated")
		a.setTuningProgress("STOPPED", fallback(problem, "PresentMon did not provide a sufficient baseline. Nothing was tuned."), 0, 0, 0, 0, peak)
		return
	}
	bestScore, bestDomain, bestStats := tuningFrameScore(baseline), -1, baseline
	type autoDomain struct {
		name  string
		start func(time.Duration) error
	}
	domains := make([]autoDomain, 0, 2)
	if capability.AMD.GPUClock {
		domains = append(domains, autoDomain{"GPU core", a.surgeStack.adlx.StartGPUClockAutoTune})
	}
	if capability.AMD.VRAMClock && a.config.Tuning.LabProfile != "conservative" {
		domains = append(domains, autoDomain{"GPU memory", a.surgeStack.adlx.StartVRAMClockAutoTune})
	}
	if len(domains) == 0 {
		_ = a.restoreHardwareTuning("AMD exposed no eligible automatic clock engine")
		a.setTuningProgress("NOT AVAILABLE", "The AMD driver exposed no automatic core or VRAM clock engine for this profile.", 0, 0, 0, 0, peak)
		return
	}
	if len(domains) > 0 {
		route := determineSurgeRoute(baseline, a.directTuningRouteTelemetry("AMD ADLX"), 0, 0, 0)
		if !gpuTuningRouteEligible(route) {
			a.recordAutopilot("hardware-tuning-route", "observed", "AMD clock discovery was skipped because the GPU was not the measured frame ceiling", route.Evidence, "AMD factory controls", "Keep monitoring for a genuinely GPU-limited window", "No AMD automatic tuning transaction was started")
			_ = a.restoreHardwareTuning("AMD GPU tuning was not relevant to the measured frame route")
			a.setTuningProgress("NOT APPLICABLE", "The GPU is finishing before the frame deadline, so AMD clock discovery would not address this game's current limit. Factory controls remain active.", 1, 0, 0, 0, peak)
			return
		}
	}
	for index, domain := range domains {
		select {
		case <-stop:
			_ = a.restoreHardwareTuning("Tuning Lab stopped")
			return
		default:
		}
		if err := a.surgeStack.adlx.RestoreFactory(); err != nil {
			_ = a.restoreHardwareTuning("AMD factory boundary could not be restored")
			a.setTuningProgress("SAFETY ROLLBACK", err.Error(), 1, 0, 0, 0, peak)
			return
		}
		a.setTuningProgress(strings.ToUpper(domain.name)+" DISCOVERY", "AMD is calculating a clock curve for this individual GPU. This can take several minutes.", 0.1+0.35*float64(index), 0, 0, 0, peak)
		if !a.recordAutopilot("amd-auto-clock", "prepared", domain.name+" automatic discovery", capability.AMD.Device, "AMD factory tuning", "Run AMD's documented per-device automatic clock engine", "ResetToFactory through ADLX") {
			_ = a.restoreHardwareTuning("The journal refused the AMD clock transaction")
			return
		}
		if err := domain.start(8 * time.Minute); err != nil {
			a.recordAutopilot("amd-auto-clock", "failed", domain.name+" automatic discovery", err.Error(), "AMD factory tuning", "No result retained", "ResetToFactory through ADLX")
			continue
		}
		a.tuningLab.mu.Lock()
		a.tuningLab.Applied = true
		a.tuningLab.mu.Unlock()
		telemetry := a.surgeStack.adlx.Snapshot()
		stats, trialPeak, fault := a.waitTuningWindow(stop, game, 18*time.Second, envelope.TemperatureLimit)
		peak = math.Max(peak, trialPeak)
		result := "stable"
		if fault != "" || tuningFrameRegression(baseline, stats) {
			result = "rejected"
		} else if comparable, _ := comparableFrameWorkload(baseline, stats); comparable && tuningFrameScore(stats) > bestScore {
			bestScore, bestDomain, bestStats, result = tuningFrameScore(stats), index, stats, "best so far"
		}
		a.appendTuningTrial(baseline, TuningTrial{At: time.Now(), Domain: domain.name, Result: result, CoreMHz: telemetry.CoreClockMHz, MemoryMHz: telemetry.VRAMClockMHz, TemperatureC: trialPeak}, stats, fault)
		a.recordAutopilot("amd-auto-clock", result, domain.name+" automatic discovery", fallback(fault, fmt.Sprintf("core %d MHz; VRAM %d MHz", telemetry.CoreClockMHz, telemetry.VRAMClockMHz)), "AMD factory tuning", "Measured AMD automatic clock result", "ResetToFactory through ADLX")
	}
	if bestDomain < 0 || !frameBenefitThresholdCrossed(baseline, bestStats) {
		_, comparison := comparableFrameWorkload(baseline, bestStats)
		a.recordAutopilot("hardware-tuning-proof", "observed", "AMD hardware tuning was withdrawn because it did not produce a material frame-delivery gain", tuningFrameComparison(baseline, bestStats)+"; "+comparison, "AMD factory tuning", "Restore factory tuning instead of retaining an unproved clock", "ResetToFactory through ADLX")
		_ = a.restoreHardwareTuning("No material AMD clock benefit was proved")
		a.setTuningProgress("WITHDRAWN", "No workload-comparable benefit crossed Kerneon's minimum threshold. AMD factory tuning was restored.", 1, 0, 0, 0, peak)
		return
	}
	if bestDomain >= 0 {
		route := determineSurgeRoute(baseline, a.directTuningRouteTelemetry("AMD ADLX"), 0, 0, 0)
		if !gpuTuningRouteEligible(route) {
			a.recordAutopilot("hardware-tuning-proof", "observed", "AMD GPU tuning was withdrawn because the GPU was not the measured frame ceiling", route.Evidence+"; "+tuningFrameComparison(baseline, bestStats), "AMD factory tuning", "Do not assign changing gameplay to a GPU clock when the GPU finishes early", "ResetToFactory through ADLX")
			_ = a.restoreHardwareTuning("The GPU was not the measured frame ceiling")
			a.setTuningProgress("WITHDRAWN", "The AMD clock was stable, but this window was limited outside GPU throughput. Factory controls were restored instead of making a false performance claim.", 1, 0, 0, 0, peak)
			return
		}
	}
	if err := a.surgeStack.adlx.RestoreFactory(); err != nil {
		_ = a.restoreHardwareTuning("AMD final apply could not begin from factory")
		return
	}
	if err := domains[bestDomain].start(8 * time.Minute); err != nil {
		_ = a.restoreHardwareTuning("AMD best clock result could not be reapplied for confirmation")
		return
	}
	a.tuningLab.mu.Lock()
	a.tuningLab.Applied = true
	a.tuningLab.mu.Unlock()
	a.setTuningProgress("REVERSAL CONFIRMATION", "Tuned B2/2 · repeating AMD's selected automatic clock result.", 0.94, 0, 0, 0, peak)
	tuned2, tunedPeak, problem := a.confirmationWindow(stop, game, tuningConfirmationWindow, tuningConfirmationSettle, envelope.TemperatureLimit)
	peak = math.Max(peak, tunedPeak)
	if problem != "" || !tuned2.Available || tuned2.Samples < 120 {
		_ = a.restoreHardwareTuning("AMD tuned confirmation window was incomplete")
		a.setTuningProgress("WITHDRAWN", fallback(problem, "The repeated AMD tuned window was incomplete. Factory controls were restored."), 1, 0, 0, 0, peak)
		return
	}
	if err := a.surgeStack.adlx.RestoreFactory(); err != nil {
		_ = a.restoreHardwareTuning("AMD confirmation could not return to factory")
		return
	}
	a.setTuningProgress("REVERSAL CONFIRMATION", "Stock A2/2 · measuring AMD factory controls on the other side of the candidate.", 0.97, 0, 0, 0, peak)
	stock2, stockPeak, problem := a.confirmationWindow(stop, game, tuningConfirmationWindow, tuningConfirmationSettle, envelope.TemperatureLimit)
	peak = math.Max(peak, stockPeak)
	if problem != "" || !stock2.Available || stock2.Samples < 120 {
		_ = a.restoreHardwareTuning("AMD stock confirmation window was incomplete")
		a.setTuningProgress("WITHDRAWN", fallback(problem, "The second AMD stock window was incomplete. Factory controls were restored."), 1, 0, 0, 0, peak)
		return
	}
	stockWindows, tunedWindows := []FrameStats{baseline, stock2}, []FrameStats{bestStats, tuned2}
	for index := range stockWindows {
		comparable, comparison := comparableFrameWorkload(stockWindows[index], tunedWindows[index])
		status := "observed"
		if comparable && !tuningFrameRegression(stockWindows[index], tunedWindows[index]) && frameBenefitThresholdCrossed(stockWindows[index], tunedWindows[index]) {
			status = "passed"
		}
		a.recordAutopilot("hardware-tuning-confirmation", status, fmt.Sprintf("Alternating AMD stock/tuned pair %d", index+1), tuningFrameComparison(stockWindows[index], tunedWindows[index])+"; "+comparison, "AMD factory controls", "Reapply and remeasure the selected automatic clock result", "ResetToFactory through ADLX")
	}
	proved, stockMean, tunedMean, confirmation := evaluateTuningConfirmation(stockWindows, tunedWindows)
	if !proved {
		a.recordAutopilot("hardware-tuning-proof", "observed", "AMD tuning was withdrawn because alternating stock/tuned confirmation did not reproduce the gain", tuningFrameComparison(stockMean, tunedMean)+"; "+confirmation, "A-B-B-A reversal confirmation", "Keep AMD factory controls", "ResetToFactory complete")
		_ = a.restoreHardwareTuning("Alternating AMD confirmation did not reproduce a material benefit")
		a.setTuningProgress("WITHDRAWN", "The AMD candidate did not reproduce its benefit across both stock/tuned pairs. Factory controls were restored.", 1, 0, 0, 0, peak)
		return
	}
	if err := domains[bestDomain].start(8 * time.Minute); err != nil {
		_ = a.restoreHardwareTuning("Confirmed AMD clock result could not be retained")
		return
	}
	retained := domains[bestDomain].name + " automatic clock result"
	a.tuningLab.mu.Lock()
	a.tuningLab.Applied = true
	a.tuningLab.mu.Unlock()
	a.recordAutopilot("hardware-tuning-proof", "proved", "AMD GPU tuning reproduced a material benefit in alternating stock/tuned confirmation", tuningFrameComparison(stockMean, tunedMean)+"; "+confirmation+"; retained "+retained, "Two factory-stock and two candidate windows in A-B-B-A order", "Retain the repeatedly measured GPU candidate for this foreground session", "AMD factory tuning restores automatically")
	a.setTuningProgress("PROVED · SESSION ONLY", retained+" retained after reversal confirmation. AMD factory tuning returns automatically.", 1, 0, 0, 0, peak)
	a.monitorAMDTuning(stop, game, envelope)
}

func (a *App) monitorAMDTuning(stop <-chan struct{}, game ProcessMetric, envelope tuningEnvelope) {
	monitor := time.NewTicker(2 * time.Second)
	defer monitor.Stop()
	lastFaultCheck := time.Now()
	for {
		select {
		case <-stop:
			_ = a.restoreHardwareTuning("Tuning Lab stopped")
			return
		case <-monitor.C:
			current, ok := a.lockedGameProcess()
			if !ok || current.PID != game.PID || foregroundProcessID() != game.PID {
				_ = a.restoreHardwareTuning("The tuned game left the foreground")
				return
			}
			telemetry := a.surgeStack.adlx.Snapshot()
			temperature := math.Max(telemetry.TemperatureC, telemetry.HotspotC)
			if temperature >= envelope.TemperatureLimit {
				_ = a.restoreHardwareTuning("The AMD thermal ceiling was reached")
				a.setTuningProgress("SAFETY ROLLBACK", "The AMD thermal ceiling activated after discovery. Factory clocks were restored.", 1, 0, 0, 0, temperature)
				return
			}
			if fault := tuningFaultSince(lastFaultCheck); fault != "" {
				_ = a.restoreHardwareTuning("A Windows hardware fault appeared after AMD discovery")
				a.setTuningProgress("SAFETY ROLLBACK", fault+". Captured controls were restored.", 1, 0, 0, 0, temperature)
				return
			}
			lastFaultCheck = time.Now()
		}
	}
}

func (a *App) stopHardwareTuning(reason string) {
	a.tuningLab.mu.Lock()
	stop, done, running := a.tuningLab.stop, a.tuningLab.done, a.tuningLab.Running
	hadState := running || a.tuningLab.Applied || a.tuningLab.Recovery || !a.tuningLab.original.CapturedAt.IsZero()
	if running && stop != nil {
		a.tuningLab.stopOnce.Do(func() { close(stop) })
	}
	a.tuningLab.mu.Unlock()
	if running && done != nil {
		select {
		case <-done:
		case <-time.After(4 * time.Second):
			_ = a.restoreHardwareTuning(reason)
		}
	} else {
		_ = a.restoreHardwareTuning(reason)
	}
	if hadState && reason != "Kerneon is closing" {
		a.tuningLab.mu.Lock()
		if a.tuningLab.Recovery {
			a.tuningLab.Stage = "ROLLBACK NEEDS ATTENTION"
			a.tuningLab.Status = reason + ". The recovery snapshot remains available because exact restoration could not be confirmed."
		} else {
			a.tuningLab.Stage = "STOPPED · STOCK RESTORED"
			a.tuningLab.Status = reason + ". Exact captured hardware controls were restored."
			a.tuningLab.Progress = 1
		}
		a.tuningLab.mu.Unlock()
		if a.hwnd != 0 {
			procPostMessageW.Call(a.hwnd, WM_APP_RENDER, 0, 0)
		}
	}
}
