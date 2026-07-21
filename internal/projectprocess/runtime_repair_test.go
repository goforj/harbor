package projectprocess

import (
	"context"
	"errors"
	"net/netip"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// runtimeRepairTestControl records policy effects while supplying deterministic native observations.
type runtimeRepairTestControl struct {
	inspection       runtimeRepairNativeInspection
	inspectErr       error
	gracefulSignaled bool
	gracefulErr      error
	forceSignaled    bool
	forceErr         error
	settled          bool
	settledErr       error
	afterInspect     func()
	afterSettled     func()
	inspectCalls     int
	gracefulCalls    int
	forceCalls       int
	settledCalls     int
}

// control exposes the fake through the same function boundary used by native implementations.
func (control *runtimeRepairTestControl) control() runtimeRepairControl {
	return runtimeRepairControl{
		inspect: func(_ context.Context, _ RuntimeRepairTarget) (runtimeRepairNativeInspection, error) {
			control.inspectCalls++
			if control.afterInspect != nil {
				control.afterInspect()
			}
			inspection := control.inspection
			if inspection.Observation != nil {
				observation := inspection.Observation.clone()
				inspection.Observation = &observation
			}
			return inspection, control.inspectErr
		},
		graceful: func(_ context.Context, _ runtimeRepairReceipt) (bool, error) {
			control.gracefulCalls++
			return control.gracefulSignaled, control.gracefulErr
		},
		force: func(_ context.Context, _ runtimeRepairReceipt) (bool, error) {
			control.forceCalls++
			return control.forceSignaled, control.forceErr
		},
		settled: func(_ context.Context, _ runtimeRepairReceipt) (bool, error) {
			control.settledCalls++
			if control.afterSettled != nil {
				control.afterSettled()
			}
			return control.settled, control.settledErr
		},
	}
}

// runtimeRepairTestObservation builds one complete dedicated session with a child-owned listener.
func runtimeRepairTestObservation(t *testing.T) runtimeRepairNativeObservation {
	t.Helper()
	checkout, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("EvalSymlinks() error = %v", err)
	}
	rootExecutable := filepath.Join(checkout, "forj")
	childExecutable := filepath.Join(checkout, "node")
	rootDigest, rootCount, rootExact, err := runtimeRepairArgumentEvidence([]string{rootExecutable, "dev"})
	if err != nil {
		t.Fatalf("runtimeRepairArgumentEvidence(root) error = %v", err)
	}
	childDigest, childCount, childExact, err := runtimeRepairArgumentEvidence([]string{childExecutable, "server.js"})
	if err != nil {
		t.Fatalf("runtimeRepairArgumentEvidence(child) error = %v", err)
	}
	root := runtimeRepairProcessFact{
		PID:                100,
		BirthToken:         "darwin:100:1",
		ParentPID:          50,
		ProcessGroupID:     100,
		SessionID:          100,
		EffectiveUID:       501,
		RealUID:            501,
		ExecutableIdentity: rootExecutable,
		ArgumentDigest:     rootDigest,
		ArgumentCount:      rootCount,
		CommandExact:       rootExact,
		WorkingDirectory:   checkout,
	}
	child := runtimeRepairProcessFact{
		PID:                101,
		BirthToken:         "darwin:101:1",
		ParentPID:          100,
		ProcessGroupID:     101,
		SessionID:          100,
		EffectiveUID:       501,
		RealUID:            501,
		ExecutableIdentity: childExecutable,
		ArgumentDigest:     childDigest,
		ArgumentCount:      childCount,
		CommandExact:       childExact,
		WorkingDirectory:   checkout,
	}
	endpoint := netip.MustParseAddrPort("127.0.0.42:38473")
	return runtimeRepairNativeObservation{
		Target:    RuntimeRepairTarget{CheckoutRoot: checkout, Endpoint: endpoint},
		DaemonUID: 501,
		Root:      root,
		RootParent: runtimeRepairParentFact{
			PID:        50,
			BirthToken: "darwin:50:1",
		},
		Members: []runtimeRepairProcessFact{root, child},
		Listener: runtimeRepairSocketFact{
			OwnerPID:        child.PID,
			OwnerBirthToken: child.BirthToken,
			FileDescriptor:  7,
			SocketHandle:    11,
			PCBHandle:       12,
			Generation:      13,
			Endpoint:        endpoint,
		},
	}
}

// runtimeRepairTestCandidate converts one valid native observation into its opaque public form.
func runtimeRepairTestCandidate(t *testing.T, observation runtimeRepairNativeObservation) RuntimeRepairCandidate {
	t.Helper()
	inspection, err := runtimeRepairInspection(runtimeRepairNativeInspection{
		State:       RuntimeRepairInspectionActionable,
		Observation: &observation,
	})
	if err != nil {
		t.Fatalf("runtimeRepairInspection() error = %v", err)
	}
	return inspection.Candidate.Clone()
}

// TestRuntimeRepairArgumentEvidenceHashesExactArgv verifies aliases remain valid while ambiguous concatenations do not collide.
func TestRuntimeRepairArgumentEvidenceHashesExactArgv(t *testing.T) {
	aliasDigest, aliasCount, aliasExact, err := runtimeRepairArgumentEvidence([]string{"/usr/local/bin/forj", "dev"})
	if err != nil {
		t.Fatalf("runtimeRepairArgumentEvidence(alias) error = %v", err)
	}
	if aliasCount != 2 || !aliasExact || len(aliasDigest) != 64 {
		t.Fatalf("alias evidence = %q, %d, %t", aliasDigest, aliasCount, aliasExact)
	}
	left, _, _, err := runtimeRepairArgumentEvidence([]string{"a", "bc"})
	if err != nil {
		t.Fatalf("runtimeRepairArgumentEvidence(left) error = %v", err)
	}
	right, _, _, err := runtimeRepairArgumentEvidence([]string{"ab", "c"})
	if err != nil {
		t.Fatalf("runtimeRepairArgumentEvidence(right) error = %v", err)
	}
	if left == right {
		t.Fatal("length-delimited argument digests collided")
	}
	wrongTail, _, exact, err := runtimeRepairArgumentEvidence([]string{"forj", "dev", "extra"})
	if err != nil || wrongTail == "" || exact {
		t.Fatalf("wrong-tail evidence = %q, %t, %v", wrongTail, exact, err)
	}
}

// TestRuntimeRepairArgumentEvidenceRejectsUnsafeBounds covers empty, NUL-bearing, and excessive argv inputs.
func TestRuntimeRepairArgumentEvidenceRejectsUnsafeBounds(t *testing.T) {
	tests := [][]string{
		nil,
		{""},
		{"forj\x00alias", "dev"},
		{strings.Repeat("x", runtimeRepairMaximumTextBytes+1)},
		make([]string, runtimeRepairMaximumArguments+1),
	}
	for index, arguments := range tests {
		if _, _, _, err := runtimeRepairArgumentEvidence(arguments); err == nil {
			t.Errorf("case %d accepted unsafe argv", index)
		}
	}
}

// TestRuntimeRepairArgumentEvidenceBindsExecutableIdentity rejects a basename-only argv spoof.
func TestRuntimeRepairArgumentEvidenceBindsExecutableIdentity(t *testing.T) {
	_, _, exact, err := runtimeRepairArgumentEvidenceForExecutable(
		"/opt/goforj/bin/forj",
		[]string{"/tmp/forj", "dev"},
	)
	if err != nil {
		t.Fatalf("runtimeRepairArgumentEvidenceForExecutable() error = %v", err)
	}
	if exact {
		t.Fatal("basename-only argv spoof was accepted as exact forj dev")
	}
	_, _, exact, err = runtimeRepairArgumentEvidenceForExecutable(
		"/opt/goforj/bin/forj",
		[]string{"/opt/goforj/bin/forj", "dev"},
	)
	if err != nil {
		t.Fatalf("runtimeRepairArgumentEvidenceForExecutable(valid) error = %v", err)
	}
	if !exact {
		t.Fatal("canonical executable argv was not accepted as exact forj dev")
	}
}

// TestRuntimeRepairObservationRequiresExactDedicatedRoot covers command, process-group, and checkout authority.
func TestRuntimeRepairObservationRequiresExactDedicatedRoot(t *testing.T) {
	observation := runtimeRepairTestObservation(t)
	if err := observation.validate(); err != nil {
		t.Fatalf("valid observation error = %v", err)
	}
	tests := []struct {
		name   string
		mutate func(*runtimeRepairNativeObservation)
	}{
		{name: "root process group", mutate: func(value *runtimeRepairNativeObservation) {
			value.Root.ProcessGroupID++
			value.Members[0].ProcessGroupID++
		}},
		{name: "root command marker", mutate: func(value *runtimeRepairNativeObservation) {
			value.Root.CommandExact = false
			value.Members[0].CommandExact = false
		}},
		{name: "root executable identity", mutate: func(value *runtimeRepairNativeObservation) {
			value.Root.ExecutableIdentity = "/opt/goforj/bin/not-forj"
			value.Members[0].ExecutableIdentity = value.Root.ExecutableIdentity
		}},
		{name: "root checkout", mutate: func(value *runtimeRepairNativeObservation) {
			value.Root.WorkingDirectory = filepath.Dir(value.Target.CheckoutRoot)
			value.Members[0].WorkingDirectory = value.Root.WorkingDirectory
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mutated := observation.clone()
			test.mutate(&mutated)
			if err := mutated.validate(); err == nil {
				t.Fatal("mutated observation passed validation")
			}
		})
	}
}

// TestRuntimeRepairObservationFingerprintCoversAuthorityFacts verifies revalidation cannot ignore any effect-bearing fact family.
func TestRuntimeRepairObservationFingerprintCoversAuthorityFacts(t *testing.T) {
	observation := runtimeRepairTestObservation(t)
	original, err := observation.fingerprint()
	if err != nil {
		t.Fatalf("fingerprint() error = %v", err)
	}
	alternateRootDigest, _, _, err := runtimeRepairArgumentEvidence([]string{"/different/path/forj", "dev"})
	if err != nil {
		t.Fatalf("runtimeRepairArgumentEvidence(alternate root) error = %v", err)
	}
	alternateChildDigest, alternateChildCount, alternateChildExact, err := runtimeRepairArgumentEvidence([]string{"/usr/bin/node", "other.js", "--watch"})
	if err != nil {
		t.Fatalf("runtimeRepairArgumentEvidence(alternate child) error = %v", err)
	}
	alternateCheckout, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("EvalSymlinks(alternate checkout) error = %v", err)
	}
	tests := []struct {
		name   string
		mutate func(*runtimeRepairNativeObservation)
	}{
		{name: "root birth", mutate: func(value *runtimeRepairNativeObservation) {
			value.Root.BirthToken = "darwin:100:2"
			value.Members[0].BirthToken = value.Root.BirthToken
		}},
		{name: "root executable", mutate: func(value *runtimeRepairNativeObservation) {
			value.Root.ExecutableIdentity = filepath.Join(value.Target.CheckoutRoot, "alternate", "forj")
			value.Members[0].ExecutableIdentity = value.Root.ExecutableIdentity
		}},
		{name: "root argv", mutate: func(value *runtimeRepairNativeObservation) {
			value.Root.ArgumentDigest = alternateRootDigest
			value.Members[0].ArgumentDigest = alternateRootDigest
		}},
		{name: "checkout and cwd", mutate: func(value *runtimeRepairNativeObservation) {
			value.Target.CheckoutRoot = alternateCheckout
			value.Root.WorkingDirectory = alternateCheckout
			value.Members[0].WorkingDirectory = alternateCheckout
		}},
		{name: "root parent", mutate: func(value *runtimeRepairNativeObservation) {
			value.Root.ParentPID = 51
			value.Members[0].ParentPID = 51
			value.RootParent.PID = 51
			value.RootParent.BirthToken = "darwin:51:1"
		}},
		{name: "session identity", mutate: func(value *runtimeRepairNativeObservation) {
			value.Root.PID = 200
			value.Root.ProcessGroupID = 200
			value.Root.SessionID = 200
			value.Members[0] = value.Root
			value.Members[1].PID = 201
			value.Members[1].ParentPID = 200
			value.Members[1].SessionID = 200
			value.Listener.OwnerPID = 201
		}},
		{name: "member executable argv and group", mutate: func(value *runtimeRepairNativeObservation) {
			value.Members[1].ProcessGroupID = 102
			value.Members[1].ExecutableIdentity = filepath.Join(value.Target.CheckoutRoot, "runtime-node")
			value.Members[1].ArgumentDigest = alternateChildDigest
			value.Members[1].ArgumentCount = alternateChildCount
			value.Members[1].CommandExact = alternateChildExact
		}},
		{name: "daemon uid", mutate: func(value *runtimeRepairNativeObservation) {
			value.DaemonUID = 502
			value.Root.EffectiveUID = 502
			value.Root.RealUID = 502
			value.Members[0].EffectiveUID = 502
			value.Members[0].RealUID = 502
			value.Members[1].EffectiveUID = 502
			value.Members[1].RealUID = 502
		}},
		{name: "listener descriptor and identity", mutate: func(value *runtimeRepairNativeObservation) {
			value.Listener.FileDescriptor = 8
			value.Listener.SocketHandle = 21
			value.Listener.PCBHandle = 22
			value.Listener.Generation = 23
		}},
		{name: "endpoint", mutate: func(value *runtimeRepairNativeObservation) {
			endpoint := netip.MustParseAddrPort("127.0.0.43:38474")
			value.Target.Endpoint = endpoint
			value.Listener.Endpoint = endpoint
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mutated := observation.clone()
			test.mutate(&mutated)
			fingerprint, err := mutated.fingerprint()
			if err != nil {
				t.Fatalf("mutated fingerprint() error = %v", err)
			}
			if fingerprint == original || runtimeRepairObservationsEqual(observation, mutated) {
				t.Fatalf("mutation did not change exact revalidation: %s", fingerprint)
			}
		})
	}
}

// TestRuntimeRepairInspectionMapsFixedStates verifies native detail cannot expand the client-facing vocabulary.
func TestRuntimeRepairInspectionMapsFixedStates(t *testing.T) {
	tests := []struct {
		state      RuntimeRepairInspectionState
		diagnostic RuntimeRepairDiagnostic
	}{
		{RuntimeRepairInspectionMissing, RuntimeRepairDiagnosticListenerMissing},
		{RuntimeRepairInspectionAmbiguous, RuntimeRepairDiagnosticCandidateAmbiguous},
		{RuntimeRepairInspectionForeign, RuntimeRepairDiagnosticForeignOwner},
		{RuntimeRepairInspectionUnreadable, RuntimeRepairDiagnosticNativeUnreadable},
		{RuntimeRepairInspectionUnsupported, RuntimeRepairDiagnosticPlatformUnsupported},
	}
	for _, test := range tests {
		inspection, err := runtimeRepairInspection(runtimeRepairNativeInspection{State: test.state})
		if err != nil {
			t.Fatalf("runtimeRepairInspection(%q) error = %v", test.state, err)
		}
		if inspection.Diagnostic != test.diagnostic || inspection.Candidate != nil {
			t.Fatalf("runtimeRepairInspection(%q) = %#v", test.state, inspection)
		}
		if err := inspection.Validate(); err != nil {
			t.Fatalf("inspection.Validate(%q) error = %v", test.state, err)
		}
	}
}

// TestRuntimeRepairListenerCardinalityPrecedesOwnership verifies global ambiguity cannot be mislabeled by a zero-owner scan.
func TestRuntimeRepairListenerCardinalityPrecedesOwnership(t *testing.T) {
	tests := []struct {
		name        string
		exact       int
		conflicting int
		want        RuntimeRepairInspectionState
	}{
		{name: "absent", want: RuntimeRepairInspectionMissing},
		{name: "one exact", exact: 1, want: RuntimeRepairInspectionActionable},
		{name: "wildcard only", conflicting: 1, want: RuntimeRepairInspectionAmbiguous},
		{name: "exact plus wildcard", exact: 1, conflicting: 1, want: RuntimeRepairInspectionAmbiguous},
		{name: "multiple exact", exact: 2, want: RuntimeRepairInspectionAmbiguous},
		{name: "negative", exact: -1, want: RuntimeRepairInspectionUnreadable},
		{name: "unbounded", exact: runtimeRepairMaximumProcesses + 1, want: RuntimeRepairInspectionUnreadable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := runtimeRepairListenerCardinality(test.exact, test.conflicting); got != test.want {
				t.Fatalf("runtimeRepairListenerCardinality(%d, %d) = %q, want %q", test.exact, test.conflicting, got, test.want)
			}
		})
	}
}

// TestRuntimeRepairCandidateKeepsOpaqueReceiptIsolated verifies public and private mutations cannot alias a clone.
func TestRuntimeRepairCandidateKeepsOpaqueReceiptIsolated(t *testing.T) {
	candidate := runtimeRepairTestCandidate(t, runtimeRepairTestObservation(t))
	clone := candidate.Clone()
	clone.Display.Command = "changed"
	clone.receipt.observation.Members[0].BirthToken = "darwin:changed:1"
	if err := candidate.Validate(); err != nil {
		t.Fatalf("original candidate changed through clone: %v", err)
	}
	forged := candidate
	forged.receipt = nil
	if err := forged.Validate(); err == nil {
		t.Fatal("candidate without process-local receipt passed validation")
	}
	forged = candidate.Clone()
	forged.Fingerprint = strings.ToUpper(forged.Fingerprint)
	if err := forged.Validate(); err == nil {
		t.Fatal("candidate with noncanonical fingerprint passed validation")
	}
}

// TestRuntimeRepairConfirmSettlesAfterOneRootSignal verifies the successful policy path and confirmation invariant.
func TestRuntimeRepairConfirmSettlesAfterOneRootSignal(t *testing.T) {
	observation := runtimeRepairTestObservation(t)
	candidate := runtimeRepairTestCandidate(t, observation)
	control := &runtimeRepairTestControl{
		inspection:       runtimeRepairNativeInspection{State: RuntimeRepairInspectionActionable, Observation: &observation},
		gracefulSignaled: true,
		settled:          true,
	}
	repairer := newRuntimeRepairerWithControl(control.control(), time.Second, time.Millisecond)
	confirmation, err := repairer.Confirm(context.Background(), candidate)
	if err != nil {
		t.Fatalf("Confirm() error = %v", err)
	}
	if err := confirmation.Validate(); err != nil {
		t.Fatalf("confirmation.Validate() error = %v", err)
	}
	if confirmation.State != RuntimeRepairConfirmationSettled || !confirmation.Signaled {
		t.Fatalf("Confirm() = %#v", confirmation)
	}
	if control.inspectCalls != 1 || control.gracefulCalls != 1 || control.settledCalls != 1 {
		t.Fatalf("calls = inspect %d, graceful %d, settled %d", control.inspectCalls, control.gracefulCalls, control.settledCalls)
	}
}

// TestRuntimeRepairConfirmDriftEmitsNoSignal verifies any structurally valid birth drift stops before the effect boundary.
func TestRuntimeRepairConfirmDriftEmitsNoSignal(t *testing.T) {
	observation := runtimeRepairTestObservation(t)
	candidate := runtimeRepairTestCandidate(t, observation)
	current := observation.clone()
	current.Root.BirthToken = "darwin:100:2"
	current.Members[0].BirthToken = current.Root.BirthToken
	control := &runtimeRepairTestControl{
		inspection:       runtimeRepairNativeInspection{State: RuntimeRepairInspectionActionable, Observation: &current},
		gracefulSignaled: true,
	}
	repairer := newRuntimeRepairerWithControl(control.control(), time.Second, time.Millisecond)
	confirmation, err := repairer.Confirm(context.Background(), candidate)
	if err != nil {
		t.Fatalf("Confirm() error = %v", err)
	}
	if err := confirmation.Validate(); err != nil {
		t.Fatalf("confirmation.Validate() error = %v", err)
	}
	if confirmation.State != RuntimeRepairConfirmationDrifted || confirmation.Signaled || control.gracefulCalls != 0 {
		t.Fatalf("Confirm() = %#v, graceful calls = %d", confirmation, control.gracefulCalls)
	}
}

// TestRuntimeRepairConfirmRejectsEveryNonActionableReobservation proves fixed native classifications all remain zero-signal.
func TestRuntimeRepairConfirmRejectsEveryNonActionableReobservation(t *testing.T) {
	states := []RuntimeRepairInspectionState{
		RuntimeRepairInspectionMissing,
		RuntimeRepairInspectionAmbiguous,
		RuntimeRepairInspectionForeign,
		RuntimeRepairInspectionUnreadable,
		RuntimeRepairInspectionUnsupported,
	}
	for _, state := range states {
		t.Run(string(state), func(t *testing.T) {
			candidate := runtimeRepairTestCandidate(t, runtimeRepairTestObservation(t))
			control := &runtimeRepairTestControl{
				inspection:       runtimeRepairNativeInspection{State: state},
				gracefulSignaled: true,
			}
			repairer := newRuntimeRepairerWithControl(control.control(), time.Second, time.Millisecond)
			confirmation, err := repairer.Confirm(context.Background(), candidate)
			if err != nil {
				t.Fatalf("Confirm() error = %v", err)
			}
			if validationErr := confirmation.Validate(); validationErr != nil {
				t.Fatalf("confirmation.Validate() error = %v", validationErr)
			}
			if confirmation.State != RuntimeRepairConfirmationDrifted || confirmation.Signaled || control.gracefulCalls != 0 {
				t.Fatalf("Confirm() = %#v, graceful calls = %d", confirmation, control.gracefulCalls)
			}
		})
	}
}

// TestRuntimeRepairConfirmCancellationAfterReobservationEmitsNoSignal verifies context drift closes the final portable effect gate.
func TestRuntimeRepairConfirmCancellationAfterReobservationEmitsNoSignal(t *testing.T) {
	observation := runtimeRepairTestObservation(t)
	candidate := runtimeRepairTestCandidate(t, observation)
	ctx, cancel := context.WithCancel(context.Background())
	control := &runtimeRepairTestControl{
		inspection: runtimeRepairNativeInspection{State: RuntimeRepairInspectionActionable, Observation: &observation},
		afterInspect: func() {
			cancel()
		},
		gracefulSignaled: true,
	}
	repairer := newRuntimeRepairerWithControl(control.control(), time.Second, time.Millisecond)
	confirmation, err := repairer.Confirm(ctx, candidate)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Confirm() error = %v", err)
	}
	if validationErr := confirmation.Validate(); validationErr != nil {
		t.Fatalf("confirmation.Validate() error = %v", validationErr)
	}
	if confirmation.State != RuntimeRepairConfirmationFailed || confirmation.Signaled || control.gracefulCalls != 0 {
		t.Fatalf("Confirm() = %#v, graceful calls = %d", confirmation, control.gracefulCalls)
	}
}

// TestRuntimeRepairConfirmPreservesPreSignalBackendFailure verifies an ordinary graceful error cannot imply an effect.
func TestRuntimeRepairConfirmPreservesPreSignalBackendFailure(t *testing.T) {
	observation := runtimeRepairTestObservation(t)
	candidate := runtimeRepairTestCandidate(t, observation)
	wantErr := errors.New("test graceful failure")
	control := &runtimeRepairTestControl{
		inspection:  runtimeRepairNativeInspection{State: RuntimeRepairInspectionActionable, Observation: &observation},
		gracefulErr: wantErr,
	}
	repairer := newRuntimeRepairerWithControl(control.control(), time.Second, time.Millisecond)
	confirmation, err := repairer.Confirm(context.Background(), candidate)
	if !errors.Is(err, wantErr) {
		t.Fatalf("Confirm() error = %v", err)
	}
	if validationErr := confirmation.Validate(); validationErr != nil {
		t.Fatalf("confirmation.Validate() error = %v", validationErr)
	}
	if confirmation.State != RuntimeRepairConfirmationFailed || confirmation.Signaled || control.settledCalls != 0 {
		t.Fatalf("Confirm() = %#v, settlement calls = %d", confirmation, control.settledCalls)
	}
}

// TestRuntimeRepairConfirmAcceptsZeroSignalBackendDrift verifies the native final gate can safely request fresh inspection.
func TestRuntimeRepairConfirmAcceptsZeroSignalBackendDrift(t *testing.T) {
	observation := runtimeRepairTestObservation(t)
	candidate := runtimeRepairTestCandidate(t, observation)
	control := &runtimeRepairTestControl{
		inspection:  runtimeRepairNativeInspection{State: RuntimeRepairInspectionActionable, Observation: &observation},
		gracefulErr: ErrRuntimeRepairDrift,
	}
	repairer := newRuntimeRepairerWithControl(control.control(), time.Second, time.Millisecond)
	confirmation, err := repairer.Confirm(context.Background(), candidate)
	if err != nil {
		t.Fatalf("Confirm() error = %v", err)
	}
	if validationErr := confirmation.Validate(); validationErr != nil {
		t.Fatalf("confirmation.Validate() error = %v", validationErr)
	}
	if confirmation.State != RuntimeRepairConfirmationDrifted || confirmation.Signaled || control.gracefulCalls != 1 || control.settledCalls != 0 {
		t.Fatalf("Confirm() = %#v, graceful calls = %d, settlement calls = %d", confirmation, control.gracefulCalls, control.settledCalls)
	}
}

// TestRuntimeRepairConfirmRejectsPostSignalDriftPairing ensures a backend cannot erase a delivered signal from the result.
func TestRuntimeRepairConfirmRejectsPostSignalDriftPairing(t *testing.T) {
	observation := runtimeRepairTestObservation(t)
	candidate := runtimeRepairTestCandidate(t, observation)
	control := &runtimeRepairTestControl{
		inspection:       runtimeRepairNativeInspection{State: RuntimeRepairInspectionActionable, Observation: &observation},
		gracefulSignaled: true,
		gracefulErr:      ErrRuntimeRepairDrift,
	}
	repairer := newRuntimeRepairerWithControl(control.control(), time.Second, time.Millisecond)
	confirmation, err := repairer.Confirm(context.Background(), candidate)
	if err == nil || !errors.Is(err, ErrRuntimeRepairDrift) {
		t.Fatalf("Confirm() error = %v", err)
	}
	if validationErr := confirmation.Validate(); validationErr != nil {
		t.Fatalf("confirmation.Validate() error = %v", validationErr)
	}
	if confirmation.State != RuntimeRepairConfirmationFailed || !confirmation.Signaled {
		t.Fatalf("Confirm() = %#v", confirmation)
	}
}

// TestRuntimeRepairConfirmReportsPostSignalNonconvergence verifies graceful timeout never escalates or loses signal state.
func TestRuntimeRepairConfirmReportsPostSignalNonconvergence(t *testing.T) {
	observation := runtimeRepairTestObservation(t)
	candidate := runtimeRepairTestCandidate(t, observation)
	control := &runtimeRepairTestControl{
		inspection:       runtimeRepairNativeInspection{State: RuntimeRepairInspectionActionable, Observation: &observation},
		gracefulSignaled: true,
		settled:          false,
	}
	repairer := newRuntimeRepairerWithControl(control.control(), 3*time.Millisecond, time.Millisecond)
	confirmation, err := repairer.Confirm(context.Background(), candidate)
	if !errors.Is(err, ErrRuntimeRepairNotSettled) {
		t.Fatalf("Confirm() error = %v", err)
	}
	if validationErr := confirmation.Validate(); validationErr != nil {
		t.Fatalf("confirmation.Validate() error = %v", validationErr)
	}
	if confirmation.State != RuntimeRepairConfirmationFailed || !confirmation.Signaled || control.gracefulCalls != 1 || control.forceCalls != 1 {
		t.Fatalf("Confirm() = %#v, graceful calls = %d, force calls = %d", confirmation, control.gracefulCalls, control.forceCalls)
	}
}

// TestRuntimeRepairConfirmEscalatesAfterGracefulNonconvergence proves a confirmed exact scope gets one bounded forceful pass.
func TestRuntimeRepairConfirmEscalatesAfterGracefulNonconvergence(t *testing.T) {
	observation := runtimeRepairTestObservation(t)
	candidate := runtimeRepairTestCandidate(t, observation)
	control := &runtimeRepairTestControl{
		inspection:       runtimeRepairNativeInspection{State: RuntimeRepairInspectionActionable, Observation: &observation},
		gracefulSignaled: true,
		forceSignaled:    true,
	}
	control.forceErr = nil
	control.afterSettled = func() {
		if control.forceCalls > 0 {
			control.settled = true
		}
	}
	repairer := newRuntimeRepairerWithControl(control.control(), 3*time.Millisecond, time.Millisecond)
	confirmation, err := repairer.Confirm(context.Background(), candidate)
	if err != nil {
		t.Fatalf("Confirm() error = %v", err)
	}
	if confirmation.State != RuntimeRepairConfirmationSettled || !confirmation.Signaled {
		t.Fatalf("Confirm() = %#v, want settled confirmation", confirmation)
	}
	if control.gracefulCalls != 1 || control.forceCalls != 1 {
		t.Fatalf("signals = graceful %d, force %d; want one each", control.gracefulCalls, control.forceCalls)
	}
}

// TestRuntimeRepairConfirmPreservesPostSignalSettlementFailures covers error and cancellation after the one graceful effect.
func TestRuntimeRepairConfirmPreservesPostSignalSettlementFailures(t *testing.T) {
	tests := []struct {
		name             string
		settlementErr    error
		cancelSettlement bool
		wantErr          error
	}{
		{name: "error", settlementErr: errors.New("test settlement failure")},
		{name: "cancellation", cancelSettlement: true, wantErr: context.Canceled},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			observation := runtimeRepairTestObservation(t)
			candidate := runtimeRepairTestCandidate(t, observation)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			control := &runtimeRepairTestControl{
				inspection:       runtimeRepairNativeInspection{State: RuntimeRepairInspectionActionable, Observation: &observation},
				gracefulSignaled: true,
				settledErr:       test.settlementErr,
			}
			if test.cancelSettlement {
				control.afterSettled = cancel
			}
			repairer := newRuntimeRepairerWithControl(control.control(), time.Second, time.Millisecond)
			confirmation, err := repairer.Confirm(ctx, candidate)
			wantErr := test.wantErr
			if wantErr == nil {
				wantErr = test.settlementErr
			}
			if !errors.Is(err, wantErr) {
				t.Fatalf("Confirm() error = %v, want %v", err, wantErr)
			}
			if validationErr := confirmation.Validate(); validationErr != nil {
				t.Fatalf("confirmation.Validate() error = %v", validationErr)
			}
			if confirmation.State != RuntimeRepairConfirmationFailed || !confirmation.Signaled || control.gracefulCalls != 1 {
				t.Fatalf("Confirm() = %#v, graceful calls = %d", confirmation, control.gracefulCalls)
			}
		})
	}
}
