//go:build windows

package main

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"kerneon/core"
)

var palette = struct{ BG, Nav, Surface, Card, CardHover, Border, Highlight, Shadow, Grid, Text, Muted, Muted2, Cyan, Violet, Blue, Green, Amber, Red uint32 }{
	BG: rgb(10, 12, 14), Nav: rgb(12, 14, 16), Surface: rgb(17, 20, 23), Card: rgb(20, 23, 27), CardHover: rgb(25, 29, 34), Border: rgb(31, 36, 41), Highlight: rgb(48, 57, 64), Shadow: rgb(4, 6, 8), Grid: rgb(29, 34, 39), Text: rgb(237, 242, 244), Muted: rgb(148, 159, 166), Muted2: rgb(87, 99, 107), Cyan: rgb(132, 218, 222), Violet: rgb(172, 163, 222), Blue: rgb(131, 174, 224), Green: rgb(124, 204, 169), Amber: rgb(222, 183, 116), Red: rgb(224, 126, 140),
}

const navWidthUnits int32 = 198

func rgb(r, g, b uint32) uint32 { return r | (g << 8) | (b << 16) }

type fontKey struct {
	Face              string
	Size, Weight, DPI int32
}
type penKey struct {
	Color uint32
	Width int32
}

var gdiCache = struct {
	brushes map[uint32]uintptr
	pens    map[penKey]uintptr
	fonts   map[fontKey]uintptr
}{make(map[uint32]uintptr), make(map[penKey]uintptr), make(map[fontKey]uintptr)}

func cachedBrush(color uint32) uintptr {
	if h := gdiCache.brushes[color]; h != 0 {
		return h
	}
	h, _, _ := procCreateSolidBrush.Call(uintptr(color))
	gdiCache.brushes[color] = h
	return h
}
func cachedPen(color uint32, width int32) uintptr {
	k := penKey{color, width}
	if h := gdiCache.pens[k]; h != 0 {
		return h
	}
	h, _, _ := procCreatePen.Call(PS_SOLID, uintptr(width), uintptr(color))
	gdiCache.pens[k] = h
	return h
}
func cachedFont(face string, size, weight, dpi int32) uintptr {
	k := fontKey{face, size, weight, dpi}
	if h := gdiCache.fonts[k]; h != 0 {
		return h
	}
	visualSize := size
	if visualSize <= 9 {
		visualSize++
	}
	height := -int32(float64(visualSize) * float64(dpi) / 72.0)
	h, _, _ := procCreateFontW.Call(uintptr(height), 0, 0, 0, uintptr(weight), 0, 0, 0, 1, 0, 0, 6, 0, uintptr(unsafe.Pointer(utf16Ptr(face))))
	gdiCache.fonts[k] = h
	return h
}
func freeGDICache() {
	for _, h := range gdiCache.brushes {
		procDeleteObject.Call(h)
	}
	for _, h := range gdiCache.pens {
		procDeleteObject.Call(h)
	}
	for _, h := range gdiCache.fonts {
		procDeleteObject.Call(h)
	}
	gdiCache.brushes = make(map[uint32]uintptr)
	gdiCache.pens = make(map[penKey]uintptr)
	gdiCache.fonts = make(map[fontKey]uintptr)
}

type BackBuffer struct {
	DC, Bitmap, Old uintptr
	W, H            int32
}

func (b *BackBuffer) Ensure(hdc uintptr, w, h int32) bool {
	if w <= 0 || h <= 0 {
		return false
	}
	if b.DC != 0 && b.W == w && b.H == h {
		return true
	}
	b.Destroy()
	b.DC, _, _ = procCreateCompatibleDC.Call(hdc)
	if b.DC == 0 {
		return false
	}
	b.Bitmap, _, _ = procCreateCompatibleBitmap.Call(hdc, uintptr(w), uintptr(h))
	if b.Bitmap == 0 {
		b.Destroy()
		return false
	}
	b.Old, _, _ = procSelectObject.Call(b.DC, b.Bitmap)
	b.W, b.H = w, h
	return true
}
func (b *BackBuffer) Destroy() {
	if b.DC != 0 && b.Old != 0 {
		procSelectObject.Call(b.DC, b.Old)
	}
	if b.Bitmap != 0 {
		procDeleteObject.Call(b.Bitmap)
	}
	if b.DC != 0 {
		procDeleteDC.Call(b.DC)
	}
	*b = BackBuffer{}
}

type Canvas struct {
	HDC, AA        uintptr
	W, H, DPI      int32
	app            *App
	lastRound      RECT
	lastRoundColor uint32
	lastRoundValid bool
}

func (c *Canvas) s(v int32) int32 { return int32(float64(v) * float64(c.DPI) / 96) }
func (c *Canvas) fill(r RECT, color uint32) {
	aaFlush(c.AA)
	procFillRect.Call(c.HDC, uintptr(unsafe.Pointer(&r)), cachedBrush(color))
}
func (c *Canvas) rounded(r RECT, radius int32, color uint32) {
	c.roundedFill(r, radius, color)
}
func (c *Canvas) roundedFill(r RECT, radius int32, color uint32) {
	if c.AA != 0 {
		aaRoundedFill(c.AA, r, c.s(radius), color)
		c.lastRound, c.lastRoundColor, c.lastRoundValid = r, color, true
		return
	}
	br := cachedBrush(color)
	oldBr, _, _ := procSelectObject.Call(c.HDC, br)
	null, _, _ := procGetStockObject.Call(NULL_PEN)
	oldPen, _, _ := procSelectObject.Call(c.HDC, null)
	procRoundRect.Call(c.HDC, uintptr(r.Left), uintptr(r.Top), uintptr(r.Right), uintptr(r.Bottom), uintptr(c.s(radius)), uintptr(c.s(radius)))
	procSelectObject.Call(c.HDC, oldPen)
	procSelectObject.Call(c.HDC, oldBr)
}
func (c *Canvas) strokeRound(r RECT, radius int32, color uint32, width int32) {
	if c.AA != 0 {
		if c.lastRoundValid && c.lastRound == r {
			aaRoundedFill(c.AA, r, c.s(radius), color)
			inset := c.s(width)
			inner := RECT{r.Left + inset, r.Top + inset, r.Right - inset, r.Bottom - inset}
			aaRoundedFill(c.AA, inner, max32(1, c.s(radius)-inset), c.lastRoundColor)
		}
		return
	}
	pen := cachedPen(color, c.s(width))
	oldPen, _, _ := procSelectObject.Call(c.HDC, pen)
	hollow, _, _ := procGetStockObject.Call(HOLLOW_BRUSH)
	oldBr, _, _ := procSelectObject.Call(c.HDC, hollow)
	procRoundRect.Call(c.HDC, uintptr(r.Left), uintptr(r.Top), uintptr(r.Right), uintptr(r.Bottom), uintptr(c.s(radius)), uintptr(c.s(radius)))
	procSelectObject.Call(c.HDC, oldBr)
	procSelectObject.Call(c.HDC, oldPen)
}
func (c *Canvas) line(x1, y1, x2, y2 int32, color uint32, width int32) {
	if c.AA != 0 {
		aaLine(c.AA, x1, y1, x2, y2, color, c.s(width))
		return
	}
	pen := cachedPen(color, c.s(width))
	old, _, _ := procSelectObject.Call(c.HDC, pen)
	procMoveToEx.Call(c.HDC, uintptr(x1), uintptr(y1), 0)
	procLineTo.Call(c.HDC, uintptr(x2), uintptr(y2))
	procSelectObject.Call(c.HDC, old)
}
func (c *Canvas) polyline(points []POINT, color uint32, width int32) {
	if len(points) < 2 {
		return
	}
	if c.AA != 0 {
		for i := 1; i < len(points); i++ {
			aaLine(c.AA, points[i-1].X, points[i-1].Y, points[i].X, points[i].Y, color, c.s(width))
		}
		return
	}
	pen := cachedPen(color, c.s(width))
	old, _, _ := procSelectObject.Call(c.HDC, pen)
	procPolyline.Call(c.HDC, uintptr(unsafe.Pointer(&points[0])), uintptr(len(points)))
	procSelectObject.Call(c.HDC, old)
}
func (c *Canvas) arc(cx, cy, radius int32, startDeg, endDeg float64, color uint32, width int32) {
	if c.AA != 0 {
		aaArc(c.AA, cx, cy, radius, startDeg, endDeg, color, c.s(width))
		return
	}
	steps := int(math.Ceil(math.Abs(endDeg-startDeg) / 5))
	if steps < 8 {
		steps = 8
	}
	points := make([]POINT, steps+1)
	for i := range points {
		t := startDeg + (endDeg-startDeg)*float64(i)/float64(steps)
		radians := t * math.Pi / 180
		points[i] = POINT{X: cx + int32(math.Round(float64(radius)*math.Cos(radians))), Y: cy + int32(math.Round(float64(radius)*math.Sin(radians)))}
	}
	c.polyline(points, color, width)
	capRadius := c.s(width) / 2
	if capRadius < 1 {
		capRadius = 1
	}
	c.circle(points[0].X, points[0].Y, capRadius, color)
	c.circle(points[len(points)-1].X, points[len(points)-1].Y, capRadius, color)
}
func (c *Canvas) circle(x, y, r int32, color uint32) {
	if c.AA != 0 {
		procGdipFillEllipseI.Call(c.AA, aaBrush(color), uintptr(x-r), uintptr(y-r), uintptr(r*2), uintptr(r*2))
		return
	}
	br := cachedBrush(color)
	oldBr, _, _ := procSelectObject.Call(c.HDC, br)
	null, _, _ := procGetStockObject.Call(NULL_PEN)
	oldPen, _, _ := procSelectObject.Call(c.HDC, null)
	procEllipse.Call(c.HDC, uintptr(x-r), uintptr(y-r), uintptr(x+r), uintptr(y+r))
	procSelectObject.Call(c.HDC, oldPen)
	procSelectObject.Call(c.HDC, oldBr)
}
func (c *Canvas) icon(handle uintptr, r RECT) {
	if handle == 0 || r.Right <= r.Left || r.Bottom <= r.Top {
		return
	}
	aaFlush(c.AA)
	procDrawIconEx.Call(c.HDC, uintptr(r.Left), uintptr(r.Top), handle, uintptr(r.Right-r.Left), uintptr(r.Bottom-r.Top), 0, 0, DI_NORMAL)
}
func (c *Canvas) text(value string, r RECT, size, weight int32, color uint32, flags uint32) {
	c.textFace(value, r, "Segoe UI Variable Text", size, weight, color, flags)
}
func (c *Canvas) mono(value string, r RECT, size, weight int32, color uint32, flags uint32) {
	c.textFace(value, r, "Segoe UI Variable Text", size, weight, color, flags)
}
func (c *Canvas) textFace(value string, r RECT, face string, size, weight int32, color uint32, flags uint32) {
	if value == "" {
		return
	}
	aaFlush(c.AA)
	font := cachedFont(face, size, weight, c.DPI)
	old, _, _ := procSelectObject.Call(c.HDC, font)
	procSetTextColor.Call(c.HDC, uintptr(color))
	procSetBkMode.Call(c.HDC, TRANSPARENT)
	rr := r
	p := utf16Ptr(value)
	procDrawTextW.Call(c.HDC, uintptr(unsafe.Pointer(p)), ^uintptr(0), uintptr(unsafe.Pointer(&rr)), uintptr(flags))
	procSelectObject.Call(c.HDC, old)
}

func (a *App) paint(hwnd uintptr) {
	var ps PAINTSTRUCT
	hdc, _, _ := procBeginPaint.Call(hwnd, uintptr(unsafe.Pointer(&ps)))
	if hdc == 0 {
		return
	}
	defer procEndPaint.Call(hwnd, uintptr(unsafe.Pointer(&ps)))
	var rc RECT
	procGetClientRect.Call(hwnd, uintptr(unsafe.Pointer(&rc)))
	if !a.back.Ensure(hdc, rc.Right, rc.Bottom) {
		return
	}
	c := &Canvas{HDC: a.back.DC, AA: newAAGraphics(a.back.DC), W: rc.Right, H: rc.Bottom, DPI: a.dpi, app: a}
	now := time.Now()
	a.animationDelta = now.Sub(a.animationLast)
	if a.animationLast.IsZero() || a.animationDelta > 100*time.Millisecond {
		a.animationDelta = time.Second / 60
	}
	a.animationLast = now
	if c.AA != 0 {
		defer procGdipDeleteGraphics.Call(c.AA)
	}
	activationActive := !a.config.Appearance.ReducedMotion && !a.surgeActivationStart.IsZero() && now.Sub(a.surgeActivationStart) >= 0 && now.Sub(a.surgeActivationStart) < surgeActivationDuration
	if activationActive && a.surgeActivationCached && a.surgeActivationBack.Ensure(hdc, c.W, c.H) {
		procBitBlt.Call(c.HDC, 0, 0, uintptr(c.W), uintptr(c.H), a.surgeActivationBack.DC, 0, 0, SRCCOPY)
		a.drawPressFeedback(c)
		a.drawSurgeActivation(c)
		aaFlush(c.AA)
		procBitBlt.Call(hdc, 0, 0, uintptr(c.W), uintptr(c.H), c.HDC, 0, 0, SRCCOPY)
		return
	}
	a.surgeActivationCached = false
	c.fill(RECT{0, 0, c.W, c.H}, palette.BG)
	a.hits = a.hits[:0]
	a.graphPlots = a.graphPlots[:0]
	a.graphLines = a.graphLines[:0]
	if a.graphDragKey == "" {
		a.graphHover = GraphSampleSelection{}
	}
	if a.compact {
		a.drawCompact(c)
	} else {
		a.drawShell(c)
	}
	if activationActive && a.surgeActivationBack.Ensure(hdc, c.W, c.H) {
		aaFlush(c.AA)
		procBitBlt.Call(a.surgeActivationBack.DC, 0, 0, uintptr(c.W), uintptr(c.H), c.HDC, 0, 0, SRCCOPY)
		a.surgeActivationCached = true
	}
	a.drawPressFeedback(c)
	a.drawSurgeActivation(c)
	aaFlush(c.AA)
	procBitBlt.Call(hdc, 0, 0, uintptr(c.W), uintptr(c.H), c.HDC, 0, 0, SRCCOPY)
}

func (a *App) drawSurgeActivation(c *Canvas) {
	if a.config.Appearance.ReducedMotion || a.surgeActivationStart.IsZero() || c.AA == 0 {
		return
	}
	progress := float64(time.Since(a.surgeActivationStart)) / float64(surgeActivationDuration)
	if progress >= 1 {
		a.surgeActivationStart = time.Time{}
		return
	}
	if progress < 0 {
		progress = 0
	}

	// The activation flourish has a fixed rendering cost at every window size.
	// A short three-stroke signal travels from the Surge navigation item into
	// the page; unlike the former window-sized ellipses it stays cheap at
	// 120/144/240 Hz and never obscures the interface.
	navW := c.s(navWidthUnits)
	originX := navW - c.s(11)
	originY := (a.surgeNavRect.Top + a.surgeNavRect.Bottom) / 2
	if a.surgeNavRect.Bottom <= a.surgeNavRect.Top {
		originY = c.H / 2
	}
	eased := 1 - math.Pow(1-progress, 3)
	targetX, targetY := c.W-c.s(178), c.s(58)
	headX := originX + int32(math.Round(float64(targetX-originX)*eased))
	headY := originY + int32(math.Round(float64(targetY-originY)*eased))
	tail := clamp01(progress / 0.38)
	tailX := originX + int32(math.Round(float64(targetX-originX)*math.Max(0, eased-tail*0.22)))
	tailY := originY + int32(math.Round(float64(targetY-originY)*math.Max(0, eased-tail*0.22)))
	fade := math.Pow(1-progress, 0.72)
	aaLineARGB(c.AA, tailX, tailY, headX, headY, colorWithAlpha(palette.Cyan, uint8(math.Round(34*fade))), c.s(7))
	aaLineARGB(c.AA, tailX, tailY, headX, headY, colorWithAlpha(palette.Cyan, uint8(math.Round(104*fade))), c.s(2))
	aaLineARGB(c.AA, tailX, tailY, headX, headY, colorWithAlpha(palette.Text, uint8(math.Round(155*fade))), c.s(1))
	if headX > originX+c.s(8) {
		headRadius := c.s(3)
		procGdipFillEllipseI.Call(c.AA, aaARGBBrush(colorWithAlpha(palette.Text, uint8(math.Round(205*fade)))), uintptr(headX-headRadius), uintptr(headY-headRadius), uintptr(headRadius*2), uintptr(headRadius*2))
	}

	// A fixed-cost scan front makes it clear that the activation applies to the
	// whole measured workspace. Three strokes create depth without blur kernels
	// or window-sized paths.
	scanProgress := clamp01((progress - 0.05) / 0.66)
	scanEased := 1 - math.Pow(1-scanProgress, 3)
	scanX := navW + int32(math.Round(float64(c.W-navW)*scanEased))
	scanEnvelope := math.Sin(math.Pi * scanProgress)
	if scanEnvelope > 0 {
		top, bottom := c.s(76), c.H-c.s(12)
		aaLineARGB(c.AA, scanX-c.s(5), top, scanX-c.s(5), bottom, colorWithAlpha(palette.Cyan, uint8(math.Round(11*scanEnvelope))), c.s(9))
		aaLineARGB(c.AA, scanX-c.s(1), top, scanX-c.s(1), bottom, colorWithAlpha(palette.Cyan, uint8(math.Round(34*scanEnvelope))), c.s(3))
		aaLineARGB(c.AA, scanX, top, scanX, bottom, colorWithAlpha(palette.Text, uint8(math.Round(112*scanEnvelope))), c.s(1))
	}

	// Resolve the travelling signal into a short, unmistakable confirmation.
	// The label is intentionally declarative rather than decorative: users can
	// see that measured control—not an arbitrary boost preset—is now live.
	labelIn := clamp01((progress - 0.28) / 0.13)
	labelOut := clamp01((0.97 - progress) / 0.18)
	labelAlpha := math.Min(labelIn, labelOut)
	if labelAlpha <= 0 {
		return
	}
	labelEase := 1 - math.Pow(1-labelIn, 3)
	boxW := c.s(278) - int32(math.Round(float64(c.s(14))*(1-labelEase)))
	boxH := c.s(62)
	centerX := navW + (c.W-navW)/2
	centerY := c.H/2 - c.s(8)
	box := RECT{centerX - boxW/2, centerY - boxH/2, centerX + boxW/2, centerY + boxH/2}
	aaRoundedFillARGB(c.AA, RECT{box.Left + c.s(3), box.Top + c.s(5), box.Right + c.s(3), box.Bottom + c.s(5)}, c.s(15), colorWithAlpha(palette.Shadow, uint8(math.Round(145*labelAlpha))))
	aaRoundedFillARGB(c.AA, box, c.s(14), colorWithAlpha(rgb(10, 14, 16), uint8(math.Round(232*labelAlpha))))
	aaRoundedFillARGB(c.AA, RECT{box.Left, box.Top, box.Left + c.s(3), box.Bottom}, c.s(2), colorWithAlpha(palette.Cyan, uint8(math.Round(205*labelAlpha))))
	c.text("SURGE ACTIVE", RECT{box.Left + c.s(18), box.Top + c.s(8), box.Right - c.s(18), box.Top + c.s(34)}, 10, 720, blend(palette.CardHover, palette.Cyan, labelAlpha), DT_LEFT|DT_VCENTER|DT_SINGLELINE)
	c.text("Measured control is live", RECT{box.Left + c.s(18), box.Top + c.s(32), box.Right - c.s(18), box.Bottom - c.s(7)}, 8, 520, blend(palette.CardHover, palette.Text, labelAlpha), DT_LEFT|DT_VCENTER|DT_SINGLELINE)
}

func sameHit(a, b HitRegion) bool {
	return a.Action == b.Action && a.Value == b.Value && a.Rect == b.Rect
}

func (a *App) pressAmount(hit HitRegion) float64 {
	if a.pressedValid && sameHit(a.pressedHit, hit) {
		return 1
	}
	if a.releasedValid && sameHit(a.releasedHit, hit) {
		progress := time.Since(a.pressReleaseStart).Seconds() / 0.09
		if progress >= 1 {
			a.releasedValid = false
			return 0
		}
		if progress < 0 {
			return 1
		}
		return math.Pow(1-progress, 2)
	}
	return 0
}

func (a *App) drawPressFeedback(c *Canvas) {
	if c.AA == 0 || a.config.Appearance.ReducedMotion {
		return
	}
	hit := HitRegion{}
	if a.pressedValid {
		hit = a.pressedHit
	} else if a.releasedValid {
		hit = a.releasedHit
	} else {
		return
	}
	if !hasGlobalPressFeedback(hit) {
		return
	}
	amount := a.pressAmount(hit)
	if amount <= 0 {
		return
	}
	r := hit.Rect
	inset := c.s(1)
	r.Left, r.Top, r.Right, r.Bottom = r.Left+inset, r.Top+inset, r.Right-inset, r.Bottom-inset
	aaRoundedFillARGB(c.AA, r, c.s(10), colorWithAlpha(palette.Cyan, uint8(math.Round(31*amount))))
}

const silentHitValue = -1 << 30

func hasGlobalPressFeedback(hit HitRegion) bool {
	return hit.Action != "" && hit.Value != silentHitValue
}

func clamp01(value float64) float64 {
	if value < 0 {
		return 0
	}
	if value > 1 {
		return 1
	}
	return value
}

func (a *App) drawShell(c *Canvas) {
	navW := c.s(navWidthUnits)
	c.fill(RECT{0, 0, navW, c.H}, palette.Nav)
	c.line(navW-1, 0, navW-1, c.H, palette.Border, 1)
	a.drawBrand(c, RECT{c.s(20), c.s(18), navW - c.s(16), c.s(80)})
	type navItem struct {
		key, label string
		color      uint32
		child      bool
	}
	items := []navItem{{"overview", "Overview", palette.Cyan, false}, {"resources", "Components", palette.Cyan, false}}
	if !a.config.Window.ResourcesCollapsed {
		items = append(items,
			navItem{"cpu", "CPU", palette.Blue, true},
			navItem{"gpu", "GPU", palette.Violet, true},
			navItem{"memory", "Memory", palette.Cyan, true},
			navItem{"storage", "Storage", palette.Amber, true},
			navItem{"network", "Network", palette.Green, true},
		)
	}
	items = append(items,
		navItem{"processes", "Processes", palette.Blue, false},
		navItem{"gaming", "Gaming", palette.Violet, false},
		navItem{"insights", "Insights", palette.Cyan, false},
		navItem{"optimize", "Surge", palette.Cyan, false},
		navItem{"history", "History", palette.Blue, false},
		navItem{"alerts", "Event Lens", palette.Amber, false},
		navItem{"hardware", "Hardware Passport", palette.Green, false},
		navItem{"system", "System", palette.Muted, false},
		navItem{"remote", "Remote Link", palette.Cyan, false},
	)
	y := c.s(92)
	gap := c.s(1)
	itemH := (c.H - c.s(150) - int32(len(items)-1)*gap) / int32(len(items))
	if itemH > c.s(36) {
		itemH = c.s(36)
	} else if itemH < c.s(27) {
		itemH = c.s(27)
	}
	a.surgeNavRect = RECT{}
	resourcePage := a.page == "cpu" || a.page == "gpu" || a.page == "memory" || a.page == "storage" || a.page == "network"
	for _, it := range items {
		r := RECT{c.s(12), y, navW - c.s(12), y + itemH}
		if it.key == "resources" {
			hover := a.animate("nav:resources", boolFloat(a.pointIn(r)), 20*time.Millisecond)
			if hover > 0.01 || (resourcePage && a.config.Window.ResourcesCollapsed) {
				amount := hover * 0.70
				if resourcePage && a.config.Window.ResourcesCollapsed && amount < 0.34 {
					amount = 0.34
				}
				c.roundedFill(r, 10, blend(palette.Nav, palette.CardHover, amount))
			}
			c.circle(r.Left+c.s(22), (r.Top+r.Bottom)/2, c.s(3), blend(palette.Muted2, palette.Cyan, 0.72))
			c.text("Components", RECT{r.Left + c.s(34), r.Top, r.Right - c.s(30), r.Bottom}, 10, 620, func() uint32 {
				if resourcePage {
					return palette.Text
				}
				return palette.Muted
			}(), DT_LEFT|DT_VCENTER|DT_SINGLELINE)
			chevron := "⌄"
			if a.config.Window.ResourcesCollapsed {
				chevron = "›"
			}
			c.text(chevron, RECT{r.Right - c.s(30), r.Top, r.Right - c.s(8), r.Bottom}, 12, 620, palette.Muted2, DT_CENTER|DT_VCENTER|DT_SINGLELINE)
			a.hit(r, "toggle-resources", 0)
			y += itemH + gap
			continue
		}
		if it.child {
			r.Left += c.s(13)
		}
		selected := a.page == it.key
		hover := a.animate("nav:"+it.key, boolFloat(a.pointIn(r)), 22*time.Millisecond)
		if it.key == "optimize" && !selected {
			// Surge should be discoverable without borrowing the filled row and
			// left rail that exclusively communicate current-page selection.
			c.circle(r.Right-c.s(12), (r.Top+r.Bottom)/2, c.s(3), blend(palette.Nav, palette.Cyan, 0.68))
		}
		if hover > 0.01 && !selected {
			c.roundedFill(r, 10, blend(palette.Nav, palette.CardHover, hover*0.72))
		}
		if selected {
			c.roundedFill(r, 10, blend(palette.Nav, it.color, 0.075))
			c.rounded(RECT{r.Left, r.Top + c.s(9), r.Left + c.s(3), r.Bottom - c.s(9)}, 3, it.color)
		}
		labelLeft := r.Left + c.s(22)
		if it.child {
			c.circle(r.Left+c.s(9), (r.Top+r.Bottom)/2, c.s(2), blend(palette.Nav, it.color, 0.78))
			labelLeft = r.Left + c.s(19)
		}
		c.text(it.label, RECT{labelLeft, r.Top, r.Right - c.s(8), r.Bottom}, func() int32 {
			if it.child {
				return 10
			}
			return 11
		}(), func() int32 {
			if selected || it.key == "optimize" {
				return 650
			}
			return 500
		}(), func() uint32 {
			if selected {
				return palette.Text
			}
			if it.key == "optimize" {
				return blend(palette.Muted, palette.Cyan, 0.58)
			}
			return palette.Muted
		}(), DT_LEFT|DT_VCENTER|DT_SINGLELINE)
		a.hit(r, "nav:"+it.key, 0)
		if it.key == "optimize" {
			a.surgeNavRect = r
		}
		y += itemH + gap
	}
	settings := RECT{c.s(12), c.H - c.s(51), navW - c.s(12), c.H - c.s(14)}
	if a.page == "settings" {
		c.roundedFill(settings, 10, blend(palette.Nav, palette.Cyan, 0.075))
		c.roundedFill(RECT{settings.Left, settings.Top + c.s(9), settings.Left + c.s(3), settings.Bottom - c.s(9)}, 3, palette.Cyan)
	}
	c.text("Settings", RECT{settings.Left + c.s(22), settings.Top, settings.Right, settings.Bottom}, 11, 600, func() uint32 {
		if a.page == "settings" {
			return palette.Text
		}
		return palette.Muted
	}(), DT_LEFT|DT_VCENTER|DT_SINGLELINE)
	a.hit(settings, "nav:settings", 0)
	content := RECT{navW, 0, c.W, c.H}
	a.drawPageHeader(c, content)
	bodyRest := RECT{navW + c.s(28), c.s(88), c.W - c.s(28), c.H - c.s(22)}
	bodyRest.Top += a.drawStartupBanner(c, content)
	body := bodyRest
	if !a.config.Appearance.ReducedMotion && !a.pageTransitionStart.IsZero() {
		progress := time.Since(a.pageTransitionStart).Seconds() / 0.17
		if progress < 1 {
			if progress < 0 {
				progress = 0
			}
			// A quartic ease settles decisively without the mechanical constant
			// speed of the original horizontal slide. Direction follows the menu.
			eased := 1 - math.Pow(1-progress, 4)
			yOffset := a.pageTransitionDirection * c.s(int32(math.Round((1-eased)*5)))
			xOffset := c.s(int32(math.Round((1 - eased) * 2)))
			body.Left += xOffset
			body.Right += xOffset
			body.Top += yOffset
			body.Bottom += yOffset
		}
	}
	switch a.page {
	case "cpu":
		a.drawCPU(c, body)
	case "gpu":
		a.drawGPU(c, body)
	case "memory":
		a.drawMemory(c, body)
	case "storage":
		a.drawStorage(c, body)
	case "network":
		a.drawNetwork(c, body)
	case "processes":
		a.drawProcesses(c, body)
	case "gaming":
		a.drawGaming(c, body)
	case "insights":
		a.drawInsights(c, body)
	case "optimize":
		a.drawOptimize(c, body)
	case "history":
		a.drawHistory(c, body)
	case "alerts":
		a.drawAlerts(c, body)
	case "system":
		a.drawSystem(c, body)
	case "hardware":
		a.drawHardware(c, body)
	case "remote":
		a.drawRemote(c, body)
	case "settings":
		a.drawSettings(c, body)
	default:
		a.drawOverview(c, body)
	}
	if !a.config.Appearance.ReducedMotion && !a.pageTransitionStart.IsZero() {
		progress := clamp01(time.Since(a.pageTransitionStart).Seconds() / 0.17)
		if progress < 1 && c.AA != 0 {
			eased := 1 - math.Pow(1-progress, 4)
			alpha := uint8(math.Round(58 * (1 - eased)))
			procGdipFillRectangleI.Call(c.AA, aaARGBBrush(colorWithAlpha(palette.BG, alpha)), uintptr(bodyRest.Left), uintptr(bodyRest.Top), uintptr(bodyRest.Right-bodyRest.Left), uintptr(bodyRest.Bottom-bodyRest.Top))
		}
	}
}

func (a *App) drawStartupBanner(c *Canvas, content RECT) int32 {
	progress := a.animate("startup-banner", boolFloat(!a.startupEnabled), 58*time.Millisecond)
	if progress <= 0.01 {
		return 0
	}
	height, gap := c.s(48), c.s(9)
	yOffset := int32(math.Round(float64(c.s(10)) * (1 - progress)))
	r := RECT{content.Left + c.s(28), c.s(80) - yOffset, content.Right - c.s(28), c.s(80) - yOffset + height}
	c.roundedFill(r, 14, blend(palette.Surface, palette.Cyan, 0.055))
	c.strokeRound(r, 14, blend(palette.Border, palette.Cyan, 0.25), 1)
	c.circle(r.Left+c.s(19), (r.Top+r.Bottom)/2, c.s(4), palette.Cyan)
	message := "For the best experience, we recommend opening Kerneon when you sign in."
	messageColor := palette.Muted
	if a.startupBannerError != "" {
		message = "Windows could not enable startup. " + a.startupBannerError
		messageColor = palette.Amber
	}
	c.text("OPEN KERNEON AT SIGN-IN", RECT{r.Left + c.s(33), r.Top + c.s(5), r.Right - c.s(195), r.Top + c.s(24)}, 7, 720, palette.Cyan, DT_LEFT|DT_VCENTER|DT_SINGLELINE)
	c.text(message, RECT{r.Left + c.s(33), r.Top + c.s(23), r.Right - c.s(195), r.Bottom - c.s(4)}, 7, 470, messageColor, DT_LEFT|DT_VCENTER|DT_SINGLELINE|DT_END_ELLIPSIS)
	a.button(c, RECT{r.Right - c.s(181), r.Top + c.s(8), r.Right - c.s(10), r.Bottom - c.s(8)}, "Enable startup", "startup-enable", 0, true)
	return int32(math.Round(float64(height+gap) * progress))
}

// drawKerneonMark is the only in-app definition of the Kerneon monogram.
// The resource generator and remote SVG use the same normalized geometry:
// 0.65R pressure arc, 0.14R stroke and 0.54R rays in one ice-white tone.
func drawKerneonMark(c *Canvas, cx, cy, radius int32, surface uint32) {
	drawKerneonMarkTone(c, cx, cy, radius, surface, rgb(224, 244, 246))
}

// drawKerneonMarkTone preserves the canonical geometry while allowing the QR
// edition to use the same single-ink mark in positive form on its light field.
func drawKerneonMarkTone(c *Canvas, cx, cy, radius int32, surface, mark uint32) {
	r := c.s(radius)
	if r < 3 {
		return
	}
	// Derive every dimension from the DPI-scaled radius. Keeping the stroke at
	// 96-DPI thickness was why the mark could still look wiry on scaled screens.
	stroke := max32(1, int32(math.Round(float64(r)*0.14)))
	arcRadius := int32(math.Round(float64(r) * 0.65))
	startX := cx - int32(math.Round(float64(r)*0.04))
	endX := cx + int32(math.Round(float64(r)*0.54))
	endY := int32(math.Round(float64(r) * 0.51))
	glow := blend(surface, mark, 0.04)
	glowWidth := stroke + max32(1, c.s(1))
	c.arc(cx, cy, arcRadius, 100, 260, glow, glowWidth)
	c.line(startX, cy, endX, cy-endY, glow, glowWidth)
	c.line(startX, cy, endX, cy+endY, glow, glowWidth)
	c.arc(cx, cy, arcRadius, 100, 260, mark, stroke)
	c.line(startX, cy, endX, cy-endY, mark, stroke)
	c.line(startX, cy, endX, cy+endY, mark, stroke)
}

func (a *App) drawBrand(c *Canvas, r RECT) {
	// Let the monogram own the masthead. A small wordmark beside it made the
	// identity read like a toolbar label and forced the actual mark too small.
	// The 42-unit mark is exactly 50% larger than the previous masthead. Its
	// geometry is optically left-heavy, so offset the mathematical centre by
	// two units to centre the visible silhouette within the whole side panel.
	drawKerneonMark(c, (r.Left+r.Right)/2+c.s(2), r.Top+c.s(31), 42, palette.Nav)
}

func (a *App) drawPageHeader(c *Canvas, r RECT) {
	titles := map[string][2]string{"overview": {"Overview", "What matters now"}, "cpu": {"CPU", "Load, clocks and the work behind them"}, "gpu": {"GPU", "Graphics load, clocks and memory"}, "memory": {"Memory", "Capacity, pressure and committed use"}, "storage": {"Storage", "Space, activity and response time"}, "network": {"Network", "Throughput and connection quality"}, "processes": {"Processes", "See what is using your PC"}, "gaming": {"Gaming", "Lock a game and measure every frame"}, "insights": {"Insights", "Personal analysis from live evidence"}, "optimize": {"Kerneon Surge", "Find bottlenecks, test reversible changes, and verify frame delivery"}, "history": {"History", "Resources on one synchronized timeline"}, "alerts": {"Event Lens", "Problems separated from everyday noise"}, "hardware": {"Hardware Passport", "Identity, provenance and change"}, "system": {"System", "Hardware and Windows at a glance"}, "remote": {"Remote Link", "Your PC, nearby and securely paired"}, "settings": {"Settings", "Behavior, appearance and privacy"}}
	t := titles[a.page]
	if t[0] == "" {
		t = titles["overview"]
	}
	viewGranted, inputGranted, _ := a.screenPermissionState()
	titleRight := r.Right - c.s(300)
	if viewGranted {
		titleRight = r.Right - c.s(500)
	}
	c.text(t[0], RECT{r.Left + c.s(28), c.s(15), titleRight, c.s(47)}, 19, 690, palette.Text, DT_LEFT|DT_VCENTER|DT_SINGLELINE|DT_END_ELLIPSIS)
	c.text(t[1], RECT{r.Left + c.s(28), c.s(45), titleRight, c.s(70)}, 9, 450, palette.Muted, DT_LEFT|DT_VCENTER|DT_SINGLELINE|DT_END_ELLIPSIS)
	if viewGranted {
		indicator := RECT{r.Right - c.s(474), c.s(20), r.Right - c.s(276), c.s(59)}
		accent, label := palette.Cyan, "SCREEN VIEW · REVOKE"
		if inputGranted {
			accent, label = palette.Amber, "SCREEN + INPUT · REVOKE"
		}
		c.roundedFill(indicator, 18, blend(palette.Surface, accent, 0.13))
		c.circle(indicator.Left+c.s(15), (indicator.Top+indicator.Bottom)/2, c.s(4), accent)
		c.text(label, RECT{indicator.Left + c.s(27), indicator.Top, indicator.Right - c.s(10), indicator.Bottom}, 7, 700, accent, DT_LEFT|DT_VCENTER|DT_SINGLELINE|DT_END_ELLIPSIS)
		a.hit(indicator, "screen-revoke", 0)
	}
	capBtn := RECT{r.Right - c.s(264), c.s(20), r.Right - c.s(132), c.s(59)}
	a.button(c, capBtn, "Capture 60s", "capture-incident", 0, false)
	mini := RECT{r.Right - c.s(120), c.s(20), r.Right - c.s(28), c.s(59)}
	a.button(c, mini, "Compact", "compact", 0, false)
}

func (a *App) drawOverview(c *Canvas, r RECT) {
	// Network health remains visible in its own metric and Network page, but it
	// must never displace the local performance or stability headline.
	p := core.ExplainPressure(core.PressureInput{CPU: a.display.CPU, GPU: a.display.GPU, Memory: a.display.Memory, Disk: a.display.Disk})
	frames := a.frames.Snapshot()
	warnings, errors := a.eventCounts()
	experience := core.CalculateExperience(core.ExperienceInput{CPU: a.display.CPU, GPU: a.display.GPU, Memory: a.display.Memory, Disk: a.display.Disk, DiskLatency: a.snapshot.Disk.LatencyMs, NetworkLatency: a.snapshot.Network.LatencyMs, Jitter: a.snapshot.Network.JitterMs, PacketLoss: a.snapshot.Network.PacketLoss, FPS: frames.FPS, OnePercentLow: frames.OnePercentLow, P95Display: frames.P95DisplayMs, Dropped: frames.DroppedPercent, FrameSamples: frames.Samples, RecentWarnings: warnings, RecentErrors: errors, GameActive: frames.Capturing})
	col := palette.Green
	if p.Severity == 1 {
		col = palette.Cyan
	} else if p.Severity > 1 {
		col = palette.Amber
	}
	pressure := RECT{r.Left, r.Top, r.Right, r.Top + c.s(88)}
	c.rounded(pressure, 14, palette.Surface)
	c.strokeRound(pressure, 14, palette.Border, 1)
	signatureW := min32(c.s(390), (pressure.Right-pressure.Left)*46/100)
	signature := RECT{pressure.Right - signatureW, pressure.Top, pressure.Right, pressure.Bottom}
	c.line(signature.Left, pressure.Top+c.s(14), signature.Left, pressure.Bottom-c.s(14), palette.Border, 1)
	c.circle(pressure.Left+c.s(23), pressure.Top+c.s(34), c.s(5), col)
	c.text("Live state", RECT{pressure.Left + c.s(39), pressure.Top + c.s(9), signature.Left - c.s(16), pressure.Top + c.s(29)}, 7, 650, palette.Muted2, DT_LEFT|DT_VCENTER|DT_SINGLELINE)
	c.text(p.Title, RECT{pressure.Left + c.s(39), pressure.Top + c.s(28), signature.Left - c.s(16), pressure.Top + c.s(52)}, 11, 660, col, DT_LEFT|DT_VCENTER|DT_SINGLELINE|DT_END_ELLIPSIS)
	c.text(p.Explanation, RECT{pressure.Left + c.s(39), pressure.Top + c.s(54), signature.Left - c.s(16), pressure.Bottom - c.s(7)}, 8, 450, palette.Muted, DT_LEFT|DT_VCENTER|DT_SINGLELINE|DT_END_ELLIPSIS)
	a.drawExperienceScore(c, signature, experience)
	a.hit(signature, "nav:alerts", 0)
	y := pressure.Bottom + c.s(12)
	gap := c.s(10)
	cols := 3
	cardW := (r.Right - r.Left - gap*int32(cols-1)) / int32(cols)
	cards := []struct {
		title, value, sub, key string
		color                  uint32
		v                      float64
	}{{"CPU", fmt.Sprintf("%4.1f%%", a.display.CPU), fmt.Sprintf("%.0f MHz  ·  %d logical", a.snapshot.CPU.FrequencyMHz, a.snapshot.CPU.Logical), "cpu", palette.Blue, a.display.CPU}, {"GPU", availabilityPercent(a.snapshot.GPU.Available, a.display.GPU), gpuSub(a.snapshot.GPU), "gpu", palette.Violet, a.display.GPU}, {"Memory", fmt.Sprintf("%4.1f%%", a.display.Memory), fmt.Sprintf("%s available", core.FormatBytes(a.snapshot.Memory.Available)), "memory", palette.Cyan, a.display.Memory}, {"Storage", fmt.Sprintf("%4.1f%%", a.display.Disk), fmt.Sprintf("%s/s read  ·  %s/s write", shortBytes(a.snapshot.Disk.ReadBps), shortBytes(a.snapshot.Disk.WriteBps)), "storage", palette.Amber, a.display.Disk}, {"Download", a.downFormatter.Format(a.display.Down, time.Now()), a.snapshot.Network.Name, "network", palette.Green, a.display.Down}, {"Latency", latencyLabel(a.snapshot.Network), fmt.Sprintf("%.1f ms jitter  ·  %.0f%% loss", a.snapshot.Network.JitterMs, a.snapshot.Network.PacketLoss), "latency", latencyColor(a.snapshot.Network), a.display.Latency}}
	for i, card := range cards {
		x := r.Left + int32(i%cols)*(cardW+gap)
		yy := y + int32(i/cols)*(c.s(102)+gap)
		cr := RECT{x, yy, x + cardW, yy + c.s(102)}
		a.metricCard(c, cr, card.title, card.value, card.sub, card.color, card.key, card.v)
	}
	graphTop := y + 2*(c.s(102)+gap)
	graph := RECT{r.Left, graphTop, r.Right, r.Bottom}
	if graph.Bottom-graph.Top > c.s(145) {
		a.drawMultiGraph(c, graph, "System activity · Last "+durationLabel(a.config.Appearance.GraphSeconds), []graphSeries{{"CPU", palette.Blue, func(h HistorySample) float64 { return h.CPU }}, {"GPU", palette.Violet, func(h HistorySample) float64 { return h.GPU }}, {"Memory", palette.Cyan, func(h HistorySample) float64 { return h.Memory }}}, 100, "overview")
	}
}

func (a *App) drawExperienceScore(c *Canvas, r RECT, score core.ExperienceScore) {
	c.text("Live experience", RECT{r.Left + c.s(16), r.Top + c.s(7), r.Right - c.s(16), r.Top + c.s(26)}, 7, 650, palette.Muted2, DT_LEFT|DT_VCENTER|DT_SINGLELINE)
	color := palette.Green
	if score.Overall < 75 {
		color = palette.Amber
	} else if score.Overall < 90 {
		color = palette.Cyan
	}
	c.mono(fmt.Sprint(score.Overall), RECT{r.Left + c.s(16), r.Top + c.s(26), r.Left + c.s(82), r.Bottom - c.s(9)}, 24, 680, color, DT_LEFT|DT_VCENTER|DT_SINGLELINE)
	c.text(score.Grade, RECT{r.Left + c.s(83), r.Top + c.s(27), r.Left + c.s(166), r.Top + c.s(48)}, 9, 660, palette.Text, DT_LEFT|DT_VCENTER|DT_SINGLELINE)
	detail := score.Detail
	if a.config.Appearance.TechnicalMode {
		detail = "Weights " + score.WeightSummary
	}
	c.text(detail, RECT{r.Left + c.s(83), r.Top + c.s(49), r.Right - c.s(128), r.Bottom - c.s(8)}, 7, 450, palette.Muted, DT_LEFT|DT_VCENTER|DT_SINGLELINE|DT_END_ELLIPSIS)
	components := []struct {
		name  string
		value int
		color uint32
	}{{"Response", score.Responsiveness, palette.Cyan}, {"Smooth", score.Smoothness, palette.Violet}, {"Headroom", score.Headroom, palette.Blue}, {"Stable", score.Stability, palette.Green}, {"Network", score.Connectivity, palette.Amber}}
	x := r.Right - c.s(116)
	y := r.Top + c.s(6)
	for _, component := range components {
		c.text(component.name, RECT{x, y, x + c.s(62), y + c.s(15)}, 6, 550, palette.Muted2, DT_LEFT|DT_VCENTER|DT_SINGLELINE)
		track := RECT{x + c.s(64), y + c.s(5), r.Right - c.s(14), y + c.s(10)}
		c.roundedFill(track, 3, palette.Grid)
		fill := track
		fill.Right = fill.Left + int32(component.value)*(track.Right-track.Left)/100
		c.roundedFill(fill, 3, component.color)
		y += c.s(15)
	}
}

func (a *App) drawLiveSignature(c *Canvas, r RECT) {
	caption, captionColor := a.signatureCaption()
	c.text("Live signature", RECT{r.Left + c.s(16), r.Top + c.s(8), r.Right - c.s(16), r.Top + c.s(28)}, 7, 650, palette.Muted2, DT_LEFT|DT_VCENTER|DT_SINGLELINE)
	c.text(caption, RECT{r.Left + c.s(16), r.Top + c.s(25), r.Right - c.s(16), r.Top + c.s(45)}, 8, 600, captionColor, DT_LEFT|DT_VCENTER|DT_SINGLELINE|DT_END_ELLIPSIS)
	history := a.graphDisplayHistory()
	if len(history) == 0 {
		return
	}
	start := len(history) - 46
	if start < 0 {
		start = 0
	}
	history = history[start:]
	plot := RECT{r.Left + c.s(16), r.Top + c.s(49), r.Right - c.s(16), r.Bottom - c.s(11)}
	center := (plot.Top + plot.Bottom) / 2
	c.line(plot.Left, center, plot.Right, center, palette.Grid, 1)
	peakNetwork := math.Max(1, math.Max(a.snapshot.Network.PeakDown, a.snapshot.Network.PeakUp))
	for i, sample := range history {
		network := math.Max(sample.Down, sample.Up) / peakNetwork * 100
		values := []float64{sample.CPU, sample.GPU, sample.Disk, network}
		colors := []uint32{palette.Blue, palette.Violet, palette.Amber, palette.Green}
		activity, color := values[0], colors[0]
		for j := 1; j < len(values); j++ {
			if values[j] > activity {
				activity, color = values[j], colors[j]
			}
		}
		activity = clampFloat(activity, 0, 100)
		x := plot.Left
		if len(history) > 1 {
			x += int32(i) * (plot.Right - plot.Left) / int32(len(history)-1)
		}
		half := c.s(1) + int32(activity/100*float64(maxInt(1, int((plot.Bottom-plot.Top)/2-c.s(1)))))
		c.line(x, center-half, x, center+half, blend(palette.Muted2, color, 0.72), 2)
	}
}

func (a *App) signatureCaption() (string, uint32) {
	items := []struct {
		name  string
		value float64
		color uint32
	}{
		{"Processor is leading", a.display.CPU, palette.Blue},
		{"Graphics is leading", a.display.GPU, palette.Violet},
		{"Storage is leading", a.display.Disk, palette.Amber},
		{"Network is leading", a.snapshot.Network.Utilization, palette.Green},
		{"Memory pressure is leading", math.Max(0, (a.display.Memory-55)*2.2), palette.Cyan},
	}
	best := items[0]
	for _, item := range items[1:] {
		if item.value > best.value {
			best = item
		}
	}
	if best.value < 8 {
		return "Quiet and balanced", palette.Green
	}
	return best.name, best.color
}

func (a *App) metricCard(c *Canvas, r RECT, title, value, sub string, color uint32, key string, current float64) {
	hover := a.pointIn(r)
	bg := palette.Card
	if hover {
		bg = palette.CardHover
	}
	c.rounded(r, 13, bg)
	c.strokeRound(r, 13, palette.Border, 1)
	c.rounded(RECT{r.Left + c.s(13), r.Top + c.s(15), r.Left + c.s(16), r.Top + c.s(39)}, 3, color)
	c.text(title, RECT{r.Left + c.s(25), r.Top + c.s(10), r.Right - c.s(12), r.Top + c.s(35)}, 8, 700, palette.Muted, DT_LEFT|DT_VCENTER|DT_SINGLELINE)
	c.mono(value, RECT{r.Left + c.s(15), r.Top + c.s(36), r.Right - c.s(13), r.Top + c.s(67)}, 16, 650, palette.Text, DT_LEFT|DT_VCENTER|DT_SINGLELINE|DT_END_ELLIPSIS)
	c.text(sub, RECT{r.Left + c.s(15), r.Top + c.s(70), r.Right - c.s(13), r.Bottom - c.s(8)}, 8, 450, palette.Muted, DT_LEFT|DT_VCENTER|DT_SINGLELINE|DT_END_ELLIPSIS)
	a.sparkline(c, RECT{r.Right - c.s(78), r.Top + c.s(13), r.Right - c.s(13), r.Top + c.s(34)}, key, color, current)
}

type graphSeries struct {
	Name  string
	Color uint32
	Value func(HistorySample) float64
}

func nearestPolylineSample(points []POINT, pointer POINT) (int, float64, bool) {
	if len(points) == 0 {
		return 0, 0, false
	}
	if len(points) == 1 {
		distance := math.Hypot(float64(pointer.X-points[0].X), float64(pointer.Y-points[0].Y))
		return 0, distance, true
	}
	bestIndex, bestDistance := 0, math.Inf(1)
	px, py := float64(pointer.X), float64(pointer.Y)
	for i := 1; i < len(points); i++ {
		x1, y1 := float64(points[i-1].X), float64(points[i-1].Y)
		x2, y2 := float64(points[i].X), float64(points[i].Y)
		dx, dy := x2-x1, y2-y1
		t := 0.0
		if lengthSquared := dx*dx + dy*dy; lengthSquared > 0 {
			t = ((px-x1)*dx + (py-y1)*dy) / lengthSquared
			t = clampFloat(t, 0, 1)
		}
		distance := math.Hypot(px-(x1+t*dx), py-(y1+t*dy))
		if distance < bestDistance {
			bestDistance = distance
			bestIndex = i - 1
			if t >= 0.5 {
				bestIndex = i
			}
		}
	}
	return bestIndex, bestDistance, true
}

func graphTooltipRect(c *Canvas, plot RECT, anchor POINT, width, height int32) RECT {
	w, h := c.s(width), c.s(height)
	x, y := anchor.X+c.s(12), anchor.Y-h-c.s(12)
	if x+w > plot.Right {
		x = anchor.X - w - c.s(12)
	}
	if x < plot.Left {
		x = plot.Left
	}
	if y < plot.Top {
		y = anchor.Y + c.s(12)
	}
	if y+h > plot.Bottom {
		y = plot.Bottom - h
	}
	return RECT{x, y, x + w, y + h}
}

func (a *App) drawMultiGraph(c *Canvas, r RECT, title string, series []graphSeries, fixedMax float64, key string) {
	c.rounded(r, 14, palette.Surface)
	c.strokeRound(r, 14, palette.Border, 1)
	c.text(title, RECT{r.Left + c.s(16), r.Top + c.s(8), r.Left + c.s(215), r.Top + c.s(32)}, 8, 700, palette.Muted, DT_LEFT|DT_VCENTER|DT_SINGLELINE|DT_END_ELLIPSIS)
	if a.graphInspectionPaused {
		a.drawGraphPauseControl(c, r)
	} else if _, focused := a.graphRanges[key]; focused {
		reset := RECT{r.Left + c.s(225), r.Top + c.s(7), r.Left + c.s(319), r.Top + c.s(32)}
		a.button(c, reset, "Reset range", "graph-reset:"+key, 0, false)
	} else {
		c.text("DRAG TO FOCUS", RECT{r.Left + c.s(225), r.Top + c.s(8), r.Left + c.s(335), r.Top + c.s(32)}, 7, 650, palette.Muted2, DT_LEFT|DT_VCENTER|DT_SINGLELINE)
	}
	lx := r.Right - c.s(16)
	for i := len(series) - 1; i >= 0; i-- {
		lx -= c.s(62)
		c.circle(lx, r.Top+c.s(20), c.s(3), series[i].Color)
		c.text(series[i].Name, RECT{lx + c.s(7), r.Top + c.s(8), lx + c.s(58), r.Top + c.s(32)}, 7, 600, palette.Muted, DT_LEFT|DT_VCENTER|DT_SINGLELINE)
	}
	plot := RECT{r.Left + c.s(16), r.Top + c.s(39), r.Right - c.s(16), r.Bottom - c.s(16)}
	if plot.Bottom <= plot.Top {
		return
	}
	for i := 0; i <= 4; i++ {
		y := plot.Top + int32(i)*(plot.Bottom-plot.Top)/4
		c.line(plot.Left, y, plot.Right, y, palette.Grid, 1)
	}
	history := a.graphDisplayHistory()
	now := a.graphDisplayNow()
	start, end := now.Add(-time.Duration(a.config.Appearance.GraphSeconds)*time.Second), now
	if focused, ok := a.graphRanges[key]; ok && focused.End.After(focused.Start) {
		start, end = focused.Start, focused.End
	}
	a.graphPlots = append(a.graphPlots, GraphPlotRegion{Rect: plot, Key: key, Start: start, End: end})
	filtered := make([]HistorySample, 0, len(history))
	for _, h := range history {
		if !h.At.Before(start) && !h.At.After(end) {
			filtered = append(filtered, h)
		}
	}
	if len(filtered) < 2 {
		c.text("Collecting telemetry…", plot, 10, 500, palette.Muted, DT_CENTER|DT_VCENTER|DT_SINGLELINE)
		return
	}
	maxV := fixedMax
	if maxV <= 0 {
		peak := 0.0
		for _, h := range filtered {
			for _, s := range series {
				peak = math.Max(peak, s.Value(h))
			}
		}
		sc := a.scalers[key]
		if sc == nil {
			sc = &core.GraphScaler{}
			a.scalers[key] = sc
		}
		maxV = sc.Update(peak, a.graphDisplayNow())
	}
	if a.graphDragKey == key {
		x1, x2 := a.graphDragStart.X, a.graphDragCurrent.X
		if x1 > x2 {
			x1, x2 = x2, x1
		}
		if x1 < plot.Left {
			x1 = plot.Left
		}
		if x2 > plot.Right {
			x2 = plot.Right
		}
		selection := RECT{x1, plot.Top, x2, plot.Bottom}
		c.fill(selection, blend(palette.Surface, palette.Cyan, 0.12))
		c.line(x1, plot.Top, x1, plot.Bottom, palette.Cyan, 1)
		c.line(x2, plot.Top, x2, plot.Bottom, palette.Cyan, 1)
	}
	type hoverCandidate struct {
		series   graphSeries
		sample   HistorySample
		point    POINT
		distance float64
	}
	var hovered *hoverCandidate
	for _, s := range series {
		pts := make([]POINT, 0, len(filtered))
		times := make([]time.Time, 0, len(filtered))
		values := make([]float64, 0, len(filtered))
		for _, h := range filtered {
			x := plot.Left + int32(core.MapTime(h.At, start, end, float64(plot.Right-plot.Left)))
			v := clampFloat(s.Value(h), 0, maxV)
			y := plot.Bottom - int32(v/maxV*float64(plot.Bottom-plot.Top))
			pts = append(pts, POINT{x, y})
			times = append(times, h.At)
			values = append(values, s.Value(h))
		}
		c.polyline(pts, s.Color, 2)
		a.graphLines = append(a.graphLines, GraphRenderedSeries{Key: key, Series: s.Name, Color: s.Color, Points: pts, Times: times, Values: values})
		if a.pointIn(plot) && a.graphDragKey == "" {
			if index, distance, ok := nearestPolylineSample(pts, a.hover); ok && distance <= float64(c.s(7)) && (hovered == nil || distance < hovered.distance) {
				hovered = &hoverCandidate{series: s, sample: filtered[index], point: pts[index], distance: distance}
			}
		}
	}
	if hovered != nil {
		a.graphHover = GraphSampleSelection{Key: key, Series: hovered.series.Name, At: hovered.sample.At, Point: hovered.point, Value: hovered.series.Value(hovered.sample), Color: hovered.series.Color}
	}
	if pinned := a.graphPinned; pinned.Key == key && !pinned.Process && !pinned.At.Before(start) && !pinned.At.After(end) {
		pinned.Point.X = plot.Left + int32(core.MapTime(pinned.At, start, end, float64(plot.Right-plot.Left)))
		pinned.Point.Y = plot.Bottom - int32(clampFloat(pinned.Value, 0, maxV)/maxV*float64(plot.Bottom-plot.Top))
		a.drawGraphBreakdown(c, plot, pinned)
	} else if hovered != nil {
		c.circle(hovered.point.X, hovered.point.Y, c.s(5), palette.CardHover)
		c.circle(hovered.point.X, hovered.point.Y, c.s(3), hovered.series.Color)
		tip := graphTooltipRect(c, plot, hovered.point, 160, 52)
		c.rounded(tip, 9, palette.CardHover)
		c.strokeRound(tip, 9, palette.Border, 1)
		c.mono(hovered.sample.At.Format("15:04:05"), RECT{tip.Left + c.s(11), tip.Top + c.s(4), tip.Right - c.s(8), tip.Top + c.s(23)}, 7, 550, palette.Muted2, DT_LEFT|DT_VCENTER|DT_SINGLELINE)
		value := hovered.series.Value(hovered.sample)
		label := fmt.Sprintf("%.1f", value)
		if key == "network" || key == "hist3" {
			label = core.FormatRate(value, a.config.Network.Units)
		} else if fixedMax == 100 {
			label = fmt.Sprintf("%.1f%%", value)
		}
		c.circle(tip.Left+c.s(12), tip.Top+c.s(35), c.s(3), hovered.series.Color)
		c.text(hovered.series.Name, RECT{tip.Left + c.s(22), tip.Top + c.s(25), tip.Right - c.s(64), tip.Bottom - c.s(5)}, 8, 600, palette.Text, DT_LEFT|DT_VCENTER|DT_SINGLELINE|DT_END_ELLIPSIS)
		c.mono(label, RECT{tip.Right - c.s(67), tip.Top + c.s(25), tip.Right - c.s(10), tip.Bottom - c.s(5)}, 8, 600, palette.Text, DT_RIGHT|DT_VCENTER|DT_SINGLELINE)
	}
}

func (a *App) drawGraphPauseControl(c *Canvas, r RECT) {
	badge := RECT{r.Left + c.s(225), r.Top + c.s(7), r.Left + c.s(362), r.Top + c.s(32)}
	c.roundedFill(badge, 13, blend(palette.Surface, palette.Amber, 0.12))
	c.circle(badge.Left+c.s(12), (badge.Top+badge.Bottom)/2, c.s(3), palette.Amber)
	c.text("INSPECTION PAUSED", RECT{badge.Left + c.s(21), badge.Top, badge.Right - c.s(7), badge.Bottom}, 6, 720, palette.Amber, DT_LEFT|DT_VCENTER|DT_SINGLELINE)
	resume := RECT{badge.Right + c.s(7), badge.Top, badge.Right + c.s(101), badge.Bottom}
	a.button(c, resume, "Resume live", "graph-resume", 0, false)
}

func (a *App) sparkline(c *Canvas, r RECT, key string, color uint32, current float64) {
	history := a.graphDisplayHistory()
	if len(history) < 2 {
		return
	}
	maxV := 100.0
	fn := func(h HistorySample) float64 { return h.CPU }
	switch key {
	case "gpu":
		fn = func(h HistorySample) float64 { return h.GPU }
	case "memory":
		fn = func(h HistorySample) float64 { return h.Memory }
	case "storage":
		fn = func(h HistorySample) float64 { return h.Disk }
	case "network":
		fn = func(h HistorySample) float64 { return h.Down }
		maxV = math.Max(1, a.snapshot.Network.PeakDown)
	case "latency":
		fn = func(h HistorySample) float64 { return h.Latency }
		maxV = math.Max(100, current*1.5)
	}
	start := len(history) - 40
	if start < 0 {
		start = 0
	}
	pts := make([]POINT, 0, len(history)-start)
	for i, h := range history[start:] {
		x := r.Left + int32(i)*(r.Right-r.Left)/int32(maxInt(1, len(history[start:])-1))
		y := r.Bottom - int32(clampFloat(fn(h), 0, maxV)/maxV*float64(r.Bottom-r.Top))
		pts = append(pts, POINT{x, y})
	}
	c.polyline(pts, color, 1)
}

func (a *App) drawComponentGraph(c *Canvas, r RECT, title, key string, total []graphSeries, fixedMax float64) {
	if a.componentBreakdown[key] {
		a.drawProcessBreakdownGraph(c, r, title+" · Top applications", key)
	} else {
		a.drawMultiGraph(c, r, title, total, fixedMax, key)
	}
	control := RECT{r.Right - c.s(202), r.Top + c.s(7), r.Right - c.s(16), r.Top + c.s(33)}
	c.roundedFill(control, 16, palette.Card)
	mid := (control.Left + control.Right) / 2
	totalR, appsR := RECT{control.Left, control.Top, mid, control.Bottom}, RECT{mid, control.Top, control.Right, control.Bottom}
	if !a.componentBreakdown[key] {
		c.roundedFill(totalR, 16, palette.CardHover)
	} else {
		c.roundedFill(appsR, 16, palette.CardHover)
	}
	c.text("Total", totalR, 7, 650, func() uint32 {
		if !a.componentBreakdown[key] {
			return palette.Text
		}
		return palette.Muted
	}(), DT_CENTER|DT_VCENTER|DT_SINGLELINE)
	c.text("Top apps", appsR, 7, 650, func() uint32 {
		if a.componentBreakdown[key] {
			return palette.Text
		}
		return palette.Muted
	}(), DT_CENTER|DT_VCENTER|DT_SINGLELINE)
	a.hit(totalR, "breakdown-set:"+key, 0)
	a.hit(appsR, "breakdown-set:"+key, 1)
}

func processHistoryValue(sample ProcessHistorySample, key string) float64 {
	switch key {
	case "memory":
		return float64(sample.WorkingSet)
	case "disk":
		return sample.ReadBps + sample.WriteBps
	default:
		return sample.CPU
	}
}

func currentProcessValue(process ProcessMetric, key string) float64 {
	switch key {
	case "memory":
		return float64(process.WorkingSet)
	case "disk":
		return process.ReadBps + process.WriteBps
	default:
		return process.CPU
	}
}

func processHistoryLabel(value float64, key string) string {
	if key == "memory" {
		return core.FormatBytes(uint64(math.Max(0, value)))
	}
	if key == "disk" {
		return core.FormatRate(value, core.UnitBytes)
	}
	return fmt.Sprintf("%.1f%%", value)
}

type graphContributor struct {
	Name  string
	Value float64
}

func (a *App) graphContributors(at time.Time, key string, limit int) []graphContributor {
	if key == "overview" {
		return nil
	}
	if key != "cpu" && key != "memory" && key != "disk" {
		return nil
	}
	type candidate struct {
		sample ProcessHistorySample
		delta  time.Duration
	}
	closest := make(map[uint32]candidate)
	for _, sample := range a.graphDisplayProcessHistory() {
		delta := sample.At.Sub(at)
		if delta < 0 {
			delta = -delta
		}
		if delta > 2*time.Second {
			continue
		}
		if current, ok := closest[sample.PID]; !ok || delta < current.delta {
			closest[sample.PID] = candidate{sample: sample, delta: delta}
		}
	}
	result := make([]graphContributor, 0, len(closest))
	for _, item := range closest {
		value := processHistoryValue(item.sample, key)
		if value > 0 {
			result = append(result, graphContributor{Name: item.sample.Name, Value: value})
		}
	}
	sort.SliceStable(result, func(i, j int) bool { return result[i].Value > result[j].Value })
	if len(result) > limit {
		result = result[:limit]
	}
	return result
}

func (a *App) drawGraphBreakdown(c *Canvas, plot RECT, pinned GraphSampleSelection) {
	c.line(pinned.Point.X, plot.Top, pinned.Point.X, plot.Bottom, blend(palette.Grid, pinned.Color, 0.72), 1)
	c.circle(pinned.Point.X, pinned.Point.Y, c.s(6), palette.CardHover)
	c.circle(pinned.Point.X, pinned.Point.Y, c.s(3), pinned.Color)

	metricKey := pinned.Key
	if pinned.Process {
		if split := strings.LastIndex(pinned.Key, "-"); split >= 0 && split+1 < len(pinned.Key) {
			metricKey = pinned.Key[split+1:]
		}
	}
	if metricKey == "overview" {
		switch strings.ToLower(pinned.Series) {
		case "cpu":
			metricKey = "cpu"
		case "memory":
			metricKey = "memory"
		default:
			metricKey = "gpu"
		}
	}
	contributors := a.graphContributors(pinned.At, metricKey, 3)
	height := int32(142)
	cardW := c.s(326)
	card := RECT{plot.Right - cardW - c.s(12), plot.Top + c.s(12), plot.Right - c.s(12), plot.Top + c.s(12+height)}
	c.roundedFill(card, 14, palette.CardHover)
	c.strokeRound(card, 14, palette.Border, 1)
	c.circle(card.Left+c.s(17), card.Top+c.s(17), c.s(3), palette.Amber)
	c.text("INSPECTION PAUSED", RECT{card.Left + c.s(27), card.Top + c.s(5), card.Right - c.s(110), card.Top + c.s(29)}, 7, 720, palette.Amber, DT_LEFT|DT_VCENTER|DT_SINGLELINE)
	a.button(c, RECT{card.Right - c.s(105), card.Top + c.s(5), card.Right - c.s(10), card.Top + c.s(30)}, "Resume live", "graph-resume", 0, false)
	c.text("WHY THIS SAMPLE", RECT{card.Left + c.s(14), card.Top + c.s(31), card.Right - c.s(118), card.Top + c.s(49)}, 6, 680, pinned.Color, DT_LEFT|DT_VCENTER|DT_SINGLELINE)
	c.mono(pinned.At.Format("15:04:05.000"), RECT{card.Right - c.s(118), card.Top + c.s(31), card.Right - c.s(14), card.Top + c.s(49)}, 6, 550, palette.Muted2, DT_RIGHT|DT_VCENTER|DT_SINGLELINE)
	valueLabel := fmt.Sprintf("%.1f", pinned.Value)
	if pinned.Process {
		valueLabel = processHistoryLabel(pinned.Value, metricKey)
	} else if metricKey == "cpu" || metricKey == "gpu" || metricKey == "memory" || metricKey == "disk" {
		valueLabel = fmt.Sprintf("%.1f%%", pinned.Value)
	} else if pinned.Key == "network" || pinned.Key == "hist3" {
		valueLabel = core.FormatRate(pinned.Value, a.config.Network.Units)
	}
	c.text(pinned.Series, RECT{card.Left + c.s(14), card.Top + c.s(49), card.Right - c.s(112), card.Top + c.s(73)}, 10, 670, palette.Text, DT_LEFT|DT_VCENTER|DT_SINGLELINE|DT_END_ELLIPSIS)
	c.mono(valueLabel, RECT{card.Right - c.s(110), card.Top + c.s(49), card.Right - c.s(14), card.Top + c.s(73)}, 9, 650, palette.Text, DT_RIGHT|DT_VCENTER|DT_SINGLELINE)
	if len(contributors) > 0 {
		c.text("Closest process samples", RECT{card.Left + c.s(14), card.Top + c.s(73), card.Right - c.s(14), card.Top + c.s(91)}, 6, 600, palette.Muted2, DT_LEFT|DT_VCENTER|DT_SINGLELINE)
		y := card.Top + c.s(91)
		for i, contributor := range contributors {
			c.circle(card.Left+c.s(17), y+c.s(8), c.s(2), []uint32{palette.Blue, palette.Violet, palette.Cyan}[i])
			c.text(contributor.Name, RECT{card.Left + c.s(25), y, card.Right - c.s(100), y + c.s(17)}, 7, 560, palette.Text, DT_LEFT|DT_VCENTER|DT_SINGLELINE|DT_END_ELLIPSIS)
			c.mono(processHistoryLabel(contributor.Value, metricKey), RECT{card.Right - c.s(98), y, card.Right - c.s(14), y + c.s(17)}, 7, 560, palette.Muted, DT_RIGHT|DT_VCENTER|DT_SINGLELINE)
			y += c.s(17)
		}
		return
	}
	context := "Windows does not expose per-process attribution for this provider."
	if pinned.Process {
		context = "This sample is already isolated to " + pinned.Series + "; the system readings below give it context."
	} else if metricKey == "gpu" {
		context = "Per-application GPU attribution is unavailable from the active Windows provider."
	} else if metricKey == "network" || pinned.Key == "network" {
		context = "Adapter counters show the total, but not which process owned each byte at this instant."
	}
	c.text(context, RECT{card.Left + c.s(14), card.Top + c.s(77), card.Right - c.s(14), card.Top + c.s(110)}, 7, 450, palette.Muted, DT_LEFT|DT_WORDBREAK|DT_END_ELLIPSIS)
	nearest := HistorySample{}
	best := time.Duration(1<<63 - 1)
	for _, sample := range a.graphDisplayHistory() {
		delta := sample.At.Sub(pinned.At)
		if delta < 0 {
			delta = -delta
		}
		if delta < best {
			best, nearest = delta, sample
		}
	}
	c.mono(fmt.Sprintf("CPU %.0f%% · GPU %.0f%% · memory %.0f%% · disk %.0f%%", nearest.CPU, nearest.GPU, nearest.Memory, nearest.Disk), RECT{card.Left + c.s(14), card.Top + c.s(115), card.Right - c.s(14), card.Bottom - c.s(9)}, 6, 560, palette.Muted2, DT_LEFT|DT_VCENTER|DT_SINGLELINE|DT_END_ELLIPSIS)
}

func (a *App) drawProcessBreakdownGraph(c *Canvas, r RECT, title, key string) {
	c.rounded(r, 14, palette.Surface)
	c.strokeRound(r, 14, palette.Border, 1)
	c.text(title, RECT{r.Left + c.s(16), r.Top + c.s(8), r.Right - c.s(220), r.Top + c.s(32)}, 8, 700, palette.Muted, DT_LEFT|DT_VCENTER|DT_SINGLELINE|DT_END_ELLIPSIS)
	if a.graphInspectionPaused {
		a.drawGraphPauseControl(c, r)
	}
	plot := RECT{r.Left + c.s(16), r.Top + c.s(42), r.Right - c.s(16), r.Bottom - c.s(43)}
	if plot.Bottom <= plot.Top {
		return
	}
	for i := 0; i <= 4; i++ {
		y := plot.Top + int32(i)*(plot.Bottom-plot.Top)/4
		c.line(plot.Left, y, plot.Right, y, palette.Grid, 1)
	}
	now := a.graphDisplayNow()
	start, end := now.Add(-time.Duration(a.config.Appearance.GraphSeconds)*time.Second), now
	if focused, ok := a.graphRanges[key]; ok && focused.End.After(focused.Start) {
		start, end = focused.Start, focused.End
	}
	a.graphPlots = append(a.graphPlots, GraphPlotRegion{Rect: plot, Key: key, Start: start, End: end})

	processes := append([]ProcessMetric(nil), a.graphDisplayProcesses()...)
	sort.SliceStable(processes, func(i, j int) bool {
		return currentProcessValue(processes[i], key) > currentProcessValue(processes[j], key)
	})
	if len(processes) > 5 {
		processes = processes[:5]
	}
	colors := []uint32{palette.Blue, palette.Violet, palette.Cyan, palette.Amber, palette.Green}
	selected := make(map[uint32]int, len(processes))
	for i, process := range processes {
		selected[process.PID] = i
	}
	points := make([][]ProcessHistorySample, len(processes))
	maxValue := 0.0
	for _, sample := range a.graphDisplayProcessHistory() {
		index, ok := selected[sample.PID]
		if !ok || sample.At.Before(start) || sample.At.After(end) {
			continue
		}
		points[index] = append(points[index], sample)
		maxValue = math.Max(maxValue, processHistoryValue(sample, key))
	}
	if key == "cpu" {
		maxValue = 100
	} else {
		maxValue *= 1.1
		if maxValue <= 0 {
			maxValue = 1
		}
	}
	if a.graphDragKey == key {
		x1, x2 := a.graphDragStart.X, a.graphDragCurrent.X
		if x1 > x2 {
			x1, x2 = x2, x1
		}
		if x1 < plot.Left {
			x1 = plot.Left
		}
		if x2 > plot.Right {
			x2 = plot.Right
		}
		c.fill(RECT{x1, plot.Top, x2, plot.Bottom}, blend(palette.Surface, palette.Cyan, 0.12))
		c.line(x1, plot.Top, x1, plot.Bottom, palette.Cyan, 1)
		c.line(x2, plot.Top, x2, plot.Bottom, palette.Cyan, 1)
	}
	drawn := 0
	type processHoverCandidate struct {
		process  ProcessMetric
		sample   ProcessHistorySample
		point    POINT
		color    uint32
		distance float64
	}
	var hovered *processHoverCandidate
	for i, samples := range points {
		if len(samples) < 2 {
			continue
		}
		pts := make([]POINT, 0, len(samples))
		times := make([]time.Time, 0, len(samples))
		values := make([]float64, 0, len(samples))
		for _, sample := range samples {
			x := plot.Left + int32(core.MapTime(sample.At, start, end, float64(plot.Right-plot.Left)))
			y := plot.Bottom - int32(clampFloat(processHistoryValue(sample, key), 0, maxValue)/maxValue*float64(plot.Bottom-plot.Top))
			pts = append(pts, POINT{x, y})
			times = append(times, sample.At)
			values = append(values, processHistoryValue(sample, key))
		}
		c.polyline(pts, colors[i], 2)
		a.graphLines = append(a.graphLines, GraphRenderedSeries{Key: key, Series: processes[i].Name, Color: colors[i], Process: true, Points: pts, Times: times, Values: values})
		if a.pointIn(plot) && a.graphDragKey == "" {
			if index, distance, ok := nearestPolylineSample(pts, a.hover); ok && distance <= float64(c.s(7)) && (hovered == nil || distance < hovered.distance) {
				hovered = &processHoverCandidate{process: processes[i], sample: samples[index], point: pts[index], color: colors[i], distance: distance}
			}
		}
		drawn++
	}
	if drawn == 0 {
		c.text("Collecting per-application history…", plot, 10, 500, palette.Muted, DT_CENTER|DT_VCENTER|DT_SINGLELINE)
	}
	if hovered != nil {
		a.graphHover = GraphSampleSelection{Key: key, Series: hovered.process.Name, At: hovered.sample.At, Point: hovered.point, Value: processHistoryValue(hovered.sample, key), Color: hovered.color, Process: true}
	}
	if pinned := a.graphPinned; pinned.Key == key && pinned.Process && !pinned.At.Before(start) && !pinned.At.After(end) {
		pinned.Point.X = plot.Left + int32(core.MapTime(pinned.At, start, end, float64(plot.Right-plot.Left)))
		pinned.Point.Y = plot.Bottom - int32(clampFloat(pinned.Value, 0, maxValue)/maxValue*float64(plot.Bottom-plot.Top))
		a.drawGraphBreakdown(c, plot, pinned)
	} else if hovered != nil {
		c.circle(hovered.point.X, hovered.point.Y, c.s(5), palette.CardHover)
		c.circle(hovered.point.X, hovered.point.Y, c.s(3), hovered.color)
		tip := graphTooltipRect(c, plot, hovered.point, 190, 54)
		c.roundedFill(tip, 12, palette.CardHover)
		c.strokeRound(tip, 12, palette.Border, 1)
		c.mono(hovered.sample.At.Format("15:04:05"), RECT{tip.Left + c.s(11), tip.Top + c.s(4), tip.Right - c.s(8), tip.Top + c.s(23)}, 7, 550, palette.Muted2, DT_LEFT|DT_VCENTER|DT_SINGLELINE)
		c.circle(tip.Left+c.s(12), tip.Top+c.s(37), c.s(3), hovered.color)
		c.text(hovered.process.Name, RECT{tip.Left + c.s(22), tip.Top + c.s(25), tip.Right - c.s(76), tip.Bottom - c.s(4)}, 8, 600, palette.Text, DT_LEFT|DT_VCENTER|DT_SINGLELINE|DT_END_ELLIPSIS)
		c.mono(processHistoryLabel(processHistoryValue(hovered.sample, key), key), RECT{tip.Right - c.s(78), tip.Top + c.s(25), tip.Right - c.s(10), tip.Bottom - c.s(4)}, 8, 600, palette.Text, DT_RIGHT|DT_VCENTER|DT_SINGLELINE)
	}
	legendTop := r.Bottom - c.s(35)
	legendW := (r.Right - r.Left - c.s(32)) / int32(maxInt(1, len(processes)))
	for i, process := range processes {
		x := r.Left + c.s(16) + int32(i)*legendW
		c.circle(x+c.s(3), legendTop+c.s(12), c.s(3), colors[i])
		c.text(process.Name, RECT{x + c.s(12), legendTop, x + legendW - c.s(5), legendTop + c.s(24)}, 7, 550, palette.Muted, DT_LEFT|DT_VCENTER|DT_SINGLELINE|DT_END_ELLIPSIS)
	}
}

func (a *App) drawCPU(c *Canvas, r RECT) {
	stats := []stat{{"Utilisation", fmt.Sprintf("%.1f%%", a.display.CPU), "Total processor activity"}, {"Kernel activity", fmt.Sprintf("%.1f%%", a.snapshot.CPU.Kernel), "Windows and driver work"}, {"Current clock", fmt.Sprintf("%.0f MHz", a.snapshot.CPU.FrequencyMHz), fmt.Sprintf("Maximum %.0f MHz", a.snapshot.CPU.MaxMHz)}, {"Topology", fmt.Sprintf("%d cores · %d logical", a.snapshot.CPU.Cores, a.snapshot.CPU.Logical), a.snapshot.CPU.Model}}
	a.drawStats(c, r, stats, func(gr RECT) {
		a.drawComponentGraph(c, gr, "Processor activity", "cpu", []graphSeries{{"Total", palette.Blue, func(h HistorySample) float64 { return h.CPU }}}, 100)
	}, []kv{{"Processes", fmt.Sprint(a.snapshot.ProcessCount)}, {"Threads", fmt.Sprint(a.snapshot.ThreadCount)}, {"Handles", fmt.Sprint(a.snapshot.HandleCount)}, {"Uptime", formatDuration(time.Since(a.snapshot.System.BootTime))}})
}
func (a *App) drawGPU(c *Canvas, r RECT) {
	avail := "Available"
	if !a.snapshot.GPU.Available {
		avail = "Unavailable on this driver/provider"
	}
	stats := []stat{{"GPU activity", availabilityPercent(a.snapshot.GPU.Available, a.display.GPU), avail}, {"Dedicated VRAM", core.FormatBytes(uint64(a.snapshot.GPU.DedicatedUsed)), "Allocated by Windows GPU clients"}, {"Shared memory", core.FormatBytes(uint64(a.snapshot.GPU.SharedUsed)), "System memory visible to GPU"}, {"Temperature", "Unavailable", "No reliable vendor-neutral Windows sensor"}}
	a.drawStats(c, r, stats, func(gr RECT) {
		a.drawMultiGraph(c, gr, "Graphics engine activity", []graphSeries{{"GPU", palette.Violet, func(h HistorySample) float64 { return h.GPU }}}, 100, "gpu")
	}, []kv{{"Adapter", a.snapshot.GPU.Model}, {"Provider", a.snapshot.GPU.Provider}, {"Sensors", "Shown only when reliable"}, {"Per-process GPU", "Provider not enabled"}})
}
func (a *App) drawMemory(c *Canvas, r RECT) {
	stats := []stat{{"In use", core.FormatBytes(a.snapshot.Memory.Used), fmt.Sprintf("%.1f%% of physical RAM", a.display.Memory)}, {"Available", core.FormatBytes(a.snapshot.Memory.Available), "Immediately available to applications"}, {"Cached", core.FormatBytes(a.snapshot.Memory.Cached), "Reusable when applications need it"}, {"Committed", fmt.Sprintf("%s / %s", core.FormatBytes(a.snapshot.Memory.Commit), core.FormatBytes(a.snapshot.Memory.CommitLimit)), fmt.Sprintf("%.1f%% of commit limit", a.snapshot.Memory.CommitPercent)}}
	a.drawStats(c, r, stats, func(gr RECT) {
		a.drawComponentGraph(c, gr, "Physical memory", "memory", []graphSeries{{"Used", palette.Cyan, func(h HistorySample) float64 { return h.Memory }}}, 100)
	}, []kv{{"Installed", core.FormatBytes(a.snapshot.Memory.Total)}, {"Paged pool", core.FormatBytes(a.snapshot.Memory.PagedPool)}, {"Non-paged pool", core.FormatBytes(a.snapshot.Memory.NonPagedPool)}, {"Note", "Cached memory is healthy and remains available."}})
}
func (a *App) drawStorage(c *Canvas, r RECT) {
	stats := []stat{{"Active time", fmt.Sprintf("%.1f%%", a.display.Disk), "Performance activity, not used space"}, {"Read", shortBytes(a.snapshot.Disk.ReadBps) + "/s", "All physical disks"}, {"Write", shortBytes(a.snapshot.Disk.WriteBps) + "/s", "All physical disks"}, {"Latency", fmt.Sprintf("%.1f ms", a.snapshot.Disk.LatencyMs), fmt.Sprintf("Queue depth %.2f", a.snapshot.Disk.Queue)}}
	extra := []kv{{"System volume free", core.FormatBytes(a.snapshot.Disk.Free)}, {"System volume size", core.FormatBytes(a.snapshot.Disk.Total)}, {"Volumes", fmt.Sprint(len(a.snapshot.Disk.Volumes))}, {"Health sensors", "Unavailable without privileged/vendor provider"}}
	a.drawStats(c, r, stats, func(gr RECT) {
		a.drawComponentGraph(c, gr, "Disk activity", "disk", []graphSeries{{"Active", palette.Amber, func(h HistorySample) float64 { return h.Disk }}}, 100)
	}, extra)
}

type stat struct{ Title, Value, Sub string }
type kv struct{ Key, Value string }

func (a *App) drawStats(c *Canvas, r RECT, stats []stat, graph func(RECT), details []kv) {
	gap := c.s(10)
	w := (r.Right - r.Left - gap*3) / 4
	for i, s := range stats {
		cr := RECT{r.Left + int32(i)*(w+gap), r.Top, r.Left + int32(i)*(w+gap) + w, r.Top + c.s(92)}
		c.rounded(cr, 13, palette.Card)
		c.strokeRound(cr, 13, palette.Border, 1)
		c.text(s.Title, RECT{cr.Left + c.s(14), cr.Top + c.s(8), cr.Right - c.s(10), cr.Top + c.s(30)}, 8, 700, palette.Muted, DT_LEFT|DT_VCENTER|DT_SINGLELINE)
		c.mono(s.Value, RECT{cr.Left + c.s(14), cr.Top + c.s(32), cr.Right - c.s(10), cr.Top + c.s(60)}, 14, 650, palette.Text, DT_LEFT|DT_VCENTER|DT_SINGLELINE|DT_END_ELLIPSIS)
		c.text(s.Sub, RECT{cr.Left + c.s(14), cr.Top + c.s(63), cr.Right - c.s(10), cr.Bottom - c.s(7)}, 7, 450, palette.Muted, DT_LEFT|DT_VCENTER|DT_SINGLELINE|DT_END_ELLIPSIS)
	}
	detailsH := c.s(94)
	graphR := RECT{r.Left, r.Top + c.s(104), r.Right, r.Bottom - detailsH - c.s(10)}
	graph(graphR)
	detailR := RECT{r.Left, r.Bottom - detailsH, r.Right, r.Bottom}
	c.rounded(detailR, 13, palette.Card)
	c.strokeRound(detailR, 13, palette.Border, 1)
	colW := (detailR.Right - detailR.Left) / int32(maxInt(1, len(details)))
	for i, d := range details {
		x := detailR.Left + int32(i)*colW
		c.text(d.Key, RECT{x + c.s(14), detailR.Top + c.s(13), x + colW - c.s(10), detailR.Top + c.s(36)}, 8, 600, palette.Muted2, DT_LEFT|DT_VCENTER|DT_SINGLELINE)
		c.text(d.Value, RECT{x + c.s(14), detailR.Top + c.s(38), x + colW - c.s(10), detailR.Bottom - c.s(12)}, 9, 550, palette.Text, DT_LEFT|DT_VCENTER|DT_SINGLELINE|DT_END_ELLIPSIS)
	}
}

func (a *App) drawNetwork(c *Canvas, r RECT) {
	gap := c.s(10)
	topH := c.s(96)
	w := (r.Right - r.Left - gap*3) / 4
	now := time.Now()
	cards := []stat{{"Download", a.downFormatter.Format(a.display.Down, now), "Peak " + core.FormatRate(a.snapshot.Network.PeakDown, a.config.Network.Units)}, {"Upload", a.upFormatter.Format(a.display.Up, now), "Peak " + core.FormatRate(a.snapshot.Network.PeakUp, a.config.Network.Units)}, {"Latency", latencyLabel(a.snapshot.Network), fmt.Sprintf("%.1f ms jitter · %.0f%% loss", a.snapshot.Network.JitterMs, a.snapshot.Network.PacketLoss)}, {"Link load", fmt.Sprintf("%.2f%%", a.snapshot.Network.Utilization), linkLabel(a.snapshot.Network.LinkDown)}}
	for i, s := range cards {
		cr := RECT{r.Left + int32(i)*(w+gap), r.Top, r.Left + int32(i)*(w+gap) + w, r.Top + topH}
		c.rounded(cr, 13, palette.Card)
		c.strokeRound(cr, 13, palette.Border, 1)
		c.text(s.Title, RECT{cr.Left + c.s(14), cr.Top + c.s(8), cr.Right - c.s(10), cr.Top + c.s(30)}, 8, 700, palette.Muted, DT_LEFT|DT_VCENTER|DT_SINGLELINE)
		c.mono(s.Value, RECT{cr.Left + c.s(14), cr.Top + c.s(33), cr.Right - c.s(10), cr.Top + c.s(62)}, 13, 650, palette.Text, DT_LEFT|DT_VCENTER|DT_SINGLELINE|DT_END_ELLIPSIS)
		c.text(s.Sub, RECT{cr.Left + c.s(14), cr.Top + c.s(66), cr.Right - c.s(10), cr.Bottom - c.s(7)}, 7, 450, palette.Muted, DT_LEFT|DT_VCENTER|DT_SINGLELINE|DT_END_ELLIPSIS)
	}
	graph := RECT{r.Left, r.Top + topH + gap, r.Right, r.Bottom - c.s(116)}
	a.drawMultiGraph(c, graph, "Throughput", []graphSeries{{"Down", palette.Green, func(h HistorySample) float64 { return h.Down }}, {"Up", palette.Violet, func(h HistorySample) float64 { return h.Up }}}, 0, "network")
	details := RECT{r.Left, r.Bottom - c.s(106), r.Right, r.Bottom}
	c.rounded(details, 13, palette.Card)
	c.strokeRound(details, 13, palette.Border, 1)
	values := []kv{{"Adapter", fallback(a.snapshot.Network.Name, "No connected adapter")}, {"Type / address", a.snapshot.Network.Kind + " · " + a.snapshot.Network.IPv4}, {"Session", fmt.Sprintf("↓ %s  ↑ %s", core.FormatBytes(a.snapshot.Network.SessionDown), core.FormatBytes(a.snapshot.Network.SessionUp))}, {"Packets", fmt.Sprintf("%.0f ↓ / %.0f ↑ pps", a.snapshot.Network.RxPPS, a.snapshot.Network.TxPPS)}, {"MTU / errors", fmt.Sprintf("%d · %d errors · %d discards", a.snapshot.Network.MTU, a.snapshot.Network.Errors, a.snapshot.Network.Discards)}}
	cw := (details.Right - details.Left) / int32(len(values))
	for i, v := range values {
		x := details.Left + int32(i)*cw
		c.text(v.Key, RECT{x + c.s(12), details.Top + c.s(11), x + cw - c.s(8), details.Top + c.s(32)}, 8, 600, palette.Muted2, DT_LEFT|DT_VCENTER|DT_SINGLELINE)
		c.text(v.Value, RECT{x + c.s(12), details.Top + c.s(35), x + cw - c.s(8), details.Bottom - c.s(11)}, 8, 550, palette.Text, DT_LEFT|DT_VCENTER|DT_WORDBREAK|DT_END_ELLIPSIS)
	}
	reset := RECT{details.Right - c.s(108), details.Top + c.s(9), details.Right - c.s(10), details.Top + c.s(35)}
	a.button(c, reset, "Reset session", "reset-session", 0, false)
}

func (a *App) drawProcesses(c *Canvas, r RECT) {
	if a.selectedPID != 0 {
		for _, process := range a.snapshot.Processes {
			if process.PID == a.selectedPID {
				a.drawProcessDetail(c, r, process, true)
				return
			}
		}
		a.drawProcessDetail(c, r, ProcessMetric{PID: a.selectedPID, Name: "Process unavailable"}, false)
		return
	}
	search := RECT{r.Left, r.Top, r.Left + c.s(330), r.Top + c.s(42)}
	c.rounded(search, 11, palette.Card)
	c.strokeRound(search, 11, palette.Border, 1)
	label := "Type to filter processes…"
	col := palette.Muted2
	if a.search != "" {
		label = a.search + "  ×"
		col = palette.Text
	}
	c.text("⌕", RECT{search.Left + c.s(12), search.Top, search.Left + c.s(38), search.Bottom}, 13, 500, palette.Muted, DT_CENTER|DT_VCENTER|DT_SINGLELINE)
	c.text(label, RECT{search.Left + c.s(42), search.Top, search.Right - c.s(12), search.Bottom}, 9, 500, col, DT_LEFT|DT_VCENTER|DT_SINGLELINE|DT_END_ELLIPSIS)
	if a.search != "" {
		a.hit(search, "clear-search", 0)
	}
	toggle := RECT{r.Right - c.s(124), r.Top, r.Right, r.Top + c.s(42)}
	a.button(c, toggle, func() string {
		if a.processMap {
			return "List view"
		}
		return "Visual map"
	}(), "toggle-process-map", 0, a.processMap)
	body := RECT{r.Left, r.Top + c.s(54), r.Right, r.Bottom}
	filtered := make([]ProcessMetric, 0, len(a.snapshot.Processes))
	for _, p := range a.snapshot.Processes {
		if a.search == "" || strings.Contains(strings.ToLower(p.Name), strings.ToLower(a.search)) {
			filtered = append(filtered, p)
		}
	}
	if a.processMap {
		a.drawProcessMap(c, body, filtered)
	} else {
		a.drawProcessList(c, body, filtered)
	}
}

func (a *App) drawProcessList(c *Canvas, r RECT, items []ProcessMetric) {
	c.rounded(r, 13, palette.Surface)
	c.strokeRound(r, 13, palette.Border, 1)
	header := RECT{r.Left + c.s(12), r.Top + c.s(7), r.Right - c.s(12), r.Top + c.s(38)}
	cols := []struct {
		x     int32
		w     int32
		label string
		right bool
	}{{0, 42, "PID", false}, {48, 270, "Process", false}, {330, 90, "CPU", true}, {430, 120, "Memory", true}, {560, 120, "Disk R/W", true}, {700, 90, "Threads", true}, {800, 90, "Handles", true}}
	for _, col := range cols {
		flags := uint32(DT_LEFT | DT_VCENTER | DT_SINGLELINE)
		if col.right {
			flags = DT_RIGHT | DT_VCENTER | DT_SINGLELINE
		}
		c.text(col.label, RECT{header.Left + c.s(col.x), header.Top, header.Left + c.s(col.x+col.w), header.Bottom}, 7, 700, palette.Muted2, flags)
	}
	y := header.Bottom
	rowH := c.s(36)
	maxRows := int((r.Bottom - y - c.s(8)) / rowH)
	if maxRows > len(items) {
		maxRows = len(items)
	}
	for i := 0; i < maxRows; i++ {
		p := items[i]
		row := RECT{r.Left + c.s(8), y, r.Right - c.s(8), y + rowH}
		if a.pointIn(row) {
			c.rounded(row, 8, palette.CardHover)
		}
		a.hit(row, "process-detail", int(p.PID))
		c.mono(fmt.Sprint(p.PID), RECT{header.Left, y, header.Left + c.s(42), y + rowH}, 7, 500, palette.Muted, DT_LEFT|DT_VCENTER|DT_SINGLELINE)
		c.text(p.Name, RECT{header.Left + c.s(48), y, header.Left + c.s(318), y + rowH}, 9, 550, palette.Text, DT_LEFT|DT_VCENTER|DT_SINGLELINE|DT_END_ELLIPSIS)
		c.mono(fmt.Sprintf("%5.1f%%", p.CPU), RECT{header.Left + c.s(330), y, header.Left + c.s(420), y + rowH}, 8, 600, cpuColor(p.CPU), DT_RIGHT|DT_VCENTER|DT_SINGLELINE)
		c.mono(core.FormatBytes(p.WorkingSet), RECT{header.Left + c.s(430), y, header.Left + c.s(550), y + rowH}, 8, 500, palette.Text, DT_RIGHT|DT_VCENTER|DT_SINGLELINE)
		c.mono(shortBytes(p.ReadBps+p.WriteBps)+"/s", RECT{header.Left + c.s(560), y, header.Left + c.s(680), y + rowH}, 8, 500, palette.Muted, DT_RIGHT|DT_VCENTER|DT_SINGLELINE)
		c.mono(fmt.Sprint(p.Threads), RECT{header.Left + c.s(700), y, header.Left + c.s(790), y + rowH}, 8, 500, palette.Muted, DT_RIGHT|DT_VCENTER|DT_SINGLELINE)
		c.mono(fmt.Sprint(p.Handles), RECT{header.Left + c.s(800), y, header.Left + c.s(890), y + rowH}, 8, 500, palette.Muted, DT_RIGHT|DT_VCENTER|DT_SINGLELINE)
		y += rowH
		c.line(r.Left+c.s(12), y, r.Right-c.s(12), y, palette.Grid, 1)
	}
}

func (a *App) drawProcessMap(c *Canvas, r RECT, items []ProcessMetric) {
	c.rounded(r, 13, palette.Surface)
	c.strokeRound(r, 13, palette.Border, 1)
	if len(items) == 0 {
		return
	}
	if len(items) > 18 {
		items = items[:18]
	}
	total := 0.0
	for _, p := range items {
		total += math.Max(float64(p.WorkingSet), 16*1024*1024)
	}
	x := r.Left + c.s(8)
	avail := r.Right - r.Left - c.s(16)
	for i, p := range items {
		weight := math.Max(float64(p.WorkingSet), 16*1024*1024) / total
		w := int32(weight * float64(avail))
		if w < c.s(54) {
			w = c.s(54)
		}
		if x+w > r.Right-c.s(8) || i == len(items)-1 {
			w = r.Right - c.s(8) - x
		}
		if w <= 0 {
			break
		}
		box := RECT{x, r.Top + c.s(8), x + w, r.Bottom - c.s(8)}
		intensity := clampFloat(p.CPU/100, 0, 1)
		color := blend(palette.CardHover, palette.Blue, intensity*0.65)
		c.rounded(box, 9, color)
		c.strokeRound(box, 9, palette.Border, 1)
		if w > c.s(66) {
			c.text(p.Name, RECT{box.Left + c.s(8), box.Top + c.s(10), box.Right - c.s(8), box.Top + c.s(36)}, 8, 600, palette.Text, DT_LEFT|DT_VCENTER|DT_SINGLELINE|DT_END_ELLIPSIS)
			c.mono(core.FormatBytes(p.WorkingSet), RECT{box.Left + c.s(8), box.Top + c.s(38), box.Right - c.s(8), box.Top + c.s(62)}, 7, 500, palette.Muted, DT_LEFT|DT_VCENTER|DT_SINGLELINE)
		}
		if a.pointIn(box) {
			tip := RECT{box.Left + c.s(5), box.Bottom - c.s(64), min32(box.Right-c.s(5), box.Left+c.s(190)), box.Bottom - c.s(6)}
			c.rounded(tip, 8, palette.BG)
			c.mono(fmt.Sprintf("CPU %.1f%%\nRAM %s", p.CPU, core.FormatBytes(p.WorkingSet)), RECT{tip.Left + c.s(8), tip.Top + c.s(5), tip.Right - c.s(8), tip.Bottom - c.s(5)}, 8, 500, palette.Text, DT_LEFT|DT_WORDBREAK)
		}
		a.hit(box, "process-detail", int(p.PID))
		x += w + c.s(4)
		avail -= c.s(4)
	}
}

func (a *App) drawProcessDetail(c *Canvas, r RECT, p ProcessMetric, available bool) {
	back := RECT{r.Left, r.Top, r.Left + c.s(126), r.Top + c.s(38)}
	a.button(c, back, "← All processes", "close-process-detail", 0, false)
	titleLeft := back.Right + c.s(18)
	c.text(p.Name, RECT{titleLeft, r.Top - c.s(2), r.Right, r.Top + c.s(24)}, 15, 680, palette.Text, DT_LEFT|DT_VCENTER|DT_SINGLELINE|DT_END_ELLIPSIS)
	path := a.selectedPath
	if path == "" {
		path = "Executable path unavailable without additional access"
	}
	c.text(fmt.Sprintf("PID %d  ·  %s", p.PID, path), RECT{titleLeft, r.Top + c.s(23), r.Right, r.Top + c.s(45)}, 8, 450, palette.Muted, DT_LEFT|DT_VCENTER|DT_SINGLELINE|DT_END_ELLIPSIS)

	y := r.Top + c.s(58)
	gap := c.s(10)
	cardW := (r.Right - r.Left - gap*3) / 4
	started := "Unavailable"
	startedSub := "The process may be protected"
	if !p.Started.IsZero() {
		started = formatDuration(time.Since(p.Started))
		startedSub = "Started " + p.Started.Format("2 Jan 2006 at 15:04")
	}
	metrics := []struct {
		label, value, sub string
		color             uint32
	}{
		{"CPU", fmt.Sprintf("%.1f%%", p.CPU), "Share of total processor capacity", palette.Blue},
		{"Working memory", core.FormatBytes(p.WorkingSet), "Private " + core.FormatBytes(p.PrivateBytes), palette.Cyan},
		{"Disk activity", shortBytes(p.ReadBps+p.WriteBps) + "/s", "Read " + shortBytes(p.ReadBps) + "/s  ·  write " + shortBytes(p.WriteBps) + "/s", palette.Amber},
		{"Running for", started, startedSub, palette.Muted},
	}
	for i, metric := range metrics {
		x := r.Left + int32(i)*(cardW+gap)
		a.metricCard(c, RECT{x, y, x + cardW, y + c.s(92)}, metric.label, metric.value, metric.sub, metric.color, "", 0)
	}
	panel := RECT{r.Left, r.Bottom - c.s(126), r.Right, r.Bottom}
	graph := RECT{r.Left, y + c.s(102), r.Right, panel.Top - c.s(10)}
	a.drawSingleProcessGraph(c, graph, p)
	c.roundedFill(panel, 15, palette.Surface)
	statusColor, statusText := palette.Green, "Live telemetry is available"
	if !available {
		statusColor, statusText = palette.Amber, "This process is no longer visible or cannot be inspected"
	}
	c.circle(panel.Left+c.s(20), panel.Top+c.s(22), c.s(4), statusColor)
	c.text(statusText, RECT{panel.Left + c.s(34), panel.Top + c.s(7), panel.Left + c.s(380), panel.Top + c.s(36)}, 9, 620, statusColor, DT_LEFT|DT_VCENTER|DT_SINGLELINE|DT_END_ELLIPSIS)
	parentName := "Unavailable"
	children := 0
	for _, candidate := range a.snapshot.Processes {
		if candidate.PID == p.Parent {
			parentName = fmt.Sprintf("%s (PID %d)", candidate.Name, candidate.PID)
		}
		if candidate.Parent == p.PID {
			children++
		}
	}
	c.text(fmt.Sprintf("%d threads  ·  %d handles  ·  parent %s  ·  %d visible children", p.Threads, p.Handles, parentName, children), RECT{panel.Left + c.s(20), panel.Top + c.s(36), panel.Right - c.s(410), panel.Top + c.s(61)}, 8, 500, palette.Muted, DT_LEFT|DT_VCENTER|DT_SINGLELINE|DT_END_ELLIPSIS)
	c.text("Scheduling preference", RECT{panel.Right - c.s(390), panel.Top + c.s(9), panel.Right - c.s(170), panel.Top + c.s(31)}, 7, 650, palette.Muted2, DT_LEFT|DT_VCENTER|DT_SINGLELINE)
	choices := []struct {
		label string
		value int
		class uint32
	}{{"Below", 0, priorityBelowNormal}, {"Normal", 1, priorityNormal}, {"Above", 2, priorityAboveNormal}}
	choiceLeft := panel.Right - c.s(390)
	choiceW := c.s(72)
	for i, choice := range choices {
		box := RECT{choiceLeft + int32(i)*choiceW, panel.Top + c.s(33), choiceLeft + int32(i+1)*choiceW - c.s(3), panel.Top + c.s(63)}
		a.button(c, box, choice.label, "process-priority", choice.value, a.processPriorityChoice == choice.class)
	}
	apply := RECT{panel.Right - c.s(163), panel.Top + c.s(33), panel.Right - c.s(18), panel.Top + c.s(63)}
	a.button(c, apply, "Apply preference", "process-priority-apply", 0, a.processPriorityChoice != a.processPriorityOriginal)
	c.text("Safe classes only. This changes scheduler preference—not a promised FPS gain.", RECT{choiceLeft, panel.Top + c.s(67), panel.Right - c.s(18), panel.Top + c.s(91)}, 7, 450, palette.Muted, DT_LEFT|DT_VCENTER|DT_SINGLELINE|DT_END_ELLIPSIS)
	actionY := panel.Bottom - c.s(42)
	copyPID := RECT{panel.Left + c.s(20), actionY, panel.Left + c.s(118), actionY + c.s(34)}
	a.button(c, copyPID, "Copy PID", "copy-process-pid", 0, false)
	copyPath := RECT{copyPID.Right + c.s(8), actionY, copyPID.Right + c.s(126), actionY + c.s(34)}
	a.button(c, copyPath, "Copy path", "copy-process-path", 0, false)
	open := RECT{copyPath.Right + c.s(8), actionY, copyPath.Right + c.s(150), actionY + c.s(34)}
	a.button(c, open, "Open location", "open-process-location", 0, false)
}

func (a *App) drawSingleProcessGraph(c *Canvas, r RECT, p ProcessMetric) {
	c.roundedFill(r, 15, palette.Card)
	metric := a.processDetailMetric
	if metric == "" {
		metric = "cpu"
	}
	c.text("Individual performance", RECT{r.Left + c.s(16), r.Top + c.s(8), r.Left + c.s(260), r.Top + c.s(34)}, 9, 650, palette.Text, DT_LEFT|DT_VCENTER|DT_SINGLELINE)
	tabs := []struct {
		name  string
		value int
	}{{"CPU", 0}, {"Memory", 1}, {"Disk", 2}}
	for i, tab := range tabs {
		box := RECT{r.Right - c.s(246) + int32(i)*c.s(76), r.Top + c.s(7), r.Right - c.s(176) + int32(i)*c.s(76), r.Top + c.s(34)}
		a.button(c, box, tab.name, "process-metric", tab.value, (metric == "cpu" && i == 0) || (metric == "memory" && i == 1) || (metric == "disk" && i == 2))
	}
	key := fmt.Sprintf("process-%d-%s", p.PID, metric)
	if a.graphInspectionPaused {
		a.drawGraphPauseControl(c, r)
	} else if _, focused := a.graphRanges[key]; focused {
		reset := RECT{r.Left + c.s(270), r.Top + c.s(8), r.Left + c.s(360), r.Top + c.s(33)}
		a.button(c, reset, "Reset range", "graph-reset:"+key, 0, false)
	} else {
		c.text("Drag to focus", RECT{r.Left + c.s(270), r.Top + c.s(8), r.Left + c.s(370), r.Top + c.s(33)}, 7, 600, palette.Muted2, DT_LEFT|DT_VCENTER|DT_SINGLELINE)
	}
	plot := RECT{r.Left + c.s(16), r.Top + c.s(43), r.Right - c.s(16), r.Bottom - c.s(15)}
	for i := 0; i <= 3; i++ {
		y := plot.Top + int32(i)*(plot.Bottom-plot.Top)/3
		c.line(plot.Left, y, plot.Right, y, palette.Grid, 1)
	}
	now := a.graphDisplayNow()
	start, end := now.Add(-time.Duration(a.config.Appearance.GraphSeconds)*time.Second), now
	if focused, ok := a.graphRanges[key]; ok && focused.End.After(focused.Start) {
		start, end = focused.Start, focused.End
	}
	a.graphPlots = append(a.graphPlots, GraphPlotRegion{Rect: plot, Key: key, Start: start, End: end})
	values := make([]ProcessHistorySample, 0, 120)
	maxValue := 0.0
	for _, sample := range a.graphDisplayProcessHistory() {
		if sample.PID == p.PID && !sample.At.Before(start) && !sample.At.After(end) {
			values = append(values, sample)
			maxValue = math.Max(maxValue, processHistoryValue(sample, metric))
		}
	}
	if metric == "cpu" {
		maxValue = math.Max(100, maxValue)
	} else {
		maxValue *= 1.12
		if maxValue <= 0 {
			maxValue = 1
		}
	}
	color := palette.Blue
	if metric == "memory" {
		color = palette.Cyan
	} else if metric == "disk" {
		color = palette.Amber
	}
	if len(values) >= 2 {
		points := make([]POINT, 0, len(values))
		times := make([]time.Time, 0, len(values))
		lineValues := make([]float64, 0, len(values))
		for _, sample := range values {
			x := plot.Left + int32(core.MapTime(sample.At, start, end, float64(plot.Right-plot.Left)))
			value := processHistoryValue(sample, metric)
			y := plot.Bottom - int32(clampFloat(value, 0, maxValue)/maxValue*float64(plot.Bottom-plot.Top))
			points = append(points, POINT{x, y})
			times = append(times, sample.At)
			lineValues = append(lineValues, value)
		}
		c.polyline(points, color, 2)
		a.graphLines = append(a.graphLines, GraphRenderedSeries{Key: key, Series: p.Name, Color: color, Process: true, Points: points, Times: times, Values: lineValues})
		if a.pointIn(plot) && a.graphDragKey == "" {
			if index, distance, ok := nearestPolylineSample(points, a.hover); ok && distance <= float64(c.s(7)) {
				sample, point := values[index], points[index]
				a.graphHover = GraphSampleSelection{Key: key, Series: p.Name, At: sample.At, Point: point, Value: processHistoryValue(sample, metric), Color: color, Process: true}
				c.circle(point.X, point.Y, c.s(5), palette.CardHover)
				c.circle(point.X, point.Y, c.s(3), color)
				tip := graphTooltipRect(c, plot, point, 184, 54)
				c.roundedFill(tip, 11, palette.CardHover)
				c.strokeRound(tip, 11, palette.Border, 1)
				c.mono(sample.At.Format("15:04:05"), RECT{tip.Left + c.s(11), tip.Top + c.s(4), tip.Right - c.s(8), tip.Top + c.s(23)}, 7, 550, palette.Muted2, DT_LEFT|DT_VCENTER|DT_SINGLELINE)
				c.circle(tip.Left+c.s(12), tip.Top+c.s(37), c.s(3), color)
				c.text(p.Name, RECT{tip.Left + c.s(22), tip.Top + c.s(25), tip.Right - c.s(76), tip.Bottom - c.s(4)}, 8, 600, palette.Text, DT_LEFT|DT_VCENTER|DT_SINGLELINE|DT_END_ELLIPSIS)
				c.mono(processHistoryLabel(processHistoryValue(sample, metric), metric), RECT{tip.Right - c.s(78), tip.Top + c.s(25), tip.Right - c.s(10), tip.Bottom - c.s(4)}, 8, 600, palette.Text, DT_RIGHT|DT_VCENTER|DT_SINGLELINE)
			}
		}
		if pinned := a.graphPinned; pinned.Key == key && pinned.Process && !pinned.At.Before(start) && !pinned.At.After(end) {
			pinned.Point.X = plot.Left + int32(core.MapTime(pinned.At, start, end, float64(plot.Right-plot.Left)))
			pinned.Point.Y = plot.Bottom - int32(clampFloat(pinned.Value, 0, maxValue)/maxValue*float64(plot.Bottom-plot.Top))
			a.drawGraphBreakdown(c, plot, pinned)
		}
	} else {
		c.text("Collecting this process's history…", plot, 9, 500, palette.Muted, DT_CENTER|DT_VCENTER|DT_SINGLELINE)
	}
	if a.graphDragKey == key {
		x1, x2 := a.graphDragStart.X, a.graphDragCurrent.X
		if x1 > x2 {
			x1, x2 = x2, x1
		}
		x1, x2 = max32(x1, plot.Left), min32(x2, plot.Right)
		c.fill(RECT{x1, plot.Top, x2, plot.Bottom}, blend(palette.Surface, color, 0.14))
		c.line(x1, plot.Top, x1, plot.Bottom, color, 1)
		c.line(x2, plot.Top, x2, plot.Bottom, color, 1)
	}
}

func (a *App) drawGaming(c *Canvas, r RECT) {
	candidates := make([]ProcessMetric, 0, 12)
	for _, process := range a.snapshot.Processes {
		name := strings.ToLower(process.Name)
		if process.PID > 4 && process.PID != uint32(syscall.Getpid()) && name != "system" && name != "idle" {
			candidates = append(candidates, process)
		}
		if len(candidates) == 12 {
			break
		}
	}
	game, lockedRunning := a.lockedGameProcess()
	if !lockedRunning && a.gameCandidatePID != 0 {
		game, _ = a.processByPID(a.gameCandidatePID)
	}
	if game.PID == 0 && len(candidates) > 0 {
		game = candidates[0]
		a.gameCandidatePID = game.PID
	}
	hero := RECT{r.Left, r.Top, r.Right, r.Top + c.s(88)}
	c.roundedFill(hero, 16, palette.Surface)
	state := "Choose the game once. Kerneon will follow the executable when its PID changes."
	if a.config.Gaming.LockedProcessName != "" {
		state = "Locked to " + a.config.Gaming.LockedProcessName
		if !lockedRunning {
			state += " · waiting for it to start"
		}
	}
	c.text("Game Focus", RECT{hero.Left + c.s(20), hero.Top + c.s(12), hero.Right - c.s(350), hero.Top + c.s(39)}, 15, 690, palette.Text, DT_LEFT|DT_VCENTER|DT_SINGLELINE)
	c.text(state, RECT{hero.Left + c.s(20), hero.Top + c.s(43), hero.Right - c.s(350), hero.Bottom - c.s(10)}, 8, 450, palette.Muted, DT_LEFT|DT_VCENTER|DT_SINGLELINE|DT_END_ELLIPSIS)
	if a.config.Gaming.LockedProcessName == "" {
		a.button(c, RECT{hero.Right - c.s(270), hero.Top + c.s(25), hero.Right - c.s(130), hero.Top + c.s(63)}, "Lock selection", "game-lock", 0, game.PID != 0)
		a.button(c, RECT{hero.Right - c.s(120), hero.Top + c.s(25), hero.Right - c.s(20), hero.Top + c.s(63)}, "Unlock", "game-unlock", 0, false)
	} else {
		hudLabel := "FPS HUD"
		if antiCheatProcessEvidence(a.snapshot.Processes).Detected {
			hudLabel = "HUD guarded"
		} else if a.config.Gaming.FPSHUD {
			hudLabel = "HUD on"
		}
		a.button(c, RECT{hero.Right - c.s(330), hero.Top + c.s(25), hero.Right - c.s(232), hero.Top + c.s(63)}, hudLabel, "toggle-fps-hud", 0, a.config.Gaming.FPSHUD)
		a.button(c, RECT{hero.Right - c.s(222), hero.Top + c.s(25), hero.Right - c.s(120), hero.Top + c.s(63)}, "Change", "game-lock", 0, game.PID != 0 && !strings.EqualFold(game.Name, a.config.Gaming.LockedProcessName))
		a.button(c, RECT{hero.Right - c.s(110), hero.Top + c.s(25), hero.Right - c.s(20), hero.Top + c.s(63)}, "Unlock", "game-unlock", 0, false)
	}
	bodyTop := hero.Bottom + c.s(11)
	left := RECT{r.Left, bodyTop, r.Left + (r.Right-r.Left)*34/100, r.Bottom}
	right := RECT{left.Right + c.s(11), bodyTop, r.Right, r.Bottom}
	c.text("Running applications", RECT{left.Left + c.s(4), left.Top, left.Right, left.Top + c.s(26)}, 8, 650, palette.Muted2, DT_LEFT|DT_VCENTER|DT_SINGLELINE)
	y := left.Top + c.s(31)
	maxRows := int((left.Bottom - y) / c.s(45))
	if maxRows > len(candidates) {
		maxRows = len(candidates)
	}
	for i := 0; i < maxRows; i++ {
		process := candidates[i]
		row := RECT{left.Left, y, left.Right, y + c.s(39)}
		selected := process.PID == game.PID
		if selected {
			c.roundedFill(row, 11, palette.CardHover)
		}
		c.circle(row.Left+c.s(15), row.Top+c.s(19), c.s(3), func() uint32 {
			if selected {
				return palette.Cyan
			}
			return palette.Muted2
		}())
		c.text(process.Name, RECT{row.Left + c.s(28), row.Top, row.Right - c.s(86), row.Bottom}, 8, 570, palette.Text, DT_LEFT|DT_VCENTER|DT_SINGLELINE|DT_END_ELLIPSIS)
		c.mono(fmt.Sprintf("%.0f%%", process.CPU), RECT{row.Right - c.s(76), row.Top, row.Right - c.s(10), row.Bottom}, 8, 560, palette.Muted, DT_RIGHT|DT_VCENTER|DT_SINGLELINE)
		a.hit(row, "game-select", int(process.PID))
		y += c.s(45)
	}
	if game.PID == 0 {
		c.roundedFill(right, 15, palette.Card)
		c.text("No selectable application is running", right, 11, 570, palette.Muted, DT_CENTER|DT_VCENTER|DT_SINGLELINE)
		return
	}
	statsH := c.s(164)
	frame := a.frames.Snapshot()
	frameValue := func(value float64, suffix string) string {
		if frame.Samples < 30 || value <= 0 {
			return "—"
		}
		return fmt.Sprintf("%.1f%s", value, suffix)
	}
	stats := []stat{
		{"Frame rate", frameValue(frame.FPS, " fps"), "30-second measured mean"},
		{"1% low", frameValue(frame.OnePercentLow, " fps"), fmt.Sprintf("0.1%% low %s", frameValue(frame.ZeroPointOnePercentLow, " fps"))},
		{"Frame time p99", frameValue(frame.P99FrameMs, " ms"), fmt.Sprintf("Worst %s", frameValue(frame.WorstFrameMs, " ms"))},
		{"Hitch rate", frameValue(frame.HitchesPerMinute, "/min"), "Refresh-aware long frames"},
		{"Display delay p95", frameValue(frame.P95DisplayMs, " ms"), "ETW present to display"},
		{"Dropped", frameValue(frame.DroppedPercent, "%"), "Measured presents"},
	}
	w := (right.Right - right.Left - c.s(18)) / 3
	for i, stat := range stats {
		column, row := int32(i%3), int32(i/3)
		boxTop := right.Top + row*c.s(82)
		box := RECT{right.Left + column*(w+c.s(9)), boxTop, right.Left + column*(w+c.s(9)) + w, boxTop + c.s(76)}
		c.roundedFill(box, 12, palette.Card)
		c.text(stat.Title, RECT{box.Left + c.s(12), box.Top + c.s(6), box.Right - c.s(8), box.Top + c.s(27)}, 7, 630, palette.Muted2, DT_LEFT|DT_VCENTER|DT_SINGLELINE)
		c.mono(stat.Value, RECT{box.Left + c.s(12), box.Top + c.s(29), box.Right - c.s(8), box.Top + c.s(55)}, 11, 630, palette.Text, DT_LEFT|DT_VCENTER|DT_SINGLELINE|DT_END_ELLIPSIS)
		c.text(stat.Sub, RECT{box.Left + c.s(12), box.Top + c.s(57), box.Right - c.s(8), box.Bottom}, 7, 450, palette.Muted, DT_LEFT|DT_VCENTER|DT_SINGLELINE)
	}
	providerText := "Frame proof unavailable · install Intel PresentMon"
	if frame.Available {
		providerText = "Frame proof ready · Intel PresentMon ETW"
		if frame.Capturing {
			providerText = fmt.Sprintf("%s · %d live frames · %s", frame.Game, frame.Samples, frameLimiterLabel(frame, a.displayRefreshHz))
		}
	}
	if frame.Error != "" {
		providerText = "Frame capture warning · " + frame.Error
	}
	c.text(providerText, RECT{right.Left + c.s(5), right.Top + statsH - c.s(1), right.Right - c.s(5), right.Top + statsH + c.s(18)}, 7, 560, func() uint32 {
		if frame.Error != "" {
			return palette.Amber
		}
		if frame.Capturing {
			return palette.Green
		}
		return palette.Muted2
	}(), DT_LEFT|DT_VCENTER|DT_SINGLELINE|DT_END_ELLIPSIS)
	a.drawSingleProcessGraph(c, RECT{right.Left, right.Top + statsH + c.s(20), right.Right, right.Bottom}, game)
}

func (a *App) drawInsights(c *Canvas, r RECT) {
	a.ai.mu.RLock()
	connected, loading, aiErr, updated := a.ai.Connected, a.ai.Loading, a.ai.Error, a.ai.Updated
	generated := append([]AIInsight(nil), a.ai.Insights...)
	a.ai.mu.RUnlock()

	hero := RECT{r.Left, r.Top, r.Right, r.Top + c.s(140)}
	c.rounded(hero, 15, palette.Surface)
	c.strokeRound(hero, 15, palette.Border, 1)
	c.text("KERNEON INTELLIGENCE", RECT{hero.Left + c.s(20), hero.Top + c.s(12), hero.Right - c.s(390), hero.Top + c.s(35)}, 8, 700, palette.Cyan, DT_LEFT|DT_VCENTER|DT_SINGLELINE)
	status := "LOCAL · PRIVATE"
	statusColor := palette.Green
	if connected {
		status = "AI CONNECTED"
		statusColor = palette.Cyan
	}
	if loading {
		status = "ANALYSING TELEMETRY…"
	} else if connected && !updated.IsZero() {
		status = "AI · UPDATED " + updated.Format("15:04")
	}
	statusR := RECT{hero.Right - c.s(190), hero.Top + c.s(14), hero.Right - c.s(20), hero.Top + c.s(40)}
	c.rounded(statusR, 14, palette.CardHover)
	c.text(status, statusR, 7, 700, statusColor, DT_CENTER|DT_VCENTER|DT_SINGLELINE|DT_END_ELLIPSIS)
	description := "Local analysis is always available. Optional AI sends only resource measurements and anonymized process metrics when you request it; it cannot control this PC."
	if connected {
		description = "GPT-5.4 Mini turns the current and last 60 seconds of anonymized telemetry into situation-specific observations. Requests are stateless; API usage is billed to your key."
	}
	c.text(description, RECT{hero.Left + c.s(20), hero.Top + c.s(42), hero.Right - c.s(320), hero.Bottom - c.s(14)}, 8, 450, palette.Muted, DT_LEFT|DT_WORDBREAK)
	if connected {
		generate := RECT{hero.Right - c.s(286), hero.Top + c.s(58), hero.Right - c.s(130), hero.Top + c.s(98)}
		a.button(c, generate, func() string {
			if loading {
				return "Analysing…"
			}
			return "Generate insights"
		}(), "ai-generate", 0, true)
		disconnect := RECT{hero.Right - c.s(120), hero.Top + c.s(58), hero.Right - c.s(20), hero.Top + c.s(98)}
		a.button(c, disconnect, "Disconnect", "ai-disconnect", 0, false)
	} else {
		connect := RECT{hero.Right - c.s(286), hero.Top + c.s(58), hero.Right - c.s(20), hero.Top + c.s(98)}
		a.button(c, connect, "Connect copied API key", "ai-connect", 0, false)
	}
	if aiErr != "" {
		c.text(aiErr, RECT{hero.Left + c.s(20), hero.Bottom - c.s(28), hero.Right - c.s(320), hero.Bottom - c.s(6)}, 7, 550, palette.Amber, DT_LEFT|DT_VCENTER|DT_SINGLELINE|DT_END_ELLIPSIS)
	}

	type insightCard struct {
		title, detail, evidence, next string
		severity                      int
		source                        string
	}
	cards := make([]insightCard, 0, 4)
	for _, item := range generated {
		cards = append(cards, insightCard{item.Title, item.Explanation, strings.Join(item.Evidence, " · "), item.NextStep, 0, "AI · " + strings.ToUpper(item.Confidence) + " CONFIDENCE"})
		if len(cards) == 4 {
			break
		}
	}
	if len(cards) == 0 {
		pressure := core.ExplainPressure(core.PressureInput{CPU: a.display.CPU, GPU: a.display.GPU, Memory: a.display.Memory, Disk: a.display.Disk})
		warnings, errors := a.eventCounts()
		cards = append(cards,
			insightCard{pressure.Title, pressure.Explanation, fmt.Sprintf("CPU %.0f%% · GPU %.0f%% · memory %.0f%% · disk %.0f%%", a.display.CPU, a.display.GPU, a.display.Memory, a.display.Disk), "Watch the synchronized History view to confirm whether the condition is sustained.", pressure.Severity, "LOCAL ANALYSIS"},
			insightCard{"Memory capacity", fmt.Sprintf("%s remains available. Cached memory is reusable and is not treated as wasted capacity.", core.FormatBytes(a.snapshot.Memory.Available)), fmt.Sprintf("Physical memory %.1f%% used", a.display.Memory), "Investigate only if pressure is sustained or paging accompanies stutter.", 0, "LOCAL ANALYSIS"},
			insightCard{"System stability", "Recent Windows problems are separated from everyday event noise before they are shown as significant.", fmt.Sprintf("%d warnings · %d errors in the current Event Lens window", warnings, errors), "Open Event Lens only when a significant event needs context.", func() int {
				if errors > 0 {
					return 2
				}
				return 0
			}(), "LOCAL ANALYSIS"})
		if len(a.snapshot.Processes) > 0 {
			p := a.snapshot.Processes[0]
			cards = append(cards, insightCard{"Top measured consumer: " + p.Name, "This is an observation, not proof that the process is causing a slowdown.", fmt.Sprintf("%.1f%% CPU · %s RAM · %s/s disk", p.CPU, core.FormatBytes(p.WorkingSet), shortBytes(p.ReadBps+p.WriteBps)), "Open Processes to inspect its path and activity before taking action.", 0, "LOCAL ANALYSIS"})
		}
	}
	y := hero.Bottom + c.s(11)
	available := r.Bottom - y - c.s(int32(maxInt(0, len(cards)-1))*9)
	h := available / int32(maxInt(1, len(cards)))
	if h > c.s(128) {
		h = c.s(128)
	}
	if h < c.s(92) {
		h = c.s(92)
	}
	for _, item := range cards {
		box := RECT{r.Left, y, r.Right, min32(r.Bottom, y+h)}
		c.rounded(box, 13, palette.Card)
		c.strokeRound(box, 13, palette.Border, 1)
		col := palette.Cyan
		if item.severity > 1 {
			col = palette.Amber
		}
		c.circle(box.Left+c.s(22), box.Top+c.s(25), c.s(4), col)
		c.text(item.source, RECT{box.Left + c.s(38), box.Top + c.s(7), box.Right - c.s(18), box.Top + c.s(27)}, 7, 700, col, DT_LEFT|DT_VCENTER|DT_SINGLELINE)
		c.text(item.title, RECT{box.Left + c.s(38), box.Top + c.s(27), box.Right - c.s(18), box.Top + c.s(51)}, 10, 680, palette.Text, DT_LEFT|DT_VCENTER|DT_SINGLELINE|DT_END_ELLIPSIS)
		c.text(item.detail, RECT{box.Left + c.s(38), box.Top + c.s(52), box.Right - c.s(18), box.Top + c.s(81)}, 8, 450, palette.Muted, DT_LEFT|DT_WORDBREAK|DT_END_ELLIPSIS)
		c.text("EVIDENCE  "+item.evidence, RECT{box.Left + c.s(38), box.Top + c.s(82), box.Right - c.s(18), box.Top + c.s(102)}, 7, 600, palette.Muted2, DT_LEFT|DT_VCENTER|DT_SINGLELINE|DT_END_ELLIPSIS)
		if box.Bottom-box.Top >= c.s(118) {
			c.text("NEXT  "+item.next, RECT{box.Left + c.s(38), box.Top + c.s(103), box.Right - c.s(18), box.Bottom - c.s(5)}, 7, 550, palette.Green, DT_LEFT|DT_VCENTER|DT_SINGLELINE|DT_END_ELLIPSIS)
		}
		y = box.Bottom + c.s(9)
		if y >= r.Bottom {
			break
		}
	}
}

type surgeFeatureCopy struct {
	Title, Benefit, Technical string
	Color                     uint32
}

func surgeFeatureCatalog(guarded bool, cpuProfile SurgeCPUProfile, route SurgeRoute, stack SurgeStackSnapshot, contention SurgeContention, stutter SurgeStutterDiagnosis, antiCheat AntiCheatState) []surgeFeatureCopy {
	if guarded {
		return []surgeFeatureCopy{
			{Title: "Smoother frame delivery", Benefit: "Targets the long, uneven frames you actually feel as judder—not just the average FPS number.", Technical: "PresentMon ETW compares 1% lows, p99 frame time and hitch rate across workload-matched windows.", Color: palette.Green},
			{Title: "The real limiter, found", Benefit: "Works out whether more CPU, GPU or a steadier display boundary is the useful route before changing anything.", Technical: route.Title + " · " + route.Confidence + " confidence. The control path is " + route.ControlPath + ".", Color: palette.Blue},
			{Title: "Game state, repaired", Benefit: "Corrects a game that Windows has accidentally left throttled or unusually restricted.", Technical: "Repairs observed execution QoS, reduced memory priority, sub-Normal scheduling or severe affinity restriction; healthy state is left alone.", Color: palette.Cyan},
			{Title: "Stutter source, explained", Benefit: "Separates a game problem from a driver stall, memory pressure, storage delay or competing application.", Technical: stutter.Title + " · " + stutter.Confidence + " confidence. Driver attribution still requires corroborating ETW evidence.", Color: palette.Green},
			{Title: "Any game, one pipeline", Benefit: "A game does not need a special Kerneon profile to receive the full measurement and protection path.", Technical: "Every locked executable receives dependency preflight, frame proof, limiter routing, transaction logging and rollback.", Color: palette.Cyan},
			{Title: "Changes only when justified", Benefit: "If the evidence says a tweak is irrelevant, Surge leaves your PC alone.", Technical: "Typed eligibility gates require the matching bottleneck, a sufficient baseline and a flushed rollback snapshot before mutation.", Color: palette.Violet},
			{Title: "Placebo automatically rejected", Benefit: "A change that cannot show a repeatable improvement does not get to stay enabled.", Technical: "Three comparable proof windows test predeclared thresholds; scene changes and one-off anomalies are rejected.", Color: palette.Amber},
			{Title: "Everything returns to normal", Benefit: "Stop Surge, leave the game or close Kerneon and the session goes back exactly as it was.", Technical: "Process, QoS, affinity, memory priority, power-plan and supported driver-profile state use identity-bound exact rollback.", Color: palette.Green},
		}
	}
	guardState := fallback(antiCheat.Policy, "Detected sessions remain external-only while the default guard is enabled.")
	return []surgeFeatureCopy{
		{Title: "Maximum supported CPU response", Benefit: "When the processor is the limit, Surge can ask Windows for its strongest supported AC response for the session.", Technical: cpuProfile.Vendor + " · disposable power scheme · EPP 0 · full processor state · aggressive boost · firmware limits remain authoritative.", Color: palette.Cyan},
		{Title: "Native GPU headroom", Benefit: "Keeps supported GPU performance ready only when frame evidence shows graphics throughput is holding the game back.", Technical: fallback(stack.GPU.Provider, "Matching vendor runtime") + " reads clocks, power, temperature and limiter reasons; documented per-game values are transactional.", Color: palette.Violet},
		{Title: "Background noise, contained", Benefit: "Temporarily asks proven competing apps to step aside while your game needs the CPU.", Technical: fallback(contention.Detail, "At most two eligible same-session user processes may move to Below Normal plus EcoQoS after a contention diagnosis."), Color: palette.Cyan},
		{Title: "A steadier display ceiling", Benefit: "Can test a refresh-aware frame ceiling when bouncing against the display limit is hurting consistency.", Technical: "A separate roughly 2%-below-refresh NVIDIA application-cap experiment requires an unstable ceiling route and game relaunch.", Color: palette.Blue},
		{Title: "Limiter-aware tuning", Benefit: "Heat and power are not spent on a control that cannot fix the measured bottleneck.", Technical: route.Title + " · route: " + route.ControlPath + ". Native limiter telemetry vetoes irrelevant clock and power experiments.", Color: palette.Violet},
		{Title: "Stutter isolated, not guessed", Benefit: "Correlates slow frames with Windows scheduling, drivers, paging, storage and background demand.", Technical: stutter.Title + " · " + stutter.Confidence + " confidence. DPC/ISR, queue, paging and disk evidence remain visible in the journal.", Color: palette.Green},
		{Title: "Regressions stop themselves", Benefit: "If a treatment makes frame delivery worse, Surge withdraws the whole experiment automatically.", Technical: "Repeated p99, hitch-rate or 1% low regression crosses the automatic kill switch and restores the pre-session snapshot.", Color: palette.Amber},
		{Title: "Anti-cheat aware by default", Benefit: "Recognised competitive-game protection keeps Surge in its safest external-only lane unless you explicitly choose otherwise.", Technical: guardState + " Injection, hooks, game-memory access and anti-cheat-process manipulation are never available.", Color: palette.Green},
	}
}

func (a *App) drawOptimize(c *Canvas, r RECT) {
	view := a.autopilotSnapshot()
	stack := a.surgeStack.Snapshot()
	cpuProfile := currentSurgeCPUProfile()
	pressure := surgeLocalPressure(a.display.CPU, a.display.GPU, a.display.Memory, a.display.Disk)
	frame := a.frames.Snapshot()
	route := determineSurgeRoute(frame, stack.GPU, a.display.CPU, a.display.GPU, a.displayRefreshHz)
	gamePID := uint32(0)
	if game, ok := a.lockedGameProcess(); ok {
		gamePID = game.PID
	}
	contention := analyzeSurgeContention(a.snapshot.Processes, gamePID)
	stutter := diagnoseSurgeStutter(frame, a.snapshot.Latency, a.snapshot.Disk, a.snapshot.Memory, contention)
	antiCheat := antiCheatProcessEvidence(a.snapshot.Processes)
	frameEvidence := ""
	if frame.Capturing && frame.Error == "" && frame.Samples >= 30 && frame.FPS > 0 && frame.AverageRenderMs > 0 {
		frameTime := 1000 / frame.FPS
		renderShare := frame.AverageRenderMs / frameTime
		frameEvidence = fmt.Sprintf("%.1f FPS  ·  1%% low %.1f  ·  p99 %.1f ms  ·  %.1f hitches/min", frame.FPS, frame.OnePercentLow, frame.P99FrameMs, frame.HitchesPerMinute)
		if renderShare <= 0.65 && frame.FPS < float64(a.displayRefreshHz)*0.97 {
			pressure = core.Pressure{Key: "frame-cpu-engine", Title: "The frame ceiling is outside GPU throughput", Explanation: "The render path completes well before the next frame. This points to a CPU/main-thread, engine or frame-cap limit; more GPU clocks would add heat, not frames.", Severity: 1}
		} else if renderShare >= 0.85 {
			pressure = core.Pressure{Key: "frame-gpu", Title: "Frame delivery is GPU-throughput limited", Explanation: "Render completion occupies nearly the whole frame. CPU scheduling changes are unlikely to help; graphics workload or supported GPU controls are the relevant levers.", Severity: 2}
		} else {
			pressure = core.Pressure{Key: "frame-mixed", Title: "Frame pressure is mixed", Explanation: "Neither the render path nor general system pressure alone explains the frame interval. Surge will keep measuring rather than force an arbitrary tweak.", Severity: 1}
		}
	}
	power := a.optimizerSnapshot()
	hero := RECT{r.Left, r.Top, r.Right, r.Top + c.s(130)}
	c.roundedFill(hero, 18, palette.Surface)
	c.roundedFill(RECT{hero.Left, hero.Top + c.s(20), hero.Left + c.s(3), hero.Bottom - c.s(20)}, 2, palette.Cyan)
	status, color := "OFF", palette.Muted2
	if a.surgeIsEnabled() {
		status, color = "READY", palette.Green
		if view.Active {
			status, color = "ACTIVE NOW", palette.Cyan
		}
	}
	c.text("SURGE · "+status, RECT{hero.Left + c.s(22), hero.Top + c.s(10), hero.Right - c.s(310), hero.Top + c.s(31)}, 7, 720, color, DT_LEFT|DT_VCENTER|DT_SINGLELINE)
	c.text("Finds bottlenecks, tests reversible changes, and measures results.", RECT{hero.Left + c.s(22), hero.Top + c.s(30), hero.Right - c.s(315), hero.Top + c.s(62)}, 15, 670, palette.Text, DT_LEFT|DT_VCENTER|DT_SINGLELINE|DT_END_ELLIPSIS)
	statusText := "Surge finds the limiting factor, tests a reversible response and keeps it only when frame data proves it helped."
	if view.Status != "" {
		statusText = "Live · " + view.Status
	}
	if !a.surgeIsEnabled() {
		statusText = "Turn it on to measure the locked game, act only on evidence and restore every session change automatically."
	}
	c.text(statusText, RECT{hero.Left + c.s(22), hero.Top + c.s(66), hero.Right - c.s(330), hero.Bottom - c.s(12)}, 8, 450, palette.Muted, DT_LEFT|DT_WORDBREAK|DT_END_ELLIPSIS)
	a.toggle(c, RECT{hero.Right - c.s(288), hero.Top + c.s(16), hero.Right - c.s(112), hero.Top + c.s(53)}, "Surge", a.surgeIsEnabled(), "toggle-autopilot")
	a.button(c, RECT{hero.Right - c.s(100), hero.Top + c.s(16), hero.Right - c.s(20), hero.Top + c.s(53)}, "Journal", "open-remediation-log", 0, false)
	guarded := a.config.Tuning.Mode != "performance"
	aggressiveLabel := "Aggressive"
	if !guarded && a.config.Tuning.Mode == "performance" {
		aggressiveLabel = "Aggressive · on"
	} else if guarded && time.Now().Before(a.surgeModeConfirmUntil) {
		aggressiveLabel = "Confirm · Aggressive"
	}
	modeLeft := hero.Right - c.s(288)
	modeW := c.s(131)
	guardedRect := RECT{modeLeft, hero.Top + c.s(60), modeLeft + modeW, hero.Top + c.s(92)}
	performanceRect := RECT{modeLeft + modeW + c.s(6), hero.Top + c.s(60), hero.Right - c.s(20), hero.Top + c.s(92)}
	a.surgeModeButton(c, guardedRect, "Guarded", "surge-mode", 0, guarded, palette.Green)
	a.surgeModeButton(c, performanceRect, aggressiveLabel, "surge-mode", 1, !guarded, palette.Violet)
	gameRow := RECT{hero.Right - c.s(288), hero.Top + c.s(98), hero.Right - c.s(20), hero.Top + c.s(124)}
	c.roundedFill(gameRow, 13, blend(palette.Surface, palette.CardHover, 0.62))
	if a.config.Gaming.LockedProcessName == "" {
		c.circle(gameRow.Left+c.s(14), (gameRow.Top+gameRow.Bottom)/2, c.s(3), palette.Muted2)
		c.text("No game selected", RECT{gameRow.Left + c.s(25), gameRow.Top, gameRow.Right - c.s(92), gameRow.Bottom}, 7, 560, palette.Muted, DT_LEFT|DT_VCENTER|DT_SINGLELINE|DT_END_ELLIPSIS)
		c.text("Choose in Gaming  ›", RECT{gameRow.Right - c.s(126), gameRow.Top, gameRow.Right - c.s(10), gameRow.Bottom}, 7, 650, palette.Cyan, DT_RIGHT|DT_VCENTER|DT_SINGLELINE)
	} else {
		c.circle(gameRow.Left+c.s(14), (gameRow.Top+gameRow.Bottom)/2, c.s(3), palette.Green)
		c.text("Following "+a.config.Gaming.LockedProcessName, RECT{gameRow.Left + c.s(25), gameRow.Top, gameRow.Right - c.s(68), gameRow.Bottom}, 7, 600, palette.Text, DT_LEFT|DT_VCENTER|DT_SINGLELINE|DT_END_ELLIPSIS)
		c.text("Change  ›", RECT{gameRow.Right - c.s(76), gameRow.Top, gameRow.Right - c.s(10), gameRow.Bottom}, 7, 650, palette.Cyan, DT_RIGHT|DT_VCENTER|DT_SINGLELINE)
	}
	a.hit(gameRow, "nav:gaming", 0)

	bodyTop := hero.Bottom + c.s(12)
	left := RECT{r.Left, bodyTop, r.Left + (r.Right-r.Left)*53/100, r.Bottom}
	right := RECT{left.Right + c.s(12), bodyTop, r.Right, r.Bottom}
	c.text("What your PC is telling us", RECT{left.Left + c.s(3), left.Top, left.Right, left.Top + c.s(27)}, 9, 660, palette.Text, DT_LEFT|DT_VCENTER|DT_SINGLELINE)
	diagnosis := RECT{left.Left, left.Top + c.s(32), left.Right, left.Top + c.s(134)}
	c.roundedFill(diagnosis, 15, palette.Card)
	pressureColor := palette.Green
	if pressure.Severity == 1 {
		pressureColor = palette.Cyan
	} else if pressure.Severity > 1 {
		pressureColor = palette.Amber
	}
	c.circle(diagnosis.Left+c.s(23), diagnosis.Top+c.s(25), c.s(4), pressureColor)
	c.text(pressure.Title, RECT{diagnosis.Left + c.s(38), diagnosis.Top + c.s(9), diagnosis.Right - c.s(18), diagnosis.Top + c.s(37)}, 11, 660, palette.Text, DT_LEFT|DT_VCENTER|DT_SINGLELINE|DT_END_ELLIPSIS)
	c.text(pressure.Explanation, RECT{diagnosis.Left + c.s(38), diagnosis.Top + c.s(39), diagnosis.Right - c.s(18), diagnosis.Top + c.s(75)}, 8, 450, palette.Muted, DT_LEFT|DT_WORDBREAK|DT_END_ELLIPSIS)
	evidence := fmt.Sprintf("CPU %.0f%%  ·  GPU %.0f%%  ·  memory %.0f%%  ·  disk %.0f%%", a.display.CPU, a.display.GPU, a.display.Memory, a.display.Disk)
	if frameEvidence != "" {
		evidence = frameEvidence
	}
	c.mono(evidence, RECT{diagnosis.Left + c.s(38), diagnosis.Top + c.s(80), diagnosis.Right - c.s(18), diagnosis.Bottom - c.s(8)}, 7, 560, pressureColor, DT_LEFT|DT_VCENTER|DT_SINGLELINE|DT_END_ELLIPSIS)

	c.text("Native performance stack", RECT{left.Left + c.s(3), diagnosis.Bottom + c.s(7), left.Right, diagnosis.Bottom + c.s(31)}, 8, 660, palette.Text, DT_LEFT|DT_VCENTER|DT_SINGLELINE)
	profileCard := RECT{left.Left, diagnosis.Bottom + c.s(33), left.Right, diagnosis.Bottom + c.s(76)}
	c.roundedFill(profileCard, 12, blend(palette.Card, palette.Cyan, 0.055))
	stackColor := palette.Green
	if !stack.CoreReady {
		stackColor = palette.Amber
	}
	c.circle(profileCard.Left+c.s(17), profileCard.Top+c.s(16), c.s(4), stackColor)
	stackTitle := fmt.Sprintf("%s CPU · required %d/%d", cpuProfile.Vendor, stack.RequiredReady, stack.RequiredTotal)
	c.text(stackTitle, RECT{profileCard.Left + c.s(30), profileCard.Top + c.s(5), profileCard.Right - c.s(160), profileCard.Top + c.s(24)}, 8, 660, palette.Text, DT_LEFT|DT_VCENTER|DT_SINGLELINE|DT_END_ELLIPSIS)
	stackDetail := cpuProfile.CapabilitySummary
	if stack.GPU.Ready {
		stackDetail = fmt.Sprintf("%s · %.0f°C · %.0f/%.0f W · %d MHz · %s", stack.GPU.Provider, stack.GPU.TemperatureC, stack.GPU.PowerW, stack.GPU.PowerLimitW, stack.GPU.GraphicsClockMHz, fallback(stack.GPU.ThrottleSummary, "limiter state available"))
		if stack.GameProfile.NVIDIA.PowerPolicyName != "" {
			stackDetail = stack.GameProfile.NVIDIA.PowerPolicyName + " · " + stackDetail
		}
	}
	c.text(stackDetail, RECT{profileCard.Left + c.s(30), profileCard.Top + c.s(22), profileCard.Right - c.s(160), profileCard.Bottom - c.s(4)}, 7, 500, palette.Muted, DT_LEFT|DT_VCENTER|DT_SINGLELINE|DT_END_ELLIPSIS)
	a.button(c, RECT{profileCard.Right - c.s(153), profileCard.Top + c.s(8), profileCard.Right - c.s(80), profileCard.Bottom - c.s(8)}, "Tuning", "surge-tuning-open", 0, true)
	a.button(c, RECT{profileCard.Right - c.s(75), profileCard.Top + c.s(8), profileCard.Right - c.s(8), profileCard.Bottom - c.s(8)}, "Stack", "surge-dependencies-open", 0, false)
	playbookHeading := "What Surge can do · Guarded"
	playbooks := surgeFeatureCatalog(true, cpuProfile, route, stack, contention, stutter, antiCheat)
	modeCardTint := uint32(palette.Card)
	if !guarded {
		playbookHeading = "What Surge can do · Aggressive"
		playbooks = surgeFeatureCatalog(false, cpuProfile, route, stack, contention, stutter, antiCheat)
		if !a.antiCheatGuardIsEnabled() {
			playbooks[len(playbooks)-1].Color = palette.Amber
			playbooks[len(playbooks)-1].Benefit = "The extra guard has been disabled by choice; compatibility and account safety cannot be guaranteed."
		}
		modeCardTint = blend(palette.Card, palette.Violet, 0.075)
	}
	c.text(playbookHeading, RECT{left.Left + c.s(3), diagnosis.Bottom + c.s(82), left.Right - c.s(190), diagnosis.Bottom + c.s(108)}, 9, 660, func() uint32 {
		if guarded {
			return palette.Text
		}
		return blend(palette.Text, palette.Violet, 0.28)
	}(), DT_LEFT|DT_VCENTER|DT_SINGLELINE|DT_END_ELLIPSIS)
	c.text("Hover for plain + technical detail", RECT{left.Right - c.s(205), diagnosis.Bottom + c.s(82), left.Right, diagnosis.Bottom + c.s(108)}, 7, 520, palette.Muted2, DT_RIGHT|DT_VCENTER|DT_SINGLELINE|DT_END_ELLIPSIS)
	gridTop := diagnosis.Bottom + c.s(113)
	gridGap := c.s(6)
	cellW := (left.Right - left.Left - gridGap) / 2
	cellH := c.s(39)
	hoveredFeature := -1
	for i, playbook := range playbooks {
		col, rowIndex := int32(i%2), int32(i/2)
		x := left.Left + col*(cellW+gridGap)
		y := gridTop + rowIndex*(cellH+gridGap)
		cell := RECT{x, y, x + cellW, y + cellH}
		hover := a.animate(fmt.Sprintf("surge-feature:%t:%d", guarded, i), boolFloat(a.pointIn(cell)), 16*time.Millisecond)
		c.roundedFill(cell, 11, blend(modeCardTint, palette.CardHover, hover*0.82))
		c.circle(cell.Left+c.s(14), (cell.Top+cell.Bottom)/2, c.s(3), blend(palette.Muted2, playbook.Color, 0.70+hover*0.30))
		c.text(playbook.Title, RECT{cell.Left + c.s(26), cell.Top, cell.Right - c.s(26), cell.Bottom}, 8, 620, blend(palette.Muted, palette.Text, 0.72+hover*0.28), DT_LEFT|DT_VCENTER|DT_SINGLELINE|DT_END_ELLIPSIS)
		if hover > 0.01 {
			c.text("i", RECT{cell.Right - c.s(23), cell.Top, cell.Right - c.s(8), cell.Bottom}, 7, 680, blend(palette.Muted2, playbook.Color, hover), DT_CENTER|DT_VCENTER|DT_SINGLELINE)
		}
		if a.pointIn(cell) {
			hoveredFeature = i
		}
	}
	planText := fallback(power.PowerPlan, "Reading Windows power policy…")
	c.text("Windows policy now · "+planText, RECT{left.Left + c.s(3), left.Bottom - c.s(38), left.Right, left.Bottom - c.s(8)}, 7, 560, palette.Muted2, DT_LEFT|DT_VCENTER|DT_SINGLELINE|DT_END_ELLIPSIS)

	c.text("Surge journal", RECT{right.Left + c.s(3), right.Top, right.Right, right.Top + c.s(27)}, 9, 660, palette.Text, DT_LEFT|DT_VCENTER|DT_SINGLELINE)
	c.text("Proof and rollback, in order", RECT{right.Left + c.s(100), right.Top, right.Right, right.Top + c.s(27)}, 7, 480, palette.Muted2, DT_RIGHT|DT_VCENTER|DT_SINGLELINE|DT_END_ELLIPSIS)
	ledger := RECT{right.Left, right.Top + c.s(34), right.Right, right.Bottom - c.s(74)}
	c.roundedFill(ledger, 15, palette.Card)
	if len(view.Records) == 0 {
		c.text("Nothing changed yet", RECT{ledger.Left + c.s(20), ledger.Top + c.s(24), ledger.Right - c.s(20), ledger.Top + c.s(54)}, 11, 630, palette.Text, DT_LEFT|DT_VCENTER|DT_SINGLELINE)
		c.text("When evidence justifies a treatment, its reason, result and exact way back appear here.", RECT{ledger.Left + c.s(20), ledger.Top + c.s(58), ledger.Right - c.s(20), ledger.Top + c.s(105)}, 8, 450, palette.Muted, DT_LEFT|DT_WORDBREAK)
	} else {
		recordY := ledger.Top + c.s(10)
		for i, record := range view.Records {
			if i == 5 || recordY+c.s(73) > ledger.Bottom {
				break
			}
			row := RECT{ledger.Left + c.s(12), recordY, ledger.Right - c.s(12), recordY + c.s(68)}
			if i > 0 {
				c.line(row.Left, row.Top, row.Right, row.Top, palette.Grid, 1)
			}
			recordColor := palette.Cyan
			if record.Status == "failed" {
				recordColor = palette.Amber
			} else if record.Status == "restored" || record.Status == "recorded" {
				recordColor = palette.Green
			}
			c.mono(record.At.Format("15:04:05"), RECT{row.Left, row.Top + c.s(6), row.Left + c.s(70), row.Top + c.s(27)}, 7, 550, palette.Muted2, DT_LEFT|DT_VCENTER|DT_SINGLELINE)
			c.text(strings.ToUpper(record.Status), RECT{row.Right - c.s(100), row.Top + c.s(6), row.Right, row.Top + c.s(27)}, 7, 700, recordColor, DT_RIGHT|DT_VCENTER|DT_SINGLELINE)
			c.text(record.Painpoint, RECT{row.Left + c.s(76), row.Top + c.s(4), row.Right - c.s(106), row.Top + c.s(29)}, 8, 620, palette.Text, DT_LEFT|DT_VCENTER|DT_SINGLELINE|DT_END_ELLIPSIS)
			c.text(record.Action, RECT{row.Left + c.s(76), row.Top + c.s(31), row.Right, row.Bottom - c.s(4)}, 7, 450, palette.Muted, DT_LEFT|DT_WORDBREAK|DT_END_ELLIPSIS)
			recordY = row.Bottom
		}
	}
	guard := RECT{right.Left, right.Bottom - c.s(64), right.Right, right.Bottom}
	c.roundedFill(guard, 14, palette.Surface)
	guardTitle, guardColor := "Anti-cheat guardrails · ON", palette.Green
	guardDetail := "Detected anti-cheat sessions stay external-only. No injection, hooks, game-memory reads or Kerneon HUD."
	if a.antiCheatGuardIsEnabled() && time.Now().Before(a.surgeGuardConfirmUntil) {
		guardTitle, guardColor = "Click Guard again to accept the risk", palette.Amber
		guardDetail = "Turning this off cannot guarantee compatibility or account safety. The confirmation expires in a few seconds."
	}
	if !a.antiCheatGuardIsEnabled() {
		guardTitle, guardColor = "Anti-cheat guardrails · OFF", palette.Amber
		guardDetail = "Normal external controls are allowed. Compatibility and account safety cannot be guaranteed; anti-cheat processes remain untouched."
	}
	c.text(guardTitle, RECT{guard.Left + c.s(16), guard.Top + c.s(7), guard.Right - c.s(218), guard.Top + c.s(30)}, 8, 650, guardColor, DT_LEFT|DT_VCENTER|DT_SINGLELINE)
	c.text("External-only protection when detected", RECT{guard.Left + c.s(16), guard.Top + c.s(31), guard.Right - c.s(218), guard.Bottom - c.s(6)}, 7, 450, palette.Muted2, DT_LEFT|DT_VCENTER|DT_SINGLELINE|DT_END_ELLIPSIS)
	guardInfoRect := RECT{guard.Left, guard.Top, guard.Right - c.s(218), guard.Bottom}
	a.toggle(c, RECT{guard.Right - c.s(210), guard.Top + c.s(18), guard.Right - c.s(112), guard.Top + c.s(43)}, "Guard", a.antiCheatGuardIsEnabled(), "toggle-anticheat-guard")
	a.button(c, RECT{guard.Right - c.s(105), guard.Top + c.s(14), guard.Right - c.s(12), guard.Top + c.s(47)}, "Safety", "surge-safety-open", 0, false)

	if !a.surgeSafetyOpen && !a.surgeDependenciesOpen && !a.surgeTuningOpen {
		if a.pointIn(guardedRect) {
			a.drawSurgeModeTooltip(c, guardedRect, "Guarded", "Evidence first. Uses reversible process repairs and only changes Windows policy when a locked foreground game, AC power and the measured bottleneck justify it.", palette.Green)
		} else if a.pointIn(performanceRect) {
			a.drawSurgeModeTooltip(c, performanceRect, "Aggressive", "An explicit, twice-confirmed session. On "+cpuProfile.Vendor+", Surge requests EPP 0, full processor state and aggressive boost in a disposable policy, then restores the exact previous plan. Heat and power use may rise.", palette.Violet)
		} else if hoveredFeature >= 0 && hoveredFeature < len(playbooks) {
			a.drawSurgeFeatureTooltip(c, r, playbooks[hoveredFeature])
		} else if a.pointIn(guardInfoRect) {
			a.drawSurgeFeatureTooltip(c, r, surgeFeatureCopy{Title: guardTitle, Benefit: "Keeps recognised anti-cheat sessions in Surge's least invasive external-only lane by default.", Technical: guardDetail, Color: guardColor})
		}
	}
	if a.surgeSafetyOpen {
		a.drawSurgeSafetyNotice(c, r, cpuProfile)
	}
	if a.surgeDependenciesOpen {
		a.drawSurgeDependencyCenter(c, r, stack)
	}
	if a.surgeTuningOpen {
		a.drawSurgeTuningLab(c, r)
	}
}

func (a *App) drawSurgeTuningLab(c *Canvas, bounds RECT) {
	if c.AA != 0 {
		procGdipFillRectangleI.Call(c.AA, aaARGBBrush(colorWithAlpha(palette.BG, 207)), uintptr(bounds.Left), uintptr(bounds.Top), uintptr(bounds.Right-bounds.Left), uintptr(bounds.Bottom-bounds.Top))
	}
	accepted := a.config.Tuning.LabDisclaimerVersion == tuningLabDisclaimerVersion
	if !accepted {
		w := min32(c.s(760), bounds.Right-bounds.Left-c.s(48))
		h := min32(c.s(535), bounds.Bottom-bounds.Top-c.s(36))
		panel := RECT{bounds.Left + (bounds.Right-bounds.Left-w)/2, bounds.Top + (bounds.Bottom-bounds.Top-h)/2, bounds.Left + (bounds.Right-bounds.Left+w)/2, bounds.Top + (bounds.Bottom-bounds.Top+h)/2}
		a.modalHitShield(bounds, panel, "surge-tuning-dismiss")
		c.roundedFill(panel, 20, palette.Surface)
		c.strokeRound(panel, 20, blend(palette.Border, palette.Amber, 0.32), 1)
		c.text("SURGE TUNING LAB · SEPARATE CONSENT", RECT{panel.Left + c.s(24), panel.Top + c.s(14), panel.Right - c.s(24), panel.Top + c.s(39)}, 8, 720, palette.Amber, DT_LEFT|DT_VCENTER|DT_SINGLELINE)
		c.text("Hardware tuning is never implied by ordinary Surge.", RECT{panel.Left + c.s(24), panel.Top + c.s(42), panel.Right - c.s(24), panel.Top + c.s(79)}, 15, 670, palette.Text, DT_LEFT|DT_VCENTER|DT_SINGLELINE|DT_END_ELLIPSIS)
		c.text("Read this in full. Acceptance is stored locally with a version and timestamp, but Tuning Lab still remains off until separately armed.", RECT{panel.Left + c.s(24), panel.Top + c.s(80), panel.Right - c.s(24), panel.Top + c.s(116)}, 8, 450, palette.Muted, DT_LEFT|DT_WORDBREAK)
		risk := RECT{panel.Left + c.s(20), panel.Top + c.s(126), panel.Right - c.s(20), panel.Bottom - c.s(92)}
		c.roundedFill(risk, 15, blend(palette.Card, palette.Amber, 0.045))
		c.text("YOU ACCEPT THE FOLLOWING RISK", RECT{risk.Left + c.s(18), risk.Top + c.s(12), risk.Right - c.s(18), risk.Top + c.s(38)}, 8, 720, palette.Amber, DT_LEFT|DT_VCENTER|DT_SINGLELINE)
		c.text(tuningLabDisclaimer, RECT{risk.Left + c.s(18), risk.Top + c.s(45), risk.Right - c.s(18), risk.Bottom - c.s(16)}, 9, 470, palette.Text, DT_LEFT|DT_WORDBREAK|DT_END_ELLIPSIS)
		label := "I understand and accept the risk"
		if time.Now().Before(a.surgeTuningConsentConfirmUntil) {
			label = "Confirm · accept hardware risk"
		}
		a.button(c, RECT{panel.Right - c.s(290), panel.Bottom - c.s(65), panel.Right - c.s(24), panel.Bottom - c.s(24)}, label, "surge-tuning-consent", 0, true)
		a.button(c, RECT{panel.Left + c.s(24), panel.Bottom - c.s(65), panel.Left + c.s(122), panel.Bottom - c.s(24)}, "Cancel", "surge-tuning-dismiss", 0, false)
		return
	}

	view := a.tuningLabSnapshot()
	w := min32(c.s(870), bounds.Right-bounds.Left-c.s(36))
	h := min32(c.s(640), bounds.Bottom-bounds.Top-c.s(26))
	panel := RECT{bounds.Left + (bounds.Right-bounds.Left-w)/2, bounds.Top + (bounds.Bottom-bounds.Top-h)/2, bounds.Left + (bounds.Right-bounds.Left+w)/2, bounds.Top + (bounds.Bottom-bounds.Top+h)/2}
	a.modalHitShield(bounds, panel, "surge-tuning-dismiss")
	c.roundedFill(panel, 20, palette.Surface)
	c.strokeRound(panel, 20, blend(palette.Border, palette.Violet, 0.28), 1)
	statusColor, status := palette.Muted2, "DISARMED"
	if a.config.Tuning.LabAutoWithSurge {
		statusColor, status = palette.Violet, "AUTO WITH SURGE"
	}
	if view.Running {
		statusColor, status = palette.Cyan, "DISCOVERY RUNNING"
	} else if view.Estimating {
		statusColor, status = palette.Cyan, "ESTIMATING HARDWARE · ABOUT 3 SECONDS"
	} else if view.Applied {
		statusColor, status = palette.Green, "PROVED · SESSION ONLY"
	} else if view.Recovery {
		statusColor, status = palette.Amber, "ROLLBACK REQUIRED"
	}
	compactLab := panel.Bottom-panel.Top < c.s(560)
	c.text("SURGE TUNING LAB · "+status, RECT{panel.Left + c.s(24), panel.Top + c.s(12), panel.Right - c.s(220), panel.Top + c.s(38)}, 8, 720, statusColor, DT_LEFT|DT_VCENTER|DT_SINGLELINE|DT_END_ELLIPSIS)
	c.text("Tests supported GPU and VRAM controls, then keeps or restores each change.", RECT{panel.Left + c.s(24), panel.Top + c.s(39), panel.Right - c.s(24), panel.Top + c.s(76)}, 15, 670, palette.Text, DT_LEFT|DT_VCENTER|DT_SINGLELINE|DT_END_ELLIPSIS)
	description := "Only manufacturer-exposed controls can arm. Each tier is estimated from this PC's live driver ranges and headroom, then every applied step is journalled, temperature-checked and compared with frame proof."
	if a.surgeIsEnabled() && view.Capability.Estimate.Ready {
		description = "Surge is active, so the completed stock estimate is locked. Kerneon will not recalculate ranges or thermal ceilings from an already-boosted system."
	} else if a.surgeIsEnabled() {
		description = "Surge is active without a stock estimate. Kerneon will not estimate from boosted hardware; pause Surge to measure the unmodified PC."
	}
	if compactLab {
		if a.surgeIsEnabled() && view.Capability.Estimate.Ready {
			description = "Using the locked stock estimate; no boosted-state recalculation."
		} else if a.surgeIsEnabled() {
			description = "Pause Surge to estimate the unmodified PC."
		} else {
			description = "Three PC-specific estimates; every real step is bounded, measured and restored."
		}
	}
	c.text(description, RECT{panel.Left + c.s(24), panel.Top + c.s(76), panel.Right - c.s(24), panel.Top + c.s(111)}, 8, 450, palette.Muted, DT_LEFT|DT_WORDBREAK|DT_END_ELLIPSIS)

	profileKeys := []string{"conservative", "balanced", "enthusiast"}
	profiles := make([]struct{ name, detail string }, len(profileKeys))
	for index, key := range profileKeys {
		profiles[index].name = map[string]string{"conservative": "Conservative", "balanced": "Balanced", "enthusiast": "Enthusiast"}[key]
		profiles[index].detail = tuningProfileCardDetail(key, view.Capability, a.config.Appearance.TechnicalMode)
		if a.surgeIsEnabled() && !view.Capability.Estimate.Ready {
			profiles[index].detail = "Stock estimate required"
		}
	}
	profileTop := panel.Top + c.s(121)
	profileHeight := c.s(48)
	toggleWidth := c.s(206)
	if compactLab {
		profileTop, profileHeight, toggleWidth = panel.Top+c.s(114), c.s(46), c.s(178)
	}
	profileWidth := (panel.Right - c.s(24) - toggleWidth - c.s(12) - (panel.Left + c.s(24)) - c.s(14)) / 3
	if profileWidth > c.s(142) {
		profileWidth = c.s(142)
	}
	profileRects := make([]RECT, len(profiles))
	for index, profile := range profiles {
		r := RECT{panel.Left + c.s(24) + int32(index)*(profileWidth+c.s(7)), profileTop, panel.Left + c.s(24) + int32(index)*(profileWidth+c.s(7)) + profileWidth, profileTop + profileHeight}
		profileRects[index] = r
		selected := a.config.Tuning.LabProfile == profileKeys[index]
		hovered := a.hoverValid && pointInRect(a.hover, r)
		c.roundedFill(r, 11, blend(palette.Card, palette.Violet, 0.04+0.10*boolFloat(selected)))
		if selected {
			c.strokeRound(r, 11, blend(palette.Border, palette.Violet, 0.38), 1)
		} else if hovered {
			c.strokeRound(r, 11, blend(palette.Border, palette.Cyan, 0.26), 1)
		}
		c.text(profile.name, RECT{r.Left + c.s(12), r.Top + c.s(5), r.Right - c.s(28), r.Top + c.s(25)}, 8, 650, func() uint32 {
			if selected {
				return palette.Text
			}
			return palette.Muted
		}(), DT_LEFT|DT_VCENTER|DT_SINGLELINE)
		infoColor := palette.Muted2
		if hovered {
			infoColor = palette.Cyan
		}
		c.circle(r.Right-c.s(15), r.Top+c.s(14), c.s(8), blend(palette.Grid, infoColor, 0.14+0.18*boolFloat(hovered)))
		c.text("i", RECT{r.Right - c.s(23), r.Top + c.s(6), r.Right - c.s(7), r.Top + c.s(22)}, 6, 720, infoColor, DT_CENTER|DT_VCENTER|DT_SINGLELINE)
		c.text(profile.detail, RECT{r.Left + c.s(12), r.Top + c.s(25), r.Right - c.s(10), r.Bottom - c.s(4)}, 6, 480, palette.Muted2, DT_LEFT|DT_VCENTER|DT_SINGLELINE|DT_END_ELLIPSIS)
		a.hit(r, "surge-tuning-profile", index)
	}
	a.toggle(c, RECT{panel.Right - c.s(24) - toggleWidth, profileTop + c.s(8), panel.Right - c.s(24), profileTop + c.s(40)}, "Auto with Surge", a.config.Tuning.LabAutoWithSurge, "surge-tuning-auto")

	domains := []TuningDomainCapability{view.Capability.GPU}
	rowTop := profileTop + c.s(61)
	if compactLab {
		gap := c.s(7)
		cardWidth := (panel.Right - panel.Left - c.s(40) - int32(len(domains)-1)*gap) / int32(len(domains))
		for index, domain := range domains {
			row := RECT{panel.Left + c.s(20) + int32(index)*(cardWidth+gap), rowTop, panel.Left + c.s(20) + int32(index)*(cardWidth+gap) + cardWidth, rowTop + c.s(132)}
			c.roundedFill(row, 13, palette.Card)
			color := palette.Muted2
			if domain.Direct {
				color = palette.Green
			} else if domain.Guided || domain.Supported {
				color = palette.Cyan
			}
			c.circle(row.Left+c.s(13), row.Top+c.s(17), c.s(3), color)
			c.text(domain.Name, RECT{row.Left + c.s(22), row.Top + c.s(5), row.Right - c.s(8), row.Top + c.s(27)}, 8, 650, palette.Text, DT_LEFT|DT_VCENTER|DT_SINGLELINE|DT_END_ELLIPSIS)
			c.text(fallback(domain.Device, "Unknown device"), RECT{row.Left + c.s(11), row.Top + c.s(27), row.Right - c.s(11), row.Top + c.s(44)}, 6, 480, palette.Muted2, DT_LEFT|DT_VCENTER|DT_SINGLELINE|DT_END_ELLIPSIS)
			c.text(domain.State, RECT{row.Left + c.s(11), row.Top + c.s(45), row.Right - c.s(11), row.Top + c.s(62)}, 6, 720, color, DT_LEFT|DT_VCENTER|DT_SINGLELINE|DT_END_ELLIPSIS)
			c.text(fallback(domain.Controls, domain.Provider), RECT{row.Left + c.s(11), row.Top + c.s(64), row.Right - c.s(11), row.Top + c.s(96)}, 6, 450, palette.Muted, DT_LEFT|DT_WORDBREAK|DT_END_ELLIPSIS)
			buttonLabel := "Official source"
			if domain.InstalledPath != "" {
				buttonLabel = "Open provider"
			}
			if domain.OfficialURL != "" || domain.InstalledPath != "" {
				a.button(c, RECT{row.Left + c.s(10), row.Bottom - c.s(34), row.Right - c.s(10), row.Bottom - c.s(7)}, buttonLabel, "surge-tuning-source", index, false)
			}
		}
		rowTop += c.s(140)
	} else {
		for index, domain := range domains {
			row := RECT{panel.Left + c.s(20), rowTop, panel.Right - c.s(20), rowTop + c.s(78)}
			c.roundedFill(row, 14, palette.Card)
			color := palette.Muted2
			if domain.Direct {
				color = palette.Green
			} else if domain.Guided || domain.Supported {
				color = palette.Cyan
			}
			c.circle(row.Left+c.s(18), row.Top+c.s(19), c.s(4), color)
			c.text(domain.Name+" · "+fallback(domain.Device, "Unknown device"), RECT{row.Left + c.s(31), row.Top + c.s(6), row.Right - c.s(190), row.Top + c.s(29)}, 9, 650, palette.Text, DT_LEFT|DT_VCENTER|DT_SINGLELINE|DT_END_ELLIPSIS)
			c.text(domain.State, RECT{row.Right - c.s(182), row.Top + c.s(7), row.Right - c.s(12), row.Top + c.s(29)}, 7, 720, color, DT_RIGHT|DT_VCENTER|DT_SINGLELINE|DT_END_ELLIPSIS)
			c.text(fallback(domain.Controls, domain.Provider)+" · "+domain.Detail, RECT{row.Left + c.s(31), row.Top + c.s(31), row.Right - c.s(142), row.Bottom - c.s(7)}, 7, 450, palette.Muted, DT_LEFT|DT_WORDBREAK|DT_END_ELLIPSIS)
			buttonLabel := "Official source"
			if domain.InstalledPath != "" {
				buttonLabel = "Open provider"
			}
			if domain.OfficialURL != "" || domain.InstalledPath != "" {
				a.button(c, RECT{row.Right - c.s(130), row.Bottom - c.s(38), row.Right - c.s(12), row.Bottom - c.s(9)}, buttonLabel, "surge-tuning-source", index, false)
			}
			rowTop += c.s(86)
		}
	}

	state := RECT{panel.Left + c.s(20), rowTop + c.s(1), panel.Right - c.s(20), panel.Bottom - c.s(70)}
	c.roundedFill(state, 13, blend(palette.Card, statusColor, 0.045))
	c.text(fallback(view.Stage, "READY FOR CAPABILITY-GATED DISCOVERY"), RECT{state.Left + c.s(16), state.Top + c.s(7), state.Right - c.s(16), state.Top + c.s(29)}, 8, 700, statusColor, DT_LEFT|DT_VCENTER|DT_SINGLELINE|DT_END_ELLIPSIS)
	c.text(fallback(view.Status, "Nothing has been changed."), RECT{state.Left + c.s(16), state.Top + c.s(30), state.Right - c.s(16), state.Bottom - c.s(8)}, 7, 460, palette.Muted, DT_LEFT|DT_WORDBREAK|DT_END_ELLIPSIS)
	if view.Running || view.Progress > 0 {
		track := RECT{state.Left + c.s(16), state.Bottom - c.s(9), state.Right - c.s(16), state.Bottom - c.s(6)}
		c.roundedFill(track, 2, palette.Grid)
		fill := track
		fill.Right = fill.Left + int32(float64(fill.Right-fill.Left)*clampFloat(view.Progress, 0, 1))
		c.roundedFill(fill, 2, statusColor)
	}

	primaryLabel, primaryAction := "Start discovery", "surge-tuning-start"
	if view.Estimating {
		primaryLabel, primaryAction = "Estimating hardware…", ""
	} else if view.Running || view.Applied {
		primaryLabel, primaryAction = "Stop + restore", "surge-tuning-stop"
	} else if !view.Capability.GPU.Direct {
		primaryLabel = "Direct provider unavailable"
		primaryAction = ""
	} else if a.surgeIsEnabled() && !view.Capability.Estimate.Ready {
		primaryLabel, primaryAction = "Pause Surge + estimate", "surge-tuning-pause-estimate"
	} else if !a.surgeIsEnabled() || a.config.Tuning.Mode != "performance" {
		primaryLabel, primaryAction = tuningSurgeEnableControl()
	} else if _, running := a.lockedGameProcess(); !running {
		primaryLabel, primaryAction = "Lock a running game first", ""
	} else if antiCheat := antiCheatProcessEvidence(a.snapshot.Processes); antiCheat.Detected && a.antiCheatGuardIsEnabled() {
		primaryLabel, primaryAction = "Blocked by anti-cheat guard", ""
	} else if !processIsElevated() {
		primaryLabel, primaryAction = "Restart elevated", "surge-tuning-elevate"
	}
	primaryRect := RECT{panel.Right - c.s(205), panel.Bottom - c.s(54), panel.Right - c.s(24), panel.Bottom - c.s(18)}
	if primaryAction == "" {
		c.roundedFill(primaryRect, 11, palette.Card)
		c.text(primaryLabel, primaryRect, 8, 600, palette.Muted2, DT_CENTER|DT_VCENTER|DT_SINGLELINE|DT_END_ELLIPSIS)
	} else {
		a.button(c, primaryRect, primaryLabel, primaryAction, 0, true)
	}
	a.button(c, RECT{panel.Left + c.s(24), panel.Bottom - c.s(54), panel.Left + c.s(122), panel.Bottom - c.s(18)}, "Done", "surge-tuning-dismiss", 0, false)
	controlTestAvailable := !view.Running && !view.Applied && !view.Recovery && !view.Estimating && !a.surgeIsEnabled() && processIsElevated() && view.Capability.GPUOffsets.Ready && (view.Capability.GPUOffsets.Core.Supported || view.Capability.GPUOffsets.Memory.Supported)
	if controlTestAvailable {
		a.button(c, RECT{panel.Right - c.s(392), panel.Bottom - c.s(54), panel.Right - c.s(215), panel.Bottom - c.s(18)}, "Test controls + restore", "surge-tuning-control-test", 0, false)
	}
	elevation := "Standard session"
	if processIsElevated() {
		elevation = "Elevated tuning session · Remote monitoring stays available"
	}
	elevationRight := panel.Right - c.s(220)
	if controlTestAvailable {
		elevationRight = panel.Right - c.s(405)
	}
	c.text(elevation, RECT{panel.Left + c.s(136), panel.Bottom - c.s(54), elevationRight, panel.Bottom - c.s(18)}, 7, 520, palette.Muted2, DT_LEFT|DT_VCENTER|DT_SINGLELINE|DT_END_ELLIPSIS)
	if a.hoverValid {
		for index, profileRect := range profileRects {
			if pointInRect(a.hover, profileRect) {
				explanation := explainTuningProfile(profileKeys[index], view.Capability, a.config.Appearance.TechnicalMode)
				if a.surgeIsEnabled() && !view.Capability.Estimate.Ready {
					explanation.Search = "Surge is already active, so Kerneon will not calculate a hardware range from the modified state. Pause Surge to capture a stock estimate."
					explanation.Stops = "No clock, power or temperature figure is estimated or armed until the PC is measured with Surge off."
				}
				a.drawTuningProfileTooltip(c, panel, profileRect, explanation)
				break
			}
		}
	}
}

func tuningSurgeEnableControl() (string, string) {
	return "Enable Surge · Aggressive", "surge-tuning-enable-aggressive"
}

func (a *App) drawTuningProfileTooltip(c *Canvas, panel, profileRect RECT, explanation tuningProfileExplanation) {
	margin := c.s(10)
	bounds := RECT{panel.Left + margin, panel.Top + margin, panel.Right - margin, panel.Bottom - margin}
	w, h := c.s(590), c.s(318)
	if explanation.Technical {
		w, h = c.s(670), c.s(398)
	}
	w = min32(w, bounds.Right-bounds.Left)
	h = min32(h, bounds.Bottom-bounds.Top)
	anchor := POINT{X: a.hover.X, Y: profileRect.Bottom}
	box := cursorTooltipRect(bounds, anchor, w, h, c.s(10))
	if c.AA != 0 {
		aaRoundedFillARGB(c.AA, RECT{box.Left + c.s(3), box.Top + c.s(5), box.Right + c.s(3), box.Bottom + c.s(5)}, c.s(16), colorWithAlpha(palette.Shadow, 175))
	}
	c.roundedFill(box, 15, palette.CardHover)
	c.strokeRound(box, 15, blend(palette.Border, explanation.Color, 0.36), 1)
	c.circle(box.Left+c.s(18), box.Top+c.s(20), c.s(5), explanation.Color)
	c.text(explanation.Title, RECT{box.Left + c.s(31), box.Top + c.s(7), box.Right - c.s(14), box.Top + c.s(32)}, 10, 690, palette.Text, DT_LEFT|DT_VCENTER|DT_SINGLELINE|DT_END_ELLIPSIS)
	c.text(explanation.BestFor, RECT{box.Left + c.s(16), box.Top + c.s(34), box.Right - c.s(16), box.Top + c.s(67)}, 7, 500, palette.Text, DT_LEFT|DT_WORDBREAK|DT_END_ELLIPSIS)
	c.line(box.Left+c.s(16), box.Top+c.s(70), box.Right-c.s(16), box.Top+c.s(70), palette.Border, 1)
	rows := []struct{ label, detail string }{
		{"WHAT IT WILL TRY", explanation.MayTest},
		{"HOW IT DECIDES", explanation.Search},
		{"WHAT THIS PC ALLOWS", explanation.ThisPC},
		{"WHEN IT UNDOES IT", explanation.Stops},
	}
	if explanation.Technical {
		rows = []struct{ label, detail string }{
			{"ACTIVE ENVELOPE", explanation.MayTest},
			{"TRIAL + ACCEPTANCE", explanation.Search},
			{"PROVIDERS + RANGES", explanation.ThisPC},
			{"ABORT + ROLLBACK", explanation.Stops},
		}
	}
	rowTop := box.Top + c.s(74)
	rowHeight := (box.Bottom - c.s(9) - rowTop) / int32(len(rows))
	for index, row := range rows {
		top := rowTop + int32(index)*rowHeight
		labelColor := palette.Muted2
		if index == 0 || index == 2 {
			labelColor = explanation.Color
		}
		labelWidth := c.s(136)
		if explanation.Technical {
			labelWidth = c.s(145)
		}
		c.text(row.label, RECT{box.Left + c.s(16), top + c.s(1), box.Left + labelWidth, top + c.s(19)}, 6, 720, labelColor, DT_LEFT|DT_VCENTER|DT_SINGLELINE|DT_END_ELLIPSIS)
		c.text(row.detail, RECT{box.Left + labelWidth + c.s(3), top, box.Right - c.s(14), top + rowHeight - c.s(2)}, 7, 460, palette.Muted, DT_LEFT|DT_WORDBREAK|DT_END_ELLIPSIS)
	}
}

func (a *App) drawSurgeDependencyCenter(c *Canvas, bounds RECT, stack SurgeStackSnapshot) {
	if c.AA != 0 {
		procGdipFillRectangleI.Call(c.AA, aaARGBBrush(colorWithAlpha(palette.BG, 194)), uintptr(bounds.Left), uintptr(bounds.Top), uintptr(bounds.Right-bounds.Left), uintptr(bounds.Bottom-bounds.Top))
	}
	w := min32(c.s(760), bounds.Right-bounds.Left-c.s(48))
	h := min32(c.s(610), bounds.Bottom-bounds.Top-c.s(36))
	panel := RECT{bounds.Left + (bounds.Right-bounds.Left-w)/2, bounds.Top + (bounds.Bottom-bounds.Top-h)/2, bounds.Left + (bounds.Right-bounds.Left+w)/2, bounds.Top + (bounds.Bottom-bounds.Top+h)/2}
	a.modalHitShield(bounds, panel, "surge-dependencies-dismiss")
	c.roundedFill(panel, 20, palette.Surface)
	c.strokeRound(panel, 20, blend(palette.Border, palette.Cyan, 0.25), 1)
	statusColor, status := palette.Green, "READY"
	if !stack.CoreReady {
		statusColor, status = palette.Amber, "ACTION REQUIRED"
	}
	c.text("PERFORMANCE STACK · "+status, RECT{panel.Left + c.s(24), panel.Top + c.s(14), panel.Right - c.s(150), panel.Top + c.s(39)}, 8, 720, statusColor, DT_LEFT|DT_VCENTER|DT_SINGLELINE)
	c.text("Surge never hides a driver or software dependency.", RECT{panel.Left + c.s(24), panel.Top + c.s(43), panel.Right - c.s(24), panel.Top + c.s(76)}, 15, 660, palette.Text, DT_LEFT|DT_VCENTER|DT_SINGLELINE|DT_END_ELLIPSIS)
	c.text("Required means this PC needs the provider before Kerneon may use that control path. Every source below is the vendor's official route.", RECT{panel.Left + c.s(24), panel.Top + c.s(76), panel.Right - c.s(24), panel.Top + c.s(111)}, 8, 450, palette.Muted, DT_LEFT|DT_WORDBREAK|DT_END_ELLIPSIS)
	a.button(c, RECT{panel.Right - c.s(128), panel.Top + c.s(14), panel.Right - c.s(24), panel.Top + c.s(45)}, "Refresh", "surge-dependency-refresh", 0, false)

	rowTop := panel.Top + c.s(120)
	rowHeight := c.s(72)
	for index, dependency := range stack.Dependencies {
		if index >= 5 || rowTop+rowHeight > panel.Bottom-c.s(100) {
			break
		}
		row := RECT{panel.Left + c.s(20), rowTop, panel.Right - c.s(20), rowTop + rowHeight}
		c.roundedFill(row, 14, palette.Card)
		color, state := palette.Green, "READY"
		if dependency.Direct && dependency.Ready {
			state = "DIRECT"
		} else if dependency.Scope != "" && dependency.Ready {
			state = "READY"
		}
		if dependency.BuiltIn && dependency.Ready {
			state = "BUILT-IN"
		}
		if !dependency.Ready {
			color, state = palette.Muted2, "OPTIONAL"
			if dependency.Scope != "" {
				color, state = palette.Cyan, dependency.Scope+" SETUP"
			}
			if dependency.Required {
				color, state = palette.Amber, "REQUIRED"
			}
		}
		c.circle(row.Left+c.s(18), row.Top+c.s(19), c.s(4), color)
		c.text(dependency.Name, RECT{row.Left + c.s(31), row.Top + c.s(7), row.Right - c.s(188), row.Top + c.s(31)}, 9, 660, palette.Text, DT_LEFT|DT_VCENTER|DT_SINGLELINE|DT_END_ELLIPSIS)
		c.text(dependency.Provider+" · "+state, RECT{row.Right - c.s(180), row.Top + c.s(7), row.Right - c.s(12), row.Top + c.s(31)}, 7, 700, color, DT_RIGHT|DT_VCENTER|DT_SINGLELINE|DT_END_ELLIPSIS)
		c.text(dependency.Purpose, RECT{row.Left + c.s(31), row.Top + c.s(33), row.Right - c.s(142), row.Bottom - c.s(8)}, 7, 450, palette.Muted, DT_LEFT|DT_WORDBREAK|DT_END_ELLIPSIS)
		buttonLabel, action := "Official source", "surge-dependency-source"
		if dependency.ID == "presentmon" && !dependency.Ready {
			buttonLabel, action = "Install", "surge-dependency-install-presentmon"
		}
		a.button(c, RECT{row.Right - c.s(132), row.Bottom - c.s(38), row.Right - c.s(12), row.Bottom - c.s(9)}, buttonLabel, action, index, dependency.Required && !dependency.Ready)
		rowTop += rowHeight + c.s(8)
	}

	message := stack.Message
	if message == "" && stack.GPU.Ready {
		message = fmt.Sprintf("%s live · %.0f°C · %.0f/%.0f W · graphics %d/%d MHz · %s", stack.GPU.Name, stack.GPU.TemperatureC, stack.GPU.PowerW, stack.GPU.PowerLimitW, stack.GPU.GraphicsClockMHz, stack.GPU.MaxGraphicsClockMHz, fallback(stack.GPU.ThrottleSummary, "driver limiter state ready"))
		if stack.GameProfile.NVIDIA.PowerPolicyName != "" {
			message += " · DRS " + stack.GameProfile.NVIDIA.PowerPolicyName
		}
	}
	if message == "" {
		message = fmt.Sprintf("%d of %d required providers are ready.", stack.RequiredReady, stack.RequiredTotal)
	}
	c.text(message, RECT{panel.Left + c.s(24), panel.Bottom - c.s(72), panel.Right - c.s(146), panel.Bottom - c.s(24)}, 7, 520, palette.Muted, DT_LEFT|DT_WORDBREAK|DT_END_ELLIPSIS)
	a.button(c, RECT{panel.Right - c.s(128), panel.Bottom - c.s(58), panel.Right - c.s(24), panel.Bottom - c.s(22)}, "Done", "surge-dependencies-dismiss", 0, true)
}

func (a *App) surgeModeButton(c *Canvas, r RECT, label, action string, value int, active bool, accent uint32) {
	key := fmt.Sprintf("surge-mode:%d", value)
	hover := a.animate(key, boolFloat(a.pointIn(r)), 16*time.Millisecond)
	bg := blend(palette.Surface, palette.CardHover, hover*0.74)
	if active {
		bg = blend(bg, accent, 0.13)
	}
	hit := HitRegion{Rect: r, Action: action, Value: value}
	press := a.pressAmount(hit)
	visual := r
	inset := int32(math.Round(float64(c.s(1)) * press))
	visual.Left, visual.Top, visual.Right, visual.Bottom = visual.Left+inset, visual.Top+inset, visual.Right-inset, visual.Bottom-inset
	c.roundedFill(visual, 10, blend(bg, accent, press*0.08))
	if active || hover > 0.04 {
		c.strokeRound(visual, 10, blend(palette.Border, accent, 0.20+0.22*math.Max(hover, boolFloat(active))), 1)
	}
	c.circle(visual.Left+c.s(14), (visual.Top+visual.Bottom)/2, c.s(3), func() uint32 {
		if active {
			return accent
		}
		return palette.Muted2
	}())
	c.text(label, RECT{visual.Left + c.s(24), visual.Top, visual.Right - c.s(28), visual.Bottom}, 7, 650, func() uint32 {
		if active {
			return blend(palette.Text, accent, 0.34)
		}
		return palette.Text
	}(), DT_LEFT|DT_VCENTER|DT_SINGLELINE|DT_END_ELLIPSIS)
	if hover > 0.01 {
		c.text("i", RECT{visual.Right - c.s(22), visual.Top, visual.Right - c.s(8), visual.Bottom}, 7, 700, blend(palette.Muted2, accent, hover), DT_CENTER|DT_VCENTER|DT_SINGLELINE)
	}
	a.hit(r, action, value)
}

func cursorTooltipRect(bounds RECT, cursor POINT, width, height, offset int32) RECT {
	x, y := cursor.X+offset, cursor.Y+offset
	if x+width > bounds.Right {
		x = cursor.X - width - offset
	}
	if y+height > bounds.Bottom {
		y = cursor.Y - height - offset
	}
	if x < bounds.Left {
		x = bounds.Left
	}
	if y < bounds.Top {
		y = bounds.Top
	}
	if x+width > bounds.Right {
		x = bounds.Right - width
	}
	if y+height > bounds.Bottom {
		y = bounds.Bottom - height
	}
	return RECT{x, y, x + width, y + height}
}

func (a *App) drawSurgeFeatureTooltip(c *Canvas, bounds RECT, feature surgeFeatureCopy) {
	if !a.hoverValid {
		return
	}
	margin := c.s(10)
	bounds = RECT{bounds.Left + margin, bounds.Top + margin, bounds.Right - margin, bounds.Bottom - margin}
	w := min32(c.s(420), bounds.Right-bounds.Left)
	h := min32(c.s(122), bounds.Bottom-bounds.Top)
	box := cursorTooltipRect(bounds, a.hover, w, h, c.s(16))
	if c.AA != 0 {
		aaRoundedFillARGB(c.AA, RECT{box.Left + c.s(3), box.Top + c.s(5), box.Right + c.s(3), box.Bottom + c.s(5)}, c.s(15), colorWithAlpha(palette.Shadow, 150))
	}
	c.roundedFill(box, 14, palette.CardHover)
	c.strokeRound(box, 14, blend(palette.Border, feature.Color, 0.30), 1)
	c.circle(box.Left+c.s(17), box.Top+c.s(19), c.s(4), feature.Color)
	c.text(feature.Title, RECT{box.Left + c.s(30), box.Top + c.s(7), box.Right - c.s(14), box.Top + c.s(31)}, 9, 680, palette.Text, DT_LEFT|DT_VCENTER|DT_SINGLELINE|DT_END_ELLIPSIS)
	c.text("WHY IT MATTERS", RECT{box.Left + c.s(16), box.Top + c.s(34), box.Left + c.s(116), box.Top + c.s(51)}, 6, 720, feature.Color, DT_LEFT|DT_VCENTER|DT_SINGLELINE)
	c.text(feature.Benefit, RECT{box.Left + c.s(118), box.Top + c.s(31), box.Right - c.s(14), box.Top + c.s(63)}, 7, 500, palette.Text, DT_LEFT|DT_WORDBREAK|DT_END_ELLIPSIS)
	c.line(box.Left+c.s(16), box.Top+c.s(67), box.Right-c.s(16), box.Top+c.s(67), palette.Border, 1)
	c.text("HOW IT WORKS", RECT{box.Left + c.s(16), box.Top + c.s(73), box.Left + c.s(116), box.Top + c.s(90)}, 6, 720, palette.Muted2, DT_LEFT|DT_VCENTER|DT_SINGLELINE)
	c.text(feature.Technical, RECT{box.Left + c.s(118), box.Top + c.s(70), box.Right - c.s(14), box.Bottom - c.s(9)}, 7, 450, palette.Muted, DT_LEFT|DT_WORDBREAK|DT_END_ELLIPSIS)
}

func (a *App) drawSurgeModeTooltip(c *Canvas, _ RECT, title, detail string, accent uint32) {
	a.drawSurgeFeatureTooltip(c, RECT{c.s(navWidthUnits), 0, c.W, c.H}, surgeFeatureCopy{
		Title: title, Benefit: "Choose how far Surge may go when the evidence supports a treatment.", Technical: detail, Color: accent,
	})
}

func (a *App) drawSurgeSafetyNotice(c *Canvas, bounds RECT, profile SurgeCPUProfile) {
	if c.AA != 0 {
		procGdipFillRectangleI.Call(c.AA, aaARGBBrush(colorWithAlpha(palette.BG, 190)), uintptr(bounds.Left), uintptr(bounds.Top), uintptr(bounds.Right-bounds.Left), uintptr(bounds.Bottom-bounds.Top))
	}
	w := min32(c.s(650), bounds.Right-bounds.Left-c.s(70))
	h := min32(c.s(374), bounds.Bottom-bounds.Top-c.s(44))
	panel := RECT{bounds.Left + (bounds.Right-bounds.Left-w)/2, bounds.Top + (bounds.Bottom-bounds.Top-h)/2, bounds.Left + (bounds.Right-bounds.Left+w)/2, bounds.Top + (bounds.Bottom-bounds.Top+h)/2}
	c.roundedFill(panel, 20, palette.Surface)
	c.strokeRound(panel, 20, blend(palette.Border, palette.Amber, 0.25), 1)
	c.circle(panel.Left+c.s(27), panel.Top+c.s(29), c.s(6), palette.Amber)
	c.text("SURGE SAFETY NOTE", RECT{panel.Left + c.s(44), panel.Top + c.s(13), panel.Right - c.s(24), panel.Top + c.s(45)}, 9, 720, palette.Amber, DT_LEFT|DT_VCENTER|DT_SINGLELINE)
	c.text("Know exactly what the stronger mode changes before you use it.", RECT{panel.Left + c.s(24), panel.Top + c.s(50), panel.Right - c.s(24), panel.Top + c.s(80)}, 13, 660, palette.Text, DT_LEFT|DT_VCENTER|DT_SINGLELINE|DT_END_ELLIPSIS)
	c.text("On this PC", RECT{panel.Left + c.s(24), panel.Top + c.s(92), panel.Left + c.s(150), panel.Top + c.s(116)}, 8, 680, palette.Cyan, DT_LEFT|DT_VCENTER|DT_SINGLELINE)
	c.text(profile.Vendor+" · "+profile.Topology+" · "+profile.CapabilitySummary, RECT{panel.Left + c.s(24), panel.Top + c.s(116), panel.Right - c.s(24), panel.Top + c.s(143)}, 8, 500, palette.Text, DT_LEFT|DT_VCENTER|DT_SINGLELINE|DT_END_ELLIPSIS)
	c.text("What Aggressive may do", RECT{panel.Left + c.s(24), panel.Top + c.s(150), panel.Right - c.s(24), panel.Top + c.s(174)}, 8, 680, palette.Violet, DT_LEFT|DT_VCENTER|DT_SINGLELINE)
	c.text("Create a temporary AC-only CPU policy, isolate measured user-space contention, and use documented per-game NVIDIA power or frame-cap values when the evidence route qualifies. Neutral experiments are withdrawn. It never writes voltage, firmware or unsupported clock offsets.", RECT{panel.Left + c.s(24), panel.Top + c.s(176), panel.Right - c.s(24), panel.Top + c.s(226)}, 8, 450, palette.Muted, DT_LEFT|DT_WORDBREAK|DT_END_ELLIPSIS)
	c.text("Your part", RECT{panel.Left + c.s(24), panel.Top + c.s(234), panel.Right - c.s(24), panel.Top + c.s(258)}, 8, 680, palette.Amber, DT_LEFT|DT_VCENTER|DT_SINGLELINE)
	c.text("Results vary. Keep backups, adequate cooling and manufacturer-supported limits. Anti-cheat guardrails are on by default; disabling them accepts compatibility and account risk. Stop Surge if temperature, stability or power draw becomes unacceptable.", RECT{panel.Left + c.s(24), panel.Top + c.s(260), panel.Right - c.s(24), panel.Top + c.s(304)}, 8, 450, palette.Muted, DT_LEFT|DT_WORDBREAK|DT_END_ELLIPSIS)
	c.text("This notice explains risk; it is not a waiver of statutory rights or liability and is not legal advice.", RECT{panel.Left + c.s(24), panel.Bottom - c.s(58), panel.Right - c.s(146), panel.Bottom - c.s(18)}, 7, 520, palette.Muted2, DT_LEFT|DT_WORDBREAK|DT_END_ELLIPSIS)
	a.hit(bounds, "surge-safety-dismiss", 0)
	a.hit(panel, "surge-safety-open", 0)
	a.button(c, RECT{panel.Right - c.s(132), panel.Bottom - c.s(55), panel.Right - c.s(24), panel.Bottom - c.s(18)}, "Understood", "surge-safety-understood", 0, true)
}

func (a *App) drawOptimizeLegacy(c *Canvas, r RECT) {
	view := a.optimizerSnapshot()
	topProcess := ProcessMetric{}
	if len(a.snapshot.Processes) > 0 {
		topProcess = a.snapshot.Processes[0]
	}
	findings := core.AnalyzeOptimizations(core.OptimizationInput{CPU: a.display.CPU, GPU: a.display.GPU, Memory: a.display.Memory, Disk: a.display.Disk, Latency: a.display.Latency, PacketLoss: a.snapshot.Network.PacketLoss, CPUFrequencyMHz: a.snapshot.CPU.FrequencyMHz, CPUMaxMHz: a.snapshot.CPU.MaxMHz, TopProcess: topProcess.Name, TopProcessCPU: topProcess.CPU, GameFocus: a.config.Gaming.GameFocus, PowerPlan: view.PowerPlan})
	powerEligible := false
	for _, finding := range findings {
		if finding.Key == "power-plan" && finding.Actionable {
			powerEligible = true
		}
	}
	planName := strings.ToLower(view.PowerPlan)
	baselineEligible := view.Baseline.Samples >= 10 && view.Baseline.CPU >= 70 && (view.Baseline.GPU < 90 || view.Baseline.CPU > view.Baseline.GPU+8)
	if baselineEligible && !strings.Contains(planName, "performance") && !strings.Contains(planName, "ultimate") && !strings.Contains(planName, "ultra") {
		powerEligible = true
	}

	hero := RECT{r.Left, r.Top, r.Right, r.Top + c.s(104)}
	c.rounded(hero, 15, palette.Surface)
	c.strokeRound(hero, 15, palette.Border, 1)
	c.text("PROOF BEFORE PROMISES", RECT{hero.Left + c.s(20), hero.Top + c.s(12), hero.Right - c.s(20), hero.Top + c.s(34)}, 8, 700, palette.Green, DT_LEFT|DT_VCENTER|DT_SINGLELINE)
	c.text("Kerneon changes one variable at a time, preserves rollback, and compares synchronized telemetry from a repeatable workload.", RECT{hero.Left + c.s(20), hero.Top + c.s(37), hero.Right - c.s(300), hero.Bottom - c.s(12)}, 11, 580, palette.Text, DT_LEFT|DT_WORDBREAK)
	badge := RECT{hero.Right - c.s(260), hero.Top + c.s(31), hero.Right - c.s(20), hero.Top + c.s(70)}
	c.rounded(badge, 20, palette.CardHover)
	badgeText := "NO SYSTEM CHANGE PENDING"
	badgeCol := palette.Green
	if view.Applied {
		badgeText, badgeCol = "EXPERIMENT ACTIVE · ROLLBACK READY", palette.Amber
	}
	c.text(badgeText, badge, 7, 700, badgeCol, DT_CENTER|DT_VCENTER|DT_SINGLELINE|DT_END_ELLIPSIS)

	bodyTop := hero.Bottom + c.s(11)
	gap := c.s(11)
	left := RECT{r.Left, bodyTop, r.Left + (r.Right-r.Left)*55/100, r.Bottom}
	right := RECT{left.Right + gap, bodyTop, r.Right, r.Bottom}
	c.text("MEASURED DIAGNOSIS", RECT{left.Left, left.Top, left.Right, left.Top + c.s(24)}, 8, 700, palette.Muted2, DT_LEFT|DT_VCENTER|DT_SINGLELINE)
	y := left.Top + c.s(30)
	for i, finding := range findings {
		if i >= 4 || y+c.s(107) > left.Bottom {
			break
		}
		box := RECT{left.Left, y, left.Right, y + c.s(101)}
		c.rounded(box, 13, palette.Card)
		c.strokeRound(box, 13, palette.Border, 1)
		col := palette.Cyan
		if finding.Severity > 1 {
			col = palette.Amber
		}
		c.circle(box.Left+c.s(20), box.Top+c.s(24), c.s(4), col)
		c.text(finding.Title, RECT{box.Left + c.s(34), box.Top + c.s(9), box.Right - c.s(12), box.Top + c.s(35)}, 9, 680, palette.Text, DT_LEFT|DT_VCENTER|DT_SINGLELINE|DT_END_ELLIPSIS)
		c.text(finding.Detail, RECT{box.Left + c.s(34), box.Top + c.s(36), box.Right - c.s(12), box.Top + c.s(66)}, 7, 450, palette.Muted, DT_LEFT|DT_WORDBREAK|DT_END_ELLIPSIS)
		c.text("EVIDENCE  "+finding.Evidence, RECT{box.Left + c.s(34), box.Top + c.s(68), box.Right - c.s(12), box.Bottom - c.s(8)}, 7, 600, col, DT_LEFT|DT_VCENTER|DT_SINGLELINE|DT_END_ELLIPSIS)
		y = box.Bottom + c.s(8)
	}
	if left.Bottom-y >= c.s(150) {
		trust := RECT{left.Left, y, left.Right, min32(left.Bottom, y+c.s(205))}
		c.rounded(trust, 13, palette.Surface)
		c.strokeRound(trust, 13, palette.Border, 1)
		c.text("SHORTCUTS KERNEON REJECTS", RECT{trust.Left + c.s(18), trust.Top + c.s(10), trust.Right - c.s(18), trust.Top + c.s(34)}, 8, 700, palette.Green, DT_LEFT|DT_VCENTER|DT_SINGLELINE)
		rejected := []struct{ title, why string }{{"Memory cleaners", "Discard useful cache; they do not add RAM."}, {"Blanket service disabling", "Trades stability for an unproven background delta."}, {"Generic registry gaming tweaks", "No controlled evidence, no safe universal gain."}}
		ry := trust.Top + c.s(41)
		for _, item := range rejected {
			c.circle(trust.Left+c.s(21), ry+c.s(11), c.s(3), palette.Muted2)
			c.text(item.title, RECT{trust.Left + c.s(34), ry, trust.Left + c.s(240), ry + c.s(23)}, 8, 650, palette.Text, DT_LEFT|DT_VCENTER|DT_SINGLELINE|DT_END_ELLIPSIS)
			c.text(item.why, RECT{trust.Left + c.s(248), ry, trust.Right - c.s(14), ry + c.s(23)}, 7, 450, palette.Muted, DT_LEFT|DT_VCENTER|DT_SINGLELINE|DT_END_ELLIPSIS)
			ry += c.s(43)
		}
	}

	c.text("CONTROLLED EXPERIMENT", RECT{right.Left, right.Top, right.Right, right.Top + c.s(24)}, 8, 700, palette.Muted2, DT_LEFT|DT_VCENTER|DT_SINGLELINE)
	panel := RECT{right.Left, right.Top + c.s(30), right.Right, right.Bottom}
	c.rounded(panel, 14, palette.Card)
	c.strokeRound(panel, 14, palette.Border, 1)
	c.text("Windows power profile", RECT{panel.Left + c.s(18), panel.Top + c.s(12), panel.Right - c.s(18), panel.Top + c.s(38)}, 12, 680, palette.Text, DT_LEFT|DT_VCENTER|DT_SINGLELINE)
	plan := fallback(view.PowerPlan, "Reading active plan…")
	c.text("CURRENT  "+strings.ToUpper(plan), RECT{panel.Left + c.s(18), panel.Top + c.s(40), panel.Right - c.s(18), panel.Top + c.s(62)}, 7, 700, palette.Cyan, DT_LEFT|DT_VCENTER|DT_SINGLELINE|DT_END_ELLIPSIS)
	experimentText := "The evidence gate is closed. Load a repeatable CPU-heavy scene before Kerneon will offer a power change; performance-oriented plans are never replaced with the generic Windows plan."
	if powerEligible {
		experimentText = "The current signal is CPU-heavy. High performance can reduce clock ramp latency on some hardware; it is not assumed to increase FPS."
	}
	c.text(experimentText, RECT{panel.Left + c.s(18), panel.Top + c.s(68), panel.Right - c.s(18), panel.Top + c.s(117)}, 8, 450, palette.Muted, DT_LEFT|DT_WORDBREAK)
	stepY := panel.Top + c.s(128)
	drawStep := func(n int, title, sub string, done bool) {
		col := palette.Muted2
		if done {
			col = palette.Green
		}
		c.circle(panel.Left+c.s(27), stepY+c.s(16), c.s(11), col)
		c.text(fmt.Sprint(n), RECT{panel.Left + c.s(16), stepY + c.s(5), panel.Left + c.s(38), stepY + c.s(27)}, 7, 700, palette.BG, DT_CENTER|DT_VCENTER|DT_SINGLELINE)
		c.text(title, RECT{panel.Left + c.s(48), stepY, panel.Right - c.s(18), stepY + c.s(25)}, 9, 650, palette.Text, DT_LEFT|DT_VCENTER|DT_SINGLELINE)
		c.text(sub, RECT{panel.Left + c.s(48), stepY + c.s(24), panel.Right - c.s(18), stepY + c.s(45)}, 7, 450, palette.Muted, DT_LEFT|DT_VCENTER|DT_SINGLELINE|DT_END_ELLIPSIS)
		stepY += c.s(52)
	}
	drawStep(1, "Capture the baseline", func() string {
		if view.Baseline.Samples > 0 {
			return fmt.Sprintf("%d samples · CPU %.0f%% · GPU %.0f%%", view.Baseline.Samples, view.Baseline.CPU, view.Baseline.GPU)
		}
		return "Play a representative scene, then capture the last 60 seconds"
	}(), view.Baseline.Samples >= 10)
	drawStep(2, "Apply one reversible change", func() string {
		if view.Applied {
			return "High performance active; previous plan: " + view.PreviousPlan
		}
		if !powerEligible {
			return "Not offered: the current evidence or active plan does not justify it"
		}
		if !view.HighPerformanceExists {
			return "High performance is not exposed by this Windows device"
		}
		return "Switch from " + plan + " to High performance"
	}(), view.Applied)
	drawStep(3, "Repeat and compare", func() string {
		if view.Comparison.Verdict != "" {
			return view.Comparison.Verdict + " · " + view.Comparison.Detail
		}
		if view.Applied {
			return "Repeat the same scene for at least 10 synchronized samples"
		}
		return "No score is produced until both runs contain enough evidence"
	}(), view.Comparison.Verdict == "Measured improvement")

	buttonY := panel.Bottom - c.s(94)
	bw := (panel.Right - panel.Left - c.s(54)) / 3
	a.button(c, RECT{panel.Left + c.s(18), buttonY, panel.Left + c.s(18) + bw, buttonY + c.s(38)}, "Capture baseline", "opt-baseline", 0, view.Baseline.Samples >= 10)
	applyAction, applyLabel := "opt-apply", "Apply + test"
	if !powerEligible && !view.Applied {
		applyAction, applyLabel = "", "Gate closed"
	}
	a.button(c, RECT{panel.Left + c.s(27) + bw, buttonY, panel.Left + c.s(27) + 2*bw, buttonY + c.s(38)}, applyLabel, applyAction, 0, view.Applied)
	a.button(c, RECT{panel.Left + c.s(36) + 2*bw, buttonY, panel.Right - c.s(18), buttonY + c.s(38)}, "Compare", "opt-compare", 0, view.Comparison.Verdict != "")
	if view.Applied {
		a.button(c, RECT{panel.Left + c.s(18), panel.Bottom - c.s(46), panel.Left + (panel.Right-panel.Left)/2 - c.s(4), panel.Bottom - c.s(10)}, "Rollback now", "opt-rollback", 0, false)
		if view.Comparison.Verdict == "Measured improvement" {
			a.button(c, RECT{panel.Left + (panel.Right-panel.Left)/2 + c.s(4), panel.Bottom - c.s(46), panel.Right - c.s(18), panel.Bottom - c.s(10)}, "Keep verified change", "opt-keep", 0, true)
		}
	}
	if view.Error != "" {
		c.text(view.Error, RECT{panel.Left + c.s(18), buttonY - c.s(30), panel.Right - c.s(18), buttonY - c.s(5)}, 7, 600, palette.Amber, DT_LEFT|DT_VCENTER|DT_SINGLELINE|DT_END_ELLIPSIS)
	}
}

func (a *App) drawHistory(c *Canvas, r RECT) {
	top := r.Top
	if len(a.incident) > 0 {
		banner := RECT{r.Left, top, r.Right, top + c.s(52)}
		c.rounded(banner, 12, palette.CardHover)
		c.text(fmt.Sprintf("INCIDENT CAPTURE · %d samples preserved in memory", len(a.incident)), RECT{banner.Left + c.s(16), banner.Top, banner.Right - c.s(16), banner.Bottom}, 9, 650, palette.Cyan, DT_LEFT|DT_VCENTER|DT_SINGLELINE)
		top = banner.Bottom + c.s(10)
	}
	gap := c.s(9)
	h := (r.Bottom - top - gap*2) / 3
	a.drawMultiGraph(c, RECT{r.Left, top, r.Right, top + h}, "CPU + GPU", []graphSeries{{"CPU", palette.Blue, func(x HistorySample) float64 { return x.CPU }}, {"GPU", palette.Violet, func(x HistorySample) float64 { return x.GPU }}}, 100, "hist1")
	top += h + gap
	a.drawMultiGraph(c, RECT{r.Left, top, r.Right, top + h}, "MEMORY + DISK", []graphSeries{{"Memory", palette.Cyan, func(x HistorySample) float64 { return x.Memory }}, {"Disk", palette.Amber, func(x HistorySample) float64 { return x.Disk }}}, 100, "hist2")
	top += h + gap
	a.drawMultiGraph(c, RECT{r.Left, top, r.Right, top + h}, "Network", []graphSeries{{"Down", palette.Green, func(x HistorySample) float64 { return x.Down }}, {"Up", palette.Violet, func(x HistorySample) float64 { return x.Up }}}, 0, "hist3")
}

func (a *App) drawAlerts(c *Canvas, r RECT) {
	view := a.eventLensSnapshot()
	if a.selectedEventRawID != "" {
		for _, event := range view.Events {
			if event.RawID == a.selectedEventRawID {
				a.drawEventDetail(c, r, event)
				return
			}
		}
		a.selectedEventRawID = ""
	}
	visible := make([]SystemEvent, 0, len(view.Events))
	crashes, hardware, performance, actionable := 0, 0, 0, 0
	for _, event := range view.Events {
		switch event.Category {
		case "Crash", "Freeze":
			crashes += event.Occurrences
		case "Hardware", "Storage", "Driver", "Trust":
			hardware += event.Occurrences
		case "Performance", "Connectivity":
			performance += event.Occurrences
		}
		if event.Category != "System" && event.Category != "Noise" {
			actionable++
		}
		if eventMatchesFilter(event, view.Filter) {
			visible = append(visible, event)
		}
	}
	hero := RECT{r.Left, r.Top, r.Right, r.Top + c.s(96)}
	c.roundedFill(hero, 16, palette.Surface)
	status := "Windows and Kerneon are being correlated"
	if view.Loading {
		status = "Reading the last 24 hours…"
	} else if view.Error != "" {
		status = "Event Lens needs attention: " + view.Error
	} else if len(view.Events) == 0 {
		status = "No warning, error, crash, freeze or degradation event was found"
	} else {
		status = fmt.Sprintf("%d event patterns found · %d worth contextualising", len(view.Events), actionable)
	}
	c.text("Your PC's recent story", RECT{hero.Left + c.s(20), hero.Top + c.s(12), hero.Right - c.s(430), hero.Top + c.s(43)}, 15, 670, palette.Text, DT_LEFT|DT_VCENTER|DT_SINGLELINE)
	c.text(status, RECT{hero.Left + c.s(20), hero.Top + c.s(47), hero.Right - c.s(430), hero.Bottom - c.s(10)}, 8, 450, palette.Muted, DT_LEFT|DT_VCENTER|DT_SINGLELINE|DT_END_ELLIPSIS)
	summaries := []struct {
		label string
		count int
		color uint32
	}{{"Crashes / freezes", crashes, palette.Red}, {"Hardware / driver", hardware, palette.Amber}, {"Degradation", performance, palette.Cyan}}
	sw := c.s(116)
	for i, summary := range summaries {
		x := hero.Right - c.s(392) + int32(i)*c.s(124)
		c.mono(fmt.Sprint(summary.count), RECT{x, hero.Top + c.s(14), x + sw, hero.Top + c.s(48)}, 16, 670, summary.color, DT_CENTER|DT_VCENTER|DT_SINGLELINE)
		c.text(summary.label, RECT{x, hero.Top + c.s(51), x + sw, hero.Bottom - c.s(10)}, 7, 550, palette.Muted, DT_CENTER|DT_VCENTER|DT_SINGLELINE|DT_END_ELLIPSIS)
	}
	filterY := hero.Bottom + c.s(10)
	filters := []struct {
		label string
		value int
	}{{"Everything", eventAll}, {"Crashes + freezes", eventCrashes}, {"Hardware", eventHardware}, {"Degradation", eventPerformance}}
	fw := (r.Right - r.Left - c.s(160) - c.s(24)) / 4
	for i, filter := range filters {
		box := RECT{r.Left + int32(i)*(fw+c.s(6)), filterY, r.Left + int32(i)*(fw+c.s(6)) + fw, filterY + c.s(34)}
		a.button(c, box, filter.label, "event-filter", filter.value, view.Filter == filter.value)
	}
	a.button(c, RECT{r.Right - c.s(142), filterY, r.Right, filterY + c.s(34)}, "Refresh Windows", "event-refresh", 0, false)
	y := filterY + c.s(45)
	if len(visible) == 0 && !view.Loading {
		c.text("Nothing in this filter", RECT{r.Left, y, r.Right, r.Bottom}, 11, 550, palette.Muted2, DT_CENTER|DT_VCENTER|DT_SINGLELINE)
		return
	}
	maxRows := int((r.Bottom - y) / c.s(104))
	if maxRows > len(visible) {
		maxRows = len(visible)
	}
	for i := 0; i < maxRows; i++ {
		event := visible[i]
		significance := eventSignificance(event)
		box := RECT{r.Left, y, r.Right, y + c.s(94)}
		boxColor := palette.Card
		if a.pointIn(box) {
			boxColor = palette.CardHover
		}
		c.roundedFill(box, 14, boxColor)
		color := palette.Amber
		if event.Category == "Performance" {
			color = palette.Cyan
		} else if event.Category == "Connectivity" {
			color = palette.Green
		} else if event.Category == "Noise" || event.Category == "System" {
			color = palette.Muted2
		} else if event.Category == "Crash" || event.Category == "Freeze" {
			color = palette.Red
		}
		c.circle(box.Left+c.s(21), box.Top+c.s(23), c.s(4), color)
		c.text(event.Category, RECT{box.Left + c.s(35), box.Top + c.s(7), box.Left + c.s(135), box.Top + c.s(28)}, 7, 700, color, DT_LEFT|DT_VCENTER|DT_SINGLELINE)
		c.text(event.Title, RECT{box.Left + c.s(35), box.Top + c.s(28), box.Right - c.s(250), box.Top + c.s(52)}, 10, 650, palette.Text, DT_LEFT|DT_VCENTER|DT_SINGLELINE|DT_END_ELLIPSIS)
		stamp := event.At.Format("Mon 15:04")
		if event.Occurrences > 1 {
			stamp += fmt.Sprintf(" · %d similar", event.Occurrences)
		}
		sigColor := eventSignificanceColor(significance.Score)
		c.text(fmt.Sprintf("%s · %d/100", significance.Label, significance.Score), RECT{box.Right - c.s(230), box.Top + c.s(6), box.Right - c.s(16), box.Top + c.s(26)}, 7, 650, sigColor, DT_RIGHT|DT_VCENTER|DT_SINGLELINE)
		c.mono(stamp+"  ›", RECT{box.Right - c.s(230), box.Top + c.s(27), box.Right - c.s(16), box.Top + c.s(48)}, 7, 540, palette.Muted2, DT_RIGHT|DT_VCENTER|DT_SINGLELINE)
		detail := event.Impact + "  Next: " + event.Next
		if a.config.Appearance.TechnicalMode {
			detail = event.RawID + "  ·  " + event.Message
		}
		c.text(detail, RECT{box.Left + c.s(35), box.Top + c.s(55), box.Right - c.s(16), box.Bottom - c.s(8)}, 7, 450, palette.Muted, DT_LEFT|DT_WORDBREAK|DT_END_ELLIPSIS)
		a.hit(box, "event-open:"+event.RawID, 0)
		y = box.Bottom + c.s(8)
	}
}

func eventSignificanceColor(score int) uint32 {
	if score >= 85 {
		return palette.Red
	}
	if score >= 48 {
		return palette.Amber
	}
	if score >= 25 {
		return palette.Cyan
	}
	return palette.Green
}

func (a *App) drawEventDetail(c *Canvas, r RECT, event SystemEvent) {
	significance := eventSignificance(event)
	cause, confirmation := eventCauseMap(event)
	back := RECT{r.Left, r.Top, r.Left + c.s(128), r.Top + c.s(34)}
	a.button(c, back, "← Event Lens", "event-close", 0, false)
	hero := RECT{r.Left, r.Top + c.s(46), r.Right, r.Top + c.s(154)}
	c.roundedFill(hero, 16, palette.Surface)
	color := eventSignificanceColor(significance.Score)
	c.mono(fmt.Sprintf("%02d", significance.Score), RECT{hero.Left + c.s(20), hero.Top + c.s(15), hero.Left + c.s(92), hero.Bottom - c.s(15)}, 28, 680, color, DT_LEFT|DT_VCENTER|DT_SINGLELINE)
	c.text(significance.Label, RECT{hero.Left + c.s(96), hero.Top + c.s(18), hero.Left + c.s(220), hero.Top + c.s(43)}, 10, 680, color, DT_LEFT|DT_VCENTER|DT_SINGLELINE)
	c.text("SIGNIFICANCE / 100", RECT{hero.Left + c.s(96), hero.Top + c.s(44), hero.Left + c.s(240), hero.Top + c.s(65)}, 7, 650, palette.Muted2, DT_LEFT|DT_VCENTER|DT_SINGLELINE)
	c.text(significance.Explanation, RECT{hero.Left + c.s(96), hero.Top + c.s(67), hero.Left + c.s(370), hero.Bottom - c.s(10)}, 7, 450, palette.Muted, DT_LEFT|DT_WORDBREAK|DT_END_ELLIPSIS)
	c.line(hero.Left+c.s(390), hero.Top+c.s(16), hero.Left+c.s(390), hero.Bottom-c.s(16), palette.Border, 1)
	c.text(event.Category+" · "+event.At.Format("Monday 15:04:05"), RECT{hero.Left + c.s(412), hero.Top + c.s(12), hero.Right - c.s(20), hero.Top + c.s(37)}, 7, 680, color, DT_LEFT|DT_VCENTER|DT_SINGLELINE|DT_END_ELLIPSIS)
	c.text(event.Title, RECT{hero.Left + c.s(412), hero.Top + c.s(37), hero.Right - c.s(20), hero.Top + c.s(70)}, 13, 680, palette.Text, DT_LEFT|DT_VCENTER|DT_SINGLELINE|DT_END_ELLIPSIS)
	c.text(fmt.Sprintf("Reported %d time(s) in this 24-hour view", event.Occurrences), RECT{hero.Left + c.s(412), hero.Top + c.s(72), hero.Right - c.s(20), hero.Bottom - c.s(9)}, 8, 450, palette.Muted, DT_LEFT|DT_VCENTER|DT_SINGLELINE)

	top := hero.Bottom + c.s(12)
	left := RECT{r.Left, top, r.Left + (r.Right-r.Left)*59/100, r.Bottom}
	right := RECT{left.Right + c.s(12), top, r.Right, r.Bottom}
	c.text("Cause map", RECT{left.Left + c.s(3), left.Top, left.Right, left.Top + c.s(27)}, 9, 680, palette.Text, DT_LEFT|DT_VCENTER|DT_SINGLELINE)
	rows := []struct{ label, value string }{
		{"Signal source", event.Source + fmt.Sprintf(" · Event %d", event.ID)},
		{"Likely cause family", cause},
		{"What would confirm it", confirmation},
		{"Safest next step", event.Next},
	}
	y := left.Top + c.s(32)
	rowH := (left.Bottom - y - c.s(3)) / 4
	for i, row := range rows {
		box := RECT{left.Left, y, left.Right, y + rowH - c.s(6)}
		c.roundedFill(box, 12, palette.Card)
		c.mono(fmt.Sprintf("%02d", i+1), RECT{box.Left + c.s(14), box.Top + c.s(8), box.Left + c.s(45), box.Top + c.s(31)}, 7, 650, color, DT_LEFT|DT_VCENTER|DT_SINGLELINE)
		c.text(row.label, RECT{box.Left + c.s(46), box.Top + c.s(7), box.Right - c.s(12), box.Top + c.s(30)}, 8, 650, palette.Text, DT_LEFT|DT_VCENTER|DT_SINGLELINE)
		c.text(row.value, RECT{box.Left + c.s(46), box.Top + c.s(29), box.Right - c.s(14), box.Bottom - c.s(7)}, 7, 450, palette.Muted, DT_LEFT|DT_WORDBREAK|DT_END_ELLIPSIS)
		y += rowH
	}
	c.text("Evidence", RECT{right.Left + c.s(3), right.Top, right.Right, right.Top + c.s(27)}, 9, 680, palette.Text, DT_LEFT|DT_VCENTER|DT_SINGLELINE)
	evidence := RECT{right.Left, right.Top + c.s(32), right.Right, right.Bottom}
	c.roundedFill(evidence, 14, palette.Card)
	c.text("WHAT WINDOWS RECORDED", RECT{evidence.Left + c.s(18), evidence.Top + c.s(13), evidence.Right - c.s(18), evidence.Top + c.s(34)}, 7, 680, palette.Muted2, DT_LEFT|DT_VCENTER|DT_SINGLELINE)
	c.text(event.Message, RECT{evidence.Left + c.s(18), evidence.Top + c.s(38), evidence.Right - c.s(18), evidence.Top + c.s(145)}, 7, 450, palette.Muted, DT_LEFT|DT_WORDBREAK|DT_END_ELLIPSIS)
	c.line(evidence.Left+c.s(18), evidence.Top+c.s(155), evidence.Right-c.s(18), evidence.Top+c.s(155), palette.Grid, 1)
	c.text("HUMAN IMPACT", RECT{evidence.Left + c.s(18), evidence.Top + c.s(165), evidence.Right - c.s(18), evidence.Top + c.s(186)}, 7, 680, palette.Muted2, DT_LEFT|DT_VCENTER|DT_SINGLELINE)
	c.text(event.Impact, RECT{evidence.Left + c.s(18), evidence.Top + c.s(190), evidence.Right - c.s(18), evidence.Top + c.s(260)}, 7, 450, palette.Muted, DT_LEFT|DT_WORDBREAK|DT_END_ELLIPSIS)
	if a.config.Appearance.TechnicalMode {
		c.mono(event.RawID, RECT{evidence.Left + c.s(18), evidence.Bottom - c.s(44), evidence.Right - c.s(18), evidence.Bottom - c.s(14)}, 7, 520, palette.Cyan, DT_LEFT|DT_VCENTER|DT_SINGLELINE|DT_END_ELLIPSIS)
	} else {
		c.text("Technical language reveals the raw channel, provider, event and record IDs.", RECT{evidence.Left + c.s(18), evidence.Bottom - c.s(52), evidence.Right - c.s(18), evidence.Bottom - c.s(12)}, 7, 450, palette.Muted2, DT_LEFT|DT_WORDBREAK|DT_END_ELLIPSIS)
	}
}

func (a *App) drawAlertsLegacy(c *Canvas, r RECT) {
	alerts := []struct {
		name, rule string
		active     bool
	}{{"CPU saturation", "Above 95% for 30 seconds", a.display.CPU >= 95}, {"Memory pressure", "Above 90% for 30 seconds", a.display.Memory >= 90}, {"Disk saturation", "Above 90% for 20 seconds", a.display.Disk >= 90}, {"Network quality", "Loss above 3% or latency above 120 ms", a.snapshot.Network.PacketLoss >= 3 || a.display.Latency >= 120}, {"GPU saturation", "Above 97% for 30 seconds", a.display.GPU >= 97}}
	y := r.Top
	for _, al := range alerts {
		box := RECT{r.Left, y, r.Right, y + c.s(72)}
		c.rounded(box, 12, palette.Card)
		c.strokeRound(box, 12, palette.Border, 1)
		c.text(al.name, RECT{box.Left + c.s(16), box.Top + c.s(8), box.Right - c.s(160), box.Top + c.s(35)}, 10, 650, palette.Text, DT_LEFT|DT_VCENTER|DT_SINGLELINE)
		c.text(al.rule, RECT{box.Left + c.s(16), box.Top + c.s(35), box.Right - c.s(160), box.Bottom - c.s(8)}, 8, 450, palette.Muted, DT_LEFT|DT_VCENTER|DT_SINGLELINE)
		badge := RECT{box.Right - c.s(120), box.Top + c.s(20), box.Right - c.s(16), box.Bottom - c.s(19)}
		if al.active {
			c.rounded(badge, 10, palette.Red)
			c.text("ACTIVE", badge, 7, 700, palette.Text, DT_CENTER|DT_VCENTER|DT_SINGLELINE)
		} else {
			c.rounded(badge, 10, palette.CardHover)
			c.text("WATCHING", badge, 7, 700, palette.Green, DT_CENTER|DT_VCENTER|DT_SINGLELINE)
		}
		y += c.s(82)
	}
}

func (a *App) drawHardware(c *Canvas, r RECT) {
	passport := a.hardwareSnapshot()
	if !passport.Loaded {
		c.roundedFill(r, 17, palette.Surface)
		message := "Reading independent hardware identity signals…"
		if !passport.Loading {
			message = "Hardware Passport is ready to inspect this PC"
			a.button(c, RECT{r.Left + c.s(24), r.Top + c.s(84), r.Left + c.s(190), r.Top + c.s(124)}, "Inspect hardware", "hardware-refresh", 0, true)
		}
		c.text(message, RECT{r.Left + c.s(24), r.Top + c.s(24), r.Right - c.s(24), r.Top + c.s(70)}, 14, 650, palette.Text, DT_LEFT|DT_VCENTER|DT_SINGLELINE)
		return
	}
	contradiction := strings.Contains(strings.ToLower(passport.Verdict), "contradiction")
	verdictColor := palette.Green
	if contradiction {
		verdictColor = palette.Amber
	} else if passport.Hypervisor != "" {
		verdictColor = palette.Cyan
	}
	hero := RECT{r.Left, r.Top, r.Right, r.Top + c.s(112)}
	c.roundedFill(hero, 17, palette.Surface)
	c.text("HARDWARE PASSPORT", RECT{hero.Left + c.s(20), hero.Top + c.s(11), hero.Right - c.s(320), hero.Top + c.s(32)}, 7, 700, verdictColor, DT_LEFT|DT_VCENTER|DT_SINGLELINE)
	c.text(passport.Verdict, RECT{hero.Left + c.s(20), hero.Top + c.s(34), hero.Right - c.s(320), hero.Top + c.s(66)}, 16, 680, palette.Text, DT_LEFT|DT_VCENTER|DT_SINGLELINE|DT_END_ELLIPSIS)
	c.text(passport.VerdictDetail, RECT{hero.Left + c.s(20), hero.Top + c.s(70), hero.Right - c.s(320), hero.Bottom - c.s(8)}, 8, 450, palette.Muted, DT_LEFT|DT_WORDBREAK|DT_END_ELLIPSIS)
	c.text("IDENTITY", RECT{hero.Right - c.s(292), hero.Top + c.s(14), hero.Right - c.s(20), hero.Top + c.s(33)}, 7, 650, palette.Muted2, DT_LEFT|DT_VCENTER|DT_SINGLELINE)
	c.mono(passport.Fingerprint, RECT{hero.Right - c.s(292), hero.Top + c.s(34), hero.Right - c.s(20), hero.Top + c.s(60)}, 9, 620, palette.Cyan, DT_LEFT|DT_VCENTER|DT_SINGLELINE)
	c.text(passport.Drift, RECT{hero.Right - c.s(292), hero.Top + c.s(62), hero.Right - c.s(20), hero.Top + c.s(84)}, 7, 550, palette.Muted, DT_LEFT|DT_VCENTER|DT_SINGLELINE|DT_END_ELLIPSIS)
	if a.config.Trust.PinnedFingerprint == "" {
		a.button(c, RECT{hero.Right - c.s(292), hero.Top + c.s(83), hero.Right - c.s(146), hero.Top + c.s(107)}, "Pin this passport", "hardware-pin", 0, false)
	}
	a.button(c, RECT{hero.Right - c.s(136), hero.Top + c.s(83), hero.Right - c.s(20), hero.Top + c.s(107)}, "Re-scan", "hardware-refresh", 0, false)

	bodyTop := hero.Bottom + c.s(11)
	left := RECT{r.Left, bodyTop, r.Left + (r.Right-r.Left)*46/100, r.Bottom}
	right := RECT{left.Right + c.s(11), bodyTop, r.Right, r.Bottom}
	c.text("Identity chain", RECT{left.Left + c.s(3), left.Top, left.Right, left.Top + c.s(25)}, 9, 660, palette.Text, DT_LEFT|DT_VCENTER|DT_SINGLELINE)
	chain := []struct {
		label, value, detail string
		color                uint32
	}{
		{"Processor", passport.CPUBrand, fmt.Sprintf("%s · family %d · model %d · stepping %d", passport.CPUVendor, passport.Family, passport.Model, passport.Stepping), palette.Blue},
		{"Mainboard", fallback(passport.Board, "Unavailable"), "BIOS / UEFI " + fallback(passport.BIOS, "unavailable"), palette.Cyan},
		{"Boot trust", func() string {
			if passport.SecureBootKnown && passport.SecureBoot {
				return "Secure Boot active"
			}
			return "Secure Boot not confirmed"
		}(), fmt.Sprintf("TPM present %t · ready %t", passport.TPMPresent, passport.TPMReady), palette.Green},
	}
	if len(passport.GPUs) > 0 {
		gpu := passport.GPUs[0]
		chain = append(chain, struct {
			label, value, detail string
			color                uint32
		}{"Graphics", gpu.Name, gpu.Vendor + " · driver " + gpu.Driver, palette.Violet})
	}
	y := left.Top + c.s(31)
	for i, item := range chain {
		box := RECT{left.Left, y, left.Right, y + c.s(79)}
		c.roundedFill(box, 13, palette.Card)
		c.circle(box.Left+c.s(21), box.Top+c.s(23), c.s(5), item.color)
		if i < len(chain)-1 {
			c.line(box.Left+c.s(21), box.Top+c.s(30), box.Left+c.s(21), box.Bottom+c.s(12), palette.Grid, 2)
		}
		c.text(item.label, RECT{box.Left + c.s(37), box.Top + c.s(7), box.Right - c.s(12), box.Top + c.s(29)}, 7, 700, item.color, DT_LEFT|DT_VCENTER|DT_SINGLELINE)
		c.text(item.value, RECT{box.Left + c.s(37), box.Top + c.s(28), box.Right - c.s(12), box.Top + c.s(52)}, 9, 620, palette.Text, DT_LEFT|DT_VCENTER|DT_SINGLELINE|DT_END_ELLIPSIS)
		c.text(item.detail, RECT{box.Left + c.s(37), box.Top + c.s(53), box.Right - c.s(12), box.Bottom - c.s(6)}, 7, 450, palette.Muted, DT_LEFT|DT_VCENTER|DT_SINGLELINE|DT_END_ELLIPSIS)
		y = box.Bottom + c.s(7)
	}
	if a.config.Appearance.TechnicalMode {
		features := strings.Join(passport.Features, " · ")
		c.text("Instruction sets", RECT{left.Left + c.s(3), y + c.s(4), left.Right, y + c.s(25)}, 7, 650, palette.Muted2, DT_LEFT|DT_VCENTER|DT_SINGLELINE)
		c.text(features, RECT{left.Left + c.s(3), y + c.s(27), left.Right, left.Bottom}, 7, 450, palette.Muted, DT_LEFT|DT_WORDBREAK|DT_END_ELLIPSIS)
	}

	c.text("Independent checks", RECT{right.Left + c.s(3), right.Top, right.Right, right.Top + c.s(25)}, 9, 660, palette.Text, DT_LEFT|DT_VCENTER|DT_SINGLELINE)
	y = right.Top + c.s(31)
	maxSignals := int((right.Bottom - y) / c.s(88))
	if maxSignals > len(passport.Signals) {
		maxSignals = len(passport.Signals)
	}
	for i := 0; i < maxSignals; i++ {
		signal := passport.Signals[i]
		box := RECT{right.Left, y, right.Right, y + c.s(80)}
		c.roundedFill(box, 13, palette.Card)
		color := palette.Green
		if signal.Level == 1 {
			color = palette.Cyan
		} else if signal.Level == 2 {
			color = palette.Amber
		}
		c.circle(box.Left+c.s(20), box.Top+c.s(23), c.s(4), color)
		c.text(signal.Title, RECT{box.Left + c.s(34), box.Top + c.s(8), box.Right - c.s(12), box.Top + c.s(34)}, 9, 640, palette.Text, DT_LEFT|DT_VCENTER|DT_SINGLELINE|DT_END_ELLIPSIS)
		detail := signal.Detail
		if a.config.Appearance.TechnicalMode {
			detail = signal.Evidence
		}
		c.text(detail, RECT{box.Left + c.s(34), box.Top + c.s(36), box.Right - c.s(12), box.Bottom - c.s(7)}, 7, 450, palette.Muted, DT_LEFT|DT_WORDBREAK|DT_END_ELLIPSIS)
		y = box.Bottom + c.s(7)
	}
}

func (a *App) drawSystem(c *Canvas, r RECT) {
	summary := []kv{{"Windows", a.snapshot.System.Windows + " · build " + a.snapshot.System.Build}, {"Computer", fallback(a.snapshot.System.Manufacturer+" "+a.snapshot.System.Model, a.snapshot.System.Hostname)}, {"Processor", a.snapshot.System.CPU}, {"Graphics", a.snapshot.System.GPU}, {"Installed memory", core.FormatBytes(a.snapshot.System.InstalledRAM)}, {"Architecture", a.snapshot.System.Architecture}, {"BIOS / UEFI", a.snapshot.System.BIOS}, {"Uptime", formatDuration(time.Since(a.snapshot.System.BootTime))}}
	copyBtn := RECT{r.Right - c.s(174), r.Top, r.Right, r.Top + c.s(40)}
	a.button(c, copyBtn, "Copy system summary", "copy-system", 0, false)
	y := r.Top + c.s(54)
	cols := 2
	gap := c.s(10)
	w := (r.Right - r.Left - gap) / 2
	for i, item := range summary {
		x := r.Left + int32(i%cols)*(w+gap)
		yy := y + int32(i/cols)*c.s(92)
		box := RECT{x, yy, x + w, yy + c.s(82)}
		c.rounded(box, 12, palette.Card)
		c.strokeRound(box, 12, palette.Border, 1)
		c.text(item.Key, RECT{box.Left + c.s(15), box.Top + c.s(10), box.Right - c.s(12), box.Top + c.s(33)}, 8, 600, palette.Muted2, DT_LEFT|DT_VCENTER|DT_SINGLELINE)
		c.text(item.Value, RECT{box.Left + c.s(15), box.Top + c.s(36), box.Right - c.s(12), box.Bottom - c.s(9)}, 10, 550, palette.Text, DT_LEFT|DT_VCENTER|DT_WORDBREAK|DT_END_ELLIPSIS)
	}
}

func (a *App) drawRemote(c *Canvas, r RECT) {
	view := a.remoteSnapshot()
	screen := a.screenGrantSnapshot()
	hero := RECT{r.Left, r.Top, r.Right, r.Top + c.s(118)}
	c.roundedFill(hero, 17, palette.Surface)
	status, statusColor := "OFF", palette.Muted2
	if view.Running {
		status, statusColor = "AVAILABLE ON THIS NETWORK", palette.Green
		if view.ControlGranted {
			if view.ControlUntilRevoked {
				status, statusColor = "REMOTE CONTROL · UNTIL REVOKED", palette.Cyan
			} else {
				status, statusColor = "REMOTE CONTROL UNTIL "+view.ControlUntil.Format("15:04"), palette.Cyan
			}
		}
	} else if view.Enabled && view.Error != "" {
		status, statusColor = "NEEDS ATTENTION", palette.Amber
	}
	c.text(status, RECT{hero.Left + c.s(20), hero.Top + c.s(12), hero.Right - c.s(220), hero.Top + c.s(34)}, 7, 700, statusColor, DT_LEFT|DT_VCENTER|DT_SINGLELINE)
	c.text("Take Kerneon with you.", RECT{hero.Left + c.s(20), hero.Top + c.s(35), hero.Right - c.s(260), hero.Top + c.s(69)}, 17, 690, palette.Text, DT_LEFT|DT_VCENTER|DT_SINGLELINE)
	detail := "A paired companion for live metrics, games, processes, Event Lens and Hardware Passport. Control is granted locally, remains allowlisted, and every command is audited."
	if view.Error != "" {
		detail = "Remote Link could not start: " + view.Error
	}
	c.text(detail, RECT{hero.Left + c.s(20), hero.Top + c.s(72), hero.Right - c.s(300), hero.Bottom - c.s(10)}, 8, 450, palette.Muted, DT_LEFT|DT_WORDBREAK|DT_END_ELLIPSIS)
	toggle := RECT{hero.Right - c.s(210), hero.Top + c.s(38), hero.Right - c.s(20), hero.Top + c.s(79)}
	a.toggle(c, toggle, "Remote Link", view.Enabled, "remote-toggle")

	bodyTop := hero.Bottom + c.s(12)
	left := RECT{r.Left, bodyTop, r.Left + (r.Right-r.Left)*57/100, r.Bottom}
	right := RECT{left.Right + c.s(12), bodyTop, r.Right, r.Bottom}
	c.roundedFill(left, 16, palette.Card)
	c.roundedFill(right, 16, palette.Card)
	if !view.Running {
		c.text("Remote Link is resting", RECT{left.Left + c.s(24), left.Top + c.s(30), left.Right - c.s(24), left.Top + c.s(66)}, 14, 650, palette.Text, DT_LEFT|DT_VCENTER|DT_SINGLELINE)
		c.text("Switch it on when you want to connect. A fresh private session and pairing code are created each time.", RECT{left.Left + c.s(24), left.Top + c.s(70), left.Right - c.s(24), left.Top + c.s(122)}, 9, 450, palette.Muted, DT_LEFT|DT_WORDBREAK)
		c.text("No background web server · no cloud account · no persistent password", RECT{left.Left + c.s(24), left.Bottom - c.s(52), left.Right - c.s(24), left.Bottom - c.s(22)}, 8, 600, palette.Green, DT_LEFT|DT_VCENTER|DT_SINGLELINE|DT_END_ELLIPSIS)
		c.text("Scan to connect", RECT{right.Left, right.Top, right.Right, right.Bottom}, 11, 600, palette.Muted2, DT_CENTER|DT_VCENTER|DT_SINGLELINE)
		return
	}
	c.text("Connect another screen", RECT{left.Left + c.s(24), left.Top + c.s(21), left.Right - c.s(24), left.Top + c.s(54)}, 14, 660, palette.Text, DT_LEFT|DT_VCENTER|DT_SINGLELINE)
	c.text("Scan for instant pairing—no code entry. Type this address only on devices that cannot scan QR codes.", RECT{left.Left + c.s(24), left.Top + c.s(58), left.Right - c.s(24), left.Top + c.s(103)}, 8, 450, palette.Muted, DT_LEFT|DT_WORDBREAK)
	address := RECT{left.Left + c.s(24), left.Top + c.s(118), left.Right - c.s(24), left.Top + c.s(172)}
	c.roundedFill(address, 14, palette.CardHover)
	c.mono(view.URL, RECT{address.Left + c.s(16), address.Top, address.Right - c.s(116), address.Bottom}, 9, 590, palette.Cyan, DT_LEFT|DT_VCENTER|DT_SINGLELINE|DT_END_ELLIPSIS)
	a.button(c, RECT{address.Right - c.s(104), address.Top + c.s(9), address.Right - c.s(10), address.Bottom - c.s(9)}, "Copy", "remote-copy-url", 0, false)
	c.text("MANUAL FALLBACK CODE", RECT{left.Left + c.s(24), left.Top + c.s(193), left.Left + c.s(220), left.Top + c.s(218)}, 7, 700, palette.Muted2, DT_LEFT|DT_VCENTER|DT_SINGLELINE)
	pairCode := "•••  •••"
	if a.remotePairCodeRevealed.Load() {
		pairCode = view.PairCode
	}
	c.mono(pairCode, RECT{left.Left + c.s(24), left.Top + c.s(220), left.Left + c.s(260), left.Top + c.s(264)}, 22, 650, palette.Text, DT_LEFT|DT_VCENTER|DT_SINGLELINE)
	revealLabel := "Reveal"
	if a.remotePairCodeRevealed.Load() {
		revealLabel = "Hide"
	}
	a.button(c, RECT{left.Left + c.s(272), left.Top + c.s(224), left.Left + c.s(354), left.Top + c.s(258)}, revealLabel, "remote-pair-code-toggle", 0, false)
	security := fmt.Sprintf("%d paired session(s) · QR pairs instantly · code is fallback only", view.SessionCount)
	c.text(security, RECT{left.Left + c.s(24), left.Top + c.s(274), left.Right - c.s(24), left.Top + c.s(323)}, 8, 450, palette.Muted, DT_LEFT|DT_WORDBREAK)
	c.text("CONTROL ACCESS", RECT{left.Left + c.s(24), left.Top + c.s(326), left.Right - c.s(24), left.Top + c.s(348)}, 7, 700, palette.Muted2, DT_LEFT|DT_VCENTER|DT_SINGLELINE)
	controlLeft, controlGap := left.Left+c.s(24), c.s(6)
	controlW := (left.Right - left.Left - c.s(48) - controlGap*2) / 3
	controlTop, controlBottom := left.Top+c.s(350), left.Top+c.s(386)
	a.button(c, RECT{controlLeft, controlTop, controlLeft + controlW, controlBottom}, "Off", "remote-control-mode", 0, !view.ControlGranted)
	a.button(c, RECT{controlLeft + controlW + controlGap, controlTop, controlLeft + controlW*2 + controlGap, controlBottom}, "15 minutes", "remote-control-mode", 1, view.ControlGranted && !view.ControlUntilRevoked)
	a.button(c, RECT{controlLeft + controlW*2 + controlGap*2, controlTop, left.Right - c.s(24), controlBottom}, "Until revoked", "remote-control-mode", 2, view.ControlUntilRevoked)
	c.text("Until revoked is cleared when Remote Link stops, pairing is reset, or you revoke access.", RECT{left.Left + c.s(24), left.Top + c.s(392), left.Right - c.s(24), left.Top + c.s(426)}, 7, 450, palette.Muted, DT_LEFT|DT_WORDBREAK|DT_END_ELLIPSIS)
	buttonY := left.Bottom - c.s(52)
	buttonW := (left.Right - left.Left - c.s(56)) / 2
	a.button(c, RECT{left.Left + c.s(24), buttonY, left.Left + c.s(24) + buttonW, left.Bottom - c.s(14)}, "New pairing · revoke old", "remote-regenerate", 0, false)
	a.button(c, RECT{left.Left + c.s(32) + buttonW, buttonY, left.Right - c.s(24), left.Bottom - c.s(14)}, "Revoke all devices", "remote-revoke-sessions", 0, false)

	c.text("Pair instantly", RECT{right.Left + c.s(22), right.Top + c.s(13), right.Right - c.s(22), right.Top + c.s(39)}, 11, 660, palette.Text, DT_CENTER|DT_VCENTER|DT_SINGLELINE)
	c.text("SCAN ONCE · OPENS KERNEON LINK", RECT{right.Left + c.s(22), right.Top + c.s(39), right.Right - c.s(22), right.Top + c.s(57)}, 6, 720, palette.Cyan, DT_CENTER|DT_VCENTER|DT_SINGLELINE)
	qrSize := min32(right.Right-right.Left-c.s(112), min32(c.s(194), (right.Bottom-right.Top)*37/100))
	qrBox := RECT{(right.Left + right.Right - qrSize) / 2, right.Top + c.s(78), (right.Left + right.Right + qrSize) / 2, right.Top + c.s(78) + qrSize}
	if len(view.QR) > 0 {
		// The scan deck belongs to the dark product surface. Only the functional
		// QR tile is light, avoiding the oversized white app-icon card that made
		// the old component feel generic and visually disconnected.
		deck := RECT{qrBox.Left - c.s(17), qrBox.Top - c.s(17), qrBox.Right + c.s(17), qrBox.Bottom + c.s(17)}
		c.roundedFill(deck, 24, blend(palette.Card, palette.CardHover, 0.72))
		c.strokeRound(deck, 24, blend(palette.Border, palette.Cyan, 0.22), 1)
		c.roundedFill(qrBox, 11, rgb(244, 248, 248))
		cells := int32(len(view.QR))
		cell := max32(1, qrSize/cells)
		drawn := cell * cells
		offsetX, offsetY := qrBox.Left+(qrSize-drawn)/2, qrBox.Top+(qrSize-drawn)/2
		// Reserve a module-aligned 7×7 field in the data matrix itself. The QR
		// flows around the identity instead of receiving a logo badge afterward.
		// Highest error correction comfortably covers this deliberate aperture.
		centerCell := cells / 2
		logoHalfCells := int32(3)
		for row, values := range view.QR {
			for col, dark := range values {
				rowCell, colCell := int32(row), int32(col)
				inLogoField := rowCell >= centerCell-logoHalfCells && rowCell <= centerCell+logoHalfCells && colCell >= centerCell-logoHalfCells && colCell <= centerCell+logoHalfCells
				if dark {
					if inLogoField {
						continue
					}
					dot := RECT{offsetX + int32(col)*cell, offsetY + int32(row)*cell, offsetX + int32(col+1)*cell, offsetY + int32(row+1)*cell}
					// Keep the machine-readable grid pixel exact. Rounded individual
					// modules or multi-tone finder zones make phone thresholding less
					// reliable. Branding and color live in the frame/header instead.
					c.fill(dot, rgb(12, 39, 42))
				}
			}
		}
		// Render the exact canonical monogram directly into the reserved field in
		// the QR's own ink. There is no circle, tile, sticker, or second colour.
		logoX, logoY := offsetX+drawn/2, offsetY+drawn/2
		drawKerneonMarkTone(c, logoX, logoY, 12, rgb(244, 248, 248), rgb(12, 39, 42))
		readyY := deck.Bottom + c.s(13)
		c.circle((right.Left+right.Right)/2-c.s(47), readyY+c.s(5), c.s(3), palette.Green)
		c.text("READY TO SCAN", RECT{(right.Left+right.Right)/2 - c.s(38), readyY - c.s(5), (right.Left+right.Right)/2 + c.s(56), readyY + c.s(16)}, 6, 720, palette.Muted, DT_LEFT|DT_VCENTER|DT_SINGLELINE)
	} else {
		c.text("Pairing code unavailable", qrBox, 10, 600, palette.Amber, DT_CENTER|DT_VCENTER|DT_SINGLELINE)
	}
	grantTop := qrBox.Bottom + c.s(56)
	sessionColor := palette.Muted2
	sessionText := "Encrypted screen transport waits for approval"
	if screen.ActiveSessions > 0 {
		sessionColor = palette.Cyan
		sessionText = fmt.Sprintf("%d encrypted screen session(s)", screen.ActiveSessions)
	}
	c.text(strings.ToUpper(sessionText), RECT{right.Left + c.s(18), grantTop, right.Right - c.s(18), grantTop + c.s(20)}, 6, 700, sessionColor, DT_CENTER|DT_VCENTER|DT_SINGLELINE|DT_END_ELLIPSIS)
	viewRow := RECT{right.Left + c.s(18), grantTop + c.s(24), right.Right - c.s(18), grantTop + c.s(78)}
	inputRow := RECT{right.Left + c.s(18), grantTop + c.s(84), right.Right - c.s(18), grantTop + c.s(138)}
	a.drawScreenGrantRow(c, viewRow, "SCREEN VIEW", "screen-view-mode", screen.ViewGranted, screen.ViewUntilRevoked, true)
	a.drawScreenGrantRow(c, inputRow, "KEYBOARD + POINTER", "screen-input-mode", screen.InputGranted, screen.InputUntilRevoked, screen.ViewGranted)
	c.text("Pairing alone never exposes pixels or input · click the global sharing pill to revoke immediately", RECT{right.Left + c.s(16), grantTop + c.s(144), right.Right - c.s(16), right.Bottom - c.s(8)}, 6, 500, palette.Muted, DT_CENTER|DT_WORDBREAK|DT_END_ELLIPSIS)
}

func (a *App) drawScreenGrantRow(c *Canvas, r RECT, label, action string, granted, untilRevoked, enabled bool) {
	labelColor := palette.Muted2
	if granted {
		labelColor = palette.Cyan
	}
	c.text(label, RECT{r.Left, r.Top, r.Left + c.s(122), r.Top + c.s(18)}, 6, 700, labelColor, DT_LEFT|DT_VCENTER|DT_SINGLELINE)
	if !enabled {
		c.text("VIEW REQUIRED", RECT{r.Left + c.s(126), r.Top, r.Right, r.Top + c.s(18)}, 6, 650, palette.Amber, DT_RIGHT|DT_VCENTER|DT_SINGLELINE)
	}
	gap := c.s(5)
	w := (r.Right - r.Left - gap*2) / 3
	labels := []string{"Off", "15 min", "Until revoked"}
	for i, text := range labels {
		box := RECT{r.Left + int32(i)*(w+gap), r.Top + c.s(21), r.Left + int32(i)*(w+gap) + w, r.Bottom}
		active := (i == 0 && !granted) || (i == 1 && granted && !untilRevoked) || (i == 2 && untilRevoked)
		if !enabled && i > 0 {
			c.roundedFill(box, 12, palette.Surface)
			c.text(text, box, 6, 600, palette.Muted2, DT_CENTER|DT_VCENTER|DT_SINGLELINE|DT_END_ELLIPSIS)
			continue
		}
		a.button(c, box, text, action, i, active)
	}
}

func (a *App) drawSettings(c *Canvas, r RECT) {
	left := RECT{r.Left, r.Top, (r.Left+r.Right)/2 - c.s(5), r.Bottom}
	right := RECT{left.Right + c.s(10), r.Top, r.Right, r.Bottom}
	rowHeight, rowStep := c.s(78), c.s(88)
	if a.startupEnabled {
		rowHeight, rowStep = c.s(68), c.s(78)
	}
	y := left.Top
	a.settingChoice(c, RECT{left.Left, y, left.Right, y + rowHeight}, "Network sampling", "Counter reads are independent from rendering", fmt.Sprintf("%d Hz", a.config.Sampling.NetworkHz), "network-hz")
	y += rowStep
	a.settingChoice(c, RECT{left.Left, y, left.Right, y + rowHeight}, "Graph frame rate", "Visible graphs stop repainting when minimized", fmt.Sprintf("%d FPS", a.config.Sampling.GraphFPS), "graph-fps")
	y += rowStep
	a.settingChoice(c, RECT{left.Left, y, left.Right, y + rowHeight}, "Graph history", "Recent high-resolution window", durationLabel(a.config.Appearance.GraphSeconds), "graph-seconds")
	y += rowStep
	units := map[core.UnitMode]string{core.UnitAuto: "Auto", core.UnitBits: "Bits/sec", core.UnitBytes: "Bytes/sec"}[a.config.Network.Units]
	a.settingChoice(c, RECT{left.Left, y, left.Right, y + rowHeight}, "Throughput units", "Auto uses hysteresis to avoid unit flicker", units, "units")
	y += rowStep
	a.settingChoice(c, RECT{left.Left, y, left.Right, y + rowHeight}, "Window material", "Mica backdrop with restrained whole-window translucency", fmt.Sprintf("%d%%", a.config.Appearance.WindowOpacity), "opacity")
	y += rowStep
	a.settingChoice(c, RECT{left.Left, y, left.Right, y + rowHeight}, "Ping cadence", "Runs independently from bandwidth sampling", func() string {
		if a.config.Network.PingSeconds == 0 {
			return "Off"
		}
		return fmt.Sprintf("Every %ds", a.config.Network.PingSeconds)
	}(), "ping")
	y = right.Top
	a.settingToggle(c, RECT{right.Left, y, right.Right, y + rowHeight}, "Adaptive sampling", "Reduce nonessential work while minimized", a.config.Sampling.Adaptive, "toggle-adaptive")
	y += rowStep
	a.settingToggle(c, RECT{right.Left, y, right.Right, y + rowHeight}, "Reduced motion", "Use immediate values and restrained transitions", a.config.Appearance.ReducedMotion, "toggle-motion")
	y += rowStep
	a.settingToggle(c, RECT{right.Left, y, right.Right, y + rowHeight}, "Always on top", "Keep the main window above normal windows", a.config.Window.AlwaysTop, "toggle-top")
	y += rowStep
	a.settingToggle(c, RECT{right.Left, y, right.Right, y + rowHeight}, "Close to tray", "Closing hides Kerneon; right-click tray icon exits", a.config.Window.CloseToTray, "toggle-tray")
	y += rowStep
	a.settingToggle(c, RECT{right.Left, y, right.Right, y + rowHeight}, "Game Focus", "Reduce Kerneon's own background work during games", a.config.Gaming.GameFocus, "toggle-gamefocus")
	y += rowStep
	a.settingToggle(c, RECT{right.Left, y, right.Right, y + rowHeight}, "Technical language", "Off: plain English · On: live ranges, APIs, thresholds and rollback detail", a.config.Appearance.TechnicalMode, "toggle-technical")
	if a.startupEnabled {
		y += rowStep
		a.settingToggle(c, RECT{right.Left, y, right.Right, y + rowHeight}, "Open at startup", "Start Kerneon automatically when you sign in to Windows", true, "toggle-startup")
	}
	bottom := r.Bottom - c.s(44)
	c.text("CREATED AND PUBLISHED BY "+strings.ToUpper(appPublisher), RECT{right.Left, bottom - c.s(28), right.Right, bottom - c.s(7)}, 7, 650, palette.Muted2, DT_CENTER|DT_VCENTER|DT_SINGLELINE)
	bw := (right.Right - right.Left - c.s(16)*2) / 3
	a.button(c, RECT{right.Left, bottom, right.Left + bw, bottom + c.s(40)}, "Open logs", "open-logs", 0, false)
	a.button(c, RECT{right.Left + bw + c.s(8), bottom, right.Left + 2*bw + c.s(8), bottom + c.s(40)}, "Export diagnostics", "export-diagnostics", 0, false)
	a.button(c, RECT{right.Right - bw, bottom, right.Right, bottom + c.s(40)}, "Restore defaults", "defaults", 0, false)
}

func (a *App) settingChoice(c *Canvas, r RECT, title, desc, value, action string) {
	c.rounded(r, 12, palette.Card)
	c.strokeRound(r, 12, palette.Border, 1)
	c.text(title, RECT{r.Left + c.s(14), r.Top + c.s(8), r.Right - c.s(176), r.Top + c.s(34)}, 9, 650, palette.Text, DT_LEFT|DT_VCENTER|DT_SINGLELINE)
	c.text(desc, RECT{r.Left + c.s(14), r.Top + c.s(36), r.Right - c.s(176), r.Bottom - c.s(8)}, 7, 450, palette.Muted, DT_LEFT|DT_VCENTER|DT_SINGLELINE|DT_END_ELLIPSIS)
	ctrl := RECT{r.Right - c.s(166), r.Top + c.s(18), r.Right - c.s(12), r.Bottom - c.s(18)}
	c.rounded(ctrl, 10, palette.CardHover)
	l := RECT{ctrl.Left, ctrl.Top, ctrl.Left + c.s(36), ctrl.Bottom}
	rr := RECT{ctrl.Right - c.s(36), ctrl.Top, ctrl.Right, ctrl.Bottom}
	c.text("‹", l, 14, 600, palette.Muted, DT_CENTER|DT_VCENTER|DT_SINGLELINE)
	c.text("›", rr, 14, 600, palette.Muted, DT_CENTER|DT_VCENTER|DT_SINGLELINE)
	c.mono(value, RECT{l.Right, ctrl.Top, rr.Left, ctrl.Bottom}, 8, 600, palette.Text, DT_CENTER|DT_VCENTER|DT_SINGLELINE|DT_END_ELLIPSIS)
	a.hit(l, action, -1)
	a.hit(rr, action, 1)
}
func (a *App) settingToggle(c *Canvas, r RECT, title, desc string, on bool, action string) {
	c.rounded(r, 12, palette.Card)
	c.strokeRound(r, 12, palette.Border, 1)
	c.text(title, RECT{r.Left + c.s(14), r.Top + c.s(8), r.Right - c.s(92), r.Top + c.s(34)}, 9, 650, palette.Text, DT_LEFT|DT_VCENTER|DT_SINGLELINE)
	c.text(desc, RECT{r.Left + c.s(14), r.Top + c.s(36), r.Right - c.s(92), r.Bottom - c.s(8)}, 7, 450, palette.Muted, DT_LEFT|DT_VCENTER|DT_SINGLELINE|DT_END_ELLIPSIS)
	sw := RECT{r.Right - c.s(68), r.Top + c.s(25), r.Right - c.s(14), r.Top + c.s(53)}
	a.toggle(c, sw, "", on, action)
}
func (a *App) toggle(c *Canvas, r RECT, label string, on bool, action string) {
	progress := a.animate("toggle:"+action, boolFloat(on), 38*time.Millisecond)
	bg := blend(palette.Muted2, palette.Cyan, progress)
	if label != "" {
		c.text(label, RECT{r.Left, r.Top, r.Right - c.s(52), r.Bottom}, 8, 600, palette.Text, DT_LEFT|DT_VCENTER|DT_SINGLELINE)
		r.Left = r.Right - c.s(48)
	}
	hit := HitRegion{Rect: r, Action: action}
	press := a.pressAmount(hit)
	visual := r
	visualInset := int32(math.Round(float64(c.s(1)) * press))
	visual.Left, visual.Top, visual.Right, visual.Bottom = visual.Left+visualInset, visual.Top+visualInset, visual.Right-visualInset, visual.Bottom-visualInset
	c.rounded(visual, 24, blend(bg, palette.Cyan, press*0.1))
	cx := visual.Left + c.s(14) + int32(progress*float64(visual.Right-visual.Left-c.s(28)))
	c.circle(cx, (visual.Top+visual.Bottom)/2, c.s(9), palette.Text)
	a.hit(r, action, 0)
}
func (a *App) button(c *Canvas, r RECT, label, action string, value int, active bool) {
	key := fmt.Sprintf("button:%s:%d:%d:%d", action, value, r.Left, r.Top)
	hover := a.animate(key, boolFloat(a.pointIn(r) && action != ""), 20*time.Millisecond)
	bg := blend(palette.Surface, palette.CardHover, hover*0.72)
	color := palette.Text
	if active {
		bg = blend(bg, palette.Cyan, 0.18)
		color = palette.Cyan
	}
	press := a.pressAmount(HitRegion{Rect: r, Action: action, Value: value})
	visual := r
	inset := int32(math.Round(float64(c.s(1)) * press))
	visual.Left, visual.Top, visual.Right, visual.Bottom = visual.Left+inset, visual.Top+inset, visual.Right-inset, visual.Bottom-inset
	bg = blend(bg, palette.Cyan, press*0.08)
	c.roundedFill(visual, 11, bg)
	if hover > 0.04 || active {
		borderMix := hover * 0.20
		if active && borderMix < 0.30 {
			borderMix = 0.30
		}
		c.strokeRound(visual, 11, blend(palette.Border, palette.Cyan, borderMix), 1)
	}
	labelRect := visual
	labelRect.Top += int32(math.Round(float64(c.s(1)) * press))
	c.text(label, labelRect, 8, 600, color, DT_CENTER|DT_VCENTER|DT_SINGLELINE)
	if action != "" {
		a.hit(r, action, value)
	}
}

func boolFloat(value bool) float64 {
	if value {
		return 1
	}
	return 0
}

func (a *App) animate(key string, target float64, tau time.Duration) float64 {
	if a.config.Appearance.ReducedMotion || tau <= 0 {
		a.animationProgress[key] = target
		return target
	}
	current := a.animationProgress[key]
	delta := a.animationDelta
	if delta <= 0 {
		delta = time.Second / 60
	}
	alpha := 1 - math.Exp(-delta.Seconds()/tau.Seconds())
	current += (target - current) * alpha
	if math.Abs(target-current) < 0.005 {
		current = target
	}
	a.animationProgress[key] = current
	return current
}
func (a *App) hit(r RECT, action string, value int) {
	a.hits = append(a.hits, HitRegion{r, action, value})
}

// modalHitShield is registered before child controls. Hit testing walks the
// list backwards, so a child added later wins, blank panel space is inert, and
// only the dimmed area outside the panel dismisses the modal.
func (a *App) modalHitShield(bounds, panel RECT, dismissAction string) {
	a.hit(bounds, dismissAction, silentHitValue)
	a.hit(panel, "", silentHitValue)
}
func (a *App) pointIn(r RECT) bool {
	return a.hoverValid && a.hover.X >= r.Left && a.hover.X < r.Right && a.hover.Y >= r.Top && a.hover.Y < r.Bottom
}

func (a *App) drawCompact(c *Canvas) {
	pad := c.s(16)
	drawKerneonMark(c, pad+c.s(11), c.s(23), 12, palette.BG)
	c.text("Kerneon", RECT{pad + c.s(28), c.s(10), c.W - pad, c.s(38)}, 10, 700, palette.Text, DT_LEFT|DT_VCENTER|DT_SINGLELINE)
	close := RECT{c.W - c.s(88), c.s(8), c.W - pad, c.s(36)}
	a.button(c, close, "Expand", "compact", 0, false)
	cards := []struct {
		label, value string
		color        uint32
	}{{"CPU", fmt.Sprintf("%.1f%%", a.display.CPU), palette.Blue}, {"GPU", availabilityPercent(a.snapshot.GPU.Available, a.display.GPU), palette.Violet}, {"Memory", fmt.Sprintf("%.1f%%", a.display.Memory), palette.Cyan}, {"Download", a.downFormatter.Format(a.display.Down, time.Now()), palette.Green}, {"Upload", a.upFormatter.Format(a.display.Up, time.Now()), palette.Violet}, {"Ping", latencyLabel(a.snapshot.Network), latencyColor(a.snapshot.Network)}}
	gap := c.s(7)
	w := (c.W - pad*2 - gap*2) / 3
	for i, v := range cards {
		x := pad + int32(i%3)*(w+gap)
		y := c.s(48) + int32(i/3)*(c.s(72)+gap)
		r := RECT{x, y, x + w, y + c.s(72)}
		c.rounded(r, 11, palette.Card)
		c.rounded(RECT{r.Left + c.s(10), r.Top + c.s(11), r.Left + c.s(13), r.Bottom - c.s(11)}, 3, v.color)
		c.text(v.label, RECT{r.Left + c.s(20), r.Top + c.s(6), r.Right - c.s(7), r.Top + c.s(29)}, 7, 700, palette.Muted, DT_LEFT|DT_VCENTER|DT_SINGLELINE)
		c.mono(v.value, RECT{r.Left + c.s(20), r.Top + c.s(31), r.Right - c.s(7), r.Bottom - c.s(7)}, 10, 650, palette.Text, DT_LEFT|DT_VCENTER|DT_SINGLELINE|DT_END_ELLIPSIS)
	}
}

func availabilityPercent(ok bool, value float64) string {
	if !ok {
		return "—"
	}
	return fmt.Sprintf("%4.1f%%", value)
}
func gpuSub(g GPUData) string {
	if !g.Available {
		return "Detecting Windows GPU provider…"
	}
	return core.FormatBytes(uint64(g.DedicatedUsed)) + " dedicated"
}
func latencyLabel(n NetworkData) string {
	if !n.PingOK && n.LatencyMs <= 0 {
		return "— ms"
	}
	return fmt.Sprintf("%.0f ms", n.LatencyMs)
}
func latencyColor(n NetworkData) uint32 {
	if !n.PingOK {
		return palette.Muted2
	}
	if n.PacketLoss >= 3 || n.LatencyMs >= 120 {
		return palette.Red
	}
	if n.LatencyMs >= 60 {
		return palette.Amber
	}
	return palette.Green
}
func linkLabel(v uint64) string {
	if v == 0 {
		return "Link speed unavailable"
	}
	if v >= 1_000_000_000 {
		return fmt.Sprintf("%.1f Gbps", float64(v)/1e9)
	}
	return fmt.Sprintf("%.0f Mbps", float64(v)/1e6)
}
func shortBytes(v float64) string {
	if v < 0 {
		v = 0
	}
	return core.FormatBytes(uint64(v))
}
func durationLabel(seconds int) string {
	if seconds >= 60 && seconds%60 == 0 {
		return fmt.Sprintf("%d min", seconds/60)
	}
	return fmt.Sprintf("%d sec", seconds)
}
func formatDuration(d time.Duration) string {
	if d < 0 {
		return "—"
	}
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	mins := int(d.Minutes()) % 60
	if days > 0 {
		return fmt.Sprintf("%dd %dh", days, hours)
	}
	return fmt.Sprintf("%dh %02dm", hours, mins)
}
func fallback(v, f string) string {
	if strings.TrimSpace(v) == "" {
		return f
	}
	return strings.TrimSpace(v)
}
func cpuColor(v float64) uint32 {
	if v >= 80 {
		return palette.Amber
	}
	if v >= 40 {
		return palette.Cyan
	}
	return palette.Text
}
func blend(a, b uint32, t float64) uint32 {
	t = clampFloat(t, 0, 1)
	ar, ag, ab := float64(a&255), float64((a>>8)&255), float64((a>>16)&255)
	br, bg, bb := float64(b&255), float64((b>>8)&255), float64((b>>16)&255)
	return rgb(uint32(ar+(br-ar)*t), uint32(ag+(bg-ag)*t), uint32(ab+(bb-ab)*t))
}
func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
func min32(a, b int32) int32 {
	if a < b {
		return a
	}
	return b
}
func max32(a, b int32) int32 {
	if a > b {
		return a
	}
	return b
}
