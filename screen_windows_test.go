//go:build windows

package main

import (
	"encoding/json"
	"image/jpeg"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
)

func TestScreenCaptureProducesAValidScaledJPEG(t *testing.T) {
	monitors := listScreenMonitors()
	if len(monitors) == 0 {
		t.Fatal("Windows returned no displays")
	}
	frame, err := captureScreenJPEG(monitors[0], 320, 60)
	if err != nil {
		t.Fatalf("capture display: %v", err)
	}
	config, err := jpeg.DecodeConfig(strings.NewReader(string(frame)))
	if err != nil {
		t.Fatalf("decode captured JPEG: %v", err)
	}
	if config.Width < 1 || config.Width > 320 || config.Height < 1 {
		t.Fatalf("unexpected frame dimensions: %dx%d", config.Width, config.Height)
	}
}

func TestScreenViewAndInputGrantsStaySeparate(t *testing.T) {
	a := &App{}
	a.setScreenGrant("input", 2)
	if a.screenInputGranted() {
		t.Fatal("input must not be granted before screen viewing")
	}
	a.setScreenGrant("view", 1)
	if !a.screenViewGranted() || a.screenInputGranted() {
		t.Fatal("viewing should not implicitly grant input")
	}
	a.setScreenGrant("input", 2)
	if !a.screenInputGranted() {
		t.Fatal("explicit input grant should work while viewing is granted")
	}
	a.setScreenGrant("view", 0)
	if a.screenViewGranted() || a.screenInputGranted() {
		t.Fatal("revoking view must revoke both pixels and input")
	}
}

func TestScreenInputValidationRejectsInvalidCoordinatesAndTypes(t *testing.T) {
	if validateScreenInputMessage(screenInputMessage{Type: "shell"}) == nil {
		t.Fatal("arbitrary screen message type should be rejected")
	}
	if validateScreenInputMessage(screenInputMessage{Type: "pointer", X: math.NaN()}) == nil {
		t.Fatal("non-finite pointer coordinates should be rejected")
	}
	if validateScreenInputMessage(screenInputMessage{Type: "pointer", X: 0.5, Y: 0.5}) != nil {
		t.Fatal("bounded pointer message should be accepted")
	}
}

func TestRemoteScreenOfferNegotiatesEncryptedDataChannels(t *testing.T) {
	a, cookie := pairedRemoteApp()
	a.screen.Peers = make(map[*webrtc.PeerConnection]*screenPeer)

	client, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	frames, err := client.CreateDataChannel("kerneon-screen", &webrtc.DataChannelInit{Ordered: boolPointer(false), MaxRetransmits: uint16Pointer(0)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = client.CreateDataChannel("kerneon-input", nil); err != nil {
		t.Fatal(err)
	}
	opened := make(chan struct{}, 1)
	frames.OnOpen(func() { opened <- struct{}{} })
	offer, err := client.CreateOffer(nil)
	if err != nil {
		t.Fatal(err)
	}
	gathered := webrtc.GatheringCompletePromise(client)
	if err = client.SetLocalDescription(offer); err != nil {
		t.Fatal(err)
	}
	select {
	case <-gathered:
	case <-time.After(6 * time.Second):
		t.Fatal("client ICE gathering timed out")
	}
	body, _ := json.Marshal(client.LocalDescription())
	request := authorizedRemoteRequest(http.MethodPost, a.remote.URL+"/api/screen/offer", string(body), cookie)
	result := httptest.NewRecorder()
	a.remoteScreenOfferAPI(result, request)
	if result.Code != http.StatusOK {
		t.Fatalf("screen offer failed: code=%d body=%s", result.Code, result.Body.String())
	}
	answer := webrtc.SessionDescription{}
	if json.Unmarshal(result.Body.Bytes(), &answer) != nil || answer.Type != webrtc.SDPTypeAnswer {
		t.Fatalf("invalid screen answer: %s", result.Body.String())
	}
	if err = client.SetRemoteDescription(answer); err != nil {
		t.Fatalf("apply screen answer: %v", err)
	}
	select {
	case <-opened:
	case <-time.After(8 * time.Second):
		t.Fatal("encrypted screen data channel did not open")
	}
	a.stopScreenSessions()
	if len(a.screen.Peers) != 0 {
		t.Fatal("screen session revocation left a peer connected")
	}
}

func boolPointer(value bool) *bool       { return &value }
func uint16Pointer(value uint16) *uint16 { return &value }
