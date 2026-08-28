package core

import "time"

type AlertState struct {
	AboveFor  time.Duration
	Cooldown  time.Duration
	aboveAt   time.Time
	lastFire  time.Time
	triggered bool
}

func (a *AlertState) Update(value, threshold, clearBelow float64, now time.Time) bool {
	if value >= threshold {
		if a.aboveAt.IsZero() {
			a.aboveAt = now
		}
		if !a.triggered && now.Sub(a.aboveAt) >= a.AboveFor && (a.lastFire.IsZero() || now.Sub(a.lastFire) >= a.Cooldown) {
			a.triggered, a.lastFire = true, now
			return true
		}
	} else if value <= clearBelow {
		a.aboveAt, a.triggered = time.Time{}, false
	}
	return false
}

type ProviderState struct {
	Failures int
	Disabled bool
	LastErr  string
}

func (p *ProviderState) Success() { p.Failures, p.Disabled, p.LastErr = 0, false, "" }
func (p *ProviderState) Failure(err error, limit int) {
	if limit < 1 {
		limit = 1
	}
	p.Failures++
	if err != nil {
		p.LastErr = err.Error()
	}
	if p.Failures >= limit {
		p.Disabled = true
	}
}

type ProcessRecord struct {
	PID       uint32
	Name      string
	FirstSeen time.Time
	LastSeen  time.Time
	ExitedAt  time.Time
}

type ProcessLedger struct{ Records map[uint32]ProcessRecord }

func (l *ProcessLedger) Update(now time.Time, active map[uint32]string) {
	if l.Records == nil {
		l.Records = make(map[uint32]ProcessRecord)
	}
	for pid, name := range active {
		r, ok := l.Records[pid]
		if !ok {
			r = ProcessRecord{PID: pid, Name: name, FirstSeen: now}
		}
		r.Name, r.LastSeen, r.ExitedAt = name, now, time.Time{}
		l.Records[pid] = r
	}
	for pid, r := range l.Records {
		if _, ok := active[pid]; !ok && r.ExitedAt.IsZero() {
			r.ExitedAt = now
			l.Records[pid] = r
		}
	}
}

func (l *ProcessLedger) Prune(before time.Time) {
	for pid, r := range l.Records {
		if !r.ExitedAt.IsZero() && r.ExitedAt.Before(before) {
			delete(l.Records, pid)
		}
	}
}

type PressureInput struct {
	CPU, GPU, Memory, Disk, Latency, PacketLoss float64
}

type Pressure struct {
	Key, Title, Explanation string
	Severity                int
}

func ExplainPressure(v PressureInput) Pressure {
	switch {
	case v.Memory >= 90:
		return Pressure{"memory", "Memory pressure is elevated", "Used memory is above 90%, leaving little headroom for applications.", 2}
	case v.GPU >= 95 && v.GPU > v.CPU+15:
		return Pressure{"gpu", "Likely GPU constrained", "GPU activity is near capacity while CPU headroom remains available.", 2}
	case v.CPU >= 95:
		return Pressure{"cpu", "CPU headroom is limited", "Processor activity is near capacity across the current sample window.", 2}
	case v.Disk >= 90:
		return Pressure{"disk", "Storage is under sustained load", "Disk active time is above 90%; storage may delay other work.", 2}
	case v.PacketLoss >= 3 || v.Latency >= 120:
		return Pressure{"network", "Network quality has degraded", "Latency or recent packet loss is high enough to affect interactive traffic.", 2}
	case v.GPU >= 65 && v.GPU >= v.CPU:
		return Pressure{"gpu", "GPU is the main system load", "Graphics activity is currently higher than the other measured resources.", 1}
	case v.CPU >= 65:
		return Pressure{"cpu", "CPU is the main system load", "Processor activity is currently the largest measured load.", 1}
	default:
		return Pressure{"comfortable", "System has comfortable headroom", "No measured resource is close to sustained capacity.", 0}
	}
}
