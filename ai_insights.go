//go:build windows

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	openAIEndpoint = "https://api.openai.com/v1/responses"
	openAIModel    = "gpt-5.4-mini"
)

type AIInsight struct {
	Title       string   `json:"title"`
	Explanation string   `json:"explanation"`
	Evidence    []string `json:"evidence"`
	NextStep    string   `json:"next_step"`
	Confidence  string   `json:"confidence"`
}

type AIState struct {
	mu        sync.RWMutex
	Connected bool
	Loading   bool
	Insights  []AIInsight
	Error     string
	Updated   time.Time
}

type telemetryDigest struct {
	CapturedAt string          `json:"captured_at"`
	Current    digestCurrent   `json:"current"`
	History    digestHistory   `json:"recent_history"`
	Processes  []digestProcess `json:"top_processes"`
}

type digestCurrent struct {
	CPU        float64 `json:"cpu_percent"`
	GPU        float64 `json:"gpu_percent"`
	Memory     float64 `json:"memory_percent"`
	Disk       float64 `json:"disk_active_percent"`
	Latency    float64 `json:"latency_ms"`
	Jitter     float64 `json:"jitter_ms"`
	PacketLoss float64 `json:"packet_loss_percent"`
	DownBps    float64 `json:"download_bytes_per_second"`
	UpBps      float64 `json:"upload_bytes_per_second"`
}

type digestHistory struct {
	Samples    int     `json:"samples"`
	CPUAvg     float64 `json:"cpu_average"`
	CPUMax     float64 `json:"cpu_maximum"`
	GPUAvg     float64 `json:"gpu_average"`
	GPUMax     float64 `json:"gpu_maximum"`
	MemoryAvg  float64 `json:"memory_average"`
	DiskAvg    float64 `json:"disk_average"`
	LatencyAvg float64 `json:"latency_average_ms"`
}

type digestProcess struct {
	Label        string  `json:"label"`
	CPU          float64 `json:"cpu_percent"`
	MemoryBytes  uint64  `json:"memory_bytes"`
	DiskReadBps  float64 `json:"disk_read_bytes_per_second"`
	DiskWriteBps float64 `json:"disk_write_bytes_per_second"`
}

func buildTelemetryDigest(s Snapshot) telemetryDigest {
	var d telemetryDigest
	d.CapturedAt = s.At.UTC().Format(time.RFC3339)
	d.Current.CPU, d.Current.GPU = s.CPU.Usage, s.GPU.Usage
	d.Current.Memory, d.Current.Disk = s.Memory.UsagePercent, s.Disk.Usage
	d.Current.Latency, d.Current.Jitter, d.Current.PacketLoss = s.Network.LatencyMs, s.Network.JitterMs, s.Network.PacketLoss
	d.Current.DownBps, d.Current.UpBps = s.Network.DownBps, s.Network.UpBps
	cut := s.At.Add(-60 * time.Second)
	for _, h := range s.History {
		if h.At.Before(cut) {
			continue
		}
		d.History.Samples++
		d.History.CPUAvg += h.CPU
		d.History.GPUAvg += h.GPU
		d.History.MemoryAvg += h.Memory
		d.History.DiskAvg += h.Disk
		d.History.LatencyAvg += h.Latency
		if h.CPU > d.History.CPUMax {
			d.History.CPUMax = h.CPU
		}
		if h.GPU > d.History.GPUMax {
			d.History.GPUMax = h.GPU
		}
	}
	if n := float64(d.History.Samples); n > 0 {
		d.History.CPUAvg /= n
		d.History.GPUAvg /= n
		d.History.MemoryAvg /= n
		d.History.DiskAvg /= n
		d.History.LatencyAvg /= n
	}
	limit := len(s.Processes)
	if limit > 5 {
		limit = 5
	}
	for i := 0; i < limit; i++ {
		p := s.Processes[i]
		item := digestProcess{Label: fmt.Sprintf("process_%d", i+1), CPU: p.CPU, MemoryBytes: p.WorkingSet, DiskReadBps: p.ReadBps, DiskWriteBps: p.WriteBps}
		d.Processes = append(d.Processes, item)
	}
	return d
}

func (a *App) hasAIKey() bool {
	if strings.TrimSpace(os.Getenv("OPENAI_API_KEY")) != "" {
		return true
	}
	_, err := readKerneonCredential()
	return err == nil
}

func (a *App) generateAIInsights() {
	a.ai.mu.Lock()
	if a.ai.Loading {
		a.ai.mu.Unlock()
		return
	}
	a.ai.Loading, a.ai.Error = true, ""
	a.ai.mu.Unlock()
	snapshot := a.engine.Snapshot()
	go func() {
		key := strings.TrimSpace(os.Getenv("OPENAI_API_KEY"))
		if key == "" {
			key, _ = readKerneonCredential()
		}
		var insights []AIInsight
		var err error
		if key == "" {
			err = errors.New("connect an OpenAI API key first")
		} else {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			insights, err = requestAIInsights(ctx, openAIEndpoint, key, buildTelemetryDigest(snapshot))
			cancel()
		}
		a.ai.mu.Lock()
		a.ai.Loading = false
		if err != nil {
			a.ai.Error = err.Error()
			a.logger.Error("ai-insights", "generation failed", err)
		} else {
			a.ai.Insights, a.ai.Error, a.ai.Updated = insights, "", time.Now()
			a.engine.AddUserEvent("insights", "AI telemetry analysis refreshed", fmt.Sprintf("%d evidence-grounded observations were generated.", len(insights)), 0)
		}
		a.ai.mu.Unlock()
		if a.hwnd != 0 {
			procPostMessageW.Call(a.hwnd, WM_APP_RENDER, 0, 0)
		}
	}()
}

func requestAIInsights(ctx context.Context, endpoint, key string, digest telemetryDigest) ([]AIInsight, error) {
	return requestAIInsightsWithClient(ctx, endpoint, key, digest, &http.Client{Timeout: 30 * time.Second})
}

func requestAIInsightsWithClient(ctx context.Context, endpoint, key string, digest telemetryDigest, client *http.Client) ([]AIInsight, error) {
	input, err := json.Marshal(digest)
	if err != nil {
		return nil, err
	}
	schema := map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{"insights": map[string]any{
			"type": "array", "minItems": 2, "maxItems": 4,
			"items": map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{
				"title": map[string]any{"type": "string"}, "explanation": map[string]any{"type": "string"},
				"evidence":  map[string]any{"type": "array", "minItems": 1, "maxItems": 3, "items": map[string]any{"type": "string"}},
				"next_step": map[string]any{"type": "string"}, "confidence": map[string]any{"type": "string", "enum": []string{"low", "medium", "high"}},
			}, "required": []string{"title", "explanation", "evidence", "next_step", "confidence"}},
		}}, "required": []string{"insights"},
	}
	payload := map[string]any{
		"model": openAIModel, "store": false, "max_output_tokens": 1200,
		"reasoning":    map[string]any{"effort": "low"},
		"instructions": "You are Kerneon's telemetry analyst. Use only the supplied measurements. Produce concise, situation-specific observations. Never invent FPS, causes, hardware facts, or performance gains. Distinguish correlation from causation. Suggest a test when evidence is insufficient. Network measurements may be secondary context but must never displace local performance, frame-delivery, resource-pressure or stability insights. Do not suggest registry cleaners, RAM cleaners, blanket service disabling, driver updaters, or arbitrary boost buttons. The output is advisory and cannot control the PC.",
		"input":        string(input),
		"text":         map[string]any{"format": map[string]any{"type": "json_schema", "name": "kerneon_insights", "strict": true, "schema": schema}},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var apiErr struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.Unmarshal(responseBody, &apiErr)
		if apiErr.Error.Message == "" {
			apiErr.Error.Message = http.StatusText(resp.StatusCode)
		}
		return nil, fmt.Errorf("OpenAI request failed: %s", apiErr.Error.Message)
	}
	var decoded struct {
		Output []struct {
			Type    string                        `json:"type"`
			Content []struct{ Type, Text string } `json:"content"`
		} `json:"output"`
	}
	if err := json.Unmarshal(responseBody, &decoded); err != nil {
		return nil, fmt.Errorf("decode OpenAI response: %w", err)
	}
	var outputText string
	for _, item := range decoded.Output {
		for _, content := range item.Content {
			if content.Type == "output_text" {
				outputText += content.Text
			}
		}
	}
	if outputText == "" {
		return nil, errors.New("OpenAI returned no insight text")
	}
	var result struct {
		Insights []AIInsight `json:"insights"`
	}
	if err := json.Unmarshal([]byte(outputText), &result); err != nil {
		return nil, fmt.Errorf("decode structured insights: %w", err)
	}
	if len(result.Insights) < 2 {
		return nil, errors.New("OpenAI returned too few grounded insights")
	}
	return result.Insights, nil
}
