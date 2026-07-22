package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/netip"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/host/networkpolicy"
	"github.com/goforj/harbor/internal/host/ownership"
	"github.com/goforj/harbor/internal/trust/localca"
)

// TestRunTrustLifecycleClosesPrivilegedAuthorityBeforeTransition proves each admitted trust operation enters the user path only after durable replay admission is closed.
func TestRunTrustLifecycleClosesPrivilegedAuthorityBeforeTransition(t *testing.T) {
	now := time.Date(2026, time.July, 22, 12, 0, 0, 0, time.UTC)
	for _, operation := range []helper.Operation{
		helper.OperationEnsureTrust,
		helper.OperationReleaseTrust,
	} {
		t.Run(string(operation), func(t *testing.T) {
			reference, redemption := trustLifecycleRedemption(t, now, operation)
			events := make([]string, 0, 12)
			handler := &lifecycleTrustHandler{
				events:   &events,
				evidence: trustLifecycleEvidence(redemption.Ticket),
			}
			dependencies := successfulTestDependencies(&events, redemption)
			dependencies.transitionTrustIdentity = func(requester string) error {
				events = append(events, "transition trust identity")
				if requester != redemption.Ticket.RequesterIdentity {
					t.Fatalf("transition requester = %q, want %q", requester, redemption.Ticket.RequesterIdentity)
				}
				return nil
			}
			dependencies.transitionAdministratorTrustIdentity = func(string) error {
				t.Fatal("administrator trust identity transitioned for current-user ticket")
				return nil
			}
			dependencies.openTrustHandler = func() (closingTrustHandler, error) {
				events = append(events, "open trust handler")
				return handler, nil
			}
			dependencies.newLoopbackIdentityHandler = func() helper.LoopbackIdentityHandler {
				t.Fatal("loopback handler constructed for trust ticket")
				return nil
			}
			dependencies.openResolverHandler = func() (closingResolverHandler, error) {
				t.Fatal("resolver handler opened for trust ticket")
				return nil, nil
			}
			dependencies.openLowPortHandler = func() (closingLowPortHandler, error) {
				t.Fatal("low-port handler opened for trust ticket")
				return nil, nil
			}

			output, err := runTrustLifecycle(t, now, reference, dependencies)
			if err != nil {
				t.Fatalf("run() error = %v", err)
			}
			if handler.calls != 1 || handler.operation != operation {
				t.Fatalf("handler calls = %d, operation = %q", handler.calls, handler.operation)
			}
			if !output.OK || output.Result == nil || output.Result.Operation != operation || output.Result.TrustEvidence == nil {
				t.Fatalf("response = %#v", output)
			}
			wantEvents := []string{
				"authorize invocation",
				"open ticket redeemer",
				"open replay guard",
				"redeem ticket",
				"consume replay claim",
				"close replay guard",
				"close ticket redeemer",
				"transition trust identity",
				"open trust handler",
				"trust mutation",
				"close trust handler",
			}
			if !slices.Equal(events, wantEvents) {
				t.Fatalf("events = %#v, want %#v", events, wantEvents)
			}
		})
	}
}

// TestRunAdministratorTrustLifecycleRestoresRootIdentity proves the administrator handler opens only after its root identity transition.
func TestRunAdministratorTrustLifecycleRestoresRootIdentity(t *testing.T) {
	now := time.Date(2026, time.July, 22, 12, 0, 0, 0, time.UTC)
	reference, redemption := trustLifecycleRedemptionForMechanism(t, now, helper.OperationEnsureTrust, networkpolicy.DarwinAdministratorTrust)
	events := make([]string, 0, 12)
	handler := &lifecycleTrustHandler{events: &events, evidence: trustLifecycleEvidence(redemption.Ticket)}
	dependencies := successfulTestDependencies(&events, redemption)
	dependencies.transitionTrustIdentity = func(string) error {
		t.Fatal("administrator trust transitioned to requester identity")
		return nil
	}
	dependencies.transitionAdministratorTrustIdentity = func(requester string) error {
		events = append(events, "transition administrator trust identity")
		if requester != redemption.Ticket.RequesterIdentity {
			t.Fatalf("administrator transition requester = %q, want %q", requester, redemption.Ticket.RequesterIdentity)
		}
		return nil
	}
	dependencies.openTrustHandler = func() (closingTrustHandler, error) {
		t.Fatal("current-user trust handler opened for administrator ticket")
		return nil, nil
	}
	dependencies.openAdministratorTrustHandler = func() (closingTrustHandler, error) {
		events = append(events, "open administrator trust handler")
		return handler, nil
	}

	output, err := runTrustLifecycle(t, now, reference, dependencies)
	if err != nil {
		t.Fatalf("run() error = %v", err)
	}
	if !output.OK || handler.calls != 1 {
		t.Fatalf("response = %#v, handler calls = %d", output, handler.calls)
	}
	wantEvents := []string{
		"authorize invocation",
		"open ticket redeemer",
		"open replay guard",
		"redeem ticket",
		"consume replay claim",
		"close replay guard",
		"close ticket redeemer",
		"transition administrator trust identity",
		"open administrator trust handler",
		"trust mutation",
		"close trust handler",
	}
	if !slices.Equal(events, wantEvents) {
		t.Fatalf("events = %#v, want %#v", events, wantEvents)
	}
}

// TestRunAdministratorTrustLifecycleStopsAfterIdentityFailure proves a consumed administrator ticket cannot open Security.framework without restored root identity.
func TestRunAdministratorTrustLifecycleStopsAfterIdentityFailure(t *testing.T) {
	now := time.Date(2026, time.July, 22, 12, 0, 0, 0, time.UTC)
	reference, redemption := trustLifecycleRedemptionForMechanism(t, now, helper.OperationEnsureTrust, networkpolicy.DarwinAdministratorTrust)
	events := make([]string, 0, 10)
	dependencies := successfulTestDependencies(&events, redemption)
	transitionErr := errors.New("administrator identity transition failed")
	dependencies.transitionTrustIdentity = func(string) error {
		t.Fatal("current-user identity transition ran for administrator ticket")
		return nil
	}
	dependencies.transitionAdministratorTrustIdentity = func(requester string) error {
		events = append(events, "transition administrator trust identity")
		if requester != redemption.Ticket.RequesterIdentity {
			t.Fatalf("administrator transition requester = %q, want %q", requester, redemption.Ticket.RequesterIdentity)
		}
		return transitionErr
	}
	dependencies.openAdministratorTrustHandler = func() (closingTrustHandler, error) {
		t.Fatal("administrator trust handler opened after identity transition failure")
		return nil, nil
	}

	_, err := runTrustLifecycle(t, now, reference, dependencies)
	if !errors.Is(err, transitionErr) {
		t.Fatalf("run() error = %v, want %v", err, transitionErr)
	}
	wantEvents := []string{
		"authorize invocation",
		"open ticket redeemer",
		"open replay guard",
		"redeem ticket",
		"consume replay claim",
		"close replay guard",
		"close ticket redeemer",
		"transition administrator trust identity",
	}
	if !slices.Equal(events, wantEvents) {
		t.Fatalf("events = %#v, want %#v", events, wantEvents)
	}
}

// TestRunTrustLifecycleRejectsUnsupportedMechanismBeforeHandlers proves a verified ticket cannot select an unreviewed native trust scope.
func TestRunTrustLifecycleRejectsUnsupportedMechanismBeforeHandlers(t *testing.T) {
	now := time.Date(2026, time.July, 22, 12, 0, 0, 0, time.UTC)
	reference, redemption := trustLifecycleRedemptionForMechanisms(t, now, helper.OperationEnsureTrust, networkpolicy.UbuntuMechanisms())
	events := make([]string, 0, 8)
	dependencies := successfulTestDependencies(&events, redemption)
	dependencies.transitionTrustIdentity = func(string) error {
		t.Fatal("unsupported trust mechanism transitioned identity")
		return nil
	}
	dependencies.openTrustHandler = func() (closingTrustHandler, error) {
		t.Fatal("current-user trust handler opened for unsupported mechanism")
		return nil, nil
	}
	dependencies.openAdministratorTrustHandler = func() (closingTrustHandler, error) {
		t.Fatal("administrator trust handler opened for unsupported mechanism")
		return nil, nil
	}

	_, err := runTrustLifecycle(t, now, reference, dependencies)
	if err == nil {
		t.Fatal("run() accepted unsupported trust mechanism")
	}
}

// TestRunTrustLifecycleRejectsBeforeTransition proves failed redemption or replay admission never opens user trust authority.
func TestRunTrustLifecycleRejectsBeforeTransition(t *testing.T) {
	now := time.Date(2026, time.July, 22, 12, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name   string
		mutate func(*runtimeDependencies, *[]string)
		want   []string
	}{
		{
			name: "redemption failure",
			mutate: func(dependencies *runtimeDependencies, events *[]string) {
				dependencies.openTicketRedeemer = func() (closingTicketRedeemer, error) {
					return &testTicketRedeemer{
						events:    events,
						redeemErr: errors.New("redeem failed"),
					}, nil
				}
			},
			want: []string{
				"redeem ticket",
				"close replay guard",
				"close ticket redeemer",
			},
		},
		{
			name: "replay failure",
			mutate: func(dependencies *runtimeDependencies, events *[]string) {
				dependencies.openReplayGuard = func() (closingReplayGuard, error) {
					return &testReplayGuard{
						events:     events,
						consumeErr: errors.New("replay failed"),
					}, nil
				}
			},
			want: []string{
				"redeem ticket",
				"consume replay claim",
				"close replay guard",
				"close ticket redeemer",
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			reference, redemption := trustLifecycleRedemption(t, now, helper.OperationEnsureTrust)
			events := make([]string, 0, 8)
			dependencies := successfulTestDependencies(&events, redemption)
			test.mutate(&dependencies, &events)
			dependencies.transitionTrustIdentity = func(string) error {
				t.Fatal("trust identity transitioned before complete admission")
				return nil
			}
			dependencies.transitionAdministratorTrustIdentity = func(string) error {
				t.Fatal("administrator trust identity transitioned before complete admission")
				return nil
			}
			dependencies.openTrustHandler = func() (closingTrustHandler, error) {
				t.Fatal("trust handler opened before complete admission")
				return nil, nil
			}
			dependencies.openAdministratorTrustHandler = func() (closingTrustHandler, error) {
				t.Fatal("administrator trust handler opened before complete admission")
				return nil, nil
			}
			_, err := runTrustLifecycle(t, now, reference, dependencies)
			if err == nil || !trustLifecycleContains(events, test.want) {
				t.Fatalf("error = %v; events = %#v, want %#v", err, events, test.want)
			}
		})
	}
}

// TestRunTrustLifecycleStopsAfterPrivilegedCloseFailure proves a consumed ticket cannot reach demotion when its root-owned authority did not close cleanly.
func TestRunTrustLifecycleStopsAfterPrivilegedCloseFailure(t *testing.T) {
	now := time.Date(2026, time.July, 22, 12, 0, 0, 0, time.UTC)
	reference, redemption := trustLifecycleRedemption(t, now, helper.OperationEnsureTrust)
	events := make([]string, 0, 8)
	closeErr := errors.New("replay close failed")
	dependencies := successfulTestDependencies(&events, redemption)
	dependencies.openReplayGuard = func() (closingReplayGuard, error) {
		return &testReplayGuard{
			events:   &events,
			closeErr: closeErr,
		}, nil
	}
	dependencies.transitionTrustIdentity = func(string) error {
		t.Fatal("trust identity transitioned after privileged close failure")
		return nil
	}
	dependencies.transitionAdministratorTrustIdentity = func(string) error {
		t.Fatal("administrator trust identity transitioned after privileged close failure")
		return nil
	}
	dependencies.openTrustHandler = func() (closingTrustHandler, error) {
		t.Fatal("trust handler opened after privileged close failure")
		return nil, nil
	}
	_, err := runTrustLifecycle(t, now, reference, dependencies)
	if !errors.Is(err, closeErr) {
		t.Fatalf("run() error = %v, want %v", err, closeErr)
	}
	if !slices.Contains(events, "consume replay claim") || slices.Contains(events, "transition trust identity") {
		t.Fatalf("events = %#v", events)
	}
}

// TestRunTrustLifecycleFailureAfterAdmission proves identity and handler failures preserve one consumed admission without invoking trust mutation.
func TestRunTrustLifecycleFailureAfterAdmission(t *testing.T) {
	now := time.Date(2026, time.July, 22, 12, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name           string
		mutate         func(*runtimeDependencies, *[]string)
		afterAdmission []string
	}{
		{
			name: "transition failure",
			mutate: func(dependencies *runtimeDependencies, events *[]string) {
				dependencies.transitionTrustIdentity = func(string) error {
					*events = append(*events, "transition trust identity")
					return errors.New("transition failed")
				}
				dependencies.openTrustHandler = func() (closingTrustHandler, error) {
					t.Fatal("trust handler opened after identity transition failure")
					return nil, nil
				}
			},
			afterAdmission: []string{
				"transition trust identity",
			},
		},
		{
			name: "handler open failure",
			mutate: func(dependencies *runtimeDependencies, events *[]string) {
				dependencies.transitionTrustIdentity = func(string) error {
					*events = append(*events, "transition trust identity")
					return nil
				}
				dependencies.openTrustHandler = func() (closingTrustHandler, error) {
					*events = append(*events, "open trust handler")
					return nil, errors.New("trust open failed")
				}
			},
			afterAdmission: []string{
				"transition trust identity",
				"open trust handler",
			},
		},
		{
			name: "mutation evidence failure",
			mutate: func(dependencies *runtimeDependencies, events *[]string) {
				dependencies.transitionTrustIdentity = func(string) error {
					*events = append(*events, "transition trust identity")
					return nil
				}
				dependencies.openTrustHandler = func() (closingTrustHandler, error) {
					*events = append(*events, "open trust handler")
					return &lifecycleTrustHandler{
						events:   events,
						evidence: helper.TrustMutationEvidence{},
					}, nil
				}
			},
			afterAdmission: []string{
				"transition trust identity",
				"open trust handler",
				"trust mutation",
				"close trust handler",
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			reference, redemption := trustLifecycleRedemption(t, now, helper.OperationEnsureTrust)
			events := make([]string, 0, 12)
			dependencies := successfulTestDependencies(&events, redemption)
			test.mutate(&dependencies, &events)
			dependencies.newLoopbackIdentityHandler = func() helper.LoopbackIdentityHandler {
				t.Fatal("loopback handler constructed after trust admission")
				return nil
			}
			dependencies.openResolverHandler = func() (closingResolverHandler, error) {
				t.Fatal("resolver handler opened after trust admission")
				return nil, nil
			}
			dependencies.openLowPortHandler = func() (closingLowPortHandler, error) {
				t.Fatal("low-port handler opened after trust admission")
				return nil, nil
			}
			_, err := runTrustLifecycle(t, now, reference, dependencies)
			wantEvents := append([]string{
				"authorize invocation",
				"open ticket redeemer",
				"open replay guard",
				"redeem ticket",
				"consume replay claim",
				"close replay guard",
				"close ticket redeemer",
			}, test.afterAdmission...)
			if err == nil || !slices.Equal(events, wantEvents) {
				t.Fatalf("error = %v; events = %#v, want %#v", err, events, wantEvents)
			}
		})
	}
}

// TestRunPanicsWhenAdmittedFactoryReturnsNil keeps invalid lazy wiring distinct from operational mutation failure.
func TestRunPanicsWhenAdmittedFactoryReturnsNil(t *testing.T) {
	now := time.Date(2026, time.July, 22, 12, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name      string
		operation helper.Operation
		configure func(*runtimeDependencies, *[]string)
		want      []string
	}{
		{
			name:      "trust",
			operation: helper.OperationEnsureTrust,
			configure: func(dependencies *runtimeDependencies, events *[]string) {
				dependencies.transitionTrustIdentity = func(string) error {
					*events = append(*events, "transition trust identity")
					return nil
				}
				dependencies.openTrustHandler = func() (closingTrustHandler, error) {
					*events = append(*events, "open trust handler")
					return nil, nil
				}
			},
			want: []string{
				"authorize invocation",
				"open ticket redeemer",
				"open replay guard",
				"redeem ticket",
				"consume replay claim",
				"close replay guard",
				"close ticket redeemer",
				"transition trust identity",
				"open trust handler",
			},
		},
		{
			name:      "resolver",
			operation: helper.OperationEnsureResolver,
			configure: func(dependencies *runtimeDependencies, events *[]string) {
				dependencies.openResolverHandler = func() (closingResolverHandler, error) {
					*events = append(*events, "open resolver handler")
					return nil, nil
				}
			},
			want: []string{
				"authorize invocation",
				"open ticket redeemer",
				"open replay guard",
				"redeem ticket",
				"consume replay claim",
				"open resolver handler",
				"close replay guard",
				"close ticket redeemer",
			},
		},
		{
			name:      "low ports",
			operation: helper.OperationEnsureLowPorts,
			configure: func(dependencies *runtimeDependencies, events *[]string) {
				dependencies.openLowPortHandler = func() (closingLowPortHandler, error) {
					*events = append(*events, "open low-port handler")
					return nil, nil
				}
			},
			want: []string{
				"authorize invocation",
				"open ticket redeemer",
				"open replay guard",
				"redeem ticket",
				"consume replay claim",
				"open low-port handler",
				"close replay guard",
				"close ticket redeemer",
			},
		},
		{
			name:      "loopback",
			operation: helper.OperationEnsureLoopbackIdentity,
			configure: func(dependencies *runtimeDependencies, events *[]string) {
				dependencies.newLoopbackIdentityHandler = func() helper.LoopbackIdentityHandler {
					*events = append(*events, "new loopback handler")
					return nil
				}
			},
			want: []string{
				"authorize invocation",
				"open ticket redeemer",
				"open replay guard",
				"redeem ticket",
				"consume replay claim",
				"new loopback handler",
				"close replay guard",
				"close ticket redeemer",
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			var reference helper.TicketReference
			var redemption helper.TicketRedemption
			switch test.operation {
			case helper.OperationEnsureLoopbackIdentity:
				reference, redemption = testRedemption(now)
			case helper.OperationEnsureTrust:
				reference, redemption = trustLifecycleRedemption(t, now, test.operation)
			default:
				reference, redemption = operationLifecycleRedemption(t, now, test.operation)
			}
			events := make([]string, 0, len(test.want))
			dependencies := successfulTestDependencies(&events, redemption)
			test.configure(&dependencies, &events)
			body, err := json.Marshal(helper.Request{
				Version:         helper.ProtocolVersion,
				TicketReference: reference,
			})
			if err != nil {
				t.Fatal(err)
			}
			var recovered any
			func() {
				defer func() {
					recovered = recover()
				}()
				_ = run(t.Context(), bytes.NewReader(body), &bytes.Buffer{}, fixedClock{now: now}, dependencies)
			}()
			if recovered == nil || !slices.Equal(events, test.want) {
				t.Fatalf("panic = %v; events = %#v, want %#v", recovered, events, test.want)
			}
		})
	}
}

// TestRunOpensSelectedResolverOrLowPortOnlyAfterReplayConsumption proves lazy family opener failures consume the admitted ticket without opening other handlers.
func TestRunOpensSelectedResolverOrLowPortOnlyAfterReplayConsumption(t *testing.T) {
	now := time.Date(2026, time.July, 22, 12, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name      string
		operation helper.Operation
		configure func(*runtimeDependencies, *[]string)
		opened    string
	}{
		{
			name:      "resolver",
			operation: helper.OperationEnsureResolver,
			opened:    "open resolver handler",
			configure: func(dependencies *runtimeDependencies, events *[]string) {
				dependencies.openResolverHandler = func() (closingResolverHandler, error) {
					*events = append(*events, "open resolver handler")
					return nil, errors.New("resolver open failed")
				}
			},
		},
		{
			name:      "low ports",
			operation: helper.OperationEnsureLowPorts,
			opened:    "open low-port handler",
			configure: func(dependencies *runtimeDependencies, events *[]string) {
				dependencies.openLowPortHandler = func() (closingLowPortHandler, error) {
					*events = append(*events, "open low-port handler")
					return nil, errors.New("low-port open failed")
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			reference, redemption := operationLifecycleRedemption(t, now, test.operation)
			events := make([]string, 0, 8)
			dependencies := successfulTestDependencies(&events, redemption)
			test.configure(&dependencies, &events)
			dependencies.openTrustHandler = func() (closingTrustHandler, error) {
				t.Fatal("trust handler opened for non-trust ticket")
				return nil, nil
			}
			dependencies.newLoopbackIdentityHandler = func() helper.LoopbackIdentityHandler {
				t.Fatal("loopback handler opened for non-loopback ticket")
				return nil
			}
			_, err := runTrustLifecycle(t, now, reference, dependencies)
			if err == nil || !slices.Contains(events, "consume replay claim") || !slices.Contains(events, test.opened) {
				t.Fatalf("error = %v; events = %#v", err, events)
			}
		})
	}
}

// lifecycleTrustHandler records exact admitted trust dispatch without native effects.
type lifecycleTrustHandler struct {
	events    *[]string
	evidence  helper.TrustMutationEvidence
	calls     int
	operation helper.Operation
}

// EnsureTrust records the admitted ensure operation.
func (handler *lifecycleTrustHandler) EnsureTrust(context.Context, helper.Ticket) (helper.TrustMutationEvidence, error) {
	*handler.events = append(*handler.events, "trust mutation")
	handler.calls++
	handler.operation = helper.OperationEnsureTrust
	return handler.evidence, nil
}

// ReleaseTrust records the admitted release operation.
func (handler *lifecycleTrustHandler) ReleaseTrust(context.Context, helper.Ticket) (helper.TrustMutationEvidence, error) {
	*handler.events = append(*handler.events, "trust mutation")
	handler.calls++
	handler.operation = helper.OperationReleaseTrust
	return handler.evidence, nil
}

// Close records disposal of the user-scoped trust authority.
func (handler *lifecycleTrustHandler) Close() error {
	*handler.events = append(*handler.events, "close trust handler")
	return nil
}

// runTrustLifecycle invokes one encoded trust ticket and decodes its response.
func runTrustLifecycle(t *testing.T, now time.Time, reference helper.TicketReference, dependencies runtimeDependencies) (helper.Response, error) {
	t.Helper()
	body, err := json.Marshal(helper.Request{
		Version:         helper.ProtocolVersion,
		TicketReference: reference,
	})
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	runErr := run(t.Context(), bytes.NewReader(body), &output, fixedClock{now: now}, dependencies)
	var response helper.Response
	if output.Len() != 0 {
		if err := json.Unmarshal(output.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
	}
	return response, runErr
}

// trustLifecycleRedemption creates one authenticated trust ticket and matching protected admission record.
func trustLifecycleRedemption(t *testing.T, now time.Time, operation helper.Operation) (helper.TicketReference, helper.TicketRedemption) {
	return trustLifecycleRedemptionForMechanisms(t, now, operation, networkpolicy.Mechanisms{
		Resolver: networkpolicy.DarwinResolverFile,
		LowPorts: networkpolicy.DarwinLaunchdRelay,
		Trust:    networkpolicy.DarwinCurrentUserTrust,
	})
}

// trustLifecycleRedemptionForMechanism creates one authenticated trust ticket with the requested tested scope.
func trustLifecycleRedemptionForMechanism(t *testing.T, now time.Time, operation helper.Operation, mechanism networkpolicy.TrustMechanism) (helper.TicketReference, helper.TicketRedemption) {
	mechanisms := networkpolicy.MacOSMechanisms()
	mechanisms.Trust = mechanism
	return trustLifecycleRedemptionForMechanisms(t, now, operation, mechanisms)
}

// trustLifecycleRedemptionForMechanisms creates one authenticated trust ticket with the requested tested platform profile.
func trustLifecycleRedemptionForMechanisms(t *testing.T, now time.Time, operation helper.Operation, mechanisms networkpolicy.Mechanisms) (helper.TicketReference, helper.TicketRedemption) {
	t.Helper()
	root := trustLifecycleRoot(t)
	loopback := netip.MustParseAddr("127.0.0.1")
	policy, err := networkpolicy.New(
		root.Fingerprint,
		mechanisms,
		networkpolicy.Listener{
			Advertised: netip.AddrPortFrom(loopback, 25000),
			Bind:       netip.AddrPortFrom(loopback, 25000),
		},
		networkpolicy.Listener{
			Advertised: netip.AddrPortFrom(loopback, 80),
			Bind:       netip.AddrPortFrom(loopback, 25001),
		},
		networkpolicy.Listener{
			Advertised: netip.AddrPortFrom(loopback, 443),
			Bind:       netip.AddrPortFrom(loopback, 25002),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	policyFingerprint, err := policy.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	reference := helper.TicketReference(strings.Repeat("a", 64))
	ticket := helper.Ticket{
		Version:                  helper.ProtocolVersion,
		Operation:                operation,
		InstallationID:           "trust-lifecycle-test",
		RequesterIdentity:        "501",
		OwnershipGeneration:      7,
		OwnershipSchemaVersion:   ownership.NetworkPolicySchemaVersion,
		NetworkPolicyFingerprint: policyFingerprint,
		NetworkPolicy:            &policy,
		ApprovedPool:             "127.77.0.0/24",
		TrustRoot:                &root,
		ExpectedTrustObservation: &helper.ExpectedTrustObservation{
			Fingerprint: strings.Repeat("b", 64),
		},
		Nonce:     strings.Repeat("c", 32),
		ExpiresAt: now.Add(time.Minute),
	}
	return reference, helper.TicketRedemption{
		Ticket: ticket,
		Admission: helper.TicketAdmission{
			TicketReference:            reference,
			RequesterIdentity:          ticket.RequesterIdentity,
			InstallationID:             ticket.InstallationID,
			OwnershipGeneration:        ticket.OwnershipGeneration,
			OwnershipSchemaVersion:     ticket.OwnershipSchemaVersion,
			NetworkPolicyFingerprint:   ticket.NetworkPolicyFingerprint,
			ApprovedPool:               ticket.ApprovedPool,
			OwnershipState:             helper.OwnershipAdmissionAlreadyCurrent,
			OwnershipFingerprint:       strings.Repeat("d", 64),
			TargetOwnershipFingerprint: strings.Repeat("d", 64),
			TicketVerifierKey:          "trust-lifecycle-verifier",
		},
	}
}

// operationLifecycleRedemption converts the valid trust fixture into another policy-bound operation family.
func operationLifecycleRedemption(t *testing.T, now time.Time, operation helper.Operation) (helper.TicketReference, helper.TicketRedemption) {
	t.Helper()
	reference, redemption := trustLifecycleRedemption(t, now, helper.OperationEnsureTrust)
	redemption.Ticket.Operation = operation
	redemption.Ticket.TrustRoot = nil
	redemption.Ticket.ExpectedTrustObservation = nil
	switch operation {
	case helper.OperationEnsureResolver, helper.OperationReleaseResolver:
		redemption.Ticket.ExpectedResolverObservation = &helper.ExpectedResolverObservation{
			Fingerprint: strings.Repeat("b", 64),
		}
	case helper.OperationEnsureLowPorts, helper.OperationReleaseLowPorts:
		redemption.Ticket.ExpectedLowPortObservation = &helper.ExpectedLowPortObservation{
			Fingerprint: strings.Repeat("b", 64),
		}
	default:
		t.Fatalf("unsupported operation %q", operation)
	}
	return reference, redemption
}

// trustLifecycleRoot derives a deterministic public root without retaining private material in the ticket.
func trustLifecycleRoot(t *testing.T) helper.TrustRoot {
	t.Helper()
	now := time.Date(2032, time.March, 4, 12, 0, 0, 0, time.UTC)
	authority, err := localca.New(localca.Config{
		CAValidity:   24 * time.Hour,
		LeafValidity: time.Hour,
		Backdate:     time.Minute,
		Now:          func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	material := authority.Material()
	return helper.TrustRoot{
		CertificatePEM: material.CertificatePEM,
		Fingerprint:    material.Fingerprint,
		NotBefore:      material.NotBefore,
		NotAfter:       material.NotAfter,
	}
}

// trustLifecycleEvidence returns the one exact postcondition accepted for a trust ticket.
func trustLifecycleEvidence(ticket helper.Ticket) helper.TrustMutationEvidence {
	postcondition := helper.TrustPostconditionExact
	if ticket.Operation == helper.OperationReleaseTrust {
		postcondition = helper.TrustPostconditionOwnedAbsent
	}
	return helper.TrustMutationEvidence{
		Changed:                true,
		AuthorityFingerprint:   ticket.TrustRoot.Fingerprint,
		Mechanism:              ticket.NetworkPolicy.Mechanisms.Trust,
		ObservationFingerprint: strings.Repeat("d", 64),
		Postcondition:          postcondition,
	}
}

// trustLifecycleContains reports whether ordered expected lifecycle events occur in the actual trace.
func trustLifecycleContains(actual []string, expected []string) bool {
	index := 0
	for _, event := range actual {
		if index < len(expected) && event == expected[index] {
			index++
		}
	}
	return index == len(expected)
}

var _ closingTrustHandler = (*lifecycleTrustHandler)(nil)
