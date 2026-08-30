//go:build windows

package main

import (
	"fmt"
	"math"
	"os"
	"runtime"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"kerneon/core"
)

const (
	NIM_ADD                 = 0
	NIM_MODIFY              = 1
	NIM_DELETE              = 2
	NIM_SETVERSION          = 4
	NIF_MESSAGE             = 0x1
	NIF_ICON                = 0x2
	NIF_TIP                 = 0x4
	NOTIFYICON_VERSION_4    = 4
	surgeActivationDuration = 580 * time.Millisecond
	mainWindowClass         = "KerneonMainWindow"
	fpsOverlayWindowClass   = "KerneonFPSOverlay"
	singleInstanceMutex     = "Kerneon.Desktop.SingleInstance.4f8e36c7"
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

type GraphRange struct{ Start, End time.Time }
type GraphPlotRegion struct {
	Rect       RECT
	Key        string
	Start, End time.Time
}

type GraphSampleSelection struct {
	Key, Series string
	At          time.Time
	Point       POINT
	Value       float64
	Color       uint32
	Process     bool
}

type GraphRenderedSeries struct {
	Key, Series string
	Color       uint32
	Process     bool
	Points      []POINT
	Times       []time.Time
	Values      []float64
}

const (
	priorityBelowNormal = 0x00004000
	priorityNormal      = 0x00000020
	priorityAboveNormal = 0x00008000
)

type App struct {
	hwnd, hInstance, icon            uintptr
	logger                           *Logger
	store                            *ConfigStore
	config                           core.Config
	engine                           *TelemetryEngine
	page                             string
	width, height                    int32
	dpi                              int32
	minimized                        atomic.Bool
	gameDetected                     atomic.Bool
	closing                          atomic.Bool
	surgeEnabled                     atomic.Bool
	surgeStateInitialized            atomic.Bool
	antiCheatGuardEnabled            atomic.Bool
	antiCheatGuardStateInitialized   atomic.Bool
	renderPending                    atomic.Bool
	motionUntil                      atomic.Int64
	renderStop                       chan struct{}
	display                          DisplayState
	snapshot                         Snapshot
	hits                             []HitRegion
	hover                            POINT
	hoverValid                       bool
	search                           string
	processMap                       bool
	selectedPID                      uint32
	selectedPath                     string
	processPriorityChoice            uint32
	processPriorityOriginal          uint32
	processDetailMetric              string
	gameCandidatePID                 uint32
	autopilot                        AutopilotState
	incident                         []HistorySample
	compact                          bool
	ai                               AIState
	optimizer                        OptimizerState
	back                             BackBuffer
	surgeActivationBack              BackBuffer
	surgeActivationCached            bool
	downFormatter, upFormatter       core.RateFormatter
	scalers                          map[string]*core.GraphScaler
	graphPlots                       []GraphPlotRegion
	graphRanges                      map[string]GraphRange
	graphDragKey                     string
	graphDragStart, graphDragCurrent POINT
	graphHover, graphPinned          GraphSampleSelection
	graphLines                       []GraphRenderedSeries
	graphInspectionPaused            bool
	graphInspectionAt                time.Time
	graphInspectionHistory           []HistorySample
	graphInspectionProcessHistory    []ProcessHistorySample
	graphInspectionProcesses         []ProcessMetric
	componentBreakdown               map[string]bool
	remote                           RemoteServer
	screen                           ScreenEngine
	frames                           FrameMonitor
	fpsOverlay                       FPSOverlay
	hardware                         HardwareState
	eventLens                        EventLensState
	selectedEventRawID               string
	lastStutterCapture               time.Time
	pageTransitionStart              time.Time
	pageTransitionDirection          int32
	surgeActivationStart             time.Time
	surgeModeConfirmUntil            time.Time
	surgeGuardConfirmUntil           time.Time
	surgeNavRect                     RECT
	surgeSafetyOpen                  bool
	surgeDependenciesOpen            bool
	surgeTuningOpen                  bool
	surgeTuningConsentConfirmUntil   time.Time
	surgeTuningResumeAfterSafety     bool
	surgeStack                       SurgeStackState
	tuningLab                        TuningLabState
	remotePairCodeRevealed           atomic.Bool
	displayRefreshHz                 int
	pressedHit, releasedHit          HitRegion
	pressedValid, releasedValid      bool
	pressReleaseStart                time.Time
	animationLast                    time.Time
	animationDelta                   time.Duration
	animationProgress                map[string]float64
	startupEnabled                   bool
	startupBannerError               string
}

func priorityLabel(priority uint32) string {
	switch priority {
	case priorityBelowNormal:
		return "Below normal"
	case priorityNormal:
		return "Normal"
	case priorityAboveNormal:
		return "Above normal"
	default:
		return "Unavailable"
	}
}

// Surge and the anti-cheat boundary are safety-critical runtime switches. They
// are mirrored atomically so a background measurement/save cannot resurrect a
// stale value after the UI has switched either control off. Tests and small
// helper Apps that have not initialised the runtime mirror fall back to config.
func (a *App) surgeIsEnabled() bool {
	if !a.surgeStateInitialized.Load() {
		return a.config.Tuning.Autopilot
	}
	return a.surgeEnabled.Load()
}

func (a *App) setSurgeEnabled(enabled bool) {
	a.config.Tuning.Autopilot = enabled
	a.surgeEnabled.Store(enabled)
	a.surgeStateInitialized.Store(true)
}

func (a *App) antiCheatGuardIsEnabled() bool {
	if !a.antiCheatGuardStateInitialized.Load() {
		return a.config.Tuning.AntiCheatGuard
	}
	return a.antiCheatGuardEnabled.Load()
}

func (a *App) setAntiCheatGuardEnabled(enabled bool) {
	a.config.Tuning.AntiCheatGuard = enabled
	a.antiCheatGuardEnabled.Store(enabled)
	a.antiCheatGuardStateInitialized.Store(true)
}

func (a *App) configSnapshot() core.Config {
	config := a.config
	config.Tuning.Autopilot = a.surgeIsEnabled()
	config.Tuning.AntiCheatGuard = a.antiCheatGuardIsEnabled()
	return config
}

func (a *App) persistCriticalConfig(operation string) bool {
	if a.store == nil {
		return false
	}
	if err := a.store.Save(a.configSnapshot()); err != nil {
		if a.logger != nil {
			a.logger.Error("settings", operation, err)
		}
		return false
	}
	return true
}

var appInstance *App

func acquireSingleInstance() (handle uintptr, alreadyRunning bool, err error) {
	name := utf16Ptr(singleInstanceMutex)
	handle, _, callErr := procCreateMutexW.Call(0, 0, uintptr(unsafe.Pointer(name)))
	if handle == 0 {
		return 0, false, fmt.Errorf("create single-instance guard: %w", callErr)
	}
	// CreateMutex returns a valid handle in both cases; GetLastError is 183 only
	// when another process already owns the named object.
	return handle, callErr == syscall.Errno(183), nil
}

func focusExistingInstance() {
	className := utf16Ptr(mainWindowClass)
	// The mutex is acquired before the first window is registered, so tolerate
	// the narrow startup race when two launches happen almost simultaneously.
	for attempt := 0; attempt < 30; attempt++ {
		hwnd, _, _ := procFindWindowW.Call(uintptr(unsafe.Pointer(className)), 0)
		if hwnd != 0 {
			restoreAndFocusWindow(hwnd)
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func focusLegacyInstanceIfPresent() bool {
	className := utf16Ptr(mainWindowClass)
	hwnd, _, _ := procFindWindowW.Call(uintptr(unsafe.Pointer(className)), 0)
	if hwnd == 0 {
		return false
	}
	restoreAndFocusWindow(hwnd)
	return true
}

func restorableWindowRect(rect RECT) bool {
	return rect.Right > rect.Left && rect.Bottom > rect.Top && rect.Left > -30000 && rect.Top > -30000 && rect.Right < 30000 && rect.Bottom < 30000
}

func windowRectVisibleInWorkArea(rect, work RECT) bool {
	if !restorableWindowRect(rect) {
		return false
	}
	visibleLeft := max32(rect.Left, work.Left)
	visibleRight := min32(rect.Right, work.Right)
	return visibleRight-visibleLeft >= 96 && rect.Top >= work.Top-8 && rect.Top <= work.Bottom-32
}

func ensureWindowVisible(hwnd uintptr) {
	if hwnd == 0 {
		return
	}
	var rect RECT
	if ok, _, _ := procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&rect))); ok == 0 {
		return
	}
	monitor, _, _ := procMonitorFromWindow.Call(hwnd, MONITOR_DEFAULTTONEAREST)
	if monitor == 0 {
		return
	}
	info := MONITORINFO{Size: uint32(unsafe.Sizeof(MONITORINFO{}))}
	if ok, _, _ := procGetMonitorInfoW.Call(monitor, uintptr(unsafe.Pointer(&info))); ok == 0 || windowRectVisibleInWorkArea(rect, info.Work) {
		return
	}
	width, height := rect.Right-rect.Left, rect.Bottom-rect.Top
	if width < 430 || width > info.Work.Right-info.Work.Left {
		width = min32(1240, info.Work.Right-info.Work.Left)
	}
	if height < 270 || height > info.Work.Bottom-info.Work.Top {
		height = min32(800, info.Work.Bottom-info.Work.Top)
	}
	x := info.Work.Left + (info.Work.Right-info.Work.Left-width)/2
	y := info.Work.Top + (info.Work.Bottom-info.Work.Top-height)/2
	procSetWindowPos.Call(hwnd, 0, uintptr(x), uintptr(y), uintptr(width), uintptr(height), SWP_NOZORDER|SWP_NOACTIVATE)
}

func primaryWindowPosition(width, height int32) (x, y int32, ok bool) {
	monitor, _, _ := procMonitorFromWindow.Call(0, MONITOR_DEFAULTTOPRIMARY)
	if monitor == 0 {
		return 0, 0, false
	}
	info := MONITORINFO{Size: uint32(unsafe.Sizeof(MONITORINFO{}))}
	if result, _, _ := procGetMonitorInfoW.Call(monitor, uintptr(unsafe.Pointer(&info))); result == 0 {
		return 0, 0, false
	}
	width = min32(width, info.Work.Right-info.Work.Left)
	height = min32(height, info.Work.Bottom-info.Work.Top)
	return info.Work.Left + (info.Work.Right-info.Work.Left-width)/2, info.Work.Top + (info.Work.Bottom-info.Work.Top-height)/2, true
}

func restoreAndFocusWindow(hwnd uintptr) {
	procShowWindow.Call(hwnd, SW_RESTORE)
	ensureWindowVisible(hwnd)
	procShowWindow.Call(hwnd, SW_SHOW)
	procSetForegroundWindow.Call(hwnd)
}

func main() {
	runtime.LockOSThread()
	elevatedTuningRequest := false
	for _, argument := range os.Args[1:] {
		if argument == "--surge-tuning-elevated" {
			elevatedTuningRequest = true
			break
		}
	}
	logger := NewLogger()
	defer logger.Close()
	defer func() {
		if recovered := recover(); recovered != nil {
			logger.Panic("main", recovered)
		}
	}()
	var instanceHandle uintptr
	var alreadyRunning bool
	var err error
	for attempt := 0; ; attempt++ {
		instanceHandle, alreadyRunning, err = acquireSingleInstance()
		if err != nil || !alreadyRunning || !elevatedTuningRequest || attempt >= 80 {
			break
		}
		procCloseHandle.Call(instanceHandle)
		time.Sleep(100 * time.Millisecond)
	}
	if err != nil {
		logger.Error("main", "single-instance guard failed", err)
		return
	}
	defer procCloseHandle.Call(instanceHandle)
	if alreadyRunning {
		focusExistingInstance()
		return
	}
	// Builds predating the named mutex still use the same private window class.
	// Detect that hidden/tray window too so an in-place upgrade never creates a
	// one-time duplicate while the previous executable is still winding down.
	if focusLegacyInstanceIfPresent() {
		return
	}
	store := NewConfigStore(logger)
	cfg := store.Load()
	page := cfg.Window.LastPage
	if elevatedTuningRequest {
		page = "optimize"
	}
	a := &App{logger: logger, store: store, config: cfg, page: page, dpi: 96, renderStop: make(chan struct{}), scalers: make(map[string]*core.GraphScaler), graphRanges: make(map[string]GraphRange), componentBreakdown: make(map[string]bool), animationProgress: make(map[string]float64), surgeTuningOpen: elevatedTuningRequest}
	a.setSurgeEnabled(cfg.Tuning.Autopilot)
	a.setAntiCheatGuardEnabled(cfg.Tuning.AntiCheatGuard)
	a.startupEnabled = startupEntryEnabled()
	a.ai.Connected = a.hasAIKey()
	a.downFormatter.Mode, a.upFormatter.Mode = cfg.Network.Units, cfg.Network.Units
	a.restoreInterruptedOptimization()
	a.restoreInterruptedAutopilotSnapshot()
	a.restoreInterruptedHardwareTuning()
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
	className := utf16Ptr(mainWindowClass)
	cursor, _, _ := procLoadCursorW.Call(0, IDC_ARROW)
	icon, _, _ := procLoadIconW.Call(hInst, 1)
	a.icon = icon
	wc := WNDCLASSEX{CbSize: uint32(unsafe.Sizeof(WNDCLASSEX{})), Style: CS_HREDRAW | CS_VREDRAW | CS_DBLCLKS, LpfnWndProc: syscall.NewCallback(wndProc), HInstance: hInst, HIcon: icon, HCursor: cursor, HbrBackground: COLOR_WINDOW + 1, LpszClassName: className, HIconSm: icon}
	if r, _, err := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc))); r == 0 {
		return fmt.Errorf("register window class: %w", err)
	}
	overlayClassName := utf16Ptr(fpsOverlayWindowClass)
	overlayClass := WNDCLASSEX{CbSize: uint32(unsafe.Sizeof(WNDCLASSEX{})), Style: CS_HREDRAW | CS_VREDRAW, LpfnWndProc: syscall.NewCallback(fpsOverlayWndProc), HInstance: hInst, HCursor: cursor, HbrBackground: COLOR_WINDOW + 1, LpszClassName: overlayClassName}
	if r, _, err := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&overlayClass))); r == 0 {
		return fmt.Errorf("register FPS overlay class: %w", err)
	}
	x, y := int32(-2147483648), int32(-2147483648) // CW_USEDEFAULT
	if !(a.config.Window.X == -1 && a.config.Window.Y == -1) && a.config.Window.X > -30000 && a.config.Window.X < 30000 && a.config.Window.Y > -30000 && a.config.Window.Y < 30000 {
		x, y = int32(a.config.Window.X), int32(a.config.Window.Y)
	} else if primaryX, primaryY, ok := primaryWindowPosition(int32(a.config.Window.Width), int32(a.config.Window.Height)); ok {
		x, y = primaryX, primaryY
	}
	hwnd, _, err := procCreateWindowExW.Call(0, uintptr(unsafe.Pointer(className)), uintptr(unsafe.Pointer(utf16Ptr("Kerneon — Understand your PC"))), WS_OVERLAPPEDWINDOW|WS_VISIBLE|WS_CLIPCHILDREN, uintptr(x), uintptr(y), uintptr(a.config.Window.Width), uintptr(a.config.Window.Height), 0, 0, hInst, 0)
	if hwnd == 0 {
		return fmt.Errorf("create window: %w", err)
	}
	a.hwnd = hwnd
	ensureWindowVisible(hwnd)
	a.displayRefreshHz = monitorRefreshRate(hwnd)
	if dpi, _, _ := procGetDpiForWindow.Call(hwnd); dpi > 0 {
		a.dpi = int32(dpi)
	}
	dark := int32(1)
	procDwmSetWindowAttribute.Call(hwnd, DWMWA_USE_IMMERSIVE_DARK_MODE, uintptr(unsafe.Pointer(&dark)), unsafe.Sizeof(dark))
	corner := int32(DWMWCP_ROUND)
	procDwmSetWindowAttribute.Call(hwnd, DWMWA_WINDOW_CORNER_PREFERENCE, uintptr(unsafe.Pointer(&corner)), unsafe.Sizeof(corner))
	a.applyWindowMaterial()
	a.applyTopMost()
	a.addTrayIcon()
	a.engine.Start()
	a.refreshSurgeStack(true)
	a.refreshEventLens()
	a.refreshHardwarePassport()
	if a.config.Remote.Enabled {
		a.startRemoteLink()
	}
	a.refreshPowerPlan()
	if !a.startupEnabled {
		a.requestMotion(650 * time.Millisecond)
	}
	go a.renderLoop()
	go a.fpsOverlayLoop()
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
			a.surgeActivationCached = false
			a.width = int32(lParam & 0xffff)
			a.height = int32((lParam >> 16) & 0xffff)
			if !a.compact {
				var outer RECT
				if ok, _, _ := procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&outer))); ok != 0 {
					a.config.Window.Width = int(outer.Right - outer.Left)
					a.config.Window.Height = int(outer.Bottom - outer.Top)
				}
			}
			procInvalidateRect.Call(hwnd, 0, 0)
		}
		return 0
	case WM_MOVE:
		iconic, _, _ := procIsIconic.Call(hwnd)
		if !a.minimized.Load() && iconic == 0 {
			var r RECT
			if ok, _, _ := procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&r))); ok != 0 && restorableWindowRect(r) {
				a.config.Window.X, a.config.Window.Y = int(r.Left), int(r.Top)
			}
		}
		return 0
	case WM_DISPLAYCHANGE:
		a.displayRefreshHz = monitorRefreshRate(hwnd)
		a.positionFPSOverlay()
		return 0
	case WM_GETMINMAXINFO:
		var mmi MINMAXINFO
		procRtlMoveMemory.Call(uintptr(unsafe.Pointer(&mmi)), lParam, unsafe.Sizeof(mmi))
		if a.compact {
			mmi.PtMinTrackSize.X = a.px(430)
			mmi.PtMinTrackSize.Y = a.px(270)
		} else {
			mmi.PtMinTrackSize.X = a.px(960)
			mmi.PtMinTrackSize.Y = a.px(640)
		}
		procRtlMoveMemory.Call(lParam, uintptr(unsafe.Pointer(&mmi)), unsafe.Sizeof(mmi))
		return 0
	case WM_DPICHANGED:
		a.dpi = int32(wParam & 0xffff)
		var r RECT
		procRtlMoveMemory.Call(uintptr(unsafe.Pointer(&r)), lParam, unsafe.Sizeof(r))
		procSetWindowPos.Call(hwnd, 0, uintptr(r.Left), uintptr(r.Top), uintptr(r.Right-r.Left), uintptr(r.Bottom-r.Top), 0)
		a.back.Destroy()
		a.surgeActivationBack.Destroy()
		a.surgeActivationCached = false
		return 0
	case WM_MOUSEMOVE:
		previous := a.hover
		a.hover = POINT{lowWord(lParam), highWord(lParam)}
		a.hoverValid = true
		if a.graphDragKey != "" || previous != a.hover {
			a.surgeActivationCached = false
			a.graphDragCurrent = a.hover
			a.requestMotion(90 * time.Millisecond)
			procInvalidateRect.Call(hwnd, 0, 0)
		}
		return 0
	case WM_LBUTTONDOWN:
		a.surgeActivationCached = false
		point := POINT{lowWord(lParam), highWord(lParam)}
		if plot, ok := a.graphPlotAt(point); ok {
			a.graphDragKey, a.graphDragStart, a.graphDragCurrent = plot.Key, point, point
			procSetCapture.Call(hwnd)
			return 0
		}
		if hit, ok := a.hitAt(point); ok {
			a.pressedHit, a.pressedValid = hit, true
			a.releasedValid = false
			procSetCapture.Call(hwnd)
			a.requestMotion(100 * time.Millisecond)
			procInvalidateRect.Call(hwnd, 0, 0)
			return 0
		}
	case WM_LBUTTONUP:
		if a.graphDragKey != "" {
			a.finishGraphDrag(POINT{lowWord(lParam), highWord(lParam)})
			procReleaseCapture.Call()
			procInvalidateRect.Call(hwnd, 0, 0)
			return 0
		}
		if a.pressedValid {
			point := POINT{lowWord(lParam), highWord(lParam)}
			hit := a.pressedHit
			a.pressedValid = false
			a.releasedHit, a.releasedValid, a.pressReleaseStart = hit, true, time.Now()
			a.requestMotion(120 * time.Millisecond)
			procReleaseCapture.Call()
			procInvalidateRect.Call(hwnd, 0, 0)
			if pointInRect(point, hit.Rect) {
				a.runAction(hit.Action, hit.Value)
			}
			return 0
		}
		a.handleClick(lowWord(lParam), highWord(lParam))
		return 0
	case WM_CAPTURECHANGED:
		if a.pressedValid {
			a.pressedValid = false
			procInvalidateRect.Call(hwnd, 0, 0)
		}
		return 0
	case WM_LBUTTONDBLCLK:
		if plot, ok := a.graphPlotAt(POINT{lowWord(lParam), highWord(lParam)}); ok {
			delete(a.graphRanges, plot.Key)
			procInvalidateRect.Call(hwnd, 0, 0)
			return 0
		}
	case WM_KEYDOWN:
		if wParam == VK_ESCAPE {
			if a.surgeTuningOpen {
				a.surgeTuningOpen = false
				a.surgeTuningConsentConfirmUntil = time.Time{}
				procInvalidateRect.Call(hwnd, 0, 0)
			} else if a.surgeDependenciesOpen {
				a.surgeDependenciesOpen = false
				procInvalidateRect.Call(hwnd, 0, 0)
			} else if a.surgeSafetyOpen {
				a.surgeSafetyOpen = false
				procInvalidateRect.Call(hwnd, 0, 0)
			} else if a.graphInspectionPaused {
				a.resumeGraphInspection()
				procInvalidateRect.Call(hwnd, 0, 0)
			} else if a.selectedPID != 0 {
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
		a.renderPending.Store(false)
		a.updateDisplay()
		procInvalidateRect.Call(hwnd, 0, 0)
		return 0
	case WM_APP_TRAY:
		event := uint32(lParam & 0xffff)
		if event == WM_LBUTTONUP || event == WM_LBUTTONDBLCLK {
			restoreAndFocusWindow(hwnd)
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
	case WM_APP_REMOTE:
		a.handleRemoteAction()
		return 0
	case WM_APP_FPS_RENDER:
		a.syncFPSOverlay()
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
	lastAutopilotCheck := time.Time{}
	lastSurgeStackCheck := time.Time{}
	lastRemoteCheck := time.Time{}
	lockedGameRunning := false
	frameTimer := time.NewTimer(time.Second / 60)
	defer frameTimer.Stop()
	for {
		now := time.Now()
		if now.Sub(lastGameCheck) >= time.Second {
			active := a.config.Gaming.GameFocus && fullscreenForeground(a.hwnd)
			if active != a.gameDetected.Load() {
				a.gameDetected.Store(active)
				a.engine.SetGameMode(active)
			}
			lastGameCheck = now
			_, lockedGameRunning = a.lockedGameProcess()
			a.updateFrameCapture()
		}
		if now.Sub(lastRemoteCheck) >= 2*time.Second {
			a.ensureRemoteLinkForMonitoring()
			lastRemoteCheck = now
		}
		stackInterval := 5 * time.Second
		if a.surgeIsEnabled() && lockedGameRunning {
			stackInterval = 2 * time.Second
		} else if a.minimized.Load() {
			stackInterval = 15 * time.Second
		}
		if now.Sub(lastSurgeStackCheck) >= stackInterval {
			a.refreshSurgeStack(false)
			lastSurgeStackCheck = now
		}
		autopilotInterval := 2 * time.Second
		if !a.surgeIsEnabled() || !lockedGameRunning {
			autopilotInterval = 5 * time.Second
		}
		if a.minimized.Load() && !lockedGameRunning {
			autopilotInterval = 10 * time.Second
		}
		if now.Sub(lastAutopilotCheck) >= autopilotInterval {
			a.autopilotTick()
			lastAutopilotCheck = now
		}
		fps := a.config.Sampling.GraphFPS
		motionActive := !a.config.Appearance.ReducedMotion && now.UnixNano() < a.motionUntil.Load()
		if motionActive || (!a.config.Appearance.ReducedMotion && now.Sub(a.surgeActivationStart) >= 0 && now.Sub(a.surgeActivationStart) < surgeActivationDuration) {
			fps = a.displayRefreshHz
			if fps < 60 {
				fps = 60
			}
		}
		if a.minimized.Load() || (a.gameDetected.Load() && !a.compact) {
			fps = 1
		}
		if fps < 1 {
			fps = 30
		}
		frameTimer.Reset(time.Second / time.Duration(fps))
		select {
		case <-frameTimer.C:
			if a.hwnd != 0 && a.renderPending.CompareAndSwap(false, true) {
				posted, _, _ := procPostMessageW.Call(a.hwnd, WM_APP_RENDER, 0, 0)
				if posted == 0 {
					a.renderPending.Store(false)
				}
			}
		case <-a.renderStop:
			return
		}
	}
}

func (a *App) requestMotion(duration time.Duration) {
	if a.config.Appearance.ReducedMotion {
		return
	}
	deadline := time.Now().Add(duration).UnixNano()
	for {
		current := a.motionUntil.Load()
		if current >= deadline || a.motionUntil.CompareAndSwap(current, deadline) {
			return
		}
	}
}

func monitorRefreshRate(hwnd uintptr) int {
	hdc, _, _ := procGetDC.Call(hwnd)
	if hdc == 0 {
		return 60
	}
	defer procReleaseDC.Call(hwnd, hdc)
	hz, _, _ := procGetDeviceCaps.Call(hdc, VREFRESH)
	if hz < 30 || hz > 500 {
		return 60
	}
	return int(hz)
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
	if page != a.page {
		a.surgeActivationCached = false
		a.resumeGraphInspection()
		a.surgeSafetyOpen = false
		a.pageTransitionDirection = pageDirection(a.page, page)
		a.pageTransitionStart = time.Now()
		a.requestMotion(190 * time.Millisecond)
	}
	a.page = page
	a.config.Window.LastPage = page
	a.hoverValid = false
	procInvalidateRect.Call(a.hwnd, 0, 0)
	if page == "optimize" {
		a.refreshPowerPlan()
	} else if page == "hardware" {
		a.refreshHardwarePassport()
	} else if page == "alerts" {
		a.refreshEventLens()
	}
}

func pageDirection(from, to string) int32 {
	order := []string{"overview", "cpu", "gpu", "memory", "storage", "network", "processes", "gaming", "insights", "optimize", "history", "alerts", "hardware", "system", "remote", "settings"}
	index := func(value string) int {
		for i, page := range order {
			if page == value {
				return i
			}
		}
		return 0
	}
	if index(to) < index(from) {
		return -1
	}
	return 1
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

func (a *App) applyWindowMaterial() {
	if a.hwnd == 0 {
		return
	}
	backdrop := int32(DWMSBT_MAINWINDOW)
	procDwmSetWindowAttribute.Call(a.hwnd, DWMWA_SYSTEMBACKDROP_TYPE, uintptr(unsafe.Pointer(&backdrop)), unsafe.Sizeof(backdrop))
	caption := palette.BG
	procDwmSetWindowAttribute.Call(a.hwnd, DWMWA_CAPTION_COLOR, uintptr(unsafe.Pointer(&caption)), unsafe.Sizeof(caption))
	border := uint32(0xFFFFFFFE) // DWMWA_COLOR_NONE
	procDwmSetWindowAttribute.Call(a.hwnd, DWMWA_BORDER_COLOR, uintptr(unsafe.Pointer(&border)), unsafe.Sizeof(border))
	opacity := a.config.Appearance.WindowOpacity
	if opacity <= 0 {
		opacity = 99
	}
	style, _, _ := procGetWindowLongPtrW.Call(a.hwnd, windowLongIndex(GWL_EXSTYLE))
	if opacity >= 100 {
		procSetWindowLongPtrW.Call(a.hwnd, windowLongIndex(GWL_EXSTYLE), style&^WS_EX_LAYERED)
	} else {
		procSetWindowLongPtrW.Call(a.hwnd, windowLongIndex(GWL_EXSTYLE), style|WS_EX_LAYERED)
		procSetLayeredWindowAttributes.Call(a.hwnd, 0, uintptr(opacity*255/100), LWA_ALPHA)
	}
	procInvalidateRect.Call(a.hwnd, 0, 0)
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

func (a *App) hitAt(point POINT) (HitRegion, bool) {
	for i := len(a.hits) - 1; i >= 0; i-- {
		if pointInRect(point, a.hits[i].Rect) {
			return a.hits[i], true
		}
	}
	return HitRegion{}, false
}

func pointInRect(point POINT, r RECT) bool {
	return point.X >= r.Left && point.X < r.Right && point.Y >= r.Top && point.Y < r.Bottom
}

func (a *App) beginGraphInspection(selection GraphSampleSelection) {
	if selection.Key == "" {
		return
	}
	if !a.graphInspectionPaused {
		a.graphInspectionPaused = true
		a.graphInspectionAt = time.Now()
		a.graphInspectionHistory = append(a.graphInspectionHistory[:0], a.snapshot.History...)
		a.graphInspectionProcessHistory = append(a.graphInspectionProcessHistory[:0], a.snapshot.ProcessHistory...)
		a.graphInspectionProcesses = append(a.graphInspectionProcesses[:0], a.snapshot.Processes...)
	}
	a.graphPinned = selection
}

func (a *App) resumeGraphInspection() {
	a.graphInspectionPaused = false
	a.graphInspectionAt = time.Time{}
	a.graphPinned = GraphSampleSelection{}
	a.graphInspectionHistory = a.graphInspectionHistory[:0]
	a.graphInspectionProcessHistory = a.graphInspectionProcessHistory[:0]
	a.graphInspectionProcesses = a.graphInspectionProcesses[:0]
}

func (a *App) graphDisplayHistory() []HistorySample {
	if a.graphInspectionPaused {
		return a.graphInspectionHistory
	}
	return a.snapshot.History
}

func (a *App) graphDisplayProcessHistory() []ProcessHistorySample {
	if a.graphInspectionPaused {
		return a.graphInspectionProcessHistory
	}
	return a.snapshot.ProcessHistory
}

func (a *App) graphDisplayProcesses() []ProcessMetric {
	if a.graphInspectionPaused {
		return a.graphInspectionProcesses
	}
	return a.snapshot.Processes
}

func (a *App) graphDisplayNow() time.Time {
	if a.graphInspectionPaused && !a.graphInspectionAt.IsZero() {
		return a.graphInspectionAt
	}
	return time.Now()
}

func (a *App) graphPlotAt(point POINT) (GraphPlotRegion, bool) {
	for i := len(a.graphPlots) - 1; i >= 0; i-- {
		plot := a.graphPlots[i]
		if point.X >= plot.Rect.Left && point.X < plot.Rect.Right && point.Y >= plot.Rect.Top && point.Y < plot.Rect.Bottom {
			return plot, true
		}
	}
	return GraphPlotRegion{}, false
}

func (a *App) finishGraphDrag(point POINT) {
	key := a.graphDragKey
	startPoint := a.graphDragStart
	a.graphDragKey = ""
	var plot GraphPlotRegion
	found := false
	for _, candidate := range a.graphPlots {
		if candidate.Key == key {
			plot, found = candidate, true
			break
		}
	}
	if !found || plot.Rect.Right <= plot.Rect.Left {
		return
	}
	x1, x2 := startPoint.X, point.X
	if x1 > x2 {
		x1, x2 = x2, x1
	}
	if x1 < plot.Rect.Left {
		x1 = plot.Rect.Left
	}
	if x2 > plot.Rect.Right {
		x2 = plot.Rect.Right
	}
	if x2-x1 < a.px(18) {
		selection := GraphSampleSelection{}
		if a.graphHover.Key == key {
			selection = a.graphHover
		} else {
			bestDistance := math.Inf(1)
			for _, line := range a.graphLines {
				if line.Key != key {
					continue
				}
				index, distance, ok := nearestPolylineSample(line.Points, point)
				if !ok || index >= len(line.Times) || index >= len(line.Values) || distance > float64(a.px(12)) || distance >= bestDistance {
					continue
				}
				bestDistance = distance
				selection = GraphSampleSelection{Key: key, Series: line.Series, At: line.Times[index], Point: line.Points[index], Value: line.Values[index], Color: line.Color, Process: line.Process}
			}
		}
		a.beginGraphInspection(selection)
		return
	}
	if !a.graphInspectionPaused {
		a.graphPinned = GraphSampleSelection{}
	}
	span := plot.End.Sub(plot.Start)
	width := float64(plot.Rect.Right - plot.Rect.Left)
	selectedStart := plot.Start.Add(time.Duration(float64(span) * float64(x1-plot.Rect.Left) / width))
	selectedEnd := plot.Start.Add(time.Duration(float64(span) * float64(x2-plot.Rect.Left) / width))
	if selectedEnd.Sub(selectedStart) >= time.Second {
		a.graphRanges[key] = GraphRange{Start: selectedStart, End: selectedEnd}
	}
}

func (a *App) runAction(action string, value int) {
	if strings.HasPrefix(action, "nav:") {
		a.navigate(strings.TrimPrefix(action, "nav:"))
		return
	}
	if strings.HasPrefix(action, "graph-reset:") {
		delete(a.graphRanges, strings.TrimPrefix(action, "graph-reset:"))
		return
	}
	if action == "graph-pin-close" || action == "graph-resume" {
		a.resumeGraphInspection()
		return
	}
	if strings.HasPrefix(action, "breakdown:") {
		key := strings.TrimPrefix(action, "breakdown:")
		a.componentBreakdown[key] = !a.componentBreakdown[key]
		return
	}
	if strings.HasPrefix(action, "breakdown-set:") {
		key := strings.TrimPrefix(action, "breakdown-set:")
		a.componentBreakdown[key] = value != 0
		return
	}
	if strings.HasPrefix(action, "event-open:") {
		a.selectedEventRawID = strings.TrimPrefix(action, "event-open:")
		return
	}
	switch action {
	case "startup-enable":
		if err := setStartupEntry(true); err != nil {
			a.startupBannerError = err.Error()
			a.logger.Error("startup", "enable at sign-in", err)
		} else {
			a.startupEnabled = true
			a.startupBannerError = ""
		}
		a.requestMotion(420 * time.Millisecond)
	case "toggle-startup":
		enabled := !a.startupEnabled
		if err := setStartupEntry(enabled); err != nil {
			a.startupBannerError = err.Error()
			a.logger.Error("startup", "change sign-in setting", err)
		} else {
			a.startupEnabled = enabled
			a.startupBannerError = ""
		}
		a.requestMotion(420 * time.Millisecond)
	case "toggle-resources":
		a.config.Window.ResourcesCollapsed = !a.config.Window.ResourcesCollapsed
		a.saveAndRestart(false)
	case "surge-safety-open":
		a.surgeDependenciesOpen, a.surgeTuningOpen = false, false
		a.surgeSafetyOpen = true
	case "surge-safety-dismiss":
		a.surgeSafetyOpen = false
		a.surgeTuningResumeAfterSafety = false
	case "surge-safety-understood":
		a.config.Tuning.SafetyNoticeSeen = true
		a.surgeSafetyOpen = false
		a.saveAndRestart(false)
		if a.surgeTuningResumeAfterSafety {
			a.surgeTuningResumeAfterSafety = false
			a.surgeTuningOpen = true
			a.runAction("surge-tuning-enable-aggressive", 0)
			return
		}
	case "surge-dependencies-open":
		a.refreshSurgeStack(true)
		a.surgeSafetyOpen, a.surgeTuningOpen = false, false
		a.surgeDependenciesOpen = true
	case "surge-dependencies-dismiss":
		a.surgeDependenciesOpen = false
	case "surge-dependency-refresh":
		a.refreshSurgeStack(true)
	case "surge-dependency-install-presentmon":
		a.installPresentMonDependency()
	case "surge-dependency-source":
		stack := a.surgeStack.Snapshot()
		if value >= 0 && value < len(stack.Dependencies) && stack.Dependencies[value].OfficialURL != "" {
			shellOpen(stack.Dependencies[value].OfficialURL)
		}
	case "surge-tuning-open":
		a.estimateTuningLabCapability()
		a.surgeSafetyOpen, a.surgeDependenciesOpen = false, false
		a.surgeTuningOpen = true
	case "surge-tuning-dismiss":
		a.surgeTuningOpen = false
		a.surgeTuningConsentConfirmUntil = time.Time{}
	case "surge-tuning-consent":
		if a.config.Tuning.LabDisclaimerVersion != tuningLabDisclaimerVersion && time.Now().After(a.surgeTuningConsentConfirmUntil) {
			a.surgeTuningConsentConfirmUntil = time.Now().Add(8 * time.Second)
			a.requestMotion(180 * time.Millisecond)
			break
		}
		a.config.Tuning.LabDisclaimerVersion = tuningLabDisclaimerVersion
		a.config.Tuning.LabDisclaimerAt = time.Now().Unix()
		a.surgeTuningConsentConfirmUntil = time.Time{}
		a.engine.AddUserEvent("tuning-lab", "Hardware tuning risk acknowledged", "Tuning Lab remains off until it is separately armed. Acceptance version and time were stored locally.", 2)
		a.saveAndRestart(false)
	case "surge-tuning-profile":
		profiles := []string{"conservative", "balanced", "enthusiast"}
		if value >= 0 && value < len(profiles) && a.config.Tuning.LabProfile != profiles[value] {
			if a.tuningLabSnapshot().Running || a.tuningLabSnapshot().Applied {
				break
			}
			a.config.Tuning.LabProfile = profiles[value]
			a.saveAndRestart(false)
		}
	case "surge-tuning-auto":
		if a.config.Tuning.LabDisclaimerVersion != tuningLabDisclaimerVersion {
			a.surgeTuningConsentConfirmUntil = time.Now().Add(8 * time.Second)
			break
		}
		arming := !a.config.Tuning.LabAutoWithSurge
		if arming {
			a.refreshTuningLabCapability()
			if !a.tuningLabSnapshot().Capability.GPU.Direct {
				a.tuningLab.mu.Lock()
				a.tuningLab.Status = "Auto with Surge cannot arm until a direct, manufacturer-supported tuning control is available on this PC."
				a.tuningLab.mu.Unlock()
				break
			}
		}
		a.config.Tuning.LabAutoWithSurge = arming
		if !a.config.Tuning.LabAutoWithSurge {
			go a.stopHardwareTuning("Automatic hardware tuning was disarmed")
		}
		a.saveAndRestart(false)
	case "surge-tuning-start":
		if !a.tuningLabSnapshot().Estimating {
			a.startHardwareTuning()
		}
	case "surge-tuning-control-test":
		if !a.tuningLabSnapshot().Estimating {
			a.startHardwareControlValidation()
		}
	case "surge-tuning-enable-aggressive":
		if !a.config.Tuning.SafetyNoticeSeen {
			a.surgeTuningOpen = false
			a.surgeTuningResumeAfterSafety = true
			a.surgeSafetyOpen = true
			break
		}
		a.refreshSurgeStack(true)
		if !a.surgeStack.Snapshot().CoreReady {
			a.surgeTuningOpen = false
			a.surgeDependenciesOpen = true
			a.recordAutopilot("dependency-gate", "blocked", "Surge cannot start from Tuning Lab until its core provider stack is ready", "One or more required providers are unavailable", "System unchanged", "Open Performance Stack", "No rollback required")
			break
		}
		if a.config.Tuning.Mode != "performance" {
			go a.stopHardwareTuning("Surge changed to Aggressive")
			a.restoreAutopilotSession("Surge changed to Aggressive")
			a.config.Tuning.Mode = "performance"
		}
		if !a.surgeIsEnabled() {
			a.setSurgeEnabled(true)
			if !a.config.Appearance.ReducedMotion {
				a.surgeActivationStart = time.Now()
				a.requestMotion(surgeActivationDuration + 30*time.Millisecond)
			}
		}
		a.tuningLab.mu.Lock()
		a.tuningLab.Status = "Surge is on in Aggressive mode. Tuning Lab will still change hardware only after its remaining capability and game checks pass."
		a.tuningLab.mu.Unlock()
		a.engine.AddUserEvent("surge", "Surge enabled from Tuning Lab", "Aggressive policy selected; all normal proof, snapshot and rollback gates remain active.", 1)
		a.persistCriticalConfig("persist Surge enabled from Tuning Lab")
	case "surge-tuning-pause-estimate":
		if a.surgeIsEnabled() {
			a.setSurgeEnabled(false)
			go a.stopHardwareTuning("Surge paused for a stock hardware estimate")
			a.restoreAutopilotSession("Surge paused for a stock hardware estimate")
			a.setSurgeEnabled(false)
			a.persistCriticalConfig("persist Surge paused for stock estimate")
		}
		a.estimateTuningLabCapability()
	case "surge-tuning-stop":
		go a.stopHardwareTuning("Stopped by the user")
	case "surge-tuning-elevate":
		if err := a.restartElevatedTuningLab(); err != nil {
			a.tuningLab.mu.Lock()
			a.tuningLab.Status = "Elevation was not started: " + err.Error()
			a.tuningLab.mu.Unlock()
		}
	case "surge-tuning-source":
		capability := a.tuningLabSnapshot().Capability
		domains := []TuningDomainCapability{capability.GPU}
		if value >= 0 && value < len(domains) {
			domain := domains[value]
			if domain.InstalledPath != "" {
				shellOpen(domain.InstalledPath)
			} else if domain.OfficialURL != "" {
				shellOpen(domain.OfficialURL)
			}
		}
	case "toggle-process-map":
		a.processMap = !a.processMap
	case "process-detail":
		a.selectedPID = uint32(value)
		a.selectedPath = queryProcessPath(a.selectedPID)
		a.processPriorityOriginal = queryProcessPriority(a.selectedPID)
		a.processPriorityChoice = a.processPriorityOriginal
		if a.processDetailMetric == "" {
			a.processDetailMetric = "cpu"
		}
	case "close-process-detail":
		a.selectedPID, a.selectedPath = 0, ""
		a.processPriorityChoice, a.processPriorityOriginal = 0, 0
	case "process-metric":
		if value == 1 {
			a.processDetailMetric = "memory"
		} else if value == 2 {
			a.processDetailMetric = "disk"
		} else {
			a.processDetailMetric = "cpu"
		}
	case "process-priority":
		if value == 0 {
			a.processPriorityChoice = priorityBelowNormal
		} else if value == 2 {
			a.processPriorityChoice = priorityAboveNormal
		} else {
			a.processPriorityChoice = priorityNormal
		}
	case "process-priority-apply":
		if err := setProcessPriority(a.selectedPID, a.processPriorityChoice); err != nil {
			a.engine.AddUserEvent("process", "Priority change refused", err.Error(), 1)
		} else {
			a.processPriorityOriginal = a.processPriorityChoice
			a.engine.AddUserEvent("process", "Scheduling preference changed", fmt.Sprintf("PID %d now uses %s priority. This is reversible and does not guarantee more performance.", a.selectedPID, priorityLabel(a.processPriorityChoice)), 1)
		}
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
	case "toggle-technical":
		a.config.Appearance.TechnicalMode = !a.config.Appearance.TechnicalMode
		a.saveAndRestart(false)
	case "toggle-adaptive":
		a.config.Sampling.Adaptive = !a.config.Sampling.Adaptive
		a.saveAndRestart(true)
	case "toggle-gamefocus":
		a.config.Gaming.GameFocus = !a.config.Gaming.GameFocus
		a.saveAndRestart(false)
	case "toggle-anticheat-guard":
		if a.antiCheatGuardIsEnabled() {
			if time.Now().After(a.surgeGuardConfirmUntil) {
				a.surgeGuardConfirmUntil = time.Now().Add(6 * time.Second)
				a.requestMotion(150 * time.Millisecond)
				procInvalidateRect.Call(a.hwnd, 0, 0)
				return
			}
			a.setAntiCheatGuardEnabled(false)
			a.surgeGuardConfirmUntil = time.Time{}
			a.engine.AddUserEvent("surge", "Anti-cheat guardrails disabled", "Kerneon may use its normal external Windows controls in detected anti-cheat sessions. Injection, hooks, game-memory access and anti-cheat process manipulation remain prohibited. Compatibility and account safety cannot be guaranteed.", 2)
		} else {
			a.setAntiCheatGuardEnabled(true)
			a.surgeGuardConfirmUntil = time.Time{}
			go a.stopHardwareTuning("Anti-cheat guardrails were enabled")
			a.restoreAutopilotSession("Anti-cheat guardrails restored")
			a.setAntiCheatGuardEnabled(true)
			a.config.Gaming.FPSHUD = false
			a.hideFPSOverlay()
			a.engine.AddUserEvent("surge", "Anti-cheat guardrails enabled", "Detected anti-cheat sessions now use Kerneon's strict external-only compatibility lane.", 0)
		}
		a.persistCriticalConfig("persist anti-cheat guardrail state")
	case "game-select":
		a.gameCandidatePID = uint32(value)
	case "game-lock":
		if process, ok := a.processByPID(a.gameCandidatePID); ok {
			a.restoreAutopilotSession("Game selection changed")
			a.config.Gaming.LockedProcessName = process.Name
			a.saveAndRestart(false)
			a.recordAutopilot("game-lock", "recorded", "Game process selected", "Locked to "+process.Name, "Automatic selection", "Kerneon will follow this executable across restarts.", "Clear the lock")
		}
	case "game-unlock":
		a.restoreAutopilotSession("Game lock removed")
		a.config.Gaming.LockedProcessName = ""
		a.config.Gaming.FPSHUD = false
		a.hideFPSOverlay()
		a.saveAndRestart(false)
	case "toggle-fps-hud":
		if a.config.Gaming.LockedProcessName != "" {
			if antiCheat := detectAntiCheat(a.snapshot.Processes); antiCheat.Detected && a.antiCheatGuardIsEnabled() {
				a.config.Gaming.FPSHUD = false
				a.hideFPSOverlay()
				a.engine.AddUserEvent("gaming", "Frame HUD kept off for anti-cheat compatibility", antiCheat.Provider+": "+antiCheat.Evidence, 1)
				break
			}
			a.config.Gaming.FPSHUD = !a.config.Gaming.FPSHUD
			if !a.config.Gaming.FPSHUD {
				a.hideFPSOverlay()
			}
			a.saveAndRestart(false)
			procPostMessageW.Call(a.hwnd, WM_APP_FPS_RENDER, 0, 0)
		}
	case "toggle-autopilot":
		enabling := !a.surgeIsEnabled()
		if enabling {
			a.refreshSurgeStack(true)
			if !a.surgeStack.Snapshot().CoreReady {
				a.surgeDependenciesOpen = true
				a.recordAutopilot("dependency-gate", "blocked", "Surge cannot prove and control this hardware with the required provider stack", "One or more required providers are unavailable", "System unchanged", "Open Performance Stack", "No rollback required")
				break
			}
		}
		a.setSurgeEnabled(enabling)
		if enabling && !a.config.Appearance.ReducedMotion {
			a.surgeActivationStart = time.Now()
			a.requestMotion(surgeActivationDuration + 30*time.Millisecond)
		}
		if !enabling {
			go a.stopHardwareTuning("Surge switched off")
			a.restoreAutopilotSession("Surge switched off")
			a.setSurgeEnabled(false)
		}
		a.persistCriticalConfig("persist Surge toggle")
	case "surge-mode":
		desired := "guarded"
		if value == 1 {
			desired = "performance"
			if !a.config.Tuning.SafetyNoticeSeen {
				a.surgeSafetyOpen = true
				procInvalidateRect.Call(a.hwnd, 0, 0)
				return
			}
			if a.config.Tuning.Mode != desired && time.Now().After(a.surgeModeConfirmUntil) {
				a.surgeModeConfirmUntil = time.Now().Add(6 * time.Second)
				a.requestMotion(150 * time.Millisecond)
				procInvalidateRect.Call(a.hwnd, 0, 0)
				return
			}
		}
		if a.config.Tuning.Mode != desired {
			go a.stopHardwareTuning("Surge policy changed")
			a.restoreAutopilotSession("Surge policy changed")
			a.config.Tuning.Mode = desired
			a.surgeModeConfirmUntil = time.Time{}
			a.saveAndRestart(false)
		}
	case "open-remediation-log":
		shellOpen(a.remediationDir())
	case "remote-toggle":
		a.config.Remote.Enabled = !a.config.Remote.Enabled
		if a.config.Remote.Enabled {
			a.remotePairCodeRevealed.Store(false)
			a.startRemoteLink()
		} else {
			a.stopRemoteLink()
		}
		a.saveAndRestart(false)
	case "remote-regenerate":
		a.regenerateRemotePairing()
		a.remotePairCodeRevealed.Store(false)
		a.recordRemoteAudit(RemoteCommand{Action: "new-pairing", RemoteIP: "local"}, "credentials rotated; existing devices and control revoked")
	case "remote-revoke-sessions":
		a.remote.mu.Lock()
		a.remote.Sessions = make(map[string]time.Time)
		a.remote.ControlUntil, a.remote.ControlUntilRevoked = time.Time{}, false
		a.remote.Pending = nil
		a.remote.mu.Unlock()
		a.regenerateRemotePairing()
		a.remotePairCodeRevealed.Store(false)
		a.recordRemoteAudit(RemoteCommand{Action: "revoke-devices", RemoteIP: "local"}, "all device sessions and control revoked")
	case "remote-pair-code-toggle":
		a.remotePairCodeRevealed.Store(!a.remotePairCodeRevealed.Load())
	case "remote-control-mode":
		a.remote.mu.Lock()
		switch value {
		case 1:
			a.remote.ControlUntil = time.Now().Add(15 * time.Minute)
			a.remote.ControlUntilRevoked = false
		case 2:
			a.remote.ControlUntil = time.Time{}
			a.remote.ControlUntilRevoked = true
		default:
			a.remote.ControlUntil = time.Time{}
			a.remote.ControlUntilRevoked = false
		}
		a.remote.mu.Unlock()
		labels := []string{"revoked", "granted for 15 minutes", "granted until revoked"}
		if value < 0 || value >= len(labels) {
			value = 0
		}
		a.recordRemoteAudit(RemoteCommand{Action: "control-access", Target: labels[value], RemoteIP: "local"}, labels[value])
	case "remote-copy-url":
		copyText(a.remoteSnapshot().URL)
	case "screen-view-mode":
		a.setScreenGrant("view", value)
	case "screen-input-mode":
		a.setScreenGrant("input", value)
	case "screen-revoke":
		a.setScreenGrant("view", 0)
	case "hardware-refresh":
		a.refreshHardwarePassport()
	case "hardware-pin":
		passport := a.hardwareSnapshot()
		if passport.Fingerprint != "" {
			a.config.Trust.PinnedFingerprint = passport.Fingerprint
			a.saveAndRestart(false)
			a.refreshHardwarePassport()
		}
	case "event-refresh":
		a.refreshEventLens()
	case "event-filter":
		a.eventLens.mu.Lock()
		a.eventLens.Filter = value
		a.eventLens.mu.Unlock()
	case "event-close":
		a.selectedEventRawID = ""
	case "ai-connect":
		key := strings.TrimSpace(readClipboardText())
		if len(key) < 20 || !strings.HasPrefix(key, "sk-") {
			a.ai.mu.Lock()
			a.ai.Error = "Clipboard does not contain a recognizable OpenAI API key."
			a.ai.mu.Unlock()
		} else if err := saveKerneonCredential(key); err != nil {
			a.ai.mu.Lock()
			a.ai.Error = "Could not store the key in Windows Credential Manager: " + err.Error()
			a.ai.mu.Unlock()
		} else {
			a.ai.mu.Lock()
			a.ai.Connected = true
			a.ai.mu.Unlock()
			a.generateAIInsights()
		}
	case "ai-disconnect":
		if err := deleteKerneonCredential(); err != nil {
			a.logger.Error("ai-insights", "remove credential", err)
		}
		a.ai.mu.Lock()
		a.ai.Connected = strings.TrimSpace(os.Getenv("OPENAI_API_KEY")) != ""
		a.ai.Insights, a.ai.Error, a.ai.Updated = nil, "", time.Time{}
		a.ai.mu.Unlock()
	case "ai-generate":
		a.generateAIInsights()
	case "opt-baseline":
		a.captureOptimizationBaseline()
	case "opt-apply":
		a.applyPerformancePlan()
	case "opt-compare":
		a.compareOptimizationRun()
	case "opt-rollback":
		a.rollbackOptimization()
	case "opt-keep":
		a.keepOptimization()
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
	case "opacity":
		vals := []int{98, 99, 100}
		a.config.Appearance.WindowOpacity = cycleInt(vals, a.config.Appearance.WindowOpacity, value)
		a.applyWindowMaterial()
		a.saveAndRestart(false)
	case "units":
		vals := []core.UnitMode{core.UnitAuto, core.UnitBits, core.UnitBytes}
		a.config.Network.Units = cycleMode(vals, a.config.Network.Units, value)
		a.downFormatter.SetMode(a.config.Network.Units)
		a.upFormatter.SetMode(a.config.Network.Units)
		a.saveAndRestart(false)
	case "ping":
		vals := []int{0, 1, 2, 5, 10}
		a.config.Network.PingSeconds = cycleInt(vals, a.config.Network.PingSeconds, value)
		a.saveAndRestart(true)
	case "defaults":
		go a.stopHardwareTuning("Settings restored to defaults")
		a.restoreAutopilotSession("Settings restored to defaults")
		a.config = core.DefaultConfig()
		a.setSurgeEnabled(a.config.Tuning.Autopilot)
		a.setAntiCheatGuardEnabled(a.config.Tuning.AntiCheatGuard)
		a.downFormatter.SetMode(a.config.Network.Units)
		a.upFormatter.SetMode(a.config.Network.Units)
		a.applyTopMost()
		a.applyWindowMaterial()
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
	a.store.SaveAsync(a.configSnapshot())
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
		procSetWindowPos.Call(a.hwnd, HWND_TOPMOST, 0, 0, uintptr(a.px(430)), uintptr(a.px(270)), SWP_NOMOVE|SWP_NOACTIVATE)
	} else {
		procSetWindowPos.Call(a.hwnd, 0, 0, 0, uintptr(a.px(int32(a.config.Window.Width))), uintptr(a.px(int32(a.config.Window.Height))), SWP_NOMOVE|SWP_NOACTIVATE)
		a.applyTopMost()
	}
	a.back.Destroy()
	a.surgeActivationBack.Destroy()
	a.surgeActivationCached = false
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

func (a *App) updateTrayIndicator() {
	if a.icon == 0 || a.hwnd == 0 {
		return
	}
	view, input, _ := a.screenPermissionState()
	tip := "Kerneon — click to restore · right-click to exit"
	if view && input {
		tip = "Kerneon — SCREEN + INPUT granted · open to revoke"
	} else if view {
		tip = "Kerneon — SCREEN VIEW granted · open to revoke"
	}
	var n notifyIconData
	n.Size = uint32(unsafe.Sizeof(n))
	n.Hwnd = a.hwnd
	n.ID = 1
	n.Flags = NIF_TIP
	copy(n.Tip[:], syscall.StringToUTF16(tip))
	procShellNotifyIconW.Call(NIM_MODIFY, uintptr(unsafe.Pointer(&n)))
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
	a.stopHardwareTuning("Kerneon is closing")
	a.restoreAutopilotSession("Kerneon is closing")
	if a.optimizerSnapshot().Applied {
		a.rollbackOptimization()
	}
	a.engine.Stop()
	a.closeSurgeProviders()
	a.frames.Stop()
	a.destroyFPSOverlay()
	a.stopRemoteLink()
	a.removeTrayIcon()
	a.back.Destroy()
	a.surgeActivationBack.Destroy()
	freeGDICache()
	shutdownGDIPlus()
	a.config.Window.LastPage = a.page
	if err := a.store.Save(a.configSnapshot()); err != nil {
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

func readClipboardText() string {
	if r, _, _ := procOpenClipboard.Call(0); r == 0 {
		return ""
	}
	defer procCloseClipboard.Call()
	h, _, _ := procGetClipboardData.Call(13)
	if h == 0 {
		return ""
	}
	p, _, _ := procGlobalLock.Call(h)
	if p == 0 {
		return ""
	}
	defer procGlobalUnlock.Call(h)
	size, _, _ := procGlobalSize.Call(h)
	if size < 2 {
		return ""
	}
	units := make([]uint16, int(size/2))
	procRtlMoveMemory.Call(uintptr(unsafe.Pointer(&units[0])), p, size)
	end := 0
	for end < len(units) && units[end] != 0 {
		end++
	}
	return syscall.UTF16ToString(units[:end])
}
