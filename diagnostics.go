package main

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"kerneon/core"
)

func (a *App) systemSummary() string {
	s := a.snapshot
	return fmt.Sprintf("Kerneon %s\nCreated and published by %s\n%s (build %s, %s)\nComputer: %s %s\nCPU: %s\nGPU: %s\nRAM: %s\nSystem volume: %s free of %s\nNetwork: %s (%s)\nUptime: %s",
		appVersion, appPublisher, s.System.Windows, s.System.Build, s.System.Architecture,
		s.System.Manufacturer, s.System.Model, s.System.CPU, s.System.GPU,
		core.FormatBytes(s.System.InstalledRAM), core.FormatBytes(s.Disk.Free), core.FormatBytes(s.Disk.Total),
		s.Network.Name, s.Network.Kind, formatDuration(time.Since(s.System.BootTime)))
}

func (a *App) exportDiagnostics() (string, error) {
	base := os.Getenv("LOCALAPPDATA")
	if base == "" {
		base, _ = os.UserCacheDir()
	}
	dir := filepath.Join(base, appName, "exports")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "Kerneon-Diagnostics-"+time.Now().Format("20060102-150405")+".zip")
	f, err := os.Create(path)
	if err != nil {
		return "", err
	}
	zw := zip.NewWriter(f)
	closeWith := func(current error) error {
		if e := zw.Close(); current == nil {
			current = e
		}
		if e := f.Close(); current == nil {
			current = e
		}
		return current
	}
	manifest := map[string]any{"application": "Kerneon", "version": appVersion, "creator": appCreator, "publisher": appPublisher, "created_utc": time.Now().UTC(), "privacy": "Usernames, home paths, IP addresses and process paths are sanitized.", "system": a.systemSummary(), "snapshot_age_ms": time.Since(a.snapshot.At).Milliseconds(), "providers": map[string]string{"gpu": a.snapshot.GPU.Provider, "gpu_error": a.snapshot.GPU.Error, "disk_error": a.snapshot.Disk.ProviderError}}
	if err := zipJSON(zw, "manifest.json", manifest); err != nil {
		return "", closeWith(err)
	}
	if err := zipJSON(zw, "settings.json", a.config); err != nil {
		return "", closeWith(err)
	}
	if data, readErr := os.ReadFile(a.logger.Path()); readErr == nil {
		entry, createErr := zw.Create("Kerneon.log")
		if createErr != nil {
			return "", closeWith(createErr)
		}
		if _, err = io.WriteString(entry, sanitize(string(data))); err != nil {
			return "", closeWith(err)
		}
	}
	return path, closeWith(nil)
}

func zipJSON(zw *zip.Writer, name string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	w, err := zw.Create(name)
	if err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
}

var ipv4Pattern = regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b`)

func sanitize(value string) string {
	if user := os.Getenv("USERNAME"); user != "" {
		value = strings.ReplaceAll(value, user, "<user>")
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		value = strings.ReplaceAll(value, home, "<home>")
	}
	return ipv4Pattern.ReplaceAllString(value, "<ip>")
}
