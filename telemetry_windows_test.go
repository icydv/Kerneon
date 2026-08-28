//go:build windows

package main

import (
	"math"
	"os"
	"testing"
	"time"

	"kerneon/core"
)

func TestWindowsCollectorsReturnSaneValues(t *testing.T) {
	memory, perf := queryMemory()
	if memory.Total == 0 || memory.Available > memory.Total {
		t.Fatalf("invalid memory totals: %+v", memory)
	}
	if memory.UsagePercent < 0 || memory.UsagePercent > 100 || math.IsNaN(memory.UsagePercent) {
		t.Fatalf("invalid memory percentage: %f", memory.UsagePercent)
	}
	if perf.ProcessCount == 0 || perf.ThreadCount == 0 || perf.HandleCount == 0 {
		t.Fatalf("invalid system counts: processes=%d threads=%d handles=%d", perf.ProcessCount, perf.ThreadCount, perf.HandleCount)
	}

	system := querySystemData()
	if system.Logical < 1 || system.Cores < 1 || system.Cores > system.Logical {
		t.Fatalf("invalid topology: %d cores, %d logical", system.Cores, system.Logical)
	}
	if system.Build == "" || system.InstalledRAM == 0 {
		t.Fatalf("incomplete system metadata: %+v", system)
	}

	volumes := queryVolumes()
	if len(volumes) == 0 {
		t.Fatal("expected at least one fixed volume")
	}
	for _, volume := range volumes {
		if volume.Total == 0 || volume.Free > volume.Total {
			t.Fatalf("invalid volume: %+v", volume)
		}
	}

	rows, err := getIfRows()
	if err != nil || len(rows) == 0 {
		t.Fatalf("network interface table unavailable: rows=%d err=%v", len(rows), err)
	}

	processes, _ := queryProcesses(nil, time.Now())
	if len(processes) == 0 {
		t.Fatal("expected process enumeration results")
	}
	foundCurrent := false
	for _, process := range processes {
		if process.PID == uint32(os.Getpid()) {
			foundCurrent = true
			break
		}
	}
	if !foundCurrent {
		t.Log("current short-lived test process was outside the top-80 telemetry set")
	}
}

func TestTelemetryEngineSustains120HzNetworkSampling(t *testing.T) {
	config := core.DefaultConfig()
	config.Sampling.NetworkHz = 120
	config.Sampling.CPUms = 200
	config.Sampling.Processms = 500
	config.Network.PingSeconds = 0
	engine := NewTelemetryEngine(config, &Logger{})
	engine.Start()
	t.Cleanup(engine.Stop)
	time.Sleep(1500 * time.Millisecond)
	snapshot := engine.Snapshot()
	if snapshot.Memory.Total == 0 || snapshot.CPU.Logical == 0 {
		t.Fatalf("engine failed to populate CPU/memory: %+v %+v", snapshot.CPU, snapshot.Memory)
	}
	if len(snapshot.History) < 4 || len(snapshot.Processes) == 0 {
		t.Fatalf("engine history/process collectors stalled: history=%d processes=%d", len(snapshot.History), len(snapshot.Processes))
	}
	if snapshot.Network.AdapterIndex != 0 {
		// The loop also performs one adapter refresh each second, so this is a
		// practical tolerance around the configured rate rather than a timer claim.
		if snapshot.Network.SampleCount < 100 || snapshot.Network.SampleCount > 220 {
			t.Fatalf("unexpected 120 Hz sample count over 1.5s: %d", snapshot.Network.SampleCount)
		}
		beforeLowPower := snapshot.Network.SampleCount
		engine.SetLowPower(true)
		time.Sleep(1200 * time.Millisecond)
		lowPowerSamples := engine.Snapshot().Network.SampleCount - beforeLowPower
		if lowPowerSamples < 5 || lowPowerSamples > 25 {
			t.Fatalf("adaptive mode did not reduce network sampling near 10 Hz: %d samples over 1.2s", lowPowerSamples)
		}
	}
	engine.Stop()
}

func TestPDHProviderFailsSoftly(t *testing.T) {
	provider := newPDHQuery()
	defer provider.Close()
	time.Sleep(150 * time.Millisecond)
	disk, gpu := provider.Sample()
	if disk.Usage < 0 || disk.Usage > 100 || math.IsNaN(disk.Usage) {
		t.Fatalf("invalid disk sample: %+v", disk)
	}
	if gpu.Usage < 0 || gpu.Usage > 100 || math.IsNaN(gpu.Usage) {
		t.Fatalf("invalid GPU sample: %+v", gpu)
	}
	if provider.handle == 0 && disk.ProviderError == "" && gpu.Error == "" {
		t.Fatal("unavailable PDH provider did not report an error")
	}
}
