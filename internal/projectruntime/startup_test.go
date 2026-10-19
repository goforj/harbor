package projectruntime

import (
	"strings"
	"testing"
)

// TestStartupEventValidationRejectsUnsafeWireValues covers the optional contract's transport boundary.
func TestStartupEventValidationRejectsUnsafeWireValues(t *testing.T) {
	valid := StartupEvent{SchemaVersion: 1, Session: "session", Sequence: 1, Phase: "task", Label: "Setup", Status: "completed", ElapsedMS: 12}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*StartupEvent){
		"version":  func(event *StartupEvent) { event.SchemaVersion = 2 },
		"sequence": func(event *StartupEvent) { event.Sequence = 0 },
		"duration": func(event *StartupEvent) { event.ElapsedMS = -1 },
		"session":  func(event *StartupEvent) { event.Session = "" },
		"path":     func(event *StartupEvent) { event.Session = "../other" },
		"label":    func(event *StartupEvent) { event.Label = strings.Repeat("x", 257) },
		"controls": func(event *StartupEvent) { event.Label = "Setup\nInjected" },
		"phase":    func(event *StartupEvent) { event.Phase = "unknown" },
		"status":   func(event *StartupEvent) { event.Status = "ready" },
	} {
		t.Run(name, func(t *testing.T) {
			event := valid
			mutate(&event)
			if err := event.Validate(); err == nil {
				t.Fatal("unsafe event accepted")
			}
		})
	}
	for name, progress := range map[string]StartupProgress{
		"session mismatch": {Events: []StartupEvent{valid}},
		"too many records": {Events: make([]StartupEvent, 257)},
	} {
		t.Run(name, func(t *testing.T) {
			if err := progress.Validate("other"); err == nil {
				t.Fatal("unsafe progress accepted")
			}
		})
	}
}

// TestLaunchDiagnosticsValidationRejectsUnsafeWireValues prevents Engine display metadata from expanding control responses.
func TestLaunchDiagnosticsValidationRejectsUnsafeWireValues(t *testing.T) {
	valid := PortConflict{Service: "mailpit", Address: "127.77.1.2", Port: 1025, Protocol: "tcp", Listener: "other-mailpit", Bound: "0.0.0.0"}
	if err := (LaunchDiagnostics{PortConflicts: []PortConflict{valid}}).Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*PortConflict){
		"port":     func(conflict *PortConflict) { conflict.Port = 0 },
		"protocol": func(conflict *PortConflict) { conflict.Protocol = "other" },
		"address":  func(conflict *PortConflict) { conflict.Address = "hostname" },
		"name":     func(conflict *PortConflict) { conflict.Listener = "other\nInjected" },
	} {
		t.Run(name, func(t *testing.T) {
			conflict := valid
			mutate(&conflict)
			if err := (LaunchDiagnostics{PortConflicts: []PortConflict{conflict}}).Validate(); err == nil {
				t.Fatal("unsafe conflict accepted")
			}
		})
	}
	if err := (LaunchDiagnostics{PortConflicts: make([]PortConflict, 17)}).Validate(); err == nil {
		t.Fatal("unbounded diagnostics accepted")
	}
}
