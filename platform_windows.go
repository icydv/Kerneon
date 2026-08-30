//go:build windows

package main

import (
	"syscall"
	"unsafe"
)

const (
	WM_DESTROY        = 0x0002
	WM_MOVE           = 0x0003
	WM_SIZE           = 0x0005
	WM_PAINT          = 0x000F
	WM_CLOSE          = 0x0010
	WM_ERASEBKGND     = 0x0014
	WM_GETMINMAXINFO  = 0x0024
	WM_NCHITTEST      = 0x0084
	WM_DISPLAYCHANGE  = 0x007E
	WM_SETCURSOR      = 0x0020
	WM_KEYDOWN        = 0x0100
	WM_CHAR           = 0x0102
	WM_MOUSEMOVE      = 0x0200
	WM_LBUTTONDOWN    = 0x0201
	WM_LBUTTONUP      = 0x0202
	WM_LBUTTONDBLCLK  = 0x0203
	WM_RBUTTONUP      = 0x0205
	WM_MOUSEWHEEL     = 0x020A
	WM_CAPTURECHANGED = 0x0215
	WM_DPICHANGED     = 0x02E0
	WM_POWERBROADCAST = 0x0218
	WM_APP            = 0x8000
	WM_APP_RENDER     = WM_APP + 1
	WM_APP_TRAY       = WM_APP + 2
	WM_APP_REMOTE     = WM_APP + 3
	WM_APP_FPS_RENDER = WM_APP + 4

	SIZE_MINIMIZED = 1
	VK_ESCAPE      = 0x1B
	VK_RETURN      = 0x0D
	VK_BACK        = 0x08
	VK_TAB         = 0x09

	WS_OVERLAPPEDWINDOW            = 0x00CF0000
	WS_POPUP                       = 0x80000000
	WS_VISIBLE                     = 0x10000000
	WS_CLIPCHILDREN                = 0x02000000
	CW_USEDEFAULT                  = 0x80000000
	SW_SHOW                        = 5
	SW_HIDE                        = 0
	SW_RESTORE                     = 9
	SW_SHOWNOACTIVATE              = 4
	CS_HREDRAW                     = 0x0002
	CS_VREDRAW                     = 0x0001
	CS_DBLCLKS                     = 0x0008
	IDC_ARROW                      = 32512
	COLOR_WINDOW                   = 5
	DT_LEFT                        = 0x0000
	DT_CENTER                      = 0x0001
	DT_RIGHT                       = 0x0002
	DT_VCENTER                     = 0x0004
	DT_WORDBREAK                   = 0x0010
	DT_SINGLELINE                  = 0x0020
	DT_END_ELLIPSIS                = 0x00008000
	TRANSPARENT                    = 1
	PS_SOLID                       = 0
	NULL_PEN                       = 8
	HOLLOW_BRUSH                   = 5
	SRCCOPY                        = 0x00CC0020
	DWMWA_USE_IMMERSIVE_DARK_MODE  = 20
	DWMWA_WINDOW_CORNER_PREFERENCE = 33
	DWMWA_BORDER_COLOR             = 34
	DWMWA_CAPTION_COLOR            = 35
	DWMWA_SYSTEMBACKDROP_TYPE      = 38
	DWMSBT_MAINWINDOW              = 2
	DWMWCP_ROUND                   = 2
	SWP_NOSIZE                     = 0x0001
	SWP_NOMOVE                     = 0x0002
	SWP_NOZORDER                   = 0x0004
	SWP_NOACTIVATE                 = 0x0010
	SWP_SHOWWINDOW                 = 0x0040
	SWP_NOOWNERZORDER              = 0x0200
	HWND_TOPMOST                   = ^uintptr(0)
	HWND_NOTOPMOST                 = ^uintptr(1)
	PM_REMOVE                      = 0x0001
	PBT_APMRESUMEAUTOMATIC         = 0x0012
	PBT_APMSUSPEND                 = 0x0004
	MOVEFILE_REPLACE_EXISTING      = 0x1
	MOVEFILE_WRITE_THROUGH         = 0x8
	MONITOR_DEFAULTTONEAREST       = 2
	MONITOR_DEFAULTTOPRIMARY       = 1
	WS_EX_LAYERED                  = 0x00080000
	WS_EX_TOPMOST                  = 0x00000008
	WS_EX_TRANSPARENT              = 0x00000020
	WS_EX_TOOLWINDOW               = 0x00000080
	WS_EX_NOACTIVATE               = 0x08000000
	GWL_EXSTYLE                    = -20
	LWA_ALPHA                      = 0x2
	DI_NORMAL                      = 0x0003
	VREFRESH                       = 116
	HTTRANSPARENT                  = -1
)

type POINT struct{ X, Y int32 }
type RECT struct{ Left, Top, Right, Bottom int32 }
type MSG struct {
	Hwnd           uintptr
	Message        uint32
	_              uint32
	WParam, LParam uintptr
	Time           uint32
	Pt             POINT
	LPrivate       uint32
}
type PAINTSTRUCT struct {
	Hdc                  uintptr
	FErase               int32
	RcPaint              RECT
	FRestore, FIncUpdate int32
	RgbReserved          [32]byte
}
type WNDCLASSEX struct {
	CbSize, Style                            uint32
	LpfnWndProc                              uintptr
	CbClsExtra, CbWndExtra                   int32
	HInstance, HIcon, HCursor, HbrBackground uintptr
	LpszMenuName, LpszClassName              *uint16
	HIconSm                                  uintptr
}
type MINMAXINFO struct{ PtReserved, PtMaxSize, PtMaxPosition, PtMinTrackSize, PtMaxTrackSize POINT }
type MONITORINFO struct {
	Size    uint32
	Monitor RECT
	Work    RECT
	Flags   uint32
}
type GUID struct {
	Data1        uint32
	Data2, Data3 uint16
	Data4        [8]byte
}

var (
	user32                            = syscall.NewLazyDLL("user32.dll")
	gdi32                             = syscall.NewLazyDLL("gdi32.dll")
	kernel32                          = syscall.NewLazyDLL("kernel32.dll")
	dwmapi                            = syscall.NewLazyDLL("dwmapi.dll")
	shell32                           = syscall.NewLazyDLL("shell32.dll")
	procRegisterClassExW              = user32.NewProc("RegisterClassExW")
	procCreateWindowExW               = user32.NewProc("CreateWindowExW")
	procFindWindowW                   = user32.NewProc("FindWindowW")
	procDefWindowProcW                = user32.NewProc("DefWindowProcW")
	procShowWindow                    = user32.NewProc("ShowWindow")
	procDestroyWindow                 = user32.NewProc("DestroyWindow")
	procUpdateWindow                  = user32.NewProc("UpdateWindow")
	procGetMessageW                   = user32.NewProc("GetMessageW")
	procTranslateMessage              = user32.NewProc("TranslateMessage")
	procDispatchMessageW              = user32.NewProc("DispatchMessageW")
	procPostQuitMessage               = user32.NewProc("PostQuitMessage")
	procBeginPaint                    = user32.NewProc("BeginPaint")
	procEndPaint                      = user32.NewProc("EndPaint")
	procGetClientRect                 = user32.NewProc("GetClientRect")
	procGetDC                         = user32.NewProc("GetDC")
	procReleaseDC                     = user32.NewProc("ReleaseDC")
	procGetWindowRect                 = user32.NewProc("GetWindowRect")
	procIsIconic                      = user32.NewProc("IsIconic")
	procGetForegroundWindow           = user32.NewProc("GetForegroundWindow")
	procGetWindowThreadProcessId      = user32.NewProc("GetWindowThreadProcessId")
	procMonitorFromWindow             = user32.NewProc("MonitorFromWindow")
	procGetMonitorInfoW               = user32.NewProc("GetMonitorInfoW")
	procInvalidateRect                = user32.NewProc("InvalidateRect")
	procLoadCursorW                   = user32.NewProc("LoadCursorW")
	procLoadIconW                     = user32.NewProc("LoadIconW")
	procPostMessageW                  = user32.NewProc("PostMessageW")
	procSetWindowPos                  = user32.NewProc("SetWindowPos")
	procSetForegroundWindow           = user32.NewProc("SetForegroundWindow")
	procGetWindowLongPtrW             = user32.NewProc("GetWindowLongPtrW")
	procSetWindowLongPtrW             = user32.NewProc("SetWindowLongPtrW")
	procSetLayeredWindowAttributes    = user32.NewProc("SetLayeredWindowAttributes")
	procDrawIconEx                    = user32.NewProc("DrawIconEx")
	procIsWindowVisible               = user32.NewProc("IsWindowVisible")
	procGetDpiForWindow               = user32.NewProc("GetDpiForWindow")
	procSetProcessDpiAwarenessContext = user32.NewProc("SetProcessDpiAwarenessContext")
	procSetCapture                    = user32.NewProc("SetCapture")
	procReleaseCapture                = user32.NewProc("ReleaseCapture")
	procOpenClipboard                 = user32.NewProc("OpenClipboard")
	procGetClipboardData              = user32.NewProc("GetClipboardData")
	procEmptyClipboard                = user32.NewProc("EmptyClipboard")
	procSetClipboardData              = user32.NewProc("SetClipboardData")
	procCloseClipboard                = user32.NewProc("CloseClipboard")
	procGlobalAlloc                   = kernel32.NewProc("GlobalAlloc")
	procGlobalLock                    = kernel32.NewProc("GlobalLock")
	procGlobalUnlock                  = kernel32.NewProc("GlobalUnlock")
	procGlobalSize                    = kernel32.NewProc("GlobalSize")
	procCreateMutexW                  = kernel32.NewProc("CreateMutexW")
	procRtlMoveMemory                 = ntdll.NewProc("RtlMoveMemory")
	procMoveFileExW                   = kernel32.NewProc("MoveFileExW")
	procGetModuleHandleW              = kernel32.NewProc("GetModuleHandleW")
	procCreateCompatibleDC            = gdi32.NewProc("CreateCompatibleDC")
	procCreateCompatibleBitmap        = gdi32.NewProc("CreateCompatibleBitmap")
	procSelectObject                  = gdi32.NewProc("SelectObject")
	procDeleteObject                  = gdi32.NewProc("DeleteObject")
	procDeleteDC                      = gdi32.NewProc("DeleteDC")
	procBitBlt                        = gdi32.NewProc("BitBlt")
	procCreateSolidBrush              = gdi32.NewProc("CreateSolidBrush")
	procFillRect                      = user32.NewProc("FillRect")
	procCreatePen                     = gdi32.NewProc("CreatePen")
	procRoundRect                     = gdi32.NewProc("RoundRect")
	procRectangle                     = gdi32.NewProc("Rectangle")
	procMoveToEx                      = gdi32.NewProc("MoveToEx")
	procLineTo                        = gdi32.NewProc("LineTo")
	procPolyline                      = gdi32.NewProc("Polyline")
	procPolygon                       = gdi32.NewProc("Polygon")
	procSetTextColor                  = gdi32.NewProc("SetTextColor")
	procSetBkMode                     = gdi32.NewProc("SetBkMode")
	procGetDeviceCaps                 = gdi32.NewProc("GetDeviceCaps")
	procDrawTextW                     = user32.NewProc("DrawTextW")
	procCreateFontW                   = gdi32.NewProc("CreateFontW")
	procGetStockObject                = gdi32.NewProc("GetStockObject")
	procEllipse                       = gdi32.NewProc("Ellipse")
	procDwmSetWindowAttribute         = dwmapi.NewProc("DwmSetWindowAttribute")
	procShellExecuteW                 = shell32.NewProc("ShellExecuteW")
	procShellNotifyIconW              = shell32.NewProc("Shell_NotifyIconW")
)

func utf16Ptr(s string) *uint16 { p, _ := syscall.UTF16PtrFromString(s); return p }
func lowWord(v uintptr) int32   { return int32(int16(v & 0xffff)) }
func highWord(v uintptr) int32  { return int32(int16((v >> 16) & 0xffff)) }
func windowLongIndex(v int32) uintptr {
	return uintptr(int64(v))
}

func replaceFile(source, target string) error {
	r, _, err := procMoveFileExW.Call(uintptr(unsafe.Pointer(utf16Ptr(source))), uintptr(unsafe.Pointer(utf16Ptr(target))), MOVEFILE_REPLACE_EXISTING|MOVEFILE_WRITE_THROUGH)
	if r == 0 {
		return err
	}
	return nil
}
