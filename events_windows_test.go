//go:build windows

package main

import (
	"testing"
	"time"
)

func TestClassifyWindowsEvent(t *testing.T) {
	tests := []struct {
		source   string
		id       int
		message  string
		category string
	}{
		{"Microsoft-Windows-WHEA-Logger", 18, "A fatal hardware error occurred", "Hardware"},
		{"Application Hang", 1002, "stopped interacting with Windows", "Freeze"},
		{"Kernel-Power", 41, "unexpected restart", "Crash"},
		{"Microsoft-Windows-Resource-Exhaustion-Detector", 2004, "low virtual memory", "Performance"},
	}
	for _, test := range tests {
		event := classifyWindowsEvent(time.Now(), test.source, "System", test.message, test.id, 2, 1)
		if event.Category != test.category {
			t.Errorf("%s/%d: got %q want %q", test.source, test.id, event.Category, test.category)
		}
	}
}

func TestGPUVendorFromPNP(t *testing.T) {
	if got := gpuVendorFromPNP(`PCI\VEN_10DE&DEV_1E81`); got != "NVIDIA" {
		t.Fatalf("got %q", got)
	}
	if got := gpuVendorFromPNP(`PCI\VEN_1002&DEV_73BF`); got != "AMD" {
		t.Fatalf("got %q", got)
	}
}

func TestEventSignificanceSeparatesNoiseFromActionableEvidence(t *testing.T) {
	routine := eventSignificance(SystemEvent{Category: "Noise", Level: 3, Occurrences: 4})
	whea := eventSignificance(SystemEvent{Category: "Hardware", Source: "Microsoft-Windows-WHEA-Logger", Level: 2, Occurrences: 3})
	if routine.Label != "Routine" || routine.Score >= 25 {
		t.Fatalf("routine event was overstated: %+v", routine)
	}
	if whea.Score < 85 || whea.Label != "High priority" {
		t.Fatalf("corroborated WHEA event was understated: %+v", whea)
	}
}
