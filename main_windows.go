//go:build windows

package main

import (
	"fmt"
	"math"
	"runtime"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"kerneon/core"
)

const (
	NIM_ADD              = 0
	NIM_DELETE           = 2
	NIM_SETVERSION       = 4
	NIF_MESSAGE          = 0x1
	NIF_ICON             = 0x2
	NIF_TIP              = 0x4
	NOTIFYICON_VERSION_4 = 4
)

type notifyIconData struct {
	Size                       uint32
	Hwnd                       uintptr
	ID, Flags, CallbackMessage uint32
	Icon                       uintptr
	Tip                        [128]uint16
	State, StateMask           uint32
	Info                       [256]uint16
	Timeout                    uint32
	InfoTitle                  [64]uint16
	InfoFlags                  uint32
	Guid                       GUID
	BalloonIcon                uintptr
}

type HitRegion struct {
	Rect   RECT
	Action string
	Value  int
}

type App struct {
	hwnd, hInstance, icon      uintptr
	logger                     *Logger
	store                      *ConfigStore
	config                     core.Config
	engine                     *TelemetryEngine
	page                       string
	width, height              int32
	dpi                        int32
	minimized                  atomic.Bool
	gameDetected               atomic.Bool
	closing                    atomic.Bool
	renderStop                 chan struct{}
	display                    DisplayState
	snapshot                   Snapshot
	hits                       []HitRegion
	hover                      POINT
	hoverValid                 bool
	search                     string
	processMap                 bool
	selectedPID                uint32
	selectedPath               string
	incident                   []HistorySample
	compact                    bool
	back                       BackBuffer
	downFormatter, upFormatter core.RateFormatter
	scalers                    map[string]*core.GraphScaler
}

var appInstance *App

func main() {
	runtime.LockOSThread()
	logger := NewLogger()
	defer logger.Close()
	defer func() {
		if recovered := recover(); recovered != nil {
			logger.Panic("main", recovered)
		}
	}()
	store := NewConfigStore(logger)
	cfg := store.Load()
	a := &App{logger: logger, store: store, config: cfg, page: cfg.Window.LastPage, dpi: 96, renderStop: make(chan struct{}), scalers: make(map[string]*core.GraphScaler)}
	a.downFormatter.Mode, a.upFormatter.Mode = cfg.Network.Units, cfg.Network.Units
	a.engine = NewTelemetryEngine(cfg, logger)
	appInstance = a
	if err := a.run(); err != nil {
		logger.Error("main", "application failed", err)
	}
}

func (a *App) run() error {
	procSetProcessDpiAwarenessContext.Call(^uintptr(3)) // DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2 (-4)
	hInst, _, _ := procGetModuleHandleW.Call(0)
	a.hInstance = hInst
	className := utf16Ptr("KerneonMainWindow")
	cursor, _, _ := procLoadCursorW.Call(0, IDC_ARROW)
	icon, _, _ := procLoadIconW.Call(hInst, 1)
	a.icon = icon
	wc := WNDCLASSEX{CbSize: uint32(unsafe.Sizeof(WNDCLASSEX{})), Style: CS_HREDRAW | CS_VREDRAW | CS_DBLCLKS, LpfnWndProc: syscall.NewCallback(wndProc), HInstance: hInst, HIcon: icon, HCursor: cursor, HbrBackground: COLOR_WINDOW + 1, LpszClassName: className, HIconSm: icon}
	if r, _, err := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc))); r == 0 {
		return fmt.Errorf("register window class: %w", err)
	}
	x, y := int32(-2147483648), int32(-2147483648) // CW_USEDEFAULT
	if a.config.Window.X >= 0 {
		x = int32(a.config.Window.X)
	}
	if a.config.Window.Y >= 0 {
		y = int32(a.config.Window.Y)
	}
	hwnd, _, err := procCreateWindowExW.Call(0, uintptr(unsafe.Pointer(className)), uintptr(unsafe.Pointer(utf16Ptr("Kerneon — Understand your PC"))), WS_OVERLAPPEDWINDOW|WS_VISIBLE|WS_CLIPCHILDREN, uintptr(x), uintptr(y), uintptr(a.config.Window.Width), uintptr(a.config.Window.Height), 0, 0, hInst, 0)
	if hwnd == 0 {
		return fmt.Errorf("create window: %w", err)
	}
	a.hwnd = hwnd
	if dpi, _, _ := procGetDpiForWindow.Call(hwnd); dpi > 0 {
		a.dpi = int32(dpi)
	}
	dark := int32(1)
	procDwmSetWindowAttribute.Call(hwnd, DWMWA_USE_IMMERSIVE_DARK_MODE, uintptr(unsafe.Pointer(&dark)), unsafe.Sizeof(dark))
	corner := int32(DWMWCP_ROUND)
	procDwmSetWindowAttribute.Call(hwnd, DWMWA_WINDOW_CORNER_PREFERENCE, uintptr(unsafe.Pointer(&corner)), unsafe.Sizeof(corner))
	a.applyTopMost()
	a.addTrayIcon()
	a.engine.Start()
	go a.renderLoop()
	procShowWindow.Call(hwnd, SW_SHOW)
	procUpdateWindow.Call(hwnd)
	var msg MSG
	for {
		r, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if int32(r) <= 0 {
			break
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&msg)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&msg)))
	}
	return nil
}

func wndProc(hwnd, msg, wParam, lParam uintptr) uintptr {
	a := appInstance
	if a == nil {
		r, _, _ := procDefWindowProcW.Call(hwnd, msg, wParam, lParam)
		return r
	}
	switch msg {
	case WM_ERASEBKGND:
		return 1
	case WM_SIZE:
		lowPower := wParam == SIZE_MINIMIZED
		a.minimized.Store(lowPower)
		a.engine.SetLowPower(lowPower)
		if wParam != SIZE_MINIMIZED {
			a.width = int32(lParam & 0xffff)
			a.height = int32((lParam >> 16) & 0xffff)
			a.config.Window.Width = int(a.width)
			a.config.Window.Height = int(a.height)
			procInvalidateRect.Call(hwnd, 0, 0)
		}
		return 0
	case WM_MOVE:
		if !a.minimized.Load() {
			var r RECT
			procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&r)))
			a.config.Window.X, a.config.Window.Y = int(r.Left), int(r.Top)
		}
		return 0
	case WM_GETMINMAXINFO:
		var mmi MINMAXINFO
		procRtlMoveMemory.Call(uintptr(unsafe.Pointer(&mmi)), lParam, unsafe.Sizeof(mmi))
		mmi.PtMinTrackSize.X = a.px(960)
		mmi.PtMinTrackSize.Y = a.px(640)
		procRtlMoveMemory.Call(lParam, uintptr(unsafe.Pointer(&mmi)), unsafe.Sizeof(mmi))
		return 0
	case WM_DPICHANGED:
		a.dpi = int32(wParam & 0xffff)
		var r RECT
		procRtlMoveMemory.Call(uintptr(unsafe.Pointer(&r)), lParam, unsafe.Sizeof(r))
		procSetWindowPos.Call(hwnd, 0, uintptr(r.Left), uintptr(r.Top), uintptr(r.Right-r.Left), uintptr(r.Bottom-r.Top), 0)
		a.back.Destroy()
		return 0
	case WM_MOUSEMOVE:
		a.hover = POINT{lowWord(lParam), highWord(lParam)}
		a.hoverValid = true
		return 0
	case WM_LBUTTONUP:
		a.handleClick(lowWord(lParam), highWord(lParam))
		return 0
	case WM_KEYDOWN:
		if wParam == VK_ESCAPE {
			if a.selectedPID != 0 {
				a.selectedPID, a.selectedPath = 0, ""
				procInvalidateRect.Call(hwnd, 0, 0)
			} else if a.page == "overview" {
				a.search = ""
			} else {
				a.navigate("overview")
			}
			return 0
		}
		if wParam == 'R' {
			a.engine.ResetSession()
			return 0
		}
		if wParam == 'M' {
			a.toggleCompact()
			return 0
		}
		if wParam == VK_BACK && a.page == "processes" && len(a.search) > 0 {
			a.search = a.search[:len(a.search)-1]
			return 0
		}
	case WM_CHAR:
		if a.page == "processes" && wParam >= 32 && wParam < 127 && len(a.search) < 48 {
			a.search += string(rune(wParam))
			return 0
		}
	case WM_APP_RENDER:
		a.updateDisplay()
		procInvalidateRect.Call(hwnd, 0, 0)
		return 0
	case WM_APP_TRAY:
		event := uint32(lParam & 0xffff)
		if event == WM_LBUTTONUP || event == WM_LBUTTONDBLCLK {
			procShowWindow.Call(hwnd, SW_RESTORE)
			procSetForegroundWindow.Call(hwnd)
			a.minimized.Store(false)
			a.engine.SetLowPower(false)
		} else if event == WM_RBUTTONUP {
			procDestroyWindow.Call(hwnd)
		}
		return 0
	case WM_POWERBROADCAST:
		if wParam == PBT_APMRESUMEAUTOMATIC {
			a.engine.Resume()
			a.engine.SetLowPower(a.minimized.Load())
			a.logger.Info("power", "system resumed")
		} else if wParam == PBT_APMSUSPEND {
			a.logger.Info("power", "system suspending")
		}
		return 1
	case WM_PAINT:
		a.paint(hwnd)
		return 0
	case WM_CLOSE:
		if a.config.Window.CloseToTray && !a.closing.Load() {
			procShowWindow.Call(hwnd, SW_HIDE)
			a.minimized.Store(true)
			a.engine.SetLowPower(true)
			return 0
		}
		procDestroyWindow.Call(hwnd)
		return 0
	case WM_DESTROY:
		a.shutdown()
		procPostQuitMessage.Call(0)
		return 0
	}
	r, _, _ := procDefWindowProcW.Call(hwnd, msg, wParam, lParam)
	return r
}

func (a *App) renderLoop() {
	lastGameCheck := time.Time{}
	for {
		if time.Since(lastGameCheck) >= time.Second {
			active := a.config.Gaming.GameFocus && fullscreenForeground(a.hwnd)
			if active != a.gameDetected.Load() {
				a.gameDetected.Store(active)
				a.engine.SetGameMode(active)
			}
			lastGameCheck = time.Now()
		}
		fps := a.config.Sampling.GraphFPS
		if a.minimized.Load() || (a.gameDetected.Load() && !a.compact) {
			fps = 1
		}
		if fps < 1 {
			fps = 30
		}
		timer := time.NewTimer(time.Second / time.Duration(fps))
		select {
		case <-timer.C:
			if a.hwnd != 0 {
				procPostMessageW.Call(a.hwnd, WM_APP_RENDER, 0, 0)
			}
		case <-a.renderStop:
			timer.Stop()
			return
		}
	}
}

func fullscreenForeground(ownWindow uintptr) bool {
	foreground, _, _ := procGetForegroundWindow.Call()
	if foreground == 0 || foreground == ownWindow {
		return false
	}
	monitor, _, _ := procMonitorFromWindow.Call(foreground, MONITOR_DEFAULTTONEAREST)
	if monitor == 0 {
		return false
	}
	var window RECT
	if ok, _, _ := procGetWindowRect.Call(foreground, uintptr(unsafe.Pointer(&window))); ok == 0 {
		return false
	}
	info := MONITORINFO{Size: uint32(unsafe.Sizeof(MONITORINFO{}))}
	if ok, _, _ := procGetMonitorInfoW.Call(monitor, uintptr(unsafe.Pointer(&info))); ok == 0 {
		return false
	}
	const tolerance = int32(2)
	return window.Left <= info.Monitor.Left+tolerance && window.Top <= info.Monitor.Top+tolerance &&
		window.Right >= info.Monitor.Right-tolerance && window.Bottom >= info.Monitor.Bottom-tolerance
}

func (a *App) updateDisplay() {
	s := a.engine.Snapshot()
	now := time.Now()
	dt := now.Sub(a.display.updated)
	if a.display.updated.IsZero() {
		dt = time.Second
	}
	a.display.updated = now
	if a.config.Appearance.ReducedMotion {
		a.display = DisplayState{CPU: s.CPU.Usage, GPU: s.GPU.Usage, Memory: s.Memory.UsagePercent, Disk: s.Disk.Usage, Down: s.Network.DownBps, Up: s.Network.UpBps, Latency: s.Network.LatencyMs, updated: now}
	} else {
		a.display.CPU = approach(a.display.CPU, s.CPU.Usage, dt, 160*time.Millisecond)
		a.display.GPU = approach(a.display.GPU, s.GPU.Usage, dt, 190*time.Millisecond)
		a.display.Memory = approach(a.display.Memory, s.Memory.UsagePercent, dt, 350*time.Millisecond)
		a.display.Disk = approach(a.display.Disk, s.Disk.Usage, dt, 180*time.Millisecond)
		a.display.Down = approach(a.display.Down, s.Network.DownBps, dt, 150*time.Millisecond)
		a.display.Up = approach(a.display.Up, s.Network.UpBps, dt, 150*time.Millisecond)
		a.display.Latency = approach(a.display.Latency, s.Network.LatencyMs, dt, 250*time.Millisecond)
	}
	a.snapshot = s
}
func approach(current, target float64, dt, tau time.Duration) float64 {
	if tau <= 0 || dt <= 0 {
		return target
	}
	alpha := 1 - math.Exp(-dt.Seconds()/tau.Seconds())
	if math.Abs(target-current) < 0.001 {
		return target
	}
	return current + alpha*(target-current)
}

func (a *App) navigate(page string) {
	a.page = page
	a.config.Window.LastPage = page
	a.hoverValid = false
	procInvalidateRect.Call(a.hwnd, 0, 0)
}
func (a *App) px(v int32) int32 { return int32(float64(v) * float64(a.dpi) / 96.0) }
func (a *App) applyTopMost() {
	insert := uintptr(HWND_NOTOPMOST)
	if a.config.Window.AlwaysTop {
		insert = HWND_TOPMOST
	}
	if a.hwnd != 0 {
		procSetWindowPos.Call(a.hwnd, insert, 0, 0, 0, 0, SWP_NOMOVE|SWP_NOSIZE|SWP_NOACTIVATE|SWP_NOOWNERZORDER)
	}
}

func (a *App) handleClick(x, y int32) {
	for i := len(a.hits) - 1; i >= 0; i-- {
		h := a.hits[i]
		if x >= h.Rect.Left && x < h.Rect.Right && y >= h.Rect.Top && y < h.Rect.Bottom {
			a.runAction(h.Action, h.Value)
			return
		}
	}
}

func (a *App) runAction(action string, value int) {
	if strings.HasPrefix(action, "nav:") {
		a.navigate(strings.TrimPrefix(action, "nav:"))
		return
	}
	switch action {
	case "toggle-process-map":
		a.processMap = !a.processMap
	case "process-detail":
		a.selectedPID = uint32(value)
		a.selectedPath = queryProcessPath(a.selectedPID)
	case "close-process-detail":
		a.selectedPID, a.selectedPath = 0, ""
	case "copy-process-pid":
		copyText(fmt.Sprint(a.selectedPID))
	case "copy-process-path":
		if a.selectedPath != "" {
			copyText(a.selectedPath)
		}
	case "open-process-location":
		if a.selectedPath != "" {
			shellOpenSelect(a.selectedPath)
		}
	case "clear-search":
		a.search = ""
	case "capture-incident":
		a.incident = a.snapshotHistory(60 * time.Second)
		a.engine.AddUserEvent("history", "Captured the last 60 seconds", fmt.Sprintf("%d synchronized samples were preserved.", len(a.incident)), 0)
		a.navigate("history")
	case "reset-session":
		a.engine.ResetSession()
	case "toggle-top":
		a.config.Window.AlwaysTop = !a.config.Window.AlwaysTop
		a.applyTopMost()
		a.saveAndRestart(false)
	case "toggle-tray":
		a.config.Window.CloseToTray = !a.config.Window.CloseToTray
		a.saveAndRestart(false)
	case "toggle-motion":
		a.config.Appearance.ReducedMotion = !a.config.Appearance.ReducedMotion
		a.saveAndRestart(false)
	case "toggle-adaptive":
		a.config.Sampling.Adaptive = !a.config.Sampling.Adaptive
		a.saveAndRestart(true)
	case "toggle-gamefocus":
		a.config.Gaming.GameFocus = !a.config.Gaming.GameFocus
		a.saveAndRestart(false)
	case "network-hz":
		vals := []int{10, 20, 30, 60, 90, 120}
		a.config.Sampling.NetworkHz = cycleInt(vals, a.config.Sampling.NetworkHz, value)
		a.saveAndRestart(true)
	case "graph-fps":
		vals := []int{30, 60, 120}
		a.config.Sampling.GraphFPS = cycleInt(vals, a.config.Sampling.GraphFPS, value)
		a.saveAndRestart(false)
	case "graph-seconds":
		vals := []int{30, 60, 120, 300}
		a.config.Appearance.GraphSeconds = cycleInt(vals, a.config.Appearance.GraphSeconds, value)
		a.saveAndRestart(false)
	case "units":
		vals := []core.UnitMode{core.UnitAuto, core.UnitBits, core.UnitBytes}
		a.config.Network.Units = cycleMode(vals, a.config.Network.Units, value)
		a.downFormatter.Mode = a.config.Network.Units
		a.upFormatter.Mode = a.config.Network.Units
		a.saveAndRestart(false)
	case "ping":
		vals := []int{0, 1, 2, 5, 10}
		a.config.Network.PingSeconds = cycleInt(vals, a.config.Network.PingSeconds, value)
		a.saveAndRestart(true)
	case "defaults":
		a.config = core.DefaultConfig()
		a.downFormatter.Mode, a.upFormatter.Mode = a.config.Network.Units, a.config.Network.Units
		a.applyTopMost()
		a.saveAndRestart(true)
	case "compact":
		a.toggleCompact()
	case "open-logs":
		shellOpen(a.logger.Dir())
	case "copy-system":
		copyText(a.systemSummary())
	case "export-diagnostics":
		if path, err := a.exportDiagnostics(); err != nil {
			a.logger.Error("diagnostics", "export", err)
		} else {
			shellOpen(path)
		}
	}
	procInvalidateRect.Call(a.hwnd, 0, 0)
}

func (a *App) saveAndRestart(restart bool) {
	if err := a.store.Save(a.config); err != nil {
		a.logger.Error("settings", "save", err)
	}
	if restart {
		a.engine.Restart(a.config)
	}
}
func (a *App) snapshotHistory(d time.Duration) []HistorySample {
	cut := time.Now().Add(-d)
	h := a.snapshot.History
	start := 0
	for start < len(h) && h[start].At.Before(cut) {
		start++
	}
	return append([]HistorySample(nil), h[start:]...)
}
func cycleInt(v []int, cur, dir int) int {
	idx := 0
	for i, x := range v {
		if x == cur {
			idx = i
			break
		}
	}
	idx = (idx + dir + len(v)) % len(v)
	return v[idx]
}
func cycleMode(v []core.UnitMode, cur core.UnitMode, dir int) core.UnitMode {
	idx := 0
	for i, x := range v {
		if x == cur {
			idx = i
			break
		}
	}
	idx = (idx + dir + len(v)) % len(v)
	return v[idx]
}

func (a *App) toggleCompact() {
	a.compact = !a.compact
	if a.compact {
		procSetWindowPos.Call(a.hwnd, HWND_TOPMOST, 0, 0, uintptr(a.px(430)), uintptr(a.px(235)), SWP_NOMOVE|SWP_NOACTIVATE)
	} else {
		procSetWindowPos.Call(a.hwnd, 0, 0, 0, uintptr(a.px(int32(a.config.Window.Width))), uintptr(a.px(int32(a.config.Window.Height))), SWP_NOMOVE|SWP_NOACTIVATE)
		a.applyTopMost()
	}
	a.back.Destroy()
}

func (a *App) addTrayIcon() {
	if a.icon == 0 {
		return
	}
	var n notifyIconData
	n.Size = uint32(unsafe.Sizeof(n))
	n.Hwnd = a.hwnd
	n.ID = 1
	n.Flags = NIF_MESSAGE | NIF_ICON | NIF_TIP
	n.CallbackMessage = WM_APP_TRAY
	n.Icon = a.icon
	copy(n.Tip[:], syscall.StringToUTF16("Kerneon — click to restore · right-click to exit"))
	procShellNotifyIconW.Call(NIM_ADD, uintptr(unsafe.Pointer(&n)))
	n.Timeout = NOTIFYICON_VERSION_4
	procShellNotifyIconW.Call(NIM_SETVERSION, uintptr(unsafe.Pointer(&n)))
}
func (a *App) removeTrayIcon() {
	var n notifyIconData
	n.Size = uint32(unsafe.Sizeof(n))
	n.Hwnd = a.hwnd
	n.ID = 1
	procShellNotifyIconW.Call(NIM_DELETE, uintptr(unsafe.Pointer(&n)))
}

func (a *App) shutdown() {
	if !a.closing.CompareAndSwap(false, true) {
		return
	}
	close(a.renderStop)
	a.engine.Stop()
	a.removeTrayIcon()
	a.back.Destroy()
	freeGDICache()
	a.config.Window.LastPage = a.page
	if err := a.store.Save(a.config); err != nil {
		a.logger.Error("settings", "save on shutdown", err)
	}
	a.logger.Info("main", "Kerneon stopped")
}

func shellOpen(path string) {
	procShellExecuteW.Call(0, uintptr(unsafe.Pointer(utf16Ptr("open"))), uintptr(unsafe.Pointer(utf16Ptr(path))), 0, 0, SW_SHOW)
}
func shellOpenSelect(path string) {
	params := "/select,\"" + strings.ReplaceAll(path, "\"", "") + "\""
	procShellExecuteW.Call(0, uintptr(unsafe.Pointer(utf16Ptr("open"))), uintptr(unsafe.Pointer(utf16Ptr("explorer.exe"))), uintptr(unsafe.Pointer(utf16Ptr(params))), 0, SW_SHOW)
}
func copyText(value string) bool {
	if r, _, _ := procOpenClipboard.Call(0); r == 0 {
		return false
	}
	defer procCloseClipboard.Call()
	procEmptyClipboard.Call()
	u := syscall.StringToUTF16(value)
	size := uintptr(len(u) * 2)
	h, _, _ := procGlobalAlloc.Call(0x0002, size)
	if h == 0 {
		return false
	}
	p, _, _ := procGlobalLock.Call(h)
	if p == 0 {
		return false
	}
	procRtlMoveMemory.Call(p, uintptr(unsafe.Pointer(&u[0])), size)
	procGlobalUnlock.Call(h)
	r, _, _ := procSetClipboardData.Call(13, h)
	return r != 0
}
