//go:build windows

package main

import (
	"kerneon/core"
	"math"
	"strings"
	"testing"
	"time"
)

func TestSafetyRuntimeStateOverridesStaleConfigSnapshots(t *testing.T) {
	a := &App{config: core.DefaultConfig()}
	a.config.Tuning.Autopilot = true
	a.config.Tuning.AntiCheatGuard = false
	if !a.surgeIsEnabled() || a.antiCheatGuardIsEnabled() {
		t.Fatal("uninitialized runtime state did not fall back to the loaded config")
	}

	a.setSurgeEnabled(false)
	a.setAntiCheatGuardEnabled(true)
	// Simulate a stale background copy landing after the user changed both
	// safety controls. The atomics remain authoritative for UI, tuning and disk.
	a.config.Tuning.Autopilot = true
	a.config.Tuning.AntiCheatGuard = false
	if a.surgeIsEnabled() {
		t.Fatal("stale config resurrected Surge after it was switched off")
	}
	if !a.antiCheatGuardIsEnabled() {
		t.Fatal("stale config disabled the anti-cheat guard")
	}
	snapshot := a.configSnapshot()
	if snapshot.Tuning.Autopilot || !snapshot.Tuning.AntiCheatGuard {
		t.Fatalf("persistence snapshot used stale safety values: %+v", snapshot.Tuning)
	}
}

func TestWindowRestoreRejectsWindowsMinimizedCoordinates(t *testing.T) {
	minimized := RECT{Left: 362, Top: -32768, Right: 1602, Bottom: -31976}
	if restorableWindowRect(minimized) {
		t.Fatal("Windows minimized coordinates were accepted as a normal position")
	}
	work := RECT{Left: 0, Top: 0, Right: 1920, Bottom: 1040}
	if windowRectVisibleInWorkArea(minimized, work) {
		t.Fatal("minimized window was classified as visible")
	}
	visible := RECT{Left: 100, Top: 80, Right: 1340, Bottom: 880}
	if !restorableWindowRect(visible) || !windowRectVisibleInWorkArea(visible, work) {
		t.Fatal("normal visible window was rejected")
	}
}

func TestNearestPolylineSampleUsesNearestSeriesGeometry(t *testing.T) {
	points := []POINT{{10, 10}, {30, 30}, {50, 10}}
	index, distance, ok := nearestPolylineSample(points, POINT{28, 29})
	if !ok || index != 1 || distance > 2 {
		t.Fatalf("unexpected hit: index=%d distance=%.2f ok=%v", index, distance, ok)
	}
	_, far, ok := nearestPolylineSample(points, POINT{30, 70})
	if !ok || far < 35 {
		t.Fatalf("far pointer should not look like a line hit: %.2f", far)
	}
}

func TestModalChildHitWinsOverBackdropAndPanelShield(t *testing.T) {
	a := &App{}
	bounds := RECT{0, 0, 500, 400}
	panel := RECT{100, 80, 400, 320}
	button := RECT{250, 200, 360, 240}
	a.modalHitShield(bounds, panel, "dismiss")
	a.hit(button, "official-source", 2)

	if hit, ok := a.hitAt(POINT{300, 220}); !ok || hit.Action != "official-source" || hit.Value != 2 {
		t.Fatalf("child control was intercepted: %+v ok=%t", hit, ok)
	}
	if hit, ok := a.hitAt(POINT{150, 150}); !ok || hit.Action != "" {
		t.Fatalf("blank panel should be inert: %+v ok=%t", hit, ok)
	}
	if hit, ok := a.hitAt(POINT{20, 20}); !ok || hit.Action != "dismiss" {
		t.Fatalf("backdrop should dismiss: %+v ok=%t", hit, ok)
	}
}

func TestModalShieldsNeverDrawFullSurfacePressFeedback(t *testing.T) {
	a := &App{}
	bounds := RECT{0, 0, 500, 400}
	panel := RECT{100, 80, 400, 320}
	a.modalHitShield(bounds, panel, "dismiss")
	for _, hit := range a.hits {
		if hasGlobalPressFeedback(hit) {
			t.Fatalf("modal shield would paint a full-surface highlight: %+v", hit)
		}
	}
	button := HitRegion{Rect: RECT{250, 200, 360, 240}, Action: "official-source", Value: 0}
	if !hasGlobalPressFeedback(button) {
		t.Fatal("real child controls must retain press feedback")
	}
}

func TestCursorTooltipTracksWithoutLeavingContent(t *testing.T) {
	bounds := RECT{200, 20, 1200, 780}
	for _, cursor := range []POINT{{220, 40}, {1180, 40}, {220, 760}, {1180, 760}, {700, 400}} {
		got := cursorTooltipRect(bounds, cursor, 420, 122, 16)
		if got.Left < bounds.Left || got.Top < bounds.Top || got.Right > bounds.Right || got.Bottom > bounds.Bottom {
			t.Fatalf("tooltip escaped bounds at %+v: %+v", cursor, got)
		}
		if pointInRect(cursor, got) {
			t.Fatalf("tooltip covered its cursor at %+v: %+v", cursor, got)
		}
	}
}

func TestSurgeFeatureCopyBalancesBenefitAndEvidence(t *testing.T) {
	inputs := struct {
		cpu        SurgeCPUProfile
		route      SurgeRoute
		stack      SurgeStackSnapshot
		contention SurgeContention
		stutter    SurgeStutterDiagnosis
		antiCheat  AntiCheatState
	}{
		cpu:        SurgeCPUProfile{Vendor: "AMD"},
		route:      SurgeRoute{Title: "CPU route", Confidence: "high", ControlPath: "Windows policy"},
		contention: SurgeContention{Detail: "No relevant contender"},
		stutter:    SurgeStutterDiagnosis{Title: "No stutter signature", Confidence: "low"},
		antiCheat:  AntiCheatState{Policy: "External-only"},
	}
	for _, guarded := range []bool{true, false} {
		features := surgeFeatureCatalog(guarded, inputs.cpu, inputs.route, inputs.stack, inputs.contention, inputs.stutter, inputs.antiCheat)
		if len(features) != 8 {
			t.Fatalf("guarded=%t: got %d feature statements, want 8", guarded, len(features))
		}
		for index, feature := range features {
			if strings.TrimSpace(feature.Title) == "" || strings.TrimSpace(feature.Benefit) == "" || strings.TrimSpace(feature.Technical) == "" {
				t.Fatalf("guarded=%t feature %d lacks progressive-disclosure copy: %+v", guarded, index, feature)
			}
			if feature.Benefit == feature.Technical {
				t.Fatalf("guarded=%t feature %d repeats technical copy as its user benefit", guarded, index)
			}
		}
	}
}

func TestMotionConvergesEquallyAtDifferentRefreshRates(t *testing.T) {
	simulate := func(refreshHz int) float64 {
		a := &App{animationProgress: make(map[string]float64)}
		frames := refreshHz / 10 // compare each display after the same 100 ms
		for range frames {
			a.animationDelta = time.Second / time.Duration(refreshHz)
			a.animate("hover", 1, 20*time.Millisecond)
		}
		return a.animationProgress["hover"]
	}
	want := simulate(60)
	for _, refreshHz := range []int{120, 144, 240} {
		got := simulate(refreshHz)
		if math.Abs(got-want) > 0.002 {
			t.Fatalf("%d Hz motion diverged: got %.5f, 60 Hz %.5f", refreshHz, got, want)
		}
	}
	if surgeActivationDuration > 600*time.Millisecond {
		t.Fatalf("Surge activation has become sluggish: %s", surgeActivationDuration)
	}
}

func TestTuningLabSurgeGateIsActionable(t *testing.T) {
	label, action := tuningSurgeEnableControl()
	if label != "Enable Surge · Aggressive" || action != "surge-tuning-enable-aggressive" {
		t.Fatalf("Tuning Lab rendered a disabled Surge gate: label=%q action=%q", label, action)
	}
	a := &App{surgeTuningOpen: true}
	a.runAction(action, 0)
	if !a.surgeSafetyOpen || a.surgeTuningOpen || !a.surgeTuningResumeAfterSafety {
		t.Fatal("first-time activation must route through the safety acknowledgement and return to Tuning Lab")
	}
}
