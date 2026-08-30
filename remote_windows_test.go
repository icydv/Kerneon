//go:build windows

package main

import (
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRemoteMonitoringRemainsAvailableDuringHardwareTuning(t *testing.T) {
	bindIP := localIPv4()
	probe, err := net.Listen("tcp", net.JoinHostPort(bindIP, "0"))
	if err != nil {
		t.Fatal(err)
	}
	port := probe.Addr().(*net.TCPAddr).Port
	_ = probe.Close()

	a := &App{}
	a.config.Remote.Enabled = true
	a.config.Remote.Port = port
	a.tuningLab.Running = true
	a.ensureRemoteLinkForMonitoring()
	defer a.stopRemoteLink()

	view := a.remoteSnapshot()
	if !view.Running || view.URL == "" {
		t.Fatalf("hardware tuning suppressed Remote Link: %+v", view)
	}
}

func pairedRemoteApp() (*App, *http.Cookie) {
	a := &App{}
	a.remote.URL = "http://192.168.50.8:47652"
	a.remote.Sessions = map[string]time.Time{"private-session": time.Now().Add(time.Hour)}
	return a, remoteSessionCookie("private-session")
}

func authorizedRemoteRequest(method, target, body string, cookie *http.Cookie) *http.Request {
	r := httptest.NewRequest(method, target, strings.NewReader(body))
	r.AddCookie(cookie)
	r.Header.Set("Origin", "http://192.168.50.8:47652")
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	r.Header.Set("X-Kerneon-Request", "remote-link")
	return r
}

func TestRemoteCredentialsHaveIndependentSecretAndCode(t *testing.T) {
	secret, code, err := randomRemoteCredentials()
	if err != nil {
		t.Fatal(err)
	}
	if len(secret) != 64 || len(code) != 6 {
		t.Fatalf("unexpected credential sizes: secret=%d code=%d", len(secret), len(code))
	}
	if secret == code {
		t.Fatal("pairing secret and fallback code must be independent")
	}
}

func TestRemoteQRUsesCompactOneUseCredential(t *testing.T) {
	token, code, err := randomRemotePairingCredentials()
	if err != nil {
		t.Fatal(err)
	}
	if len(token) != 22 || len(code) != 6 {
		t.Fatalf("unexpected QR credential sizes: token=%d code=%d", len(token), len(code))
	}
	link := remoteQuickPairURL("http://192.168.50.8:47652", token)
	if !strings.HasPrefix(link, "http://192.168.50.8:47652/#p=") || strings.Contains(link, code) {
		t.Fatalf("QR must carry its own scan-only credential, got %q", link)
	}
	bitmap := remotePairingQR("http://192.168.50.8:47652", token)
	if len(bitmap) == 0 || len(bitmap) > 65 {
		t.Fatalf("QR should remain sparse enough for reliable desktop scanning, cells=%d", len(bitmap))
	}
	if !strings.Contains(remotePairHTML, "location.hash") || !strings.Contains(remotePairHTML, "fetch('/q?t='") {
		t.Fatal("the scanned fragment must be exchanged in-browser rather than exposed to camera preview requests")
	}
}

func TestRemoteSessionCookieIsHttpOnlyAndStrict(t *testing.T) {
	cookie := remoteSessionCookie("secret")
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.Path != "/" {
		t.Fatalf("unsafe cookie attributes: %#v", cookie)
	}
}

func TestRemoteQuickPairSurvivesCameraToBrowserHandoff(t *testing.T) {
	a := &App{}
	a.remote.URL = "http://192.168.50.8:47652"
	a.remote.pairToken = strings.Repeat("a", 22)
	a.remote.PairCode = "123456"
	a.remote.Sessions = make(map[string]time.Time)

	first := httptest.NewRecorder()
	a.remoteQuickPair(first, httptest.NewRequest(http.MethodGet, "/pair/qr?t="+strings.Repeat("a", 22), nil))
	if first.Code != http.StatusSeeOther || len(first.Result().Cookies()) != 1 {
		t.Fatalf("first pairing should create a session and redirect, code=%d", first.Code)
	}
	if a.remote.PairCode == "" || a.remote.pairToken == "" || a.remote.pairToken == strings.Repeat("a", 22) || len(a.remote.QR) == 0 {
		t.Fatal("successful pairing must leave a fresh hidden code and one-use QR ready for the next device")
	}
	second := httptest.NewRecorder()
	a.remoteQuickPair(second, httptest.NewRequest(http.MethodGet, "/pair/qr?t="+strings.Repeat("a", 22), nil))
	if second.Code != http.StatusSeeOther || len(second.Result().Cookies()) != 1 {
		t.Fatalf("camera-to-browser handoff should create its own session, code=%d", second.Code)
	}
	a.remote.handoffTokenUntil = time.Now().Add(-time.Second)
	expired := httptest.NewRecorder()
	a.remoteQuickPair(expired, httptest.NewRequest(http.MethodGet, "/pair/qr?t="+strings.Repeat("a", 22), nil))
	if expired.Code != http.StatusUnauthorized {
		t.Fatalf("handoff credential must expire promptly, code=%d", expired.Code)
	}
}

func TestScannedQRCodeSkipsManualPairingPage(t *testing.T) {
	a := &App{}
	a.remote.URL = "http://192.168.50.8:47652"
	a.remote.pairToken = strings.Repeat("q", 22)
	a.remote.PairCode = "123456"
	a.remote.Sessions = make(map[string]time.Time)

	scan := httptest.NewRecorder()
	a.remoteQuickPair(scan, httptest.NewRequest(http.MethodGet, "/pair/qr?t="+strings.Repeat("q", 22), nil))
	if scan.Code != http.StatusSeeOther || scan.Header().Get("Location") != "/" {
		t.Fatalf("scan should establish trust and go directly to the dashboard, code=%d location=%q", scan.Code, scan.Header().Get("Location"))
	}
	cookies := scan.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("scan should receive one device-session cookie, got %d", len(cookies))
	}

	landingRequest := httptest.NewRequest(http.MethodGet, "/", nil)
	landingRequest.AddCookie(cookies[0])
	landing := httptest.NewRecorder()
	a.remoteHome(landing, landingRequest)
	body := landing.Body.String()
	if landing.Code != http.StatusOK || !strings.Contains(body, "Monitor and control this PC") || strings.Contains(body, `name="code"`) {
		t.Fatalf("scanned device must land on the dashboard without a pairing-code form, code=%d", landing.Code)
	}

	manual := httptest.NewRecorder()
	a.remoteHome(manual, httptest.NewRequest(http.MethodGet, "/", nil))
	if !strings.Contains(manual.Body.String(), "Manual fallback") || !strings.Contains(manual.Body.String(), `name="code"`) {
		t.Fatal("typing the base address should retain the explicit manual-code fallback")
	}
}

func TestRemoteDashboardHasOneTidySurgeControlSurfaceAndAnimatedGaugeCluster(t *testing.T) {
	page := remoteDashboardDocument()
	for _, fragment := range []string{
		`data-view="surge"`, `id="view-surge"`, `id="cpuGauge"`, `id="gpuGauge"`,
		`id="vramGauge"`, `id="powerGauge"`, `id="tempGauge"`, `requestAnimationFrame(frame)`,
		`id="surgeProof"`, `id="proofFPS"`, `Above Surge start`, `--base-angle`,
	} {
		if !strings.Contains(page, fragment) {
			t.Fatalf("remote dashboard omitted %q", fragment)
		}
	}
	for _, id := range []string{"status", "controlHint", "auto", "focus", "capture", "unlock", "lease"} {
		if count := strings.Count(page, `id="`+id+`"`); count != 1 {
			t.Fatalf("remote control id %q appears %d times", id, count)
		}
	}
	if strings.Contains(page, "Your PC still feels close") || strings.Contains(page, "Measured regulation, never a blind boost") {
		t.Fatal("remote refresh retained vague or duplicated product copy")
	}
}

func TestRemoteDashboardJavaScriptParses(t *testing.T) {
	node := os.Getenv("CODEX_MCP_NODE_PATH")
	if node == "" {
		var err error
		node, err = exec.LookPath("node")
		if err != nil {
			t.Skip("Node.js is not available for the dashboard syntax check")
		}
	}
	page := remoteDashboardDocument()
	start, end := strings.LastIndex(page, "<script>"), strings.LastIndex(page, "</script>")
	if start < 0 || end <= start {
		t.Fatal("remote dashboard script was not found")
	}
	scriptPath := filepath.Join(t.TempDir(), "remote-dashboard.js")
	if err := os.WriteFile(scriptPath, []byte(page[start+len("<script>"):end]), 0o600); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command(node, "--check", scriptPath).CombinedOutput(); err != nil {
		t.Fatalf("remote dashboard JavaScript did not parse: %v\n%s", err, output)
	}
}

func TestRemoteSurgeProofNeverCallsClockMovementAPerformanceGain(t *testing.T) {
	inProgress := buildRemoteSurgeProof(true, autopilotView{
		Active:           true,
		FrameBaseline:    FrameStats{Available: true, Samples: 900, FPS: 100, OnePercentLow: 70, P99FrameMs: 15, HitchesPerMinute: 6},
		FrameAfter:       FrameStats{Available: true, Samples: 1200, FPS: 106, OnePercentLow: 76, P99FrameMs: 13, HitchesPerMinute: 3},
		FrameProofChecks: 1, FrameComparable: 1, ExperimentalChanges: true,
	})
	if inProgress.Proved || inProgress.Ready || inProgress.State != "measuring" {
		t.Fatalf("an incomplete comparison was presented as proof: %+v", inProgress)
	}

	proved := buildRemoteSurgeProof(true, autopilotView{
		Active:         true,
		FrameBaseline:  FrameStats{Available: true, Samples: 900, FPS: 100, OnePercentLow: 70, P99FrameMs: 15, HitchesPerMinute: 6},
		FrameAfter:     FrameStats{Available: true, Samples: 3600, FPS: 106, OnePercentLow: 76, P99FrameMs: 13, HitchesPerMinute: 3},
		FrameProofDone: true, FrameProofChecks: 3, FrameComparable: 3,
		ExperimentalChanges: true, FrameBenefitCounts: map[string]int{"1% low": 2},
	})
	if !proved.Proved || !proved.Ready || proved.State != "proved" || proved.RepeatedMetric != "1% low" {
		t.Fatalf("repeatable comparable frame evidence was not surfaced: %+v", proved)
	}
	if proved.FPSDeltaPercent <= 0 || proved.LowDeltaPercent <= 0 || proved.P99BetterPercent <= 0 || proved.HitchBetterPercent <= 0 {
		t.Fatalf("better-oriented deltas used the wrong direction: %+v", proved)
	}
}

func TestRemoteSurgeProofStaysPassiveWhenSurgeIsOff(t *testing.T) {
	proof := buildRemoteSurgeProof(false, autopilotView{})
	if proof.State != "off" || proof.Proved || proof.Ready || !strings.Contains(proof.Detail, "Live hardware monitoring stays on") {
		t.Fatalf("disabled Surge should retain honest live monitoring: %+v", proof)
	}
}

func TestNewPairingRevokesEveryExistingSessionAndControlGrant(t *testing.T) {
	a := &App{}
	a.remote.server = &http.Server{}
	a.remote.URL = "http://192.168.50.8:47652"
	a.remote.pairToken = strings.Repeat("a", 64)
	a.remote.PairCode = "123456"
	a.remote.Sessions = map[string]time.Time{"phone": time.Now().Add(time.Hour), "tablet": time.Now().Add(time.Hour)}
	a.remote.PairFailures = map[string]remotePairFailure{"192.168.50.20": {Count: 2}}
	a.remote.ControlUntilRevoked = true
	a.remote.Pending = &RemoteCommand{Action: "capture"}

	a.regenerateRemotePairing()

	if len(a.remote.Sessions) != 0 || a.remote.ControlUntilRevoked || a.remote.ControlUntil.After(time.Now()) || a.remote.Pending != nil {
		t.Fatalf("trust reset left access behind: sessions=%d persistent=%t until=%v pending=%#v", len(a.remote.Sessions), a.remote.ControlUntilRevoked, a.remote.ControlUntil, a.remote.Pending)
	}
	if a.remote.PairCode == "" || a.remote.PairCode == "123456" || a.remote.pairToken == strings.Repeat("a", 64) || len(a.remote.QR) == 0 {
		t.Fatal("trust reset did not publish fresh pairing credentials")
	}
}

func TestUntilRevokedControlIsExplicitAndRevocable(t *testing.T) {
	a := &App{}
	a.remote.ControlUntilRevoked = true
	if !a.remoteControlGranted() {
		t.Fatal("until-revoked mode should grant allowlisted control")
	}
	a.remote.ControlUntilRevoked = false
	if a.remoteControlGranted() {
		t.Fatal("clearing the persistent grant should revoke control immediately")
	}
}

func TestRemoteHeaderGuardRejectsDNSRebindingHost(t *testing.T) {
	handler := remoteHeaders(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }), "192.168.50.8:47652")
	bad := httptest.NewRequest(http.MethodGet, "http://attacker.invalid/", nil)
	bad.Host = "attacker.invalid"
	badResult := httptest.NewRecorder()
	handler.ServeHTTP(badResult, bad)
	if badResult.Code != http.StatusMisdirectedRequest {
		t.Fatalf("unexpected host should be rejected, code=%d", badResult.Code)
	}

	good := httptest.NewRequest(http.MethodGet, "http://192.168.50.8:47652/", nil)
	good.Host = "192.168.50.8:47652"
	goodResult := httptest.NewRecorder()
	handler.ServeHTTP(goodResult, good)
	if goodResult.Code != http.StatusNoContent {
		t.Fatalf("bound host should pass, code=%d", goodResult.Code)
	}
	if goodResult.Header().Get("Content-Security-Policy") == "" {
		t.Fatal("security headers should be applied")
	}
}

func TestRemoteControlRequiresOriginHeaderAndLocalLease(t *testing.T) {
	a, cookie := pairedRemoteApp()
	body := `{"action":"capture","target":""}`

	noLease := httptest.NewRecorder()
	a.remoteActionAPI(noLease, authorizedRemoteRequest(http.MethodPost, a.remote.URL+"/api/action", body, cookie))
	if noLease.Code != http.StatusForbidden {
		t.Fatalf("control without a local lease should be forbidden, code=%d", noLease.Code)
	}

	a.remote.ControlUntil = time.Now().Add(15 * time.Minute)
	crossOriginRequest := authorizedRemoteRequest(http.MethodPost, a.remote.URL+"/api/action", body, cookie)
	crossOriginRequest.Header.Set("Origin", "http://attacker.invalid")
	crossOrigin := httptest.NewRecorder()
	a.remoteActionAPI(crossOrigin, crossOriginRequest)
	if crossOrigin.Code != http.StatusUnauthorized {
		t.Fatalf("cross-origin control should be refused, code=%d", crossOrigin.Code)
	}

	accepted := httptest.NewRecorder()
	a.remoteActionAPI(accepted, authorizedRemoteRequest(http.MethodPost, a.remote.URL+"/api/action", body, cookie))
	if accepted.Code != http.StatusAccepted || a.remote.Pending == nil || a.remote.Pending.Action != "capture" {
		t.Fatalf("allowlisted command with a lease should be queued, code=%d pending=%#v", accepted.Code, a.remote.Pending)
	}
}

func TestRemoteControlRejectsArbitraryAction(t *testing.T) {
	a, cookie := pairedRemoteApp()
	a.remote.ControlUntil = time.Now().Add(15 * time.Minute)
	request := authorizedRemoteRequest(http.MethodPost, a.remote.URL+"/api/action", `{"action":"run-command","target":"whoami"}`, cookie)
	result := httptest.NewRecorder()
	a.remoteActionAPI(result, request)
	if result.Code != http.StatusBadRequest {
		t.Fatalf("arbitrary actions must never enter the queue, code=%d", result.Code)
	}
}
