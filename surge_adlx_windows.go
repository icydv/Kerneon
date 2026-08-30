//go:build windows

package main

import (
	"fmt"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	adlxOK                 = 0
	adlxAlreadyEnabled     = 1
	adlxAlreadyInitialized = 2
	adlxResetNeeded        = 18
	adlxSDKFullVersion     = uint64(1)<<48 | uint64(5)<<32 | 124
)

type ADLXGPUCapability struct {
	Ready, AtFactory                           bool
	AutoTuning, GPUClock, VRAMClock            bool
	ManualGPUClock, ManualVRAMClock, Undervolt bool
	Core, VRAM                                 ADLXClockCapability
	Device, Provider, Initialization           string
	Error                                      string
}

type ADLXClockCapability struct {
	Supported                  bool
	Current, Default, Min, Max int32
	Step                       int32
	API, Error                 string
}

type ADLXTelemetry struct {
	Ready                                 bool
	Usage, TemperatureC, HotspotC, PowerW float64
	CoreClockMHz, VRAMClockMHz, VoltageMV int32
	Error                                 string
}

type adlxIntRange struct {
	Min, Max, Step int32
}

type ADLXProvider struct {
	mu                                           sync.Mutex
	dll                                          *syscall.LazyDLL
	terminate                                    *syscall.LazyProc
	initialized                                  bool
	system, gpuList, gpu, tuning, autoTune       uintptr
	manualGFX, manualVRAM, performance           uintptr
	manualGFXV21, manualVRAMV21, metricsTracking bool
}

func adlxSucceeded(result uintptr) bool {
	value := uint32(result)
	return value == adlxOK || value == adlxAlreadyEnabled || value == adlxAlreadyInitialized
}

func adlxResultError(operation string, result uintptr) error {
	labels := map[uint32]string{
		3: "unspecified driver failure", 4: "invalid arguments", 5: "SDK/driver version mismatch",
		6: "unknown interface", 7: "provider terminated", 8: "ADL initialization failed",
		9: "item not found", 10: "invalid provider object", 11: "provider objects still active",
		12: "feature not supported", 13: "another tuning operation is pending", 14: "GPU is inactive",
		15: "GPU is currently in use", 16: "operation timed out", 17: "feature is not active",
		18: "factory reset is required before this operation",
	}
	value := uint32(result)
	return fmt.Errorf("%s: ADLX result %d (%s)", operation, value, fallback(labels[value], "unknown driver result"))
}

func adlxMethod(object uintptr, index uintptr) uintptr {
	if object == 0 {
		return 0
	}
	vtable := *(*uintptr)(unsafe.Pointer(object))
	if vtable == 0 {
		return 0
	}
	return *(*uintptr)(unsafe.Pointer(vtable + index*unsafe.Sizeof(uintptr(0))))
}

func adlxCall(object uintptr, index uintptr, args ...uintptr) (uintptr, error) {
	method := adlxMethod(object, index)
	if method == 0 {
		return 0, fmt.Errorf("ADLX method %d is unavailable", index)
	}
	callArgs := make([]uintptr, 1, len(args)+1)
	callArgs[0] = object
	callArgs = append(callArgs, args...)
	result, _, _ := syscall.SyscallN(method, callArgs...)
	return result, nil
}

func adlxRelease(object uintptr) {
	if object != 0 {
		_, _ = adlxCall(object, 1)
	}
}

func adlxCString(pointer uintptr, maximum int) string {
	if pointer == 0 || maximum <= 0 {
		return ""
	}
	bytes := unsafe.Slice((*byte)(unsafe.Pointer(pointer)), maximum)
	for index, value := range bytes {
		if value == 0 {
			return string(bytes[:index])
		}
	}
	return string(bytes)
}

func (provider *ADLXProvider) initialize() error {
	if provider.initialized && provider.system != 0 {
		return nil
	}
	provider.dll = syscall.NewLazyDLL("amdadlx64.dll")
	if err := provider.dll.Load(); err != nil {
		return fmt.Errorf("load AMD ADLX: %w", err)
	}
	provider.terminate = provider.dll.NewProc("ADLXTerminate")
	if err := provider.terminate.Find(); err != nil {
		return fmt.Errorf("AMD ADLX terminate export is unavailable")
	}
	initializers := []struct {
		name, label string
	}{
		{"ADLXInitialize", "current-driver initialization"},
		{"ADLXInitializeWithIncompatibleDriver", "legacy-driver compatibility initialization"},
	}
	var last error
	for _, initializer := range initializers {
		proc := provider.dll.NewProc(initializer.name)
		if err := proc.Find(); err != nil {
			last = err
			continue
		}
		var system uintptr
		result, _, _ := proc.Call(uintptr(adlxSDKFullVersion), uintptr(unsafe.Pointer(&system)))
		if adlxSucceeded(result) && system != 0 {
			provider.system, provider.initialized = system, true
			return nil
		}
		last = adlxResultError(initializer.label, result)
	}
	if last == nil {
		last = fmt.Errorf("no compatible ADLX initializer was exported")
	}
	return last
}

func (provider *ADLXProvider) discoverObjects() error {
	if err := provider.initialize(); err != nil {
		return err
	}
	if provider.gpu != 0 && provider.tuning != 0 {
		return nil
	}
	var list uintptr
	result, err := adlxCall(provider.system, 1, uintptr(unsafe.Pointer(&list))) // IADLXSystem::GetGPUs
	if err != nil || !adlxSucceeded(result) || list == 0 {
		return adlxResultError("enumerate Radeon GPUs", result)
	}
	provider.gpuList = list
	size, err := adlxCall(list, 3) // IADLXList::Size
	if err != nil || size == 0 {
		return fmt.Errorf("AMD ADLX returned no Radeon GPU")
	}
	var gpu uintptr
	result, err = adlxCall(list, 11, 0, uintptr(unsafe.Pointer(&gpu))) // IADLXGPUList::At
	if err != nil || !adlxSucceeded(result) || gpu == 0 {
		return adlxResultError("open first Radeon GPU", result)
	}
	provider.gpu = gpu
	var tuning uintptr
	result, err = adlxCall(provider.system, 8, uintptr(unsafe.Pointer(&tuning))) // IADLXSystem::GetGPUTuningServices
	if err != nil || !adlxSucceeded(result) || tuning == 0 {
		return adlxResultError("open AMD GPU tuning services", result)
	}
	provider.tuning = tuning
	provider.tryOpenTuningInterfaces()
	var performance uintptr
	if result, _ := adlxCall(provider.system, 9, uintptr(unsafe.Pointer(&performance))); adlxSucceeded(result) && performance != 0 {
		provider.performance = performance
		if result, _ = adlxCall(performance, 11); adlxSucceeded(result) { // StartPerformanceMetricsTracking
			provider.metricsTracking = true
		}
	}
	return nil
}

func adlxQueryInterface(base uintptr, name string) uintptr {
	if base == 0 {
		return 0
	}
	iid, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return 0
	}
	var resultInterface uintptr
	result, err := adlxCall(base, 2, uintptr(unsafe.Pointer(iid)), uintptr(unsafe.Pointer(&resultInterface)))
	if err != nil || !adlxSucceeded(result) {
		return 0
	}
	return resultInterface
}

func (provider *ADLXProvider) openTuningInterface(serviceMethod uintptr, names ...string) (uintptr, string) {
	var base uintptr
	result, err := adlxCall(provider.tuning, serviceMethod, provider.gpu, uintptr(unsafe.Pointer(&base)))
	if err != nil || !adlxSucceeded(result) || base == 0 {
		return 0, ""
	}
	defer adlxRelease(base)
	for _, name := range names {
		if typed := adlxQueryInterface(base, name); typed != 0 {
			return typed, name
		}
	}
	return 0, ""
}

func (provider *ADLXProvider) tryOpenTuningInterfaces() {
	var baseAuto uintptr
	if result, _ := adlxCall(provider.tuning, 12, provider.gpu, uintptr(unsafe.Pointer(&baseAuto))); adlxSucceeded(result) && baseAuto != 0 {
		provider.autoTune = adlxQueryInterface(baseAuto, "IADLXGPUAutoTuning")
		adlxRelease(baseAuto)
	}
	if supported, _ := adlxBoolMethodWithGPU(provider.tuning, 8, provider.gpu); supported {
		provider.manualGFX, _ = provider.openTuningInterface(14, "IADLXManualGraphicsTuning2_1")
		provider.manualGFXV21 = provider.manualGFX != 0
		if provider.manualGFX == 0 {
			provider.manualGFX, _ = provider.openTuningInterface(14, "IADLXManualGraphicsTuning2")
		}
	}
	if supported, _ := adlxBoolMethodWithGPU(provider.tuning, 9, provider.gpu); supported {
		provider.manualVRAM, _ = provider.openTuningInterface(15, "IADLXManualVRAMTuning2_1")
		provider.manualVRAMV21 = provider.manualVRAM != 0
		if provider.manualVRAM == 0 {
			provider.manualVRAM, _ = provider.openTuningInterface(15, "IADLXManualVRAMTuning2")
		}
	}
}

func adlxBoolMethod(object uintptr, index uintptr) (bool, error) {
	var value uint8
	result, err := adlxCall(object, index, uintptr(unsafe.Pointer(&value)))
	if err != nil {
		return false, err
	}
	if !adlxSucceeded(result) {
		return false, adlxResultError("query AMD tuning capability", result)
	}
	return value != 0, nil
}

func adlxReadInt(object uintptr, index uintptr) (int32, error) {
	var value int32
	result, err := adlxCall(object, index, uintptr(unsafe.Pointer(&value)))
	if err != nil {
		return 0, err
	}
	if !adlxSucceeded(result) {
		return 0, adlxResultError("read AMD tuning value", result)
	}
	return value, nil
}

func adlxReadRange(object uintptr, index uintptr) (adlxIntRange, error) {
	var value adlxIntRange
	result, err := adlxCall(object, index, uintptr(unsafe.Pointer(&value)))
	if err != nil {
		return value, err
	}
	if !adlxSucceeded(result) {
		return value, adlxResultError("read AMD tuning range", result)
	}
	if value.Min >= value.Max || value.Step <= 0 {
		return value, fmt.Errorf("AMD returned an inconsistent clock range %d..%d step %d", value.Min, value.Max, value.Step)
	}
	return value, nil
}

func (provider *ADLXProvider) readManualCoreCapability() ADLXClockCapability {
	capability := ADLXClockCapability{API: "AMD ADLX ManualGraphicsTuning2"}
	if provider.manualGFX == 0 {
		capability.Error = "manual GPU-clock interface is not exposed"
		return capability
	}
	rangeValue, err := adlxReadRange(provider.manualGFX, 6) // GetGPUMaxFrequencyRange
	if err != nil {
		capability.Error = err.Error()
		return capability
	}
	current, err := adlxReadInt(provider.manualGFX, 7) // GetGPUMaxFrequency
	if err != nil || current < rangeValue.Min || current > rangeValue.Max {
		capability.Error = fallback(func() string {
			if err != nil {
				return err.Error()
			}
			return "current GPU clock is outside the reported range"
		}(), "invalid current GPU clock")
		return capability
	}
	defaultValue := current
	if provider.manualGFXV21 {
		if value, defaultErr := adlxReadInt(provider.manualGFX, 13); defaultErr == nil { // GetGPUMaxFrequencyDefault
			defaultValue = value
		}
	}
	capability.Supported = true
	capability.Current, capability.Default = current, defaultValue
	capability.Min, capability.Max, capability.Step = rangeValue.Min, rangeValue.Max, rangeValue.Step
	return capability
}

func (provider *ADLXProvider) readManualVRAMCapability() ADLXClockCapability {
	capability := ADLXClockCapability{API: "AMD ADLX ManualVRAMTuning2"}
	if provider.manualVRAM == 0 {
		capability.Error = "manual VRAM-clock interface is not exposed"
		return capability
	}
	rangeValue, err := adlxReadRange(provider.manualVRAM, 7) // GetMaxVRAMFrequencyRange
	if err != nil {
		capability.Error = err.Error()
		return capability
	}
	current, err := adlxReadInt(provider.manualVRAM, 8) // GetMaxVRAMFrequency
	if err != nil || current < rangeValue.Min || current > rangeValue.Max {
		if err != nil {
			capability.Error = err.Error()
		} else {
			capability.Error = "current VRAM clock is outside the reported range"
		}
		return capability
	}
	defaultValue := current
	if provider.manualVRAMV21 {
		if value, defaultErr := adlxReadInt(provider.manualVRAM, 10); defaultErr == nil { // GetMaxVRAMFrequencyDefault
			defaultValue = value
		}
	}
	capability.Supported = true
	capability.Current, capability.Default = current, defaultValue
	capability.Min, capability.Max, capability.Step = rangeValue.Min, rangeValue.Max, rangeValue.Step
	return capability
}

func (provider *ADLXProvider) Capability() ADLXGPUCapability {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	capability := ADLXGPUCapability{Provider: "AMD ADLX clock tuning"}
	if err := provider.discoverObjects(); err != nil {
		capability.Error = err.Error()
		return capability
	}
	var namePointer uintptr
	if result, err := adlxCall(provider.gpu, 7, uintptr(unsafe.Pointer(&namePointer))); err == nil && adlxSucceeded(result) {
		capability.Device = strings.TrimSpace(adlxCString(namePointer, 256))
	}
	capability.AutoTuning, _ = adlxBoolMethodWithGPU(provider.tuning, 6, provider.gpu)
	capability.AtFactory, _ = adlxBoolMethodWithGPU(provider.tuning, 4, provider.gpu)
	if provider.autoTune != 0 {
		capability.Undervolt, _ = adlxBoolMethod(provider.autoTune, 3)
		capability.GPUClock, _ = adlxBoolMethod(provider.autoTune, 4)
		capability.VRAMClock, _ = adlxBoolMethod(provider.autoTune, 5)
	}
	capability.Core = provider.readManualCoreCapability()
	capability.VRAM = provider.readManualVRAMCapability()
	capability.ManualGPUClock, capability.ManualVRAMClock = capability.Core.Supported, capability.VRAM.Supported
	capability.Ready = capability.ManualGPUClock || capability.ManualVRAMClock || (capability.AutoTuning && (capability.GPUClock || capability.VRAMClock))
	if !capability.Ready {
		capability.Error = "The installed Radeon driver did not expose manual or automatic GPU/VRAM clock tuning for this card."
	}
	return capability
}

func adlxBoolMethodWithGPU(object uintptr, index uintptr, gpu uintptr) (bool, error) {
	var value uint8
	result, err := adlxCall(object, index, gpu, uintptr(unsafe.Pointer(&value)))
	if err != nil {
		return false, err
	}
	if !adlxSucceeded(result) {
		return false, adlxResultError("query AMD tuning state", result)
	}
	return value != 0, nil
}

func validateADLXClock(capability ADLXClockCapability, value int32) error {
	if !capability.Supported {
		return fmt.Errorf("clock domain is not supported")
	}
	if value < capability.Min || value > capability.Max {
		return fmt.Errorf("clock %d MHz is outside AMD's range %d..%d MHz", value, capability.Min, capability.Max)
	}
	if capability.Step > 1 && (value-capability.Min)%capability.Step != 0 && value != capability.Current {
		return fmt.Errorf("clock %d MHz does not follow AMD's %d MHz step", value, capability.Step)
	}
	return nil
}

func adlxSetInt(object uintptr, index uintptr, operation string, value int32) error {
	result, err := adlxCall(object, index, uintptr(uint32(value)))
	if err != nil {
		return err
	}
	if !adlxSucceeded(result) {
		return adlxResultError(operation, result)
	}
	return nil
}

func (provider *ADLXProvider) SetManualClocks(coreMHz, vramMHz *int32) error {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if err := provider.discoverObjects(); err != nil {
		return err
	}
	core, memory := provider.readManualCoreCapability(), provider.readManualVRAMCapability()
	if coreMHz != nil {
		if err := validateADLXClock(core, *coreMHz); err != nil {
			return fmt.Errorf("AMD GPU core: %w", err)
		}
	}
	if vramMHz != nil {
		if err := validateADLXClock(memory, *vramMHz); err != nil {
			return fmt.Errorf("AMD VRAM: %w", err)
		}
	}
	coreChanged := false
	if coreMHz != nil && *coreMHz != core.Current {
		if err := adlxSetInt(provider.manualGFX, 8, "set AMD maximum GPU clock", *coreMHz); err != nil {
			return err
		}
		coreChanged = true
	}
	if vramMHz != nil && *vramMHz != memory.Current {
		if err := adlxSetInt(provider.manualVRAM, 9, "set AMD maximum VRAM clock", *vramMHz); err != nil {
			if coreChanged {
				_ = adlxSetInt(provider.manualGFX, 8, "roll back AMD maximum GPU clock", core.Current)
			}
			return err
		}
	}
	return nil
}

func adlxReadDouble(object uintptr, index uintptr) (float64, error) {
	var value float64
	result, err := adlxCall(object, index, uintptr(unsafe.Pointer(&value)))
	if err != nil {
		return 0, err
	}
	if !adlxSucceeded(result) {
		return 0, adlxResultError("read AMD performance metric", result)
	}
	return value, nil
}

func (provider *ADLXProvider) Snapshot() ADLXTelemetry {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	telemetry := ADLXTelemetry{}
	if err := provider.discoverObjects(); err != nil {
		telemetry.Error = err.Error()
		return telemetry
	}
	if provider.performance == 0 {
		telemetry.Error = "AMD performance monitoring is unavailable"
		return telemetry
	}
	var metrics uintptr
	result, err := adlxCall(provider.performance, 18, provider.gpu, uintptr(unsafe.Pointer(&metrics))) // GetCurrentGPUMetrics
	if err != nil || !adlxSucceeded(result) || metrics == 0 {
		telemetry.Error = adlxResultError("read current AMD GPU metrics", result).Error()
		return telemetry
	}
	defer adlxRelease(metrics)
	telemetry.Usage, _ = adlxReadDouble(metrics, 4)
	telemetry.CoreClockMHz, _ = adlxReadInt(metrics, 5)
	telemetry.VRAMClockMHz, _ = adlxReadInt(metrics, 6)
	telemetry.TemperatureC, _ = adlxReadDouble(metrics, 7)
	telemetry.HotspotC, _ = adlxReadDouble(metrics, 8)
	telemetry.PowerW, _ = adlxReadDouble(metrics, 10)
	telemetry.VoltageMV, _ = adlxReadInt(metrics, 13)
	telemetry.Ready = telemetry.TemperatureC > 0 || telemetry.CoreClockMHz > 0
	if !telemetry.Ready {
		telemetry.Error = "AMD returned no live clock or temperature sample"
	}
	return telemetry
}

func (provider *ADLXProvider) startAutoClock(index uintptr, timeout time.Duration) error {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if err := provider.discoverObjects(); err != nil {
		return err
	}
	listener, done := newADLXAutoListener()
	result, err := adlxCall(provider.autoTune, index, uintptr(unsafe.Pointer(listener)))
	if err != nil {
		freeADLXAutoListener(listener)
		return err
	}
	if uint32(result) == adlxResetNeeded {
		freeADLXAutoListener(listener)
		return adlxResultError("start AMD automatic clock tuning", result)
	}
	if !adlxSucceeded(result) {
		freeADLXAutoListener(listener)
		return adlxResultError("start AMD automatic clock tuning", result)
	}
	provider.mu.Unlock()
	select {
	case <-done:
	case <-time.After(timeout):
		provider.mu.Lock()
		freeADLXAutoListener(listener)
		return fmt.Errorf("AMD automatic clock tuning did not finish within %s", timeout.Round(time.Second))
	}
	provider.mu.Lock()
	freeADLXAutoListener(listener)
	return nil
}

func (provider *ADLXProvider) StartGPUClockAutoTune(timeout time.Duration) error {
	return provider.startAutoClock(10, timeout) // IADLXGPUAutoTuning::StartOverclockGPU
}

func (provider *ADLXProvider) StartVRAMClockAutoTune(timeout time.Duration) error {
	return provider.startAutoClock(11, timeout) // IADLXGPUAutoTuning::StartOverclockVRAM
}

func (provider *ADLXProvider) RestoreFactory() error {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if err := provider.discoverObjects(); err != nil {
		return err
	}
	result, err := adlxCall(provider.tuning, 5, provider.gpu)
	if err != nil {
		return err
	}
	if !adlxSucceeded(result) {
		return adlxResultError("restore AMD GPU factory tuning", result)
	}
	return nil
}

func (provider *ADLXProvider) Close() {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if provider.metricsTracking && provider.performance != 0 {
		_, _ = adlxCall(provider.performance, 12) // StopPerformanceMetricsTracking
	}
	adlxRelease(provider.performance)
	adlxRelease(provider.manualVRAM)
	adlxRelease(provider.manualGFX)
	adlxRelease(provider.autoTune)
	adlxRelease(provider.tuning)
	adlxRelease(provider.gpu)
	adlxRelease(provider.gpuList)
	provider.performance, provider.manualVRAM, provider.manualGFX = 0, 0, 0
	provider.autoTune, provider.tuning, provider.gpu, provider.gpuList = 0, 0, 0, 0
	if provider.initialized && provider.terminate != nil {
		_, _, _ = provider.terminate.Call()
	}
	provider.system, provider.initialized, provider.metricsTracking = 0, false, false
}

type adlxAutoListenerVTable struct {
	Acquire, Release, QueryInterface, Complete uintptr
}

type adlxAutoListener struct {
	VTable *adlxAutoListenerVTable
}

var (
	adlxListenerOnce sync.Once
	adlxListenerVT   adlxAutoListenerVTable
	adlxListenerDone sync.Map
)

type adlxListenerRegistration struct {
	listener *adlxAutoListener
	done     chan struct{}
}

func initializeADLXListenerVTable() {
	adlxListenerVT.Acquire = syscall.NewCallback(func(uintptr) uintptr { return 1 })
	adlxListenerVT.Release = syscall.NewCallback(func(uintptr) uintptr { return 1 })
	adlxListenerVT.QueryInterface = syscall.NewCallback(func(_ uintptr, _ uintptr, output uintptr) uintptr {
		if output != 0 {
			*(*uintptr)(unsafe.Pointer(output)) = 0
		}
		return 6 // ADLX_UNKNOWN_INTERFACE
	})
	adlxListenerVT.Complete = syscall.NewCallback(func(listener uintptr, _ uintptr) uintptr {
		if value, ok := adlxListenerDone.Load(listener); ok {
			select {
			case value.(adlxListenerRegistration).done <- struct{}{}:
			default:
			}
		}
		return 1
	})
}

func newADLXAutoListener() (*adlxAutoListener, <-chan struct{}) {
	adlxListenerOnce.Do(initializeADLXListenerVTable)
	listener := &adlxAutoListener{VTable: &adlxListenerVT}
	done := make(chan struct{}, 1)
	adlxListenerDone.Store(uintptr(unsafe.Pointer(listener)), adlxListenerRegistration{listener: listener, done: done})
	return listener, done
}

func freeADLXAutoListener(listener *adlxAutoListener) {
	if listener != nil {
		adlxListenerDone.Delete(uintptr(unsafe.Pointer(listener)))
	}
}
