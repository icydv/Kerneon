//go:build windows

package main

import (
	"fmt"
	"strings"

	"github.com/klauspost/cpuid/v2"
)

// SurgeCPUProfile is the capability boundary Surge uses when explaining and
// selecting CPU policy. Vendor identity is informative; an actual exposed
// feature is required before Surge describes that capability as present.
type SurgeCPUProfile struct {
	Vendor, Brand, Topology string
	CapabilitySummary       string
	PolicyNote              string
	Evidence                string
	Virtual, Hybrid         bool
	PerformanceAllowed      bool
}

func currentSurgeCPUProfile() SurgeCPUProfile {
	cpu := cpuid.CPU
	vendor := surgeCPUVendor(cpu.VendorString, cpu.BrandName)
	brand := strings.TrimSpace(cpu.BrandName)
	if brand == "" {
		brand = strings.TrimSpace(cpu.VendorString)
	}
	if brand == "" {
		brand = "Unidentified x86 processor"
	}
	physical, logical := cpu.PhysicalCores, cpu.LogicalCores
	if physical <= 0 {
		physical = 1
	}
	if logical <= 0 {
		logical = physical
	}
	profile := SurgeCPUProfile{
		Vendor: vendor, Brand: brand,
		Topology: fmt.Sprintf("%d cores · %d threads", physical, logical),
		Virtual:  cpu.VM(), Hybrid: cpu.Supports(cpuid.HYBRID_CPU),
		PerformanceAllowed: !cpu.VM(),
	}
	capabilities := make([]string, 0, 5)
	if profile.Hybrid {
		capabilities = append(capabilities, "hybrid core topology")
	}
	if cpu.ThreadsPerCore > 1 || cpu.Supports(cpuid.HTT) {
		capabilities = append(capabilities, "SMT")
	}
	if cpu.Supports(cpuid.CPBOOST) || cpu.BoostFreq > cpu.Hz {
		capabilities = append(capabilities, "hardware boost")
	}
	if cpu.Supports(cpuid.CPPC) {
		capabilities = append(capabilities, "CPPC")
	}
	if cpu.Supports(cpuid.AVX2) {
		capabilities = append(capabilities, "AVX2")
	}
	if len(capabilities) == 0 {
		capabilities = append(capabilities, "standard Windows power controls")
	}
	profile.CapabilitySummary = strings.Join(capabilities, " · ")
	switch vendor {
	case "AMD":
		profile.PolicyNote = "AMD identity detected. Surge uses Windows/firmware performance controls and does not apply Intel-only assumptions."
	case "Intel":
		if profile.Hybrid {
			profile.PolicyNote = "Intel hybrid topology detected. Windows remains responsible for P/E-core placement; Surge will not pin a game to a guessed core class."
		} else {
			profile.PolicyNote = "Intel identity detected. Surge uses Windows/firmware performance controls and does not apply AMD-only assumptions."
		}
	default:
		profile.PolicyNote = "Vendor-specific assumptions are disabled; only capabilities exposed by the CPU and Windows are considered."
	}
	if profile.Virtual {
		profile.PolicyNote = "A virtual CPU is exposed. Host clocks are outside this guest, so Surge will not offer the Aggressive CPU policy."
	}
	profile.Evidence = fmt.Sprintf("%s · family %d model %d stepping %d · %s · %s", vendor, cpu.Family, cpu.Model, cpu.Stepping, profile.Topology, profile.CapabilitySummary)
	return profile
}

func surgeCPUVendor(vendorString, brand string) string {
	identity := strings.ToLower(vendorString + " " + brand)
	switch {
	case strings.Contains(identity, "authenticamd") || strings.Contains(identity, "advanced micro devices") || strings.Contains(identity, " amd ") || strings.HasPrefix(identity, "amd "):
		return "AMD"
	case strings.Contains(identity, "genuineintel") || strings.Contains(identity, " intel") || strings.HasPrefix(identity, "intel"):
		return "Intel"
	default:
		return "Other x86"
	}
}
