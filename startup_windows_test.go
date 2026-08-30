//go:build windows

package main

import "testing"

func TestStartupCommandQuotesExecutablePath(t *testing.T) {
	got := startupCommand(`C:\Program Files\Kerneon\Kerneon.exe`)
	want := `"C:\Program Files\Kerneon\Kerneon.exe" --startup`
	if got != want {
		t.Fatalf("startup command = %q, want %q", got, want)
	}
}
