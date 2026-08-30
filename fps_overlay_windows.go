//go:build windows

package main

import (
	"fmt"
	"math"
	"time"
	"unsafe"
)

// FPSOverlay is a native, external window. It never injects code, hooks a
// renderer, or reads game memory; PresentMon supplies one event per presented
// frame and this surface is composited by Windows above the foreground game.
type FPSOverlay struct {
	hwnd         uintptr
	back         BackBuffer
	dpi          int32
	shown        bool
	cached       FrameStats
	cachedAt     time.Time
	lastPosition time.Time
}

func (a *App) fpsOverlayLoop() {
	for {
		fps := 2
		if a.config.Gaming.FPSHUD {
			// PresentMon already supplies one measurement event per presented
			// frame. Repainting a layered desktop window at 144/240 Hz can
			// disturb independent-flip pacing, so the human-readable number is
			// intentionally composed at 5 Hz with no decorative animation.
			fps = 5
		}
		timer := time.NewTimer(time.Second / time.Duration(fps))
		select {
		case <-timer.C:
			if a.hwnd != 0 {
				procPostMessageW.Call(a.hwnd, WM_APP_FPS_RENDER, 0, 0)
			}
		case <-a.renderStop:
			timer.Stop()
			return
		}
	}
}

func fpsOverlayWndProc(hwnd, msg, wParam, lParam uintptr) uintptr {
	a := appInstance
	if a == nil {
		r, _, _ := procDefWindowProcW.Call(hwnd, msg, wParam, lParam)
		return r
	}
	switch msg {
	case WM_ERASEBKGND:
		return 1
	case WM_NCHITTEST:
		return ^uintptr(0) // HTTRANSPARENT: every pointer action reaches the game.
	case WM_PAINT:
		a.paintFPSOverlay(hwnd)
		return 0
	case WM_DPICHANGED:
		a.fpsOverlay.dpi = int32(wParam & 0xffff)
		var rect RECT
		procRtlMoveMemory.Call(uintptr(unsafe.Pointer(&rect)), lParam, unsafe.Sizeof(rect))
		procSetWindowPos.Call(hwnd, HWND_TOPMOST, uintptr(rect.Left), uintptr(rect.Top), uintptr(rect.Right-rect.Left), uintptr(rect.Bottom-rect.Top), SWP_NOACTIVATE)
		a.fpsOverlay.back.Destroy()
		return 0
	case WM_CLOSE:
		procShowWindow.Call(hwnd, SW_HIDE)
		a.fpsOverlay.shown = false
		return 0
	case WM_DESTROY:
		a.fpsOverlay.back.Destroy()
		if a.fpsOverlay.hwnd == hwnd {
			a.fpsOverlay.hwnd = 0
			a.fpsOverlay.shown = false
		}
		return 0
	}
	r, _, _ := procDefWindowProcW.Call(hwnd, msg, wParam, lParam)
	return r
}

func (a *App) syncFPSOverlay() {
	if !a.config.Gaming.FPSHUD || a.config.Gaming.LockedProcessName == "" {
		a.hideFPSOverlay()
		return
	}
	// A desktop-composited HUD is not injected, but a detected anti-cheat still
	// gets the strictest possible lane: no Kerneon surface above the game.
	if a.antiCheatGuardIsEnabled() && antiCheatProcessEvidence(a.snapshot.Processes).Detected {
		a.hideFPSOverlay()
		return
	}
	game, running := a.lockedGameProcess()
	if !running || foregroundProcessID() != game.PID {
		a.hideFPSOverlay()
		return
	}
	if !a.ensureFPSOverlay() {
		return
	}
	now := time.Now()
	a.fpsOverlay.cached = a.frames.Snapshot()
	a.fpsOverlay.cachedAt = now
	if now.Sub(a.fpsOverlay.lastPosition) >= time.Second {
		a.positionFPSOverlay()
	}
	if !a.fpsOverlay.shown {
		procShowWindow.Call(a.fpsOverlay.hwnd, SW_SHOWNOACTIVATE)
		a.fpsOverlay.shown = true
	}
	// Frame collection is naturally per-present. The desktop surface is updated
	// only at the readable 5 Hz cadence so the counter cannot become the
	// workload it is measuring or interfere with frame presentation.
	procInvalidateRect.Call(a.fpsOverlay.hwnd, 0, 0)
}

func (a *App) ensureFPSOverlay() bool {
	if a.fpsOverlay.hwnd != 0 {
		return true
	}
	foreground, _, _ := procGetForegroundWindow.Call()
	dpi := int32(96)
	if foreground != 0 {
		if value, _, _ := procGetDpiForWindow.Call(foreground); value > 0 {
			dpi = int32(value)
		}
	}
	scale := func(value int32) int32 { return int32(math.Round(float64(value) * float64(dpi) / 96)) }
	width, height := scale(196), scale(68)
	exStyle := uintptr(WS_EX_TOPMOST | WS_EX_TRANSPARENT | WS_EX_TOOLWINDOW | WS_EX_NOACTIVATE | WS_EX_LAYERED)
	hwnd, _, _ := procCreateWindowExW.Call(exStyle, uintptr(unsafe.Pointer(utf16Ptr(fpsOverlayWindowClass))), 0, WS_POPUP, 0, 0, uintptr(width), uintptr(height), 0, 0, a.hInstance, 0)
	if hwnd == 0 {
		return false
	}
	a.fpsOverlay.hwnd, a.fpsOverlay.dpi = hwnd, dpi
	dark := int32(1)
	procDwmSetWindowAttribute.Call(hwnd, DWMWA_USE_IMMERSIVE_DARK_MODE, uintptr(unsafe.Pointer(&dark)), unsafe.Sizeof(dark))
	corner := int32(DWMWCP_ROUND)
	procDwmSetWindowAttribute.Call(hwnd, DWMWA_WINDOW_CORNER_PREFERENCE, uintptr(unsafe.Pointer(&corner)), unsafe.Sizeof(corner))
	procSetLayeredWindowAttributes.Call(hwnd, 0, 244, LWA_ALPHA)
	a.positionFPSOverlay()
	return true
}

func (a *App) positionFPSOverlay() {
	hwnd := a.fpsOverlay.hwnd
	if hwnd == 0 {
		return
	}
	foreground, _, _ := procGetForegroundWindow.Call()
	if foreground == 0 || foreground == a.hwnd || foreground == hwnd {
		return
	}
	monitor, _, _ := procMonitorFromWindow.Call(foreground, MONITOR_DEFAULTTONEAREST)
	if monitor == 0 {
		return
	}
	info := MONITORINFO{Size: uint32(unsafe.Sizeof(MONITORINFO{}))}
	if ok, _, _ := procGetMonitorInfoW.Call(monitor, uintptr(unsafe.Pointer(&info))); ok == 0 {
		return
	}
	dpi := a.fpsOverlay.dpi
	if value, _, _ := procGetDpiForWindow.Call(foreground); value > 0 {
		dpi = int32(value)
		a.fpsOverlay.dpi = dpi
	}
	scale := func(value int32) int32 { return int32(math.Round(float64(value) * float64(dpi) / 96)) }
	left, top := info.Monitor.Left+scale(18), info.Monitor.Top+scale(18)
	procSetWindowPos.Call(hwnd, HWND_TOPMOST, uintptr(left), uintptr(top), uintptr(scale(196)), uintptr(scale(68)), SWP_NOACTIVATE|SWP_NOOWNERZORDER|SWP_SHOWWINDOW)
	a.fpsOverlay.shown = true
	a.fpsOverlay.lastPosition = time.Now()
}

func (a *App) hideFPSOverlay() {
	if a.fpsOverlay.hwnd != 0 && a.fpsOverlay.shown {
		procShowWindow.Call(a.fpsOverlay.hwnd, SW_HIDE)
		a.fpsOverlay.shown = false
	}
}

func (a *App) destroyFPSOverlay() {
	if a.fpsOverlay.hwnd != 0 {
		procDestroyWindow.Call(a.fpsOverlay.hwnd)
	}
	a.fpsOverlay.back.Destroy()
	a.fpsOverlay = FPSOverlay{}
}

func (a *App) paintFPSOverlay(hwnd uintptr) {
	var ps PAINTSTRUCT
	hdc, _, _ := procBeginPaint.Call(hwnd, uintptr(unsafe.Pointer(&ps)))
	if hdc == 0 {
		return
	}
	defer procEndPaint.Call(hwnd, uintptr(unsafe.Pointer(&ps)))
	var rect RECT
	procGetClientRect.Call(hwnd, uintptr(unsafe.Pointer(&rect)))
	if !a.fpsOverlay.back.Ensure(hdc, rect.Right, rect.Bottom) {
		return
	}
	dpi := a.fpsOverlay.dpi
	if dpi <= 0 {
		dpi = 96
	}
	c := &Canvas{HDC: a.fpsOverlay.back.DC, AA: newAAGraphics(a.fpsOverlay.back.DC), W: rect.Right, H: rect.Bottom, DPI: dpi, app: a}
	if c.AA != 0 {
		defer procGdipDeleteGraphics.Call(c.AA)
	}
	c.fill(RECT{0, 0, c.W, c.H}, rgb(15, 17, 20))
	c.roundedFill(RECT{0, 0, c.W, c.H}, 15, rgb(18, 21, 25))
	c.line(c.s(14), c.s(1), c.W-c.s(14), c.s(1), palette.Cyan, 1)
	statusColor, heading := palette.Cyan, "KERNEON  ·  FRAME HUD"
	if a.fpsOverlay.cached.Error != "" {
		statusColor, heading = palette.Amber, "KERNEON  ·  HUD DEGRADED"
	}
	c.circle(c.s(17), c.s(18), c.s(3), statusColor)
	c.text(heading, RECT{c.s(27), c.s(6), c.W - c.s(58), c.s(29)}, 7, 720, palette.Muted, DT_LEFT|DT_VCENTER|DT_SINGLELINE)
	c.mono("PER FRAME", RECT{c.W - c.s(64), c.s(6), c.W - c.s(12), c.s(29)}, 7, 650, palette.Muted2, DT_RIGHT|DT_VCENTER|DT_SINGLELINE)
	stats := a.fpsOverlay.cached
	if stats.Samples >= 30 && stats.FPS > 0 {
		c.mono(fmt.Sprintf("%.0f", stats.FPS), RECT{c.s(14), c.s(25), c.s(78), c.H - c.s(5)}, 19, 690, palette.Text, DT_LEFT|DT_VCENTER|DT_SINGLELINE)
		c.text("FPS", RECT{c.s(75), c.s(35), c.s(108), c.H - c.s(6)}, 8, 700, palette.Cyan, DT_LEFT|DT_VCENTER|DT_SINGLELINE)
		low := "—"
		if stats.OnePercentLow > 0 {
			low = fmt.Sprintf("%.0f", stats.OnePercentLow)
		}
		c.text("1% LOW", RECT{c.s(121), c.s(28), c.W - c.s(12), c.s(45)}, 7, 650, palette.Muted2, DT_RIGHT|DT_VCENTER|DT_SINGLELINE)
		c.mono(low, RECT{c.s(121), c.s(42), c.W - c.s(12), c.H - c.s(6)}, 10, 670, palette.Text, DT_RIGHT|DT_VCENTER|DT_SINGLELINE)
	} else {
		c.text("Measuring frame delivery…", RECT{c.s(14), c.s(28), c.W - c.s(14), c.H - c.s(7)}, 10, 600, palette.Text, DT_LEFT|DT_VCENTER|DT_SINGLELINE|DT_END_ELLIPSIS)
	}
	aaFlush(c.AA)
	procBitBlt.Call(hdc, 0, 0, uintptr(c.W), uintptr(c.H), c.HDC, 0, 0, SRCCOPY)
}
