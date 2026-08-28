package core

import (
	"encoding/json"
	"errors"
	"fmt"
)

const ConfigVersion = 2

type SamplingConfig struct {
	NetworkHz int  `json:"network_hz"`
	GraphFPS  int  `json:"graph_fps"`
	CPUms     int  `json:"cpu_ms"`
	Processms int  `json:"process_ms"`
	SensorSec int  `json:"sensor_sec"`
	Adaptive  bool `json:"adaptive"`
}

type Config struct {
	Version int `json:"version"`
	Window  struct {
		Width       int    `json:"width"`
		Height      int    `json:"height"`
		X           int    `json:"x"`
		Y           int    `json:"y"`
		Maximized   bool   `json:"maximized"`
		LastPage    string `json:"last_page"`
		AlwaysTop   bool   `json:"always_on_top"`
		CloseToTray bool   `json:"close_to_tray"`
	} `json:"window"`
	Sampling SamplingConfig `json:"sampling"`
	Network  struct {
		AdapterIndex uint32   `json:"adapter_index"`
		Units        UnitMode `json:"units"`
		PingTarget   string   `json:"ping_target"`
		PingSeconds  int      `json:"ping_seconds"`
	} `json:"network"`
	Appearance struct {
		ReducedMotion bool `json:"reduced_motion"`
		GraphSeconds  int  `json:"graph_seconds"`
	} `json:"appearance"`
	History struct {
		Enabled          bool `json:"enabled"`
		RetentionMinutes int  `json:"retention_minutes"`
	} `json:"history"`
	Gaming struct {
		GameFocus bool `json:"game_focus"`
	} `json:"gaming"`
}

func DefaultConfig() Config {
	var c Config
	c.Version = ConfigVersion
	c.Window.Width, c.Window.Height = 1240, 800
	c.Window.X, c.Window.Y = -1, -1
	c.Window.LastPage = "overview"
	c.Window.CloseToTray = true
	c.Sampling = SamplingConfig{NetworkHz: 60, GraphFPS: 30, CPUms: 500, Processms: 1500, SensorSec: 5, Adaptive: true}
	c.Network.Units = UnitAuto
	c.Network.PingTarget, c.Network.PingSeconds = "1.1.1.1", 2
	c.Appearance.GraphSeconds = 60
	c.History.Enabled, c.History.RetentionMinutes = true, 60
	c.Gaming.GameFocus = true
	return c
}

func ParseConfig(data []byte) (Config, error) {
	if len(data) == 0 {
		return DefaultConfig(), nil
	}
	var header struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(data, &header); err != nil {
		return DefaultConfig(), fmt.Errorf("decode settings: %w", err)
	}
	if header.Version == 0 {
		return migrateLegacy(data)
	}
	if header.Version > ConfigVersion {
		return DefaultConfig(), errors.New("settings were created by a newer Kerneon version")
	}
	c := DefaultConfig()
	if err := json.Unmarshal(data, &c); err != nil {
		return DefaultConfig(), fmt.Errorf("decode settings: %w", err)
	}
	ValidateConfig(&c)
	c.Version = ConfigVersion
	return c, nil
}

func migrateLegacy(data []byte) (Config, error) {
	var old struct {
		PollingHz       int      `json:"polling_hz"`
		AdapterIndex    uint32   `json:"adapter_index"`
		GraphSeconds    int      `json:"graph_seconds"`
		Units           UnitMode `json:"units"`
		PingTarget      string   `json:"ping_target"`
		PingIntervalSec int      `json:"ping_interval_sec"`
		AlwaysOnTop     bool     `json:"always_on_top"`
	}
	if err := json.Unmarshal(data, &old); err != nil {
		return DefaultConfig(), err
	}
	c := DefaultConfig()
	if old.PollingHz != 0 {
		c.Sampling.NetworkHz = old.PollingHz
	}
	c.Network.AdapterIndex, c.Window.AlwaysTop = old.AdapterIndex, old.AlwaysOnTop
	if old.GraphSeconds != 0 {
		c.Appearance.GraphSeconds = old.GraphSeconds
	}
	if old.Units != "" {
		c.Network.Units = old.Units
	}
	if old.PingTarget != "" {
		c.Network.PingTarget = old.PingTarget
	}
	if old.PingIntervalSec >= 0 {
		c.Network.PingSeconds = old.PingIntervalSec
	}
	ValidateConfig(&c)
	return c, nil
}

func ValidateConfig(c *Config) {
	c.Version = ConfigVersion
	c.Window.Width = clamp(c.Window.Width, 960, 5120)
	c.Window.Height = clamp(c.Window.Height, 640, 2880)
	if !oneOf(c.Window.LastPage, "overview", "cpu", "gpu", "memory", "storage", "network", "processes", "gaming", "insights", "history", "alerts", "system", "settings") {
		c.Window.LastPage = "overview"
	}
	if !oneOfInt(c.Sampling.NetworkHz, 10, 20, 30, 60, 90, 120) {
		c.Sampling.NetworkHz = 60
	}
	if !oneOfInt(c.Sampling.GraphFPS, 30, 60, 120) {
		c.Sampling.GraphFPS = 30
	}
	c.Sampling.CPUms = clamp(c.Sampling.CPUms, 200, 2000)
	c.Sampling.Processms = clamp(c.Sampling.Processms, 500, 10000)
	c.Sampling.SensorSec = clamp(c.Sampling.SensorSec, 2, 60)
	if c.Network.Units != UnitAuto && c.Network.Units != UnitBits && c.Network.Units != UnitBytes {
		c.Network.Units = UnitAuto
	}
	if c.Network.PingTarget == "" || len(c.Network.PingTarget) > 253 {
		c.Network.PingTarget = "1.1.1.1"
	}
	if !oneOfInt(c.Network.PingSeconds, 0, 1, 2, 5, 10) {
		c.Network.PingSeconds = 2
	}
	if !oneOfInt(c.Appearance.GraphSeconds, 30, 60, 120, 300) {
		c.Appearance.GraphSeconds = 60
	}
	c.History.RetentionMinutes = clamp(c.History.RetentionMinutes, 5, 10080)
}

func clamp(value, low, high int) int {
	if value < low {
		return low
	}
	if value > high {
		return high
	}
	return value
}
func oneOf(value string, values ...string) bool {
	for _, v := range values {
		if value == v {
			return true
		}
	}
	return false
}
func oneOfInt(value int, values ...int) bool {
	for _, v := range values {
		if value == v {
			return true
		}
	}
	return false
}
