package main

import "time"

const (
	appName    = "Kerneon"
	appVersion = "1.0.0"
)

type CPUData struct {
	Usage, Kernel, FrequencyMHz, MaxMHz float64
	Cores, Logical                      int
	Model                               string
}

type MemoryData struct {
	Total, Used, Available, Cached               uint64
	Commit, CommitLimit, PagedPool, NonPagedPool uint64
	UsagePercent, CommitPercent                  float64
}

type GPUData struct {
	Usage, DedicatedUsed, DedicatedLimit, SharedUsed float64
	Model, Provider, Error                           string
	Available                                        bool
}

type DiskData struct {
	Usage, ReadBps, WriteBps, Queue, LatencyMs float64
	Total, Free                                uint64
	Volumes                                    []VolumeData
	ProviderError                              string
}

type VolumeData struct {
	Name, Label, FileSystem string
	Total, Free             uint64
}

type NetworkData struct {
	AdapterIndex                                      uint32
	Name, Description, Kind, IPv4, IPv6, Gateway, DNS string
	MTU                                               uint32
	LinkDown, LinkUp                                  uint64
	DownBps, UpBps, PeakDown, PeakUp                  float64
	RxPPS, TxPPS, Utilization                         float64
	SessionDown, SessionUp                            uint64
	Errors, Discards                                  uint64
	LatencyMs, JitterMs, PacketLoss                   float64
	PingSamples                                       int
	SampleCount                                       uint64
	PingOK, Connected                                 bool
	LastError                                         string
}

type ProcessMetric struct {
	PID, Parent, Threads, Handles uint32
	Name, Path                    string
	CPU                           float64
	WorkingSet, PrivateBytes      uint64
	ReadBps, WriteBps             float64
	Started                       time.Time
}

type HistorySample struct {
	At                                        time.Time
	CPU, GPU, Memory, Disk, Down, Up, Latency float64
}

type Event struct {
	At       time.Time
	Kind     string
	Title    string
	Detail   string
	Severity int
}

type SystemData struct {
	Windows, Build, Architecture, Hostname string
	Manufacturer, Model, BIOS              string
	CPU, GPU                               string
	Cores, Logical                         int
	InstalledRAM                           uint64
	BootTime                               time.Time
}

type Snapshot struct {
	At                                     time.Time
	CPU                                    CPUData
	Memory                                 MemoryData
	GPU                                    GPUData
	Disk                                   DiskData
	Network                                NetworkData
	Processes                              []ProcessMetric
	History                                []HistorySample
	Events                                 []Event
	System                                 SystemData
	ProcessCount, ThreadCount, HandleCount uint32
}

type DisplayState struct {
	CPU, GPU, Memory, Disk, Down, Up, Latency float64
	updated                                   time.Time
}
