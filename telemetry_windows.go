//go:build windows

package main

import (
	"fmt"
	"math"
	"net"
	"os"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unicode/utf16"
	"unsafe"

	"kerneon/core"
)

const (
	IF_OPER_STATUS_UP                 = 1
	IF_TYPE_SOFTWARE_LOOPBACK         = 24
	TH32CS_SNAPPROCESS                = 0x00000002
	PROCESS_QUERY_INFORMATION         = 0x0400
	PROCESS_QUERY_LIMITED_INFORMATION = 0x1000
	PROCESS_VM_READ                   = 0x0010
	PDH_FMT_DOUBLE                    = 0x00000200
	PDH_MORE_DATA                     = 0x800007D2
	KEY_READ                          = 0x20019
	HKEY_LOCAL_MACHINE                = 0x80000002
	REG_SZ                            = 1
	REG_EXPAND_SZ                     = 2
)

var (
	iphlpapi                             = syscall.NewLazyDLL("iphlpapi.dll")
	psapi                                = syscall.NewLazyDLL("psapi.dll")
	pdh                                  = syscall.NewLazyDLL("pdh.dll")
	powrprof                             = syscall.NewLazyDLL("powrprof.dll")
	ntdll                                = syscall.NewLazyDLL("ntdll.dll")
	advapi32                             = syscall.NewLazyDLL("advapi32.dll")
	procGetIfTable2                      = iphlpapi.NewProc("GetIfTable2")
	procGetIfEntry2                      = iphlpapi.NewProc("GetIfEntry2")
	procFreeMibTable                     = iphlpapi.NewProc("FreeMibTable")
	procIcmpCreateFile                   = iphlpapi.NewProc("IcmpCreateFile")
	procIcmpSendEcho                     = iphlpapi.NewProc("IcmpSendEcho")
	procIcmpCloseHandle                  = iphlpapi.NewProc("IcmpCloseHandle")
	procGetSystemTimes                   = kernel32.NewProc("GetSystemTimes")
	procGetLogicalProcessorInformationEx = kernel32.NewProc("GetLogicalProcessorInformationEx")
	procGlobalMemoryStatusEx             = kernel32.NewProc("GlobalMemoryStatusEx")
	procGetTickCount64                   = kernel32.NewProc("GetTickCount64")
	procGetLogicalDrives                 = kernel32.NewProc("GetLogicalDrives")
	procGetDriveTypeW                    = kernel32.NewProc("GetDriveTypeW")
	procGetDiskFreeSpaceExW              = kernel32.NewProc("GetDiskFreeSpaceExW")
	procGetVolumeInformationW            = kernel32.NewProc("GetVolumeInformationW")
	procCreateToolhelp32Snapshot         = kernel32.NewProc("CreateToolhelp32Snapshot")
	procProcess32FirstW                  = kernel32.NewProc("Process32FirstW")
	procProcess32NextW                   = kernel32.NewProc("Process32NextW")
	procOpenProcess                      = kernel32.NewProc("OpenProcess")
	procCloseHandle                      = kernel32.NewProc("CloseHandle")
	procGetProcessTimes                  = kernel32.NewProc("GetProcessTimes")
	procGetProcessIoCounters             = kernel32.NewProc("GetProcessIoCounters")
	procGetProcessHandleCount            = kernel32.NewProc("GetProcessHandleCount")
	procQueryFullProcessImageNameW       = kernel32.NewProc("QueryFullProcessImageNameW")
	procGetPerformanceInfo               = psapi.NewProc("GetPerformanceInfo")
	procK32GetProcessMemoryInfo          = kernel32.NewProc("K32GetProcessMemoryInfo")
	procCallNtPowerInformation           = powrprof.NewProc("CallNtPowerInformation")
	procRtlGetVersion                    = ntdll.NewProc("RtlGetVersion")
	procPdhOpenQueryW                    = pdh.NewProc("PdhOpenQueryW")
	procPdhAddEnglishCounterW            = pdh.NewProc("PdhAddEnglishCounterW")
	procPdhCollectQueryData              = pdh.NewProc("PdhCollectQueryData")
	procPdhGetFormattedCounterValue      = pdh.NewProc("PdhGetFormattedCounterValue")
	procPdhGetFormattedCounterArrayW     = pdh.NewProc("PdhGetFormattedCounterArrayW")
	procPdhCloseQuery                    = pdh.NewProc("PdhCloseQuery")
	procRegOpenKeyExW                    = advapi32.NewProc("RegOpenKeyExW")
	procRegQueryValueExW                 = advapi32.NewProc("RegQueryValueExW")
	procRegEnumKeyExW                    = advapi32.NewProc("RegEnumKeyExW")
	procRegCloseKey                      = advapi32.NewProc("RegCloseKey")
)

type filetime struct{ Low, High uint32 }

func (f filetime) Uint64() uint64 { return uint64(f.High)<<32 | uint64(f.Low) }

type memoryStatusEx struct {
	Length, MemoryLoad                                 uint32
	TotalPhys, AvailPhys, TotalPageFile, AvailPageFile uint64
	TotalVirtual, AvailVirtual, AvailExtendedVirtual   uint64
}

type performanceInformation struct {
	Size                                          uint32
	_                                             uint32
	CommitTotal, CommitLimit, CommitPeak          uintptr
	PhysicalTotal, PhysicalAvailable, SystemCache uintptr
	KernelTotal, KernelPaged, KernelNonpaged      uintptr
	PageSize                                      uintptr
	HandleCount, ProcessCount, ThreadCount        uint32
}

type processorPowerInformation struct{ Number, MaxMhz, CurrentMhz, MhzLimit, MaxIdleState, CurrentIdleState uint32 }

type MIBIfRow2 struct {
	InterfaceLuid                                                                   uint64
	InterfaceIndex                                                                  uint32
	InterfaceGuid                                                                   GUID
	Alias                                                                           [257]uint16
	Description                                                                     [257]uint16
	PhysicalAddressLength                                                           uint32
	PhysicalAddress                                                                 [32]byte
	PermanentPhysicalAddress                                                        [32]byte
	Mtu, Type, TunnelType, MediaType, PhysicalMediumType, AccessType, DirectionType uint32
	InterfaceAndOperStatusFlags                                                     uint8
	_padFlags                                                                       [3]byte
	OperStatus, AdminStatus, MediaConnectState                                      uint32
	NetworkGuid                                                                     GUID
	ConnectionType                                                                  uint32
	TransmitLinkSpeed, ReceiveLinkSpeed                                             uint64
	InOctets, InUcastPkts, InNUcastPkts, InDiscards, InErrors, InUnknownProtos      uint64
	InUcastOctets, InMulticastOctets, InBroadcastOctets                             uint64
	OutOctets, OutUcastPkts, OutNUcastPkts, OutDiscards, OutErrors                  uint64
	OutUcastOctets, OutMulticastOctets, OutBroadcastOctets, OutQLen                 uint64
}

type processEntry32 struct {
	Size, Usage, ProcessID             uint32
	DefaultHeapID                      uintptr
	ModuleID, Threads, ParentProcessID uint32
	PriClassBase                       int32
	Flags                              uint32
	ExeFile                            [260]uint16
}

type processMemoryCountersEx struct {
	CB, PageFaultCount                                 uint32
	PeakWorkingSetSize, WorkingSetSize                 uintptr
	QuotaPeakPagedPoolUsage, QuotaPagedPoolUsage       uintptr
	QuotaPeakNonPagedPoolUsage, QuotaNonPagedPoolUsage uintptr
	PagefileUsage, PeakPagefileUsage, PrivateUsage     uintptr
}

type ioCounters struct{ ReadOps, WriteOps, OtherOps, ReadBytes, WriteBytes, OtherBytes uint64 }

type osVersionInfo struct {
	Size, Major, Minor, Build, PlatformID uint32
	CSDVersion                            [128]uint16
}

type pdhFormattedValue struct {
	Status uint32
	_      uint32
	Value  float64
}
type pdhValueItem struct {
	Name  *uint16
	Value pdhFormattedValue
}

type pdhQuery struct {
	handle                                                 uintptr
	diskUsage, diskRead, diskWrite, diskQueue, diskLatency uintptr
	gpuUsage, gpuDedicated, gpuShared                      uintptr
	ready                                                  bool
	error                                                  string
}

type processPrevious struct {
	CPU, Read, Write uint64
	At               time.Time
}

type TelemetryEngine struct {
	mu       sync.RWMutex
	cfg      core.Config
	logger   *Logger
	snapshot Snapshot
	stop     chan struct{}
	wg       sync.WaitGroup
	running  bool
	lowPower atomic.Bool
	gameMode atomic.Bool
	history  *core.Ring[HistorySample]
	events   *core.Ring[Event]

	cpuIdle, cpuKernel, cpuUser uint64
	cpuReady                    bool
	networkIndex                uint32
	networkPrev                 MIBIfRow2
	networkAt                   time.Time
	networkReady                bool
	autoRows                    map[uint32]MIBIfRow2
	displayDown, displayUp      core.EMA
	processPrev                 map[uint32]processPrevious
	pingResults                 []bool
	pingLatencies               []float64
	pdh                         *pdhQuery
}

func NewTelemetryEngine(cfg core.Config, logger *Logger) *TelemetryEngine {
	return &TelemetryEngine{cfg: cfg, logger: logger, history: core.NewRing[HistorySample](36000), events: core.NewRing[Event](200), autoRows: make(map[uint32]MIBIfRow2), processPrev: make(map[uint32]processPrevious), displayDown: core.NewEMA(180 * time.Millisecond), displayUp: core.NewEMA(180 * time.Millisecond)}
}

func (e *TelemetryEngine) Start() {
	e.mu.Lock()
	if e.running {
		e.mu.Unlock()
		return
	}
	e.stop = make(chan struct{})
	e.running = true
	e.snapshot.System = querySystemData()
	e.snapshot.At = time.Now()
	e.pdh = newPDHQuery()
	stop := e.stop
	e.mu.Unlock()
	e.addEvent("system", "Kerneon monitoring started", "Collectors are running at adaptive sampling rates.", 0)
	e.wg.Add(5)
	go e.cpuLoop(stop)
	go e.networkLoop(stop)
	go e.mediumLoop(stop)
	go e.pingLoop(stop)
	go e.slowLoop(stop)
}

func (e *TelemetryEngine) Stop() {
	e.mu.Lock()
	if !e.running {
		e.mu.Unlock()
		return
	}
	close(e.stop)
	e.running = false
	p := e.pdh
	e.pdh = nil
	e.mu.Unlock()
	e.wg.Wait()
	if p != nil {
		p.Close()
	}
}

func (e *TelemetryEngine) Restart(cfg core.Config) {
	e.Stop()
	e.mu.Lock()
	e.cfg = cfg
	e.networkReady = false
	e.autoRows = make(map[uint32]MIBIfRow2)
	e.processPrev = make(map[uint32]processPrevious)
	e.mu.Unlock()
	e.Start()
}

func (e *TelemetryEngine) Snapshot() Snapshot {
	e.mu.RLock()
	defer e.mu.RUnlock()
	s := e.snapshot
	s.Processes = append([]ProcessMetric(nil), e.snapshot.Processes...)
	s.Disk.Volumes = append([]VolumeData(nil), e.snapshot.Disk.Volumes...)
	s.History = e.history.Values(nil)
	s.Events = e.events.Values(nil)
	return s
}

func (e *TelemetryEngine) ResetSession() {
	e.mu.Lock()
	e.snapshot.Network.SessionDown, e.snapshot.Network.SessionUp = 0, 0
	e.snapshot.Network.PeakDown, e.snapshot.Network.PeakUp = 0, 0
	e.snapshot.Network.Errors, e.snapshot.Network.Discards = 0, 0
	e.networkReady = false
	e.events.Add(Event{At: time.Now(), Kind: "network", Title: "Network session reset", Detail: "Peaks, transfers and error deltas were cleared.", Severity: 0})
	e.mu.Unlock()
}

func (e *TelemetryEngine) Resume() {
	e.mu.Lock()
	e.cpuReady, e.networkReady = false, false
	e.processPrev = make(map[uint32]processPrevious)
	e.events.Add(Event{At: time.Now(), Kind: "power", Title: "System resumed", Detail: "Rate baselines were reset to prevent false spikes.", Severity: 0})
	e.mu.Unlock()
}

func (e *TelemetryEngine) AddUserEvent(kind, title, detail string, severity int) {
	e.addEvent(kind, title, detail, severity)
}

func (e *TelemetryEngine) SetLowPower(enabled bool) { e.lowPower.Store(enabled) }
func (e *TelemetryEngine) SetGameMode(enabled bool) { e.gameMode.Store(enabled) }

func (e *TelemetryEngine) addEvent(kind, title, detail string, severity int) {
	e.mu.Lock()
	e.events.Add(Event{At: time.Now(), Kind: kind, Title: title, Detail: detail, Severity: severity})
	e.mu.Unlock()
}

func (e *TelemetryEngine) recoverCollector(subsystem string) {
	if recovered := recover(); recovered != nil {
		e.logger.Panic(subsystem, recovered)
		e.mu.Lock()
		e.events.Add(Event{At: time.Now(), Kind: "provider", Title: subsystem + " monitoring unavailable", Detail: "The provider failed safely; see local diagnostics for details.", Severity: 2})
		e.mu.Unlock()
	}
}

func (e *TelemetryEngine) cpuLoop(stop <-chan struct{}) {
	defer e.wg.Done()
	defer e.recoverCollector("cpu-memory")
	e.sampleCPUAndMemory()
	for {
		e.mu.RLock()
		interval := time.Duration(e.cfg.Sampling.CPUms) * time.Millisecond
		adaptive := e.cfg.Sampling.Adaptive
		e.mu.RUnlock()
		if adaptive && e.lowPower.Load() && interval < time.Second {
			interval = time.Second
		}
		timer := time.NewTimer(interval)
		select {
		case <-timer.C:
			e.sampleCPUAndMemory()
		case <-stop:
			timer.Stop()
			return
		}
	}
}

func (e *TelemetryEngine) networkLoop(stop <-chan struct{}) {
	defer e.wg.Done()
	defer e.recoverCollector("network")
	refresh := time.Time{}
	for {
		e.mu.RLock()
		hz := e.cfg.Sampling.NetworkHz
		adaptive := e.cfg.Sampling.Adaptive
		e.mu.RUnlock()
		if adaptive && e.lowPower.Load() && hz > 10 {
			hz = 10
		}
		if hz < 1 {
			hz = 60
		}
		now := time.Now()
		if refresh.IsZero() || now.Sub(refresh) >= time.Second {
			e.refreshNetworkAdapter(now)
			refresh = now
		}
		e.sampleNetwork(now)
		timer := time.NewTimer(time.Second / time.Duration(hz))
		select {
		case <-timer.C:
		case <-stop:
			timer.Stop()
			return
		}
	}
}

func (e *TelemetryEngine) mediumLoop(stop <-chan struct{}) {
	defer e.wg.Done()
	defer e.recoverCollector("process-disk-gpu")
	e.sampleMedium()
	for {
		e.mu.RLock()
		interval := time.Duration(e.cfg.Sampling.Processms) * time.Millisecond
		adaptive := e.cfg.Sampling.Adaptive
		e.mu.RUnlock()
		if adaptive && e.lowPower.Load() && interval < 5*time.Second {
			interval = 5 * time.Second
		}
		timer := time.NewTimer(interval)
		select {
		case <-timer.C:
			e.sampleMedium()
		case <-stop:
			timer.Stop()
			return
		}
	}
}

func (e *TelemetryEngine) pingLoop(stop <-chan struct{}) {
	defer e.wg.Done()
	defer e.recoverCollector("latency")
	for {
		e.mu.RLock()
		sec, target := e.cfg.Network.PingSeconds, e.cfg.Network.PingTarget
		e.mu.RUnlock()
		if sec > 0 {
			ms, ok := pingTarget(target, 900*time.Millisecond)
			e.recordPing(ms, ok)
		}
		wait := time.Second
		if sec > 0 {
			wait = time.Duration(sec) * time.Second
		}
		timer := time.NewTimer(wait)
		select {
		case <-timer.C:
		case <-stop:
			timer.Stop()
			return
		}
	}
}

func (e *TelemetryEngine) slowLoop(stop <-chan struct{}) {
	defer e.wg.Done()
	defer e.recoverCollector("system-metadata")
	for {
		interval := 30 * time.Second
		if e.lowPower.Load() || e.gameMode.Load() {
			interval = 2 * time.Minute
		}
		timer := time.NewTimer(interval)
		select {
		case <-timer.C:
			s := querySystemData()
			e.mu.Lock()
			e.snapshot.System = s
			e.mu.Unlock()
		case <-stop:
			timer.Stop()
			return
		}
	}
}

func (e *TelemetryEngine) sampleCPUAndMemory() {
	now := time.Now()
	var idle, kernel, user filetime
	r, _, err := procGetSystemTimes.Call(uintptr(unsafe.Pointer(&idle)), uintptr(unsafe.Pointer(&kernel)), uintptr(unsafe.Pointer(&user)))
	if r == 0 {
		e.logger.Error("cpu", "GetSystemTimes", err)
		return
	}
	idleV, kernelV, userV := idle.Uint64(), kernel.Uint64(), user.Uint64()
	e.mu.Lock()
	if e.cpuReady {
		dIdle := idleV - e.cpuIdle
		dKernel := kernelV - e.cpuKernel
		dUser := userV - e.cpuUser
		total := dKernel + dUser
		if total > 0 && dKernel >= dIdle {
			e.snapshot.CPU.Usage = clampFloat(float64(total-dIdle)/float64(total)*100, 0, 100)
			e.snapshot.CPU.Kernel = clampFloat(float64(dKernel-dIdle)/float64(total)*100, 0, 100)
		}
	}
	e.cpuIdle, e.cpuKernel, e.cpuUser, e.cpuReady = idleV, kernelV, userV, true
	e.mu.Unlock()

	mem, perf := queryMemory()
	freq, maxFreq := queryCPUFrequency()
	e.mu.Lock()
	e.snapshot.Memory = mem
	e.snapshot.CPU.FrequencyMHz, e.snapshot.CPU.MaxMHz = freq, maxFreq
	e.snapshot.CPU.Cores = e.snapshot.System.Cores
	e.snapshot.CPU.Logical = e.snapshot.System.Logical
	e.snapshot.CPU.Model = e.snapshot.System.CPU
	e.snapshot.ProcessCount, e.snapshot.ThreadCount, e.snapshot.HandleCount = perf.ProcessCount, perf.ThreadCount, perf.HandleCount
	e.snapshot.At = now
	h := HistorySample{At: now, CPU: e.snapshot.CPU.Usage, GPU: e.snapshot.GPU.Usage, Memory: mem.UsagePercent, Disk: e.snapshot.Disk.Usage, Down: e.snapshot.Network.DownBps, Up: e.snapshot.Network.UpBps, Latency: e.snapshot.Network.LatencyMs}
	e.history.Add(h)
	e.mu.Unlock()
}

func queryMemory() (MemoryData, performanceInformation) {
	var m memoryStatusEx
	m.Length = uint32(unsafe.Sizeof(m))
	procGlobalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&m)))
	var p performanceInformation
	p.Size = uint32(unsafe.Sizeof(p))
	procGetPerformanceInfo.Call(uintptr(unsafe.Pointer(&p)), uintptr(p.Size))
	page := uint64(p.PageSize)
	result := MemoryData{Total: m.TotalPhys, Available: m.AvailPhys, Used: m.TotalPhys - m.AvailPhys, Cached: uint64(p.SystemCache) * page, Commit: uint64(p.CommitTotal) * page, CommitLimit: uint64(p.CommitLimit) * page, PagedPool: uint64(p.KernelPaged) * page, NonPagedPool: uint64(p.KernelNonpaged) * page, UsagePercent: float64(m.MemoryLoad)}
	if result.CommitLimit > 0 {
		result.CommitPercent = float64(result.Commit) / float64(result.CommitLimit) * 100
	}
	return result, p
}

func queryCPUFrequency() (float64, float64) {
	count := runtime.NumCPU()
	if count < 1 {
		return 0, 0
	}
	items := make([]processorPowerInformation, count)
	r, _, _ := procCallNtPowerInformation.Call(11, 0, 0, uintptr(unsafe.Pointer(&items[0])), uintptr(len(items))*unsafe.Sizeof(items[0]))
	if r != 0 {
		return 0, 0
	}
	var cur, max float64
	for _, it := range items {
		cur += float64(it.CurrentMhz)
		if float64(it.MaxMhz) > max {
			max = float64(it.MaxMhz)
		}
	}
	return cur / float64(len(items)), max
}

func (e *TelemetryEngine) refreshNetworkAdapter(now time.Time) {
	rows, err := getIfRows()
	if err != nil {
		e.mu.Lock()
		e.snapshot.Network.LastError = err.Error()
		e.snapshot.Network.Connected = false
		e.mu.Unlock()
		return
	}
	e.mu.Lock()
	wanted := e.cfg.Network.AdapterIndex
	current := e.networkIndex
	bestScore := uint64(0)
	var chosen MIBIfRow2
	found := false
	for _, row := range rows {
		if row.Type == IF_TYPE_SOFTWARE_LOOPBACK || row.OperStatus != IF_OPER_STATUS_UP {
			continue
		}
		alias := strings.ToLower(utf16String(row.Alias[:]))
		description := strings.ToLower(utf16String(row.Description[:]))
		if row.InterfaceAndOperStatusFlags&0x02 != 0 || strings.Contains(alias, "filter") || strings.Contains(description, "lightweight filter") || strings.Contains(description, "wfp") {
			continue
		}
		if wanted != 0 && row.InterfaceIndex != wanted {
			continue
		}
		score := uint64(0)
		if prev, ok := e.autoRows[row.InterfaceIndex]; ok {
			score = counterDelta(row.InOctets, prev.InOctets) + counterDelta(row.OutOctets, prev.OutOctets)
		} else {
			score = maxU64(row.ReceiveLinkSpeed, row.TransmitLinkSpeed) / 1_000_000
		}
		e.autoRows[row.InterfaceIndex] = row
		if !found || score > bestScore || (score == bestScore && row.InterfaceIndex == current) {
			chosen, bestScore, found = row, score, true
		}
	}
	if found {
		changed := e.networkIndex != 0 && e.networkIndex != chosen.InterfaceIndex
		e.networkIndex = chosen.InterfaceIndex
		if changed {
			e.networkReady = false
			e.events.Add(Event{At: now, Kind: "network", Title: "Network adapter changed", Detail: "Now monitoring " + utf16String(chosen.Alias[:]), Severity: 0})
		}
		e.updateNetworkMetaLocked(chosen)
	} else {
		e.snapshot.Network.Connected = false
		e.snapshot.Network.LastError = "No connected adapter"
	}
	e.mu.Unlock()
}

func (e *TelemetryEngine) sampleNetwork(now time.Time) {
	e.mu.RLock()
	idx := e.networkIndex
	e.mu.RUnlock()
	if idx == 0 {
		return
	}
	row := MIBIfRow2{InterfaceIndex: idx}
	r, _, err := procGetIfEntry2.Call(uintptr(unsafe.Pointer(&row)))
	if r != 0 {
		e.mu.Lock()
		e.snapshot.Network.Connected = false
		e.snapshot.Network.LastError = fmt.Sprintf("GetIfEntry2: %v", err)
		e.networkReady = false
		e.mu.Unlock()
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.snapshot.Network.SampleCount++
	if !e.networkReady || e.networkPrev.InterfaceIndex != idx {
		e.networkPrev = row
		e.networkAt = now
		e.networkReady = true
		e.snapshot.Network.DownBps = 0
		e.snapshot.Network.UpBps = 0
		e.updateNetworkMetaLocked(row)
		return
	}
	dt := now.Sub(e.networkAt)
	if dt <= 0 {
		return
	}
	din := counterDelta(row.InOctets, e.networkPrev.InOctets)
	dout := counterDelta(row.OutOctets, e.networkPrev.OutOctets)
	if row.InOctets < e.networkPrev.InOctets || row.OutOctets < e.networkPrev.OutOctets {
		e.networkPrev = row
		e.networkAt = now
		return
	}
	down := float64(din) / dt.Seconds()
	up := float64(dout) / dt.Seconds()
	e.snapshot.Network.DownBps = e.displayDown.Update(down, dt)
	e.snapshot.Network.UpBps = e.displayUp.Update(up, dt)
	e.snapshot.Network.SessionDown += din
	e.snapshot.Network.SessionUp += dout
	e.snapshot.Network.PeakDown = math.Max(e.snapshot.Network.PeakDown, down)
	e.snapshot.Network.PeakUp = math.Max(e.snapshot.Network.PeakUp, up)
	e.snapshot.Network.RxPPS = float64(counterDelta(row.InUcastPkts+row.InNUcastPkts, e.networkPrev.InUcastPkts+e.networkPrev.InNUcastPkts)) / dt.Seconds()
	e.snapshot.Network.TxPPS = float64(counterDelta(row.OutUcastPkts+row.OutNUcastPkts, e.networkPrev.OutUcastPkts+e.networkPrev.OutNUcastPkts)) / dt.Seconds()
	e.snapshot.Network.Errors += counterDelta(row.InErrors+row.OutErrors, e.networkPrev.InErrors+e.networkPrev.OutErrors)
	e.snapshot.Network.Discards += counterDelta(row.InDiscards+row.OutDiscards, e.networkPrev.InDiscards+e.networkPrev.OutDiscards)
	link := maxU64(row.ReceiveLinkSpeed, row.TransmitLinkSpeed)
	if link > 0 {
		e.snapshot.Network.Utilization = clampFloat(math.Max(down, up)*8/float64(link)*100, 0, 100)
	}
	e.snapshot.Network.Connected = row.OperStatus == IF_OPER_STATUS_UP
	e.snapshot.Network.LastError = ""
	e.networkPrev = row
	e.networkAt = now
}

func (e *TelemetryEngine) updateNetworkMetaLocked(row MIBIfRow2) {
	n := &e.snapshot.Network
	n.AdapterIndex = row.InterfaceIndex
	n.Name = utf16String(row.Alias[:])
	n.Description = utf16String(row.Description[:])
	n.MTU = row.Mtu
	n.LinkDown = row.ReceiveLinkSpeed
	n.LinkUp = row.TransmitLinkSpeed
	n.Connected = row.OperStatus == IF_OPER_STATUS_UP
	switch row.Type {
	case 6:
		n.Kind = "Ethernet"
	case 71:
		n.Kind = "Wi-Fi"
	case 23, 131:
		n.Kind = "VPN / tunnel"
	case 243, 244:
		n.Kind = "Mobile"
	default:
		n.Kind = "Network"
	}
	n.IPv4, n.IPv6 = interfaceAddresses(int(row.InterfaceIndex))
}

func getIfRows() ([]MIBIfRow2, error) {
	var table unsafe.Pointer
	r, _, _ := procGetIfTable2.Call(uintptr(unsafe.Pointer(&table)))
	if r != 0 || table == nil {
		return nil, fmt.Errorf("GetIfTable2: %d", r)
	}
	defer procFreeMibTable.Call(uintptr(table))
	n := *(*uint32)(table)
	base := unsafe.Add(table, 8)
	size := unsafe.Sizeof(MIBIfRow2{})
	rows := make([]MIBIfRow2, n)
	for i := uint32(0); i < n; i++ {
		rows[i] = *(*MIBIfRow2)(unsafe.Add(base, uintptr(i)*size))
	}
	return rows, nil
}

func interfaceAddresses(index int) (string, string) {
	inf, err := net.InterfaceByIndex(index)
	if err != nil {
		return "—", "—"
	}
	addrs, _ := inf.Addrs()
	v4, v6 := "—", "—"
	for _, a := range addrs {
		ip, _, err := net.ParseCIDR(a.String())
		if err != nil {
			continue
		}
		if ip.To4() != nil && v4 == "—" {
			v4 = ip.String()
		} else if ip.To4() == nil && v6 == "—" {
			v6 = ip.String()
		}
	}
	return v4, v6
}

func (e *TelemetryEngine) recordPing(ms float64, ok bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.pingResults = append(e.pingResults, ok)
	if len(e.pingResults) > 30 {
		e.pingResults = e.pingResults[len(e.pingResults)-30:]
	}
	if ok {
		e.pingLatencies = append(e.pingLatencies, ms)
		if len(e.pingLatencies) > 30 {
			e.pingLatencies = e.pingLatencies[len(e.pingLatencies)-30:]
		}
	}
	bad := 0
	for _, v := range e.pingResults {
		if !v {
			bad++
		}
	}
	e.snapshot.Network.PingSamples = len(e.pingResults)
	if len(e.pingResults) >= 10 {
		e.snapshot.Network.PacketLoss = float64(bad) / float64(len(e.pingResults)) * 100
	} else {
		e.snapshot.Network.PacketLoss = 0
	}
	e.snapshot.Network.PingOK = ok
	if ok {
		e.snapshot.Network.LatencyMs = ms
	}
	if len(e.pingLatencies) > 1 {
		sum := 0.0
		for i := 1; i < len(e.pingLatencies); i++ {
			sum += math.Abs(e.pingLatencies[i] - e.pingLatencies[i-1])
		}
		e.snapshot.Network.JitterMs = sum / float64(len(e.pingLatencies)-1)
	}
}

func pingTarget(target string, timeout time.Duration) (float64, bool) {
	ip := net.ParseIP(target)
	if ip == nil {
		ips, err := net.LookupIP(target)
		if err != nil {
			return 0, false
		}
		for _, candidate := range ips {
			if candidate.To4() != nil {
				ip = candidate
				break
			}
		}
	}
	ip = ip.To4()
	if ip == nil {
		return 0, false
	}
	h, _, _ := procIcmpCreateFile.Call()
	if h == 0 || h == ^uintptr(0) {
		return 0, false
	}
	defer procIcmpCloseHandle.Call(h)
	dest := uintptr(uint32(ip[0]) | uint32(ip[1])<<8 | uint32(ip[2])<<16 | uint32(ip[3])<<24)
	data := []byte("Kerneon")
	reply := make([]byte, 256)
	n, _, _ := procIcmpSendEcho.Call(h, dest, uintptr(unsafe.Pointer(&data[0])), uintptr(len(data)), 0, uintptr(unsafe.Pointer(&reply[0])), uintptr(len(reply)), uintptr(timeout.Milliseconds()))
	if n == 0 {
		return 0, false
	}
	return float64(*(*uint32)(unsafe.Pointer(&reply[8]))), true
}

func (e *TelemetryEngine) sampleMedium() {
	now := time.Now()
	processes, prev := queryProcesses(e.processPrev, now)
	volumes := queryVolumes()
	e.mu.Lock()
	e.processPrev = prev
	e.snapshot.Processes = processes
	e.snapshot.Disk.Volumes = volumes
	if len(volumes) > 0 {
		e.snapshot.Disk.Total = volumes[0].Total
		e.snapshot.Disk.Free = volumes[0].Free
	}
	p := e.pdh
	e.mu.Unlock()
	if p != nil {
		disk, gpu := p.Sample()
		e.mu.Lock()
		disk.Volumes = volumes
		if len(volumes) > 0 {
			disk.Total = volumes[0].Total
			disk.Free = volumes[0].Free
		}
		e.snapshot.Disk = disk
		if gpu.Model == "" {
			gpu.Model = e.snapshot.System.GPU
		}
		e.snapshot.GPU = gpu
		e.mu.Unlock()
	}
}

func queryProcesses(previous map[uint32]processPrevious, now time.Time) ([]ProcessMetric, map[uint32]processPrevious) {
	snap, _, _ := procCreateToolhelp32Snapshot.Call(TH32CS_SNAPPROCESS, 0)
	if snap == 0 || snap == ^uintptr(0) {
		return nil, previous
	}
	defer procCloseHandle.Call(snap)
	next := make(map[uint32]processPrevious)
	result := make([]ProcessMetric, 0, 128)
	var entry processEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	r, _, _ := procProcess32FirstW.Call(snap, uintptr(unsafe.Pointer(&entry)))
	for r != 0 {
		p := ProcessMetric{PID: entry.ProcessID, Parent: entry.ParentProcessID, Threads: entry.Threads, Name: utf16String(entry.ExeFile[:])}
		if p.PID != 0 {
			queryProcessDetail(&p, previous[p.PID], now, next)
		}
		result = append(result, p)
		entry.Size = uint32(unsafe.Sizeof(entry))
		r, _, _ = procProcess32NextW.Call(snap, uintptr(unsafe.Pointer(&entry)))
	}
	sort.Slice(result, func(i, j int) bool {
		si := result[i].CPU + float64(result[i].WorkingSet)/1e9*2 + (result[i].ReadBps+result[i].WriteBps)/50e6
		sj := result[j].CPU + float64(result[j].WorkingSet)/1e9*2 + (result[j].ReadBps+result[j].WriteBps)/50e6
		return si > sj
	})
	if len(result) > 80 {
		result = result[:80]
	}
	return result, next
}

func queryProcessDetail(p *ProcessMetric, prev processPrevious, now time.Time, next map[uint32]processPrevious) {
	h, _, _ := procOpenProcess.Call(PROCESS_QUERY_LIMITED_INFORMATION|PROCESS_QUERY_INFORMATION|PROCESS_VM_READ, 0, uintptr(p.PID))
	if h == 0 {
		return
	}
	defer procCloseHandle.Call(h)
	var create, exit, kernel, user filetime
	procGetProcessTimes.Call(h, uintptr(unsafe.Pointer(&create)), uintptr(unsafe.Pointer(&exit)), uintptr(unsafe.Pointer(&kernel)), uintptr(unsafe.Pointer(&user)))
	cpu := kernel.Uint64() + user.Uint64()
	var io ioCounters
	procGetProcessIoCounters.Call(h, uintptr(unsafe.Pointer(&io)))
	cur := processPrevious{CPU: cpu, Read: io.ReadBytes, Write: io.WriteBytes, At: now}
	next[p.PID] = cur
	if !prev.At.IsZero() && now.After(prev.At) {
		dt := now.Sub(prev.At).Seconds()
		if cpu >= prev.CPU {
			p.CPU = clampFloat(float64(cpu-prev.CPU)/1e7/dt/float64(runtime.NumCPU())*100, 0, 100)
		}
		if io.ReadBytes >= prev.Read {
			p.ReadBps = float64(io.ReadBytes-prev.Read) / dt
		}
		if io.WriteBytes >= prev.Write {
			p.WriteBps = float64(io.WriteBytes-prev.Write) / dt
		}
	}
	var mem processMemoryCountersEx
	mem.CB = uint32(unsafe.Sizeof(mem))
	if r, _, _ := procK32GetProcessMemoryInfo.Call(h, uintptr(unsafe.Pointer(&mem)), uintptr(mem.CB)); r != 0 {
		p.WorkingSet = uint64(mem.WorkingSetSize)
		p.PrivateBytes = uint64(mem.PrivateUsage)
	}
	var handles uint32
	procGetProcessHandleCount.Call(h, uintptr(unsafe.Pointer(&handles)))
	p.Handles = handles
	if c := create.Uint64(); c > 0 {
		p.Started = filetimeToTime(c)
	}
}

func queryProcessPath(pid uint32) string {
	h, _, _ := procOpenProcess.Call(PROCESS_QUERY_LIMITED_INFORMATION, 0, uintptr(pid))
	if h == 0 {
		return ""
	}
	defer procCloseHandle.Call(h)
	buf := make([]uint16, 32768)
	size := uint32(len(buf))
	r, _, _ := procQueryFullProcessImageNameW.Call(h, 0, uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)))
	if r == 0 || size == 0 {
		return ""
	}
	return utf16String(buf[:size])
}

func filetimeToTime(v uint64) time.Time {
	const epoch = 116444736000000000
	if v < epoch {
		return time.Time{}
	}
	return time.Unix(0, int64(v-epoch)*100)
}

func queryVolumes() []VolumeData {
	mask, _, _ := procGetLogicalDrives.Call()
	result := make([]VolumeData, 0, 4)
	for i := 0; i < 26; i++ {
		if mask&(1<<i) == 0 {
			continue
		}
		root := fmt.Sprintf("%c:\\", 'A'+i)
		if typ, _, _ := procGetDriveTypeW.Call(uintptr(unsafe.Pointer(utf16Ptr(root)))); typ != 3 {
			continue
		}
		var freeAvail, total, totalFree uint64
		if r, _, _ := procGetDiskFreeSpaceExW.Call(uintptr(unsafe.Pointer(utf16Ptr(root))), uintptr(unsafe.Pointer(&freeAvail)), uintptr(unsafe.Pointer(&total)), uintptr(unsafe.Pointer(&totalFree))); r == 0 {
			continue
		}
		label := make([]uint16, 128)
		fs := make([]uint16, 32)
		procGetVolumeInformationW.Call(uintptr(unsafe.Pointer(utf16Ptr(root))), uintptr(unsafe.Pointer(&label[0])), uintptr(len(label)), 0, 0, 0, uintptr(unsafe.Pointer(&fs[0])), uintptr(len(fs)))
		result = append(result, VolumeData{Name: root, Label: utf16String(label), FileSystem: utf16String(fs), Total: total, Free: totalFree})
	}
	return result
}

func newPDHQuery() *pdhQuery {
	p := &pdhQuery{}
	r, _, _ := procPdhOpenQueryW.Call(0, 0, uintptr(unsafe.Pointer(&p.handle)))
	if r != 0 {
		p.error = fmt.Sprintf("PdhOpenQuery: 0x%x", r)
		return p
	}
	p.add(`\PhysicalDisk(_Total)\% Disk Time`, &p.diskUsage)
	p.add(`\PhysicalDisk(_Total)\Disk Read Bytes/sec`, &p.diskRead)
	p.add(`\PhysicalDisk(_Total)\Disk Write Bytes/sec`, &p.diskWrite)
	p.add(`\PhysicalDisk(_Total)\Avg. Disk Queue Length`, &p.diskQueue)
	p.add(`\PhysicalDisk(_Total)\Avg. Disk sec/Transfer`, &p.diskLatency)
	p.add(`\GPU Engine(*)\Utilization Percentage`, &p.gpuUsage)
	p.add(`\GPU Adapter Memory(*)\Dedicated Usage`, &p.gpuDedicated)
	p.add(`\GPU Adapter Memory(*)\Shared Usage`, &p.gpuShared)
	procPdhCollectQueryData.Call(p.handle)
	return p
}

func (p *pdhQuery) add(path string, dst *uintptr) {
	if p.handle == 0 {
		return
	}
	r, _, _ := procPdhAddEnglishCounterW.Call(p.handle, uintptr(unsafe.Pointer(utf16Ptr(path))), 0, uintptr(unsafe.Pointer(dst)))
	if r != 0 && p.error == "" {
		p.error = fmt.Sprintf("counter %s: 0x%x", path, r)
	}
}
func (p *pdhQuery) Close() {
	if p.handle != 0 {
		procPdhCloseQuery.Call(p.handle)
		p.handle = 0
	}
}
func (p *pdhQuery) Sample() (DiskData, GPUData) {
	d := DiskData{ProviderError: p.error}
	g := GPUData{Provider: "Windows GPU performance counters", Error: p.error}
	if p.handle == 0 {
		return d, g
	}
	r, _, _ := procPdhCollectQueryData.Call(p.handle)
	if r != 0 {
		d.ProviderError = fmt.Sprintf("PDH collect: 0x%x", r)
		g.Error = d.ProviderError
		return d, g
	}
	d.Usage = clampFloat(p.value(p.diskUsage), 0, 100)
	d.ReadBps = math.Max(0, p.value(p.diskRead))
	d.WriteBps = math.Max(0, p.value(p.diskWrite))
	d.Queue = math.Max(0, p.value(p.diskQueue))
	d.LatencyMs = math.Max(0, p.value(p.diskLatency)*1000)
	g.Usage = clampFloat(p.arrayMax(p.gpuUsage), 0, 100)
	g.DedicatedUsed = math.Max(0, p.arraySum(p.gpuDedicated))
	g.SharedUsed = math.Max(0, p.arraySum(p.gpuShared))
	g.Available = p.gpuUsage != 0 && g.Error == ""
	return d, g
}
func (p *pdhQuery) value(counter uintptr) float64 {
	if counter == 0 {
		return 0
	}
	var v pdhFormattedValue
	r, _, _ := procPdhGetFormattedCounterValue.Call(counter, PDH_FMT_DOUBLE, 0, uintptr(unsafe.Pointer(&v)))
	if r != 0 || v.Status != 0 || math.IsNaN(v.Value) || math.IsInf(v.Value, 0) {
		return 0
	}
	return v.Value
}
func (p *pdhQuery) array(counter uintptr) []pdhValueItem {
	if counter == 0 {
		return nil
	}
	var size, count uint32
	r, _, _ := procPdhGetFormattedCounterArrayW.Call(counter, PDH_FMT_DOUBLE, uintptr(unsafe.Pointer(&size)), uintptr(unsafe.Pointer(&count)), 0)
	if r != PDH_MORE_DATA || size == 0 {
		return nil
	}
	buf := make([]byte, size)
	r, _, _ = procPdhGetFormattedCounterArrayW.Call(counter, PDH_FMT_DOUBLE, uintptr(unsafe.Pointer(&size)), uintptr(unsafe.Pointer(&count)), uintptr(unsafe.Pointer(&buf[0])))
	if r != 0 || count == 0 {
		return nil
	}
	return append([]pdhValueItem(nil), unsafe.Slice((*pdhValueItem)(unsafe.Pointer(&buf[0])), count)...)
}
func (p *pdhQuery) arrayMax(counter uintptr) float64 {
	m := 0.0
	for _, it := range p.array(counter) {
		if it.Value.Status == 0 && it.Value.Value > m {
			m = it.Value.Value
		}
	}
	return m
}
func (p *pdhQuery) arraySum(counter uintptr) float64 {
	s := 0.0
	for _, it := range p.array(counter) {
		if it.Value.Status == 0 && it.Value.Value > 0 {
			s += it.Value.Value
		}
	}
	return s
}

func querySystemData() SystemData {
	var v osVersionInfo
	v.Size = uint32(unsafe.Sizeof(v))
	procRtlGetVersion.Call(uintptr(unsafe.Pointer(&v)))
	host, _ := os.Hostname()
	m, _ := queryMemory()
	manufacturer, _ := regString(HKEY_LOCAL_MACHINE, `HARDWARE\DESCRIPTION\System\BIOS`, `SystemManufacturer`)
	model, _ := regString(HKEY_LOCAL_MACHINE, `HARDWARE\DESCRIPTION\System\BIOS`, `SystemProductName`)
	bios, _ := regString(HKEY_LOCAL_MACHINE, `HARDWARE\DESCRIPTION\System\BIOS`, `BIOSVersion`)
	cpu, _ := regString(HKEY_LOCAL_MACHINE, `HARDWARE\DESCRIPTION\System\CentralProcessor\0`, `ProcessorNameString`)
	gpu := queryGPUName()
	ticks, _, _ := procGetTickCount64.Call()
	cores, logical := queryCPUTopology()
	return SystemData{Windows: windowsEdition(v.Major, v.Minor, v.Build), Build: fmt.Sprintf("%d", v.Build), Architecture: runtime.GOARCH, Hostname: host, Manufacturer: manufacturer, Model: model, BIOS: bios, CPU: strings.TrimSpace(cpu), GPU: gpu, Cores: cores, Logical: logical, InstalledRAM: m.Total, BootTime: time.Now().Add(-time.Duration(ticks) * time.Millisecond)}
}

func queryCPUTopology() (int, int) {
	var size uint32
	procGetLogicalProcessorInformationEx.Call(0, 0, uintptr(unsafe.Pointer(&size)))
	if size < 8 {
		return runtime.NumCPU(), runtime.NumCPU()
	}
	buf := make([]byte, size)
	r, _, _ := procGetLogicalProcessorInformationEx.Call(0, uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)))
	if r == 0 {
		return runtime.NumCPU(), runtime.NumCPU()
	}
	cores := 0
	for offset := uint32(0); offset+8 <= size; {
		base := unsafe.Add(unsafe.Pointer(&buf[0]), offset)
		relation := *(*uint32)(base)
		entrySize := *(*uint32)(unsafe.Add(base, 4))
		if entrySize < 8 || offset+entrySize > size {
			break
		}
		if relation == 0 {
			cores++
		}
		offset += entrySize
	}
	if cores == 0 {
		cores = runtime.NumCPU()
	}
	return cores, runtime.NumCPU()
}
func windowsEdition(major, minor, build uint32) string {
	if major >= 10 && build >= 22000 {
		return "Windows 11"
	}
	if major >= 10 {
		return "Windows 10"
	}
	return fmt.Sprintf("Windows %d.%d", major, minor)
}

func regString(root uintptr, path, name string) (string, error) {
	var key uintptr
	r, _, err := procRegOpenKeyExW.Call(root, uintptr(unsafe.Pointer(utf16Ptr(path))), 0, KEY_READ, uintptr(unsafe.Pointer(&key)))
	if r != 0 {
		return "", err
	}
	defer procRegCloseKey.Call(key)
	var typ, size uint32
	r, _, err = procRegQueryValueExW.Call(key, uintptr(unsafe.Pointer(utf16Ptr(name))), 0, uintptr(unsafe.Pointer(&typ)), 0, uintptr(unsafe.Pointer(&size)))
	if r != 0 || (typ != REG_SZ && typ != REG_EXPAND_SZ) {
		return "", err
	}
	buf := make([]uint16, size/2+1)
	r, _, err = procRegQueryValueExW.Call(key, uintptr(unsafe.Pointer(utf16Ptr(name))), 0, uintptr(unsafe.Pointer(&typ)), uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)))
	if r != 0 {
		return "", err
	}
	return utf16String(buf), nil
}
func queryGPUName() string {
	path := `SYSTEM\CurrentControlSet\Control\Class\{4d36e968-e325-11ce-bfc1-08002be10318}`
	var key uintptr
	r, _, _ := procRegOpenKeyExW.Call(HKEY_LOCAL_MACHINE, uintptr(unsafe.Pointer(utf16Ptr(path))), 0, KEY_READ, uintptr(unsafe.Pointer(&key)))
	if r != 0 {
		return "Windows GPU"
	}
	defer procRegCloseKey.Call(key)
	names := make([]string, 0, 2)
	for i := uint32(0); i < 32; i++ {
		buf := make([]uint16, 64)
		n := uint32(len(buf))
		r, _, _ := procRegEnumKeyExW.Call(key, uintptr(i), uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&n)), 0, 0, 0, 0)
		if r != 0 {
			break
		}
		sub := path + `\` + utf16String(buf[:n])
		if value, err := regString(HKEY_LOCAL_MACHINE, sub, "DriverDesc"); err == nil && value != "" && !containsString(names, value) {
			names = append(names, value)
		}
	}
	if len(names) == 0 {
		return "Windows GPU"
	}
	return strings.Join(names, " + ")
}

func utf16String(value []uint16) string {
	for i, v := range value {
		if v == 0 {
			value = value[:i]
			break
		}
	}
	return string(utf16.Decode(value))
}
func counterDelta(cur, prev uint64) uint64 {
	if cur >= prev {
		return cur - prev
	}
	return 0
}
func maxU64(a, b uint64) uint64 {
	if a > b {
		return a
	}
	return b
}
func clampFloat(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
func containsString(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}
