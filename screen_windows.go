//go:build windows

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
	"unsafe"

	"github.com/pion/webrtc/v4"
)

const (
	inputMouse    = 0
	inputKeyboard = 1

	mouseLeftDown   = 0x0002
	mouseLeftUp     = 0x0004
	mouseRightDown  = 0x0008
	mouseRightUp    = 0x0010
	mouseMiddleDown = 0x0020
	mouseMiddleUp   = 0x0040
	mouseWheel      = 0x0800

	keyEventKeyUp   = 0x0002
	keyEventUnicode = 0x0004
)

type ScreenEngine struct {
	mu                sync.RWMutex
	ViewUntil         time.Time
	InputUntil        time.Time
	ViewUntilRevoked  bool
	InputUntilRevoked bool
	Monitor           int
	Peers             map[*webrtc.PeerConnection]*screenPeer
	LastError         string
}

type screenPeer struct {
	connection   *webrtc.PeerConnection
	sessionToken string
	remoteIP     string
	streamOnce   sync.Once
	inputMu      sync.Mutex
	inputWindow  time.Time
	inputCount   int
}

type screenView struct {
	ViewGranted, InputGranted           bool
	ViewUntilRevoked, InputUntilRevoked bool
	ViewUntil, InputUntil               time.Time
	Monitor, ActiveSessions             int
	Monitors                            []ScreenMonitor
	Error                               string
}

type screenInputMessage struct {
	Type    string  `json:"type"`
	Action  string  `json:"action"`
	Code    string  `json:"code"`
	Text    string  `json:"text"`
	X       float64 `json:"x"`
	Y       float64 `json:"y"`
	Button  int     `json:"button"`
	Delta   int     `json:"delta"`
	Monitor int     `json:"monitor"`
}

type windowsInput struct {
	Type uint32
	_    uint32
	Data [32]byte
}

type windowsMouseInput struct {
	DX, DY      int32
	MouseData   uint32
	Flags, Time uint32
	ExtraInfo   uintptr
}

type windowsKeyboardInput struct {
	VK, Scan  uint16
	Flags     uint32
	Time      uint32
	ExtraInfo uintptr
}

var (
	procSetCursorPos = user32.NewProc("SetCursorPos")
	procSendInput    = user32.NewProc("SendInput")
)

func (a *App) screenSnapshot() screenView {
	result := a.screenGrantSnapshot()
	result.Monitors = listScreenMonitors()
	return result
}

func (a *App) screenGrantSnapshot() screenView {
	a.screen.mu.Lock()
	now := time.Now()
	if !a.screen.ViewUntilRevoked && !a.screen.ViewUntil.After(now) {
		a.screen.ViewUntil = time.Time{}
	}
	if !a.screen.InputUntilRevoked && !a.screen.InputUntil.After(now) {
		a.screen.InputUntil = time.Time{}
	}
	view := a.screen.ViewUntilRevoked || a.screen.ViewUntil.After(now)
	input := view && (a.screen.InputUntilRevoked || a.screen.InputUntil.After(now))
	result := screenView{ViewGranted: view, InputGranted: input, ViewUntilRevoked: a.screen.ViewUntilRevoked, InputUntilRevoked: a.screen.InputUntilRevoked, ViewUntil: a.screen.ViewUntil, InputUntil: a.screen.InputUntil, Monitor: a.screen.Monitor, ActiveSessions: len(a.screen.Peers), Error: a.screen.LastError}
	a.screen.mu.Unlock()
	return result
}

func (a *App) screenPermissionState() (view, input bool, monitor int) {
	a.screen.mu.RLock()
	now := time.Now()
	view = a.screen.ViewUntilRevoked || a.screen.ViewUntil.After(now)
	input = view && (a.screen.InputUntilRevoked || a.screen.InputUntil.After(now))
	monitor = a.screen.Monitor
	a.screen.mu.RUnlock()
	return
}

func (a *App) setScreenGrant(kind string, mode int) {
	a.screen.mu.Lock()
	now := time.Now()
	switch kind {
	case "view":
		a.screen.ViewUntil, a.screen.ViewUntilRevoked = time.Time{}, false
		if mode == 1 {
			a.screen.ViewUntil = now.Add(15 * time.Minute)
		} else if mode == 2 {
			a.screen.ViewUntilRevoked = true
		} else {
			a.screen.InputUntil, a.screen.InputUntilRevoked = time.Time{}, false
		}
	case "input":
		viewAllowed := a.screen.ViewUntilRevoked || a.screen.ViewUntil.After(now)
		if mode != 0 && !viewAllowed {
			a.screen.mu.Unlock()
			return
		}
		a.screen.InputUntil, a.screen.InputUntilRevoked = time.Time{}, false
		if mode == 1 {
			a.screen.InputUntil = now.Add(15 * time.Minute)
		} else if mode == 2 {
			a.screen.InputUntilRevoked = true
		}
	}
	a.screen.mu.Unlock()
	a.updateTrayIndicator()
	procInvalidateRect.Call(a.hwnd, 0, 0)
	a.recordRemoteAudit(RemoteCommand{Action: "screen-" + kind + "-grant", Target: []string{"off", "15 minutes", "until revoked"}[screenClampInt(mode, 0, 2)], RemoteIP: "local"}, "local screen permission changed")
}

func (a *App) screenViewGranted() bool {
	view, _, _ := a.screenPermissionState()
	return view
}

func (a *App) screenInputGranted() bool {
	view, input, _ := a.screenPermissionState()
	return view && input
}

func (a *App) stopScreenSessions() {
	a.screen.mu.Lock()
	peers := make([]*webrtc.PeerConnection, 0, len(a.screen.Peers))
	for connection := range a.screen.Peers {
		peers = append(peers, connection)
	}
	a.screen.Peers = make(map[*webrtc.PeerConnection]*screenPeer)
	a.screen.ViewUntil, a.screen.InputUntil = time.Time{}, time.Time{}
	a.screen.ViewUntilRevoked, a.screen.InputUntilRevoked = false, false
	a.screen.mu.Unlock()
	for _, connection := range peers {
		_ = connection.Close()
	}
	a.updateTrayIndicator()
}

func (a *App) removeScreenPeer(connection *webrtc.PeerConnection) {
	a.screen.mu.Lock()
	delete(a.screen.Peers, connection)
	a.screen.mu.Unlock()
	procPostMessageW.Call(a.hwnd, WM_APP_RENDER, 0, 0)
}

func (a *App) remoteSessionStillValid(token string) bool {
	a.remote.mu.RLock()
	expires, ok := a.remote.Sessions[token]
	a.remote.mu.RUnlock()
	return ok && expires.After(time.Now())
}

func (a *App) remoteScreenOfferAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.Header.Get("X-Kerneon-Request") != "remote-link" || !a.remoteSameOrigin(r) || !a.remoteAuthorized(r) {
		http.Error(w, "Not authorized", http.StatusUnauthorized)
		return
	}
	cookie, err := r.Cookie("kerneon_session")
	if err != nil {
		http.Error(w, "Pair with Kerneon first", http.StatusUnauthorized)
		return
	}
	var offer webrtc.SessionDescription
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128<<10)).Decode(&offer); err != nil || offer.Type != webrtc.SDPTypeOffer || len(offer.SDP) > 120<<10 {
		http.Error(w, "Invalid WebRTC offer", http.StatusBadRequest)
		return
	}
	a.screen.mu.Lock()
	if a.screen.Peers == nil {
		a.screen.Peers = make(map[*webrtc.PeerConnection]*screenPeer)
	}
	if len(a.screen.Peers) >= 4 {
		a.screen.mu.Unlock()
		http.Error(w, "Too many active screen sessions", http.StatusTooManyRequests)
		return
	}
	a.screen.mu.Unlock()

	connection, err := newRemoteScreenPeerConnection()
	if err != nil {
		http.Error(w, "Could not create the encrypted screen transport", http.StatusInternalServerError)
		return
	}
	peer := &screenPeer{connection: connection, sessionToken: cookie.Value, remoteIP: remoteClientIP(r)}
	a.screen.mu.Lock()
	if len(a.screen.Peers) >= 4 {
		a.screen.mu.Unlock()
		_ = connection.Close()
		http.Error(w, "Too many active screen sessions", http.StatusTooManyRequests)
		return
	}
	a.screen.Peers[connection] = peer
	a.screen.LastError = ""
	a.screen.mu.Unlock()

	connection.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		if state == webrtc.PeerConnectionStateFailed || state == webrtc.PeerConnectionStateDisconnected || state == webrtc.PeerConnectionStateClosed {
			a.removeScreenPeer(connection)
			_ = connection.Close()
		}
	})
	a.remote.mu.RLock()
	expires := a.remote.Sessions[cookie.Value]
	a.remote.mu.RUnlock()
	if expires.After(time.Now()) {
		time.AfterFunc(time.Until(expires), func() {
			if !a.remoteSessionStillValid(cookie.Value) {
				_ = connection.Close()
			}
		})
	}
	connection.OnDataChannel(func(channel *webrtc.DataChannel) {
		switch channel.Label() {
		case "kerneon-screen":
			channel.OnOpen(func() {
				peer.streamOnce.Do(func() { go a.streamScreenFrames(peer, channel) })
			})
		case "kerneon-input":
			channel.OnMessage(func(message webrtc.DataChannelMessage) {
				if message.IsString {
					a.handleScreenInput(peer, message.Data)
				}
			})
		}
	})
	if err = connection.SetRemoteDescription(offer); err != nil {
		a.removeScreenPeer(connection)
		_ = connection.Close()
		http.Error(w, "Could not accept the screen offer", http.StatusBadRequest)
		return
	}
	answer, err := connection.CreateAnswer(nil)
	if err != nil {
		a.removeScreenPeer(connection)
		_ = connection.Close()
		http.Error(w, "Could not create the screen answer", http.StatusInternalServerError)
		return
	}
	gathering := webrtc.GatheringCompletePromise(connection)
	if err = connection.SetLocalDescription(answer); err != nil {
		a.removeScreenPeer(connection)
		_ = connection.Close()
		http.Error(w, "Could not start the encrypted screen transport", http.StatusInternalServerError)
		return
	}
	select {
	case <-gathering:
	case <-time.After(6 * time.Second):
		a.removeScreenPeer(connection)
		_ = connection.Close()
		http.Error(w, "Screen transport discovery timed out", http.StatusGatewayTimeout)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(connection.LocalDescription())
	procPostMessageW.Call(a.hwnd, WM_APP_RENDER, 0, 0)
}

func newRemoteScreenPeerConnection() (*webrtc.PeerConnection, error) {
	settings := webrtc.SettingEngine{}
	settings.SetNetworkTypes([]webrtc.NetworkType{webrtc.NetworkTypeUDP4})
	settings.SetIPFilter(func(ip net.IP) bool { return ip.IsPrivate() || ip.IsLoopback() })
	api := webrtc.NewAPI(webrtc.WithSettingEngine(settings))
	return api.NewPeerConnection(webrtc.Configuration{})
}

func (a *App) streamScreenFrames(peer *screenPeer, channel *webrtc.DataChannel) {
	ticker := time.NewTicker(125 * time.Millisecond)
	defer ticker.Stop()
	lastStatus := time.Time{}
	for range ticker.C {
		if channel.ReadyState() != webrtc.DataChannelStateOpen || !a.remoteSessionStillValid(peer.sessionToken) {
			_ = peer.connection.Close()
			return
		}
		viewGranted, _, monitor := a.screenPermissionState()
		if !viewGranted {
			if time.Since(lastStatus) >= time.Second {
				_ = channel.SendText(screenStatusJSON(a.screenSnapshot(), "Waiting for local viewing approval"))
				lastStatus = time.Now()
			}
			continue
		}
		if time.Since(lastStatus) >= 2*time.Second {
			_ = channel.SendText(screenStatusJSON(a.screenSnapshot(), "Encrypted view active"))
			lastStatus = time.Now()
		}
		if channel.BufferedAmount() > 768<<10 {
			continue
		}
		frame, err := captureScreenJPEG(screenMonitorAt(monitor), 1280, 68)
		if err != nil {
			a.screen.mu.Lock()
			a.screen.LastError = err.Error()
			a.screen.mu.Unlock()
			continue
		}
		if err := channel.Send(frame); err != nil {
			return
		}
	}
}

func screenStatusJSON(view screenView, message string) string {
	payload := struct {
		Type, Message string
		View, Input   bool
		Monitor       int
		Monitors      []ScreenMonitor
	}{"status", message, view.ViewGranted, view.InputGranted, view.Monitor, view.Monitors}
	data, _ := json.Marshal(payload)
	return string(data)
}

func (peer *screenPeer) allowInputEvent() bool {
	peer.inputMu.Lock()
	defer peer.inputMu.Unlock()
	now := time.Now()
	if peer.inputWindow.IsZero() || now.Sub(peer.inputWindow) >= time.Second {
		peer.inputWindow, peer.inputCount = now, 0
	}
	peer.inputCount++
	return peer.inputCount <= 240
}

func (a *App) handleScreenInput(peer *screenPeer, payload []byte) {
	if len(payload) == 0 || len(payload) > 4096 || !peer.allowInputEvent() || !a.remoteSessionStillValid(peer.sessionToken) {
		return
	}
	var event screenInputMessage
	if json.Unmarshal(payload, &event) != nil {
		return
	}
	if validateScreenInputMessage(event) != nil {
		return
	}
	if event.Type == "monitor" {
		if !a.screenViewGranted() {
			return
		}
		monitors := listScreenMonitors()
		if event.Monitor >= 0 && event.Monitor < len(monitors) {
			a.screen.mu.Lock()
			a.screen.Monitor = event.Monitor
			a.screen.mu.Unlock()
		}
		return
	}
	if !a.screenInputGranted() {
		return
	}
	_, _, monitorIndex := a.screenPermissionState()
	monitor := screenMonitorAt(monitorIndex)
	switch event.Type {
	case "pointer":
		applyScreenPointer(event, monitor)
	case "key":
		applyScreenKey(event.Code, event.Action == "up")
	case "text":
		if utf8.RuneCountInString(event.Text) <= 64 {
			applyScreenText(event.Text)
		}
	}
}

func applyScreenPointer(event screenInputMessage, monitor ScreenMonitor) {
	x := monitor.Bounds.Left + int32(math.Round(clampFloat(event.X, 0, 1)*float64(max32(1, monitor.Width-1))))
	y := monitor.Bounds.Top + int32(math.Round(clampFloat(event.Y, 0, 1)*float64(max32(1, monitor.Height-1))))
	procSetCursorPos.Call(uintptr(int64(x)), uintptr(int64(y)))
	flags, data := uint32(0), uint32(0)
	switch event.Action {
	case "down":
		flags = []uint32{mouseLeftDown, mouseMiddleDown, mouseRightDown}[screenClampInt(event.Button, 0, 2)]
	case "up":
		flags = []uint32{mouseLeftUp, mouseMiddleUp, mouseRightUp}[screenClampInt(event.Button, 0, 2)]
	case "wheel":
		flags = mouseWheel
		delta := screenClampInt(event.Delta, -6, 6) * 120
		data = uint32(int32(delta))
	default:
		return
	}
	sendMouseInput(flags, data)
}

func sendMouseInput(flags, data uint32) {
	input := windowsInput{Type: inputMouse}
	*(*windowsMouseInput)(unsafe.Pointer(&input.Data[0])) = windowsMouseInput{MouseData: data, Flags: flags}
	procSendInput.Call(1, uintptr(unsafe.Pointer(&input)), unsafe.Sizeof(input))
}

func applyScreenKey(code string, up bool) {
	vk := screenVirtualKey(code)
	if vk == 0 {
		return
	}
	flags := uint32(0)
	if up {
		flags = keyEventKeyUp
	}
	input := windowsInput{Type: inputKeyboard}
	*(*windowsKeyboardInput)(unsafe.Pointer(&input.Data[0])) = windowsKeyboardInput{VK: vk, Flags: flags}
	procSendInput.Call(1, uintptr(unsafe.Pointer(&input)), unsafe.Sizeof(input))
}

func applyScreenText(text string) {
	for _, character := range text {
		if character == '\r' || character == '\n' {
			applyScreenKey("Enter", false)
			applyScreenKey("Enter", true)
			continue
		}
		if character > 0xffff {
			continue
		}
		for _, up := range []bool{false, true} {
			flags := uint32(keyEventUnicode)
			if up {
				flags |= keyEventKeyUp
			}
			input := windowsInput{Type: inputKeyboard}
			*(*windowsKeyboardInput)(unsafe.Pointer(&input.Data[0])) = windowsKeyboardInput{Scan: uint16(character), Flags: flags}
			procSendInput.Call(1, uintptr(unsafe.Pointer(&input)), unsafe.Sizeof(input))
		}
	}
}

func screenVirtualKey(code string) uint16 {
	if len(code) == 4 && strings.HasPrefix(code, "Key") {
		character := code[3]
		if character >= 'A' && character <= 'Z' {
			return uint16(character)
		}
	}
	if len(code) == 6 && strings.HasPrefix(code, "Digit") {
		character := code[5]
		if character >= '0' && character <= '9' {
			return uint16(character)
		}
	}
	keys := map[string]uint16{
		"Backspace": 0x08, "Tab": 0x09, "Enter": 0x0D, "ShiftLeft": 0x10, "ShiftRight": 0x10,
		"ControlLeft": 0x11, "ControlRight": 0x11, "AltLeft": 0x12, "AltRight": 0x12, "Escape": 0x1B,
		"Space": 0x20, "PageUp": 0x21, "PageDown": 0x22, "End": 0x23, "Home": 0x24,
		"ArrowLeft": 0x25, "ArrowUp": 0x26, "ArrowRight": 0x27, "ArrowDown": 0x28,
		"Insert": 0x2D, "Delete": 0x2E, "F1": 0x70, "F2": 0x71, "F3": 0x72, "F4": 0x73,
		"F5": 0x74, "F6": 0x75, "F7": 0x76, "F8": 0x77, "F9": 0x78, "F10": 0x79, "F11": 0x7A, "F12": 0x7B,
	}
	return keys[code]
}

func validateScreenInputMessage(event screenInputMessage) error {
	if event.Type != "pointer" && event.Type != "key" && event.Type != "text" && event.Type != "monitor" {
		return errors.New("unsupported input type")
	}
	if math.IsNaN(event.X) || math.IsNaN(event.Y) || math.IsInf(event.X, 0) || math.IsInf(event.Y, 0) {
		return fmt.Errorf("invalid pointer coordinates")
	}
	return nil
}

func screenClampInt(value, minimum, maximum int) int {
	if value < minimum {
		return minimum
	}
	if value > maximum {
		return maximum
	}
	return value
}
