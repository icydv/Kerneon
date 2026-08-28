//go:build windows

package main

import (
	"fmt"
	"math"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"kerneon/core"
)

var palette = struct{ BG, Nav, Surface, Card, CardHover, Border, Highlight, Shadow, Grid, Text, Muted, Muted2, Cyan, Violet, Blue, Green, Amber, Red uint32 }{
	BG: rgb(14, 15, 17), Nav: rgb(16, 17, 19), Surface: rgb(22, 23, 26), Card: rgb(26, 27, 31), CardHover: rgb(31, 32, 37), Border: rgb(49, 51, 58), Highlight: rgb(67, 69, 78), Shadow: rgb(7, 8, 10), Grid: rgb(37, 38, 43), Text: rgb(241, 241, 244), Muted: rgb(157, 162, 173), Muted2: rgb(99, 104, 116), Cyan: rgb(145, 184, 239), Violet: rgb(181, 165, 235), Blue: rgb(134, 173, 232), Green: rgb(126, 202, 171), Amber: rgb(224, 183, 111), Red: rgb(227, 126, 142),
}

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
	HDC       uintptr
	W, H, DPI int32
	app       *App
}

func (c *Canvas) s(v int32) int32 { return int32(float64(v) * float64(c.DPI) / 96) }
func (c *Canvas) fill(r RECT, color uint32) {
	procFillRect.Call(c.HDC, uintptr(unsafe.Pointer(&r)), cachedBrush(color))
}
func (c *Canvas) rounded(r RECT, radius int32, color uint32) {
	if (color == palette.Card || color == palette.Surface || color == palette.CardHover) && r.Bottom-r.Top > c.s(24) {
		shadow := RECT{r.Left, r.Top + c.s(2), r.Right, r.Bottom + c.s(2)}
		c.roundedFill(shadow, radius, palette.Shadow)
		if radius < 16 {
			radius = 16
		}
	}
	c.roundedFill(r, radius, color)
	if (color == palette.Card || color == palette.Surface || color == palette.CardHover) && r.Right-r.Left > c.s(40) {
		c.line(r.Left+c.s(radius), r.Top+c.s(1), r.Right-c.s(radius), r.Top+c.s(1), palette.Highlight, 1)
	}
}
func (c *Canvas) roundedFill(r RECT, radius int32, color uint32) {
	br := cachedBrush(color)
	oldBr, _, _ := procSelectObject.Call(c.HDC, br)
	null, _, _ := procGetStockObject.Call(NULL_PEN)
	oldPen, _, _ := procSelectObject.Call(c.HDC, null)
	procRoundRect.Call(c.HDC, uintptr(r.Left), uintptr(r.Top), uintptr(r.Right), uintptr(r.Bottom), uintptr(c.s(radius)), uintptr(c.s(radius)))
	procSelectObject.Call(c.HDC, oldPen)
	procSelectObject.Call(c.HDC, oldBr)
}
func (c *Canvas) strokeRound(r RECT, radius int32, color uint32, width int32) {
	if color == palette.Border && radius < 16 {
		radius = 16
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
	pen := cachedPen(color, c.s(width))
	old, _, _ := procSelectObject.Call(c.HDC, pen)
	procPolyline.Call(c.HDC, uintptr(unsafe.Pointer(&points[0])), uintptr(len(points)))
	procSelectObject.Call(c.HDC, old)
}
func (c *Canvas) arc(cx, cy, radius int32, startDeg, endDeg float64, color uint32, width int32) {
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
	br := cachedBrush(color)
	oldBr, _, _ := procSelectObject.Call(c.HDC, br)
	null, _, _ := procGetStockObject.Call(NULL_PEN)
	oldPen, _, _ := procSelectObject.Call(c.HDC, null)
	procEllipse.Call(c.HDC, uintptr(x-r), uintptr(y-r), uintptr(x+r), uintptr(y+r))
	procSelectObject.Call(c.HDC, oldPen)
	procSelectObject.Call(c.HDC, oldBr)
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
	c := &Canvas{HDC: a.back.DC, W: rc.Right, H: rc.Bottom, DPI: a.dpi, app: a}
	c.fill(RECT{0, 0, c.W, c.H}, palette.BG)
	a.hits = a.hits[:0]
	if a.compact {
		a.drawCompact(c)
	} else {
		a.drawShell(c)
	}
	procBitBlt.Call(hdc, 0, 0, uintptr(c.W), uintptr(c.H), c.HDC, 0, 0, SRCCOPY)
}

func (a *App) drawShell(c *Canvas) {
	navW := c.s(206)
	c.fill(RECT{0, 0, navW, c.H}, palette.Nav)
	c.line(navW-1, 0, navW-1, c.H, palette.Border, 1)
	a.drawBrand(c, RECT{c.s(20), c.s(18), navW - c.s(16), c.s(80)})
	items := []struct {
		key, label string
		color      uint32
	}{{"overview", "Overview", palette.Cyan}, {"cpu", "CPU", palette.Blue}, {"gpu", "GPU", palette.Violet}, {"memory", "Memory", palette.Cyan}, {"storage", "Storage", palette.Amber}, {"network", "Network", palette.Green}, {"processes", "Processes", palette.Blue}, {"gaming", "Gaming", palette.Violet}, {"insights", "Insights", palette.Cyan}, {"history", "History", palette.Blue}, {"alerts", "Alerts", palette.Amber}, {"system", "System", palette.Muted}}
	y := c.s(92)
	itemH := c.s(36)
	gap := c.s(2)
	for _, it := range items {
		r := RECT{c.s(12), y, navW - c.s(12), y + itemH}
		selected := a.page == it.key
		if selected {
			c.rounded(r, 10, palette.CardHover)
			c.rounded(RECT{r.Left, r.Top + c.s(9), r.Left + c.s(3), r.Bottom - c.s(9)}, 3, it.color)
		}
		c.text(it.label, RECT{r.Left + c.s(22), r.Top, r.Right - c.s(8), r.Bottom}, 11, func() int32 {
			if selected {
				return 650
			}
			return 500
		}(), func() uint32 {
			if selected {
				return palette.Text
			}
			return palette.Muted
		}(), DT_LEFT|DT_VCENTER|DT_SINGLELINE)
		a.hit(r, "nav:"+it.key, 0)
		y += itemH + gap
	}
	settings := RECT{c.s(12), c.H - c.s(51), navW - c.s(12), c.H - c.s(14)}
	if a.page == "settings" {
		c.rounded(settings, 10, palette.CardHover)
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
	body := RECT{navW + c.s(28), c.s(94), c.W - c.s(28), c.H - c.s(22)}
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
	case "history":
		a.drawHistory(c, body)
	case "alerts":
		a.drawAlerts(c, body)
	case "system":
		a.drawSystem(c, body)
	case "settings":
		a.drawSettings(c, body)
	default:
		a.drawOverview(c, body)
	}
}

func (a *App) drawBrand(c *Canvas, r RECT) {
	cx, cy := r.Left+c.s(19), r.Top+c.s(24)
	c.arc(cx, cy, c.s(12), 39, 321, palette.Text, 2)
	c.circle(cx, cy, c.s(2), palette.Blue)
	c.text("Kerneon", RECT{r.Left + c.s(50), r.Top, r.Right, r.Top + c.s(31)}, 13, 680, palette.Text, DT_LEFT|DT_VCENTER|DT_SINGLELINE)
	c.text("Understand your PC", RECT{r.Left + c.s(50), r.Top + c.s(27), r.Right, r.Top + c.s(48)}, 8, 500, palette.Muted2, DT_LEFT|DT_VCENTER|DT_SINGLELINE)
}

func (a *App) drawPageHeader(c *Canvas, r RECT) {
	titles := map[string][2]string{"overview": {"Overview", "What your PC is doing right now"}, "cpu": {"CPU", "Processor load, clocks and system activity"}, "gpu": {"GPU", "Graphics engines and memory"}, "memory": {"Memory", "Physical and committed memory, explained"}, "storage": {"Storage", "Space and performance are different things"}, "network": {"Network", "Live throughput and connection quality"}, "processes": {"Processes", "Find what is consuming your PC"}, "gaming": {"Gaming", "Focused telemetry with Kerneon out of the way"}, "insights": {"Insights", "Factual observations from local telemetry"}, "history": {"History", "Synchronized resource timelines"}, "alerts": {"Alerts", "Sustained conditions, not noisy samples"}, "system": {"System", "Hardware and Windows at a glance"}, "settings": {"Settings", "Sampling, behavior and privacy"}}
	t := titles[a.page]
	if t[0] == "" {
		t = titles["overview"]
	}
	c.text(t[0], RECT{r.Left + c.s(28), c.s(20), r.Right - c.s(300), c.s(52)}, 20, 700, palette.Text, DT_LEFT|DT_VCENTER|DT_SINGLELINE)
	c.text(t[1], RECT{r.Left + c.s(28), c.s(50), r.Right - c.s(300), c.s(76)}, 10, 450, palette.Muted, DT_LEFT|DT_VCENTER|DT_SINGLELINE)
	capBtn := RECT{r.Right - c.s(264), c.s(25), r.Right - c.s(132), c.s(64)}
	a.button(c, capBtn, "Capture 60s", "capture-incident", 0, false)
	mini := RECT{r.Right - c.s(120), c.s(25), r.Right - c.s(28), c.s(64)}
	a.button(c, mini, "Compact", "compact", 0, false)
}

func (a *App) drawOverview(c *Canvas, r RECT) {
	p := core.ExplainPressure(core.PressureInput{CPU: a.display.CPU, GPU: a.display.GPU, Memory: a.display.Memory, Disk: a.display.Disk, Latency: a.display.Latency, PacketLoss: a.snapshot.Network.PacketLoss})
	col := palette.Green
	if p.Severity == 1 {
		col = palette.Cyan
	} else if p.Severity > 1 {
		col = palette.Amber
	}
	pressure := RECT{r.Left, r.Top, r.Right, r.Top + c.s(66)}
	c.rounded(pressure, 14, palette.Surface)
	c.strokeRound(pressure, 14, palette.Border, 1)
	c.circle(pressure.Left+c.s(23), pressure.Top+c.s(25), c.s(5), col)
	c.text(p.Title, RECT{pressure.Left + c.s(39), pressure.Top + c.s(10), pressure.Right - c.s(18), pressure.Top + c.s(32)}, 10, 650, col, DT_LEFT|DT_VCENTER|DT_SINGLELINE)
	c.text(p.Explanation, RECT{pressure.Left + c.s(39), pressure.Top + c.s(32), pressure.Right - c.s(18), pressure.Bottom - c.s(7)}, 9, 450, palette.Muted, DT_LEFT|DT_VCENTER|DT_SINGLELINE|DT_END_ELLIPSIS)
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

func (a *App) drawMultiGraph(c *Canvas, r RECT, title string, series []graphSeries, fixedMax float64, key string) {
	c.rounded(r, 14, palette.Surface)
	c.strokeRound(r, 14, palette.Border, 1)
	c.text(title, RECT{r.Left + c.s(16), r.Top + c.s(8), r.Right - c.s(200), r.Top + c.s(32)}, 8, 700, palette.Muted, DT_LEFT|DT_VCENTER|DT_SINGLELINE)
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
	history := a.snapshot.History
	seconds := a.config.Appearance.GraphSeconds
	cut := time.Now().Add(-time.Duration(seconds) * time.Second)
	filtered := history[:0]
	for _, h := range history {
		if !h.At.Before(cut) {
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
		maxV = sc.Update(peak, time.Now())
	}
	start, end := cut, time.Now()
	for _, s := range series {
		pts := make([]POINT, 0, len(filtered))
		for _, h := range filtered {
			x := plot.Left + int32(core.MapTime(h.At, start, end, float64(plot.Right-plot.Left)))
			v := clampFloat(s.Value(h), 0, maxV)
			y := plot.Bottom - int32(v/maxV*float64(plot.Bottom-plot.Top))
			pts = append(pts, POINT{x, y})
		}
		c.polyline(pts, s.Color, 2)
	}
	if a.pointIn(plot) {
		c.line(a.hover.X, plot.Top, a.hover.X, plot.Bottom, palette.Muted2, 1)
		closest := filtered[0]
		best := int32(math.MaxInt32)
		for _, h := range filtered {
			x := plot.Left + int32(core.MapTime(h.At, start, end, float64(plot.Right-plot.Left)))
			d := x - a.hover.X
			if d < 0 {
				d = -d
			}
			if d < best {
				best = d
				closest = h
			}
		}
		tipW := c.s(148)
		tx := a.hover.X + c.s(10)
		if tx+tipW > plot.Right {
			tx = a.hover.X - tipW - c.s(10)
		}
		tip := RECT{tx, plot.Top + c.s(8), tx + tipW, plot.Top + c.s(34) + int32(len(series))*c.s(17)}
		c.rounded(tip, 9, palette.CardHover)
		c.strokeRound(tip, 9, palette.Border, 1)
		c.mono(closest.At.Format("15:04:05"), RECT{tip.Left + c.s(10), tip.Top + c.s(4), tip.Right - c.s(8), tip.Top + c.s(23)}, 8, 600, palette.Text, DT_LEFT|DT_VCENTER|DT_SINGLELINE)
		yy := tip.Top + c.s(23)
		for _, s := range series {
			c.circle(tip.Left+c.s(11), yy+c.s(8), c.s(2), s.Color)
			c.mono(fmt.Sprintf("%s  %.1f", s.Name, s.Value(closest)), RECT{tip.Left + c.s(20), yy, tip.Right - c.s(8), yy + c.s(16)}, 7, 500, palette.Muted, DT_LEFT|DT_VCENTER|DT_SINGLELINE)
			yy += c.s(17)
		}
	}
}

func (a *App) sparkline(c *Canvas, r RECT, key string, color uint32, current float64) {
	history := a.snapshot.History
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

func (a *App) drawCPU(c *Canvas, r RECT) {
	stats := []stat{{"Utilisation", fmt.Sprintf("%.1f%%", a.display.CPU), "Total processor activity"}, {"Kernel activity", fmt.Sprintf("%.1f%%", a.snapshot.CPU.Kernel), "Windows and driver work"}, {"Current clock", fmt.Sprintf("%.0f MHz", a.snapshot.CPU.FrequencyMHz), fmt.Sprintf("Maximum %.0f MHz", a.snapshot.CPU.MaxMHz)}, {"Topology", fmt.Sprintf("%d cores · %d logical", a.snapshot.CPU.Cores, a.snapshot.CPU.Logical), a.snapshot.CPU.Model}}
	a.drawStats(c, r, stats, func(gr RECT) {
		a.drawMultiGraph(c, gr, "Processor activity", []graphSeries{{"Total", palette.Blue, func(h HistorySample) float64 { return h.CPU }}}, 100, "cpu")
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
		a.drawMultiGraph(c, gr, "Physical memory", []graphSeries{{"Used", palette.Cyan, func(h HistorySample) float64 { return h.Memory }}}, 100, "memory")
	}, []kv{{"Installed", core.FormatBytes(a.snapshot.Memory.Total)}, {"Paged pool", core.FormatBytes(a.snapshot.Memory.PagedPool)}, {"Non-paged pool", core.FormatBytes(a.snapshot.Memory.NonPagedPool)}, {"Note", "Cached memory is healthy and remains available."}})
}
func (a *App) drawStorage(c *Canvas, r RECT) {
	stats := []stat{{"Active time", fmt.Sprintf("%.1f%%", a.display.Disk), "Performance activity, not used space"}, {"Read", shortBytes(a.snapshot.Disk.ReadBps) + "/s", "All physical disks"}, {"Write", shortBytes(a.snapshot.Disk.WriteBps) + "/s", "All physical disks"}, {"Latency", fmt.Sprintf("%.1f ms", a.snapshot.Disk.LatencyMs), fmt.Sprintf("Queue depth %.2f", a.snapshot.Disk.Queue)}}
	extra := []kv{{"System volume free", core.FormatBytes(a.snapshot.Disk.Free)}, {"System volume size", core.FormatBytes(a.snapshot.Disk.Total)}, {"Volumes", fmt.Sprint(len(a.snapshot.Disk.Volumes))}, {"Health sensors", "Unavailable without privileged/vendor provider"}}
	a.drawStats(c, r, stats, func(gr RECT) {
		a.drawMultiGraph(c, gr, "Disk activity", []graphSeries{{"Active", palette.Amber, func(h HistorySample) float64 { return h.Disk }}}, 100, "disk")
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
	cards := []stat{{"Download", a.downFormatter.Format(a.display.Down, now), "Peak " + a.downFormatter.Format(a.snapshot.Network.PeakDown, now)}, {"Upload", a.upFormatter.Format(a.display.Up, now), "Peak " + a.upFormatter.Format(a.snapshot.Network.PeakUp, now)}, {"Latency", latencyLabel(a.snapshot.Network), fmt.Sprintf("%.1f ms jitter · %.0f%% loss", a.snapshot.Network.JitterMs, a.snapshot.Network.PacketLoss)}, {"Link load", fmt.Sprintf("%.2f%%", a.snapshot.Network.Utilization), linkLabel(a.snapshot.Network.LinkDown)}}
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
	columns := int32(4)
	cardW := (r.Right - r.Left - gap*(columns-1)) / columns
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
		{"GPU", "—", "Per-process attribution unavailable", palette.Violet},
		{"Network", "—", "Per-process attribution unavailable", palette.Green},
		{"Threads", fmt.Sprint(p.Threads), "Active execution threads", palette.Violet},
		{"Handles", fmt.Sprint(p.Handles), "Open operating-system objects", palette.Green},
		{"Running for", started, startedSub, palette.Muted},
	}
	for i, metric := range metrics {
		x := r.Left + int32(i%int(columns))*(cardW+gap)
		yy := y + int32(i/int(columns))*(c.s(104)+gap)
		a.metricCard(c, RECT{x, yy, x + cardW, yy + c.s(104)}, metric.label, metric.value, metric.sub, metric.color, "", 0)
	}

	panelTop := y + 2*(c.s(104)+gap) + c.s(4)
	panel := RECT{r.Left, panelTop, r.Right, r.Bottom}
	c.rounded(panel, 14, palette.Surface)
	c.strokeRound(panel, 14, palette.Border, 1)
	statusColor, statusText := palette.Green, "Live telemetry is available"
	if !available {
		statusColor, statusText = palette.Amber, "This process is no longer visible or cannot be inspected"
	}
	c.circle(panel.Left+c.s(24), panel.Top+c.s(25), c.s(4), statusColor)
	c.text(statusText, RECT{panel.Left + c.s(38), panel.Top + c.s(10), panel.Right - c.s(20), panel.Top + c.s(39)}, 10, 620, statusColor, DT_LEFT|DT_VCENTER|DT_SINGLELINE|DT_END_ELLIPSIS)
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
	c.text("Kerneon uses read-only Windows process APIs. It does not stop, suspend, prioritize, or modify applications.", RECT{panel.Left + c.s(20), panel.Top + c.s(43), panel.Right - c.s(20), panel.Top + c.s(70)}, 9, 450, palette.Muted, DT_LEFT|DT_VCENTER|DT_SINGLELINE|DT_END_ELLIPSIS)
	c.text(fmt.Sprintf("Parent  %s     ·     Visible child processes  %d", parentName, children), RECT{panel.Left + c.s(20), panel.Top + c.s(74), panel.Right - c.s(20), panel.Top + c.s(101)}, 8, 520, palette.Muted, DT_LEFT|DT_VCENTER|DT_SINGLELINE|DT_END_ELLIPSIS)
	actionY := panel.Bottom - c.s(50)
	copyPID := RECT{panel.Left + c.s(20), actionY, panel.Left + c.s(118), actionY + c.s(34)}
	a.button(c, copyPID, "Copy PID", "copy-process-pid", 0, false)
	copyPath := RECT{copyPID.Right + c.s(8), actionY, copyPID.Right + c.s(126), actionY + c.s(34)}
	a.button(c, copyPath, "Copy path", "copy-process-path", 0, false)
	open := RECT{copyPath.Right + c.s(8), actionY, copyPath.Right + c.s(150), actionY + c.s(34)}
	a.button(c, open, "Open location", "open-process-location", 0, false)
}

func (a *App) drawGaming(c *Canvas, r RECT) {
	game := ProcessMetric{Name: "No game detected"}
	for _, p := range a.snapshot.Processes {
		n := strings.ToLower(p.Name)
		if p.PID != uint32(syscall.Getpid()) && !strings.Contains(n, "system") && !strings.Contains(n, "idle") {
			game = p
			break
		}
	}
	hero := RECT{r.Left, r.Top, r.Right, r.Top + c.s(112)}
	c.rounded(hero, 15, palette.Surface)
	c.strokeRound(hero, 15, palette.Border, 1)
	c.text("Active game candidate", RECT{hero.Left + c.s(20), hero.Top + c.s(13), hero.Right - c.s(20), hero.Top + c.s(36)}, 8, 700, palette.Violet, DT_LEFT|DT_VCENTER|DT_SINGLELINE)
	c.text(game.Name, RECT{hero.Left + c.s(20), hero.Top + c.s(40), hero.Right - c.s(220), hero.Top + c.s(73)}, 18, 700, palette.Text, DT_LEFT|DT_VCENTER|DT_SINGLELINE|DT_END_ELLIPSIS)
	c.text("Automatic selection uses the most active user process. PresentMon frame capture is not bundled.", RECT{hero.Left + c.s(20), hero.Top + c.s(76), hero.Right - c.s(20), hero.Bottom - c.s(10)}, 8, 450, palette.Muted, DT_LEFT|DT_VCENTER|DT_SINGLELINE|DT_END_ELLIPSIS)
	focus := RECT{hero.Right - c.s(190), hero.Top + c.s(35), hero.Right - c.s(20), hero.Top + c.s(76)}
	a.toggle(c, focus, "Game Focus", a.config.Gaming.GameFocus, "toggle-gamefocus")
	body := RECT{r.Left, hero.Bottom + c.s(12), r.Right, r.Bottom}
	stats := []stat{{"Game CPU", fmt.Sprintf("%.1f%%", game.CPU), "Process utilisation"}, {"Game memory", core.FormatBytes(game.WorkingSet), "Working set"}, {"System GPU", availabilityPercent(a.snapshot.GPU.Available, a.display.GPU), "Windows GPU engines"}, {"Latency", latencyLabel(a.snapshot.Network), "Current ping target"}}
	gap := c.s(10)
	w := (body.Right - body.Left - gap*3) / 4
	for i, s := range stats {
		cr := RECT{body.Left + int32(i)*(w+gap), body.Top, body.Left + int32(i)*(w+gap) + w, body.Top + c.s(94)}
		c.rounded(cr, 13, palette.Card)
		c.text(s.Title, RECT{cr.Left + c.s(14), cr.Top + c.s(9), cr.Right, cr.Top + c.s(32)}, 8, 700, palette.Muted, DT_LEFT|DT_VCENTER|DT_SINGLELINE)
		c.mono(s.Value, RECT{cr.Left + c.s(14), cr.Top + c.s(35), cr.Right - c.s(10), cr.Top + c.s(63)}, 14, 650, palette.Text, DT_LEFT|DT_VCENTER|DT_SINGLELINE)
		c.text(s.Sub, RECT{cr.Left + c.s(14), cr.Top + c.s(66), cr.Right - c.s(10), cr.Bottom}, 7, 450, palette.Muted, DT_LEFT|DT_VCENTER|DT_SINGLELINE)
	}
	gr := RECT{body.Left, body.Top + c.s(106), body.Right, body.Bottom}
	a.drawMultiGraph(c, gr, "Gaming headroom", []graphSeries{{"CPU", palette.Blue, func(h HistorySample) float64 { return h.CPU }}, {"GPU", palette.Violet, func(h HistorySample) float64 { return h.GPU }}}, 100, "gaming")
}

func (a *App) drawInsights(c *Canvas, r RECT) {
	pressure := core.ExplainPressure(core.PressureInput{CPU: a.display.CPU, GPU: a.display.GPU, Memory: a.display.Memory, Disk: a.display.Disk, Latency: a.display.Latency, PacketLoss: a.snapshot.Network.PacketLoss})
	items := []Event{{time.Now(), "pressure", pressure.Title, pressure.Explanation, pressure.Severity}, {time.Now(), "memory", "Memory has comfortable reclaimable capacity", fmt.Sprintf("%s is available; cached memory remains reusable by applications.", core.FormatBytes(a.snapshot.Memory.Available)), 0}, {time.Now(), "network", "Connection quality snapshot", fmt.Sprintf("Latency %.0f ms, jitter %.1f ms and recent loss %.0f%%.", a.snapshot.Network.LatencyMs, a.snapshot.Network.JitterMs, a.snapshot.Network.PacketLoss), func() int {
		if a.snapshot.Network.PacketLoss >= 3 {
			return 2
		}
		return 0
	}()}}
	if len(a.snapshot.Processes) > 0 {
		p := a.snapshot.Processes[0]
		items = append(items, Event{time.Now(), "process", "Top consumer: " + p.Name, fmt.Sprintf("%.1f%% CPU, %s RAM and %s/s disk activity.", p.CPU, core.FormatBytes(p.WorkingSet), shortBytes(p.ReadBps+p.WriteBps)), 0})
	}
	y := r.Top
	for _, it := range items {
		h := c.s(92)
		box := RECT{r.Left, y, r.Right, y + h}
		c.rounded(box, 13, palette.Card)
		c.strokeRound(box, 13, palette.Border, 1)
		col := palette.Cyan
		if it.Severity > 1 {
			col = palette.Amber
		}
		c.circle(box.Left+c.s(22), box.Top+c.s(25), c.s(4), col)
		c.text(strings.ToUpper(it.Title), RECT{box.Left + c.s(38), box.Top + c.s(11), box.Right - c.s(18), box.Top + c.s(38)}, 9, 700, col, DT_LEFT|DT_VCENTER|DT_SINGLELINE|DT_END_ELLIPSIS)
		c.text(it.Detail, RECT{box.Left + c.s(38), box.Top + c.s(39), box.Right - c.s(18), box.Bottom - c.s(10)}, 9, 450, palette.Muted, DT_LEFT|DT_WORDBREAK)
		y += h + c.s(10)
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

func (a *App) drawSettings(c *Canvas, r RECT) {
	left := RECT{r.Left, r.Top, (r.Left+r.Right)/2 - c.s(5), r.Bottom}
	right := RECT{left.Right + c.s(10), r.Top, r.Right, r.Bottom}
	y := left.Top
	a.settingChoice(c, RECT{left.Left, y, left.Right, y + c.s(78)}, "Network sampling", "Counter reads are independent from rendering", fmt.Sprintf("%d Hz", a.config.Sampling.NetworkHz), "network-hz")
	y += c.s(88)
	a.settingChoice(c, RECT{left.Left, y, left.Right, y + c.s(78)}, "Graph frame rate", "Visible graphs stop repainting when minimized", fmt.Sprintf("%d FPS", a.config.Sampling.GraphFPS), "graph-fps")
	y += c.s(88)
	a.settingChoice(c, RECT{left.Left, y, left.Right, y + c.s(78)}, "Graph history", "Recent high-resolution window", durationLabel(a.config.Appearance.GraphSeconds), "graph-seconds")
	y += c.s(88)
	units := map[core.UnitMode]string{core.UnitAuto: "Auto", core.UnitBits: "Bits/sec", core.UnitBytes: "Bytes/sec"}[a.config.Network.Units]
	a.settingChoice(c, RECT{left.Left, y, left.Right, y + c.s(78)}, "Throughput units", "Auto uses hysteresis to avoid unit flicker", units, "units")
	y += c.s(88)
	a.settingChoice(c, RECT{left.Left, y, left.Right, y + c.s(78)}, "Ping cadence", "Runs independently from bandwidth sampling", func() string {
		if a.config.Network.PingSeconds == 0 {
			return "Off"
		}
		return fmt.Sprintf("Every %ds", a.config.Network.PingSeconds)
	}(), "ping")
	y = right.Top
	a.settingToggle(c, RECT{right.Left, y, right.Right, y + c.s(78)}, "Adaptive sampling", "Reduce nonessential work while minimized", a.config.Sampling.Adaptive, "toggle-adaptive")
	y += c.s(88)
	a.settingToggle(c, RECT{right.Left, y, right.Right, y + c.s(78)}, "Reduced motion", "Use immediate values and restrained transitions", a.config.Appearance.ReducedMotion, "toggle-motion")
	y += c.s(88)
	a.settingToggle(c, RECT{right.Left, y, right.Right, y + c.s(78)}, "Always on top", "Keep the main window above normal windows", a.config.Window.AlwaysTop, "toggle-top")
	y += c.s(88)
	a.settingToggle(c, RECT{right.Left, y, right.Right, y + c.s(78)}, "Close to tray", "Closing hides Kerneon; right-click tray icon exits", a.config.Window.CloseToTray, "toggle-tray")
	y += c.s(88)
	a.settingToggle(c, RECT{right.Left, y, right.Right, y + c.s(78)}, "Game Focus", "Reduce Kerneon's own background work during games", a.config.Gaming.GameFocus, "toggle-gamefocus")
	bottom := r.Bottom - c.s(44)
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
	bg := palette.Muted2
	if on {
		bg = palette.Cyan
	}
	if label != "" {
		c.text(label, RECT{r.Left, r.Top, r.Right - c.s(52), r.Bottom}, 8, 600, palette.Text, DT_LEFT|DT_VCENTER|DT_SINGLELINE)
		r.Left = r.Right - c.s(48)
	}
	c.rounded(r, 24, bg)
	cx := r.Left + c.s(14)
	if on {
		cx = r.Right - c.s(14)
	}
	c.circle(cx, (r.Top+r.Bottom)/2, c.s(9), palette.Text)
	a.hit(r, action, 0)
}
func (a *App) button(c *Canvas, r RECT, label, action string, value int, active bool) {
	bg := palette.Card
	if active || a.pointIn(r) {
		bg = palette.CardHover
	}
	c.rounded(r, 10, bg)
	c.strokeRound(r, 10, palette.Border, 1)
	c.text(label, r, 8, 600, func() uint32 {
		if active {
			return palette.Cyan
		}
		return palette.Text
	}(), DT_CENTER|DT_VCENTER|DT_SINGLELINE)
	a.hit(r, action, value)
}
func (a *App) hit(r RECT, action string, value int) {
	a.hits = append(a.hits, HitRegion{r, action, value})
}
func (a *App) pointIn(r RECT) bool {
	return a.hoverValid && a.hover.X >= r.Left && a.hover.X < r.Right && a.hover.Y >= r.Top && a.hover.Y < r.Bottom
}

func (a *App) drawCompact(c *Canvas) {
	pad := c.s(16)
	c.text("Kerneon", RECT{pad, c.s(10), c.W - pad, c.s(38)}, 10, 700, palette.Text, DT_LEFT|DT_VCENTER|DT_SINGLELINE)
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
