//go:build windows

package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func TestTelemetryDigestExcludesProcessAndNetworkIdentity(t *testing.T) {
	now := time.Now()
	s := Snapshot{At: now, Network: NetworkData{IPv4: "192.0.2.55", Name: "Private Adapter"}, Processes: []ProcessMetric{{Name: "secret-game.exe", Path: `C:\Private\secret-game.exe`, CPU: 31, WorkingSet: 1234}}, History: []HistorySample{{At: now, CPU: 22, GPU: 77}}}
	b, err := json.Marshal(buildTelemetryDigest(s))
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	for _, secret := range []string{"secret-game", "C:\\Private", "192.0.2.55", "Private Adapter"} {
		if strings.Contains(text, secret) {
			t.Fatalf("telemetry digest leaked %q: %s", secret, text)
		}
	}
	if !strings.Contains(text, "process_1") || !strings.Contains(text, `"cpu_percent":31`) {
		t.Fatalf("digest lost anonymous performance evidence: %s", text)
	}
}

func TestAIInsightsRequestIsStatelessAndStructured(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("authorization header missing")
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["store"] != false {
			t.Errorf("AI request was not stateless: %#v", body["store"])
		}
		text, _ := body["text"].(map[string]any)
		format, _ := text["format"].(map[string]any)
		if format["type"] != "json_schema" || format["strict"] != true {
			t.Errorf("strict structured output missing: %#v", format)
		}
		response := `{"output":[{"type":"message","content":[{"type":"output_text","text":"{\"insights\":[{\"title\":\"GPU load\",\"explanation\":\"GPU is the leading measured load.\",\"evidence\":[\"GPU 94%\"],\"next_step\":\"Repeat the scene.\",\"confidence\":\"high\"},{\"title\":\"CPU headroom\",\"explanation\":\"CPU remains below saturation.\",\"evidence\":[\"CPU 51%\"],\"next_step\":\"Keep monitoring.\",\"confidence\":\"medium\"}]}"}]}]}`
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(response)), Request: r}, nil
	})}
	insights, err := requestAIInsightsWithClient(context.Background(), "https://api.openai.test/v1/responses", "test-key", telemetryDigest{}, client)
	if err != nil {
		t.Fatal(err)
	}
	if len(insights) != 2 || insights[0].Title != "GPU load" {
		t.Fatalf("unexpected insights: %+v", insights)
	}
}
