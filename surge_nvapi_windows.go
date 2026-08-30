//go:build windows

package main

import (
	"encoding/binary"
	"fmt"
	"sync"
	"syscall"
	"unsafe"
)

const (
	// Interface IDs, structures, and enum values are taken from NVIDIA's
	// public MIT-licensed NVAPI SDK revision cd6918f60b3c9a0476fdfe7e89bb32330602049d.
	// Kerneon dynamically queries the driver runtime and does not ship nvapi64.lib.
	nvapiOK                    = int32(0)
	nvapiExecutableNotFound    = int32(-166)
	nvapiSettingNotFound       = int32(-160)
	nvapiIDInitialize          = uint32(0x0150e828)
	nvapiIDUnload              = uint32(0xd22bdd7e)
	nvapiIDGetErrorMessage     = uint32(0x6c2d048c)
	nvapiIDDRSCreateSession    = uint32(0x0694d52e)
	nvapiIDDRSDestroySession   = uint32(0xdad9cff8)
	nvapiIDDRSLoadSettings     = uint32(0x375dbd6b)
	nvapiIDDRSSaveSettings     = uint32(0xfcbc7e14)
	nvapiIDDRSGetGlobalProfile = uint32(0x617bff9f)
	nvapiIDDRSGetBaseProfile   = uint32(0xda8466a0)
	nvapiIDDRSFindApplication  = uint32(0xeee566b2)
	nvapiIDDRSGetSetting       = uint32(0x73bf8338)
	nvapiIDDRSSetSetting       = uint32(0x577dd202)
	nvapiIDDRSDeleteSetting    = uint32(0xe4a26362)
	nvidiaPreferredPStateID    = uint32(0x1057eb71)
	nvidiaPreferMaximum        = uint32(0x00000001)
	nvidiaFrameRateLimitID     = uint32(0x10835002)
	nvapiUnicodeStringMax      = 2048
	nvapiBinaryDataMax         = 4096
	nvapiDRSApplicationVersion = 4
	nvapiDRSSettingVersion     = 1
)

var (
	nvapiDLL             = syscall.NewLazyDLL("nvapi64.dll")
	procNVQueryInterface = nvapiDLL.NewProc("nvapi_QueryInterface")
)

type nvDRSApplication struct {
	Version, IsPredefined               uint32
	AppName, UserFriendlyName, Launcher [nvapiUnicodeStringMax]uint16
	FileInFolder                        [nvapiUnicodeStringMax]uint16
	Flags                               uint32
	CommandLine                         [nvapiUnicodeStringMax]uint16
}

type nvDRSSetting struct {
	Version             uint32
	SettingName         [nvapiUnicodeStringMax]uint16
	SettingID           uint32
	SettingType         uint32
	SettingLocation     uint32
	IsCurrentPredefined uint32
	IsPredefinedValid   uint32
	PredefinedValue     [nvapiBinaryDataMax + 4]byte
	CurrentValue        [nvapiBinaryDataMax + 4]byte
}

type SurgeNVIDIAProfile struct {
	Ready, Found, Inherited bool
	Provider, Error         string
	PowerPolicy             uint32
	PowerPolicyName         string
	Location                string
}

// NVAPISettingSnapshot is deliberately small and serializable so it can live
// in the crash-recovery journal before a driver profile is changed.
type NVAPISettingSnapshot struct {
	ExecutablePath string `json:"executable_path"`
	SettingID      uint32 `json:"setting_id"`
	PreviousValue  uint32 `json:"previous_value"`
	Explicit       bool   `json:"explicit"`
}

type NVAPIProvider struct {
	mu          sync.Mutex
	initialized bool
	functions   map[uint32]uintptr
}

func nvapiVersion(size uintptr, version uint32) uint32 {
	return uint32(size) | version<<16
}

func nvapiStatusCall(function uintptr, args ...uintptr) int32 {
	result, _, _ := syscall.SyscallN(function, args...)
	return int32(result)
}

func (provider *NVAPIProvider) function(id uint32) uintptr {
	if provider.functions != nil {
		if function := provider.functions[id]; function != 0 {
			return function
		}
	}
	function, _, _ := procNVQueryInterface.Call(uintptr(id))
	if provider.functions == nil {
		provider.functions = make(map[uint32]uintptr)
	}
	provider.functions[id] = function
	return function
}

func (provider *NVAPIProvider) initialize() error {
	if provider.initialized {
		return nil
	}
	if err := nvapiDLL.Load(); err != nil {
		return fmt.Errorf("load NVIDIA NVAPI: %w", err)
	}
	initialize := provider.function(nvapiIDInitialize)
	if initialize == 0 {
		return fmt.Errorf("NVIDIA NVAPI initialization entry point is unavailable")
	}
	if status := nvapiStatusCall(initialize); status != nvapiOK {
		return fmt.Errorf("NVIDIA NVAPI initialization failed: %s", provider.errorMessage(status))
	}
	provider.initialized = true
	return nil
}

func (provider *NVAPIProvider) errorMessage(status int32) string {
	function := provider.function(nvapiIDGetErrorMessage)
	if function == 0 {
		return fmt.Sprintf("status %d", status)
	}
	var message [64]byte
	if nvapiStatusCall(function, uintptr(status), uintptr(unsafe.Pointer(&message[0]))) != nvapiOK {
		return fmt.Sprintf("status %d", status)
	}
	for index, value := range message {
		if value == 0 {
			return string(message[:index])
		}
	}
	return string(message[:])
}

func nvapiUnicode(value string) [nvapiUnicodeStringMax]uint16 {
	encoded := syscall.StringToUTF16(value)
	var destination [nvapiUnicodeStringMax]uint16
	if len(encoded) > len(destination) {
		encoded = encoded[:len(destination)]
		encoded[len(encoded)-1] = 0
	}
	copy(destination[:], encoded)
	return destination
}

func nvidiaPowerPolicyName(value uint32) string {
	switch value {
	case 0:
		return "Adaptive"
	case 1:
		return "Prefer maximum performance"
	case 2:
		return "Driver controlled"
	case 3:
		return "Prefer consistent performance"
	case 4:
		return "Prefer minimum power"
	case 5:
		return "Optimal power"
	default:
		return fmt.Sprintf("Driver value %d", value)
	}
}

func nvidiaSettingLocation(value uint32) string {
	switch value {
	case 0:
		return "application profile"
	case 1:
		return "global profile"
	case 2:
		return "base profile"
	case 3:
		return "driver default"
	default:
		return "unknown profile layer"
	}
}

func (provider *NVAPIProvider) QueryPowerPolicy(executablePath string) SurgeNVIDIAProfile {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	snapshot := SurgeNVIDIAProfile{Provider: "NVIDIA NVAPI DRS"}
	if err := provider.initialize(); err != nil {
		snapshot.Error = err.Error()
		return snapshot
	}
	create, destroy := provider.function(nvapiIDDRSCreateSession), provider.function(nvapiIDDRSDestroySession)
	load, global := provider.function(nvapiIDDRSLoadSettings), provider.function(nvapiIDDRSGetGlobalProfile)
	base := provider.function(nvapiIDDRSGetBaseProfile)
	find, get := provider.function(nvapiIDDRSFindApplication), provider.function(nvapiIDDRSGetSetting)
	if create == 0 || destroy == 0 || load == 0 || global == 0 || base == 0 || get == 0 {
		snapshot.Error = "NVIDIA driver does not expose the required public DRS functions"
		return snapshot
	}
	var session uintptr
	if status := nvapiStatusCall(create, uintptr(unsafe.Pointer(&session))); status != nvapiOK || session == 0 {
		snapshot.Error = "Create DRS session: " + provider.errorMessage(status)
		return snapshot
	}
	defer nvapiStatusCall(destroy, session)
	if status := nvapiStatusCall(load, session); status != nvapiOK {
		snapshot.Error = "Load DRS settings: " + provider.errorMessage(status)
		return snapshot
	}
	var profile uintptr
	if executablePath != "" {
		if find == 0 {
			snapshot.Error = "NVIDIA driver does not expose application-profile lookup"
			return snapshot
		}
		application := nvDRSApplication{Version: nvapiVersion(unsafe.Sizeof(nvDRSApplication{}), nvapiDRSApplicationVersion)}
		name := nvapiUnicode(executablePath)
		status := nvapiStatusCall(find, session, uintptr(unsafe.Pointer(&name[0])), uintptr(unsafe.Pointer(&profile)), uintptr(unsafe.Pointer(&application)))
		if status == nvapiExecutableNotFound {
			snapshot.Ready = true
			snapshot.Error = "No NVIDIA application profile exists for this executable yet"
			return snapshot
		}
		if status != nvapiOK {
			snapshot.Error = "Find application profile: " + provider.errorMessage(status)
			return snapshot
		}
		snapshot.Found = true
	} else {
		status := nvapiStatusCall(global, session, uintptr(unsafe.Pointer(&profile)))
		if status != nvapiOK || profile == 0 {
			snapshot.Error = "Read current global profile: " + provider.errorMessage(status)
			return snapshot
		}
		snapshot.Found = true
	}
	readSetting := func(handle uintptr) (nvDRSSetting, int32) {
		setting := nvDRSSetting{Version: nvapiVersion(unsafe.Sizeof(nvDRSSetting{}), nvapiDRSSettingVersion)}
		status := nvapiStatusCall(get, session, handle, uintptr(nvidiaPreferredPStateID), uintptr(unsafe.Pointer(&setting)))
		return setting, status
	}
	setting, status := readSetting(profile)
	locationOverride := ""
	if status == nvapiSettingNotFound && executablePath != "" {
		var inherited uintptr
		if globalStatus := nvapiStatusCall(global, session, uintptr(unsafe.Pointer(&inherited))); globalStatus == nvapiOK && inherited != 0 {
			setting, status = readSetting(inherited)
			locationOverride = "global profile"
		}
	}
	if status == nvapiSettingNotFound {
		var inherited uintptr
		if baseStatus := nvapiStatusCall(base, session, uintptr(unsafe.Pointer(&inherited))); baseStatus == nvapiOK && inherited != 0 {
			setting, status = readSetting(inherited)
			locationOverride = "base profile"
		}
	}
	if status == nvapiSettingNotFound {
		snapshot.Ready = true
		snapshot.Inherited = true
		snapshot.PowerPolicyName = "Driver default (no explicit override)"
		snapshot.Location = "driver default"
		return snapshot
	}
	if status != nvapiOK {
		snapshot.Error = "Read NVIDIA power policy: " + provider.errorMessage(status)
		return snapshot
	}
	snapshot.Ready = true
	snapshot.PowerPolicy = binary.LittleEndian.Uint32(setting.CurrentValue[:4])
	snapshot.PowerPolicyName = nvidiaPowerPolicyName(snapshot.PowerPolicy)
	snapshot.Location = nvidiaSettingLocation(setting.SettingLocation)
	if locationOverride != "" {
		snapshot.Location = locationOverride
	}
	snapshot.Inherited = setting.SettingLocation != 0
	return snapshot
}

func (provider *NVAPIProvider) applicationDWORDSnapshot(executablePath string, settingID uint32) (NVAPISettingSnapshot, error) {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	result := NVAPISettingSnapshot{ExecutablePath: executablePath, SettingID: settingID}
	if executablePath == "" {
		return result, fmt.Errorf("an application path is required")
	}
	if err := provider.initialize(); err != nil {
		return result, err
	}
	create, destroy := provider.function(nvapiIDDRSCreateSession), provider.function(nvapiIDDRSDestroySession)
	load, find, get := provider.function(nvapiIDDRSLoadSettings), provider.function(nvapiIDDRSFindApplication), provider.function(nvapiIDDRSGetSetting)
	if create == 0 || destroy == 0 || load == 0 || find == 0 || get == 0 {
		return result, fmt.Errorf("NVIDIA driver does not expose the required public DRS read functions")
	}
	var session, profile uintptr
	if status := nvapiStatusCall(create, uintptr(unsafe.Pointer(&session))); status != nvapiOK || session == 0 {
		return result, fmt.Errorf("create DRS session: %s", provider.errorMessage(status))
	}
	defer nvapiStatusCall(destroy, session)
	if status := nvapiStatusCall(load, session); status != nvapiOK {
		return result, fmt.Errorf("load DRS settings: %s", provider.errorMessage(status))
	}
	application := nvDRSApplication{Version: nvapiVersion(unsafe.Sizeof(nvDRSApplication{}), nvapiDRSApplicationVersion)}
	name := nvapiUnicode(executablePath)
	if status := nvapiStatusCall(find, session, uintptr(unsafe.Pointer(&name[0])), uintptr(unsafe.Pointer(&profile)), uintptr(unsafe.Pointer(&application))); status != nvapiOK || profile == 0 {
		return result, fmt.Errorf("find NVIDIA application profile: %s", provider.errorMessage(status))
	}
	setting := nvDRSSetting{Version: nvapiVersion(unsafe.Sizeof(nvDRSSetting{}), nvapiDRSSettingVersion)}
	status := nvapiStatusCall(get, session, profile, uintptr(settingID), uintptr(unsafe.Pointer(&setting)))
	if status == nvapiSettingNotFound {
		return result, nil
	}
	if status != nvapiOK {
		return result, fmt.Errorf("read NVIDIA profile setting 0x%08X: %s", settingID, provider.errorMessage(status))
	}
	result.PreviousValue = binary.LittleEndian.Uint32(setting.CurrentValue[:4])
	result.Explicit = setting.SettingLocation == 0
	return result, nil
}

func (provider *NVAPIProvider) applyApplicationDWORD(snapshot NVAPISettingSnapshot, value uint32) error {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	return provider.writeApplicationDWORD(snapshot.ExecutablePath, snapshot.SettingID, value, false)
}

func (provider *NVAPIProvider) restoreApplicationDWORD(snapshot NVAPISettingSnapshot) error {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	return provider.writeApplicationDWORD(snapshot.ExecutablePath, snapshot.SettingID, snapshot.PreviousValue, !snapshot.Explicit)
}

func (provider *NVAPIProvider) writeApplicationDWORD(executablePath string, settingID, value uint32, remove bool) error {
	if executablePath == "" {
		return fmt.Errorf("an application path is required")
	}
	if err := provider.initialize(); err != nil {
		return err
	}
	create, destroy := provider.function(nvapiIDDRSCreateSession), provider.function(nvapiIDDRSDestroySession)
	load, save := provider.function(nvapiIDDRSLoadSettings), provider.function(nvapiIDDRSSaveSettings)
	find := provider.function(nvapiIDDRSFindApplication)
	set, deleteSetting := provider.function(nvapiIDDRSSetSetting), provider.function(nvapiIDDRSDeleteSetting)
	if create == 0 || destroy == 0 || load == 0 || save == 0 || find == 0 || set == 0 || deleteSetting == 0 {
		return fmt.Errorf("NVIDIA driver does not expose the required public DRS write functions")
	}
	var session, profile uintptr
	if status := nvapiStatusCall(create, uintptr(unsafe.Pointer(&session))); status != nvapiOK || session == 0 {
		return fmt.Errorf("create DRS session: %s", provider.errorMessage(status))
	}
	defer nvapiStatusCall(destroy, session)
	if status := nvapiStatusCall(load, session); status != nvapiOK {
		return fmt.Errorf("load DRS settings: %s", provider.errorMessage(status))
	}
	application := nvDRSApplication{Version: nvapiVersion(unsafe.Sizeof(nvDRSApplication{}), nvapiDRSApplicationVersion)}
	name := nvapiUnicode(executablePath)
	if status := nvapiStatusCall(find, session, uintptr(unsafe.Pointer(&name[0])), uintptr(unsafe.Pointer(&profile)), uintptr(unsafe.Pointer(&application))); status != nvapiOK || profile == 0 {
		return fmt.Errorf("find NVIDIA application profile: %s", provider.errorMessage(status))
	}
	if remove {
		status := nvapiStatusCall(deleteSetting, session, profile, uintptr(settingID))
		if status != nvapiOK && status != nvapiSettingNotFound {
			return fmt.Errorf("remove NVIDIA profile setting 0x%08X: %s", settingID, provider.errorMessage(status))
		}
	} else {
		setting := nvDRSSetting{
			Version:   nvapiVersion(unsafe.Sizeof(nvDRSSetting{}), nvapiDRSSettingVersion),
			SettingID: settingID, SettingType: 0,
		}
		binary.LittleEndian.PutUint32(setting.CurrentValue[:4], value)
		if status := nvapiStatusCall(set, session, profile, uintptr(unsafe.Pointer(&setting))); status != nvapiOK {
			return fmt.Errorf("write NVIDIA profile setting 0x%08X: %s", settingID, provider.errorMessage(status))
		}
	}
	if status := nvapiStatusCall(save, session); status != nvapiOK {
		return fmt.Errorf("save NVIDIA application profile: %s", provider.errorMessage(status))
	}
	return nil
}

func (provider *NVAPIProvider) Close() {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if provider.initialized {
		if unload := provider.function(nvapiIDUnload); unload != 0 {
			_ = nvapiStatusCall(unload)
		}
	}
	provider.initialized = false
	provider.functions = nil
}
