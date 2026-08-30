//go:build windows

package main

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unsafe"
)

func TestSummarizeSurgeReadinessRequiresEveryRequiredProvider(t *testing.T) {
	dependencies := []SurgeDependency{
		{ID: "presentmon", Required: true, Ready: true},
		{ID: "nvidia", Required: true, Ready: false},
		{ID: "amd", Required: false, Ready: false},
	}
	ready, total, coreReady := summarizeSurgeReadiness(dependencies)
	if ready != 1 || total != 2 || coreReady {
		t.Fatalf("unexpected readiness: ready=%d total=%d core=%t", ready, total, coreReady)
	}
}

func TestNVAPIDRSReadsRequestedApplicationProfile(t *testing.T) {
	executable := os.Getenv("KERNEON_NVAPI_TEST_EXE")
	if executable == "" {
		t.Skip("KERNEON_NVAPI_TEST_EXE is not set")
	}
	var provider NVAPIProvider
	snapshot := provider.QueryPowerPolicy(executable)
	defer provider.Close()
	if !snapshot.Ready || !snapshot.Found {
		t.Fatalf("application profile was unavailable: %+v", snapshot)
	}
	t.Logf("application NVIDIA policy: %s (%s, inherited=%t)", snapshot.PowerPolicyName, snapshot.Location, snapshot.Inherited)
}

func TestXMLProfileValueIsElementSpecific(t *testing.T) {
	data := []byte(`<Settings><VSync value="1" /><ReflexMode value="0" /></Settings>`)
	if got := xmlProfileValue(data, "ReflexMode"); got != "0" {
		t.Fatalf("ReflexMode=%q", got)
	}
	if got := xmlProfileValue(data, "VSync"); got != "1" {
		t.Fatalf("VSync=%q", got)
	}
}

func TestUnknownGameStillReceivesUniversalSurgePipeline(t *testing.T) {
	profile := detectSurgeGameProfile("A_Game_Kerneon_Has_Never_Seen.exe", "")
	if !profile.Detected || !profile.Universal {
		t.Fatalf("unknown game was not accepted by the universal pipeline: %+v", profile)
	}
	if profile.Adapter != "Universal" || profile.Provider != "Universal executable engine" {
		t.Fatalf("unknown game was incorrectly tied to a title adapter: %+v", profile)
	}
	if profile.LatencyOpportunity == "" || profile.Evidence == "" {
		t.Fatalf("unknown game did not receive analysis context: %+v", profile)
	}
}

func TestAntiCheatProcessForcesExternalOnlyPolicy(t *testing.T) {
	state := antiCheatProcessEvidence([]ProcessMetric{{PID: 44, Name: "BEService.exe"}})
	if !state.Detected || state.Provider != "BattlEye" || !strings.Contains(state.Policy, "External-only") {
		t.Fatalf("unexpected anti-cheat state: %+v", state)
	}
	clear := antiCheatProcessEvidence([]ProcessMetric{{PID: 55, Name: "ordinary-game.exe"}})
	if clear.Detected || strings.Contains(clear.Policy, "External-only") {
		t.Fatalf("ordinary executable should retain the non-injected base policy: %+v", clear)
	}
}

func TestNVAPIDRSSettingLayoutAndDWORDEncoding(t *testing.T) {
	if got := unsafe.Sizeof(nvDRSSetting{}); got != 12320 {
		t.Fatalf("NVDRS_SETTING_V1 layout drifted: got %d bytes", got)
	}
	setting := nvDRSSetting{Version: nvapiVersion(unsafe.Sizeof(nvDRSSetting{}), nvapiDRSSettingVersion), SettingID: nvidiaPreferredPStateID}
	binary.LittleEndian.PutUint32(setting.CurrentValue[:4], nvidiaPreferMaximum)
	if setting.Version != 0x00013020 || binary.LittleEndian.Uint32(setting.CurrentValue[:4]) != 1 {
		t.Fatalf("unexpected NVAPI setting encoding: version=0x%08X value=%d", setting.Version, binary.LittleEndian.Uint32(setting.CurrentValue[:4]))
	}
}

func TestDescribeNVMLReasonsSeparatesIdleFromConstraints(t *testing.T) {
	if got := describeNVMLReasons(nvmlReasonGPUIdle); got != "idle" {
		t.Fatalf("idle summary=%q", got)
	}
	got := describeNVMLReasons(nvmlReasonPowerCap | nvmlReasonSWThermal)
	if got != "power cap, thermal" {
		t.Fatalf("constraint summary=%q", got)
	}
}

func TestTuningTargetsNeverExceedProviderOrProfileBounds(t *testing.T) {
	if got := tuningTarget(0, 500, 120, 0.9); got != 120 {
		t.Fatalf("clock hard cap ignored: got %d", got)
	}
	if got := tuningTarget(470, 500, 120, 0.9); got != 497 {
		t.Fatalf("clock provider range calculation changed: got %d", got)
	}
	if got := tuningPowerTarget(250000, 400000, 15, 0.9); got != 265000 {
		t.Fatalf("power hard cap ignored: got %.0f W", float64(got)/1000)
	}
	if got := tuningPowerTarget(398000, 400000, 15, 0.9); got > 400000 {
		t.Fatalf("power target escaped provider maximum: got %.0f W", float64(got)/1000)
	}
}

func TestTuningProfileExplanationsAreSpecificAndHardwareAware(t *testing.T) {
	capability := TuningLabCapability{GPUOffsets: NVMLClockCapability{
		Core:   NVMLClockDomainCapability{Supported: true, Current: 0, Min: -200, Max: 250, PState: 0, API: "nvmlDeviceGet/SetClockOffsets"},
		Memory: NVMLClockDomainCapability{Supported: true, Current: 0, Min: -500, Max: 1500, PState: 0, API: "nvmlDeviceGet/SetClockOffsets"},
		Power:  NVMLPowerCapability{Supported: true, Current: 250000, Default: 250000, Min: 125000, Max: 300000},
	}, Estimate: TuningHardwareEstimate{Ready: true, Samples: 7, Duration: 3 * time.Second, AverageTemperatureC: 42, PeakTemperatureC: 45, AveragePowerW: 35, PeakPowerW: 42}}
	wants := map[string][]string{
		"conservative": {"8 W", "75 MHz", "69°C", "3 W"},
		"balanced":     {"15 W", "120 MHz", "76°C", "5 W"},
		"enthusiast":   {"30 W", "180 MHz", "82°C", "5 W"},
	}
	for profile, fragments := range wants {
		explanation := explainTuningProfile(profile, capability, true)
		joined := explanation.Title + " " + explanation.BestFor + " " + explanation.MayTest + " " + explanation.Search + " " + explanation.ThisPC + " " + explanation.Stops
		for _, fragment := range fragments {
			if !strings.Contains(joined, fragment) {
				t.Fatalf("%s explanation omitted %q: %s", profile, fragment, joined)
			}
		}
		if !strings.Contains(explanation.ThisPC, "GPU core P0") || !strings.Contains(explanation.ThisPC, "driver range") || !strings.Contains(explanation.Stops, "write-ahead snapshot") {
			t.Fatalf("%s explanation hid current capability or rollback: %+v", profile, explanation)
		}
		easy := explainTuningProfile(profile, capability, false)
		if strings.Contains(easy.Search, "p99") || strings.Contains(easy.Search, "NVML") || !strings.Contains(easy.Stops, "puts every changed value back exactly as it was") {
			t.Fatalf("%s easy explanation leaked jargon or hid rollback: %+v", profile, easy)
		}
	}
}

func TestTuningProfileTemperatureIsHiddenUntilLiveEstimateIsReady(t *testing.T) {
	capability := TuningLabCapability{}
	for _, profile := range []string{"conservative", "balanced", "enthusiast"} {
		detail := tuningProfileCardDetail(profile, capability, false)
		if strings.Contains(detail, "°C") || !strings.Contains(detail, "Estimating") {
			t.Fatalf("%s exposed a temperature before telemetry was ready: %q", profile, detail)
		}
		explanation := explainTuningProfile(profile, capability, true)
		if strings.Contains(explanation.Search, "°C") || !strings.Contains(explanation.Search, "unarmed") {
			t.Fatalf("%s tooltip armed or exposed a premature ceiling: %+v", profile, explanation)
		}
	}
}

func TestTuningProfileTemperatureIsCalculatedFromMeasuredPeak(t *testing.T) {
	cool := TuningHardwareEstimate{Ready: true, Samples: 7, PeakTemperatureC: 40}
	warm := TuningHardwareEstimate{Ready: true, Samples: 7, PeakTemperatureC: 50}
	previous := 0.0
	for _, profile := range []string{"conservative", "balanced", "enthusiast"} {
		coolLimit, ok := estimatedProfileTemperatureLimit(profile, cool)
		if !ok {
			t.Fatalf("%s did not produce a limit from valid telemetry", profile)
		}
		warmLimit, ok := estimatedProfileTemperatureLimit(profile, warm)
		if !ok || warmLimit <= coolLimit {
			t.Fatalf("%s limit was not dynamic: cool %.0f°C, warm %.0f°C", profile, coolLimit, warmLimit)
		}
		if coolLimit <= previous || coolLimit > tuningProfileEnvelope(profile).TemperatureLimit {
			t.Fatalf("%s produced an invalid profile ordering/bound: %.0f°C after %.0f°C", profile, coolLimit, previous)
		}
		previous = coolLimit
	}
}

func TestTuningEstimateIsDerivedFromLiveHeadroom(t *testing.T) {
	capability := TuningLabCapability{GPUOffsets: NVMLClockCapability{
		Core:   NVMLClockDomainCapability{Supported: true, Current: 25, Min: -200, Max: 225},
		Memory: NVMLClockDomainCapability{Supported: true, Current: 100, Min: -500, Max: 700},
		Power:  NVMLPowerCapability{Supported: true, Current: 250000, Min: 125000, Max: 275000},
	}, Estimate: TuningHardwareEstimate{Ready: true, Samples: 7, PeakTemperatureC: 60}}
	coolEnvelope, ok := estimatedTuningEnvelope("enthusiast", capability.Estimate)
	if !ok {
		t.Fatal("valid cool estimate did not arm an envelope")
	}
	coreCool, memoryCool, powerCool := estimatedTuningDeltas(capability, coolEnvelope)
	capability.Estimate.PeakTemperatureC = 80
	hotEnvelope, ok := estimatedTuningEnvelope("enthusiast", capability.Estimate)
	if !ok {
		t.Fatal("valid hot estimate did not arm an envelope")
	}
	coreHot, memoryHot, powerHot := estimatedTuningDeltas(capability, hotEnvelope)
	if coreCool <= coreHot || memoryCool <= memoryHot || powerCool <= powerHot {
		t.Fatalf("thermal/headroom estimate did not change: cool=%d/%d/%d hot=%d/%d/%d", coreCool, memoryCool, powerCool, coreHot, memoryHot, powerHot)
	}
	if coreCool > 200 || memoryCool > 600 || powerCool > 25 {
		t.Fatalf("estimate escaped live device headroom: %d/%d/%d", coreCool, memoryCool, powerCool)
	}
}

func TestActiveSurgeLocksCompletedStockEstimate(t *testing.T) {
	a := &App{}
	a.config.Tuning.Autopilot = true
	a.tuningLab.Capability.Estimate = TuningHardwareEstimate{Ready: true, Samples: 7, PeakTemperatureC: 57}
	a.tuningLab.Estimating = true
	a.holdTuningEstimateWhileSurgeActive()
	view := a.tuningLabSnapshot()
	if view.Estimating || !view.Capability.Estimate.Ready || view.Capability.Estimate.PeakTemperatureC != 57 {
		t.Fatalf("active Surge changed its stock estimate: %+v", view)
	}
	if view.Stage != "STOCK ESTIMATE LOCKED" || !strings.Contains(view.Status, "will not recalculate") {
		t.Fatalf("active Surge did not visibly lock the estimate: %q / %q", view.Stage, view.Status)
	}
}

func TestActiveSurgeNeverCreatesMissingEstimate(t *testing.T) {
	a := &App{}
	a.config.Tuning.Autopilot = true
	a.tuningLab.Estimating = true
	a.holdTuningEstimateWhileSurgeActive()
	view := a.tuningLabSnapshot()
	if view.Estimating || view.Capability.Estimate.Ready {
		t.Fatalf("active Surge invented or continued an estimate: %+v", view)
	}
	if view.Stage != "STOCK ESTIMATE REQUIRED" || !strings.Contains(view.Status, "Pause Surge") {
		t.Fatalf("missing stock estimate was not explained: %q / %q", view.Stage, view.Status)
	}
}

func TestTuningStepsTerminateAtExactTarget(t *testing.T) {
	clock := tuningSteps(0, 52, 15)
	if len(clock) != 4 || clock[len(clock)-1] != 52 {
		t.Fatalf("unexpected clock steps: %v", clock)
	}
	power := tuningPowerSteps(250000, 262000, 5)
	if len(power) != 3 || power[len(power)-1] != 262000 {
		t.Fatalf("unexpected power steps: %v", power)
	}
}

func TestNVMLControlValidationIsCapabilityBound(t *testing.T) {
	clock := NVMLClockDomainCapability{Supported: true, Current: 0, Min: -100, Max: 150}
	if err := validateNVMLClockOffset(clock, 151); err == nil {
		t.Fatal("out-of-range clock offset was accepted")
	}
	if err := validateNVMLClockOffset(clock, -100); err != nil {
		t.Fatalf("driver boundary was rejected: %v", err)
	}
	power := NVMLPowerCapability{Supported: true, Current: 250000, Default: 250000, Min: 200000, Max: 300000}
	if err := validateNVMLPowerLimit(power, 300001); err == nil {
		t.Fatal("out-of-range power limit was accepted")
	}
	if err := validateNVMLPowerLimit(power, 300000); err != nil {
		t.Fatalf("driver power boundary was rejected: %v", err)
	}
}

func TestForcedHardwareControlTargetIsSmallAndCapabilityBound(t *testing.T) {
	capability := NVMLClockCapability{
		Core:   NVMLClockDomainCapability{Supported: true, Current: 0, Min: -200, Max: 15},
		Memory: NVMLClockDomainCapability{Supported: true, Current: 100, Min: -500, Max: 1000},
		Power:  NVMLPowerCapability{Supported: true, Current: 250000, Min: 125000, Max: 250500},
	}
	target := boundedHardwareControlValidationTarget(capability)
	if !target.Changed || target.CoreMHz != 15 || target.MemoryMHz != 200 || target.PowerMilliwatts != 250500 {
		t.Fatalf("unexpected bounded control target: %+v", target)
	}
	if target.CoreMHz > capability.Core.Max || target.MemoryMHz > capability.Memory.Max || target.PowerMilliwatts > capability.Power.Max {
		t.Fatalf("control target escaped driver range: %+v", target)
	}
}

func TestForcedHardwareControlReadbackRequiresExactValues(t *testing.T) {
	capability := NVMLClockCapability{
		Core:   NVMLClockDomainCapability{Supported: true, Current: 25},
		Memory: NVMLClockDomainCapability{Supported: true, Current: 200},
		Power:  NVMLPowerCapability{Supported: true, Current: 251000},
	}
	if err := verifyHardwareControlReadback(capability, 25, 200, 251000); err != nil {
		t.Fatalf("exact driver read-back was rejected: %v", err)
	}
	capability.Memory.Current = 100
	if err := verifyHardwareControlReadback(capability, 25, 200, 251000); err == nil || !strings.Contains(err.Error(), "VRAM") {
		t.Fatalf("mismatched driver read-back was accepted: %v", err)
	}
}

func TestRollbackSnapshotRequestsOnlyOriginallySupportedControls(t *testing.T) {
	snapshot := hardwareTuningSnapshot{CoreSupported: false, MemorySupported: true, PowerSupported: true}
	core, memory, power := snapshotControlPointers(snapshot, 15, 75, 260000)
	if core != nil || memory == nil || *memory != 75 || power == nil || *power != 260000 {
		t.Fatalf("unexpected rollback control selection: core=%v memory=%v power=%v", core, memory, power)
	}
}

func TestNVMLProviderSmokeWhenNVIDIARuntimeIsPresent(t *testing.T) {
	if existingFile(filepath.Join(windowsSystemDirectory(), "nvml.dll")) == "" {
		t.Skip("NVIDIA NVML is not installed")
	}
	var provider NVMLProvider
	snapshot := provider.Snapshot()
	defer provider.Close()
	if !snapshot.Ready {
		t.Fatalf("NVML runtime was detected but provider was not ready: %s", snapshot.Error)
	}
	if snapshot.Name == "" {
		t.Fatal("NVML did not return the GPU name")
	}
	t.Logf("%s: %.0fC %.1f/%.1fW %dMHz", snapshot.Name, snapshot.TemperatureC, snapshot.PowerW, snapshot.PowerLimitW, snapshot.GraphicsClockMHz)
	capability := provider.ClockCapability()
	t.Logf("tuning controls: core=%t memory=%t power=%t (%s)", capability.Core.Supported, capability.Memory.Supported, capability.Power.Supported, capability.Error)
}

func TestNVAPIDRSReadsGlobalPowerPolicyWhenRuntimeIsPresent(t *testing.T) {
	if existingFile(filepath.Join(windowsSystemDirectory(), "nvapi64.dll")) == "" {
		t.Skip("NVIDIA NVAPI is not installed")
	}
	var provider NVAPIProvider
	snapshot := provider.QueryPowerPolicy("")
	defer provider.Close()
	if !snapshot.Ready || !snapshot.Found {
		t.Fatalf("NVAPI DRS was detected but global profile was unavailable: %+v", snapshot)
	}
	if snapshot.PowerPolicyName == "" || snapshot.Location == "" {
		t.Fatalf("NVAPI returned an incomplete power profile: %+v", snapshot)
	}
	t.Logf("global NVIDIA policy: %s (%s)", snapshot.PowerPolicyName, snapshot.Location)
}
