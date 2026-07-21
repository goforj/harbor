//go:build !darwin

package projectprocess

import (
	"context"
	"net/netip"
	"path/filepath"
	"testing"
)

// TestRuntimeRepairUnsupportedAdapterReturnsFixedState verifies other hosts never infer Darwin process authority.
func TestRuntimeRepairUnsupportedAdapterReturnsFixedState(t *testing.T) {
	checkout, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("EvalSymlinks() error = %v", err)
	}
	inspection, err := NewRuntimeRepairer().Inspect(context.Background(), RuntimeRepairTarget{
		CheckoutRoot: checkout,
		Endpoint:     netip.MustParseAddrPort("127.0.0.72:39471"),
	})
	if err != nil {
		t.Fatalf("Inspect() error = %v", err)
	}
	if inspection.State != RuntimeRepairInspectionUnsupported ||
		inspection.Diagnostic != RuntimeRepairDiagnosticPlatformUnsupported || inspection.Candidate != nil {
		t.Fatalf("Inspect() = %#v", inspection)
	}
	if err := inspection.Validate(); err != nil {
		t.Fatalf("inspection.Validate() error = %v", err)
	}
}

// TestUnattributedRuntimeUnsupportedAdapterReturnsFixedState verifies other hosts never infer unattributed process authority.
func TestUnattributedRuntimeUnsupportedAdapterReturnsFixedState(t *testing.T) {
	checkout, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("EvalSymlinks() error = %v", err)
	}
	inspection, err := NewUnattributedRuntimeInspector().Inspect(context.Background(), RuntimeRepairTarget{
		CheckoutRoot: checkout,
		Endpoint:     netip.MustParseAddrPort("127.0.0.72:39471"),
	})
	if err != nil {
		t.Fatalf("Inspect() error = %v", err)
	}
	if inspection.State != RuntimeRepairInspectionUnsupported ||
		inspection.Diagnostic != RuntimeRepairDiagnosticPlatformUnsupported || inspection.Candidate != nil {
		t.Fatalf("Inspect() = %#v", inspection)
	}
	if err := inspection.Validate(); err != nil {
		t.Fatalf("inspection.Validate() error = %v", err)
	}
}
