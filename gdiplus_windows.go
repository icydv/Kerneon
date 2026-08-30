//go:build windows

package main

import (
	"math"
	"sync"
	"syscall"
	"unsafe"
)

type gdiplusStartupInput struct {
	Version                  uint32
	DebugEventCallback       uintptr
	SuppressBackgroundThread int32
	SuppressExternalCodecs   int32
}

var (
	gdiplus                       = syscall.NewLazyDLL("gdiplus.dll")
	procGdiplusStartup            = gdiplus.NewProc("GdiplusStartup")
	procGdiplusShutdown           = gdiplus.NewProc("GdiplusShutdown")
	procGdipCreateFromHDC         = gdiplus.NewProc("GdipCreateFromHDC")
	procGdipDeleteGraphics        = gdiplus.NewProc("GdipDeleteGraphics")
	procGdipFlush                 = gdiplus.NewProc("GdipFlush")
	procGdipSetSmoothingMode      = gdiplus.NewProc("GdipSetSmoothingMode")
	procGdipSetPixelOffsetMode    = gdiplus.NewProc("GdipSetPixelOffsetMode")
	procGdipSetCompositingQuality = gdiplus.NewProc("GdipSetCompositingQuality")
	procGdipCreateSolidFill       = gdiplus.NewProc("GdipCreateSolidFill")
	procGdipDeleteBrush           = gdiplus.NewProc("GdipDeleteBrush")
	procGdipCreatePath            = gdiplus.NewProc("GdipCreatePath")
	procGdipDeletePath            = gdiplus.NewProc("GdipDeletePath")
	procGdipAddPathLineI          = gdiplus.NewProc("GdipAddPathLineI")
	procGdipAddPathBezierI        = gdiplus.NewProc("GdipAddPathBezierI")
	procGdipClosePathFigure       = gdiplus.NewProc("GdipClosePathFigure")
	procGdipFillPath              = gdiplus.NewProc("GdipFillPath")
	procGdipFillRectangleI        = gdiplus.NewProc("GdipFillRectangleI")
	procGdipFillEllipseI          = gdiplus.NewProc("GdipFillEllipseI")
	procGdipFillPolygonI          = gdiplus.NewProc("GdipFillPolygonI")
	procGdipAddPathEllipseI       = gdiplus.NewProc("GdipAddPathEllipseI")
	gdipOnce                      sync.Once
	gdipToken                     uintptr
	gdipBrushes                   = make(map[uint32]uintptr)
	gdipARGBBrushes               = make(map[uint32]uintptr)
	gdipRoundPaths                = make(map[roundPathKey]uintptr)
)

type roundPathKey struct {
	Left, Top, Right, Bottom, Radius int32
}

func aaFlush(graphics uintptr) {
	if graphics != 0 {
		procGdipFlush.Call(graphics, 1) // FlushIntentionSync before GDI uses this HDC.
	}
}

func aaRoundedFill(graphics uintptr, r RECT, radius int32, color uint32) {
	if graphics == 0 || r.Right <= r.Left || r.Bottom <= r.Top {
		return
	}
	w, h := r.Right-r.Left, r.Bottom-r.Top
	if radius < 1 {
		procGdipFillRectangleI.Call(graphics, aaBrush(color), uintptr(r.Left), uintptr(r.Top), uintptr(w), uintptr(h))
		return
	}
	if radius*2 > w {
		radius = w / 2
	}
	if radius*2 > h {
		radius = h / 2
	}
	path := aaRoundedPath(r, radius)
	if path != 0 {
		procGdipFillPath.Call(graphics, aaBrush(color), path)
	}
}

func aaRoundedFillARGB(graphics uintptr, r RECT, radius int32, argb uint32) {
	if graphics == 0 || r.Right <= r.Left || r.Bottom <= r.Top {
		return
	}
	w, h := r.Right-r.Left, r.Bottom-r.Top
	if radius < 1 {
		procGdipFillRectangleI.Call(graphics, aaARGBBrush(argb), uintptr(r.Left), uintptr(r.Top), uintptr(w), uintptr(h))
		return
	}
	if radius*2 > w {
		radius = w / 2
	}
	if radius*2 > h {
		radius = h / 2
	}
	if path := aaRoundedPath(r, radius); path != 0 {
		procGdipFillPath.Call(graphics, aaARGBBrush(argb), path)
	}
}

func aaLine(graphics uintptr, x1, y1, x2, y2 int32, color uint32, width int32) {
	aaLineWithBrush(graphics, x1, y1, x2, y2, aaBrush(color), width)
}

func aaLineARGB(graphics uintptr, x1, y1, x2, y2 int32, argb uint32, width int32) {
	aaLineWithBrush(graphics, x1, y1, x2, y2, aaARGBBrush(argb), width)
}

func aaLineWithBrush(graphics uintptr, x1, y1, x2, y2 int32, brush uintptr, width int32) {
	if graphics == 0 {
		return
	}
	if width < 1 {
		width = 1
	}
	dx, dy := float64(x2-x1), float64(y2-y1)
	length := math.Hypot(dx, dy)
	if length == 0 {
		procGdipFillEllipseI.Call(graphics, brush, uintptr(x1-width/2), uintptr(y1-width/2), uintptr(width), uintptr(width))
		return
	}
	half := math.Max(.65, float64(width)/2)
	nx, ny := -dy/length*half, dx/length*half
	points := [4]POINT{
		{X: int32(math.Round(float64(x1) + nx)), Y: int32(math.Round(float64(y1) + ny))},
		{X: int32(math.Round(float64(x2) + nx)), Y: int32(math.Round(float64(y2) + ny))},
		{X: int32(math.Round(float64(x2) - nx)), Y: int32(math.Round(float64(y2) - ny))},
		{X: int32(math.Round(float64(x1) - nx)), Y: int32(math.Round(float64(y1) - ny))},
	}
	procGdipFillPolygonI.Call(graphics, brush, uintptr(unsafe.Pointer(&points[0])), 4, 0)
	radius := max32(1, (width+1)/2)
	procGdipFillEllipseI.Call(graphics, brush, uintptr(x1-radius), uintptr(y1-radius), uintptr(radius*2), uintptr(radius*2))
	procGdipFillEllipseI.Call(graphics, brush, uintptr(x2-radius), uintptr(y2-radius), uintptr(radius*2), uintptr(radius*2))
}

// aaArc fills a continuous antialiased annular segment built from cubic Bezier
// curves. A many-sided integer polygon still bakes tiny stair steps into the
// geometry before GDI+ sees it; two smooth cubic segments preserve a genuinely
// curved silhouette even when the mark is only a few dozen pixels high.
func aaArc(graphics uintptr, cx, cy, radius int32, startDeg, endDeg float64, color uint32, width int32) {
	if graphics == 0 || radius <= 0 || width <= 0 || startDeg == endDeg {
		return
	}
	pointAt := func(degrees, r float64) POINT {
		radians := degrees * math.Pi / 180
		return POINT{X: cx + int32(math.Round(r*math.Cos(radians))), Y: cy + int32(math.Round(r*math.Sin(radians)))}
	}
	var path uintptr
	if status, _, _ := procGdipCreatePath.Call(0, uintptr(unsafe.Pointer(&path))); status != 0 || path == 0 {
		return
	}
	defer procGdipDeletePath.Call(path)
	addArc := func(r, from, to float64) {
		segments := int(math.Ceil(math.Abs(to-from) / 80))
		if segments < 1 {
			segments = 1
		}
		for i := 0; i < segments; i++ {
			a := from + (to-from)*float64(i)/float64(segments)
			b := from + (to-from)*float64(i+1)/float64(segments)
			aRad, bRad := a*math.Pi/180, b*math.Pi/180
			k := 4.0 / 3.0 * math.Tan((bRad-aRad)/4)
			p0 := pointAt(a, r)
			p3 := pointAt(b, r)
			p1 := POINT{X: cx + int32(math.Round(r*(math.Cos(aRad)-k*math.Sin(aRad)))), Y: cy + int32(math.Round(r*(math.Sin(aRad)+k*math.Cos(aRad))))}
			p2 := POINT{X: cx + int32(math.Round(r*(math.Cos(bRad)+k*math.Sin(bRad)))), Y: cy + int32(math.Round(r*(math.Sin(bRad)-k*math.Cos(bRad))))}
			procGdipAddPathBezierI.Call(path, uintptr(p0.X), uintptr(p0.Y), uintptr(p1.X), uintptr(p1.Y), uintptr(p2.X), uintptr(p2.Y), uintptr(p3.X), uintptr(p3.Y))
		}
	}
	outer := float64(radius) + float64(width)/2
	inner := math.Max(0, float64(radius)-float64(width)/2)
	addArc(outer, startDeg, endDeg)
	outerEnd, innerEnd := pointAt(endDeg, outer), pointAt(endDeg, inner)
	procGdipAddPathLineI.Call(path, uintptr(outerEnd.X), uintptr(outerEnd.Y), uintptr(innerEnd.X), uintptr(innerEnd.Y))
	addArc(inner, endDeg, startDeg)
	innerStart, outerStart := pointAt(startDeg, inner), pointAt(startDeg, outer)
	procGdipAddPathLineI.Call(path, uintptr(innerStart.X), uintptr(innerStart.Y), uintptr(outerStart.X), uintptr(outerStart.Y))
	procGdipClosePathFigure.Call(path)
	procGdipFillPath.Call(graphics, aaBrush(color), path)
	capRadius := max32(1, (width+1)/2)
	for _, degrees := range []float64{startDeg, endDeg} {
		cap := pointAt(degrees, float64(radius))
		procGdipFillEllipseI.Call(graphics, aaBrush(color), uintptr(cap.X-capRadius), uintptr(cap.Y-capRadius), uintptr(capRadius*2), uintptr(capRadius*2))
	}
}

// aaEllipseARGB fills one alternate-winding GDI+ path instead of approximating
// a ring with hundreds of individually antialiased segments. All native
// arguments remain integers, which is ABI-safe through syscall on Windows.
func aaEllipseARGB(graphics uintptr, cx, cy, radius int32, argb uint32, width int32) {
	if graphics == 0 || radius <= 0 || width <= 0 {
		return
	}
	if width >= radius {
		procGdipFillEllipseI.Call(graphics, aaARGBBrush(argb), uintptr(cx-radius), uintptr(cy-radius), uintptr(radius*2), uintptr(radius*2))
		return
	}
	var path uintptr
	if status, _, _ := procGdipCreatePath.Call(0, uintptr(unsafe.Pointer(&path))); status != 0 || path == 0 {
		return
	}
	inner := radius - width
	procGdipAddPathEllipseI.Call(path, uintptr(cx-radius), uintptr(cy-radius), uintptr(radius*2), uintptr(radius*2))
	procGdipAddPathEllipseI.Call(path, uintptr(cx-inner), uintptr(cy-inner), uintptr(inner*2), uintptr(inner*2))
	procGdipFillPath.Call(graphics, aaARGBBrush(argb), path)
	procGdipDeletePath.Call(path)
}

func startGDIPlus() {
	gdipOnce.Do(func() {
		input := gdiplusStartupInput{Version: 1}
		status, _, _ := procGdiplusStartup.Call(uintptr(unsafe.Pointer(&gdipToken)), uintptr(unsafe.Pointer(&input)), 0)
		if status != 0 {
			gdipToken = 0
		}
	})
}

func newAAGraphics(hdc uintptr) uintptr {
	startGDIPlus()
	if gdipToken == 0 {
		return 0
	}
	var graphics uintptr
	if status, _, _ := procGdipCreateFromHDC.Call(hdc, uintptr(unsafe.Pointer(&graphics))); status != 0 {
		return 0
	}
	procGdipSetSmoothingMode.Call(graphics, 6)      // AntiAlias8x8
	procGdipSetPixelOffsetMode.Call(graphics, 2)    // HighQuality, without a half-pixel fringe
	procGdipSetCompositingQuality.Call(graphics, 3) // Gamma-corrected edge blending
	return graphics
}

func colorARGB(color uint32) uint32 {
	r := color & 0xff
	g := (color >> 8) & 0xff
	b := (color >> 16) & 0xff
	return 0xff000000 | r<<16 | g<<8 | b
}

func aaBrush(color uint32) uintptr {
	if brush := gdipBrushes[color]; brush != 0 {
		return brush
	}
	var brush uintptr
	if status, _, _ := procGdipCreateSolidFill.Call(uintptr(colorARGB(color)), uintptr(unsafe.Pointer(&brush))); status == 0 {
		gdipBrushes[color] = brush
	}
	return brush
}

func aaARGBBrush(argb uint32) uintptr {
	if brush := gdipARGBBrushes[argb]; brush != 0 {
		return brush
	}
	var brush uintptr
	if status, _, _ := procGdipCreateSolidFill.Call(uintptr(argb), uintptr(unsafe.Pointer(&brush))); status == 0 {
		gdipARGBBrushes[argb] = brush
	}
	return brush
}

func colorWithAlpha(color uint32, alpha uint8) uint32 {
	r := color & 0xff
	g := (color >> 8) & 0xff
	b := (color >> 16) & 0xff
	return uint32(alpha)<<24 | r<<16 | g<<8 | b
}

func aaRoundedPath(r RECT, radius int32) uintptr {
	w, h := r.Right-r.Left, r.Bottom-r.Top
	if radius < 1 || w <= 0 || h <= 0 {
		return 0
	}
	if radius*2 > w {
		radius = w / 2
	}
	if radius*2 > h {
		radius = h / 2
	}
	key := roundPathKey{r.Left, r.Top, r.Right, r.Bottom, radius}
	if path := gdipRoundPaths[key]; path != 0 {
		return path
	}
	var path uintptr
	if status, _, _ := procGdipCreatePath.Call(0, uintptr(unsafe.Pointer(&path))); status != 0 {
		return 0
	}
	// One continuous path avoids the seams and dark overlap fringe produced by
	// composing four independently-antialiased ellipses. 0.552 approximates a
	// quarter circle with a cubic Bezier while keeping every native argument an
	// integer (and therefore ABI-safe through syscall on Windows/amd64).
	k := int32(math.Round(float64(radius) * 0.5522847498))
	procGdipAddPathLineI.Call(path, uintptr(r.Left+radius), uintptr(r.Top), uintptr(r.Right-radius), uintptr(r.Top))
	procGdipAddPathBezierI.Call(path,
		uintptr(r.Right-radius), uintptr(r.Top),
		uintptr(r.Right-radius+k), uintptr(r.Top),
		uintptr(r.Right), uintptr(r.Top+radius-k),
		uintptr(r.Right), uintptr(r.Top+radius))
	procGdipAddPathLineI.Call(path, uintptr(r.Right), uintptr(r.Top+radius), uintptr(r.Right), uintptr(r.Bottom-radius))
	procGdipAddPathBezierI.Call(path,
		uintptr(r.Right), uintptr(r.Bottom-radius),
		uintptr(r.Right), uintptr(r.Bottom-radius+k),
		uintptr(r.Right-radius+k), uintptr(r.Bottom),
		uintptr(r.Right-radius), uintptr(r.Bottom))
	procGdipAddPathLineI.Call(path, uintptr(r.Right-radius), uintptr(r.Bottom), uintptr(r.Left+radius), uintptr(r.Bottom))
	procGdipAddPathBezierI.Call(path,
		uintptr(r.Left+radius), uintptr(r.Bottom),
		uintptr(r.Left+radius-k), uintptr(r.Bottom),
		uintptr(r.Left), uintptr(r.Bottom-radius+k),
		uintptr(r.Left), uintptr(r.Bottom-radius))
	procGdipAddPathLineI.Call(path, uintptr(r.Left), uintptr(r.Bottom-radius), uintptr(r.Left), uintptr(r.Top+radius))
	procGdipAddPathBezierI.Call(path,
		uintptr(r.Left), uintptr(r.Top+radius),
		uintptr(r.Left), uintptr(r.Top+radius-k),
		uintptr(r.Left+radius-k), uintptr(r.Top),
		uintptr(r.Left+radius), uintptr(r.Top))
	procGdipClosePathFigure.Call(path)
	gdipRoundPaths[key] = path
	return path
}

func shutdownGDIPlus() {
	for _, brush := range gdipBrushes {
		procGdipDeleteBrush.Call(brush)
	}
	for _, brush := range gdipARGBBrushes {
		procGdipDeleteBrush.Call(brush)
	}
	for _, path := range gdipRoundPaths {
		procGdipDeletePath.Call(path)
	}
	gdipBrushes = make(map[uint32]uintptr)
	gdipARGBBrushes = make(map[uint32]uintptr)
	gdipRoundPaths = make(map[roundPathKey]uintptr)
	if gdipToken != 0 {
		procGdiplusShutdown.Call(gdipToken)
		gdipToken = 0
	}
}
