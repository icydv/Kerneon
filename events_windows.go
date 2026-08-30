//go:build windows

package main

import (
	"context"
	"encoding/xml"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"kerneon/core"
)

const (
	eventAll = iota
	eventCrashes
	eventHardware
	eventPerformance
)

type SystemEvent struct {
	At                                  time.Time
	RecordID                            int64
	ID, Level, Occurrences              int
	Source, Log, Category               string
	Title, Message, Impact, Next, RawID string
}

type EventSignificance struct {
	Score       int
	Label       string
	Explanation string
}

func eventSignificance(event SystemEvent) EventSignificance {
	base := map[string]int{
		"Noise": 6, "System": 18, "Connectivity": 28, "Performance": 42,
		"Trust": 48, "Driver": 52, "Freeze": 58, "Crash": 62,
		"Storage": 66, "Hardware": 70,
	}[event.Category]
	if base == 0 {
		base = 24
	}
	score := base
	if event.Level == 1 {
		score += 20
	} else if event.Level == 2 {
		score += 10
	}
	if event.Occurrences > 1 {
		score += min(15, (event.Occurrences-1)*3)
	}
	if strings.Contains(strings.ToLower(event.Source), "whea") {
		score += 12
	}
	if score > 100 {
		score = 100
	}
	label, explanation := "Routine", "Normal operating noise unless a repeatable symptom points to the same timestamp."
	switch {
	case score >= 85:
		label, explanation = "High priority", "A severe or corroborated signal deserves timely investigation and preserved evidence."
	case score >= 68:
		label, explanation = "Important", "This can affect reliability or data integrity; investigate if the machine was symptomatic."
	case score >= 48:
		label, explanation = "Review", "Worth reviewing, especially if it repeats or aligns with a visible problem."
	case score >= 25:
		label, explanation = "Context", "Useful context, but not proof that anything is broken on its own."
	}
	return EventSignificance{Score: score, Label: label, Explanation: explanation}
}

func eventCauseMap(event SystemEvent) (cause, confirmation string) {
	switch event.Category {
	case "Noise":
		return "Windows component isolation and per-application permissions commonly generate this bookkeeping event.", "Only treat it as causal if the same component, timestamp, and a repeatable application failure line up."
	case "Connectivity":
		return "Adapter-driver recovery, radio conditions, access-point reachability, or DNS upstream response.", "Match this time against packet loss, jitter, link changes, and whether other devices were affected."
	case "Trust":
		return "Firmware trust configuration, Secure Boot certificates, TPM readiness, or an attestation-policy mismatch.", "Compare Hardware Passport boot-trust state and the event's raw ID with the firmware/vendor guidance."
	case "Driver":
		return "A blocked, incompatible, missing, or slow-starting driver/service owned by the named component.", "Identify the exact service binary and vendor; confirmation is the same component failing after a clean restart."
	case "Crash":
		return "Application failure, driver failure, power loss, or system instability—the event type determines which branch is plausible.", "Use the faulting module, WHEA/driver events immediately before it, and a repeatable workload to narrow the branch."
	case "Freeze":
		return "A blocked application thread, storage wait, memory pressure, driver stall, or resource contention.", "Look for matching disk latency, commit pressure, driver recovery, and the same executable freezing again."
	case "Hardware":
		return "CPU, memory, PCIe, device, power, thermal, or firmware stability; this event alone does not identify the physical part.", "Preserve WHEA bank/device details and correlate repeats before stress testing or changing clocks."
	case "Storage":
		return "Drive media, controller/driver, cabling, power, or file-system recovery.", "Confirm with repeated device IDs, latency/queue spikes, and read-only vendor health diagnostics before repair writes."
	case "Performance":
		return "Resource exhaustion, a slow dependency, timeout, or a process that failed to finish promptly.", "Correlate CPU, memory commit, disk latency, frame time, and the named process over the same interval."
	default:
		return "Windows reported a provider-specific condition that needs more context before it can be attributed.", "Look for repetition, a matching user-visible symptom, and a second independent signal at the same time."
	}
}

type StutterIncident struct {
	At        time.Time
	Frames    FrameStats
	Latency   LatencyData
	Disk      DiskData
	Memory    MemoryData
	History   []HistorySample
	Processes []ProcessMetric
}

type EventLensState struct {
	mu         sync.RWMutex
	Events     []SystemEvent
	Incidents  []StutterIncident
	Loading    bool
	Error      string
	Filter     int
	LastLoaded time.Time
}

type eventLensView struct {
	Events     []SystemEvent
	Incidents  []StutterIncident
	Loading    bool
	Error      string
	Filter     int
	LastLoaded time.Time
}

func (a *App) eventLensSnapshot() eventLensView {
	a.eventLens.mu.RLock()
	defer a.eventLens.mu.RUnlock()
	return eventLensView{append([]SystemEvent(nil), a.eventLens.Events...), append([]StutterIncident(nil), a.eventLens.Incidents...), a.eventLens.Loading, a.eventLens.Error, a.eventLens.Filter, a.eventLens.LastLoaded}
}

func (a *App) refreshEventLens() {
	a.eventLens.mu.Lock()
	if a.eventLens.Loading {
		a.eventLens.mu.Unlock()
		return
	}
	a.eventLens.Loading = true
	a.eventLens.mu.Unlock()
	go func() {
		events, err := queryWindowsEvents()
		a.eventLens.mu.Lock()
		if err != nil {
			a.eventLens.Error = err.Error()
			a.logger.Error("event-lens", "Windows event query failed", err)
		} else {
			for _, incident := range a.eventLens.Incidents {
				events = append(events, stutterSystemEvent(incident))
			}
			sort.SliceStable(events, func(i, j int) bool { return events[i].At.After(events[j].At) })
			a.eventLens.Events, a.eventLens.Error, a.eventLens.LastLoaded = events, "", time.Now()
			a.logger.Info("event-lens", fmt.Sprintf("loaded %d correlated event groups", len(events)))
		}
		a.eventLens.Loading = false
		a.eventLens.mu.Unlock()
		if a.hwnd != 0 {
			procPostMessageW.Call(a.hwnd, WM_APP_RENDER, 0, 0)
		}
	}()
}

func queryWindowsEvents() ([]SystemEvent, error) {
	rows := make([]windowsEventRow, 0, 240)
	for _, logName := range []string{"System", "Application"} {
		logRows, err := queryWindowsEventLog(logName)
		if err != nil {
			return nil, fmt.Errorf("read %s event log: %w", strings.ToLower(logName), err)
		}
		rows = append(rows, logRows...)
	}
	grouped := make(map[string]*SystemEvent)
	for _, row := range rows {
		at, _ := time.Parse(time.RFC3339Nano, row.Time)
		event := classifyWindowsEvent(at.Local(), row.Provider, row.Log, row.Message, row.ID, row.Level, row.RecordID)
		key := fmt.Sprintf("%s|%d|%s", row.Provider, row.ID, event.Category)
		if existing := grouped[key]; existing != nil {
			existing.Occurrences++
			if event.At.After(existing.At) {
				existing.At, existing.Message, existing.RecordID = event.At, event.Message, event.RecordID
			}
		} else {
			event.Occurrences = 1
			copy := event
			grouped[key] = &copy
		}
	}
	result := make([]SystemEvent, 0, len(grouped))
	for _, event := range grouped {
		result = append(result, *event)
	}
	sort.SliceStable(result, func(i, j int) bool { return result[i].At.After(result[j].At) })
	return result, nil
}

type windowsEventRow struct {
	Time, Provider, Log, Message string
	ID, Level                    int
	RecordID                     int64
}

type eventXMLDocument struct {
	Events []struct {
		System struct {
			Provider struct {
				Name string `xml:"Name,attr"`
			} `xml:"Provider"`
			EventID int `xml:"EventID"`
			Level   int `xml:"Level"`
			Time    struct {
				SystemTime string `xml:"SystemTime,attr"`
			} `xml:"TimeCreated"`
			RecordID int64  `xml:"EventRecordID"`
			Channel  string `xml:"Channel"`
		} `xml:"System"`
		EventData struct {
			Data []struct {
				Name  string `xml:"Name,attr"`
				Value string `xml:",chardata"`
			} `xml:"Data"`
		} `xml:"EventData"`
	} `xml:"Event"`
}

func queryWindowsEventLog(logName string) ([]windowsEventRow, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	query := `*[System[(Level=1 or Level=2 or Level=3) and TimeCreated[timediff(@SystemTime) <= 86400000]]]`
	command := exec.CommandContext(ctx, "wevtutil.exe", "qe", logName, "/q:"+query, "/f:xml", "/c:120", "/rd:true")
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	output, err := command.Output()
	if err != nil {
		return nil, err
	}
	if len(output) == 0 {
		return nil, nil
	}
	wrapped := append([]byte("<Events>"), output...)
	wrapped = append(wrapped, []byte("</Events>")...)
	var document eventXMLDocument
	if err := xml.Unmarshal(wrapped, &document); err != nil {
		return nil, err
	}
	rows := make([]windowsEventRow, 0, len(document.Events))
	for _, event := range document.Events {
		parts := make([]string, 0, len(event.EventData.Data))
		for _, data := range event.EventData.Data {
			value := strings.TrimSpace(data.Value)
			if value == "" {
				continue
			}
			if data.Name != "" {
				value = data.Name + ": " + value
			}
			parts = append(parts, value)
		}
		message := strings.Join(parts, " · ")
		if message == "" {
			message = fmt.Sprintf("%s recorded Event %d", event.System.Provider.Name, event.System.EventID)
		}
		rows = append(rows, windowsEventRow{Time: event.System.Time.SystemTime, Provider: event.System.Provider.Name, Log: event.System.Channel, Message: message, ID: event.System.EventID, Level: event.System.Level, RecordID: event.System.RecordID})
	}
	return rows, nil
}

func classifyWindowsEvent(at time.Time, source, log, message string, id, level int, recordID int64) SystemEvent {
	category, title := "System", source+" reported a problem"
	impact := "Windows recorded a warning or error. Its effect depends on what was happening at the same time."
	next := "Compare its timestamp with performance and frame history."
	sourceLower, messageLower := strings.ToLower(source), strings.ToLower(message)
	switch {
	case strings.Contains(sourceLower, "distributedcom") && id == 10016:
		category, title, impact, next = "Noise", "Windows recorded routine DCOM permission bookkeeping", "This event is common on healthy Windows installations and is not evidence of a performance problem by itself.", "No action is recommended unless a repeatable application failure points to the same component and timestamp."
	case strings.Contains(sourceLower, "netwtw") || strings.Contains(sourceLower, "dns-client") || strings.Contains(sourceLower, "wlan-autoconfig") || strings.Contains(sourceLower, "e2fexpress"):
		category, title, impact, next = "Connectivity", "The network stack reported an interruption", "A driver recovery or lookup timeout can create a short disconnect, latency spike, or failed request.", "Compare this timestamp with Kerneon's latency and packet-loss history before changing a driver or adapter setting."
	case strings.Contains(sourceLower, "tpm-wmi"):
		category, title, impact, next = "Trust", "Windows platform trust needs attention", "TPM, Secure Boot, or attestation state did not meet the check Windows was performing.", "Review Hardware Passport boot trust and the raw event before changing firmware settings."
	case strings.Contains(sourceLower, "service control manager") && (id == 7000 || id == 7001 || id == 7009 || id == 7011):
		category, title, impact, next = "Driver", "A Windows service or driver did not start correctly", "A blocked, missing, or timed-out service can remove hardware functionality or delay startup.", "Use the raw service name to identify its owner; update or remove only that component rather than applying a generic registry fix."
	case strings.Contains(sourceLower, "winsrvext") && id == 100:
		category, title, impact, next = "Performance", "An application delayed shutdown", "Windows waited for a process that did not finish promptly.", "Check whether the same executable repeats and update or close it before shutdown."
	case (sourceLower == "application error" && id == 1000) || strings.Contains(sourceLower, "windows error reporting"):
		category, title, impact, next = "Crash", "An application crashed", "The affected application stopped unexpectedly and may have lost unsaved work.", "Check the faulting application/module and look for a repeated version pattern."
	case (sourceLower == "application hang" && id == 1002) || strings.Contains(messageLower, "stopped interacting"):
		category, title, impact, next = "Freeze", "An application stopped responding", "Windows detected a hang rather than a clean exit.", "Compare memory, disk, CPU and frame pressure before blaming the application."
	case strings.Contains(sourceLower, "whea"):
		category, title, impact, next = "Hardware", "Hardware error telemetry was reported", "WHEA signals can indicate CPU, memory, PCIe, power or stability faults.", "Treat repeated WHEA events as significant; preserve component details before stress testing."
	case sourceLower == "kernel-power" && id == 41:
		category, title, impact, next = "Crash", "Windows did not shut down cleanly", "The PC restarted or lost power before Windows could record a normal shutdown.", "Check power, thermals, WHEA and driver events immediately before this timestamp."
	case strings.Contains(sourceLower, "display") && id == 4101:
		category, title, impact, next = "Hardware", "The graphics driver recovered", "A display-driver timeout can cause a freeze, black screen, or major frame-time spike.", "Correlate with GPU load, driver version, temperature tooling and any game crash."
	case strings.Contains(sourceLower, "disk") || strings.Contains(sourceLower, "storahci") || strings.Contains(sourceLower, "stornvme") || strings.Contains(sourceLower, "ntfs"):
		category, title, impact, next = "Storage", "Storage or file-system trouble was reported", "I/O retries or timeouts can make the whole PC appear frozen.", "Check SMART/vendor diagnostics and correlate with disk queue and latency; make a backup before repair writes."
	case strings.Contains(sourceLower, "resource-exhaustion") || id == 2004:
		category, title, impact, next = "Performance", "Windows ran short of committed memory", "Applications may stall or close when commit capacity is exhausted.", "Inspect the named consumers, commit limit, and page-file configuration."
	case strings.Contains(messageLower, "performance") || strings.Contains(messageLower, "degrad") || strings.Contains(messageLower, "timeout"):
		category, title = "Performance", "Windows recorded a performance-impacting condition"
	}
	return SystemEvent{At: at, RecordID: recordID, ID: id, Level: level, Source: source, Log: log, Category: category, Title: title, Message: strings.TrimSpace(message), Impact: impact, Next: next, RawID: fmt.Sprintf("%s/%s · Event %d · Record %d", log, source, id, recordID)}
}

func (a *App) recordStutterIfNeeded(stats FrameStats) {
	if stats.Samples < 180 || stats.FPS <= 0 || time.Since(a.lastStutterCapture) < 90*time.Second {
		return
	}
	averageFrameMs := 1000 / stats.FPS
	if stats.OnePercentLow >= stats.FPS*0.58 && stats.DroppedPercent < 2 && stats.P95DisplayMs < 65 && stats.HitchesPerMinute < 4 && stats.P99FrameMs < averageFrameMs*2 {
		return
	}
	snapshot := a.engine.Snapshot()
	processes := append([]ProcessMetric(nil), snapshot.Processes...)
	if len(processes) > 8 {
		processes = processes[:8]
	}
	incident := StutterIncident{At: time.Now(), Frames: stats, Latency: snapshot.Latency, Disk: snapshot.Disk, Memory: snapshot.Memory, History: recentHistory(snapshot.History, 45*time.Second), Processes: processes}
	a.eventLens.mu.Lock()
	a.eventLens.Incidents = append([]StutterIncident{incident}, a.eventLens.Incidents...)
	if len(a.eventLens.Incidents) > 12 {
		a.eventLens.Incidents = a.eventLens.Incidents[:12]
	}
	a.eventLens.Events = append([]SystemEvent{stutterSystemEvent(incident)}, a.eventLens.Events...)
	a.eventLens.mu.Unlock()
	a.lastStutterCapture = incident.At
	a.engine.AddUserEvent("stutter", "Stutter flight recorder captured", fmt.Sprintf("FPS %.1f · 1%% low %.1f · p99 frame %.1f ms · %.1f hitches/min · dropped %.1f%%", stats.FPS, stats.OnePercentLow, stats.P99FrameMs, stats.HitchesPerMinute, stats.DroppedPercent), 2)
}

func recentHistory(history []HistorySample, duration time.Duration) []HistorySample {
	cutoff := time.Now().Add(-duration)
	start := 0
	for start < len(history) && history[start].At.Before(cutoff) {
		start++
	}
	return append([]HistorySample(nil), history[start:]...)
}

func stutterSystemEvent(incident StutterIncident) SystemEvent {
	top := "No dominant process was recorded"
	if len(incident.Processes) > 0 {
		process := incident.Processes[0]
		top = fmt.Sprintf("Top measured process: %s at %.1f%% CPU and %s working memory", process.Name, process.CPU, core.FormatBytes(process.WorkingSet))
	}
	contention := analyzeSurgeContention(incident.Processes, incident.Frames.PID)
	diagnosis := diagnoseSurgeStutter(incident.Frames, incident.Latency, incident.Disk, incident.Memory, contention)
	return SystemEvent{At: incident.At, ID: 1, Level: 2, Source: "Kerneon Flight Recorder", Log: "Kerneon", Category: "Performance", Title: "A meaningful stutter pattern was captured", Message: fmt.Sprintf("Mean %.1f FPS, 1%% low %.1f FPS, p99 frame %.1f ms, worst %.1f ms, %.1f hitches/min, dropped %.1f%%. %s. Route: %s (%s confidence).", incident.Frames.FPS, incident.Frames.OnePercentLow, incident.Frames.P99FrameMs, incident.Frames.WorstFrameMs, incident.Frames.HitchesPerMinute, incident.Frames.DroppedPercent, top, diagnosis.Title, diagnosis.Confidence), Impact: diagnosis.Explanation, Next: "Use the preserved DPC/interrupt, scheduler, storage, paging and process evidence to confirm this route before remediation.", Occurrences: 1, RawID: "Kerneon/stutter-flight-recorder"}
}

func (a *App) eventCounts() (warnings, errors int) {
	view := a.eventLensSnapshot()
	cutoff := time.Now().Add(-24 * time.Hour)
	for _, event := range view.Events {
		if event.At.Before(cutoff) {
			continue
		}
		if event.Category == "System" {
			continue
		}
		occurrences := event.Occurrences
		if occurrences > 3 {
			occurrences = 3
		}
		if event.Level <= 2 {
			errors += occurrences
		} else {
			warnings += occurrences
		}
	}
	return
}

func eventMatchesFilter(event SystemEvent, filter int) bool {
	switch filter {
	case eventCrashes:
		return event.Category == "Crash" || event.Category == "Freeze"
	case eventHardware:
		return event.Category == "Hardware" || event.Category == "Storage" || event.Category == "Driver" || event.Category == "Trust"
	case eventPerformance:
		return event.Category == "Performance" || event.Category == "Connectivity"
	default:
		return true
	}
}
