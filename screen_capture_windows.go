//go:build windows

package main

import (
	"bytes"
	"errors"
	"image"
	"image/jpeg"
	"math"
	"syscall"
	"unsafe"
)

const (
	dibRGBColors       = 0
	biRGB              = 0
	monitorInfoPrimary = 1
	stretchHalftone    = 4
	captureBLT         = 0x40000000
	cursorShowing      = 1
)

type bitmapInfoHeader struct {
	Size          uint32
	Width         int32
	Height        int32
	Planes        uint16
	BitCount      uint16
	Compression   uint32
	SizeImage     uint32
	XPelsPerMeter int32
	YPelsPerMeter int32
	ClrUsed       uint32
	ClrImportant  uint32
}

type bitmapInfo struct {
	Header bitmapInfoHeader
	Colors [1]uint32
}

type monitorInfoEx struct {
	Size    uint32
	Monitor RECT
	Work    RECT
	Flags   uint32
	Device  [32]uint16
}

type cursorInfo struct {
	Size   uint32
	Flags  uint32
	Cursor uintptr
	Screen POINT
}

type ScreenMonitor struct {
	Index   int    `json:"index"`
	Name    string `json:"name"`
	Width   int32  `json:"width"`
	Height  int32  `json:"height"`
	Primary bool   `json:"primary"`
	Bounds  RECT   `json:"-"`
}

var (
	procEnumDisplayMonitors = user32.NewProc("EnumDisplayMonitors")
	procCreateDIBSection    = gdi32.NewProc("CreateDIBSection")
	procSetStretchBltMode   = gdi32.NewProc("SetStretchBltMode")
	procStretchBlt          = gdi32.NewProc("StretchBlt")
	procGetCursorInfo       = user32.NewProc("GetCursorInfo")
)

func listScreenMonitors() []ScreenMonitor {
	monitors := make([]ScreenMonitor, 0, 4)
	callback := syscall.NewCallback(func(handle, _ uintptr, _ *RECT, _ uintptr) uintptr {
		info := monitorInfoEx{Size: uint32(unsafe.Sizeof(monitorInfoEx{}))}
		ok, _, _ := procGetMonitorInfoW.Call(handle, uintptr(unsafe.Pointer(&info)))
		if ok == 0 || info.Monitor.Right <= info.Monitor.Left || info.Monitor.Bottom <= info.Monitor.Top {
			return 1
		}
		name := syscall.UTF16ToString(info.Device[:])
		if name == "" {
			name = "Display"
		}
		monitors = append(monitors, ScreenMonitor{Index: len(monitors), Name: name, Width: info.Monitor.Right - info.Monitor.Left, Height: info.Monitor.Bottom - info.Monitor.Top, Primary: info.Flags&monitorInfoPrimary != 0, Bounds: info.Monitor})
		return 1
	})
	procEnumDisplayMonitors.Call(0, 0, callback, 0)
	if len(monitors) == 0 {
		monitors = append(monitors, ScreenMonitor{Index: 0, Name: "Primary display", Width: 1, Height: 1, Primary: true, Bounds: RECT{Right: 1, Bottom: 1}})
	}
	return monitors
}

func screenMonitorAt(index int) ScreenMonitor {
	monitors := listScreenMonitors()
	if index >= 0 && index < len(monitors) {
		return monitors[index]
	}
	for _, monitor := range monitors {
		if monitor.Primary {
			return monitor
		}
	}
	return monitors[0]
}

func captureScreenJPEG(monitor ScreenMonitor, maxWidth int32, quality int) ([]byte, error) {
	sourceW, sourceH := monitor.Bounds.Right-monitor.Bounds.Left, monitor.Bounds.Bottom-monitor.Bounds.Top
	if sourceW <= 1 || sourceH <= 1 {
		return nil, errors.New("display has no capturable area")
	}
	width, height := sourceW, sourceH
	if maxWidth > 0 && width > maxWidth {
		scale := float64(maxWidth) / float64(width)
		width = maxWidth
		height = int32(math.Max(1, math.Round(float64(height)*scale)))
	}

	screenDC, _, _ := procGetDC.Call(0)
	if screenDC == 0 {
		return nil, errors.New("Windows did not provide a screen device context")
	}
	defer procReleaseDC.Call(0, screenDC)
	memoryDC, _, _ := procCreateCompatibleDC.Call(screenDC)
	if memoryDC == 0 {
		return nil, errors.New("could not create the screen capture surface")
	}
	defer procDeleteDC.Call(memoryDC)

	info := bitmapInfo{Header: bitmapInfoHeader{Size: uint32(unsafe.Sizeof(bitmapInfoHeader{})), Width: width, Height: -height, Planes: 1, BitCount: 32, Compression: biRGB}}
	var pixels unsafe.Pointer
	bitmap, _, _ := procCreateDIBSection.Call(memoryDC, uintptr(unsafe.Pointer(&info)), dibRGBColors, uintptr(unsafe.Pointer(&pixels)), 0, 0)
	if bitmap == 0 || pixels == nil {
		return nil, errors.New("could not allocate the screen capture bitmap")
	}
	defer procDeleteObject.Call(bitmap)
	old, _, _ := procSelectObject.Call(memoryDC, bitmap)
	if old != 0 {
		defer procSelectObject.Call(memoryDC, old)
	}
	procSetStretchBltMode.Call(memoryDC, stretchHalftone)
	ok, _, _ := procStretchBlt.Call(memoryDC, 0, 0, uintptr(width), uintptr(height), screenDC, uintptr(int64(monitor.Bounds.Left)), uintptr(int64(monitor.Bounds.Top)), uintptr(sourceW), uintptr(sourceH), SRCCOPY|captureBLT)
	if ok == 0 {
		return nil, errors.New("Windows refused the display capture")
	}

	// Include the visible cursor in the preview. Windows supplies screen-space
	// coordinates; the capture surface may be downscaled.
	cursor := cursorInfo{Size: uint32(unsafe.Sizeof(cursorInfo{}))}
	if shown, _, _ := procGetCursorInfo.Call(uintptr(unsafe.Pointer(&cursor))); shown != 0 && cursor.Flags&cursorShowing != 0 && cursor.Cursor != 0 {
		x := int32(float64(cursor.Screen.X-monitor.Bounds.Left) * float64(width) / float64(sourceW))
		y := int32(float64(cursor.Screen.Y-monitor.Bounds.Top) * float64(height) / float64(sourceH))
		procDrawIconEx.Call(memoryDC, uintptr(int64(x)), uintptr(int64(y)), cursor.Cursor, 0, 0, 0, 0, DI_NORMAL)
	}

	length := int(width * height * 4)
	bgra := unsafe.Slice((*byte)(pixels), length)
	frame := image.NewRGBA(image.Rect(0, 0, int(width), int(height)))
	for offset := 0; offset < length; offset += 4 {
		frame.Pix[offset], frame.Pix[offset+1], frame.Pix[offset+2], frame.Pix[offset+3] = bgra[offset+2], bgra[offset+1], bgra[offset], 0xff
	}
	var encoded bytes.Buffer
	if quality < 35 || quality > 90 {
		quality = 68
	}
	if err := jpeg.Encode(&encoded, frame, &jpeg.Options{Quality: quality}); err != nil {
		return nil, err
	}
	return encoded.Bytes(), nil
}
