//go:build windows

package main

import (
	"fmt"
	"strings"
	"sync"
	"syscall"
	"unsafe"
)

const (
	nvmlSuccess              = 0
	nvmlErrorUninitialized   = 1
	nvmlErrorInvalidArgument = 2
	nvmlErrorNotSupported    = 3
	nvmlErrorNoPermission    = 4
	nvmlTemperatureGPU       = 0
	nvmlClockGraphics        = 0
	nvmlClockMemory          = 2
	nvmlReasonGPUIdle        = uint64(1 << 0)
	nvmlReasonAppClocks      = uint64(1 << 1)
	nvmlReasonPowerCap       = uint64(1 << 2)
	nvmlReasonHWSlowdown     = uint64(1 << 3)
	nvmlReasonSyncBoost      = uint64(1 << 4)
	nvmlReasonSWThermal      = uint64(1 << 5)
	nvmlReasonHWthermal      = uint64(1 << 6)
	nvmlReasonPowerBrake     = uint64(1 << 7)
	nvmlReasonDisplay        = uint64(1 << 8)
)

var (
	nvmlDLL                            = syscall.NewLazyDLL("nvml.dll")
	procNVMLInit                       = nvmlDLL.NewProc("nvmlInit_v2")
	procNVMLShutdown                   = nvmlDLL.NewProc("nvmlShutdown")
	procNVMLDeviceGetCount             = nvmlDLL.NewProc("nvmlDeviceGetCount_v2")
	procNVMLDeviceGetHandleByIndex     = nvmlDLL.NewProc("nvmlDeviceGetHandleByIndex_v2")
	procNVMLDeviceGetName              = nvmlDLL.NewProc("nvmlDeviceGetName")
	procNVMLDeviceGetUtilization       = nvmlDLL.NewProc("nvmlDeviceGetUtilizationRates")
	procNVMLDeviceGetTemperature       = nvmlDLL.NewProc("nvmlDeviceGetTemperature")
	procNVMLDeviceGetPowerUsage        = nvmlDLL.NewProc("nvmlDeviceGetPowerUsage")
	procNVMLDeviceGetPowerLimit        = nvmlDLL.NewProc("nvmlDeviceGetPowerManagementLimit")
	procNVMLDeviceGetDefaultPowerLimit = nvmlDLL.NewProc("nvmlDeviceGetPowerManagementDefaultLimit")
	procNVMLDeviceGetPowerConstraints  = nvmlDLL.NewProc("nvmlDeviceGetPowerManagementLimitConstraints")
	procNVMLDeviceGetClock             = nvmlDLL.NewProc("nvmlDeviceGetClockInfo")
	procNVMLDeviceGetMaxClock          = nvmlDLL.NewProc("nvmlDeviceGetMaxClockInfo")
	procNVMLDeviceGetPState            = nvmlDLL.NewProc("nvmlDeviceGetPerformanceState")
	procNVMLDeviceGetThrottleReasons   = nvmlDLL.NewProc("nvmlDeviceGetCurrentClocksThrottleReasons")
	procNVMLDeviceGetGPCOffset         = nvmlDLL.NewProc("nvmlDeviceGetGpcClkVfOffset")
	procNVMLDeviceGetMemoryOffset      = nvmlDLL.NewProc("nvmlDeviceGetMemClkVfOffset")
	procNVMLDeviceGetGPCOffsetRange    = nvmlDLL.NewProc("nvmlDeviceGetGpcClkMinMaxVfOffset")
	procNVMLDeviceGetMemoryOffsetRange = nvmlDLL.NewProc("nvmlDeviceGetMemClkMinMaxVfOffset")
	procNVMLDeviceGetClockOffsets      = nvmlDLL.NewProc("nvmlDeviceGetClockOffsets")
	procNVMLDeviceSetGPCOffset         = nvmlDLL.NewProc("nvmlDeviceSetGpcClkVfOffset")
	procNVMLDeviceSetMemoryOffset      = nvmlDLL.NewProc("nvmlDeviceSetMemClkVfOffset")
	procNVMLDeviceSetClockOffsets      = nvmlDLL.NewProc("nvmlDeviceSetClockOffsets")
	procNVMLDeviceSetPowerLimit        = nvmlDLL.NewProc("nvmlDeviceSetPowerManagementLimit")
)

type nvmlUtilization struct {
	GPU, Memory uint32
}

// SurgeGPUTelemetry is deliberately read-only. Control calls live behind a
// separate, privileged provider boundary so merely opening Surge can never
// change clocks, voltage, fans, or power limits.
type SurgeGPUTelemetry struct {
	Ready                 bool
	Provider, Name, Error string
	GPUUtil, MemoryUtil   float64
	TemperatureC          float64
	PowerW, PowerLimitW   float64
	DefaultPowerLimitW    float64
	MinPowerLimitW        float64
	MaxPowerLimitW        float64
	GraphicsClockMHz      uint32
	MaxGraphicsClockMHz   uint32
	MemoryClockMHz        uint32
	MaxMemoryClockMHz     uint32
	PState                uint32
	ThrottleReasons       uint64
	ThrottleSummary       string
}

type NVMLProvider struct {
	mu          sync.Mutex
	initialized bool
	device      uintptr
}

type NVMLClockDomainCapability struct {
	Supported         bool
	Current, Min, Max int32
	PState            uint32
	API               string
	Error             string
}

// nvmlClockOffsetV1 is the public NVML 555+ clock-offset structure. NVIDIA's
// structure version convention stores the byte size in the low 24 bits and
// the structure revision in the high byte.
type nvmlClockOffsetV1 struct {
	Version           uint32
	Type              uint32
	PState            uint32
	ClockOffsetMHz    int32
	MinClockOffsetMHz int32
	MaxClockOffsetMHz int32
}

const nvmlClockOffsetV1Version = uint32(unsafe.Sizeof(nvmlClockOffsetV1{})) | 1<<24

type NVMLPowerCapability struct {
	Supported                  bool
	Current, Default, Min, Max uint32
	Error                      string
}

// NVMLClockCapability contains only ranges returned by the installed NVIDIA
// driver. Kerneon never invents an offset range and never treats the presence
// of NVML telemetry as proof that a consumer GPU exposes privileged writes.
type NVMLClockCapability struct {
	Ready             bool
	Device            string
	Core, Memory      NVMLClockDomainCapability
	Power             NVMLPowerCapability
	RequiresElevation bool
	Error             string
}

func nvmlCallOK(proc *syscall.LazyProc, args ...uintptr) bool {
	result, _, _ := proc.Call(args...)
	return uint32(result) == nvmlSuccess
}

func nvmlResult(proc *syscall.LazyProc, args ...uintptr) uint32 {
	result, _, _ := proc.Call(args...)
	return uint32(result)
}

func nvmlResultError(operation string, result uint32) error {
	label := fmt.Sprintf("NVML result %d", result)
	switch result {
	case nvmlErrorUninitialized:
		label = "NVML is not initialized"
	case nvmlErrorInvalidArgument:
		label = "the driver rejected the requested range"
	case nvmlErrorNotSupported:
		label = "this GPU or driver does not expose the control"
	case nvmlErrorNoPermission:
		label = "administrator permission is required"
	}
	return fmt.Errorf("%s: %s", operation, label)
}

func (provider *NVMLProvider) initialize() error {
	if provider.initialized && provider.device != 0 {
		return nil
	}
	if err := nvmlDLL.Load(); err != nil {
		return fmt.Errorf("load NVIDIA NVML: %w", err)
	}
	if !nvmlCallOK(procNVMLInit) {
		return fmt.Errorf("NVIDIA NVML refused initialization")
	}
	var count uint32
	if !nvmlCallOK(procNVMLDeviceGetCount, uintptr(unsafe.Pointer(&count))) || count == 0 {
		_, _, _ = procNVMLShutdown.Call()
		return fmt.Errorf("NVIDIA NVML did not expose a GPU")
	}
	var device uintptr
	if !nvmlCallOK(procNVMLDeviceGetHandleByIndex, 0, uintptr(unsafe.Pointer(&device))) || device == 0 {
		_, _, _ = procNVMLShutdown.Call()
		return fmt.Errorf("NVIDIA NVML did not return a device handle")
	}
	provider.initialized, provider.device = true, device
	return nil
}

func (provider *NVMLProvider) Snapshot() SurgeGPUTelemetry {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	telemetry := SurgeGPUTelemetry{Provider: "NVIDIA NVML"}
	if err := provider.initialize(); err != nil {
		telemetry.Error = err.Error()
		return telemetry
	}
	telemetry.Ready = true
	device := provider.device

	var name [96]byte
	if nvmlCallOK(procNVMLDeviceGetName, device, uintptr(unsafe.Pointer(&name[0])), uintptr(len(name))) {
		if end := strings.IndexByte(string(name[:]), 0); end >= 0 {
			telemetry.Name = string(name[:end])
		}
	}
	var utilization nvmlUtilization
	if nvmlCallOK(procNVMLDeviceGetUtilization, device, uintptr(unsafe.Pointer(&utilization))) {
		telemetry.GPUUtil, telemetry.MemoryUtil = float64(utilization.GPU), float64(utilization.Memory)
	}
	var value uint32
	if nvmlCallOK(procNVMLDeviceGetTemperature, device, nvmlTemperatureGPU, uintptr(unsafe.Pointer(&value))) {
		telemetry.TemperatureC = float64(value)
	}
	if nvmlCallOK(procNVMLDeviceGetPowerUsage, device, uintptr(unsafe.Pointer(&value))) {
		telemetry.PowerW = float64(value) / 1000
	}
	if nvmlCallOK(procNVMLDeviceGetPowerLimit, device, uintptr(unsafe.Pointer(&value))) {
		telemetry.PowerLimitW = float64(value) / 1000
	}
	if nvmlCallOK(procNVMLDeviceGetDefaultPowerLimit, device, uintptr(unsafe.Pointer(&value))) {
		telemetry.DefaultPowerLimitW = float64(value) / 1000
	}
	var minimum, maximum uint32
	if nvmlCallOK(procNVMLDeviceGetPowerConstraints, device, uintptr(unsafe.Pointer(&minimum)), uintptr(unsafe.Pointer(&maximum))) {
		telemetry.MinPowerLimitW, telemetry.MaxPowerLimitW = float64(minimum)/1000, float64(maximum)/1000
	}
	if nvmlCallOK(procNVMLDeviceGetClock, device, nvmlClockGraphics, uintptr(unsafe.Pointer(&value))) {
		telemetry.GraphicsClockMHz = value
	}
	if nvmlCallOK(procNVMLDeviceGetMaxClock, device, nvmlClockGraphics, uintptr(unsafe.Pointer(&value))) {
		telemetry.MaxGraphicsClockMHz = value
	}
	if nvmlCallOK(procNVMLDeviceGetClock, device, nvmlClockMemory, uintptr(unsafe.Pointer(&value))) {
		telemetry.MemoryClockMHz = value
	}
	if nvmlCallOK(procNVMLDeviceGetMaxClock, device, nvmlClockMemory, uintptr(unsafe.Pointer(&value))) {
		telemetry.MaxMemoryClockMHz = value
	}
	if nvmlCallOK(procNVMLDeviceGetPState, device, uintptr(unsafe.Pointer(&value))) {
		telemetry.PState = value
	}
	var reasons uint64
	if nvmlCallOK(procNVMLDeviceGetThrottleReasons, device, uintptr(unsafe.Pointer(&reasons))) {
		telemetry.ThrottleReasons = reasons
		telemetry.ThrottleSummary = describeNVMLReasons(reasons)
	}
	return telemetry
}

func readNVMLClockDomainLegacy(device uintptr, get, getRange *syscall.LazyProc, name string) NVMLClockDomainCapability {
	domain := NVMLClockDomainCapability{}
	if err := get.Find(); err != nil {
		domain.Error = name + " offset API is unavailable in this driver"
		return domain
	}
	if err := getRange.Find(); err != nil {
		domain.Error = name + " range API is unavailable in this driver"
		return domain
	}
	var current, minimum, maximum int32
	if result := nvmlResult(get, device, uintptr(unsafe.Pointer(&current))); result != nvmlSuccess {
		domain.Error = nvmlResultError("read "+name+" offset", result).Error()
		return domain
	}
	if result := nvmlResult(getRange, device, uintptr(unsafe.Pointer(&minimum)), uintptr(unsafe.Pointer(&maximum))); result != nvmlSuccess {
		domain.Error = nvmlResultError("read "+name+" offset range", result).Error()
		return domain
	}
	if minimum >= maximum || current < minimum || current > maximum {
		domain.Error = name + " driver range is internally inconsistent"
		return domain
	}
	domain.Supported, domain.Current, domain.Min, domain.Max = true, current, minimum, maximum
	domain.API = "legacy NVML VF-offset API"
	return domain
}

func readNVMLClockDomainV1(device uintptr, clockType uint32, name string) NVMLClockDomainCapability {
	domain := NVMLClockDomainCapability{}
	if err := procNVMLDeviceGetClockOffsets.Find(); err != nil {
		domain.Error = "the current NVIDIA driver does not contain the unified clock-offset query"
		return domain
	}
	if err := procNVMLDeviceSetClockOffsets.Find(); err != nil {
		domain.Error = "the current NVIDIA driver does not contain the unified clock-offset write"
		return domain
	}
	// P0 is the full-performance state used for the gaming offset. Do not scan
	// or alter unrelated low-power P-states merely to make a control appear.
	info := nvmlClockOffsetV1{Version: nvmlClockOffsetV1Version, Type: clockType, PState: 0}
	if result := nvmlResult(procNVMLDeviceGetClockOffsets, device, uintptr(unsafe.Pointer(&info))); result != nvmlSuccess {
		domain.Error = nvmlResultError("read P0 "+name+" offset and range", result).Error()
		return domain
	}
	if info.MinClockOffsetMHz >= info.MaxClockOffsetMHz || info.ClockOffsetMHz < info.MinClockOffsetMHz || info.ClockOffsetMHz > info.MaxClockOffsetMHz {
		domain.Error = name + " unified driver range is internally inconsistent"
		return domain
	}
	domain.Supported = true
	domain.Current, domain.Min, domain.Max = info.ClockOffsetMHz, info.MinClockOffsetMHz, info.MaxClockOffsetMHz
	domain.PState, domain.API = info.PState, "nvmlDeviceGet/SetClockOffsets"
	return domain
}

func readNVMLClockDomain(device uintptr, clockType uint32, legacyGet, legacyRange *syscall.LazyProc, name string) NVMLClockDomainCapability {
	modern := readNVMLClockDomainV1(device, clockType, name)
	if modern.Supported {
		return modern
	}
	legacy := readNVMLClockDomainLegacy(device, legacyGet, legacyRange, name)
	if legacy.Supported {
		return legacy
	}
	if modern.Error != "" && legacy.Error != "" {
		legacy.Error = modern.Error + "; compatibility fallback: " + legacy.Error
	}
	return legacy
}

func readNVMLPowerCapability(device uintptr) NVMLPowerCapability {
	domain := NVMLPowerCapability{}
	if err := procNVMLDeviceSetPowerLimit.Find(); err != nil {
		domain.Error = "power-limit write API is unavailable in this driver"
		return domain
	}
	if result := nvmlResult(procNVMLDeviceGetPowerLimit, device, uintptr(unsafe.Pointer(&domain.Current))); result != nvmlSuccess {
		domain.Error = nvmlResultError("read power limit", result).Error()
		return domain
	}
	if result := nvmlResult(procNVMLDeviceGetDefaultPowerLimit, device, uintptr(unsafe.Pointer(&domain.Default))); result != nvmlSuccess {
		domain.Error = nvmlResultError("read default power limit", result).Error()
		return domain
	}
	if result := nvmlResult(procNVMLDeviceGetPowerConstraints, device, uintptr(unsafe.Pointer(&domain.Min)), uintptr(unsafe.Pointer(&domain.Max))); result != nvmlSuccess {
		domain.Error = nvmlResultError("read power-limit range", result).Error()
		return domain
	}
	if domain.Min > domain.Max || domain.Current < domain.Min || domain.Current > domain.Max || domain.Default < domain.Min || domain.Default > domain.Max {
		domain.Error = "power-limit driver range is internally inconsistent"
		return domain
	}
	domain.Supported = true
	return domain
}

func (provider *NVMLProvider) ClockCapability() NVMLClockCapability {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	capability := NVMLClockCapability{RequiresElevation: true}
	if err := provider.initialize(); err != nil {
		capability.Error = err.Error()
		return capability
	}
	var name [96]byte
	if nvmlCallOK(procNVMLDeviceGetName, provider.device, uintptr(unsafe.Pointer(&name[0])), uintptr(len(name))) {
		capability.Device = strings.TrimRight(string(name[:]), "\x00")
	}
	capability.Core = readNVMLClockDomain(provider.device, nvmlClockGraphics, procNVMLDeviceGetGPCOffset, procNVMLDeviceGetGPCOffsetRange, "graphics")
	capability.Memory = readNVMLClockDomain(provider.device, nvmlClockMemory, procNVMLDeviceGetMemoryOffset, procNVMLDeviceGetMemoryOffsetRange, "video-memory")
	capability.Power = readNVMLPowerCapability(provider.device)
	capability.Ready = capability.Core.Supported || capability.Memory.Supported || capability.Power.Supported
	if !capability.Ready {
		capability.Error = capability.Core.Error
		if capability.Error == "" {
			capability.Error = capability.Memory.Error
		}
		if capability.Error == "" {
			capability.Error = capability.Power.Error
		}
	}
	return capability
}

func validateNVMLClockOffset(domain NVMLClockDomainCapability, value int32) error {
	if !domain.Supported {
		return fmt.Errorf("clock domain is not supported")
	}
	if value < domain.Min || value > domain.Max {
		return fmt.Errorf("offset %d MHz is outside the driver range %d..%d MHz", value, domain.Min, domain.Max)
	}
	return nil
}

func validateNVMLPowerLimit(domain NVMLPowerCapability, value uint32) error {
	if !domain.Supported {
		return fmt.Errorf("power-limit control is not supported")
	}
	if value < domain.Min || value > domain.Max {
		return fmt.Errorf("power limit %.1f W is outside the driver range %.1f..%.1f W", float64(value)/1000, float64(domain.Min)/1000, float64(domain.Max)/1000)
	}
	return nil
}

func setNVMLClockOffset(device uintptr, proc *syscall.LazyProc, name string, value int32) error {
	if err := proc.Find(); err != nil {
		return fmt.Errorf("%s write API is unavailable", name)
	}
	result := nvmlResult(proc, device, uintptr(uint32(value)))
	if result != nvmlSuccess {
		return nvmlResultError("set "+name+" offset", result)
	}
	return nil
}

func setNVMLClockDomain(device uintptr, domain NVMLClockDomainCapability, legacyProc *syscall.LazyProc, clockType uint32, name string, value int32) error {
	if domain.API != "nvmlDeviceGet/SetClockOffsets" {
		return setNVMLClockOffset(device, legacyProc, name, value)
	}
	info := nvmlClockOffsetV1{
		Version: nvmlClockOffsetV1Version,
		Type:    clockType, PState: domain.PState,
		ClockOffsetMHz: value,
	}
	result := nvmlResult(procNVMLDeviceSetClockOffsets, device, uintptr(unsafe.Pointer(&info)))
	if result != nvmlSuccess {
		return nvmlResultError("set P0 "+name+" offset", result)
	}
	return nil
}

func setNVMLPowerLimit(device uintptr, value uint32) error {
	if err := procNVMLDeviceSetPowerLimit.Find(); err != nil {
		return fmt.Errorf("power-limit write API is unavailable")
	}
	result := nvmlResult(procNVMLDeviceSetPowerLimit, device, uintptr(value))
	if result != nvmlSuccess {
		return nvmlResultError("set power limit", result)
	}
	return nil
}

// SetClockOffsets is the only NVIDIA clock mutation boundary in Kerneon. The
// caller must already have written an exact rollback snapshot. Each requested
// value is checked against the range returned by this same driver session.
func (provider *NVMLProvider) SetControls(core, memory *int32, powerLimit *uint32) error {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if err := provider.initialize(); err != nil {
		return err
	}
	var currentCore, currentMemory NVMLClockDomainCapability
	var currentPower NVMLPowerCapability
	if core != nil {
		currentCore = readNVMLClockDomain(provider.device, nvmlClockGraphics, procNVMLDeviceGetGPCOffset, procNVMLDeviceGetGPCOffsetRange, "graphics")
		if err := validateNVMLClockOffset(currentCore, *core); err != nil {
			return fmt.Errorf("graphics clock: %w", err)
		}
	}
	if memory != nil {
		currentMemory = readNVMLClockDomain(provider.device, nvmlClockMemory, procNVMLDeviceGetMemoryOffset, procNVMLDeviceGetMemoryOffsetRange, "video-memory")
		if err := validateNVMLClockOffset(currentMemory, *memory); err != nil {
			return fmt.Errorf("video-memory clock: %w", err)
		}
	}
	if powerLimit != nil {
		currentPower = readNVMLPowerCapability(provider.device)
		if err := validateNVMLPowerLimit(currentPower, *powerLimit); err != nil {
			return err
		}
	}
	powerChanged := false
	if powerLimit != nil && *powerLimit != currentPower.Current {
		if err := setNVMLPowerLimit(provider.device, *powerLimit); err != nil {
			return err
		}
		powerChanged = true
	}
	coreChanged := false
	if core != nil && *core != currentCore.Current {
		if err := setNVMLClockDomain(provider.device, currentCore, procNVMLDeviceSetGPCOffset, nvmlClockGraphics, "graphics", *core); err != nil {
			if powerChanged {
				_ = setNVMLPowerLimit(provider.device, currentPower.Current)
			}
			return err
		}
		coreChanged = true
	}
	if memory != nil && *memory != currentMemory.Current {
		if err := setNVMLClockDomain(provider.device, currentMemory, procNVMLDeviceSetMemoryOffset, nvmlClockMemory, "video-memory", *memory); err != nil {
			if coreChanged {
				_ = setNVMLClockDomain(provider.device, currentCore, procNVMLDeviceSetGPCOffset, nvmlClockGraphics, "graphics", currentCore.Current)
			}
			if powerChanged {
				_ = setNVMLPowerLimit(provider.device, currentPower.Current)
			}
			return err
		}
	}
	return nil
}

// SetClockOffsets retains the narrow call used by existing callers while the
// full tuning transaction can also include a driver-bounded power ceiling.
func (provider *NVMLProvider) SetClockOffsets(core, memory *int32) error {
	return provider.SetControls(core, memory, nil)
}

func describeNVMLReasons(reasons uint64) string {
	if reasons == 0 {
		return "No active clock limiter"
	}
	labels := make([]string, 0, 5)
	if reasons&nvmlReasonGPUIdle != 0 {
		labels = append(labels, "idle")
	}
	if reasons&nvmlReasonPowerCap != 0 {
		labels = append(labels, "power cap")
	}
	if reasons&(nvmlReasonSWThermal|nvmlReasonHWthermal) != 0 {
		labels = append(labels, "thermal")
	}
	if reasons&(nvmlReasonHWSlowdown|nvmlReasonPowerBrake) != 0 {
		labels = append(labels, "hardware safeguard")
	}
	if reasons&nvmlReasonAppClocks != 0 {
		labels = append(labels, "application clocks")
	}
	if reasons&nvmlReasonSyncBoost != 0 {
		labels = append(labels, "sync boost")
	}
	if reasons&nvmlReasonDisplay != 0 {
		labels = append(labels, "display clock")
	}
	if len(labels) == 0 {
		return fmt.Sprintf("Driver reason mask 0x%X", reasons)
	}
	return strings.Join(labels, ", ")
}

func (provider *NVMLProvider) Close() {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if provider.initialized {
		_, _, _ = procNVMLShutdown.Call()
	}
	provider.initialized, provider.device = false, 0
}
