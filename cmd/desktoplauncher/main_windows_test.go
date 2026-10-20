//go:build windows

package main

import (
	"errors"
	"testing"
)

// TestWindowsDesktopLauncher rejects arguments before activation and preserves the selected desktop's outcome.
func TestWindowsDesktopLauncher(t *testing.T) {
	if _, err := runDesktopLauncher([]string{"--foreign"}, func() (uint32, error) { t.Fatal("desktop activated with arguments"); return 0, nil }); err == nil {
		t.Fatal("desktop argument accepted")
	}
	failure := errors.New("desktop native admission failed")
	code, err := runDesktopLauncher(nil, func() (uint32, error) { return 19, failure })
	if code != 19 || !errors.Is(err, failure) {
		t.Fatalf("code=%d, error=%v", code, err)
	}
}
