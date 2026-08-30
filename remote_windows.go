//go:build windows

package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"kerneon/core"

	qrcode "github.com/skip2/go-qrcode"
)

type RemoteServer struct {
	mu                  sync.RWMutex
	server              *http.Server
	URL                 string
	PairCode            string
	pairToken           string
	handoffToken        string
	handoffTokenUntil   time.Time
	Error               string
	QR                  [][]bool
	Sessions            map[string]time.Time
	PairFailures        map[string]remotePairFailure
	ControlUntil        time.Time
	ControlUntilRevoked bool
	Games               []InstalledGame
	Pending             *RemoteCommand
}

type remoteView struct {
	Running, Enabled, PairUsed, ControlGranted, ControlUntilRevoked bool
	URL, PairCode, Error                                            string
	QR                                                              [][]bool
	SessionCount, GameCount                                         int
	ControlUntil                                                    time.Time
}

type remotePairFailure struct {
	Count        int
	BlockedUntil time.Time
}

type RemoteCommand struct {
	Action, Target, RemoteIP string
}

func (a *App) remoteSnapshot() remoteView {
	a.remote.mu.Lock()
	defer a.remote.mu.Unlock()
	now := time.Now()
	for token, expiry := range a.remote.Sessions {
		if !expiry.After(now) {
			delete(a.remote.Sessions, token)
		}
	}
	return remoteView{Running: a.remote.server != nil, Enabled: a.config.Remote.Enabled, PairUsed: len(a.remote.Sessions) > 0, ControlGranted: a.remote.ControlUntilRevoked || a.remote.ControlUntil.After(now), ControlUntilRevoked: a.remote.ControlUntilRevoked, URL: a.remote.URL, PairCode: a.remote.PairCode, Error: a.remote.Error, QR: a.remote.QR, SessionCount: len(a.remote.Sessions), GameCount: len(a.remote.Games), ControlUntil: a.remote.ControlUntil}
}

func randomRemoteCredentials() (string, string, error) {
	secretBytes := make([]byte, 32)
	codeBytes := make([]byte, 4)
	if _, err := rand.Read(secretBytes); err != nil {
		return "", "", err
	}
	if _, err := rand.Read(codeBytes); err != nil {
		return "", "", err
	}
	value := uint32(codeBytes[0])<<24 | uint32(codeBytes[1])<<16 | uint32(codeBytes[2])<<8 | uint32(codeBytes[3])
	return hex.EncodeToString(secretBytes), fmt.Sprintf("%06d", value%1000000), nil
}

func randomRemotePairingCredentials() (string, string, error) {
	// A 128-bit URL-safe one-use credential keeps the QR sparse enough to scan
	// quickly from the desktop while remaining far stronger than the manual
	// six-digit fallback code.
	tokenBytes := make([]byte, 16)
	codeBytes := make([]byte, 4)
	if _, err := rand.Read(tokenBytes); err != nil {
		return "", "", err
	}
	if _, err := rand.Read(codeBytes); err != nil {
		return "", "", err
	}
	value := uint32(codeBytes[0])<<24 | uint32(codeBytes[1])<<16 | uint32(codeBytes[2])<<8 | uint32(codeBytes[3])
	return base64.RawURLEncoding.EncodeToString(tokenBytes), fmt.Sprintf("%06d", value%1000000), nil
}

func remoteQuickPairURL(url, token string) string {
	// Keep the scan payload deliberately short. Fewer QR cells produce larger,
	// calmer modules at the same physical size and materially improve camera
	// acquisition from a high-DPI desktop display. Carrying the secret in the
	// fragment also prevents passive camera/link previews from consuming it:
	// fragments never reach the server until our pairing page exchanges one.
	return url + "/#p=" + token
}

func remotePairingQR(url, token string) [][]bool {
	code, err := qrcode.New(remoteQuickPairURL(url, token), qrcode.Highest)
	if err != nil {
		return nil
	}
	return code.Bitmap()
}

// rotateRemotePairingLocked replaces both user-visible credentials. The
// caller must hold remote.mu and decides separately whether device sessions
// should survive the rotation.
func (a *App) rotateRemotePairingLocked() error {
	pairToken, code, err := randomRemotePairingCredentials()
	if err != nil {
		return err
	}
	a.remote.pairToken, a.remote.PairCode = pairToken, code
	a.remote.QR = remotePairingQR(a.remote.URL, pairToken)
	return nil
}

func localIPv4() string {
	interfaces, _ := net.Interfaces()
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addresses, _ := iface.Addrs()
		for _, address := range addresses {
			ip, _, err := net.ParseCIDR(address.String())
			if err == nil && ip.To4() != nil && ip.IsPrivate() && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() {
				return ip.String()
			}
		}
	}
	return "127.0.0.1"
}

func (a *App) startRemoteLink() {
	a.remote.mu.Lock()
	if a.remote.server != nil {
		a.remote.mu.Unlock()
		return
	}
	pairToken, code, err := randomRemotePairingCredentials()
	if err != nil {
		a.remote.Error = err.Error()
		a.remote.mu.Unlock()
		return
	}
	bindIP := localIPv4()
	listener, err := net.Listen("tcp", net.JoinHostPort(bindIP, strconv.Itoa(a.config.Remote.Port)))
	if err != nil {
		a.remote.Error = err.Error()
		a.remote.mu.Unlock()
		return
	}
	mux := http.NewServeMux()
	expectedHost := net.JoinHostPort(bindIP, strconv.Itoa(a.config.Remote.Port))
	server := &http.Server{Handler: remoteHeaders(mux, expectedHost), ReadHeaderTimeout: 4 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10}
	a.remote.server, a.remote.pairToken, a.remote.PairCode = server, pairToken, code
	a.remote.handoffToken, a.remote.handoffTokenUntil = "", time.Time{}
	a.remote.Sessions, a.remote.PairFailures = make(map[string]time.Time), make(map[string]remotePairFailure)
	a.remote.Games = discoverInstalledGames()
	a.remote.ControlUntil, a.remote.ControlUntilRevoked, a.remote.Pending = time.Time{}, false, nil
	a.remote.URL = "http://" + expectedHost
	a.remote.QR = remotePairingQR(a.remote.URL, pairToken)
	a.remote.Error = ""
	a.remote.mu.Unlock()

	mux.HandleFunc("/", a.remoteHome)
	mux.HandleFunc("/pair", a.remotePair)
	mux.HandleFunc("/q", a.remoteQuickPair)
	mux.HandleFunc("/pair/qr", a.remoteQuickPair)
	// Preserve the previous route only so an already-open scanner preview gets
	// a clear one-use rejection/redirect path rather than an ambiguous 404.
	mux.HandleFunc("/quick", a.remoteQuickPair)
	mux.HandleFunc("/api/snapshot", a.remoteSnapshotAPI)
	mux.HandleFunc("/api/games", a.remoteGamesAPI)
	mux.HandleFunc("/api/action", a.remoteActionAPI)
	mux.HandleFunc("/api/screen/offer", a.remoteScreenOfferAPI)
	go func() {
		if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
			a.remote.mu.Lock()
			a.remote.Error = err.Error()
			a.remote.server = nil
			a.remote.mu.Unlock()
		}
	}()
}

// ensureRemoteLinkForMonitoring reconciles the saved preference with the live
// listener. Surge and hardware tuning deliberately do not suppress Remote Link:
// its paired dashboard is the product's monitoring surface during those runs.
func (a *App) ensureRemoteLinkForMonitoring() {
	if !a.config.Remote.Enabled || a.closing.Load() {
		return
	}
	view := a.remoteSnapshot()
	if !view.Running && view.Error == "" {
		a.startRemoteLink()
	}
}

func (a *App) stopRemoteLink() {
	a.remote.mu.Lock()
	server := a.remote.server
	a.remote.server, a.remote.pairToken, a.remote.PairCode, a.remote.URL, a.remote.QR = nil, "", "", "", nil
	a.remote.handoffToken, a.remote.handoffTokenUntil = "", time.Time{}
	a.remote.Sessions, a.remote.PairFailures, a.remote.Games, a.remote.Pending = nil, nil, nil, nil
	a.remote.ControlUntil, a.remote.ControlUntilRevoked = time.Time{}, false
	a.remote.mu.Unlock()
	a.stopScreenSessions()
	if server != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	}
}

func (a *App) remoteQuickPair(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	provided := r.URL.Query().Get("t")
	if provided == "" {
		provided = r.URL.Query().Get("token")
	}
	a.remote.mu.Lock()
	pairToken := a.remote.pairToken
	now := time.Now()
	currentToken := pairToken != "" && len(provided) == len(pairToken) && subtle.ConstantTimeCompare([]byte(provided), []byte(pairToken)) == 1
	handoffToken := a.remote.handoffToken != "" && a.remote.handoffTokenUntil.After(now) && len(provided) == len(a.remote.handoffToken) && subtle.ConstantTimeCompare([]byte(provided), []byte(a.remote.handoffToken)) == 1
	if !currentToken && !handoffToken {
		a.remote.mu.Unlock()
		http.Error(w, "This pairing link is no longer valid", http.StatusUnauthorized)
		return
	}
	session, _, err := randomRemoteCredentials()
	if err != nil {
		a.remote.mu.Unlock()
		http.Error(w, "Could not create a private session", http.StatusInternalServerError)
		return
	}
	a.remote.Sessions[session] = now.Add(8 * time.Hour)
	if currentToken {
		// Camera previews and the full browser can use separate cookie jars. Keep
		// the consumed token valid only for this short handoff, so tapping the
		// preview reaches the dashboard instead of the manual fallback page.
		if err := a.rotateRemotePairingLocked(); err != nil {
			delete(a.remote.Sessions, session)
			a.remote.mu.Unlock()
			http.Error(w, "Could not rotate the one-use pairing link", http.StatusInternalServerError)
			return
		}
		a.remote.handoffToken = provided
		a.remote.handoffTokenUntil = now.Add(45 * time.Second)
	}
	a.remotePairCodeRevealed.Store(false)
	a.remote.mu.Unlock()
	http.SetCookie(w, remoteSessionCookie(session))
	procPostMessageW.Call(a.hwnd, WM_APP_RENDER, 0, 0)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func remoteHeaders(next http.Handler, expectedHost string) http.Handler {
	_, expectedPort, _ := net.SplitHostPort(expectedHost)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.EqualFold(r.Host, expectedHost) && !strings.EqualFold(r.Host, net.JoinHostPort("localhost", expectedPort)) && !strings.EqualFold(r.Host, net.JoinHostPort("127.0.0.1", expectedPort)) {
			http.Error(w, "Invalid host", http.StatusMisdirectedRequest)
			return
		}
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; connect-src 'self'; frame-ancestors 'none'; form-action 'self'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

func (a *App) remoteAuthorized(r *http.Request) bool {
	cookie, err := r.Cookie("kerneon_session")
	if err != nil {
		return false
	}
	a.remote.mu.Lock()
	defer a.remote.mu.Unlock()
	expiry, exists := a.remote.Sessions[cookie.Value]
	if !exists || !expiry.After(time.Now()) {
		delete(a.remote.Sessions, cookie.Value)
		return false
	}
	return true
}

func remoteSessionCookie(session string) *http.Cookie {
	return &http.Cookie{Name: "kerneon_session", Value: session, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: 8 * 60 * 60}
}

func remoteClientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (a *App) remoteHome(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" || r.Method != http.MethodGet {
		http.NotFound(w, r)
		return
	}
	if !a.remoteAuthorized(r) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(remotePairHTML))
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(remoteDashboardDocument()))
}

func (a *App) remotePair(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	_ = r.ParseForm()
	provided := strings.TrimSpace(r.FormValue("code"))
	client := remoteClientIP(r)
	a.remote.mu.Lock()
	failure := a.remote.PairFailures[client]
	if failure.BlockedUntil.After(time.Now()) {
		a.remote.mu.Unlock()
		http.Error(w, "Too many attempts. Wait before trying again.", http.StatusTooManyRequests)
		return
	}
	expected := a.remote.PairCode
	if len(provided) != 6 || subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) != 1 {
		failure.Count++
		if failure.Count >= 5 {
			failure.BlockedUntil = time.Now().Add(2 * time.Minute)
			failure.Count = 0
		}
		a.remote.PairFailures[client] = failure
		a.remote.mu.Unlock()
		time.Sleep(350 * time.Millisecond)
		http.Error(w, "That code did not match. Check Kerneon and try again.", http.StatusUnauthorized)
		return
	}
	session, _, err := randomRemoteCredentials()
	if err != nil {
		a.remote.mu.Unlock()
		http.Error(w, "Could not create a private session", http.StatusInternalServerError)
		return
	}
	a.remote.Sessions[session] = time.Now().Add(8 * time.Hour)
	delete(a.remote.PairFailures, client)
	if err := a.rotateRemotePairingLocked(); err != nil {
		delete(a.remote.Sessions, session)
		a.remote.mu.Unlock()
		http.Error(w, "Could not rotate the one-use pairing code", http.StatusInternalServerError)
		return
	}
	a.remote.handoffToken, a.remote.handoffTokenUntil = "", time.Time{}
	a.remotePairCodeRevealed.Store(false)
	a.remote.mu.Unlock()
	http.SetCookie(w, remoteSessionCookie(session))
	procPostMessageW.Call(a.hwnd, WM_APP_RENDER, 0, 0)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (a *App) regenerateRemotePairing() {
	a.remote.mu.Lock()
	if a.remote.server == nil {
		a.remote.mu.Unlock()
		return
	}
	if err := a.rotateRemotePairingLocked(); err != nil {
		a.remote.Error = err.Error()
		a.remote.mu.Unlock()
		return
	}
	a.remote.handoffToken, a.remote.handoffTokenUntil = "", time.Time{}
	a.remote.Sessions = make(map[string]time.Time)
	a.remote.PairFailures = make(map[string]remotePairFailure)
	a.remote.ControlUntil, a.remote.ControlUntilRevoked = time.Time{}, false
	a.remote.Pending = nil
	a.remote.mu.Unlock()
	a.stopScreenSessions()
}

func (a *App) remoteControlGranted() bool {
	a.remote.mu.RLock()
	defer a.remote.mu.RUnlock()
	return a.remote.ControlUntilRevoked || a.remote.ControlUntil.After(time.Now())
}

type remoteSurgeProof struct {
	State              string  `json:"state"`
	Title              string  `json:"title"`
	Detail             string  `json:"detail"`
	RepeatedMetric     string  `json:"repeated_metric"`
	Ready              bool    `json:"ready"`
	Proved             bool    `json:"proved"`
	Checks             int     `json:"checks"`
	Comparable         int     `json:"comparable"`
	BaselineFPS        float64 `json:"baseline_fps"`
	AfterFPS           float64 `json:"after_fps"`
	BaselineLow        float64 `json:"baseline_low"`
	AfterLow           float64 `json:"after_low"`
	BaselineP99        float64 `json:"baseline_p99_ms"`
	AfterP99           float64 `json:"after_p99_ms"`
	BaselineHitches    float64 `json:"baseline_hitches_per_minute"`
	AfterHitches       float64 `json:"after_hitches_per_minute"`
	FPSDeltaPercent    float64 `json:"fps_delta_percent"`
	LowDeltaPercent    float64 `json:"low_delta_percent"`
	P99BetterPercent   float64 `json:"p99_better_percent"`
	HitchBetterPercent float64 `json:"hitch_better_percent"`
}

func higherIsBetterPercent(before, after float64) float64 {
	if before <= 0 || after <= 0 {
		return 0
	}
	return (after - before) / before * 100
}

func lowerIsBetterPercent(before, after float64) float64 {
	if before <= 0 || after <= 0 {
		return 0
	}
	return (before - after) / before * 100
}

func buildRemoteSurgeProof(enabled bool, auto autopilotView) remoteSurgeProof {
	proof := remoteSurgeProof{
		State: "off", Title: "Surge is off",
		Detail: "Live hardware monitoring stays on. Enable Surge with a locked game to start a measured comparison.",
		Checks: auto.FrameProofChecks, Comparable: auto.FrameComparable,
		BaselineFPS: auto.FrameBaseline.FPS, AfterFPS: auto.FrameAfter.FPS,
		BaselineLow: auto.FrameBaseline.OnePercentLow, AfterLow: auto.FrameAfter.OnePercentLow,
		BaselineP99: auto.FrameBaseline.P99FrameMs, AfterP99: auto.FrameAfter.P99FrameMs,
		BaselineHitches: auto.FrameBaseline.HitchesPerMinute, AfterHitches: auto.FrameAfter.HitchesPerMinute,
	}
	proof.FPSDeltaPercent = higherIsBetterPercent(proof.BaselineFPS, proof.AfterFPS)
	proof.LowDeltaPercent = higherIsBetterPercent(proof.BaselineLow, proof.AfterLow)
	proof.P99BetterPercent = lowerIsBetterPercent(proof.BaselineP99, proof.AfterP99)
	proof.HitchBetterPercent = lowerIsBetterPercent(proof.BaselineHitches, proof.AfterHitches)
	if !enabled {
		return proof
	}
	statusLower := strings.ToLower(auto.Status)
	if strings.Contains(statusLower, "unproven treatment withdrawn") {
		proof.State, proof.Title, proof.Ready = "not-proved", "No repeatable improvement proved", true
		proof.Detail = "The treatment did not repeat a material frame-delivery benefit, so Kerneon withdrew it and is not claiming a gain."
		return proof
	}
	if strings.Contains(statusLower, "frame regression rolled back") {
		proof.State, proof.Title, proof.Ready = "not-proved", "Regression detected and reversed", true
		proof.Detail = "Comparable frame data regressed twice, so Kerneon restored the captured pre-Surge state. No improvement is claimed."
		return proof
	}
	proof.State, proof.Title = "waiting", "Waiting for a locked game"
	proof.Detail = "The gauges remain live. Performance proof starts only after the selected game is foreground and producing frame data."
	if !auto.Active {
		return proof
	}
	proof.State, proof.Title = "baseline", "Building the before picture"
	proof.Detail = "Kerneon is recording an untreated frame-delivery window before it attributes any result to Surge."
	if !auto.FrameBaseline.Available || auto.FrameBaseline.Samples < 60 {
		return proof
	}
	proof.State, proof.Title = "measuring", "Baseline captured"
	proof.Detail = fmt.Sprintf("Measuring comparable gameplay after treatment · %d of 3 checks complete.", auto.FrameProofChecks)
	if !auto.FrameProofDone {
		return proof
	}
	proof.Ready = true
	proved, metric, count := experimentalFrameProofEarned(auto.FrameComparable, auto.FrameBenefitCounts)
	proof.RepeatedMetric = metric
	if auto.ExperimentalChanges && proved {
		proof.State, proof.Title, proof.Proved = "proved", "Surge improvement proved", true
		proof.Detail = fmt.Sprintf("%s crossed its benefit threshold in %d comparable windows. The result below is measured, not inferred from the clock change.", metric, count)
		return proof
	}
	if auto.ExperimentalChanges {
		proof.State, proof.Title = "not-proved", "No repeatable improvement proved"
		proof.Detail = fmt.Sprintf("%d comparable windows did not repeat the same material benefit. Kerneon will not claim a gain from these readings.", auto.FrameComparable)
		return proof
	}
	if auto.ActionsApplied {
		proof.State, proof.Title = "verified", "System repairs verified"
		proof.Detail = "Comparable checks found no repeatable material regression. This validates the repair session, but does not turn ordinary clock movement into a claimed FPS gain."
		return proof
	}
	proof.State, proof.Title = "observed", "No eligible repair was needed"
	proof.Detail = "Kerneon measured the session but made no performance-changing treatment, so there is no Surge uplift to claim."
	return proof
}

func (a *App) remoteSnapshotAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet || !a.remoteAuthorized(r) {
		http.Error(w, "Pair with Kerneon first", http.StatusUnauthorized)
		return
	}
	snapshot := a.engine.Snapshot()
	processes := snapshot.Processes
	if len(processes) > 6 {
		processes = processes[:6]
	}
	type remoteProcess struct {
		PID    uint32  `json:"pid"`
		Name   string  `json:"name"`
		CPU    float64 `json:"cpu"`
		RAM    uint64  `json:"ram"`
		Locked bool    `json:"locked"`
	}
	resultProcesses := make([]remoteProcess, 0, len(processes))
	for _, process := range processes {
		resultProcesses = append(resultProcesses, remoteProcess{process.PID, process.Name, process.CPU, process.WorkingSet, strings.EqualFold(process.Name, a.config.Gaming.LockedProcessName)})
	}
	auto := a.autopilotSnapshot()
	frames := a.frames.Snapshot()
	warnings, errors := a.eventCounts()
	experience := core.CalculateExperience(core.ExperienceInput{CPU: snapshot.CPU.Usage, GPU: snapshot.GPU.Usage, Memory: snapshot.Memory.UsagePercent, Disk: snapshot.Disk.Usage, DiskLatency: snapshot.Disk.LatencyMs, NetworkLatency: snapshot.Network.LatencyMs, Jitter: snapshot.Network.JitterMs, PacketLoss: snapshot.Network.PacketLoss, FPS: frames.FPS, OnePercentLow: frames.OnePercentLow, P95Display: frames.P95DisplayMs, Dropped: frames.DroppedPercent, FrameSamples: frames.Samples, RecentWarnings: warnings, RecentErrors: errors, GameActive: frames.Capturing})
	eventView := a.eventLensSnapshot()
	type remoteEvent struct {
		At       time.Time `json:"at"`
		Title    string    `json:"title"`
		Category string    `json:"category"`
		Level    string    `json:"level"`
		Score    int       `json:"score"`
	}
	remoteEvents := make([]remoteEvent, 0, 4)
	for _, event := range eventView.Events {
		if len(remoteEvents) == 4 {
			break
		}
		significance := eventSignificance(event)
		remoteEvents = append(remoteEvents, remoteEvent{event.At, event.Title, event.Category, significance.Label, significance.Score})
	}
	passport := a.hardwareSnapshot()
	remote := a.remoteSnapshot()
	screen := a.screenSnapshot()
	stack := a.surgeStack.Snapshot()
	tuning := a.tuningLabSnapshot()
	// Remote Link is a monitoring surface in its own right. Query the active
	// graphics provider here so the dials remain live even when the Surge page
	// is closed and the cached Performance Stack refresh is between samples.
	modelLower := strings.ToLower(fallback(snapshot.GPU.Model, stack.GPU.Name))
	if strings.Contains(modelLower, "nvidia") || strings.Contains(modelLower, "geforce") {
		if live := a.surgeStack.nvml.Snapshot(); live.Ready {
			stack.GPU = live
		}
	}
	if strings.Contains(modelLower, "amd") || strings.Contains(modelLower, "radeon") {
		if live := a.surgeStack.adlx.Snapshot(); live.Ready {
			gpuName := fallback(tuning.Capability.AMD.Device, snapshot.GPU.Model)
			stack.GPU.Ready, stack.GPU.Name, stack.GPU.Provider = true, gpuName, "AMD ADLX"
			stack.GPU.GraphicsClockMHz, stack.GPU.MemoryClockMHz = uint32(live.CoreClockMHz), uint32(live.VRAMClockMHz)
			stack.GPU.TemperatureC, stack.GPU.PowerW = math.Max(live.TemperatureC, live.HotspotC), live.PowerW
		}
	}
	thermalLimit := 0.0
	if envelope, ready := estimatedTuningEnvelope(a.config.Tuning.LabProfile, tuning.Capability.Estimate); ready {
		thermalLimit = envelope.TemperatureLimit
	}
	gpuName := stack.GPU.Name
	gpuProvider := stack.GPU.Provider
	gpuClockMHz, gpuMaxClockMHz := float64(stack.GPU.GraphicsClockMHz), float64(stack.GPU.MaxGraphicsClockMHz)
	vramClockMHz, vramMaxClockMHz := float64(stack.GPU.MemoryClockMHz), float64(stack.GPU.MaxMemoryClockMHz)
	gpuTemperatureC, gpuPowerW, gpuPowerLimitW := stack.GPU.TemperatureC, stack.GPU.PowerW, stack.GPU.PowerLimitW
	if !stack.GPU.Ready && tuning.Capability.AMD.Ready {
		amd := a.surgeStack.adlx.Snapshot()
		gpuName, gpuProvider = fallback(tuning.Capability.AMD.Device, snapshot.GPU.Model), "AMD ADLX"
		gpuClockMHz, vramClockMHz = float64(amd.CoreClockMHz), float64(amd.VRAMClockMHz)
		gpuMaxClockMHz, vramMaxClockMHz = float64(tuning.Capability.AMD.Core.Max), float64(tuning.Capability.AMD.VRAM.Max)
		gpuTemperatureC, gpuPowerW = math.Max(amd.TemperatureC, amd.HotspotC), amd.PowerW
		gpuPowerLimitW = tuning.PowerLimitW
	}
	a.tuningLab.mu.RLock()
	tuningOriginal := a.tuningLab.original
	a.tuningLab.mu.RUnlock()
	cpuClockBaseline := snapshot.CPU.FrequencyMHz
	if auto.HardwareBaseline.CPUClockMHz.Samples > 0 {
		cpuClockBaseline = auto.HardwareBaseline.CPUClockMHz.Value
	}
	gpuClockBaseline := gpuClockMHz
	vramClockBaseline := vramClockMHz
	if auto.HardwareBaseline.GPUClockMHz.Samples > 0 {
		gpuClockBaseline = auto.HardwareBaseline.GPUClockMHz.Value
	}
	if auto.HardwareBaseline.VRAMClockMHz.Samples > 0 {
		vramClockBaseline = auto.HardwareBaseline.VRAMClockMHz.Value
	}
	coreOffsetDelta := tuning.CoreMHz - tuningOriginal.CoreOffset
	memoryOffsetDelta := tuning.MemoryMHz - tuningOriginal.MemoryOffset
	if tuningOriginal.CoreSupported && coreOffsetDelta > 0 {
		gpuClockBaseline = math.Max(0, gpuClockMHz-float64(coreOffsetDelta))
	}
	if tuningOriginal.MemorySupported && memoryOffsetDelta > 0 {
		vramClockBaseline = math.Max(0, vramClockMHz-float64(memoryOffsetDelta))
	}
	gpuPowerBaselineW := stack.GPU.DefaultPowerLimitW
	if auto.HardwareBaseline.GPUPowerLimitW.Samples > 0 {
		gpuPowerBaselineW = auto.HardwareBaseline.GPUPowerLimitW.Value
	}
	if tuningOriginal.PowerSupported && tuningOriginal.PowerLimitMilliwatts > 0 {
		gpuPowerBaselineW = float64(tuningOriginal.PowerLimitMilliwatts) / 1000
	}
	if gpuPowerBaselineW <= 0 {
		gpuPowerBaselineW = gpuPowerLimitW
	}
	gpuTemperatureBaselineC := gpuTemperatureC
	if auto.HardwareBaseline.GPUTemperatureC.Samples > 0 {
		gpuTemperatureBaselineC = auto.HardwareBaseline.GPUTemperatureC.Value
	}
	gpuPowerMaxW := stack.GPU.MaxPowerLimitW
	if gpuPowerMaxW <= 0 {
		gpuPowerMaxW = math.Max(gpuPowerLimitW, gpuPowerBaselineW) * 1.15
	}
	if tuning.Capability.GPUOffsets.Core.Supported {
		gpuMaxClockMHz += math.Max(0, float64(tuning.Capability.GPUOffsets.Core.Max))
	}
	if tuning.Capability.GPUOffsets.Memory.Supported {
		vramMaxClockMHz += math.Max(0, float64(tuning.Capability.GPUOffsets.Memory.Max))
	}
	proof := buildRemoteSurgeProof(a.surgeIsEnabled(), auto)
	response := struct {
		At                      time.Time        `json:"at"`
		CPU                     float64          `json:"cpu"`
		GPU                     float64          `json:"gpu"`
		Memory                  float64          `json:"memory"`
		Disk                    float64          `json:"disk"`
		Down                    float64          `json:"down"`
		Up                      float64          `json:"up"`
		Latency                 float64          `json:"latency"`
		Processes               []remoteProcess  `json:"processes"`
		Autopilot               bool             `json:"autopilot"`
		GameFocus               bool             `json:"game_focus"`
		Game                    string           `json:"game"`
		Status                  string           `json:"status"`
		FPS                     float64          `json:"fps"`
		OneLow                  float64          `json:"one_low"`
		Experience              int              `json:"experience"`
		Grade                   string           `json:"grade"`
		Events                  []remoteEvent    `json:"events"`
		Hardware                string           `json:"hardware"`
		System                  string           `json:"system"`
		Control                 bool             `json:"control"`
		ControlUntil            time.Time        `json:"control_until"`
		ControlUntilRevoked     bool             `json:"control_until_revoked"`
		ScreenView              bool             `json:"screen_view"`
		ScreenInput             bool             `json:"screen_input"`
		ScreenMonitor           int              `json:"screen_monitor"`
		ScreenSessions          int              `json:"screen_sessions"`
		ScreenMonitors          []ScreenMonitor  `json:"screen_monitors"`
		CPUClockMHz             float64          `json:"cpu_clock_mhz"`
		CPUMaxMHz               float64          `json:"cpu_max_mhz"`
		CPUClockBaselineMHz     float64          `json:"cpu_clock_baseline_mhz"`
		GPUName                 string           `json:"gpu_name"`
		GPUProvider             string           `json:"gpu_provider"`
		GPUClockMHz             float64          `json:"gpu_clock_mhz"`
		GPUMaxClockMHz          float64          `json:"gpu_max_clock_mhz"`
		GPUClockBaselineMHz     float64          `json:"gpu_clock_baseline_mhz"`
		VRAMClockMHz            float64          `json:"vram_clock_mhz"`
		VRAMMaxClockMHz         float64          `json:"vram_max_clock_mhz"`
		VRAMClockBaselineMHz    float64          `json:"vram_clock_baseline_mhz"`
		GPUPowerW               float64          `json:"gpu_power_w"`
		GPUPowerLimitW          float64          `json:"gpu_power_limit_w"`
		GPUPowerBaselineW       float64          `json:"gpu_power_baseline_w"`
		GPUPowerMaxW            float64          `json:"gpu_power_max_w"`
		GPUTemperatureC         float64          `json:"gpu_temperature_c"`
		GPUTemperatureBaselineC float64          `json:"gpu_temperature_baseline_c"`
		GPUCoreOffsetMHz        int32            `json:"gpu_core_offset_mhz"`
		VRAMOffsetMHz           int32            `json:"vram_offset_mhz"`
		TuningThermalLimit      float64          `json:"tuning_thermal_limit_c"`
		TuningProgress          float64          `json:"tuning_progress"`
		TuningStage             string           `json:"tuning_stage"`
		TuningStatus            string           `json:"tuning_status"`
		TuningProfile           string           `json:"tuning_profile"`
		TuningReady             bool             `json:"tuning_ready"`
		TuningRunning           bool             `json:"tuning_running"`
		TuningEstimating        bool             `json:"tuning_estimating"`
		TuningApplied           bool             `json:"tuning_applied"`
		TuningRecovery          bool             `json:"tuning_recovery"`
		SurgeProof              remoteSurgeProof `json:"surge_proof"`
	}{
		At: snapshot.At, CPU: snapshot.CPU.Usage, GPU: snapshot.GPU.Usage, Memory: snapshot.Memory.UsagePercent,
		Disk: snapshot.Disk.Usage, Down: snapshot.Network.DownBps, Up: snapshot.Network.UpBps, Latency: snapshot.Network.LatencyMs,
		Processes: resultProcesses, Autopilot: a.surgeIsEnabled(), GameFocus: a.config.Gaming.GameFocus,
		Game: a.config.Gaming.LockedProcessName, Status: auto.Status, FPS: frames.FPS, OneLow: frames.OnePercentLow,
		Experience: experience.Overall, Grade: experience.Grade, Events: remoteEvents,
		Hardware: fallback(passport.Verdict, "Passport is still loading"), System: snapshot.System.Hostname,
		Control: remote.ControlGranted, ControlUntil: remote.ControlUntil, ControlUntilRevoked: remote.ControlUntilRevoked,
		ScreenView: screen.ViewGranted, ScreenInput: screen.InputGranted, ScreenMonitor: screen.Monitor, ScreenSessions: screen.ActiveSessions, ScreenMonitors: screen.Monitors,
		CPUClockMHz: snapshot.CPU.FrequencyMHz, CPUMaxMHz: snapshot.CPU.MaxMHz, CPUClockBaselineMHz: cpuClockBaseline,
		GPUName: gpuName, GPUProvider: gpuProvider, GPUClockMHz: gpuClockMHz, GPUMaxClockMHz: gpuMaxClockMHz,
		GPUClockBaselineMHz: gpuClockBaseline, VRAMClockMHz: vramClockMHz, VRAMMaxClockMHz: vramMaxClockMHz, VRAMClockBaselineMHz: vramClockBaseline,
		GPUPowerW: gpuPowerW, GPUPowerLimitW: gpuPowerLimitW, GPUPowerBaselineW: gpuPowerBaselineW, GPUPowerMaxW: gpuPowerMaxW,
		GPUTemperatureC: gpuTemperatureC, GPUTemperatureBaselineC: gpuTemperatureBaselineC,
		GPUCoreOffsetMHz: tuning.CoreMHz, VRAMOffsetMHz: tuning.MemoryMHz, TuningThermalLimit: thermalLimit,
		TuningProgress: tuning.Progress, TuningStage: tuning.Stage, TuningStatus: tuning.Status, TuningProfile: a.config.Tuning.LabProfile,
		TuningReady: tuning.Capability.Estimate.Ready, TuningRunning: tuning.Running, TuningEstimating: tuning.Estimating, TuningApplied: tuning.Applied, TuningRecovery: tuning.Recovery,
		SurgeProof: proof,
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(response)
}

func (a *App) remoteGamesAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet || !a.remoteAuthorized(r) {
		http.Error(w, "Pair with Kerneon first", http.StatusUnauthorized)
		return
	}
	a.remote.mu.RLock()
	games := append([]InstalledGame(nil), a.remote.Games...)
	a.remote.mu.RUnlock()
	type publicGame struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		Provider string `json:"provider"`
	}
	result := make([]publicGame, 0, len(games))
	for _, game := range games {
		result = append(result, publicGame{game.ID, game.Name, game.Provider})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result)
}

func (a *App) remoteActionAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !a.remoteAuthorized(r) || r.Header.Get("X-Kerneon-Request") != "remote-link" || !a.remoteSameOrigin(r) {
		http.Error(w, "Not authorized", http.StatusUnauthorized)
		return
	}
	var body struct {
		Action string `json:"action"`
		Target string `json:"target"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2048)).Decode(&body); err != nil || len(body.Target) > 256 {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}
	if !a.remoteControlGranted() {
		http.Error(w, "Remote control is locked. Grant control on the PC.", http.StatusForbidden)
		return
	}
	switch body.Action {
	case "capture", "autopilot-on", "autopilot-off", "focus-on", "focus-off", "game-unlock":
		body.Target = ""
	case "launch-game":
		if _, ok := a.remoteGameByID(body.Target); !ok {
			http.Error(w, "Game is not in the verified installed library", http.StatusBadRequest)
			return
		}
	case "lock-process":
		pid, err := strconv.ParseUint(body.Target, 10, 32)
		if err != nil {
			http.Error(w, "Invalid process", http.StatusBadRequest)
			return
		}
		if _, ok := a.processByPID(uint32(pid)); !ok {
			http.Error(w, "That process is no longer running", http.StatusConflict)
			return
		}
	default:
		http.Error(w, "Action is not in the remote allowlist", http.StatusBadRequest)
		return
	}
	a.remote.mu.Lock()
	if a.remote.Pending != nil {
		a.remote.mu.Unlock()
		http.Error(w, "Kerneon is handling another command", http.StatusConflict)
		return
	}
	a.remote.Pending = &RemoteCommand{Action: body.Action, Target: body.Target, RemoteIP: remoteClientIP(r)}
	a.remote.mu.Unlock()
	procPostMessageW.Call(a.hwnd, WM_APP_REMOTE, 0, 0)
	w.WriteHeader(http.StatusAccepted)
}

func (a *App) remoteSameOrigin(r *http.Request) bool {
	a.remote.mu.RLock()
	expected := a.remote.URL
	a.remote.mu.RUnlock()
	origin := strings.TrimSuffix(r.Header.Get("Origin"), "/")
	return expected != "" && origin == expected && (r.Header.Get("Sec-Fetch-Site") == "" || r.Header.Get("Sec-Fetch-Site") == "same-origin")
}

func (a *App) remoteGameByID(id string) (InstalledGame, bool) {
	a.remote.mu.RLock()
	defer a.remote.mu.RUnlock()
	for _, game := range a.remote.Games {
		if game.ID == id {
			return game, true
		}
	}
	return InstalledGame{}, false
}

func (a *App) handleRemoteAction() {
	a.remote.mu.Lock()
	command := a.remote.Pending
	a.remote.Pending = nil
	a.remote.mu.Unlock()
	if command == nil || !a.remoteControlGranted() {
		return
	}
	status := "accepted"
	switch command.Action {
	case "capture":
		a.runAction("capture-incident", 0)
	case "autopilot-on":
		if !a.surgeIsEnabled() {
			a.runAction("toggle-autopilot", 0)
		}
	case "autopilot-off":
		if a.surgeIsEnabled() {
			a.runAction("toggle-autopilot", 0)
		}
	case "focus-on":
		if !a.config.Gaming.GameFocus {
			a.runAction("toggle-gamefocus", 0)
		}
	case "focus-off":
		if a.config.Gaming.GameFocus {
			a.runAction("toggle-gamefocus", 0)
		}
	case "launch-game":
		if game, ok := a.remoteGameByID(command.Target); ok {
			shellOpen(game.LaunchTarget)
		} else {
			status = "refused: library target disappeared"
		}
	case "lock-process":
		pid, _ := strconv.ParseUint(command.Target, 10, 32)
		if process, ok := a.processByPID(uint32(pid)); ok {
			a.restoreAutopilotSession("Remote game selection changed")
			a.config.Gaming.LockedProcessName = process.Name
			a.saveAndRestart(false)
		} else {
			status = "refused: process ended"
		}
	case "game-unlock":
		a.restoreAutopilotSession("Remote game lock removed")
		a.config.Gaming.LockedProcessName = ""
		a.saveAndRestart(false)
	}
	a.recordRemoteAudit(*command, status)
}

func (a *App) recordRemoteAudit(command RemoteCommand, status string) {
	if a.logger == nil {
		return
	}
	record := struct {
		At, RemoteIP, Action, Target, Status string
	}{time.Now().UTC().Format(time.RFC3339Nano), command.RemoteIP, command.Action, command.Target, status}
	data, err := json.Marshal(record)
	if err != nil {
		return
	}
	dir := filepath.Join(a.logger.Dir(), "remote-audit")
	if os.MkdirAll(dir, 0o700) != nil {
		return
	}
	file, err := os.OpenFile(filepath.Join(dir, "remote-"+time.Now().Format("20060102")+".jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	_, _ = file.Write(append(data, '\n'))
	_ = file.Sync()
	_ = file.Close()
}

const remoteMarkSVG = `<svg class="brandMark" width="100%" height="100%" viewBox="0 0 100 100" role="img" aria-label="Kerneon"><path d="M44.4 82A32.5 32.5 0 0 1 44.4 18M48 50L77 24.5M48 50L77 75.5" fill="none" stroke="#e0f4f6" stroke-opacity=".08" stroke-width="11" stroke-linecap="round" stroke-linejoin="round"/><path d="M44.4 82A32.5 32.5 0 0 1 44.4 18M48 50L77 24.5M48 50L77 75.5" fill="none" stroke="#e0f4f6" stroke-width="7" stroke-linecap="round" stroke-linejoin="round"/></svg>`

const remotePairHTML = `<!doctype html><html lang="en"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Kerneon · Pairing</title><script>if(location.hash.indexOf('#p=')===0)document.documentElement.className='scanBoot'</script><style>
:root{color-scheme:dark;font-family:Inter,"Segoe UI",sans-serif;background:#0b0d10;color:#f2f5f7}*{box-sizing:border-box}body{margin:0;min-height:100svh;display:grid;place-items:center;padding:24px;background:radial-gradient(800px 500px at 10% 0%,#122128 0%,transparent 55%),#0b0d10}.pair{width:min(440px,100%);padding:30px;border:1px solid #263139;border-radius:26px;background:#11161acc;backdrop-filter:blur(22px)}.eyebrow{color:#86e2e8;font-size:11px;font-weight:750;letter-spacing:.8px;text-transform:uppercase;margin-bottom:15px}.brandMark{display:block;width:42px;height:42px;margin-bottom:26px}h1{font-size:28px;letter-spacing:-.7px;margin:0 0 10px}p{color:#95a2aa;line-height:1.55;margin:0 0 24px}input{width:100%;height:58px;border:1px solid #334149;border-radius:16px;background:#0b0f12;color:white;text-align:center;font:600 24px ui-monospace;letter-spacing:8px;outline:none}input:focus{border-color:#7ddce4;box-shadow:0 0 0 4px #7ddce414}button{width:100%;height:50px;margin-top:12px;border:0;border-radius:16px;background:#dffcff;color:#071013;font-weight:700;font-size:15px}.fine{font-size:11px;color:#6f7e85;margin:14px 0 0}.scan{display:none}.scanBoot .manual{display:none}.scanBoot .scan{display:block}.scanError{color:#e6c48b}</style><main class="pair">` + remoteMarkSVG + `<section class="scan"><div class="eyebrow">Secure local pairing</div><h1>Connecting to Kerneon…</h1><p>Your device is exchanging the scanned credential for a private session. No code entry is required.</p><p class="fine scanError" hidden>The scan could not be completed. Rescan the current QR code or use the manual fallback.</p></section><section class="manual"><div class="eyebrow">Manual fallback</div><h1>Enter the pairing code</h1><p>Use this only when this device cannot scan Kerneon's QR code. Scanning pairs immediately and never asks you to type these digits.</p><form method="post" action="/pair"><input name="code" inputmode="numeric" pattern="[0-9]{6}" maxlength="6" autocomplete="one-time-code" autofocus aria-label="Six-digit manual pairing code"><button>Connect manually</button></form><p class="fine">Pairing remains on your local network and ends when Kerneon closes or access is revoked.</p></section></main><script>(function(){var q=new URLSearchParams(location.hash.slice(1)),t=q.get('p');if(!/^[A-Za-z0-9_-]{22}$/.test(t||''))return;fetch('/q?t='+encodeURIComponent(t),{credentials:'same-origin',cache:'no-store'}).then(function(r){if(!r.ok)throw new Error('pairing refused');location.replace('/')}).catch(function(){history.replaceState(null,'',location.pathname);document.documentElement.className='';document.querySelector('.scanError').hidden=false;document.querySelector('.manual').style.display='none';document.querySelector('.scan').style.display='block'})})()</script></html>`

const remoteDashboardHTML = `<!doctype html><html lang="en"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1,viewport-fit=cover"><title>Kerneon Link</title><style>
:root{color-scheme:dark;font-family:"Segoe UI Variable Text","Segoe UI",sans-serif;background:#080b0d;color:#eef3f4;--muted:#91a0a7;--quiet:#6f7e85;--line:#263239;--line2:#303f47;--glass:rgba(17,23,27,.92);--soft:rgba(22,31,36,.78);--cyan:#89e5eb;--green:#8bd4ad;--amber:#e6c48b;--violet:#a9b0fb}*{box-sizing:border-box}html{min-height:100%;background:#080b0d}body{margin:0;min-height:100svh;background:radial-gradient(880px 540px at -8% -10%,#18313a 0%,transparent 60%),radial-gradient(760px 480px at 108% 8%,#1c2036 0%,transparent 58%),#080b0d}button,a{-webkit-tap-highlight-color:transparent}.shell{width:min(1220px,100%);margin:auto;padding:max(20px,env(safe-area-inset-top)) clamp(16px,4vw,46px) max(28px,env(safe-area-inset-bottom))}header{display:flex;align-items:center;justify-content:space-between;gap:18px;padding:6px 1px 28px}.brand{display:flex;align-items:center;gap:13px}.mark{position:relative;width:38px;height:38px;border-radius:50%;background:#11191d;border:1px solid #40525a;box-shadow:inset 0 0 0 7px #0b1013}.mark:after{content:"";position:absolute;inset:9px;border:3px solid var(--cyan);border-left-color:transparent;border-radius:50%;transform:rotate(-28deg)}.product{font-size:18px;font-weight:670;letter-spacing:-.35px}.product span{color:var(--muted);font-weight:450}.live{font-size:12px;color:var(--green);display:flex;align-items:center;gap:8px}.pulse{width:7px;height:7px;border-radius:50%;background:currentColor;box-shadow:0 0 0 5px rgba(139,212,173,.1)}.hero{display:flex;align-items:end;justify-content:space-between;gap:24px;margin-bottom:26px}.hero h1{font-size:clamp(31px,5.5vw,58px);line-height:.98;letter-spacing:-2.4px;margin:0;max-width:760px}.hero p{color:var(--muted);line-height:1.5;max-width:390px;margin:0 0 3px}.nav{position:sticky;top:10px;z-index:3;display:flex;gap:5px;overflow:auto;padding:6px;margin-bottom:18px;border:1px solid var(--line);border-radius:17px;background:rgba(11,15,18,.88);backdrop-filter:blur(20px);scrollbar-width:none}.nav::-webkit-scrollbar{display:none}.nav button,.button{appearance:none;border:1px solid transparent;border-radius:12px;background:transparent;color:var(--muted);font:600 13px inherit;padding:10px 14px;white-space:nowrap;cursor:pointer;transition:background .12s ease,color .12s ease,border-color .12s ease,transform .08s ease}.nav button:hover,.button:hover{background:#172025;color:#f0f5f6}.nav button.active{background:#dffcff;color:#071013}.button{border-color:var(--line2);background:#151d21;color:#e8eff1;text-align:center}.button.primary{background:#dffcff;color:#071013;border-color:transparent}.button.cyan{border-color:#45656b;color:var(--cyan)}.button:active{transform:scale(.985)}.button:disabled{opacity:.38;cursor:not-allowed;transform:none}.notice{display:none;margin:0 0 12px;padding:12px 15px;border:1px solid #664e2f;border-radius:13px;background:#2b2117;color:#edcc96;font-size:13px}.notice.show{display:block}.view{display:none;animation:arrive .16s ease-out}.view.active{display:block}@keyframes arrive{from{opacity:.35;transform:translateY(5px)}to{opacity:1;transform:none}}.metrics{display:grid;grid-template-columns:1.3fr repeat(5,1fr);gap:10px}.card,.metric{border:1px solid var(--line);border-radius:19px;background:var(--glass);backdrop-filter:blur(18px)}.metric{padding:17px;min-width:0}.metric.heroMetric{background:linear-gradient(145deg,rgba(31,53,59,.96),rgba(16,24,28,.96))}.label{color:var(--muted);font-size:11px;font-weight:600;letter-spacing:.15px}.value{font:650 clamp(22px,3.5vw,34px) "Segoe UI Variable Display","Segoe UI",sans-serif;letter-spacing:-1.15px;margin-top:11px;overflow:hidden;text-overflow:ellipsis}.heroMetric .value{color:var(--cyan)}.sub{color:var(--quiet);font-size:11px;margin-top:6px;white-space:nowrap;overflow:hidden;text-overflow:ellipsis}.layout{display:grid;grid-template-columns:minmax(0,1.45fr) minmax(280px,.75fr);gap:11px;margin-top:11px}.card{padding:20px;min-width:0}.cardTitle{display:flex;justify-content:space-between;align-items:start;gap:16px;margin-bottom:14px}.cardTitle h2{font-size:16px;letter-spacing:-.25px;margin:0}.cardTitle p{color:var(--quiet);font-size:12px;margin:4px 0 0}.badge{border:1px solid var(--line2);border-radius:999px;color:var(--muted);font-size:10px;font-weight:700;padding:5px 8px;white-space:nowrap}.badge.good{color:var(--green);border-color:#365847}.badge.cyan{color:var(--cyan);border-color:#3d5e64}.rows{display:grid}.row{display:grid;grid-template-columns:minmax(0,1fr) auto auto auto;gap:12px;align-items:center;min-height:52px;border-top:1px solid #202a30;font-size:13px}.row:first-child{border-top:0}.rowName{overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.mono{color:var(--muted);font:12px ui-monospace,"Cascadia Mono",monospace}.tiny{padding:7px 9px;font-size:11px}.controlGrid{display:grid;grid-template-columns:1fr 1fr;gap:8px}.controlGrid .wide{grid-column:1/-1}.statusCopy{color:var(--muted);font-size:13px;line-height:1.55;margin:4px 0 18px}.lease{padding:12px 14px;border-radius:13px;background:#10171a;color:var(--quiet);font-size:12px;margin-top:11px}.lease.on{background:#122226;color:var(--cyan)}.sectionIntro{margin:4px 0 18px;color:var(--muted);line-height:1.55;max-width:720px}.gameGrid{display:grid;grid-template-columns:repeat(3,minmax(0,1fr));gap:10px}.game{display:flex;min-height:132px;flex-direction:column;justify-content:space-between;padding:17px;border:1px solid var(--line);border-radius:17px;background:linear-gradient(150deg,#141d21,#0f1417)}.game strong{font-size:15px}.provider{color:var(--violet);font-size:10px;font-weight:700;text-transform:uppercase;letter-spacing:.8px;margin-bottom:8px}.game .button{margin-top:16px;width:100%}.events{display:grid;gap:9px}.event{display:grid;grid-template-columns:64px minmax(0,1fr) auto;gap:13px;align-items:center;padding:14px;border:1px solid var(--line);border-radius:15px;background:#11171a}.score{height:44px;width:44px;display:grid;place-items:center;border-radius:50%;border:1px solid #47575e;color:var(--amber);font:650 13px ui-monospace}.eventTitle{font-size:13px;font-weight:620}.eventMeta{color:var(--quiet);font-size:11px;margin-top:5px}.systemGrid{display:grid;grid-template-columns:1fr 1fr;gap:10px}.fact{padding:18px;border:1px solid var(--line);border-radius:16px;background:#11171a}.fact strong{display:block;margin-top:7px;font-size:15px}.screen{max-width:1100px}.screenToolbar{display:flex;align-items:center;gap:9px;flex-wrap:wrap;margin:16px 0 10px}.screenToolbar select,.screenType input{min-height:42px;border:1px solid var(--line2);border-radius:12px;background:#11191d;color:#eaf1f2;padding:9px 12px;font:600 13px inherit}.screenToolbar select{min-width:180px}.screenStatus{color:var(--muted);font-size:12px;margin-left:auto}.screenStage{position:relative;display:grid;place-items:center;min-height:280px;padding:10px;border:1px solid var(--line);border-radius:19px;background:radial-gradient(600px 320px at 50% 10%,#16262c,#080b0d 72%);overflow:hidden}.screenStage canvas{display:block;max-width:100%;max-height:68vh;width:auto;height:auto;border-radius:11px;outline:none;touch-action:none;box-shadow:0 18px 60px #0008}.screenStage canvas.control{cursor:crosshair}.screenPlaceholder{position:absolute;color:var(--quiet);text-align:center;line-height:1.6;pointer-events:none}.screenType{display:grid;grid-template-columns:1fr auto;gap:8px;margin-top:10px}.screenType input{width:100%}.screenTrust{display:grid;grid-template-columns:1fr 1fr;gap:10px;margin-top:10px}.screenTrust div{padding:13px 15px;border:1px solid var(--line);border-radius:14px;background:#101619;color:var(--muted);font-size:12px;line-height:1.45}.screenTrust b{display:block;color:var(--cyan);font-size:10px;margin-bottom:5px}.fine{color:var(--quiet);font-size:11px;line-height:1.5;margin-top:16px}.empty{padding:32px 14px;text-align:center;color:var(--quiet);font-size:13px}.locked{font-size:12px;color:var(--amber);margin:0 0 10px}.mobileOnly{display:none}@media(max-width:900px){.metrics{grid-template-columns:repeat(3,1fr)}.layout{grid-template-columns:1fr}.gameGrid{grid-template-columns:repeat(2,1fr)}}@media(max-width:620px){.shell{padding-left:14px;padding-right:14px}.hero{align-items:start;flex-direction:column}.hero h1{font-size:39px;letter-spacing:-1.5px}.hero p{font-size:13px}.metrics{grid-template-columns:1fr 1fr}.metric{padding:14px}.metrics .heroMetric{grid-column:1/-1}.layout{margin-top:9px}.card{padding:16px}.gameGrid,.systemGrid,.screenTrust{grid-template-columns:1fr}.row{grid-template-columns:minmax(0,1fr) auto auto;gap:8px}.row .ram{display:none}.event{grid-template-columns:50px minmax(0,1fr)}.event time{display:none}.screenToolbar>*{flex:1 1 100%}.screenStatus{margin:0}.screenStage{min-height:220px;padding:5px}.mobileOnly{display:block}}@media(prefers-reduced-motion:reduce){*{animation:none!important;transition:none!important}}</style>
<div class="shell"><header><div class="brand"><span style="display:block;width:38px;height:38px;flex:none">` + remoteMarkSVG + `</span><div class="product">Kerneon <span>Link</span></div></div><div class="live" id="live"><i class="pulse"></i><span>Connecting</span></div></header><section class="hero"><h1>Your PC still feels close.</h1><p>Live context, your verified game library, and deliberately limited control—without a cloud relay.</p></section><nav class="nav" aria-label="Remote sections"><button class="active" data-view="overview">Overview</button><button data-view="games">Games</button><button data-view="events">Event Lens</button><button data-view="system">System</button><button data-view="screen">Screen</button></nav><div class="notice" id="notice"></div>
<section class="view active" id="view-overview"><div class="metrics"><div class="metric heroMetric"><div class="label">Experience score</div><div class="value" id="experience">—</div><div class="sub" id="grade">Measured now</div></div><div class="metric"><div class="label">Frame rate</div><div class="value" id="fps">—</div><div class="sub" id="low">1% low —</div></div><div class="metric"><div class="label">CPU</div><div class="value" id="cpu">—</div><div class="sub">Processor demand</div></div><div class="metric"><div class="label">GPU</div><div class="value" id="gpu">—</div><div class="sub">Graphics demand</div></div><div class="metric"><div class="label">Memory</div><div class="value" id="memory">—</div><div class="sub">Physical use</div></div><div class="metric"><div class="label">Disk</div><div class="value" id="disk">—</div><div class="sub">Active time</div></div></div><div class="layout"><article class="card"><div class="cardTitle"><div><h2>What is active</h2><p>Top live consumers; select one as the game process.</p></div><span class="badge" id="gameBadge">No game locked</span></div><div class="rows" id="processes"><div class="empty">Reading processes…</div></div></article><article class="card"><div class="cardTitle"><div><h2>Surge</h2><p>Measured regulation, never a blind boost.</p></div><span class="badge cyan">LOCAL GRANT</span></div><div class="statusCopy" id="status">Reading Kerneon…</div><div class="locked" id="controlHint">Controls are locked on the PC.</div><div class="controlGrid"><button class="button primary remoteAction" id="auto">Surge</button><button class="button remoteAction" id="focus">Game Focus</button><button class="button wide remoteAction" id="capture">Capture the last 60 seconds</button><button class="button wide remoteAction" id="unlock">Release selected game</button></div><div class="lease" id="lease">Grant control in Kerneon to make changes.</div></article></div></section>
<section class="view" id="view-games"><div class="cardTitle"><div><h2>Verified game library</h2><p class="sectionIntro">Games found in installed Steam and Epic manifests. Remote Link can launch only these allowlisted entries.</p></div><span class="badge" id="gameCount">0 GAMES</span></div><div class="gameGrid" id="games"><div class="empty">Reading installed libraries…</div></div><div class="card" style="margin-top:11px"><div class="cardTitle"><div><h2>Running now</h2><p>Lock the process you actually want Kerneon to follow.</p></div></div><div class="rows" id="gameProcesses"></div></div></section>
<section class="view" id="view-events"><div class="cardTitle"><div><h2>Event Lens</h2><p class="sectionIntro">Recent context with significance already separated from routine Windows noise.</p></div></div><div class="events" id="events"><div class="empty">Reading recent context…</div></div></section>
<section class="view" id="view-system"><div class="systemGrid"><div class="fact"><span class="label">Hardware Passport</span><strong id="hardware">Loading identity evidence…</strong></div><div class="fact"><span class="label">Computer</span><strong id="systemName">—</strong></div><div class="fact"><span class="label">Network down</span><strong id="down">—</strong></div><div class="fact"><span class="label">Network up</span><strong id="up">—</strong></div><div class="fact"><span class="label">Latency</span><strong id="latency">—</strong></div><div class="fact"><span class="label">Remote trust</span><strong>Paired device + explicit local grant</strong></div></div><p class="fine">Remote Link listens only on this PC's private network address. It exposes no arbitrary command shell or file browser. Screen and input require their own visible grants.</p></section>
<section class="view screen" id="view-screen"><h2>Kerneon Session</h2><p class="sectionIntro">A Kerneon-native, encrypted WebRTC preview. The PC grants screen viewing and keyboard/pointer access separately; the global sharing pill revokes both immediately.</p><div class="screenToolbar"><button class="button primary" id="screenConnect">Connect encrypted view</button><select id="screenMonitor" aria-label="Display to view"><option>Reading displays…</option></select><span class="screenStatus" id="screenStatus">Waiting to connect</span></div><div class="screenStage" id="screenStage"><canvas id="screenCanvas" tabindex="0" aria-label="Remote computer screen"></canvas><div class="screenPlaceholder" id="screenPlaceholder">No pixels leave the PC until<br>screen viewing is approved locally.</div></div><div class="screenType"><input id="screenText" type="text" autocomplete="off" placeholder="Type text on the PC" aria-label="Text to type remotely"><button class="button" id="screenSendText">Type on PC</button></div><div class="screenTrust"><div><b>VIEW BOUNDARY</b>Captured JPEG frames travel only inside the WebRTC DTLS data channel. Pairing by itself sees no pixels.</div><div><b>INPUT BOUNDARY</b>Touch, mouse and keyboard events are ignored until the separate local input grant is active.</div></div><p class="fine">Designed for current mobile and desktop browsers on the same trusted private network. Windows secure-desktop/UAC surfaces and protected content are intentionally not captured.</p></section></div>
<script>
'use strict';let state={},games=[],activeView='overview',screenPC=null,screenFrames=null,screenInput=null,screenDecoding=false;const $=id=>document.getElementById(id);const esc=v=>String(v==null?'':v).replace(/[&<>"']/g,ch=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[ch]));const pct=v=>(Number(v)||0).toFixed(1)+'%';function rate(value){let n=Math.max(0,Number(value)||0),units=['B/s','KB/s','MB/s','GB/s','TB/s'],i=0;while(n>=1024&&i<units.length-1){n/=1024;i++}const digits=i===0?0:n>=100?0:n>=10?1:2;return n.toFixed(digits)+' '+units[i]}function memory(value){let n=Math.max(0,Number(value)||0),units=['B','KB','MB','GB','TB'],i=0;while(n>=1024&&i<units.length-1){n/=1024;i++}return n.toFixed(i===0?0:n>=10?1:2)+' '+units[i]}function showNotice(message,good){const el=$('notice');el.textContent=message||'';el.style.borderColor=good?'#355b49':'';el.style.background=good?'#12231b':'';el.style.color=good?'#9cddb9':'';el.classList.toggle('show',Boolean(message));if(message)setTimeout(()=>{if(el.textContent===message)el.classList.remove('show')},4200)}function switchView(name){activeView=name;document.querySelectorAll('.nav button').forEach(b=>b.classList.toggle('active',b.dataset.view===name));document.querySelectorAll('.view').forEach(v=>v.classList.toggle('active',v.id==='view-'+name));if(name==='games'&&!games.length)loadGames();if(name==='screen'&&!screenPC)connectScreen()}document.querySelectorAll('.nav button').forEach(b=>b.onclick=()=>switchView(b.dataset.view));
function renderProcesses(){const items=Array.isArray(state.processes)?state.processes:[];const html=items.map(p=>'<div class="row"><span class="rowName">'+esc(p.name)+(p.locked?' <span class="badge good">LOCKED</span>':'')+'</span><span class="mono">'+pct(p.cpu)+'</span><span class="mono ram">'+memory(p.ram)+'</span><button class="button tiny processLock remoteAction" data-pid="'+Number(p.pid)+'" '+(state.control?'':'disabled')+'>'+(p.locked?'Selected':'Use as game')+'</button></div>').join('');$('processes').innerHTML=html||'<div class="empty">No process samples yet.</div>';$('gameProcesses').innerHTML=html||'<div class="empty">No process samples yet.</div>';document.querySelectorAll('.processLock').forEach(b=>b.onclick=()=>act('lock-process',b.dataset.pid));}
function renderEvents(){const items=Array.isArray(state.events)?state.events:[];$('events').innerHTML=items.map(e=>'<article class="event"><div class="score">'+Number(e.score||0)+'</div><div><div class="eventTitle">'+esc(e.title)+'</div><div class="eventMeta">'+esc(e.category)+' · '+esc(e.level)+'</div></div><time class="mono">'+new Date(e.at).toLocaleTimeString([], {hour:'2-digit',minute:'2-digit'})+'</time></article>').join('')||'<div class="empty">No notable recent events. Routine noise stays quiet.</div>'}
function renderScreenState(){const monitors=Array.isArray(state.screen_monitors)?state.screen_monitors:[],select=$('screenMonitor'),previous=String(state.screen_monitor||0);if(select.options.length!==monitors.length||[...select.options].some((o,i)=>Number(o.value)!==Number(monitors[i]?.index))){select.innerHTML=monitors.map(m=>'<option value="'+Number(m.index)+'">'+esc((m.primary?'Primary · ':'')+m.name+' · '+m.width+'×'+m.height)+'</option>').join('')||'<option>No displays available</option>'}select.value=previous;const connected=screenFrames&&screenFrames.readyState==='open',view=Boolean(state.screen_view),input=Boolean(state.screen_input),status=$('screenStatus'),canvas=$('screenCanvas'),placeholder=$('screenPlaceholder');$('screenConnect').textContent=connected?'Reconnect encrypted view':'Connect encrypted view';$('screenText').disabled=!input;$('screenSendText').disabled=!input;canvas.classList.toggle('control',input);if(!view){status.textContent=connected?'Connected · waiting for view approval on the PC':'View is locked on the PC';placeholder.style.display='block';canvas.style.opacity='.25'}else{status.textContent=input?'Encrypted view + input active':'Encrypted view active · input locked';canvas.style.opacity='1';if(canvas.width>300)placeholder.style.display='none'}}
function waitForICE(pc){if(pc.iceGatheringState==='complete')return Promise.resolve();return new Promise(resolve=>{const timeout=setTimeout(resolve,5000);pc.addEventListener('icegatheringstatechange',()=>{if(pc.iceGatheringState==='complete'){clearTimeout(timeout);resolve()}},{once:false})})}
function closeScreen(){if(screenPC){screenPC.close()}screenPC=null;screenFrames=null;screenInput=null;renderScreenState()}
async function connectScreen(){closeScreen();if(!window.RTCPeerConnection){$('screenStatus').textContent='This browser does not support WebRTC';return}try{const pc=new RTCPeerConnection({iceServers:[]});screenPC=pc;screenFrames=pc.createDataChannel('kerneon-screen',{ordered:false,maxRetransmits:0});screenFrames.binaryType='arraybuffer';screenInput=pc.createDataChannel('kerneon-input');screenFrames.onopen=()=>{$('screenStatus').textContent='Encrypted channel connected';renderScreenState()};screenFrames.onclose=()=>{$('screenStatus').textContent='Encrypted channel closed'};screenFrames.onmessage=screenFrameMessage;pc.onconnectionstatechange=()=>{if(['failed','closed','disconnected'].includes(pc.connectionState)){$('screenStatus').textContent='Screen link '+pc.connectionState}};const offer=await pc.createOffer();await pc.setLocalDescription(offer);await waitForICE(pc);const response=await fetch('/api/screen/offer',{method:'POST',headers:{'Content-Type':'application/json','X-Kerneon-Request':'remote-link'},body:JSON.stringify(pc.localDescription)});if(!response.ok)throw new Error((await response.text()).trim()||'Screen offer refused');await pc.setRemoteDescription(await response.json());$('screenStatus').textContent='Negotiating encrypted channel…'}catch(error){closeScreen();$('screenStatus').textContent=error.message||'Could not connect the screen view'}}
async function screenFrameMessage(event){if(typeof event.data==='string'){try{const update=JSON.parse(event.data);if(update.type==='status'){state.screen_view=Boolean(update.View??update.view);state.screen_input=Boolean(update.Input??update.input);state.screen_monitor=Number(update.Monitor??update.monitor)||0;state.screen_monitors=update.Monitors||update.monitors||state.screen_monitors;renderScreenState()}}catch{}return}if(screenDecoding)return;screenDecoding=true;try{const bitmap=await createImageBitmap(new Blob([event.data],{type:'image/jpeg'})),canvas=$('screenCanvas'),ctx=canvas.getContext('2d',{alpha:false});canvas.width=bitmap.width;canvas.height=bitmap.height;ctx.drawImage(bitmap,0,0);bitmap.close();$('screenPlaceholder').style.display='none'}finally{screenDecoding=false}}
function sendScreen(message){if(screenInput&&screenInput.readyState==='open')screenInput.send(JSON.stringify(message))}
function pointerMessage(event,action){if(!state.screen_input)return;const rect=$('screenCanvas').getBoundingClientRect();if(rect.width<1||rect.height<1)return;sendScreen({type:'pointer',action:action,x:Math.max(0,Math.min(1,(event.clientX-rect.left)/rect.width)),y:Math.max(0,Math.min(1,(event.clientY-rect.top)/rect.height)),button:event.button,delta:0})}
const screenCanvas=$('screenCanvas');screenCanvas.onpointermove=e=>pointerMessage(e,'move');screenCanvas.onpointerdown=e=>{screenCanvas.focus();screenCanvas.setPointerCapture?.(e.pointerId);pointerMessage(e,'down');e.preventDefault()};screenCanvas.onpointerup=e=>{pointerMessage(e,'up');e.preventDefault()};screenCanvas.oncontextmenu=e=>e.preventDefault();screenCanvas.onwheel=e=>{if(state.screen_input){const rect=screenCanvas.getBoundingClientRect();sendScreen({type:'pointer',action:'wheel',x:(e.clientX-rect.left)/rect.width,y:(e.clientY-rect.top)/rect.height,button:0,delta:-Math.sign(e.deltaY)});e.preventDefault()}};screenCanvas.onkeydown=e=>{if(state.screen_input){sendScreen({type:'key',action:'down',code:e.code});e.preventDefault()}};screenCanvas.onkeyup=e=>{if(state.screen_input){sendScreen({type:'key',action:'up',code:e.code});e.preventDefault()}};$('screenMonitor').onchange=e=>sendScreen({type:'monitor',monitor:Number(e.target.value)});$('screenConnect').onclick=connectScreen;$('screenSendText').onclick=()=>{const input=$('screenText'),text=input.value;if(text&&state.screen_input){sendScreen({type:'text',text:text});input.value=''}};$('screenText').onkeydown=e=>{if(e.key==='Enter'){$('screenSendText').click();e.preventDefault()}};
function render(){['cpu','gpu','memory','disk'].forEach(k=>$(k).textContent=pct(state[k]));$('experience').textContent=Number(state.experience||0)+'/100';$('grade').textContent=(state.grade||'—')+' · weighted experience';$('fps').textContent=state.fps>0?Number(state.fps).toFixed(1):'—';$('low').textContent='1% low '+(state.one_low>0?Number(state.one_low).toFixed(1):'—');$('status').textContent=state.status||'Surge is observing without intervention.';$('auto').textContent='Surge · '+(state.autopilot?'on':'off');$('focus').textContent='Game Focus · '+(state.game_focus?'on':'off');$('gameBadge').textContent=state.game||'No game locked';$('unlock').disabled=!state.control||!state.game;$('hardware').textContent=state.hardware||'Passport is still loading';$('systemName').textContent=state.system||'—';$('down').textContent=rate(state.down);$('up').textContent=rate(state.up);$('latency').textContent=state.latency>0?Number(state.latency).toFixed(1)+' ms':'Not sampled';document.querySelectorAll('.remoteAction').forEach(b=>{if(b.id!=='unlock')b.disabled=!state.control});const lease=$('lease'),hint=$('controlHint');if(state.control){const persistent=Boolean(state.control_until_revoked);const until=persistent?'until revoked':new Date(state.control_until).toLocaleTimeString([], {hour:'2-digit',minute:'2-digit'});lease.textContent='Control is locally approved '+(persistent?'until revoked.':'until '+until+'.')+' Every accepted command is written to the audit log.';lease.classList.add('on');hint.textContent=persistent?'This device has a local until-revoked grant.':'This device has a temporary local control grant.';hint.style.color='var(--green)'}else{lease.textContent='Grant control in Kerneon to make changes.';lease.classList.remove('on');hint.textContent='Controls are locked on the PC.';hint.style.color=''}renderProcesses();renderEvents();renderScreenState()}
async function refresh(){try{const r=await fetch('/api/snapshot',{cache:'no-store'});if(!r.ok)throw new Error('Pairing expired');state=await r.json();$('live').innerHTML='<i class="pulse"></i><span>Live · '+new Date(state.at).toLocaleTimeString()+'</span>';render()}catch(error){$('live').innerHTML='<i class="pulse" style="color:#e6c48b"></i><span>Link interrupted</span>'}}
async function loadGames(){try{const r=await fetch('/api/games',{cache:'no-store'});if(!r.ok)throw 0;games=await r.json();$('gameCount').textContent=games.length+' GAME'+(games.length===1?'':'S');$('games').innerHTML=games.map(g=>'<article class="game"><div><div class="provider">'+esc(g.provider)+'</div><strong>'+esc(g.name)+'</strong></div><button class="button primary launch remoteAction" data-id="'+esc(g.id)+'" '+(state.control?'':'disabled')+'>Launch</button></article>').join('')||'<div class="empty">No supported Steam or Epic manifests were found.</div>';document.querySelectorAll('.launch').forEach(b=>b.onclick=()=>act('launch-game',b.dataset.id))}catch{$('games').innerHTML='<div class="empty">The installed library could not be read.</div>'}}
async function act(action,target){if(!state.control){showNotice('Remote control is locked. Grant access on the PC first.');return}try{const r=await fetch('/api/action',{method:'POST',headers:{'Content-Type':'application/json','X-Kerneon-Request':'remote-link'},body:JSON.stringify({action:action,target:target||''})});if(!r.ok){const message=await r.text();throw new Error(message.trim()||'Command refused')}showNotice('Command accepted by Kerneon.',true);setTimeout(refresh,260)}catch(error){showNotice(error.message||'Kerneon refused that command.')}}$('auto').onclick=()=>act(state.autopilot?'autopilot-off':'autopilot-on');$('focus').onclick=()=>act(state.game_focus?'focus-off':'focus-on');$('capture').onclick=()=>act('capture');$('unlock').onclick=()=>act('game-unlock');refresh();setInterval(refresh,1000);
</script></html>`

const remoteOverviewSurgeCardHTML = `<article class="card"><div class="cardTitle"><div><h2>Surge</h2><p>Measured regulation, never a blind boost.</p></div><span class="badge cyan">LOCAL GRANT</span></div><div class="statusCopy" id="status">Reading Kerneon…</div><div class="locked" id="controlHint">Controls are locked on the PC.</div><div class="controlGrid"><button class="button primary remoteAction" id="auto">Surge</button><button class="button remoteAction" id="focus">Game Focus</button><button class="button wide remoteAction" id="capture">Capture the last 60 seconds</button><button class="button wide remoteAction" id="unlock">Release selected game</button></div><div class="lease" id="lease">Grant control in Kerneon to make changes.</div></article>`

const remoteDashboardPolishCSS = `
body{background:radial-gradient(720px 420px at 4% -8%,rgba(35,72,77,.3),transparent 62%),radial-gradient(620px 400px at 104% 0%,rgba(44,45,75,.22),transparent 60%),#0a0c0e}
.shell{width:min(1180px,100%)}header{padding-bottom:22px}.hero{align-items:center;margin-bottom:20px}.hero h1{font-size:clamp(28px,4vw,44px);line-height:1.02;letter-spacing:-1.6px}.hero p{max-width:470px}.nav{border-radius:14px;padding:5px;background:rgba(12,15,17,.9)}.nav button,.button{border-radius:10px;transition:background .1s ease,color .1s ease,border-color .1s ease,transform .08s ease}.nav button.active{background:rgba(132,218,222,.13);border-color:rgba(132,218,222,.28);color:var(--cyan)}.button.primary{background:rgba(132,218,222,.14);border-color:rgba(132,218,222,.34);color:var(--cyan)}.card,.metric{border-color:#20292e;border-radius:16px;background:rgba(17,21,24,.92);box-shadow:0 12px 34px rgba(0,0,0,.12)}.metric.heroMetric{background:linear-gradient(145deg,rgba(25,42,46,.96),rgba(16,21,24,.96))}#view-overview .layout{grid-template-columns:1fr}.surgeRemoteLayout{display:grid;grid-template-columns:minmax(0,1.55fr) minmax(280px,.72fr);gap:11px}.ocPanel{position:relative;overflow:hidden}.ocPanel:before{content:"";position:absolute;inset:0;background:radial-gradient(520px 220px at 45% -30%,rgba(132,218,222,.11),transparent 65%);pointer-events:none}.ocHeader{position:relative}.ocProgress{height:3px;border-radius:3px;background:#1a2226;overflow:hidden;margin:4px 0 22px}.ocProgress span{display:block;height:100%;width:0;background:var(--cyan);box-shadow:0 0 15px rgba(132,218,222,.38);transition:width .24s ease}.gaugeGrid{position:relative;display:grid;grid-template-columns:repeat(5,minmax(112px,1fr));gap:10px}.ocGauge{min-width:0;padding:14px 10px 13px;text-align:center;border:1px solid #222d32;border-radius:15px;background:rgba(10,14,16,.72)}.gaugeRing{--p:0;--gauge:var(--cyan);width:clamp(88px,10vw,116px);aspect-ratio:1;margin:0 auto 12px;padding:7px;border-radius:50%;background:conic-gradient(var(--gauge) calc(var(--p)*1%),#1b2428 0);transform:translateZ(0)}.gaugeCore{height:100%;display:grid;place-content:center;border-radius:50%;background:#0f1417;box-shadow:inset 0 0 0 1px #243036}.gaugeValue{font:680 clamp(18px,2.5vw,25px) "Segoe UI Variable Display","Segoe UI",sans-serif;letter-spacing:-.8px}.gaugeUnit{color:var(--quiet);font-size:9px;margin-top:1px}.gaugeLabel{font-size:11px;font-weight:650}.gaugeDetail{height:28px;color:var(--quiet);font-size:9px;line-height:1.35;margin-top:5px}.ocPanel[data-mode="testing"] .gaugeRing{animation:ocBreath 1.05s ease-in-out infinite}.ocPanel[data-mode="applied"] .gaugeRing{--gauge:var(--green)}.ocPanel[data-mode="rollback"] .gaugeRing{--gauge:var(--amber)}@keyframes ocBreath{50%{filter:brightness(1.22);transform:scale(1.015)}}.ocSummary{position:relative;margin-top:14px;padding:13px 15px;border:1px solid #222d32;border-radius:13px;background:#0e1316;color:var(--muted);font-size:12px;line-height:1.5}.ocProvider{color:var(--quiet);font-size:10px;margin-top:6px}.controlCard .statusCopy{margin-bottom:12px}.controlCard .cardTitle{margin-bottom:10px}
@media(max-width:980px){.surgeRemoteLayout{grid-template-columns:1fr}.gaugeGrid{grid-template-columns:repeat(3,minmax(108px,1fr))}}
@media(max-width:620px){.hero h1{font-size:32px}.hero p{font-size:12px}.gaugeGrid{grid-template-columns:1fr 1fr;gap:8px}.ocGauge{padding:12px 8px}.gaugeRing{width:96px}.ocGauge:last-child{grid-column:1/-1}.surgeRemoteLayout{gap:9px}}
@media(prefers-reduced-motion:reduce){.ocPanel[data-mode="testing"] .gaugeRing{animation:none}}
`

const remoteSurgeSectionHTML = `<section class="view" id="view-surge"><div class="surgeRemoteLayout"><article class="card ocPanel" id="ocPanel" data-mode="idle"><div class="cardTitle ocHeader"><div><h2>Live hardware tuning</h2><p>Current clocks, power and temperature from this PC.</p></div><span class="badge cyan" id="ocStage">MONITORING</span></div><div class="ocProgress"><span id="ocProgress"></span></div><div class="gaugeGrid"><div class="ocGauge"><div class="gaugeRing" id="cpuGauge"><div class="gaugeCore"><div class="gaugeValue" id="cpuClock">—</div><div class="gaugeUnit">MHz</div></div></div><div class="gaugeLabel">CPU clock</div><div class="gaugeDetail" id="cpuClockDetail">Waiting for telemetry</div></div><div class="ocGauge"><div class="gaugeRing" id="gpuGauge"><div class="gaugeCore"><div class="gaugeValue" id="gpuClock">—</div><div class="gaugeUnit">MHz</div></div></div><div class="gaugeLabel">GPU core</div><div class="gaugeDetail" id="gpuClockDetail">Waiting for telemetry</div></div><div class="ocGauge"><div class="gaugeRing" id="vramGauge"><div class="gaugeCore"><div class="gaugeValue" id="vramClock">—</div><div class="gaugeUnit">MHz</div></div></div><div class="gaugeLabel">Graphics memory</div><div class="gaugeDetail" id="vramClockDetail">Waiting for telemetry</div></div><div class="ocGauge"><div class="gaugeRing" id="powerGauge"><div class="gaugeCore"><div class="gaugeValue" id="gpuPower">—</div><div class="gaugeUnit">watts</div></div></div><div class="gaugeLabel">GPU power</div><div class="gaugeDetail" id="gpuPowerDetail">Waiting for telemetry</div></div><div class="ocGauge"><div class="gaugeRing" id="tempGauge"><div class="gaugeCore"><div class="gaugeValue" id="gpuTemp">—</div><div class="gaugeUnit">°C</div></div></div><div class="gaugeLabel">GPU temperature</div><div class="gaugeDetail" id="gpuTempDetail">Ceiling appears after estimate</div></div></div><div class="ocSummary"><strong id="ocStatus">Hardware tuning is not running.</strong><div class="ocProvider" id="ocProvider">Reading the hardware provider…</div></div></article><article class="card controlCard"><div class="cardTitle"><div><h2>Surge controls</h2><p>Only actions approved on the PC are available here.</p></div><span class="badge cyan">LOCAL GRANT</span></div><div class="statusCopy" id="status">Reading Kerneon…</div><div class="locked" id="controlHint">Controls are locked on the PC.</div><div class="controlGrid"><button class="button primary remoteAction" id="auto">Surge</button><button class="button remoteAction" id="focus">Game Focus</button><button class="button wide remoteAction" id="capture">Capture the last 60 seconds</button><button class="button wide remoteAction" id="unlock">Release selected game</button></div><div class="lease" id="lease">Grant control in Kerneon to make changes.</div></article></div></section>`

const remoteDashboardGaugeJS = `
const gaugeValues={},gaugeNumbers={},gaugeTokens={},reduceGaugeMotion=matchMedia('(prefers-reduced-motion: reduce)').matches;
function gaugeNumber(value){return Number.isFinite(Number(value))?Number(value):0}
function drawGauge(id,value,maximum){const ring=$(id+'Gauge'),label=$(id);if(!ring||!label)return;const target=Math.max(0,gaugeNumber(value)),max=Math.max(0,gaugeNumber(maximum)),targetPercent=max>0?Math.max(0,Math.min(100,target/max*100)):0,startPercent=gaugeValues[id]??0,startNumber=gaugeNumbers[id]??0,token=(gaugeTokens[id]||0)+1;gaugeTokens[id]=token;const paint=(number,percent)=>{gaugeValues[id]=percent;gaugeNumbers[id]=number;ring.style.setProperty('--p',percent.toFixed(2));label.textContent=number>0?Math.round(number).toLocaleString():'—'};if(reduceGaugeMotion){paint(target,targetPercent);return}const begun=performance.now(),duration=380;function frame(now){if(gaugeTokens[id]!==token)return;const p=Math.min(1,(now-begun)/duration),ease=1-Math.pow(1-p,3),percent=startPercent+(targetPercent-startPercent)*ease,number=startNumber+(target-startNumber)*ease;paint(number,percent);if(p<1)requestAnimationFrame(frame)}requestAnimationFrame(frame)}
function signedMHz(value){const n=Math.round(gaugeNumber(value));return (n>0?'+':'')+n+' MHz offset'}
function renderOC(){const panel=$('ocPanel');if(!panel)return;let mode='idle',stage='MONITORING';if(state.tuning_recovery){mode='rollback';stage='ROLLBACK REQUIRED'}else if(state.tuning_estimating){mode='testing';stage='ESTIMATING'}else if(state.tuning_running){mode='testing';stage='TESTING'}else if(state.tuning_applied){mode='applied';stage='RETAINED'}else if(state.autopilot){stage='SURGE ACTIVE'}panel.dataset.mode=mode;$('ocStage').textContent=stage;$('ocProgress').style.width=(Math.max(0,Math.min(1,gaugeNumber(state.tuning_progress)))*100).toFixed(1)+'%';drawGauge('cpuClock',state.cpu_clock_mhz,state.cpu_max_mhz);drawGauge('gpuClock',state.gpu_clock_mhz,state.gpu_max_clock_mhz);drawGauge('vramClock',state.vram_clock_mhz,state.vram_max_clock_mhz);drawGauge('gpuPower',state.gpu_power_w,state.gpu_power_limit_w);drawGauge('gpuTemp',state.gpu_temperature_c,state.tuning_thermal_limit_c||100);$('cpuClockDetail').textContent=state.cpu_max_mhz>0?'Reference maximum '+Math.round(state.cpu_max_mhz).toLocaleString()+' MHz':'Live processor clock';$('gpuClockDetail').textContent=signedMHz(state.gpu_core_offset_mhz);$('vramClockDetail').textContent=signedMHz(state.vram_offset_mhz);$('gpuPowerDetail').textContent=state.gpu_power_limit_w>0?'Limit '+Math.round(state.gpu_power_limit_w)+' W':'Power limit unavailable';$('gpuTempDetail').textContent=state.tuning_thermal_limit_c>0?'Measured ceiling '+Math.round(state.tuning_thermal_limit_c)+'°C':(state.tuning_estimating?'Ceiling pending live estimate':'No measured ceiling armed');$('ocStatus').textContent=state.tuning_status||'Hardware tuning is not running.';$('ocProvider').textContent=[state.gpu_name,state.gpu_provider,state.tuning_profile?state.tuning_profile+' profile':''].filter(Boolean).join(' · ')||'No direct GPU telemetry provider reported';}
`

const remoteDashboardGaugeV2CSS = `
.gaugeRing{--base-angle:0deg;--total-angle:0deg;--gauge-base:#72838a;background:conic-gradient(from 225deg,var(--gauge-base) 0deg var(--base-angle),var(--cyan) var(--base-angle) var(--total-angle),#1b2428 var(--total-angle) 270deg,transparent 270deg 360deg);box-shadow:inset 0 0 0 1px rgba(152,178,187,.08),0 0 0 1px rgba(0,0,0,.24)}
.ocPanel[data-mode="applied"] .gaugeRing{--gauge-base:#72838a}.ocPanel[data-mode="rollback"] .gaugeRing{--gauge-base:var(--amber)}
.gaugeRing[data-uplift="true"]{filter:drop-shadow(0 0 7px rgba(137,229,235,.13))}.gaugeDetail{height:31px}.gaugeKey{display:flex;align-items:center;justify-content:flex-end;gap:13px;margin:10px 2px 2px;color:var(--quiet);font-size:9px}.gaugeKey span{display:flex;align-items:center;gap:6px}.gaugeKey i{width:13px;height:3px;border-radius:9px;background:#72838a}.gaugeKey .surgeKey i{background:var(--cyan);box-shadow:0 0 8px rgba(137,229,235,.3)}
.surgeProof{position:relative;margin-top:14px;padding:16px;border:1px solid #26343a;border-radius:15px;background:linear-gradient(145deg,rgba(16,24,28,.97),rgba(12,17,20,.97));overflow:hidden}.surgeProof:before{content:"";position:absolute;inset:0 auto 0 0;width:2px;background:#61727a}.surgeProof[data-state="proved"]:before{background:var(--cyan);box-shadow:0 0 14px var(--cyan)}.surgeProof[data-state="not-proved"]:before{background:var(--amber)}.proofHead{position:relative;display:flex;align-items:start;justify-content:space-between;gap:12px}.proofEyebrow{color:var(--quiet);font-size:9px;font-weight:760;letter-spacing:.72px}.proofTitle{display:block;margin-top:5px;font-size:14px;letter-spacing:-.2px}.proofClaim{border:1px solid #34434a;border-radius:999px;padding:5px 8px;color:var(--quiet);font-size:9px;font-weight:750;white-space:nowrap}.surgeProof[data-state="proved"] .proofClaim{color:var(--cyan);border-color:#3f6970;background:rgba(137,229,235,.07)}.surgeProof[data-state="not-proved"] .proofClaim{color:var(--amber);border-color:#5b4c35}.proofDetail{position:relative;margin:9px 0 0;color:var(--muted);font-size:11px;line-height:1.5}.proofMetrics{position:relative;display:grid;grid-template-columns:repeat(4,1fr);gap:7px;margin-top:13px}.proofMetric{min-width:0;padding:10px;border:1px solid #222d32;border-radius:11px;background:#0c1114}.proofMetric span{display:block;color:var(--quiet);font-size:9px}.proofMetric strong{display:block;margin-top:5px;font-size:12px}.proofMetric small{display:block;margin-top:4px;color:var(--quiet);font-size:9px}.proofMetric small.positive{color:var(--green)}.proofMetric small.negative{color:#e3a2a2}.proofEvidence{position:relative;margin-top:10px;color:var(--quiet);font-size:9px}
@media(max-width:700px){.proofMetrics{grid-template-columns:1fr 1fr}.gaugeKey{justify-content:center}}
`

const remoteSurgeProofHTML = `<div class="gaugeKey"><span><i></i>Live / session baseline</span><span class="surgeKey"><i></i>Above Surge start</span></div><div class="surgeProof" id="surgeProof" data-state="off"><div class="proofHead"><div><div class="proofEyebrow">MEASURED SURGE RESULT</div><strong class="proofTitle" id="proofTitle">Surge is off</strong></div><span class="proofClaim" id="proofClaim">NO CLAIM ACTIVE</span></div><p class="proofDetail" id="proofDetail">Live hardware monitoring stays on. Enable Surge with a locked game to start a measured comparison.</p><div class="proofMetrics" id="proofMetrics" hidden><div class="proofMetric"><span>Average FPS</span><strong id="proofFPS">—</strong><small id="proofFPSDelta">—</small></div><div class="proofMetric"><span>1% low</span><strong id="proofLow">—</strong><small id="proofLowDelta">—</small></div><div class="proofMetric"><span>p99 frame time</span><strong id="proofP99">—</strong><small id="proofP99Delta">—</small></div><div class="proofMetric"><span>Hitches / minute</span><strong id="proofHitches">—</strong><small id="proofHitchesDelta">—</small></div></div><div class="proofEvidence" id="proofEvidence">No comparison is running.</div></div>`

const remoteDashboardGaugeV2JS = `
const gaugeRingIDs={cpuClock:'cpuGauge',gpuClock:'gpuGauge',vramClock:'vramGauge',gpuPower:'powerGauge',gpuTemp:'tempGauge'},gaugeBaseAngles={},gaugeTotalAngles={};
function drawGauge(id,value,baseline,maximum,showUplift){const ring=$(gaugeRingIDs[id]),label=$(id);if(!ring||!label)return;const target=Math.max(0,gaugeNumber(value)),reference=Math.max(0,gaugeNumber(baseline)),max=Math.max(target,reference,gaugeNumber(maximum)),uplift=Boolean(showUplift&&reference>0&&target>reference),baseValue=uplift?reference:target,targetBaseAngle=max>0?Math.min(270,baseValue/max*270):0,targetTotalAngle=max>0?Math.min(270,target/max*270):0,startBase=gaugeBaseAngles[id]??0,startTotal=gaugeTotalAngles[id]??0,startNumber=gaugeNumbers[id]??0,token=(gaugeTokens[id]||0)+1;gaugeTokens[id]=token;ring.dataset.uplift=uplift?'true':'false';const paint=(number,baseAngle,totalAngle)=>{gaugeBaseAngles[id]=baseAngle;gaugeTotalAngles[id]=totalAngle;gaugeNumbers[id]=number;ring.style.setProperty('--base-angle',baseAngle.toFixed(2)+'deg');ring.style.setProperty('--total-angle',totalAngle.toFixed(2)+'deg');label.textContent=number>0?Math.round(number).toLocaleString():'—'};if(reduceGaugeMotion){paint(target,targetBaseAngle,targetTotalAngle);return}const begun=performance.now(),duration=340;function frame(now){if(gaugeTokens[id]!==token)return;const p=Math.min(1,(now-begun)/duration),ease=1-Math.pow(1-p,3);paint(startNumber+(target-startNumber)*ease,startBase+(targetBaseAngle-startBase)*ease,startTotal+(targetTotalAngle-startTotal)*ease);if(p<1)requestAnimationFrame(frame)}requestAnimationFrame(frame)}
function observedDetail(current,baseline,unit,active,fallbackText){const now=gaugeNumber(current),start=gaugeNumber(baseline),delta=now-start;if(active&&start>0&&delta>0.5)return 'Started '+Math.round(start).toLocaleString()+' '+unit+' · +'+Math.round(delta).toLocaleString()+' observed';return fallbackText}
function proofMetric(valueID,deltaID,before,after,delta,unit){const value=$(valueID),change=$(deltaID),valid=gaugeNumber(before)>0&&gaugeNumber(after)>0;if(!value||!change)return;value.textContent=valid?formatProofValue(before,unit)+' → '+formatProofValue(after,unit):'—';change.className=valid?(delta>.25?'positive':delta<-.25?'negative':''):'';change.textContent=!valid?'Waiting for comparable data':delta>.05?'+'+delta.toFixed(1)+'% better':delta<-.05?Math.abs(delta).toFixed(1)+'% worse':'No material change'}
function formatProofValue(value,unit){return (unit==='ms'?gaugeNumber(value).toFixed(1):Math.round(gaugeNumber(value)).toLocaleString())+' '+unit}
function renderSurgeProof(){const box=$('surgeProof'),proof=state.surge_proof||{};if(!box)return;box.dataset.state=proof.state||'off';$('proofTitle').textContent=proof.title||'No comparison active';$('proofDetail').textContent=proof.detail||'Live monitoring remains available.';const ready=Boolean(proof.ready),hasData=gaugeNumber(proof.baseline_fps)>0&&gaugeNumber(proof.after_fps)>0;$('proofMetrics').hidden=!hasData;$('proofClaim').textContent=proof.proved?'MEASURED UPLIFT':ready?'RESULT READY':state.autopilot?'NO CLAIM YET':'NO CLAIM ACTIVE';$('proofEvidence').textContent=state.autopilot?(gaugeNumber(proof.checks)+' checks · '+gaugeNumber(proof.comparable)+' comparable'+(proof.repeated_metric?' · repeated '+proof.repeated_metric:'')):'Live gauges continue with Surge disabled.';proofMetric('proofFPS','proofFPSDelta',proof.baseline_fps,proof.after_fps,gaugeNumber(proof.fps_delta_percent),'FPS');proofMetric('proofLow','proofLowDelta',proof.baseline_low,proof.after_low,gaugeNumber(proof.low_delta_percent),'FPS');proofMetric('proofP99','proofP99Delta',proof.baseline_p99_ms,proof.after_p99_ms,gaugeNumber(proof.p99_better_percent),'ms');proofMetric('proofHitches','proofHitchesDelta',proof.baseline_hitches_per_minute,proof.after_hitches_per_minute,gaugeNumber(proof.hitch_better_percent),'/min')}
function renderOC(){const panel=$('ocPanel');if(!panel)return;let mode='idle',stage='MONITORING';if(state.tuning_recovery){mode='rollback';stage='ROLLBACK REQUIRED'}else if(state.tuning_estimating){mode='testing';stage='ESTIMATING'}else if(state.tuning_running){mode='testing';stage='TESTING'}else if(state.tuning_applied){mode='applied';stage='RETAINED'}else if(state.autopilot){stage='SURGE ACTIVE'}panel.dataset.mode=mode;$('ocStage').textContent=stage;$('ocProgress').style.width=(Math.max(0,Math.min(1,gaugeNumber(state.tuning_progress)))*100).toFixed(1)+'%';const tuningActive=Boolean(state.autopilot||state.tuning_running||state.tuning_applied||gaugeNumber(state.gpu_core_offset_mhz)>0||gaugeNumber(state.vram_offset_mhz)>0),cpuMax=Math.max(gaugeNumber(state.cpu_max_mhz),gaugeNumber(state.cpu_clock_mhz)*1.08),gpuMax=Math.max(gaugeNumber(state.gpu_max_clock_mhz),gaugeNumber(state.gpu_clock_mhz)*1.08),vramMax=Math.max(gaugeNumber(state.vram_max_clock_mhz),gaugeNumber(state.vram_clock_mhz)*1.08),powerValue=gaugeNumber(state.gpu_power_limit_w)||gaugeNumber(state.gpu_power_w),powerMax=Math.max(gaugeNumber(state.gpu_power_max_w),powerValue*1.08),gpuFallback=gaugeNumber(state.gpu_core_offset_mhz)!==0?signedMHz(state.gpu_core_offset_mhz):'Live now · no session uplift',vramFallback=gaugeNumber(state.vram_offset_mhz)!==0?signedMHz(state.vram_offset_mhz):'Live now · no session uplift';drawGauge('cpuClock',state.cpu_clock_mhz,state.cpu_clock_baseline_mhz,cpuMax,state.autopilot);drawGauge('gpuClock',state.gpu_clock_mhz,state.gpu_clock_baseline_mhz,gpuMax,tuningActive);drawGauge('vramClock',state.vram_clock_mhz,state.vram_clock_baseline_mhz,vramMax,tuningActive);drawGauge('gpuPower',powerValue,state.gpu_power_baseline_w,powerMax,tuningActive);drawGauge('gpuTemp',state.gpu_temperature_c,state.gpu_temperature_baseline_c,state.tuning_thermal_limit_c||100,false);$('cpuClockDetail').textContent=observedDetail(state.cpu_clock_mhz,state.cpu_clock_baseline_mhz,'MHz',state.autopilot,state.cpu_max_mhz>0?'Hardware maximum '+Math.round(state.cpu_max_mhz).toLocaleString()+' MHz':'Live processor clock');$('gpuClockDetail').textContent=observedDetail(state.gpu_clock_mhz,state.gpu_clock_baseline_mhz,'MHz',tuningActive,gpuFallback);$('vramClockDetail').textContent=observedDetail(state.vram_clock_mhz,state.vram_clock_baseline_mhz,'MHz',tuningActive,vramFallback);$('gpuPowerDetail').textContent=observedDetail(powerValue,state.gpu_power_baseline_w,'W',tuningActive,'Live draw '+(state.gpu_power_w>0?Math.round(state.gpu_power_w)+' W':'unavailable'));const tempDelta=gaugeNumber(state.gpu_temperature_c)-gaugeNumber(state.gpu_temperature_baseline_c);$('gpuTempDetail').textContent=state.autopilot&&state.gpu_temperature_baseline_c>0?'Started '+Math.round(state.gpu_temperature_baseline_c)+'°C · '+(tempDelta>0?'+':'')+tempDelta.toFixed(1)+'°C now':(state.tuning_thermal_limit_c>0?'Measured ceiling '+Math.round(state.tuning_thermal_limit_c)+'°C':'Live temperature');$('ocStatus').textContent=state.tuning_status||'Hardware tuning is not running. Live monitoring remains active.';$('ocProvider').textContent=[state.gpu_name,state.gpu_provider,state.tuning_profile?state.tuning_profile+' profile':''].filter(Boolean).join(' · ')||'No direct GPU telemetry provider reported';renderSurgeProof()}
`

func remoteDashboardDocument() string {
	page := remoteDashboardHTML
	page = strings.Replace(page, "</style>", remoteDashboardPolishCSS+remoteDashboardGaugeV2CSS+"</style>", 1)
	page = strings.Replace(page, `<section class="hero"><h1>Your PC still feels close.</h1><p>Live context, your verified game library, and deliberately limited control—without a cloud relay.</p></section>`, `<section class="hero"><h1>Monitor and control this PC.</h1><p>Live performance, games, events and approved actions on your local network.</p></section>`, 1)
	page = strings.Replace(page, `<button class="active" data-view="overview">Overview</button><button data-view="games">Games</button>`, `<button class="active" data-view="overview">Overview</button><button data-view="surge">Surge</button><button data-view="games">Games</button>`, 1)
	page = strings.Replace(page, remoteOverviewSurgeCardHTML, "", 1)
	page = strings.Replace(page, `<section class="view" id="view-games">`, remoteSurgeSectionHTML+`<section class="view" id="view-games">`, 1)
	page = strings.Replace(page, `<div class="gaugeLabel">GPU power</div>`, `<div class="gaugeLabel">Power ceiling</div>`, 1)
	page = strings.Replace(page, `<div class="ocSummary">`, remoteSurgeProofHTML+`<div class="ocSummary">`, 1)
	page = strings.Replace(page, "function renderProcesses()", remoteDashboardGaugeJS+remoteDashboardGaugeV2JS+"\nfunction renderProcesses()", 1)
	page = strings.Replace(page, "function render(){", "function render(){renderOC();", 1)
	return page
}
