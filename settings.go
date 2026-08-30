package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"kerneon/core"
)

type ConfigStore struct {
	path       string
	logger     *Logger
	writerOnce sync.Once
	writes     chan configWriteRequest
}

type configWriteRequest struct {
	config core.Config
	done   chan error
}

func NewConfigStore(logger *Logger) *ConfigStore {
	base, err := os.UserConfigDir()
	if err != nil || base == "" {
		base = "."
	}
	return &ConfigStore{path: filepath.Join(base, appName, "settings.json"), logger: logger}
}

func (s *ConfigStore) Load() core.Config {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		s.logger.Error("settings", "create config directory", err)
	}
	data, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		if migrated, ok := s.loadPulseNet(); ok {
			_ = s.Save(migrated)
			return migrated
		}
		return core.DefaultConfig()
	}
	if err != nil {
		s.logger.Error("settings", "read settings", err)
		return core.DefaultConfig()
	}
	cfg, parseErr := core.ParseConfig(data)
	if parseErr == nil {
		return cfg
	}
	s.logger.Error("settings", "settings invalid; restored defaults", parseErr)
	corrupt := s.path + ".corrupt-" + time.Now().Format("20060102-150405")
	if err := os.Rename(s.path, corrupt); err != nil {
		s.logger.Error("settings", "preserve malformed settings", err)
	}
	_ = s.Save(cfg)
	return cfg
}

func (s *ConfigStore) loadPulseNet() (core.Config, bool) {
	base, err := os.UserConfigDir()
	if err != nil {
		return core.Config{}, false
	}
	data, err := os.ReadFile(filepath.Join(base, "PulseNet", "settings.json"))
	if err != nil {
		return core.Config{}, false
	}
	cfg, err := core.ParseConfig(data)
	if err != nil {
		s.logger.Error("settings", "legacy PulseNet settings could not be migrated", err)
		return core.Config{}, false
	}
	s.logger.Info("settings", "migrated PulseNet settings")
	return cfg, true
}

func (s *ConfigStore) Save(cfg core.Config) error {
	s.ensureWriter()
	done := make(chan error, 1)
	s.writes <- configWriteRequest{config: cfg, done: done}
	return <-done
}

// SaveAsync keeps routine UI state persistence off the window thread. Every
// write still travels through the same FIFO worker as crash-critical Save
// calls, so a slow disk cannot make a toggle stutter or reorder settings.
func (s *ConfigStore) SaveAsync(cfg core.Config) {
	s.ensureWriter()
	s.writes <- configWriteRequest{config: cfg}
}

func (s *ConfigStore) ensureWriter() {
	s.writerOnce.Do(func() {
		s.writes = make(chan configWriteRequest, 64)
		go func() {
			for request := range s.writes {
				err := s.saveFile(request.config)
				if request.done != nil {
					request.done <- err
					continue
				}
				if err != nil && s.logger != nil {
					s.logger.Error("settings", "async save", err)
				}
			}
		}()
	})
}

func (s *ConfigStore) saveFile(cfg core.Config) error {
	core.ValidateConfig(&cfg)
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), "settings-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err = tmp.Write(data); err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err := replaceFile(tmpName, s.path); err != nil {
		return fmt.Errorf("replace settings: %w", err)
	}
	return nil
}

func (s *ConfigStore) Path() string { return s.path }
