package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"time"
)

type Logger struct {
	mu   sync.Mutex
	file *os.File
	dir  string
	path string
}

func NewLogger() *Logger {
	base := os.Getenv("LOCALAPPDATA")
	if base == "" {
		base, _ = os.UserCacheDir()
	}
	dir := filepath.Join(base, appName, "logs")
	_ = os.MkdirAll(dir, 0o755)
	l := &Logger{dir: dir, path: filepath.Join(dir, "Kerneon.log")}
	l.rotate()
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err == nil {
		l.file = f
	}
	return l
}

func (l *Logger) rotate() {
	info, err := os.Stat(l.path)
	if err != nil || info.Size() < 1_000_000 {
		return
	}
	_ = os.Remove(l.path + ".3")
	_ = os.Rename(l.path+".2", l.path+".3")
	_ = os.Rename(l.path+".1", l.path+".2")
	_ = os.Rename(l.path, l.path+".1")
}

func (l *Logger) write(level, subsystem, message string, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return
	}
	line := fmt.Sprintf("%s %-5s [%s] %s", time.Now().UTC().Format(time.RFC3339Nano), level, subsystem, strings.ReplaceAll(message, "\n", " "))
	if err != nil {
		line += ": " + strings.ReplaceAll(err.Error(), "\n", " ")
	}
	_, _ = fmt.Fprintln(l.file, line)
}

func (l *Logger) Info(subsystem, message string) { l.write("INFO", subsystem, message, nil) }
func (l *Logger) Error(subsystem, message string, err error) {
	l.write("ERROR", subsystem, message, err)
}
func (l *Logger) Panic(subsystem string, recovered any) {
	l.write("FATAL", subsystem, fmt.Sprintf("panic: %v\n%s", recovered, debug.Stack()), nil)
}
func (l *Logger) Close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file != nil {
		_ = l.file.Sync()
		_ = l.file.Close()
		l.file = nil
	}
}
func (l *Logger) Dir() string  { return l.dir }
func (l *Logger) Path() string { return l.path }
