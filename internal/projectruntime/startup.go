package projectruntime

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"unicode"
	"unicode/utf8"
)

// StartupEvent is diagnostic progress from the runtime, independent of Harbor readiness and process ownership.
type StartupEvent struct {
	SchemaVersion int    `json:"schema_version"`
	Session       string `json:"session"`
	Sequence      uint64 `json:"sequence"`
	Phase         string `json:"phase"`
	Label         string `json:"label"`
	Status        string `json:"status"`
	ElapsedMS     int64  `json:"elapsed_ms"`
}

// Validate bounds an event before it crosses the desktop transport.
func (event StartupEvent) Validate() error {
	if event.SchemaVersion != 1 || event.Sequence == 0 || event.Sequence > 256 || event.ElapsedMS < 0 || event.ElapsedMS > 7*24*60*60*1000 {
		return errors.New("invalid startup progress bounds")
	}
	if event.Session == "" || len(event.Session) > 128 || !utf8.ValidString(event.Session) || strings.ContainsAny(event.Session, "/\\\x00\n\r") {
		return errors.New("invalid startup progress session")
	}
	if event.Label == "" || len(event.Label) > 256 || !utf8.ValidString(event.Label) || strings.IndexFunc(event.Label, unicode.IsControl) >= 0 {
		return errors.New("invalid startup progress label")
	}
	switch event.Phase {
	case "startup", "task", "app.prepare", "frontend.build":
	default:
		return errors.New("unknown startup progress phase")
	}
	switch event.Status {
	case "started", "completed", "failed", "cancelled":
	default:
		return errors.New("unknown startup progress status")
	}
	return nil
}

// StartupProgress retains at most one startup's bounded diagnostic records.
type StartupProgress struct {
	Events  []StartupEvent `json:"events"`
	Invalid bool           `json:"invalid,omitempty"`
}

// Validate prevents mixed sessions and unbounded metadata in activity responses.
func (progress StartupProgress) Validate(session string) error {
	if len(progress.Events) > 256 {
		return errors.New("too many startup progress events")
	}
	encoded, err := json.Marshal(progress)
	if err != nil || len(encoded) > 40*1024 {
		return errors.New("startup progress metadata exceeds its bound")
	}
	for index, event := range progress.Events {
		if err := event.Validate(); err != nil {
			return err
		}
		if event.Session != session || event.Sequence != uint64(index+1) {
			return errors.New("startup progress session or sequence mismatch")
		}
	}
	return nil
}

// PortConflict reports a fresh competing container publication without granting authority over it.
type PortConflict struct {
	Service  string `json:"service"`
	Address  string `json:"address"`
	Port     uint16 `json:"port"`
	Protocol string `json:"protocol"`
	Listener string `json:"listener"`
	Bound    string `json:"bound"`
}

// LaunchDiagnostics contains bounded, non-secret observations from the local Engine.
type LaunchDiagnostics struct {
	PortConflicts []PortConflict `json:"port_conflicts"`
}

// Validate bounds read-only diagnostics before desktop transport without admitting lifecycle authority.
func (diagnostics LaunchDiagnostics) Validate() error {
	if len(diagnostics.PortConflicts) > 16 {
		return errors.New("too many port conflict diagnostics")
	}
	for _, conflict := range diagnostics.PortConflicts {
		if conflict.Port == 0 || (conflict.Protocol != "tcp" && conflict.Protocol != "udp") {
			return errors.New("invalid diagnostic port")
		}
		for _, address := range []string{conflict.Address, conflict.Bound} {
			if _, err := netip.ParseAddr(address); err != nil {
				return fmt.Errorf("invalid diagnostic address: %w", err)
			}
		}
		for _, name := range []string{conflict.Service, conflict.Listener} {
			if name == "" || len(name) > 256 || !utf8.ValidString(name) || strings.IndexFunc(name, unicode.IsControl) >= 0 {
				return errors.New("invalid diagnostic name")
			}
		}
	}
	return nil
}
