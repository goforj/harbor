package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/helper"
)

// lifecycleLowPortHandler records when the helper releases its low-port authority.
type lifecycleLowPortHandler struct {
	helper.UnavailableLowPortHandler
	events   *[]string
	closeErr error
}

// Close records release of the reviewed native low-port boundary.
func (handler *lifecycleLowPortHandler) Close() error {
	*handler.events = append(*handler.events, "close low-port handler")
	return handler.closeErr
}

// TestProductionDependenciesIncludeLowPortComposition proves every platform makes availability an explicit choice.
func TestProductionDependenciesIncludeLowPortComposition(t *testing.T) {
	dependencies := productionDependencies()
	if dependencies.openLowPortHandler == nil {
		t.Fatal("production low-port dependency is nil")
	}
	handler, err := dependencies.openLowPortHandler()
	if err != nil {
		t.Fatalf("openLowPortHandler() error = %v", err)
	}
	if handler == nil {
		t.Fatal("openLowPortHandler() handler = nil")
	}
	if err := handler.Close(); err != nil {
		t.Fatalf("handler.Close() error = %v", err)
	}
}

// TestRunOpensLowPortAuthorityBeforeReadingAndClosesItFirst pins complete composition and reverse-order cleanup.
func TestRunOpensLowPortAuthorityBeforeReadingAndClosesItFirst(t *testing.T) {
	now := time.Date(2026, time.July, 22, 12, 0, 0, 0, time.UTC)
	reference, redemption := testRedemption(now)
	body, err := json.Marshal(helper.Request{Version: helper.ProtocolVersion, TicketReference: reference})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	events := make([]string, 0, 14)
	dependencies := successfulTestDependencies(&events, redemption)
	dependencies.openLowPortHandler = func() (closingLowPortHandler, error) {
		events = append(events, "open low-port handler")
		return &lifecycleLowPortHandler{events: &events}, nil
	}
	reader := &recordingReader{reader: bytes.NewReader(body), events: &events}
	var output bytes.Buffer

	if err := run(context.Background(), reader, &output, fixedClock{now: now}, dependencies); err != nil {
		t.Fatalf("run() error = %v", err)
	}
	wantEvents := []string{
		"authorize invocation",
		"open ticket redeemer",
		"open replay guard",
		"open resolver handler",
		"open low-port handler",
		"new loopback handler",
		"read request",
		"redeem ticket",
		"consume replay claim",
		"ensure loopback identity",
		"close low-port handler",
		"close resolver handler",
		"close replay guard",
		"close ticket redeemer",
	}
	if !slices.Equal(events, wantEvents) {
		t.Fatalf("events = %#v, want %#v", events, wantEvents)
	}
}

// TestRunLowPortOpenFailuresLeaveInputUnreadAndReleasePriorAuthority covers errors and invalid nil wiring.
func TestRunLowPortOpenFailuresLeaveInputUnreadAndReleasePriorAuthority(t *testing.T) {
	tests := []struct {
		name      string
		open      func(*[]string) func() (closingLowPortHandler, error)
		wantError string
	}{
		{
			name: "open error",
			open: func(events *[]string) func() (closingLowPortHandler, error) {
				return func() (closingLowPortHandler, error) {
					*events = append(*events, "open low-port handler")
					return nil, errors.New("low-port open failed")
				}
			},
			wantError: "low-port open failed",
		},
		{
			name: "nil handler",
			open: func(events *[]string) func() (closingLowPortHandler, error) {
				return func() (closingLowPortHandler, error) {
					*events = append(*events, "open low-port handler")
					return nil, nil
				}
			},
			wantError: "handler is nil",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			now := time.Date(2026, time.July, 22, 12, 0, 0, 0, time.UTC)
			_, redemption := testRedemption(now)
			events := make([]string, 0, 8)
			dependencies := successfulTestDependencies(&events, redemption)
			dependencies.openLowPortHandler = test.open(&events)
			reader := &recordingReader{reader: bytes.NewReader([]byte("{}")), events: &events}
			var output bytes.Buffer

			err := run(context.Background(), reader, &output, fixedClock{now: now}, dependencies)
			if err == nil || !bytes.Contains([]byte(err.Error()), []byte(test.wantError)) {
				t.Fatalf("run() error = %v, want text %q", err, test.wantError)
			}
			wantEvents := []string{
				"authorize invocation",
				"open ticket redeemer",
				"open replay guard",
				"open resolver handler",
				"open low-port handler",
				"close resolver handler",
				"close replay guard",
				"close ticket redeemer",
			}
			if !slices.Equal(events, wantEvents) {
				t.Fatalf("events = %#v, want %#v", events, wantEvents)
			}
			if reader.recorded || output.Len() != 0 {
				t.Fatalf("request recorded = %t, output = %q, want neither", reader.recorded, output.String())
			}
		})
	}
}

// TestRunJoinsLowPortCloseFailure proves cleanup failure remains visible after a successful served request.
func TestRunJoinsLowPortCloseFailure(t *testing.T) {
	now := time.Date(2026, time.July, 22, 12, 0, 0, 0, time.UTC)
	reference, redemption := testRedemption(now)
	body, err := json.Marshal(helper.Request{Version: helper.ProtocolVersion, TicketReference: reference})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	wantErr := errors.New("low-port close failed")
	events := make([]string, 0, 14)
	dependencies := successfulTestDependencies(&events, redemption)
	dependencies.openLowPortHandler = func() (closingLowPortHandler, error) {
		events = append(events, "open low-port handler")
		return &lifecycleLowPortHandler{events: &events, closeErr: wantErr}, nil
	}
	var output bytes.Buffer

	err = run(context.Background(), bytes.NewReader(body), &output, fixedClock{now: now}, dependencies)
	if !errors.Is(err, wantErr) {
		t.Fatalf("run() error = %v, want %v", err, wantErr)
	}
	var response helper.Response
	if decodeErr := json.Unmarshal(output.Bytes(), &response); decodeErr != nil {
		t.Fatalf("json.Unmarshal() error = %v", decodeErr)
	}
	if !response.OK {
		t.Fatalf("response = %#v, want successful served request", response)
	}
}

var _ closingLowPortHandler = (*lifecycleLowPortHandler)(nil)
