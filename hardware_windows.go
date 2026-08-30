//go:build windows

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/klauspost/cpuid/v2"
)

type TrustSignal struct {
	Title, Detail, Evidence string
	Level                   int
}

type GPUIdentity struct {
	Name, PNPDeviceID, Vendor, Driver string
	Signed                            bool
}

type HardwarePassport struct {
	Loaded, Loading               bool
	Verdict, VerdictDetail, Error string
	Fingerprint, Drift            string
	CPUBrand, CPUVendor           string
	Family, Model, Stepping       int
	PhysicalCores, LogicalCores   int
	BaseHz, BoostHz               int64
	L1D, L1I, L2, L3, CacheLine   int
	Features                      []string
	Hypervisor                    string
	SecureBoot, SecureBootKnown   bool
	TPMPresent, TPMReady          bool
	Board, BIOS                   string
	GPUs                          []GPUIdentity
	Signals                       []TrustSignal
}

type HardwareState struct {
	mu       sync.RWMutex
	passport HardwarePassport
}

func (a *App) hardwareSnapshot() HardwarePassport {
	a.hardware.mu.RLock()
	defer a.hardware.mu.RUnlock()
	result := a.hardware.passport
	result.Features = append([]string(nil), result.Features...)
	result.GPUs = append([]GPUIdentity(nil), result.GPUs...)
	result.Signals = append([]TrustSignal(nil), result.Signals...)
	return result
}

func (a *App) refreshHardwarePassport() {
	a.hardware.mu.Lock()
	if a.hardware.passport.Loading {
		a.hardware.mu.Unlock()
		return
	}
	a.hardware.passport.Loading = true
	a.hardware.mu.Unlock()
	go func() {
		passport := collectHardwarePassport(a.snapshot.System, a.config.Trust.PinnedFingerprint)
		a.hardware.mu.Lock()
		a.hardware.passport = passport
		a.hardware.mu.Unlock()
		if a.hwnd != 0 {
			procPostMessageW.Call(a.hwnd, WM_APP_RENDER, 0, 0)
		}
	}()
}

func collectHardwarePassport(system SystemData, pinned string) HardwarePassport {
	cpu := cpuid.CPU
	passport := HardwarePassport{
		Loaded: true, CPUBrand: cpu.BrandName, CPUVendor: cpu.VendorString,
		Family: cpu.Family, Model: cpu.Model, Stepping: cpu.Stepping,
		PhysicalCores: cpu.PhysicalCores, LogicalCores: cpu.LogicalCores,
		BaseHz: cpu.Hz, BoostHz: cpu.BoostFreq, L1D: cpu.Cache.L1D, L1I: cpu.Cache.L1I,
		L2: cpu.Cache.L2, L3: cpu.Cache.L3, CacheLine: cpu.CacheLine,
		Features: cpu.FeatureSet(), Board: strings.TrimSpace(system.Manufacturer + " " + system.Model), BIOS: system.BIOS,
	}
	if cpu.VM() {
		passport.Hypervisor = fallback(cpu.HypervisorVendorString, "Hypervisor present")
	}
	passport.SecureBoot, passport.SecureBootKnown, passport.TPMPresent, passport.TPMReady = queryPlatformTrust()
	passport.GPUs = queryGPUIdentities()

	brand, vendor := strings.ToLower(cpu.BrandName), strings.ToLower(cpu.VendorString)
	vendorConsistent := (strings.Contains(vendor, "intel") && strings.Contains(brand, "intel")) ||
		(strings.Contains(vendor, "amd") && strings.Contains(brand, "amd")) || cpu.VM()
	if vendorConsistent {
		passport.Signals = append(passport.Signals, TrustSignal{"CPU identity is internally consistent", "The CPUID vendor, brand, family, model and stepping do not contradict one another.", fmt.Sprintf("%s · family %d model %d stepping %d", cpu.VendorString, cpu.Family, cpu.Model, cpu.Stepping), 0})
	} else {
		passport.Signals = append(passport.Signals, TrustSignal{"CPU identity contradiction", "The raw CPUID vendor and marketing brand do not follow a recognized pairing.", cpu.VendorString + " · " + cpu.BrandName, 2})
	}
	if cpu.VM() {
		passport.Signals = append(passport.Signals, TrustSignal{"Virtualization changes the trust boundary", "A hypervisor can intentionally present a synthetic identity, so physical authenticity cannot be assessed from this guest.", passport.Hypervisor, 1})
	}
	if passport.SecureBootKnown && passport.SecureBoot {
		passport.Signals = append(passport.Signals, TrustSignal{"Secure Boot is active", "Windows reports that the UEFI boot chain is enforcing trusted signatures.", "Confirm-SecureBootUEFI = True", 0})
	} else if passport.SecureBootKnown {
		passport.Signals = append(passport.Signals, TrustSignal{"Secure Boot is disabled", "This is not proof of counterfeit hardware, but it weakens boot-chain provenance.", "Confirm-SecureBootUEFI = False", 1})
	} else {
		passport.Signals = append(passport.Signals, TrustSignal{"Secure Boot could not be verified", "Legacy BIOS, permissions, or platform support prevented an independent result.", "No supported UEFI result", 1})
	}
	if passport.TPMPresent && passport.TPMReady {
		passport.Signals = append(passport.Signals, TrustSignal{"TPM is present and ready", "The platform exposes a ready Trusted Platform Module for future measured-boot attestation.", "TpmPresent=True · TpmReady=True", 0})
	} else {
		passport.Signals = append(passport.Signals, TrustSignal{"TPM attestation is unavailable", "Kerneon cannot anchor this passport to a ready TPM on the current platform.", fmt.Sprintf("present=%t ready=%t", passport.TPMPresent, passport.TPMReady), 1})
	}
	for _, gpu := range passport.GPUs {
		level, title := 0, gpu.Name+" has coherent PCI lineage"
		expected := gpuVendorFromPNP(gpu.PNPDeviceID)
		if expected != "" && !strings.Contains(strings.ToLower(gpu.Vendor+" "+gpu.Name), strings.ToLower(expected)) {
			level, title = 2, gpu.Name+" has a vendor-lineage contradiction"
		} else if expected == "" {
			level, title = 1, gpu.Name+" could not be mapped to a known PCI vendor"
		}
		if !gpu.Signed && level < 1 {
			level = 1
		}
		passport.Signals = append(passport.Signals, TrustSignal{title, "Kerneon cross-checked the Windows name/vendor, PCI hardware ID, driver version, and signature state.", fmt.Sprintf("%s · %s · driver %s · signed=%t", gpu.PNPDeviceID, gpu.Vendor, gpu.Driver, gpu.Signed), level})
	}
	stable := []string{cpu.VendorString, cpu.BrandName, fmt.Sprint(cpu.Family), fmt.Sprint(cpu.Model), fmt.Sprint(cpu.Stepping), passport.Board, passport.BIOS}
	for _, gpu := range passport.GPUs {
		stable = append(stable, gpu.PNPDeviceID)
	}
	hash := sha256.Sum256([]byte(strings.Join(stable, "\x00")))
	passport.Fingerprint = strings.ToUpper(hex.EncodeToString(hash[:12]))
	if pinned == "" {
		passport.Drift = "Not pinned yet"
	} else if strings.EqualFold(pinned, passport.Fingerprint) {
		passport.Drift = "Matches the pinned passport"
		passport.Signals = append(passport.Signals, TrustSignal{"Hardware identity matches the pinned passport", "The stable CPU, board, BIOS, and PCI identity has not drifted since it was pinned.", passport.Fingerprint, 0})
	} else {
		passport.Drift = "Changed since the passport was pinned"
		passport.Signals = append(passport.Signals, TrustSignal{"Hardware identity changed", "A stable CPU, board, BIOS, or GPU identity differs from the pinned passport. Review before trusting the machine.", "current=" + passport.Fingerprint + " pinned=" + pinned, 2})
	}
	contradictions := 0
	for _, signal := range passport.Signals {
		if signal.Level == 2 {
			contradictions++
		}
	}
	if contradictions > 0 {
		passport.Verdict = "Identity contradiction found"
		passport.VerdictDetail = "Independent identity signals disagree. This deserves manual verification; it is not by itself proof of fraud."
	} else if cpu.VM() {
		passport.Verdict = "Consistent virtual identity"
		passport.VerdictDetail = "The guest identity is coherent, but the hypervisor—not the physical hardware—is the trust authority."
	} else {
		passport.Verdict = "No identity contradictions found"
		passport.VerdictDetail = "The available independent signals agree. Sophisticated firmware spoofing still requires vendor-side or cryptographic attestation to rule out."
	}
	return passport
}

func runPowerShellJSON(script string, target any) error {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "powershell.exe", "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", script)
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	output, err := command.Output()
	if err != nil {
		return err
	}
	return json.Unmarshal(output, target)
}

func queryPlatformTrust() (secureBoot, secureKnown, tpmPresent, tpmReady bool) {
	var state struct {
		SecureBoot *bool `json:"secure_boot"`
		TPMPresent bool  `json:"tpm_present"`
		TPMReady   bool  `json:"tpm_ready"`
	}
	script := `$secure=$null; try {$secure=Confirm-SecureBootUEFI -ErrorAction Stop} catch {}; $t=Get-Tpm -ErrorAction SilentlyContinue; [pscustomobject]@{secure_boot=$secure;tpm_present=[bool]$t.TpmPresent;tpm_ready=[bool]$t.TpmReady}|ConvertTo-Json -Compress`
	if runPowerShellJSON(script, &state) == nil {
		if state.SecureBoot != nil {
			secureBoot, secureKnown = *state.SecureBoot, true
		}
		tpmPresent, tpmReady = state.TPMPresent, state.TPMReady
	}
	return
}

func queryGPUIdentities() []GPUIdentity {
	var rows []struct {
		Name, PNPDeviceID, Vendor, Driver string
		Signed                            bool
	}
	script := `$drivers=Get-CimInstance Win32_PnPSignedDriver; $rows=@(Get-CimInstance Win32_VideoController|ForEach-Object{$v=$_;$s=$drivers|Where-Object DeviceID -eq $v.PNPDeviceID|Select-Object -First 1;[pscustomobject]@{Name=$v.Name;PNPDeviceID=$v.PNPDeviceID;Vendor=$v.AdapterCompatibility;Driver=$v.DriverVersion;Signed=[bool]$s.IsSigned}}); ConvertTo-Json -InputObject $rows -Compress`
	if runPowerShellJSON(script, &rows) != nil {
		return nil
	}
	result := make([]GPUIdentity, 0, len(rows))
	for _, row := range rows {
		result = append(result, GPUIdentity{row.Name, row.PNPDeviceID, row.Vendor, row.Driver, row.Signed})
	}
	return result
}

func gpuVendorFromPNP(id string) string {
	id = strings.ToUpper(id)
	switch {
	case strings.Contains(id, "VEN_10DE"):
		return "NVIDIA"
	case strings.Contains(id, "VEN_1002") || strings.Contains(id, "VEN_1022"):
		return "AMD"
	case strings.Contains(id, "VEN_8086"):
		return "Intel"
	}
	return ""
}
