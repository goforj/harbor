//go:build !darwin

package main

import (
	"context"
	"errors"
	"testing"

	"github.com/goforj/harbor/internal/helper"
)

// TestUnsupportedLowPortAdapterComposition proves non-Darwin builds fail closed without retaining resources.
func TestUnsupportedLowPortAdapterComposition(t *testing.T) {
	handler, err := openPlatformLowPortHandler()
	if err != nil {
		t.Fatalf("openPlatformLowPortHandler() error = %v", err)
	}
	if _, ok := handler.(unavailableClosingLowPortHandler); !ok {
		t.Fatalf("openPlatformLowPortHandler() type = %T, want unavailableClosingLowPortHandler", handler)
	}
	if _, err := handler.EnsureLowPorts(context.Background(), helper.Ticket{}, helper.TicketAdmission{}); !errors.Is(err, helper.ErrMutationUnavailable) {
		t.Fatalf("EnsureLowPorts() error = %v, want ErrMutationUnavailable", err)
	}
	if _, err := handler.ReleaseLowPorts(context.Background(), helper.Ticket{}, helper.TicketAdmission{}); !errors.Is(err, helper.ErrMutationUnavailable) {
		t.Fatalf("ReleaseLowPorts() error = %v, want ErrMutationUnavailable", err)
	}
	if err := handler.Close(); err != nil {
		t.Fatalf("handler.Close() error = %v", err)
	}
}
