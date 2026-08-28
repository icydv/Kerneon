package core

import (
	"fmt"
	"math"
	"time"
)

type UnitMode string

const (
	UnitAuto  UnitMode = "auto"
	UnitBits  UnitMode = "bits"
	UnitBytes UnitMode = "bytes"
)

type RateFormatter struct {
	Mode      UnitMode
	unit      int
	candidate int
	since     time.Time
}

var bitUnits = []string{"bps", "Kbps", "Mbps", "Gbps", "Tbps"}
var byteUnits = []string{"B/s", "KB/s", "MB/s", "GB/s", "TB/s"}

func (f *RateFormatter) Format(bytesPerSecond float64, now time.Time) string {
	if bytesPerSecond < 0 || math.IsNaN(bytesPerSecond) || math.IsInf(bytesPerSecond, 0) {
		bytesPerSecond = 0
	}
	base := bytesPerSecond * 8
	units := bitUnits
	if f.Mode == UnitBytes {
		base = bytesPerSecond
		units = byteUnits
	}
	wanted := 0
	for wanted < len(units)-1 && base >= math.Pow(1000, float64(wanted+1)) {
		wanted++
	}
	if f.Mode == UnitAuto || f.Mode == "" {
		f.applyHysteresis(base, wanted, now)
	} else {
		f.unit = wanted
	}
	div := math.Pow(1000, float64(f.unit))
	value := base / div
	precision := 0
	if value < 10 && f.unit > 0 {
		precision = 2
	} else if value < 100 && f.unit > 0 {
		precision = 1
	}
	return fmt.Sprintf("%.*f %s", precision, value, units[f.unit])
}

func (f *RateFormatter) applyHysteresis(base float64, wanted int, now time.Time) {
	if wanted < f.unit {
		lowerBoundary := math.Pow(1000, float64(f.unit)) * 0.88
		if base >= lowerBoundary {
			wanted = f.unit
		}
	} else if wanted > f.unit {
		upperBoundary := math.Pow(1000, float64(f.unit+1)) * 1.08
		if base < upperBoundary {
			wanted = f.unit
		}
	}
	if wanted == f.unit {
		f.candidate, f.since = wanted, time.Time{}
		return
	}
	if wanted != f.candidate {
		f.candidate, f.since = wanted, now
		return
	}
	if !f.since.IsZero() && now.Sub(f.since) >= 650*time.Millisecond {
		f.unit, f.since = wanted, time.Time{}
	}
}

func FormatBytes(value uint64) string {
	v := float64(value)
	units := []string{"B", "KB", "MB", "GB", "TB", "PB"}
	i := 0
	for v >= 1000 && i < len(units)-1 {
		v /= 1000
		i++
	}
	if i == 0 {
		return fmt.Sprintf("%.0f %s", v, units[i])
	}
	return fmt.Sprintf("%.1f %s", v, units[i])
}

type GraphScaler struct {
	Value      float64
	lastGrowth time.Time
}

var scaleSteps = []float64{1, 2, 5, 10, 20, 50, 100, 200, 500, 1_000, 2_000, 5_000, 10_000, 20_000, 50_000, 100_000}

func (g *GraphScaler) Update(peak float64, now time.Time) float64 {
	if peak < 0 || math.IsNaN(peak) || math.IsInf(peak, 0) {
		peak = 0
	}
	target := niceCeiling(math.Max(1, peak*1.12))
	if g.Value == 0 {
		g.Value, g.lastGrowth = target, now
		return g.Value
	}
	if target > g.Value {
		g.Value, g.lastGrowth = target, now
	} else if target < g.Value*0.55 && now.Sub(g.lastGrowth) > 4*time.Second {
		g.Value = target
	}
	return g.Value
}

func niceCeiling(value float64) float64 {
	if value <= 1 {
		return 1
	}
	power := math.Pow(1000, math.Floor(math.Log(value)/math.Log(1000)))
	normal := value / power
	for _, step := range scaleSteps {
		if step >= normal {
			return step * power
		}
	}
	return math.Ceil(normal) * power
}
