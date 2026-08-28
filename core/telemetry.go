package core

import (
	"math"
	"time"
)

// RateResult describes one monotonic-counter transition. Reset distinguishes a
// provider restart from a valid zero-rate sample so callers can avoid spikes.
type RateResult struct {
	Delta uint64
	Rate  float64
	Valid bool
	Reset bool
}

type RateTracker struct {
	value uint64
	at    time.Time
	ready bool
}

func (r *RateTracker) Reset() { *r = RateTracker{} }

func (r *RateTracker) Add(value uint64, at time.Time) RateResult {
	if !r.ready {
		r.value, r.at, r.ready = value, at, true
		return RateResult{}
	}
	dt := at.Sub(r.at).Seconds()
	prev := r.value
	r.value, r.at = value, at
	if dt <= 0 || dt < 1e-6 || math.IsNaN(dt) || math.IsInf(dt, 0) {
		return RateResult{}
	}
	var delta uint64
	if value >= prev {
		delta = value - prev
	} else if prev > math.MaxUint64/2 && value < math.MaxUint64/2 {
		delta = (math.MaxUint64 - prev) + value + 1
	} else {
		return RateResult{Reset: true}
	}
	return RateResult{Delta: delta, Rate: float64(delta) / dt, Valid: true}
}

type EMA struct {
	Tau   time.Duration
	value float64
	ready bool
}

func NewEMA(tau time.Duration) EMA { return EMA{Tau: tau} }
func (e *EMA) Reset()              { e.value, e.ready = 0, false }
func (e *EMA) Value() float64      { return e.value }

func (e *EMA) Update(value float64, elapsed time.Duration) float64 {
	if !e.ready || e.Tau <= 0 {
		e.value, e.ready = value, true
		return e.value
	}
	if elapsed <= 0 {
		return e.value
	}
	alpha := 1 - math.Exp(-elapsed.Seconds()/e.Tau.Seconds())
	if alpha > 1 {
		alpha = 1
	}
	e.value += alpha * (value - e.value)
	return e.value
}

type RollingAverage struct {
	values []float64
	next   int
	count  int
	sum    float64
}

func NewRollingAverage(capacity int) *RollingAverage {
	if capacity < 1 {
		capacity = 1
	}
	return &RollingAverage{values: make([]float64, capacity)}
}

func (r *RollingAverage) Add(value float64) float64 {
	if r.count < len(r.values) {
		r.count++
	} else {
		r.sum -= r.values[r.next]
	}
	r.values[r.next] = value
	r.sum += value
	r.next = (r.next + 1) % len(r.values)
	return r.Value()
}

func (r *RollingAverage) Value() float64 {
	if r.count == 0 {
		return 0
	}
	return r.sum / float64(r.count)
}

type Ring[T any] struct {
	values []T
	next   int
	count  int
}

func NewRing[T any](capacity int) *Ring[T] {
	if capacity < 1 {
		capacity = 1
	}
	return &Ring[T]{values: make([]T, capacity)}
}

func (r *Ring[T]) Add(value T) {
	r.values[r.next] = value
	r.next = (r.next + 1) % len(r.values)
	if r.count < len(r.values) {
		r.count++
	}
}

func (r *Ring[T]) Len() int { return r.count }

func (r *Ring[T]) Values(dst []T) []T {
	if cap(dst) < r.count {
		dst = make([]T, r.count)
	} else {
		dst = dst[:r.count]
	}
	start := r.next - r.count
	if start < 0 {
		start += len(r.values)
	}
	for i := 0; i < r.count; i++ {
		dst[i] = r.values[(start+i)%len(r.values)]
	}
	return dst
}

type Point struct {
	At    time.Time
	Value float64
}

type Bucket struct {
	Start time.Time
	Min   float64
	Max   float64
	Avg   float64
	Count int
}

func Aggregate(points []Point, interval time.Duration) []Bucket {
	if interval <= 0 || len(points) == 0 {
		return nil
	}
	result := make([]Bucket, 0, len(points))
	var current Bucket
	var sum float64
	for _, p := range points {
		start := p.At.Truncate(interval)
		if current.Count == 0 || !start.Equal(current.Start) {
			if current.Count > 0 {
				current.Avg = sum / float64(current.Count)
				result = append(result, current)
			}
			current = Bucket{Start: start, Min: p.Value, Max: p.Value, Count: 1}
			sum = p.Value
			continue
		}
		current.Count++
		sum += p.Value
		current.Min = math.Min(current.Min, p.Value)
		current.Max = math.Max(current.Max, p.Value)
	}
	current.Avg = sum / float64(current.Count)
	return append(result, current)
}

func MapTime(at, start, end time.Time, width float64) float64 {
	span := end.Sub(start).Seconds()
	if span <= 0 || width <= 0 {
		return 0
	}
	x := at.Sub(start).Seconds() / span * width
	return math.Max(0, math.Min(width, x))
}
