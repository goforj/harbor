package launcher

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/network/identity"
)

// TestInvokeWritesCanonicalRequestAndSucceeds verifies one transport attempt and correlated success evidence.
func TestInvokeWritesCanonicalRequestAndSucceeds(t *testing.T) {
	now := time.Date(2026, time.July, 19, 12, 0, 0, 0, time.UTC)
	issued := validLaunchTicket(t, now)
	calls := 0
	transport := transportFunc(func(_ context.Context, request io.Reader, response io.Writer) TransportResult {
		calls++
		decoded, err := helper.DecodeRequest(request)
		if err != nil {
			t.Fatalf("decode transport request: %v", err)
		}
		if decoded.Version != helper.ProtocolVersion || decoded.TicketReference != issued.reference {
			t.Fatalf("transport request = %#v", decoded)
		}
		if err := helper.WriteResponse(response, successResponse(issued.operation, issued.address)); err != nil {
			t.Fatalf("write transport response: %v", err)
		}
		return TransportResult{State: TransportCompleted, ExitCode: ExitCodeSucceeded}
	})

	outcome, err := New(transport, fixedClock{now: now}).Invoke(nil, issued)
	if err != nil {
		t.Fatalf("invoke launcher: %v", err)
	}
	if calls != 1 {
		t.Fatalf("transport calls = %d, want 1", calls)
	}
	if outcome.State != Succeeded || outcome.Exit == nil || outcome.Exit.Code != ExitCodeSucceeded {
		t.Fatalf("outcome = %#v", outcome)
	}
	if !reflect.DeepEqual(outcome.Response, successResponse(issued.operation, issued.address)) {
		t.Fatalf("response = %#v", outcome.Response)
	}
	if strings.Contains(fmt.Sprintf("%#v", outcome), string(issued.reference)) {
		t.Fatal("outcome exposed the opaque ticket reference")
	}
}

// TestInvokePoolWritesCanonicalRequestAndSucceeds verifies aggregate consent correlates all decoded pool evidence.
func TestInvokePoolWritesCanonicalRequestAndSucceeds(t *testing.T) {
	now := time.Date(2026, time.July, 19, 12, 0, 0, 0, time.UTC)
	issued := validPoolLaunchTicket(t, now)
	wantResponse := poolSuccessResponse(issued.pool)
	calls := 0
	transport := transportFunc(func(_ context.Context, request io.Reader, response io.Writer) TransportResult {
		calls++
		decoded, err := helper.DecodeRequest(request)
		if err != nil {
			t.Fatalf("decode transport request: %v", err)
		}
		if decoded.Version != helper.ProtocolVersion || decoded.TicketReference != issued.reference {
			t.Fatalf("transport request = %#v", decoded)
		}
		if err := helper.WriteResponse(response, wantResponse); err != nil {
			t.Fatalf("write transport response: %v", err)
		}
		return TransportResult{State: TransportCompleted, ExitCode: ExitCodeSucceeded}
	})

	outcome, err := New(transport, fixedClock{now: now}).InvokePool(nil, issued)
	if err != nil {
		t.Fatalf("InvokePool() error = %v", err)
	}
	if calls != 1 || outcome.State != Succeeded || outcome.Exit == nil || outcome.Exit.Code != ExitCodeSucceeded {
		t.Fatalf("calls = %d, outcome = %#v", calls, outcome)
	}
	if !reflect.DeepEqual(outcome.Response, wantResponse) {
		t.Fatalf("response = %#v, want %#v", outcome.Response, wantResponse)
	}
	if strings.Contains(fmt.Sprintf("%#v", outcome), string(issued.reference)) {
		t.Fatal("pool outcome exposed the opaque ticket reference")
	}
}

// TestInvokeClassifiesValidHelperFailure verifies structured helper rejection remains distinct from launch failure.
func TestInvokeClassifiesValidHelperFailure(t *testing.T) {
	now := time.Date(2026, time.July, 19, 12, 0, 0, 0, time.UTC)
	issued := validLaunchTicket(t, now)
	transport := transportFunc(func(_ context.Context, _ io.Reader, response io.Writer) TransportResult {
		if err := helper.WriteResponse(response, failureResponse()); err != nil {
			t.Fatalf("write transport response: %v", err)
		}
		return TransportResult{State: TransportCompleted, ExitCode: ExitCodeHelperFailed}
	})

	outcome, err := New(transport, fixedClock{now: now}).Invoke(context.Background(), issued)
	if err != nil {
		t.Fatalf("invoke launcher: %v", err)
	}
	if outcome.State != HelperFailed || outcome.Exit == nil || outcome.Exit.Code != ExitCodeHelperFailed {
		t.Fatalf("outcome = %#v", outcome)
	}
	if outcome.Response.Error == nil || outcome.Response.Error.Code != helper.ErrorCodeAuthenticationFailed {
		t.Fatalf("helper failure = %#v", outcome.Response)
	}
}

// TestInvokePoolPreservesExistingOutcomeSemantics verifies aggregate launches change only success correlation.
func TestInvokePoolPreservesExistingOutcomeSemantics(t *testing.T) {
	now := time.Date(2026, time.July, 19, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		configure func(context.CancelFunc, PoolLaunchTicket, io.Writer) TransportResult
		want      OutcomeState
		wantExit  bool
	}{
		{
			name: "helper failed",
			configure: func(_ context.CancelFunc, _ PoolLaunchTicket, response io.Writer) TransportResult {
				if err := helper.WriteResponse(response, failureResponse()); err != nil {
					t.Fatalf("write helper failure: %v", err)
				}
				return TransportResult{State: TransportCompleted, ExitCode: ExitCodeHelperFailed}
			},
			want: HelperFailed, wantExit: true,
		},
		{
			name: "declined",
			configure: func(_ context.CancelFunc, _ PoolLaunchTicket, _ io.Writer) TransportResult {
				return TransportResult{State: TransportDeclined}
			},
			want: Declined,
		},
		{
			name: "unavailable",
			configure: func(_ context.CancelFunc, _ PoolLaunchTicket, _ io.Writer) TransportResult {
				return TransportResult{State: TransportUnavailable}
			},
			want: Unavailable,
		},
		{
			name: "transport indeterminate",
			configure: func(_ context.CancelFunc, _ PoolLaunchTicket, _ io.Writer) TransportResult {
				return TransportResult{State: TransportIndeterminate}
			},
			want: Indeterminate,
		},
		{
			name: "mismatched pool",
			configure: func(_ context.CancelFunc, _ PoolLaunchTicket, response io.Writer) TransportResult {
				if err := helper.WriteResponse(response, poolSuccessResponse(netip.MustParsePrefix("127.77.0.16/29"))); err != nil {
					t.Fatalf("write mismatched pool response: %v", err)
				}
				return TransportResult{State: TransportCompleted, ExitCode: ExitCodeSucceeded}
			},
			want: Indeterminate, wantExit: true,
		},
		{
			name: "mismatched operation",
			configure: func(_ context.CancelFunc, _ PoolLaunchTicket, response io.Writer) TransportResult {
				if err := helper.WriteResponse(response, successResponse(helper.OperationEnsureLoopbackIdentity, netip.MustParseAddr("127.77.0.8"))); err != nil {
					t.Fatalf("write mismatched operation response: %v", err)
				}
				return TransportResult{State: TransportCompleted, ExitCode: ExitCodeSucceeded}
			},
			want: Indeterminate, wantExit: true,
		},
		{
			name: "success with helper failure exit",
			configure: func(_ context.CancelFunc, issued PoolLaunchTicket, response io.Writer) TransportResult {
				if err := helper.WriteResponse(response, poolSuccessResponse(issued.pool)); err != nil {
					t.Fatalf("write pool response: %v", err)
				}
				return TransportResult{State: TransportCompleted, ExitCode: ExitCodeHelperFailed}
			},
			want: Indeterminate, wantExit: true,
		},
		{
			name: "post-start cancellation",
			configure: func(cancel context.CancelFunc, issued PoolLaunchTicket, response io.Writer) TransportResult {
				if err := helper.WriteResponse(response, poolSuccessResponse(issued.pool)); err != nil {
					t.Fatalf("write pool response: %v", err)
				}
				cancel()
				return TransportResult{State: TransportCompleted, ExitCode: ExitCodeSucceeded}
			},
			want: Indeterminate, wantExit: true,
		},
		{
			name: "decline with response",
			configure: func(_ context.CancelFunc, _ PoolLaunchTicket, response io.Writer) TransportResult {
				_, _ = io.WriteString(response, "unexpected")
				return TransportResult{State: TransportDeclined}
			},
			want: Indeterminate,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			issued := validPoolLaunchTicket(t, now)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			transport := transportFunc(func(_ context.Context, _ io.Reader, response io.Writer) TransportResult {
				calls++
				return test.configure(cancel, issued, response)
			})
			outcome, err := New(transport, fixedClock{now: now}).InvokePool(ctx, issued)
			if err != nil {
				t.Fatalf("InvokePool() error = %v", err)
			}
			if calls != 1 || outcome.State != test.want || (outcome.Exit != nil) != test.wantExit {
				t.Fatalf("calls = %d, outcome = %#v", calls, outcome)
			}
			if test.want == HelperFailed {
				if outcome.Response.Error == nil || outcome.Response.Error.Code != helper.ErrorCodeAuthenticationFailed {
					t.Fatalf("helper failure = %#v", outcome.Response)
				}
			} else if outcome.Response.Version != 0 {
				t.Fatalf("unexpected response = %#v", outcome.Response)
			}
		})
	}
}

// TestInvokeRejectsResponseExitMismatches verifies process status and structured response must agree.
func TestInvokeRejectsResponseExitMismatches(t *testing.T) {
	now := time.Date(2026, time.July, 19, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name     string
		response func(LaunchTicket) helper.Response
		exitCode int
	}{
		{
			name: "success with helper failure exit",
			response: func(issued LaunchTicket) helper.Response {
				return successResponse(issued.operation, issued.address)
			},
			exitCode: ExitCodeHelperFailed,
		},
		{
			name: "success with unknown exit",
			response: func(issued LaunchTicket) helper.Response {
				return successResponse(issued.operation, issued.address)
			},
			exitCode: 2,
		},
		{
			name:     "failure with success exit",
			response: func(LaunchTicket) helper.Response { return failureResponse() },
			exitCode: ExitCodeSucceeded,
		},
		{
			name:     "failure with unknown exit",
			response: func(LaunchTicket) helper.Response { return failureResponse() },
			exitCode: 2,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			issued := validLaunchTicket(t, now)
			transport := transportFunc(func(_ context.Context, _ io.Reader, response io.Writer) TransportResult {
				if err := helper.WriteResponse(response, test.response(issued)); err != nil {
					t.Fatalf("write transport response: %v", err)
				}
				return TransportResult{State: TransportCompleted, ExitCode: test.exitCode}
			})
			outcome, err := New(transport, fixedClock{now: now}).Invoke(context.Background(), issued)
			if err != nil {
				t.Fatalf("invoke launcher: %v", err)
			}
			if outcome.State != Indeterminate || outcome.Exit == nil || outcome.Exit.Code != test.exitCode || outcome.Response.Version != 0 {
				t.Fatalf("outcome = %#v", outcome)
			}
		})
	}
}

// TestInvokePreservesProvenNoChildOutcomes verifies only native prelaunch proofs become decline or unavailable.
func TestInvokePreservesProvenNoChildOutcomes(t *testing.T) {
	now := time.Date(2026, time.July, 19, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		transport TransportState
		want      OutcomeState
	}{
		{name: "declined", transport: TransportDeclined, want: Declined},
		{name: "unavailable", transport: TransportUnavailable, want: Unavailable},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			transport := transportFunc(func(_ context.Context, request io.Reader, _ io.Writer) TransportResult {
				calls++
				if _, err := helper.DecodeRequest(request); err != nil {
					t.Fatalf("decode transport request: %v", err)
				}
				return TransportResult{State: test.transport}
			})
			outcome, err := New(transport, fixedClock{now: now}).Invoke(context.Background(), validLaunchTicket(t, now))
			if err != nil {
				t.Fatalf("invoke launcher: %v", err)
			}
			if calls != 1 || outcome.State != test.want || outcome.Exit != nil || outcome.Response.Version != 0 {
				t.Fatalf("calls = %d, outcome = %#v", calls, outcome)
			}
		})
	}
}

// TestInvokeClassifiesUncertainCompletions verifies every possibly-started ambiguous exchange remains indeterminate.
func TestInvokeClassifiesUncertainCompletions(t *testing.T) {
	now := time.Date(2026, time.July, 19, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		configure func(context.CancelFunc, LaunchTicket, io.Writer) TransportResult
		wantExit  bool
	}{
		{
			name: "transport indeterminate",
			configure: func(_ context.CancelFunc, _ LaunchTicket, _ io.Writer) TransportResult {
				return TransportResult{State: TransportIndeterminate}
			},
		},
		{
			name: "unknown transport state",
			configure: func(_ context.CancelFunc, _ LaunchTicket, _ io.Writer) TransportResult {
				return TransportResult{}
			},
		},
		{
			name: "completed without response",
			configure: func(_ context.CancelFunc, _ LaunchTicket, _ io.Writer) TransportResult {
				return TransportResult{State: TransportCompleted, ExitCode: 1}
			},
			wantExit: true,
		},
		{
			name: "malformed response",
			configure: func(_ context.CancelFunc, _ LaunchTicket, response io.Writer) TransportResult {
				_, _ = io.WriteString(response, "not-json")
				return TransportResult{State: TransportCompleted, ExitCode: 1}
			},
			wantExit: true,
		},
		{
			name: "oversized response",
			configure: func(_ context.CancelFunc, _ LaunchTicket, response io.Writer) TransportResult {
				body := strings.Repeat("x", helper.MaxResponseBytes*2)
				written, err := io.WriteString(response, body)
				if err != nil || written != len(body) {
					t.Fatalf("write oversized response = %d, %v", written, err)
				}
				return TransportResult{State: TransportCompleted, ExitCode: 1}
			},
			wantExit: true,
		},
		{
			name: "mismatched operation",
			configure: func(_ context.CancelFunc, issued LaunchTicket, response io.Writer) TransportResult {
				if err := helper.WriteResponse(response, successResponse(helper.OperationEnsureLoopbackIdentity, issued.address)); err != nil {
					t.Fatalf("write transport response: %v", err)
				}
				return TransportResult{State: TransportCompleted}
			},
			wantExit: true,
		},
		{
			name: "mismatched address",
			configure: func(_ context.CancelFunc, issued LaunchTicket, response io.Writer) TransportResult {
				if err := helper.WriteResponse(response, successResponse(issued.operation, netip.MustParseAddr("127.77.0.11"))); err != nil {
					t.Fatalf("write transport response: %v", err)
				}
				return TransportResult{State: TransportCompleted}
			},
			wantExit: true,
		},
		{
			name: "post-start cancellation",
			configure: func(cancel context.CancelFunc, issued LaunchTicket, response io.Writer) TransportResult {
				if err := helper.WriteResponse(response, successResponse(issued.operation, issued.address)); err != nil {
					t.Fatalf("write transport response: %v", err)
				}
				cancel()
				return TransportResult{State: TransportCompleted}
			},
			wantExit: true,
		},
		{
			name: "decline with response",
			configure: func(_ context.CancelFunc, _ LaunchTicket, response io.Writer) TransportResult {
				_, _ = io.WriteString(response, "unexpected")
				return TransportResult{State: TransportDeclined}
			},
		},
		{
			name: "unavailable with response",
			configure: func(_ context.CancelFunc, _ LaunchTicket, response io.Writer) TransportResult {
				_, _ = io.WriteString(response, "unexpected")
				return TransportResult{State: TransportUnavailable}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			issued := validLaunchTicket(t, now)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			transport := transportFunc(func(_ context.Context, _ io.Reader, response io.Writer) TransportResult {
				calls++
				return test.configure(cancel, issued, response)
			})
			outcome, err := New(transport, fixedClock{now: now}).Invoke(ctx, issued)
			if err != nil {
				t.Fatalf("invoke launcher: %v", err)
			}
			if calls != 1 || outcome.State != Indeterminate || outcome.Response.Version != 0 {
				t.Fatalf("calls = %d, outcome = %#v", calls, outcome)
			}
			if (outcome.Exit != nil) != test.wantExit {
				t.Fatalf("outcome exit = %#v, want present %t", outcome.Exit, test.wantExit)
			}
		})
	}
}

// TestInvokeRejectsInvalidLaunchTicketBeforeTransport verifies invalid capabilities never reach native launch.
func TestInvokeRejectsInvalidLaunchTicketBeforeTransport(t *testing.T) {
	now := time.Date(2026, time.July, 19, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name   string
		mutate func(*LaunchTicket)
	}{
		{name: "operation ID", mutate: func(ticket *LaunchTicket) { ticket.operationID = "" }},
		{name: "lease key", mutate: func(ticket *LaunchTicket) { ticket.leaseKey = identity.LeaseKey{} }},
		{name: "reference", mutate: func(ticket *LaunchTicket) { ticket.reference = "short" }},
		{name: "helper operation", mutate: func(ticket *LaunchTicket) { ticket.operation = "run_command" }},
		{name: "address", mutate: func(ticket *LaunchTicket) { ticket.address = netip.MustParseAddr("192.0.2.10") }},
		{name: "zero expiry", mutate: func(ticket *LaunchTicket) { ticket.expiresAt = time.Time{} }},
		{name: "non-UTC expiry", mutate: func(ticket *LaunchTicket) { ticket.expiresAt = now.In(time.FixedZone("test", 0)).Add(time.Minute) }},
		{name: "expired", mutate: func(ticket *LaunchTicket) { ticket.expiresAt = now }},
		{name: "excessive lifetime", mutate: func(ticket *LaunchTicket) { ticket.expiresAt = now.Add(helper.MaxTicketLifetime + time.Nanosecond) }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			issued := validLaunchTicket(t, now)
			test.mutate(&issued)
			calls := 0
			transport := transportFunc(func(context.Context, io.Reader, io.Writer) TransportResult {
				calls++
				return TransportResult{State: TransportUnavailable}
			})
			outcome, err := New(transport, fixedClock{now: now}).Invoke(context.Background(), issued)
			if err == nil {
				t.Fatal("expected metadata validation error")
			}
			if calls != 0 || outcome.State != "" {
				t.Fatalf("calls = %d, outcome = %#v", calls, outcome)
			}
			if strings.Contains(err.Error(), string(issued.reference)) {
				t.Fatal("validation error exposed the opaque ticket reference")
			}
		})
	}
}

// TestInvokePoolRejectsInvalidLaunchTicketBeforeTransport verifies forged aggregate metadata never opens native consent.
func TestInvokePoolRejectsInvalidLaunchTicketBeforeTransport(t *testing.T) {
	now := time.Date(2026, time.July, 19, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name   string
		mutate func(*PoolLaunchTicket)
	}{
		{name: "operation ID", mutate: func(ticket *PoolLaunchTicket) { ticket.operationID = "" }},
		{name: "reference", mutate: func(ticket *PoolLaunchTicket) { ticket.reference = "short" }},
		{name: "helper operation", mutate: func(ticket *PoolLaunchTicket) { ticket.operation = helper.OperationEnsureLoopbackIdentity }},
		{name: "zero pool", mutate: func(ticket *PoolLaunchTicket) { ticket.pool = netip.Prefix{} }},
		{name: "non-loopback pool", mutate: func(ticket *PoolLaunchTicket) { ticket.pool = netip.MustParsePrefix("192.0.2.8/29") }},
		{name: "wrong prefix", mutate: func(ticket *PoolLaunchTicket) { ticket.pool = netip.MustParsePrefix("127.77.0.0/24") }},
		{name: "host bits", mutate: func(ticket *PoolLaunchTicket) { ticket.pool = netip.MustParsePrefix("127.77.0.9/29") }},
		{name: "zero expiry", mutate: func(ticket *PoolLaunchTicket) { ticket.expiresAt = time.Time{} }},
		{name: "non-UTC expiry", mutate: func(ticket *PoolLaunchTicket) {
			ticket.expiresAt = now.In(time.FixedZone("test", 0)).Add(time.Minute)
		}},
		{name: "expired", mutate: func(ticket *PoolLaunchTicket) { ticket.expiresAt = now }},
		{name: "excessive lifetime", mutate: func(ticket *PoolLaunchTicket) {
			ticket.expiresAt = now.Add(helper.MaxTicketLifetime + time.Nanosecond)
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			issued := validPoolLaunchTicket(t, now)
			test.mutate(&issued)
			calls := 0
			transport := transportFunc(func(context.Context, io.Reader, io.Writer) TransportResult {
				calls++
				return TransportResult{State: TransportUnavailable}
			})
			outcome, err := New(transport, fixedClock{now: now}).InvokePool(context.Background(), issued)
			if err == nil {
				t.Fatal("expected pool metadata validation error")
			}
			if calls != 0 || outcome.State != "" {
				t.Fatalf("calls = %d, outcome = %#v", calls, outcome)
			}
			if strings.Contains(err.Error(), string(issued.reference)) {
				t.Fatal("validation error exposed the opaque ticket reference")
			}
		})
	}
}

// TestInvokeReturnsPrelaunchCancellationWithoutTransport verifies a cancelled caller cannot open native consent.
func TestInvokeReturnsPrelaunchCancellationWithoutTransport(t *testing.T) {
	now := time.Date(2026, time.July, 19, 12, 0, 0, 0, time.UTC)
	calls := 0
	transport := transportFunc(func(context.Context, io.Reader, io.Writer) TransportResult {
		calls++
		return TransportResult{State: TransportUnavailable}
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	outcome, err := New(transport, fixedClock{now: now}).Invoke(ctx, validLaunchTicket(t, now))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("invoke error = %v, want context canceled", err)
	}
	if calls != 0 || outcome.State != "" {
		t.Fatalf("calls = %d, outcome = %#v", calls, outcome)
	}
}

// TestInvokePoolReturnsPrelaunchCancellationWithoutTransport verifies cancellation precedes aggregate metadata and consent.
func TestInvokePoolReturnsPrelaunchCancellationWithoutTransport(t *testing.T) {
	now := time.Date(2026, time.July, 19, 12, 0, 0, 0, time.UTC)
	calls := 0
	transport := transportFunc(func(context.Context, io.Reader, io.Writer) TransportResult {
		calls++
		return TransportResult{State: TransportUnavailable}
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	outcome, err := New(transport, fixedClock{now: now}).InvokePool(ctx, validPoolLaunchTicket(t, now))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("InvokePool() error = %v, want context canceled", err)
	}
	if calls != 0 || outcome.State != "" {
		t.Fatalf("calls = %d, outcome = %#v", calls, outcome)
	}
}

// TestNewRequiresDependencies verifies security-critical collaborators cannot be omitted.
func TestNewRequiresDependencies(t *testing.T) {
	clock := fixedClock{now: time.Date(2026, time.July, 19, 12, 0, 0, 0, time.UTC)}
	transport := transportFunc(func(context.Context, io.Reader, io.Writer) TransportResult {
		return TransportResult{State: TransportUnavailable}
	})
	var typedNilTransport transportFunc
	var typedNilClock *fixedClock
	tests := []struct {
		name  string
		build func()
	}{
		{name: "transport", build: func() { New(nil, clock) }},
		{name: "typed nil transport", build: func() { New(typedNilTransport, clock) }},
		{name: "clock", build: func() { New(transport, nil) }},
		{name: "typed nil clock", build: func() { New(transport, typedNilClock) }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("expected constructor panic")
				}
			}()
			test.build()
		})
	}
}

// TestBoundedResponseWriterDiscardsOverflow verifies a noisy child cannot grow launcher memory without bound.
func TestBoundedResponseWriterDiscardsOverflow(t *testing.T) {
	writer := &boundedResponseWriter{}
	body := bytes.Repeat([]byte{'x'}, helper.MaxResponseBytes*2)
	written, err := writer.Write(body)
	if err != nil || written != len(body) {
		t.Fatalf("write = %d, %v", written, err)
	}
	written, err = writer.Write(body)
	if err != nil || written != len(body) {
		t.Fatalf("second write = %d, %v", written, err)
	}
	if len(writer.Bytes()) != helper.MaxResponseBytes+1 {
		t.Fatalf("captured bytes = %d, want %d", len(writer.Bytes()), helper.MaxResponseBytes+1)
	}
}

type transportFunc func(context.Context, io.Reader, io.Writer) TransportResult

// Invoke adapts a deterministic function to the native transport contract.
func (invoke transportFunc) Invoke(ctx context.Context, request io.Reader, response io.Writer) TransportResult {
	return invoke(ctx, request, response)
}

type fixedClock struct {
	now time.Time
}

// Now returns the deterministic launcher admission instant.
func (clock fixedClock) Now() time.Time {
	return clock.now
}

// validLaunchTicket returns one complete short-lived release capability for launcher tests.
func validLaunchTicket(t *testing.T, now time.Time) LaunchTicket {
	t.Helper()
	leaseKey, err := identity.NewPrimaryKey(domain.ProjectID("project-1"))
	if err != nil {
		t.Fatalf("create lease key: %v", err)
	}
	ticket, err := NewLaunchTicket(
		domain.OperationID("operation-1"),
		leaseKey,
		helper.TicketReference(strings.Repeat("a", 64)),
		helper.OperationReleaseLoopbackIdentity,
		"127.77.0.10",
		now.Add(time.Minute),
	)
	if err != nil {
		t.Fatalf("create launch ticket: %v", err)
	}
	return ticket
}

// validPoolLaunchTicket returns one complete short-lived aggregate pool capability for launcher tests.
func validPoolLaunchTicket(t *testing.T, now time.Time) PoolLaunchTicket {
	t.Helper()
	ticket, err := NewPoolLaunchTicket(
		"operation-pool",
		helper.TicketReference(strings.Repeat("c", 64)),
		helper.OperationEnsureLoopbackPool,
		"127.77.0.8/29",
		now.Add(time.Minute),
	)
	if err != nil {
		t.Fatalf("create pool launch ticket: %v", err)
	}
	return ticket
}

// successResponse returns valid postcondition evidence for one allowlisted operation and address.
func successResponse(operation helper.Operation, address netip.Addr) helper.Response {
	state := helper.ObservationOwned
	if operation == helper.OperationReleaseLoopbackIdentity {
		state = helper.ObservationAbsent
	}
	return helper.Response{
		Version: helper.ProtocolVersion,
		OK:      true,
		Result: &helper.OperationResult{
			Operation: operation,
			Evidence: helper.MutationEvidence{
				Changed: true,
				Address: address.String(),
				Observation: helper.ExpectedObservation{
					State:       state,
					Fingerprint: strings.Repeat("b", 64),
				},
			},
		},
	}
}

// poolSuccessResponse returns valid owned postcondition evidence for every address in one canonical /29.
func poolSuccessResponse(pool netip.Prefix) helper.Response {
	identities := make([]helper.MutationEvidence, 0, 8)
	address := pool.Addr()
	for range 8 {
		identities = append(identities, helper.MutationEvidence{
			Changed: true,
			Address: address.String(),
			Observation: helper.ExpectedObservation{
				State:       helper.ObservationOwned,
				Fingerprint: strings.Repeat("d", 64),
			},
		})
		address = address.Next()
	}
	return helper.Response{
		Version: helper.ProtocolVersion,
		OK:      true,
		Result: &helper.OperationResult{
			Operation: helper.OperationEnsureLoopbackPool,
			PoolEvidence: &helper.PoolMutationEvidence{
				Pool:       pool.String(),
				Identities: identities,
			},
		},
	}
}

// failureResponse returns one valid bounded helper authentication failure.
func failureResponse() helper.Response {
	return helper.Response{
		Version: helper.ProtocolVersion,
		OK:      false,
		Error: &helper.ResponseError{
			Code:    helper.ErrorCodeAuthenticationFailed,
			Message: "helper ticket redemption failed",
		},
	}
}
