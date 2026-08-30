//go:build windows

package main

import (
	"fmt"
	"os"
	"strings"

	"golang.org/x/sys/windows/registry"
)

const (
	startupRegistryPath = `Software\Microsoft\Windows\CurrentVersion\Run`
	startupValueName    = "Kerneon"
)

func startupCommand(executable string) string {
	return `"` + strings.ReplaceAll(executable, `"`, "") + `" --startup`
}

func startupEntryEnabled() bool {
	executable, err := os.Executable()
	if err != nil {
		return false
	}
	key, err := registry.OpenKey(registry.CURRENT_USER, startupRegistryPath, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer key.Close()
	value, _, err := key.GetStringValue(startupValueName)
	return err == nil && strings.EqualFold(strings.TrimSpace(value), startupCommand(executable))
}

func setStartupEntry(enabled bool) error {
	key, _, err := registry.CreateKey(registry.CURRENT_USER, startupRegistryPath, registry.SET_VALUE|registry.QUERY_VALUE)
	if err != nil {
		return fmt.Errorf("open current-user startup settings: %w", err)
	}
	defer key.Close()
	if !enabled {
		if err := key.DeleteValue(startupValueName); err != nil && err != registry.ErrNotExist {
			return fmt.Errorf("remove Kerneon startup entry: %w", err)
		}
		return nil
	}
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve Kerneon executable: %w", err)
	}
	if err := key.SetStringValue(startupValueName, startupCommand(executable)); err != nil {
		return fmt.Errorf("save Kerneon startup entry: %w", err)
	}
	return nil
}
