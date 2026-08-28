package main

import (
	"os"
	"path/filepath"
	"testing"

	"kerneon/core"
)

func TestConfigStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	store := &ConfigStore{path: filepath.Join(dir, "settings.json"), logger: &Logger{}}
	config := core.DefaultConfig()
	config.Window.LastPage = "network"
	config.Sampling.NetworkHz = 120
	if err := store.Save(config); err != nil {
		t.Fatalf("save settings: %v", err)
	}
	loaded := store.Load()
	if loaded.Window.LastPage != "network" || loaded.Sampling.NetworkHz != 120 {
		t.Fatalf("round trip changed settings: %+v", loaded)
	}
	temporary, err := filepath.Glob(filepath.Join(dir, "settings-*.tmp"))
	if err != nil || len(temporary) != 0 {
		t.Fatalf("atomic save left temporary files: %v %v", temporary, err)
	}
}

func TestConfigStorePreservesMalformedFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(path, []byte(`{"version":2,"sampling":`), 0o600); err != nil {
		t.Fatal(err)
	}
	store := &ConfigStore{path: path, logger: &Logger{}}
	loaded := store.Load()
	if loaded.Version != core.ConfigVersion || loaded.Sampling.NetworkHz != 60 {
		t.Fatalf("defaults were not restored: %+v", loaded)
	}
	backups, err := filepath.Glob(path + ".corrupt-*")
	if err != nil || len(backups) != 1 {
		t.Fatalf("malformed settings were not preserved: %v %v", backups, err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("replacement settings missing: %v", err)
	}
}
