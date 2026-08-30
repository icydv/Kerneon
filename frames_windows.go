//go:build windows

package main

import (
	"bufio"
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"math"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

type FrameSample struct {
	At             time.Time
	FrameTimeMs    float64
	DisplayDelayMs float64
	RenderMs       float64
	GPUActiveMs    float64
	Dropped        bool
}

type FrameStats struct {
	Available, Capturing               bool
	Provider, Game, Error              string
	PID                                uint32
	FPS, OnePercentLow, P95DisplayMs   float64
	DroppedPercent, AverageGPUActiveMs float64
	AverageRenderMs                    float64
	ZeroPointOnePercentLow             float64
	P99FrameMs, WorstFrameMs           float64
	HitchesPerMinute, FramePacingCV    float64
	Samples, HitchCount                int
}

type FrameMonitor struct {
	mu          sync.RWMutex
	command     *exec.Cmd
	cancel      context.CancelFunc
	executable  string
	sessionName string
	pid         uint32
	game        string
	errorText   string
	samples     []FrameSample
}

var (
	presentMonPathOnce          sync.Once
	legacyPresentMonCleanupOnce sync.Once
	presentMonPath              string
)

const legacyPresentMonSession = "KerneonSurgeTuning"

func presentMonExecutable() string {
	presentMonPathOnce.Do(func() {
		matches, _ := filepath.Glob(`C:\Program Files\Intel\PresentMon\PresentMonConsoleApplication\PresentMon-*-x64.exe`)
		if len(matches) > 0 {
			sort.Strings(matches)
			presentMonPath = matches[len(matches)-1]
		}
	})
	return presentMonPath
}

func cleanupLegacyPresentMonSessions(executable string) {
	legacyPresentMonCleanupOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, executable, "--session_name", legacyPresentMonSession, "--terminate_existing_session")
		command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
		_ = command.Run()
	})
}

func presentMonCaptureArgs(pid uint32, session string) []string {
	return []string{
		"--process_id", fmt.Sprint(pid),
		"--output_stdout",
		"--no_console_stats",
		"--v1_metrics",
		"--set_circular_buffer_size", "16384",
		"--session_name", session,
		"--stop_existing_session",
		"--terminate_on_proc_exit",
	}
}

func (monitor *FrameMonitor) Start(pid uint32, game string) {
	monitor.mu.RLock()
	if monitor.command != nil && monitor.pid == pid {
		monitor.mu.RUnlock()
		return
	}
	monitor.mu.RUnlock()
	monitor.Stop()
	executable := presentMonExecutable()
	if executable == "" {
		monitor.mu.Lock()
		monitor.errorText = "Intel PresentMon is not installed"
		monitor.mu.Unlock()
		return
	}
	cleanupLegacyPresentMonSessions(executable)
	ctx, cancel := context.WithCancel(context.Background())
	session := fmt.Sprintf("KerneonFrames-%d", pid)
	command := exec.CommandContext(ctx, executable, presentMonCaptureArgs(pid, session)...)
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	stdout, err := command.StdoutPipe()
	if err != nil {
		cancel()
		monitor.mu.Lock()
		monitor.errorText = err.Error()
		monitor.mu.Unlock()
		return
	}
	stderr, err := command.StderrPipe()
	if err != nil {
		cancel()
		monitor.mu.Lock()
		monitor.errorText = err.Error()
		monitor.mu.Unlock()
		return
	}
	if err := command.Start(); err != nil {
		cancel()
		monitor.mu.Lock()
		monitor.errorText = err.Error()
		monitor.mu.Unlock()
		return
	}
	monitor.mu.Lock()
	monitor.command, monitor.cancel = command, cancel
	monitor.executable, monitor.sessionName = executable, session
	monitor.pid, monitor.game, monitor.errorText = pid, game, ""
	monitor.samples = monitor.samples[:0]
	monitor.mu.Unlock()
	go monitor.consume(command, stdout)
	go monitor.consumeDiagnostics(command, stderr)
}

func (monitor *FrameMonitor) consumeDiagnostics(command *exec.Cmd, output io.Reader) {
	scanner := bufio.NewScanner(output)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		lower := strings.ToLower(line)
		if line == "" || (!strings.Contains(lower, "warning") && !strings.Contains(lower, "lost")) {
			continue
		}
		monitor.mu.Lock()
		if monitor.command == command {
			monitor.errorText = line
		}
		monitor.mu.Unlock()
	}
}

func (monitor *FrameMonitor) consume(command *exec.Cmd, output io.Reader) {
	reader := csv.NewReader(bufio.NewReaderSize(output, 128*1024))
	header, err := reader.Read()
	if err != nil {
		monitor.captureEnded(command, err)
		return
	}
	columns := make(map[string]int, len(header))
	for index, name := range header {
		columns[strings.TrimSpace(name)] = index
	}
	field := func(record []string, name string) float64 {
		index, ok := columns[name]
		if !ok || index >= len(record) {
			return 0
		}
		value, _ := strconv.ParseFloat(record[index], 64)
		return value
	}
	for {
		record, err := reader.Read()
		if err != nil {
			if err != io.EOF {
				monitor.captureEnded(command, err)
			} else {
				monitor.captureEnded(command, nil)
			}
			return
		}
		sample := FrameSample{At: time.Now(), FrameTimeMs: field(record, "msBetweenPresents"), DisplayDelayMs: field(record, "msUntilDisplayed"), RenderMs: field(record, "msUntilRenderComplete"), GPUActiveMs: field(record, "msGPUActive"), Dropped: field(record, "Dropped") != 0}
		if sample.FrameTimeMs <= 0 || sample.FrameTimeMs > 1000 {
			continue
		}
		monitor.mu.Lock()
		monitor.samples = append(monitor.samples, sample)
		cutoff := sample.At.Add(-90 * time.Second)
		start := 0
		for start < len(monitor.samples) && monitor.samples[start].At.Before(cutoff) {
			start++
		}
		if start > 0 {
			copy(monitor.samples, monitor.samples[start:])
			monitor.samples = monitor.samples[:len(monitor.samples)-start]
		}
		monitor.mu.Unlock()
	}
}

func (monitor *FrameMonitor) captureEnded(command *exec.Cmd, readErr error) {
	_ = command.Wait()
	monitor.mu.Lock()
	if monitor.command == command {
		monitor.command, monitor.cancel = nil, nil
		if readErr != nil && monitor.errorText == "" {
			monitor.errorText = readErr.Error()
		}
	}
	monitor.mu.Unlock()
}

func (monitor *FrameMonitor) Stop() {
	monitor.mu.Lock()
	command, cancel := monitor.command, monitor.cancel
	executable, session := monitor.executable, monitor.sessionName
	monitor.command, monitor.cancel = nil, nil
	monitor.pid, monitor.game = 0, ""
	monitor.mu.Unlock()
	if command == nil {
		return
	}
	if executable != "" && session != "" {
		ctx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
		stop := exec.CommandContext(ctx, executable, "--session_name", session, "--terminate_existing_session")
		stop.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
		_ = stop.Run()
		stopCancel()
	}
	if cancel != nil {
		cancel()
	}
}

func (monitor *FrameMonitor) Snapshot() FrameStats {
	return monitor.SnapshotWindow(time.Now().Add(-30*time.Second), time.Now())
}

func (monitor *FrameMonitor) SnapshotWindow(from, to time.Time) FrameStats {
	monitor.mu.RLock()
	samples := append([]FrameSample(nil), monitor.samples...)
	stats := FrameStats{Available: presentMonExecutable() != "", Capturing: monitor.command != nil, Provider: "Intel PresentMon ETW", Game: monitor.game, PID: monitor.pid, Error: monitor.errorText}
	monitor.mu.RUnlock()
	frameTimes, displayTimes := make([]float64, 0, len(samples)), make([]float64, 0, len(samples))
	dropped := 0
	for _, sample := range samples {
		if sample.At.Before(from) || sample.At.After(to) {
			continue
		}
		stats.Samples++
		if sample.Dropped {
			dropped++
			continue
		}
		frameTimes = append(frameTimes, sample.FrameTimeMs)
		if sample.DisplayDelayMs > 0 {
			displayTimes = append(displayTimes, sample.DisplayDelayMs)
		}
		stats.AverageGPUActiveMs += sample.GPUActiveMs
		stats.AverageRenderMs += sample.RenderMs
	}
	if len(frameTimes) > 0 {
		sum := 0.0
		for _, value := range frameTimes {
			sum += value
		}
		stats.FPS = 1000 / (sum / float64(len(frameTimes)))
		sort.Float64s(frameTimes)
		stats.OnePercentLow = onePercentLow(frameTimes)
		stats.ZeroPointOnePercentLow = tailPercentLow(frameTimes, 0.001)
		stats.P99FrameMs = percentile(frameTimes, 0.99)
		stats.WorstFrameMs = frameTimes[len(frameTimes)-1]
		median := percentile(frameTimes, 0.50)
		hitchThreshold := math.Max(median*2, median+8)
		for _, frameTime := range frameTimes {
			if frameTime >= hitchThreshold {
				stats.HitchCount++
			}
		}
		minutes := to.Sub(from).Minutes()
		if minutes <= 0 {
			minutes = 0.5
		}
		stats.HitchesPerMinute = float64(stats.HitchCount) / minutes
		mean := sum / float64(len(frameTimes))
		if mean > 0 {
			variance := 0.0
			for _, frameTime := range frameTimes {
				delta := frameTime - mean
				variance += delta * delta
			}
			stats.FramePacingCV = math.Sqrt(variance/float64(len(frameTimes))) / mean
		}
		stats.AverageGPUActiveMs /= float64(len(frameTimes))
		stats.AverageRenderMs /= float64(len(frameTimes))
	}
	if len(displayTimes) > 0 {
		sort.Float64s(displayTimes)
		stats.P95DisplayMs = percentile(displayTimes, 0.95)
	}
	if stats.Samples > 0 {
		stats.DroppedPercent = float64(dropped) * 100 / float64(stats.Samples)
	}
	return stats
}

func onePercentLow(sortedFrameTimes []float64) float64 {
	return tailPercentLow(sortedFrameTimes, 0.01)
}

func tailPercentLow(sortedFrameTimes []float64, fraction float64) float64 {
	if len(sortedFrameTimes) == 0 {
		return 0
	}
	if fraction <= 0 {
		fraction = 0.01
	}
	count := int(math.Ceil(float64(len(sortedFrameTimes)) * fraction))
	if count < 1 {
		count = 1
	}
	start := len(sortedFrameTimes) - count
	sum := 0.0
	for _, value := range sortedFrameTimes[start:] {
		sum += value
	}
	return 1000 / (sum / float64(count))
}

func percentile(sorted []float64, quantile float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	index := int(quantile * float64(len(sorted)-1))
	if index < 0 {
		index = 0
	}
	if index >= len(sorted) {
		index = len(sorted) - 1
	}
	return sorted[index]
}

func frameLimiterLabel(stats FrameStats, refreshHz int) string {
	if stats.Samples < 30 || stats.FPS <= 0 || stats.AverageRenderMs <= 0 {
		return "collecting limiter evidence"
	}
	frameTime := 1000 / stats.FPS
	gpuShare := stats.AverageRenderMs / frameTime
	if gpuShare >= 0.85 {
		return fmt.Sprintf("GPU throughput · render path %.0f%% of frame", gpuShare*100)
	}
	if gpuShare <= 0.65 && (refreshHz <= 0 || stats.FPS < float64(refreshHz)*0.97) {
		return fmt.Sprintf("CPU / engine / frame cap · render path %.0f%% of frame", gpuShare*100)
	}
	return fmt.Sprintf("mixed frame pressure · render path %.0f%% of frame", gpuShare*100)
}

func (a *App) updateFrameCapture() {
	game, running := a.lockedGameProcess()
	if !running {
		a.frames.Stop()
		return
	}
	a.frames.Start(game.PID, game.Name)
	a.recordStutterIfNeeded(a.frames.Snapshot())
}
