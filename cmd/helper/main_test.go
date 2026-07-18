package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/helper"
)

// TestRunFailsClosed verifies the seed executable cannot redeem even a canonical opaque reference.
func TestRunFailsClosed(t *testing.T) {
	request := helper.Request{
		Version:         helper.ProtocolVersion,
		TicketReference: helper.TicketReference("rrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrr"),
	}
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	var output bytes.Buffer
	err = run(context.Background(), bytes.NewReader(body), &output, fixedClock{now: time.Now().UTC()})
	if !errors.Is(err, helper.ErrTicketRedemptionUnavailable) {
		t.Fatalf("run error = %v, want ticket redemption unavailable", err)
	}
	var response helper.Response
	if err := json.Unmarshal(output.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.OK || response.Error == nil || response.Error.Code != helper.ErrorCodeAuthenticationUnavailable {
		t.Fatalf("unexpected response: %#v", response)
	}
}

type fixedClock struct {
	now time.Time
}

// Now returns the deterministic main-package test time.
func (c fixedClock) Now() time.Time {
	return c.now
}
