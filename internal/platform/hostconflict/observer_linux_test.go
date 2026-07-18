//go:build linux

package hostconflict

import (
	"context"
	"encoding/binary"
	"errors"
	"net"
	"net/netip"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// linuxExchangeCall records one native request without exposing mutable slices to a test.
type linuxExchangeCall struct {
	messageType uint16
	flags       uint16
	payload     []byte
	multipart   bool
}

// scriptedLinuxNetlink returns fixture replies in request order.
type scriptedLinuxNetlink struct {
	replies []linuxNetlinkReply
	errors  []error
	calls   []linuxExchangeCall
}

// linuxTestDatagramResult supplies one transaction-state fixture.
type linuxTestDatagramResult struct {
	payload   []byte
	address   *unix.SockaddrNetlink
	oversized bool
	err       error
}

// linuxTestProcFixture models a verified procfs tree with injectable operation failures.
type linuxTestProcFixture struct {
	nextDescriptor int
	paths          map[int]string
	offsets        map[int]int
	contents       map[string]string
	failAt         string
	wrongFSAt      string
	nonregularAt   string
	interruptAt    string
	interrupted    bool
	cancelAt       string
	cancel         context.CancelFunc
	closed         int
}

// linuxTestObservationSession records closure while satisfying the pass-level netlink contract.
type linuxTestObservationSession struct {
	closed   int
	closeErr error
}

// exchange fails because pass-orchestration fixtures replace every native fact collector.
func (session *linuxTestObservationSession) exchange(context.Context, uint16, uint16, []byte, bool) (linuxNetlinkReply, error) {
	return linuxNetlinkReply{}, errors.New("unexpected pass fixture exchange")
}

// close records that a successful or poisoned pass session cannot be reused.
func (session *linuxTestObservationSession) close() error {
	session.closed++
	return session.closeErr
}

// linuxTestPassFixture drives each observer orchestration boundary independently.
type linuxTestPassFixture struct {
	failAt         string
	namespaceCalls int
	sessions       []*linuxTestObservationSession
	observation    Observation
	interfaces     linuxInterfaceSnapshot
	error          error
}

// exchange records a defensive payload copy before consuming its scripted result.
func (script *scriptedLinuxNetlink) exchange(_ context.Context, messageType uint16, flags uint16, payload []byte, multipart bool) (linuxNetlinkReply, error) {
	script.calls = append(script.calls, linuxExchangeCall{messageType: messageType, flags: flags, payload: append([]byte(nil), payload...), multipart: multipart})
	index := len(script.calls) - 1
	if index < len(script.errors) && script.errors[index] != nil {
		return linuxNetlinkReply{}, script.errors[index]
	}
	if index >= len(script.replies) {
		return linuxNetlinkReply{}, errors.New("unexpected fixture exchange")
	}
	return script.replies[index], nil
}

// linuxTestDatagramSource consumes a bounded fixture queue and fails if the transaction over-reads it.
func linuxTestDatagramSource(t *testing.T, results []linuxTestDatagramResult, calls *int) linuxNetlinkDatagramSource {
	t.Helper()
	return func(context.Context, int) ([]byte, *unix.SockaddrNetlink, bool, error) {
		if *calls >= len(results) {
			t.Fatal("receiveLinuxNetlinkWith() requested an unexpected datagram")
		}
		result := results[*calls]
		*calls = *calls + 1
		return result.payload, result.address, result.oversized, result.err
	}
}

// linuxTestProcOperations returns deterministic syscall seams for the fixture tree.
func linuxTestProcOperations(fixture *linuxTestProcFixture) linuxProcOperations {
	if fixture.paths == nil {
		fixture.paths = make(map[int]string)
	}
	if fixture.offsets == nil {
		fixture.offsets = make(map[int]int)
	}
	if fixture.contents == nil {
		fixture.contents = make(map[string]string)
	}
	fixture.nextDescriptor = 10
	return linuxProcOperations{
		open:     fixture.open,
		openAt:   fixture.openAt,
		close:    fixture.close,
		fileStat: fixture.fileStat,
		fsStat:   fixture.fsStat,
		read:     fixture.read,
	}
}

// open allocates the procfs root descriptor.
func (fixture *linuxTestProcFixture) open(path string, _ int, _ uint32) (int, error) {
	return fixture.openPath("open:"+path, path)
}

// openAt resolves one component below an already verified fixture descriptor.
func (fixture *linuxTestProcFixture) openAt(parent int, name string, _ int, _ uint32) (int, error) {
	return fixture.openPath("open:"+fixture.paths[parent]+"/"+name, fixture.paths[parent]+"/"+name)
}

// openPath applies failure and cancellation hooks before allocating a descriptor.
func (fixture *linuxTestProcFixture) openPath(operation string, path string) (int, error) {
	fixture.trigger(operation)
	if fixture.failAt == operation {
		return -1, errors.New("proc fixture failure")
	}
	fixture.nextDescriptor++
	descriptor := fixture.nextDescriptor
	fixture.paths[descriptor] = path
	return descriptor, nil
}

// close records descriptor cleanup without erasing evidence needed by assertions.
func (fixture *linuxTestProcFixture) close(int) error {
	fixture.closed++
	return nil
}

// fsStat reports procfs unless the fixture selects an error or foreign mount.
func (fixture *linuxTestProcFixture) fsStat(descriptor int, status *unix.Statfs_t) error {
	operation := "fs:" + fixture.paths[descriptor]
	fixture.trigger(operation)
	if fixture.failAt == operation {
		return errors.New("proc fixture failure")
	}
	if fixture.wrongFSAt == operation {
		status.Type = 0
		return nil
	}
	status.Type = unix.PROC_SUPER_MAGIC
	return nil
}

// fileStat reports regular sysctl files unless the fixture selects another type or an error.
func (fixture *linuxTestProcFixture) fileStat(descriptor int, status *unix.Stat_t) error {
	operation := "stat:" + fixture.paths[descriptor]
	fixture.trigger(operation)
	if fixture.failAt == operation {
		return errors.New("proc fixture failure")
	}
	if fixture.nonregularAt == operation {
		status.Mode = unix.S_IFDIR
	} else {
		status.Mode = unix.S_IFREG
	}
	return nil
}

// read returns bounded fixture content and can inject one EINTR before progress.
func (fixture *linuxTestProcFixture) read(descriptor int, destination []byte) (int, error) {
	path := fixture.paths[descriptor]
	operation := "read:" + path
	fixture.trigger(operation)
	if fixture.failAt == operation {
		return 0, errors.New("proc fixture failure")
	}
	if fixture.interruptAt == operation && !fixture.interrupted {
		fixture.interrupted = true
		return 0, unix.EINTR
	}
	content := fixture.contents[path]
	if content == "" {
		content = "0\n"
	}
	offset := fixture.offsets[descriptor]
	if offset >= len(content) {
		return 0, nil
	}
	read := copy(destination, content[offset:])
	fixture.offsets[descriptor] += read
	return read, nil
}

// trigger cancels the fixture context immediately after the selected bounded operation.
func (fixture *linuxTestProcFixture) trigger(operation string) {
	if fixture.cancelAt == operation && fixture.cancel != nil {
		fixture.cancel()
	}
}

// linuxTestPassOperations converts one safe reference observation into injectable pass operations.
func linuxTestPassOperations(fixture *linuxTestPassFixture) linuxPassOperations {
	return linuxPassOperations{
		namespace: func() (NetworkScope, error) {
			fixture.namespaceCalls++
			if fixture.failAt == "namespace-before" && fixture.namespaceCalls == 1 || fixture.failAt == "namespace-after" && fixture.namespaceCalls == 2 {
				return NetworkScope{}, fixture.error
			}
			if fixture.failAt == "namespace-change" && fixture.namespaceCalls == 2 {
				return NewMacOSScope(), nil
			}
			return fixture.observation.Scope, nil
		},
		open: func(protocol int) (linuxObservationSession, error) {
			if fixture.failAt == "route-open" && protocol == unix.NETLINK_ROUTE || fixture.failAt == "socket-open" && protocol == unix.NETLINK_SOCK_DIAG {
				return nil, fixture.error
			}
			session := &linuxTestObservationSession{}
			if fixture.failAt == "route-close" && protocol == unix.NETLINK_ROUTE || fixture.failAt == "socket-close" && protocol == unix.NETLINK_SOCK_DIAG {
				session.closeErr = fixture.error
			}
			fixture.sessions = append(fixture.sessions, session)
			return session, nil
		},
		interfaces: func(context.Context, linuxNetlinkExchanger) (linuxInterfaceSnapshot, error) {
			if fixture.failAt == "interfaces" {
				return linuxInterfaceSnapshot{}, fixture.error
			}
			return fixture.interfaces, nil
		},
		routes: func(context.Context, linuxNetlinkExchanger, Request, uint32, linuxInterfaceSnapshot) (RouteSnapshot, error) {
			if fixture.failAt == "routes" {
				return RouteSnapshot{}, fixture.error
			}
			if fixture.failAt == "invalid" {
				return RouteSnapshot{Complete: true}, nil
			}
			return fixture.observation.Routes, nil
		},
		sockets: func(context.Context, linuxNetlinkExchanger, Request) (SocketSnapshot, error) {
			if fixture.failAt == "sockets" {
				return SocketSnapshot{}, fixture.error
			}
			return fixture.observation.Sockets, nil
		},
		policy: func(context.Context, linuxInterfaceSnapshot) (*LinuxPolicyFacts, error) {
			if fixture.failAt == "policy" {
				return nil, fixture.error
			}
			return fixture.observation.Policy.Linux, nil
		},
	}
}

// TestObserveStableLinuxRequiresConsecutivePasses proves retry bounds and A-B-A instability handling.
func TestObserveStableLinuxRequiresConsecutivePasses(t *testing.T) {
	reference := safeLinuxObservation(t)
	changed := safeLinuxObservation(t)
	changed.Policy.Linux.IPNonlocalBind = true

	tests := []struct {
		name      string
		sequence  []Observation
		wantCalls int
		wantError string
	}{
		{name: "first pair", sequence: []Observation{reference, reference}, wantCalls: 2},
		{name: "later pair", sequence: []Observation{reference, changed, changed}, wantCalls: 3},
		{name: "alternating", sequence: []Observation{reference, changed, reference, changed}, wantCalls: 4, wantError: "did not stabilize"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			observe := func(context.Context, Request, uint32) (Observation, error) {
				observation := test.sequence[calls]
				calls++
				return observation, nil
			}
			_, err := observeStableLinux(context.Background(), reference.Request, 1000, observe)
			if test.wantError == "" && err != nil {
				t.Fatalf("observeStableLinux() error = %v", err)
			}
			if test.wantError != "" && (err == nil || !strings.Contains(err.Error(), test.wantError)) {
				t.Fatalf("observeStableLinux() error = %v, want containing %q", err, test.wantError)
			}
			if calls != test.wantCalls {
				t.Fatalf("observeStableLinux() calls = %d, want %d", calls, test.wantCalls)
			}
		})
	}
}

// TestObserveStableLinuxPropagatesCancellationAndPassErrors covers failures before stable evidence exists.
func TestObserveStableLinuxPropagatesCancellationAndPassErrors(t *testing.T) {
	reference := safeLinuxObservation(t)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := observeStableLinux(canceled, reference.Request, 1000, func(context.Context, Request, uint32) (Observation, error) {
		t.Fatal("canceled observation invoked pass")
		return Observation{}, nil
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("observeStableLinux(canceled) error = %v", err)
	}
	sentinel := errors.New("fixture failure")
	if _, err := observeStableLinux(context.Background(), reference.Request, 1000, func(context.Context, Request, uint32) (Observation, error) {
		return Observation{}, sentinel
	}); !errors.Is(err, sentinel) {
		t.Fatalf("observeStableLinux(failure) error = %v", err)
	}
	calls := 0
	observation, err := observeStableLinux(context.Background(), reference.Request, 1000, func(context.Context, Request, uint32) (Observation, error) {
		calls++
		if calls == 1 {
			return Observation{}, errLinuxNetlinkInterrupted
		}
		return reference, nil
	})
	if err != nil || calls != 3 || observation.Scope.Platform != PlatformLinux {
		t.Fatalf("observeStableLinux(interrupted) = %#v, calls %d, error %v", observation, calls, err)
	}
	if _, err := observeStableLinux(context.Background(), reference.Request, 1000, func(context.Context, Request, uint32) (Observation, error) {
		return Observation{}, nil
	}); err == nil || !strings.Contains(err.Error(), "invalid native facts") {
		t.Fatalf("observeStableLinux(invalid facts) error = %v", err)
	}
	if _, err := observeStableLinux(context.Background(), reference.Request, 1000, func(context.Context, Request, uint32) (Observation, error) {
		return Observation{}, errLinuxNetlinkInterrupted
	}); err == nil || !strings.Contains(err.Error(), "could not complete") {
		t.Fatalf("observeStableLinux(interrupted exhaustion) error = %v", err)
	}
}

// TestObserveLinuxPassWithClosesEverySessionOnSuccessAndFailure covers pass orchestration boundaries.
func TestObserveLinuxPassWithClosesEverySessionOnSuccessAndFailure(t *testing.T) {
	reference := safeLinuxObservation(t)
	sentinel := errors.New("pass fixture failure")
	for _, test := range []struct {
		name          string
		failAt        string
		wantSessions  int
		wantClosed    int
		wantError     bool
		changeRequest bool
	}{
		{name: "success", wantSessions: 2, wantClosed: 2},
		{name: "route only success", wantSessions: 1, wantClosed: 1, changeRequest: true},
		{name: "namespace before", failAt: "namespace-before", wantError: true},
		{name: "route open", failAt: "route-open", wantError: true},
		{name: "interfaces", failAt: "interfaces", wantSessions: 1, wantClosed: 1, wantError: true},
		{name: "routes", failAt: "routes", wantSessions: 1, wantClosed: 1, wantError: true},
		{name: "route close", failAt: "route-close", wantSessions: 1, wantClosed: 1, wantError: true},
		{name: "socket open", failAt: "socket-open", wantSessions: 1, wantClosed: 1, wantError: true},
		{name: "sockets", failAt: "sockets", wantSessions: 2, wantClosed: 2, wantError: true},
		{name: "socket close", failAt: "socket-close", wantSessions: 2, wantClosed: 2, wantError: true},
		{name: "policy", failAt: "policy", wantSessions: 2, wantClosed: 2, wantError: true},
		{name: "namespace after", failAt: "namespace-after", wantSessions: 2, wantClosed: 2, wantError: true},
		{name: "namespace change", failAt: "namespace-change", wantSessions: 2, wantClosed: 2, wantError: true},
		{name: "invalid facts", failAt: "invalid", wantSessions: 2, wantClosed: 2, wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := reference.Request
			if test.changeRequest {
				var err error
				request, err = NewPreAssignmentRequest(reference.Request.Candidate(), nil)
				if err != nil {
					t.Fatalf("NewPreAssignmentRequest() error = %v", err)
				}
			}
			fixture := &linuxTestPassFixture{
				failAt:      test.failAt,
				observation: reference,
				interfaces:  linuxTestInterfaces(),
				error:       sentinel,
			}
			observation, err := observeLinuxPassWith(context.Background(), request, 1000, linuxTestPassOperations(fixture))
			if test.wantError && err == nil {
				t.Fatal("observeLinuxPassWith() error = nil")
			}
			if !test.wantError && err != nil {
				t.Fatalf("observeLinuxPassWith() error = %v", err)
			}
			if !test.wantError && observation.Scope.Platform != PlatformLinux {
				t.Fatalf("observeLinuxPassWith() observation = %#v", observation)
			}
			closed := 0
			for _, session := range fixture.sessions {
				closed += session.closed
			}
			if len(fixture.sessions) != test.wantSessions || closed != test.wantClosed {
				t.Fatalf("observeLinuxPassWith() sessions = %d, closes = %d, want %d/%d", len(fixture.sessions), closed, test.wantSessions, test.wantClosed)
			}
		})
	}
}

// TestObserveLinuxRejectsInvalidAndCanceledRequests covers public admission checks before thread pinning.
func TestObserveLinuxRejectsInvalidAndCanceledRequests(t *testing.T) {
	if _, err := ObserveLinux(context.Background(), Request{}, 1000); err == nil {
		t.Fatal("ObserveLinux(invalid request) error = nil")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ObserveLinux(ctx, mustRequest(t), 1000); !errors.Is(err, context.Canceled) {
		t.Fatalf("ObserveLinux(canceled) error = %v", err)
	}
	if normalizeLinuxObservationContext(nil) == nil {
		t.Fatal("normalizeLinuxObservationContext(nil) = nil")
	}
}

// TestLinuxNetlinkCodecsRejectAmbiguousFrames covers request identity, alignment, and signed status parsing.
func TestLinuxNetlinkCodecsRejectAmbiguousFrames(t *testing.T) {
	payload := []byte{1, 2, 3}
	datagram := marshalLinuxNetlinkRequest(unix.RTM_NEWLINK, unix.NLM_F_MULTI, 7, 41, payload)
	messages, err := parseLinuxNetlinkDatagram(datagram, 41, 7)
	if err != nil {
		t.Fatalf("parseLinuxNetlinkDatagram() error = %v", err)
	}
	if len(messages) != 1 || messages[0].messageType != unix.RTM_NEWLINK || !reflect.DeepEqual(messages[0].payload, payload) {
		t.Fatalf("parseLinuxNetlinkDatagram() = %#v", messages)
	}

	tests := []struct {
		name   string
		mutate func([]byte) []byte
	}{
		{name: "short header", mutate: func(frame []byte) []byte { return frame[:unix.SizeofNlMsghdr-1] }},
		{name: "short length", mutate: func(frame []byte) []byte { binary.NativeEndian.PutUint32(frame[:4], 4); return frame }},
		{name: "long length", mutate: func(frame []byte) []byte {
			binary.NativeEndian.PutUint32(frame[:4], uint32(len(frame)+4))
			return frame
		}},
		{name: "wrong sequence", mutate: func(frame []byte) []byte { binary.NativeEndian.PutUint32(frame[8:12], 8); return frame }},
		{name: "wrong port", mutate: func(frame []byte) []byte { binary.NativeEndian.PutUint32(frame[12:16], 42); return frame }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			frame := test.mutate(append([]byte(nil), datagram...))
			if _, err := parseLinuxNetlinkDatagram(frame, 41, 7); err == nil {
				t.Fatal("parseLinuxNetlinkDatagram() error = nil")
			}
		})
	}

	zero := make([]byte, 4)
	if err := parseLinuxNetlinkError(zero); err != nil {
		t.Fatalf("parseLinuxNetlinkError(ACK) error = %v", err)
	}
	negativePermission := -int32(unix.EPERM)
	binary.NativeEndian.PutUint32(zero, uint32(negativePermission))
	if err := parseLinuxNetlinkError(zero); !errors.Is(err, unix.EPERM) {
		t.Fatalf("parseLinuxNetlinkError(EPERM) error = %v", err)
	}
	if err := parseLinuxNetlinkError(nil); err == nil {
		t.Fatal("parseLinuxNetlinkError(short) error = nil")
	}
	binary.NativeEndian.PutUint32(zero, 1)
	if err := parseLinuxNetlinkError(zero); err == nil {
		t.Fatal("parseLinuxNetlinkError(positive) error = nil")
	}
	if err := parseLinuxNetlinkDone(nil); err != nil {
		t.Fatalf("parseLinuxNetlinkDone(empty) error = %v", err)
	}
	if err := parseLinuxNetlinkDone([]byte{0}); err == nil {
		t.Fatal("parseLinuxNetlinkDone(short) error = nil")
	}
	binary.NativeEndian.PutUint32(zero, 0)
	if err := parseLinuxNetlinkDone(zero); err != nil {
		t.Fatalf("parseLinuxNetlinkDone(zero) error = %v", err)
	}
	interrupted := -int32(unix.EINTR)
	binary.NativeEndian.PutUint32(zero, uint32(interrupted))
	if err := parseLinuxNetlinkDone(zero); !errors.Is(err, unix.EINTR) {
		t.Fatalf("parseLinuxNetlinkDone(EINTR) error = %v", err)
	}
	binary.NativeEndian.PutUint32(zero, 1)
	if err := parseLinuxNetlinkDone(zero); err == nil {
		t.Fatal("parseLinuxNetlinkDone(positive) error = nil")
	}
}

// TestReceiveLinuxNetlinkEnforcesTransactionCompleteness covers interruption, sender, and termination rules.
func TestReceiveLinuxNetlinkEnforcesTransactionCompleteness(t *testing.T) {
	const portID = 41
	const sequence = 7
	kernel := &unix.SockaddrNetlink{Family: unix.AF_NETLINK}
	data := marshalLinuxNetlinkRequest(unix.RTM_NEWROUTE, 0, sequence, portID, []byte{1})
	multipartData := marshalLinuxNetlinkRequest(unix.RTM_NEWROUTE, unix.NLM_F_MULTI, sequence, portID, []byte{1})
	donePayload := make([]byte, 4)
	done := marshalLinuxNetlinkRequest(unix.NLMSG_DONE, unix.NLM_F_MULTI, sequence, portID, donePayload)

	t.Run("non-multipart terminates on data", func(t *testing.T) {
		calls := 0
		reply, err := receiveLinuxNetlinkWith(context.Background(), 1, portID, sequence, false, 2, linuxTestDatagramSource(t, []linuxTestDatagramResult{{payload: data, address: kernel}}, &calls))
		if err != nil || calls != 1 || reply.truncated || len(reply.messages) != 1 {
			t.Fatalf("receiveLinuxNetlinkWith() = %#v, calls %d, error %v", reply, calls, err)
		}
	})

	t.Run("multipart requires done", func(t *testing.T) {
		calls := 0
		reply, err := receiveLinuxNetlinkWith(context.Background(), 1, portID, sequence, true, 2, linuxTestDatagramSource(t, []linuxTestDatagramResult{{payload: multipartData, address: kernel}, {payload: done, address: kernel}}, &calls))
		if err != nil || calls != 2 || reply.truncated || len(reply.messages) != 1 {
			t.Fatalf("receiveLinuxNetlinkWith() = %#v, calls %d, error %v", reply, calls, err)
		}
	})

	t.Run("data after done is rejected", func(t *testing.T) {

		calls := 0
		datagram := append(append([]byte(nil), done...), data...)
		_, err := receiveLinuxNetlinkWith(context.Background(), 1, portID, sequence, true, 1, linuxTestDatagramSource(t, []linuxTestDatagramResult{{payload: datagram, address: kernel}}, &calls))
		if err == nil {
			t.Fatal("receiveLinuxNetlinkWith(data after DONE) error = nil")
		}
	})

	t.Run("missing done reaches bound", func(t *testing.T) {
		calls := 0
		_, err := receiveLinuxNetlinkWith(context.Background(), 1, portID, sequence, true, 2, linuxTestDatagramSource(t, []linuxTestDatagramResult{{payload: multipartData, address: kernel}, {payload: multipartData, address: kernel}}, &calls))
		if !errors.Is(err, errLinuxNetlinkReplyLimit) {
			t.Fatalf("receiveLinuxNetlinkWith() error = %v", err)
		}
	})

	for name, fixture := range map[string]linuxTestDatagramResult{
		"dump interrupted": {payload: marshalLinuxNetlinkRequest(unix.RTM_NEWROUTE, unix.NLM_F_MULTI|unix.NLM_F_DUMP_INTR, sequence, portID, []byte{1}), address: kernel},
		"overrun":          {payload: marshalLinuxNetlinkRequest(unix.NLMSG_OVERRUN, 0, sequence, portID, nil), address: kernel},
		"ENOBUFS":          {err: unix.ENOBUFS},
	} {
		t.Run(name, func(t *testing.T) {
			calls := 0
			_, err := receiveLinuxNetlinkWith(context.Background(), 1, portID, sequence, true, 1, linuxTestDatagramSource(t, []linuxTestDatagramResult{fixture}, &calls))
			if !errors.Is(err, errLinuxNetlinkInterrupted) {
				t.Fatalf("receiveLinuxNetlinkWith() error = %v", err)
			}
		})
	}

	for name, address := range map[string]*unix.SockaddrNetlink{
		"missing sender":      nil,
		"foreign sender port": {Family: unix.AF_NETLINK, Pid: 9},
		"multicast sender":    {Family: unix.AF_NETLINK, Groups: 1},
	} {
		t.Run(name, func(t *testing.T) {
			calls := 0
			_, err := receiveLinuxNetlinkWith(context.Background(), 1, portID, sequence, false, 1, linuxTestDatagramSource(t, []linuxTestDatagramResult{{payload: data, address: address}}, &calls))
			if err == nil {
				t.Fatal("receiveLinuxNetlinkWith(foreign sender) error = nil")
			}
		})
	}

	t.Run("oversized datagram", func(t *testing.T) {
		calls := 0
		_, err := receiveLinuxNetlinkWith(context.Background(), 1, portID, sequence, true, 2, linuxTestDatagramSource(t, []linuxTestDatagramResult{{address: kernel, oversized: true}}, &calls))
		if !errors.Is(err, errLinuxNetlinkReplyLimit) || calls != 1 {
			t.Fatalf("receiveLinuxNetlinkWith(oversized) calls %d, error %v", calls, err)
		}
	})

	t.Run("source failure", func(t *testing.T) {
		sentinel := errors.New("source fixture failure")
		calls := 0
		_, err := receiveLinuxNetlinkWith(context.Background(), 1, portID, sequence, true, 1, linuxTestDatagramSource(t, []linuxTestDatagramResult{{err: sentinel}}, &calls))
		if !errors.Is(err, sentinel) {
			t.Fatalf("receiveLinuxNetlinkWith(source failure) error = %v", err)
		}
	})

	t.Run("noop precedes data", func(t *testing.T) {
		calls := 0
		noop := marshalLinuxNetlinkRequest(unix.NLMSG_NOOP, 0, sequence, portID, nil)
		datagram := append(noop, data...)
		reply, err := receiveLinuxNetlinkWith(context.Background(), 1, portID, sequence, false, 1, linuxTestDatagramSource(t, []linuxTestDatagramResult{{payload: datagram, address: kernel}}, &calls))
		if err != nil || len(reply.messages) != 1 {
			t.Fatalf("receiveLinuxNetlinkWith(NOOP) = %#v, error %v", reply, err)
		}
	})

	t.Run("error ACK precedes data", func(t *testing.T) {
		calls := 0
		ack := marshalLinuxNetlinkRequest(unix.NLMSG_ERROR, 0, sequence, portID, make([]byte, 4))
		datagram := append(ack, data...)
		reply, err := receiveLinuxNetlinkWith(context.Background(), 1, portID, sequence, false, 1, linuxTestDatagramSource(t, []linuxTestDatagramResult{{payload: datagram, address: kernel}}, &calls))
		if err != nil || len(reply.messages) != 1 {
			t.Fatalf("receiveLinuxNetlinkWith(ACK) = %#v, error %v", reply, err)
		}
	})

	for name, status := range map[string]int32{"error": -int32(unix.EPERM), "invalid": 1} {
		t.Run("error reply "+name, func(t *testing.T) {
			payload := make([]byte, 4)
			binary.NativeEndian.PutUint32(payload, uint32(status))
			calls := 0
			frame := marshalLinuxNetlinkRequest(unix.NLMSG_ERROR, 0, sequence, portID, payload)
			if _, err := receiveLinuxNetlinkWith(context.Background(), 1, portID, sequence, false, 1, linuxTestDatagramSource(t, []linuxTestDatagramResult{{payload: frame, address: kernel}}, &calls)); err == nil {
				t.Fatal("receiveLinuxNetlinkWith(error reply) error = nil")
			}
		})
	}
}

// TestReceiveLinuxNetlinkDatagramChecksPeekAndConsume covers MSG_TRUNC and source-address mechanics.
func TestReceiveLinuxNetlinkDatagramChecksPeekAndConsume(t *testing.T) {
	frame := []byte{1, 2, 3, 4}
	kernel := &unix.SockaddrNetlink{Family: unix.AF_NETLINK}
	ready := func(context.Context, int, int16) error { return nil }

	t.Run("exact", func(t *testing.T) {
		calls := 0
		recvmsg := func(_ int, payload []byte, _ []byte, flags int) (int, int, int, unix.Sockaddr, error) {
			calls++
			if flags&unix.MSG_PEEK != 0 {
				return len(frame), 0, unix.MSG_TRUNC, kernel, nil
			}
			copy(payload, frame)
			return len(frame), 0, 0, kernel, nil
		}
		payload, address, oversized, err := receiveLinuxNetlinkDatagramWith(context.Background(), 1, ready, recvmsg)
		if err != nil || oversized || calls != 2 || address.Pid != 0 || !reflect.DeepEqual(payload, frame) {
			t.Fatalf("receiveLinuxNetlinkDatagramWith() = %v, %#v, %t, calls %d, error %v", payload, address, oversized, calls, err)
		}
	})

	t.Run("oversized", func(t *testing.T) {
		recvmsg := func(_ int, _ []byte, _ []byte, flags int) (int, int, int, unix.Sockaddr, error) {
			if flags&unix.MSG_PEEK != 0 {
				return linuxMaximumDatagramBytes + 1, 0, unix.MSG_TRUNC, kernel, nil
			}
			return linuxMaximumDatagramBytes + 1, 0, unix.MSG_TRUNC, kernel, nil
		}
		payload, _, oversized, err := receiveLinuxNetlinkDatagramWith(context.Background(), 1, ready, recvmsg)
		if err != nil || !oversized || payload != nil {
			t.Fatalf("receiveLinuxNetlinkDatagramWith(oversized) = %v, %t, %v", payload, oversized, err)
		}
	})

	for name, receive := range map[string]linuxNetlinkRecvmsg{
		"empty": func(_ int, _ []byte, _ []byte, _ int) (int, int, int, unix.Sockaddr, error) {
			return 0, 0, 0, kernel, nil
		},
		"peek failure": func(_ int, _ []byte, _ []byte, _ int) (int, int, int, unix.Sockaddr, error) {
			return 0, 0, 0, kernel, errors.New("peek fixture failure")
		},
		"unexpected truncation": func(_ int, _ []byte, _ []byte, flags int) (int, int, int, unix.Sockaddr, error) {
			if flags&unix.MSG_PEEK != 0 {
				return len(frame), 0, unix.MSG_TRUNC, kernel, nil
			}
			return len(frame), 0, unix.MSG_TRUNC, kernel, nil
		},
		"changed length": func(_ int, _ []byte, _ []byte, flags int) (int, int, int, unix.Sockaddr, error) {
			if flags&unix.MSG_PEEK != 0 {
				return len(frame), 0, unix.MSG_TRUNC, kernel, nil
			}
			return len(frame) - 1, 0, 0, kernel, nil
		},
		"wrong address": func(_ int, _ []byte, _ []byte, flags int) (int, int, int, unix.Sockaddr, error) {
			if flags&unix.MSG_PEEK != 0 {
				return len(frame), 0, unix.MSG_TRUNC, kernel, nil
			}
			return len(frame), 0, 0, &unix.SockaddrInet4{}, nil
		},
		"receive failure": func(_ int, _ []byte, _ []byte, flags int) (int, int, int, unix.Sockaddr, error) {
			if flags&unix.MSG_PEEK != 0 {
				return len(frame), 0, unix.MSG_TRUNC, kernel, nil
			}
			return 0, 0, 0, kernel, errors.New("receive fixture failure")
		},
		"oversized not truncated": func(_ int, _ []byte, _ []byte, flags int) (int, int, int, unix.Sockaddr, error) {
			if flags&unix.MSG_PEEK != 0 {
				return linuxMaximumDatagramBytes + 1, 0, unix.MSG_TRUNC, kernel, nil
			}
			return linuxMaximumDatagramBytes + 1, 0, 0, kernel, nil
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, _, err := receiveLinuxNetlinkDatagramWith(context.Background(), 1, ready, receive); err == nil {
				t.Fatal("receiveLinuxNetlinkDatagramWith() error = nil")
			}
		})
	}

	sentinel := errors.New("poll fixture failure")
	if _, _, _, err := receiveLinuxNetlinkDatagramWith(context.Background(), 1, func(context.Context, int, int16) error { return sentinel }, nil); !errors.Is(err, sentinel) {
		t.Fatalf("receiveLinuxNetlinkDatagramWith(poll failure) error = %v", err)
	}

	t.Run("retries transient syscalls", func(t *testing.T) {
		calls := 0
		recvmsg := func(_ int, payload []byte, _ []byte, flags int) (int, int, int, unix.Sockaddr, error) {
			calls++
			switch calls {
			case 1:
				return 0, 0, 0, kernel, unix.EINTR
			case 2:
				return len(frame), 0, unix.MSG_TRUNC, kernel, nil
			case 3:
				return 0, 0, 0, kernel, unix.EAGAIN
			case 4:
				return len(frame), 0, unix.MSG_TRUNC, kernel, nil
			default:
				copy(payload, frame)
				return len(frame), 0, 0, kernel, nil
			}
		}
		payload, _, _, err := receiveLinuxNetlinkDatagramWith(context.Background(), 1, ready, recvmsg)
		if err != nil || calls != 5 || !reflect.DeepEqual(payload, frame) {
			t.Fatalf("receiveLinuxNetlinkDatagramWith(retries) = %v, calls %d, error %v", payload, calls, err)
		}
	})
}

// TestOpenLinuxNetlinkWithRequiresStrictOptions covers setup cleanup and option failure.
func TestOpenLinuxNetlinkWithRequiresStrictOptions(t *testing.T) {
	closed := 0
	options := make([]int, 0, 3)
	operations := linuxNetlinkOpenOperations{
		socket: func(int, int, int) (int, error) { return 17, nil },
		bind:   func(int, unix.Sockaddr) error { return nil },
		setSocketOption: func(_ int, _ int, option int, _ int) error {
			options = append(options, option)
			return nil
		},
		localAddress: func(int) (unix.Sockaddr, error) { return &unix.SockaddrNetlink{Family: unix.AF_NETLINK, Pid: 22}, nil },
		close: func(int) error {
			closed++
			return nil
		},
	}
	client, err := openLinuxNetlinkWith(unix.NETLINK_ROUTE, operations)
	if err != nil {
		t.Fatalf("openLinuxNetlinkWith() error = %v", err)
	}
	if !reflect.DeepEqual(options, []int{unix.NETLINK_EXT_ACK, unix.NETLINK_CAP_ACK, unix.NETLINK_GET_STRICT_CHK}) {
		t.Fatalf("netlink options = %v", options)
	}
	if err := client.close(); err != nil || closed != 1 {
		t.Fatalf("client.close() = %v, closes %d", err, closed)
	}

	sentinel := errors.New("option fixture failure")
	closed = 0
	operations.setSocketOption = func(_ int, _ int, option int, _ int) error {
		if option == unix.NETLINK_CAP_ACK {
			return sentinel
		}
		return nil
	}
	if _, err := openLinuxNetlinkWith(unix.NETLINK_ROUTE, operations); !errors.Is(err, sentinel) {
		t.Fatalf("openLinuxNetlinkWith(option failure) error = %v", err)
	}
	if closed != 1 {
		t.Fatalf("openLinuxNetlinkWith(option failure) closes = %d, want 1", closed)
	}
}

// TestOpenLinuxNetlinkWithRejectsEverySetupFailure proves partial descriptors never escape cleanup.
func TestOpenLinuxNetlinkWithRejectsEverySetupFailure(t *testing.T) {
	sentinel := errors.New("setup fixture failure")
	tests := []struct {
		name   string
		mutate func(*linuxNetlinkOpenOperations)
	}{
		{name: "socket", mutate: func(operations *linuxNetlinkOpenOperations) {
			operations.socket = func(int, int, int) (int, error) { return -1, sentinel }
		}},
		{name: "bind", mutate: func(operations *linuxNetlinkOpenOperations) {
			operations.bind = func(int, unix.Sockaddr) error { return sentinel }
		}},
		{name: "local address", mutate: func(operations *linuxNetlinkOpenOperations) {
			operations.localAddress = func(int) (unix.Sockaddr, error) { return nil, sentinel }
		}},
		{name: "wrong family", mutate: func(operations *linuxNetlinkOpenOperations) {
			operations.localAddress = func(int) (unix.Sockaddr, error) { return &unix.SockaddrInet4{}, nil }
		}},
		{name: "zero port", mutate: func(operations *linuxNetlinkOpenOperations) {
			operations.localAddress = func(int) (unix.Sockaddr, error) { return &unix.SockaddrNetlink{}, nil }
		}},
		{name: "multicast local", mutate: func(operations *linuxNetlinkOpenOperations) {
			operations.localAddress = func(int) (unix.Sockaddr, error) { return &unix.SockaddrNetlink{Pid: 1, Groups: 1}, nil }
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			closed := 0
			operations := linuxNetlinkOpenOperations{
				socket:          func(int, int, int) (int, error) { return 17, nil },
				bind:            func(int, unix.Sockaddr) error { return nil },
				setSocketOption: func(int, int, int, int) error { return nil },
				localAddress:    func(int) (unix.Sockaddr, error) { return &unix.SockaddrNetlink{Pid: 22}, nil },
				close:           func(int) error { closed++; return nil },
			}
			test.mutate(&operations)
			_, err := openLinuxNetlinkWith(unix.NETLINK_ROUTE, operations)
			if err == nil {
				t.Fatal("openLinuxNetlinkWith() error = nil")
			}
			wantClosed := 1
			if test.name == "socket" {
				wantClosed = 0
			}
			if closed != wantClosed {
				t.Fatalf("openLinuxNetlinkWith() closes = %d, want %d", closed, wantClosed)
			}
		})
	}
	closeSentinel := errors.New("close fixture failure")
	client := &linuxNetlinkClient{fileDescriptor: 17, closeFile: func(int) error { return closeSentinel }}
	if err := client.close(); !errors.Is(err, closeSentinel) {
		t.Fatalf("client.close() error = %v", err)
	}
}

// TestSendLinuxNetlinkWithRetriesOnlyExpectedConditions covers EINTR, backpressure, and fatal errors.
func TestSendLinuxNetlinkWithRetriesOnlyExpectedConditions(t *testing.T) {
	sentinel := errors.New("send fixture failure")
	calls := 0
	err := sendLinuxNetlinkWith(context.Background(), 1, []byte{1}, func(int, []byte, int, unix.Sockaddr) error {
		calls++
		if calls == 1 {
			return unix.EINTR
		}
		if calls == 2 {
			return unix.EAGAIN
		}
		return nil
	}, func(context.Context, int, int16) error { return nil })
	if err != nil || calls != 3 {
		t.Fatalf("sendLinuxNetlinkWith(retries) calls = %d, error %v", calls, err)
	}
	if err := sendLinuxNetlinkWith(context.Background(), 1, nil, func(int, []byte, int, unix.Sockaddr) error { return sentinel }, func(context.Context, int, int16) error { return nil }); !errors.Is(err, sentinel) {
		t.Fatalf("sendLinuxNetlinkWith(fatal) error = %v", err)
	}
	if err := sendLinuxNetlinkWith(context.Background(), 1, nil, func(int, []byte, int, unix.Sockaddr) error { return unix.EWOULDBLOCK }, func(context.Context, int, int16) error { return sentinel }); !errors.Is(err, sentinel) {
		t.Fatalf("sendLinuxNetlinkWith(poll failure) error = %v", err)
	}
}

// TestPollLinuxNetlinkWithCoversDeadlineAndReadiness validates cancellation without wall-clock sleeps.
func TestPollLinuxNetlinkWithCoversDeadlineAndReadiness(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := pollLinuxNetlinkWith(canceled, 1, unix.POLLIN, time.Now, func([]unix.PollFd, int) (int, error) { return 0, nil }); !errors.Is(err, context.Canceled) {
		t.Fatalf("pollLinuxNetlinkWith(canceled) error = %v", err)
	}
	deadline := time.Unix(100, 0)
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	if err := pollLinuxNetlinkWith(ctx, 1, unix.POLLIN, func() time.Time { return deadline }, func([]unix.PollFd, int) (int, error) { return 0, nil }); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("pollLinuxNetlinkWith(deadline) error = %v", err)
	}

	sentinel := errors.New("poll fixture failure")
	if err := pollLinuxNetlinkWith(context.Background(), 1, unix.POLLIN, time.Now, func([]unix.PollFd, int) (int, error) { return 0, sentinel }); !errors.Is(err, sentinel) {
		t.Fatalf("pollLinuxNetlinkWith(failure) error = %v", err)
	}
	calls := 0
	err := pollLinuxNetlinkWith(context.Background(), 1, unix.POLLIN, time.Now, func(descriptors []unix.PollFd, _ int) (int, error) {
		calls++
		if calls == 1 {
			return 0, unix.EINTR
		}
		if calls == 2 {
			return 0, nil
		}
		descriptors[0].Revents = unix.POLLIN
		return 1, nil
	})
	if err != nil || calls != 3 {
		t.Fatalf("pollLinuxNetlinkWith(retries) calls = %d, error %v", calls, err)
	}
	if err := pollLinuxNetlinkWith(context.Background(), 1, unix.POLLIN, time.Now, func(descriptors []unix.PollFd, _ int) (int, error) {
		descriptors[0].Revents = unix.POLLNVAL
		return 1, nil
	}); err == nil {
		t.Fatal("pollLinuxNetlinkWith(POLLNVAL) error = nil")
	}
}

// TestLinuxAttributeCodecPreservesDuplicates proves padding and repeated authority fields remain visible.
func TestLinuxAttributeCodecPreservesDuplicates(t *testing.T) {
	payload := marshalLinuxNetlinkAttribute(nil, unix.RTA_UID, []byte{1, 2, 3, 4})
	payload = marshalLinuxNetlinkAttribute(payload, unix.RTA_UID, []byte{5, 6, 7, 8})
	attributes, err := parseLinuxNetlinkAttributes(payload)
	if err != nil {
		t.Fatalf("parseLinuxNetlinkAttributes() error = %v", err)
	}
	if len(attributes[unix.RTA_UID]) != 2 {
		t.Fatalf("RTA_UID values = %d, want 2", len(attributes[unix.RTA_UID]))
	}
	if _, _, err := oneLinuxAttribute(attributes, unix.RTA_UID); err == nil {
		t.Fatal("oneLinuxAttribute(duplicate) error = nil")
	}
	if _, err := parseLinuxNetlinkAttributes([]byte{3, 0, 1, 0}); err == nil {
		t.Fatal("parseLinuxNetlinkAttributes(short length) error = nil")
	}
	flagged := marshalLinuxNetlinkAttribute(nil, unix.RTA_UID|0x4000, []byte{1, 2, 3, 4})
	flaggedAttributes, err := parseLinuxNetlinkAttributes(flagged)
	if err != nil {
		t.Fatalf("parseLinuxNetlinkAttributes(flagged) error = %v", err)
	}
	if _, _, err := oneLinuxAttribute(flaggedAttributes, unix.RTA_UID); err == nil {
		t.Fatal("oneLinuxAttribute(flagged) error = nil")
	}
}

// TestObserveLinuxInterfacesSelectsKernelLoopback exercises raw link parsing and strict loopback cardinality.
func TestObserveLinuxInterfacesSelectsKernelLoopback(t *testing.T) {
	script := &scriptedLinuxNetlink{replies: []linuxNetlinkReply{{messages: []linuxNetlinkMessage{
		{messageType: unix.RTM_NEWLINK, payload: linuxTestLinkPayload(1, "loop", unix.IFF_UP|unix.IFF_RUNNING|unix.IFF_LOOPBACK, unix.ARPHRD_LOOPBACK)},
		{messageType: unix.RTM_NEWLINK, payload: linuxTestLinkPayload(2, "eth0", unix.IFF_UP|unix.IFF_RUNNING, unix.ARPHRD_ETHER)},
	}}}}
	snapshot, err := observeLinuxInterfaces(context.Background(), script)
	if err != nil {
		t.Fatalf("observeLinuxInterfaces() error = %v", err)
	}
	if snapshot.loopback.Interface != (InterfaceIdentity{Name: "loop", Index: 1}) || len(snapshot.ordered) != 2 {
		t.Fatalf("observeLinuxInterfaces() = %#v", snapshot)
	}
	if len(script.calls) != 1 || script.calls[0].messageType != unix.RTM_GETLINK || !script.calls[0].multipart || len(script.calls[0].payload) != unix.SizeofIfInfomsg {
		t.Fatalf("observeLinuxInterfaces() calls = %#v", script.calls)
	}

	missing := &scriptedLinuxNetlink{replies: []linuxNetlinkReply{{messages: []linuxNetlinkMessage{{messageType: unix.RTM_NEWLINK, payload: linuxTestLinkPayload(2, "eth0", unix.IFF_UP, unix.ARPHRD_ETHER)}}}}}
	if _, err := observeLinuxInterfaces(context.Background(), missing); err == nil {
		t.Fatal("observeLinuxInterfaces(no loopback) error = nil")
	}
}

// TestObserveLinuxInterfacesRejectsAmbiguousDumps covers repeated identities, malformed facts, and kernel interruption.
func TestObserveLinuxInterfacesRejectsAmbiguousDumps(t *testing.T) {
	loopback := linuxTestLinkPayload(1, "lo", unix.IFF_UP|unix.IFF_RUNNING|unix.IFF_LOOPBACK, unix.ARPHRD_LOOPBACK)
	tests := []struct {
		name     string
		reply    linuxNetlinkReply
		wantFail bool
		check    func(*testing.T, linuxInterfaceSnapshot)
	}{
		{name: "unexpected type", reply: linuxNetlinkReply{messages: []linuxNetlinkMessage{{messageType: unix.RTM_NEWROUTE, payload: loopback}}}, wantFail: true},
		{name: "duplicate index", reply: linuxNetlinkReply{messages: []linuxNetlinkMessage{{messageType: unix.RTM_NEWLINK, payload: loopback}, {messageType: unix.RTM_NEWLINK, payload: linuxTestLinkPayload(1, "other", unix.IFF_UP, unix.ARPHRD_ETHER)}}}, wantFail: true},
		{name: "duplicate name", reply: linuxNetlinkReply{messages: []linuxNetlinkMessage{{messageType: unix.RTM_NEWLINK, payload: loopback}, {messageType: unix.RTM_NEWLINK, payload: linuxTestLinkPayload(2, "lo", unix.IFF_UP, unix.ARPHRD_ETHER)}}}, wantFail: true},
		{name: "multiple native", reply: linuxNetlinkReply{messages: []linuxNetlinkMessage{{messageType: unix.RTM_NEWLINK, payload: loopback}, {messageType: unix.RTM_NEWLINK, payload: linuxTestLinkPayload(2, "lo2", unix.IFF_UP|unix.IFF_RUNNING|unix.IFF_LOOPBACK, unix.ARPHRD_LOOPBACK)}}}, wantFail: true},
		{name: "truncated", reply: linuxNetlinkReply{truncated: true, messages: []linuxNetlinkMessage{{messageType: unix.RTM_NEWLINK, payload: loopback}}}, check: func(t *testing.T, snapshot linuxInterfaceSnapshot) {
			if snapshot.complete || !snapshot.truncated {
				t.Fatalf("truncated snapshot = %#v", snapshot)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			snapshot, err := observeLinuxInterfaces(context.Background(), &scriptedLinuxNetlink{replies: []linuxNetlinkReply{test.reply}})
			if test.wantFail && err == nil {
				t.Fatal("observeLinuxInterfaces() error = nil")
			}
			if !test.wantFail && err != nil {
				t.Fatalf("observeLinuxInterfaces() error = %v", err)
			}
			if test.check != nil {
				test.check(t, snapshot)
			}
		})
	}
	messages := make([]linuxNetlinkMessage, 0, maximumPolicyInterfaces+2)
	messages = append(messages, linuxNetlinkMessage{messageType: unix.RTM_NEWLINK, payload: loopback})
	for index := 2; index <= maximumPolicyInterfaces+1; index++ {
		messages = append(messages, linuxNetlinkMessage{messageType: unix.RTM_NEWLINK, payload: linuxTestLinkPayload(uint32(index), "e"+strconv.Itoa(index), unix.IFF_UP, unix.ARPHRD_ETHER)})
	}
	messages = append(messages, linuxNetlinkMessage{messageType: unix.RTM_NEWLINK, payload: linuxTestLinkPayload(maximumPolicyInterfaces+1, "duplicate", unix.IFF_UP, unix.ARPHRD_ETHER)})
	if _, err := observeLinuxInterfaces(context.Background(), &scriptedLinuxNetlink{replies: []linuxNetlinkReply{{messages: messages}}}); err == nil {
		t.Fatal("observeLinuxInterfaces(duplicate beyond bound) error = nil")
	}
}

// TestParseLinuxInterfaceRejectsMalformedNames covers every fixed-header and IFLA_IFNAME boundary.
func TestParseLinuxInterfaceRejectsMalformedNames(t *testing.T) {
	valid := linuxTestLinkPayload(1, "lo", unix.IFF_UP|unix.IFF_RUNNING|unix.IFF_LOOPBACK, unix.ARPHRD_LOOPBACK)
	tests := map[string][]byte{
		"short":          valid[:unix.SizeofIfInfomsg-1],
		"zero index":     linuxTestLinkPayload(0, "lo", unix.IFF_UP, unix.ARPHRD_LOOPBACK),
		"missing name":   make([]byte, unix.SizeofIfInfomsg),
		"unterminated":   marshalLinuxNetlinkAttribute(make([]byte, unix.SizeofIfInfomsg), unix.IFLA_IFNAME, []byte("lo")),
		"embedded null":  marshalLinuxNetlinkAttribute(make([]byte, unix.SizeofIfInfomsg), unix.IFLA_IFNAME, []byte{'l', 0, 'o', 0}),
		"duplicate name": marshalLinuxNetlinkAttribute(valid, unix.IFLA_IFNAME, []byte("other\x00")),
		"long name":      linuxTestLinkPayload(1, "abcdefghijklmnop", unix.IFF_UP, unix.ARPHRD_LOOPBACK),
		"control name":   linuxTestLinkPayload(1, "bad\n", unix.IFF_UP, unix.ARPHRD_LOOPBACK),
	}
	for name, payload := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := parseLinuxInterface(payload); err == nil {
				t.Fatal("parseLinuxInterface() error = nil")
			}
		})
	}
}

// TestObserveLinuxInterfacesCoversExchangeAndRetentionFailures completes bounded link-dump handling.
func TestObserveLinuxInterfacesCoversExchangeAndRetentionFailures(t *testing.T) {
	sentinel := errors.New("link fixture failure")
	if _, err := observeLinuxInterfaces(context.Background(), &scriptedLinuxNetlink{errors: []error{sentinel}}); !errors.Is(err, sentinel) {
		t.Fatalf("observeLinuxInterfaces(exchange failure) error = %v", err)
	}
	if _, err := observeLinuxInterfaces(context.Background(), &scriptedLinuxNetlink{replies: []linuxNetlinkReply{{messages: []linuxNetlinkMessage{{messageType: unix.RTM_NEWLINK, payload: []byte{1}}}}}}); err == nil {
		t.Fatal("observeLinuxInterfaces(parse failure) error = nil")
	}
	messages := make([]linuxNetlinkMessage, 0, maximumPolicyInterfaces+1)
	for index := 1; index <= maximumPolicyInterfaces; index++ {
		messages = append(messages, linuxNetlinkMessage{messageType: unix.RTM_NEWLINK, payload: linuxTestLinkPayload(uint32(index), "e"+strconv.Itoa(index), unix.IFF_UP, unix.ARPHRD_ETHER)})
	}
	messages = append(messages, linuxNetlinkMessage{messageType: unix.RTM_NEWLINK, payload: linuxTestLinkPayload(maximumPolicyInterfaces+1, "lo", unix.IFF_UP|unix.IFF_RUNNING|unix.IFF_LOOPBACK, unix.ARPHRD_LOOPBACK)})
	if _, err := observeLinuxInterfaces(context.Background(), &scriptedLinuxNetlink{replies: []linuxNetlinkReply{{messages: messages}}}); err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("observeLinuxInterfaces(loopback outside bound) error = %v", err)
	}
}

// TestLinuxRouteLookupBindsUIDProtocolAndPort verifies policy-routing selectors use native and network byte order correctly.
func TestLinuxRouteLookupBindsUIDProtocolAndPort(t *testing.T) {
	payload := marshalLinuxRouteLookup(testCandidate, 501, SocketRequirement{Transport: TransportUDP4, Port: 5353})
	if binary.NativeEndian.Uint32(payload[8:12]) != unix.RTM_F_LOOKUP_TABLE|unix.RTM_F_FIB_MATCH {
		t.Fatalf("route flags = %#x", binary.NativeEndian.Uint32(payload[8:12]))
	}
	attributes, err := parseLinuxNetlinkAttributes(payload[unix.SizeofRtMsg:])
	if err != nil {
		t.Fatalf("parseLinuxNetlinkAttributes() error = %v", err)
	}
	if got := binary.NativeEndian.Uint32(attributes[unix.RTA_UID][0].payload); got != 501 {
		t.Fatalf("RTA_UID = %d, want 501", got)
	}
	if got := attributes[unix.RTA_IP_PROTO][0].payload; !reflect.DeepEqual(got, []byte{unix.IPPROTO_UDP}) {
		t.Fatalf("RTA_IP_PROTO = %v", got)
	}
	if got := binary.BigEndian.Uint16(attributes[unix.RTA_DPORT][0].payload); got != 5353 {
		t.Fatalf("RTA_DPORT = %d, want 5353", got)
	}

	generic := marshalLinuxRouteLookup(testCandidate, 501, SocketRequirement{})
	genericAttributes, err := parseLinuxNetlinkAttributes(generic[unix.SizeofRtMsg:])
	if err != nil {
		t.Fatalf("parseLinuxNetlinkAttributes(generic) error = %v", err)
	}
	if len(genericAttributes[unix.RTA_IP_PROTO]) != 0 || len(genericAttributes[unix.RTA_DPORT]) != 0 {
		t.Fatalf("generic route selectors = %#v", genericAttributes)
	}
}

// TestParseLinuxRouteNormalizesMatchingFacts covers baseline, default, and unrepresentable routes.
func TestParseLinuxRouteNormalizesMatchingFacts(t *testing.T) {
	interfaces := linuxTestInterfaces()
	baselinePayload := linuxTestRoutePayload(netip.MustParsePrefix("127.0.0.0/8"), unix.RTN_LOCAL, 1, netip.Addr{}, nil)
	fact, matches, representable, err := parseLinuxRoute(baselinePayload, testCandidate, interfaces)
	if err != nil || !matches || !representable {
		t.Fatalf("parseLinuxRoute(baseline) = %#v, %t, %t, %v", fact, matches, representable, err)
	}
	if !fact.NativeLoopback || fact.Destination != netip.MustParsePrefix("127.0.0.0/8") {
		t.Fatalf("parseLinuxRoute(baseline) fact = %#v", fact)
	}

	defaultPayload := linuxTestRoutePayload(netip.MustParsePrefix("0.0.0.0/0"), unix.RTN_UNICAST, 2, netip.MustParseAddr("192.0.2.1"), nil)
	fact, matches, representable, err = parseLinuxRoute(defaultPayload, testCandidate, interfaces)
	if err != nil || !matches || !representable || fact.NativeLoopback || fact.Gateway != netip.MustParseAddr("192.0.2.1") {
		t.Fatalf("parseLinuxRoute(default) = %#v, %t, %t, %v", fact, matches, representable, err)
	}

	nonmatching := linuxTestRoutePayload(netip.MustParsePrefix("192.0.2.0/24"), unix.RTN_UNICAST, 2, netip.Addr{}, nil)
	_, matches, representable, err = parseLinuxRoute(nonmatching, testCandidate, interfaces)
	if err != nil || matches || !representable {
		t.Fatalf("parseLinuxRoute(nonmatching) = matches %t representable %t error %v", matches, representable, err)
	}

	for name, payload := range map[string][]byte{
		"blackhole":         linuxTestRoutePayload(netip.MustParsePrefix("127.0.0.0/8"), unix.RTN_BLACKHOLE, 1, netip.Addr{}, nil),
		"multipath":         linuxTestRoutePayload(netip.MustParsePrefix("127.0.0.0/8"), unix.RTN_LOCAL, 1, netip.Addr{}, map[uint16][]byte{unix.RTA_MULTIPATH: {1}}),
		"unknown interface": linuxTestRoutePayload(netip.MustParsePrefix("127.0.0.0/8"), unix.RTN_LOCAL, 99, netip.Addr{}, nil),
	} {
		t.Run(name, func(t *testing.T) {
			_, matches, representable, err := parseLinuxRoute(payload, testCandidate, interfaces)
			if err != nil || !matches || representable {
				t.Fatalf("parseLinuxRoute() = matches %t representable %t error %v", matches, representable, err)
			}
		})
	}
}

// TestParseLinuxRouteRejectsMalformedAuthorityFields covers family, prefix, and duplicate selector facts.
func TestParseLinuxRouteRejectsMalformedAuthorityFields(t *testing.T) {
	interfaces := linuxTestInterfaces()
	valid := linuxTestRoutePayload(netip.MustParsePrefix("127.0.0.0/8"), unix.RTN_LOCAL, 1, netip.Addr{}, nil)
	wrongFamily := append([]byte(nil), valid...)
	wrongFamily[0] = unix.AF_INET6
	badPrefix := append([]byte(nil), valid...)
	badPrefix[1] = 33
	missingDestination := marshalLinuxRouteMessage(unix.AF_INET, 8, 0)
	missingDestination[7] = unix.RTN_LOCAL
	noncanonical := marshalLinuxRouteMessage(unix.AF_INET, 8, 0)
	noncanonical[7] = unix.RTN_LOCAL
	noncanonical = marshalLinuxNetlinkAttribute(noncanonical, unix.RTA_DST, []byte{127, 1, 0, 0})
	oif := make([]byte, 4)
	binary.NativeEndian.PutUint32(oif, 1)
	noncanonical = marshalLinuxNetlinkAttribute(noncanonical, unix.RTA_OIF, oif)
	duplicateDestination := marshalLinuxNetlinkAttribute(append([]byte(nil), valid...), unix.RTA_DST, []byte{127, 0, 0, 0})
	duplicateInterface := marshalLinuxNetlinkAttribute(append([]byte(nil), valid...), unix.RTA_OIF, oif)
	duplicateGateway := marshalLinuxNetlinkAttribute(append([]byte(nil), valid...), unix.RTA_GATEWAY, []byte{192, 0, 2, 1})
	duplicateGateway = marshalLinuxNetlinkAttribute(duplicateGateway, unix.RTA_GATEWAY, []byte{192, 0, 2, 2})
	for name, payload := range map[string][]byte{
		"short":                 valid[:unix.SizeofRtMsg-1],
		"wrong family":          wrongFamily,
		"bad prefix":            badPrefix,
		"missing destination":   missingDestination,
		"noncanonical":          noncanonical,
		"duplicate destination": duplicateDestination,
		"duplicate interface":   duplicateInterface,
		"duplicate gateway":     duplicateGateway,
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, _, err := parseLinuxRoute(payload, testCandidate, interfaces); err == nil {
				t.Fatal("parseLinuxRoute() error = nil")
			}
		})
	}

	missingInterface := linuxTestRoutePayload(netip.MustParsePrefix("127.0.0.0/8"), unix.RTN_LOCAL, 0, netip.Addr{}, nil)
	_, matches, representable, err := parseLinuxRoute(missingInterface, testCandidate, interfaces)
	if err != nil || !matches || representable {
		t.Fatalf("parseLinuxRoute(missing interface) = matches %t representable %t error %v", matches, representable, err)
	}
	badGateway := linuxTestRoutePayload(netip.MustParsePrefix("127.0.0.0/8"), unix.RTN_LOCAL, 1, netip.Addr{}, map[uint16][]byte{unix.RTA_GATEWAY: {1, 2, 3}})
	_, matches, representable, err = parseLinuxRoute(badGateway, testCandidate, interfaces)
	if err != nil || !matches || representable {
		t.Fatalf("parseLinuxRoute(bad gateway) = matches %t representable %t error %v", matches, representable, err)
	}
	for name, mutate := range map[string]func([]byte){
		"source prefix": func(payload []byte) { payload[2] = 32 },
		"TOS":           func(payload []byte) { payload[3] = 1 },
		"route flags":   func(payload []byte) { binary.NativeEndian.PutUint32(payload[8:12], unix.RTM_F_CLONED) },
		"wrong table":   func(payload []byte) { payload[4] = unix.RT_TABLE_MAIN },
		"wrong protocol": func(payload []byte) {
			payload[5] = unix.RTPROT_BOOT
		},
		"wrong scope": func(payload []byte) { payload[6] = unix.RT_SCOPE_LINK },
	} {
		t.Run(name, func(t *testing.T) {
			payload := append([]byte(nil), valid...)
			mutate(payload)
			_, matches, representable, err := parseLinuxRoute(payload, testCandidate, interfaces)
			if err != nil || !matches || representable {
				t.Fatalf("parseLinuxRoute() = matches %t representable %t error %v", matches, representable, err)
			}
		})
	}
	encapsulated := linuxTestRoutePayload(linuxOrdinaryLoopbackPrefix, unix.RTN_LOCAL, 1, netip.Addr{}, map[uint16][]byte{unix.RTA_ENCAP: {1}})
	_, matches, representable, err = parseLinuxRoute(encapsulated, testCandidate, interfaces)
	if err != nil || !matches || representable {
		t.Fatalf("parseLinuxRoute(encap) = matches %t representable %t error %v", matches, representable, err)
	}
	selectedTable := append([]byte(nil), valid...)
	selectedTable[4] = unix.RT_TABLE_MAIN
	_, matches, representable, err = parseLinuxSelectedRoute(selectedTable, testCandidate, interfaces)
	if err != nil || !matches || !representable {
		t.Fatalf("parseLinuxSelectedRoute(main table) = matches %t representable %t error %v", matches, representable, err)
	}
}

// TestObserveLinuxRoutesRequiresEveryFlowToSelectTheDumpedRoute covers FIB consistency and incompleteness.
func TestObserveLinuxRoutesRequiresEveryFlowToSelectTheDumpedRoute(t *testing.T) {
	request, err := NewPreAssignmentRequest(testCandidate, []SocketRequirement{{Transport: TransportTCP4, Port: 443}})
	if err != nil {
		t.Fatalf("NewPreAssignmentRequest() error = %v", err)
	}
	interfaces := linuxTestInterfaces()
	baseline := linuxTestRoutePayload(netip.MustParsePrefix("127.0.0.0/8"), unix.RTN_LOCAL, 1, netip.Addr{}, nil)
	defaultRoute := linuxTestRoutePayload(netip.MustParsePrefix("0.0.0.0/0"), unix.RTN_UNICAST, 2, netip.MustParseAddr("192.0.2.1"), nil)
	script := &scriptedLinuxNetlink{replies: []linuxNetlinkReply{
		{messages: []linuxNetlinkMessage{{messageType: unix.RTM_NEWROUTE, payload: baseline}, {messageType: unix.RTM_NEWROUTE, payload: defaultRoute}}},
		{messages: []linuxNetlinkMessage{{messageType: unix.RTM_NEWROUTE, payload: baseline}}},
		{messages: []linuxNetlinkMessage{{messageType: unix.RTM_NEWROUTE, payload: baseline}}},
	}}
	snapshot, err := observeLinuxRoutes(context.Background(), script, request, 1000, interfaces)
	if err != nil {
		t.Fatalf("observeLinuxRoutes() error = %v", err)
	}
	if !snapshot.Complete || snapshot.Selected == nil || len(snapshot.Matching) != 2 {
		t.Fatalf("observeLinuxRoutes() = %#v", snapshot)
	}
	if len(script.calls) != 3 {
		t.Fatalf("route calls = %d, want 3", len(script.calls))
	}

	different := linuxTestRoutePayload(netip.MustParsePrefix("0.0.0.0/0"), unix.RTN_UNICAST, 2, netip.MustParseAddr("192.0.2.1"), nil)
	script = &scriptedLinuxNetlink{replies: []linuxNetlinkReply{
		{messages: []linuxNetlinkMessage{{messageType: unix.RTM_NEWROUTE, payload: baseline}, {messageType: unix.RTM_NEWROUTE, payload: defaultRoute}}},
		{messages: []linuxNetlinkMessage{{messageType: unix.RTM_NEWROUTE, payload: baseline}}},
		{messages: []linuxNetlinkMessage{{messageType: unix.RTM_NEWROUTE, payload: different}}},
	}}
	snapshot, err = observeLinuxRoutes(context.Background(), script, request, 1000, interfaces)
	if err != nil {
		t.Fatalf("observeLinuxRoutes(inconsistent) error = %v", err)
	}
	if snapshot.Complete || snapshot.Selected != nil {
		t.Fatalf("observeLinuxRoutes(inconsistent) = %#v", snapshot)
	}
}

// TestObserveLinuxRoutesPreservesIncompleteAndTruncatedEvidence covers dump and selection failure shapes.
func TestObserveLinuxRoutesPreservesIncompleteAndTruncatedEvidence(t *testing.T) {
	request, err := NewPreAssignmentRequest(testCandidate, nil)
	if err != nil {
		t.Fatalf("NewPreAssignmentRequest() error = %v", err)
	}
	interfaces := linuxTestInterfaces()
	baseline := linuxTestRoutePayload(linuxOrdinaryLoopbackPrefix, unix.RTN_LOCAL, 1, netip.Addr{}, nil)
	sentinel := errors.New("route fixture failure")
	if _, err := observeLinuxRoutes(context.Background(), &scriptedLinuxNetlink{errors: []error{sentinel}}, request, 1000, interfaces); !errors.Is(err, sentinel) {
		t.Fatalf("observeLinuxRoutes(exchange failure) error = %v", err)
	}
	for name, payload := range map[string]linuxNetlinkMessage{
		"unexpected type": {messageType: unix.RTM_NEWLINK, payload: baseline},
		"parse failure":   {messageType: unix.RTM_NEWROUTE, payload: []byte{1}},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := observeLinuxRoutes(context.Background(), &scriptedLinuxNetlink{replies: []linuxNetlinkReply{{messages: []linuxNetlinkMessage{payload}}}}, request, 1000, interfaces)
			if err == nil {
				t.Fatal("observeLinuxRoutes() error = nil")
			}
		})
	}
	nonmatching := linuxTestRoutePayload(netip.MustParsePrefix("192.0.2.0/24"), unix.RTN_UNICAST, 2, netip.Addr{}, nil)
	unrepresentable := linuxTestRoutePayload(linuxOrdinaryLoopbackPrefix, unix.RTN_BLACKHOLE, 1, netip.Addr{}, nil)
	script := &scriptedLinuxNetlink{replies: []linuxNetlinkReply{
		{messages: []linuxNetlinkMessage{{messageType: unix.RTM_NEWROUTE, payload: nonmatching}, {messageType: unix.RTM_NEWROUTE, payload: unrepresentable}, {messageType: unix.RTM_NEWROUTE, payload: baseline}}},
		{messages: []linuxNetlinkMessage{{messageType: unix.RTM_NEWROUTE, payload: baseline}}},
	}}
	snapshot, err := observeLinuxRoutes(context.Background(), script, request, 1000, interfaces)
	if err != nil || snapshot.Complete || snapshot.Selected == nil || snapshot.Truncated {
		t.Fatalf("observeLinuxRoutes(unrepresentable) = %#v, error %v", snapshot, err)
	}

	script = &scriptedLinuxNetlink{replies: []linuxNetlinkReply{
		{messages: []linuxNetlinkMessage{{messageType: unix.RTM_NEWROUTE, payload: baseline}}},
		{truncated: true},
	}}
	snapshot, err = observeLinuxRoutes(context.Background(), script, request, 1000, interfaces)
	if err != nil || snapshot.Complete || !snapshot.Truncated || snapshot.Selected != nil {
		t.Fatalf("observeLinuxRoutes(truncated selection) = %#v, error %v", snapshot, err)
	}

	many := make([]linuxNetlinkMessage, 0, maximumRouteFacts+1)
	defaultRoute := linuxTestRoutePayload(netip.MustParsePrefix("0.0.0.0/0"), unix.RTN_UNICAST, 2, netip.MustParseAddr("192.0.2.1"), nil)
	for index := 0; index < maximumRouteFacts; index++ {
		many = append(many, linuxNetlinkMessage{messageType: unix.RTM_NEWROUTE, payload: defaultRoute})
	}
	many = append(many, linuxNetlinkMessage{messageType: unix.RTM_NEWROUTE, payload: baseline})
	script = &scriptedLinuxNetlink{replies: []linuxNetlinkReply{
		{messages: many},
		{messages: []linuxNetlinkMessage{{messageType: unix.RTM_NEWROUTE, payload: baseline}}},
	}}
	snapshot, err = observeLinuxRoutes(context.Background(), script, request, 1000, interfaces)
	if err != nil || snapshot.Complete || !snapshot.Truncated || len(snapshot.Matching) != maximumRouteFacts {
		t.Fatalf("observeLinuxRoutes(fact bound) = complete %t truncated %t facts %d error %v", snapshot.Complete, snapshot.Truncated, len(snapshot.Matching), err)
	}
}

// TestObserveLinuxSelectedRoutesCoversIncompleteReplies exercises every per-flow aggregation failure.
func TestObserveLinuxSelectedRoutesCoversIncompleteReplies(t *testing.T) {
	request, err := NewPreAssignmentRequest(testCandidate, nil)
	if err != nil {
		t.Fatalf("NewPreAssignmentRequest() error = %v", err)
	}
	interfaces := linuxTestInterfaces()
	baseline := linuxTestRoutePayload(linuxOrdinaryLoopbackPrefix, unix.RTN_LOCAL, 1, netip.Addr{}, nil)
	sentinel := errors.New("selection fixture failure")
	if _, _, _, err := observeLinuxSelectedRoutes(context.Background(), &scriptedLinuxNetlink{errors: []error{sentinel}}, request, 1000, interfaces); !errors.Is(err, sentinel) {
		t.Fatalf("observeLinuxSelectedRoutes(exchange failure) error = %v", err)
	}
	for name, reply := range map[string]linuxNetlinkReply{
		"truncated":       {truncated: true},
		"missing message": {},
		"wrong type":      {messages: []linuxNetlinkMessage{{messageType: unix.RTM_NEWLINK, payload: baseline}}},
		"nonmatching":     {messages: []linuxNetlinkMessage{{messageType: unix.RTM_NEWROUTE, payload: linuxTestRoutePayload(netip.MustParsePrefix("192.0.2.0/24"), unix.RTN_UNICAST, 2, netip.Addr{}, nil)}}},
		"unrepresentable": {messages: []linuxNetlinkMessage{{messageType: unix.RTM_NEWROUTE, payload: linuxTestRoutePayload(linuxOrdinaryLoopbackPrefix, unix.RTN_BLACKHOLE, 1, netip.Addr{}, nil)}}},
	} {
		t.Run(name, func(t *testing.T) {
			selected, complete, truncated, err := observeLinuxSelectedRoutes(context.Background(), &scriptedLinuxNetlink{replies: []linuxNetlinkReply{reply}}, request, 1000, interfaces)
			if err != nil || selected != nil || complete {
				t.Fatalf("observeLinuxSelectedRoutes() = %#v, complete %t truncated %t error %v", selected, complete, truncated, err)
			}
			if name == "truncated" && !truncated {
				t.Fatal("observeLinuxSelectedRoutes(truncated) truncated = false")
			}
		})
	}
	if _, _, _, err := observeLinuxSelectedRoutes(context.Background(), &scriptedLinuxNetlink{replies: []linuxNetlinkReply{{messages: []linuxNetlinkMessage{{messageType: unix.RTM_NEWROUTE, payload: []byte{1}}}}}}, request, 1000, interfaces); err == nil {
		t.Fatal("observeLinuxSelectedRoutes(parse failure) error = nil")
	}
}

// TestLinuxInetDiagCodecDistinguishesIPv6WildcardBehavior covers listener filtering and SKV6ONLY strictness.
func TestLinuxInetDiagCodecDistinguishesIPv6WildcardBehavior(t *testing.T) {
	request, err := NewPreAssignmentRequest(testCandidate, []SocketRequirement{{Transport: TransportTCP4, Port: 443}})
	if err != nil {
		t.Fatalf("NewPreAssignmentRequest() error = %v", err)
	}
	payload := linuxTestInetDiagPayload(unix.AF_INET6, linuxTCPListenState, 443, netip.IPv6Unspecified(), map[uint16][]byte{linuxInetDiagSKV6Only: {1}})
	fact, relevant, complete, err := parseLinuxInetDiagMessage(payload, unix.AF_INET6, unix.IPPROTO_TCP, request)
	if err != nil || !relevant || !complete || fact.IPv6Only != IPv6OnlyEnabled || !fact.TCPAccepting {
		t.Fatalf("parseLinuxInetDiagMessage(v6only) = %#v, %t, %t, %v", fact, relevant, complete, err)
	}

	payload = linuxTestInetDiagPayload(unix.AF_INET6, linuxTCPListenState, 443, netip.IPv6Unspecified(), nil)
	fact, relevant, complete, err = parseLinuxInetDiagMessage(payload, unix.AF_INET6, unix.IPPROTO_TCP, request)
	if err != nil || !relevant || complete || fact.IPv6Only != IPv6OnlyUnknown {
		t.Fatalf("parseLinuxInetDiagMessage(unknown) = %#v, %t, %t, %v", fact, relevant, complete, err)
	}

	payload = linuxTestInetDiagPayload(unix.AF_INET6, linuxTCPListenState, 443, netip.IPv6Unspecified(), map[uint16][]byte{linuxInetDiagSKV6Only: {2}})
	if _, _, _, err := parseLinuxInetDiagMessage(payload, unix.AF_INET6, unix.IPPROTO_TCP, request); err == nil {
		t.Fatal("parseLinuxInetDiagMessage(invalid SKV6ONLY) error = nil")
	}

	payload = linuxTestInetDiagPayload(unix.AF_INET, linuxTCPListenState, 80, netip.IPv4Unspecified(), nil)
	_, relevant, complete, err = parseLinuxInetDiagMessage(payload, unix.AF_INET, unix.IPPROTO_TCP, request)
	if err != nil || relevant || !complete {
		t.Fatalf("parseLinuxInetDiagMessage(unrequested) = relevant %t complete %t error %v", relevant, complete, err)
	}
}

// TestLinuxInetDiagCodecRejectsMalformedAndMappedMessages covers strict family and attribute parsing.
func TestLinuxInetDiagCodecRejectsMalformedAndMappedMessages(t *testing.T) {
	request, err := NewPreAssignmentRequest(testCandidate, []SocketRequirement{{Transport: TransportTCP4, Port: 443}})
	if err != nil {
		t.Fatalf("NewPreAssignmentRequest() error = %v", err)
	}
	valid := linuxTestInetDiagPayload(unix.AF_INET, linuxTCPListenState, 443, testCandidate, nil)
	wrongFamily := append([]byte(nil), valid...)
	wrongFamily[0] = unix.AF_INET6
	wrongState := append([]byte(nil), valid...)
	wrongState[1] = 1
	badPadding := append([]byte(nil), valid...)
	badPadding[12] = 1
	badAttribute := linuxTestInetDiagPayload(unix.AF_INET6, linuxTCPListenState, 443, netip.IPv6Unspecified(), map[uint16][]byte{linuxInetDiagSKV6Only: {1, 0}})
	duplicateAttribute := linuxTestInetDiagPayload(unix.AF_INET6, linuxTCPListenState, 443, netip.IPv6Unspecified(), map[uint16][]byte{linuxInetDiagSKV6Only: {1}})
	duplicateAttribute = marshalLinuxNetlinkAttribute(duplicateAttribute, linuxInetDiagSKV6Only, []byte{1})
	for name, fixture := range map[string]struct {
		payload  []byte
		family   uint8
		protocol uint8
	}{
		"short":              {payload: valid[:linuxInetDiagMessageBytes-1], family: unix.AF_INET, protocol: unix.IPPROTO_TCP},
		"wrong family":       {payload: wrongFamily, family: unix.AF_INET, protocol: unix.IPPROTO_TCP},
		"wrong TCP state":    {payload: wrongState, family: unix.AF_INET, protocol: unix.IPPROTO_TCP},
		"IPv4 padding":       {payload: badPadding, family: unix.AF_INET, protocol: unix.IPPROTO_TCP},
		"bad SKV6ONLY":       {payload: badAttribute, family: unix.AF_INET6, protocol: unix.IPPROTO_TCP},
		"duplicate SKV6ONLY": {payload: duplicateAttribute, family: unix.AF_INET6, protocol: unix.IPPROTO_TCP},
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, _, err := parseLinuxInetDiagMessage(fixture.payload, fixture.family, fixture.protocol, request); err == nil {
				t.Fatal("parseLinuxInetDiagMessage() error = nil")
			}
		})
	}

	mapped := netip.MustParseAddr("::ffff:127.77.0.10")
	mappedPayload := linuxTestInetDiagPayload(unix.AF_INET6, linuxTCPListenState, 443, mapped, nil)
	if _, _, _, err := parseLinuxInetDiagMessage(mappedPayload, unix.AF_INET6, unix.IPPROTO_TCP, request); err == nil {
		t.Fatal("parseLinuxInetDiagMessage(mapped) error = nil")
	}
	nonWildcardV6Only := linuxTestInetDiagPayload(unix.AF_INET6, linuxTCPListenState, 443, netip.IPv6Loopback(), map[uint16][]byte{linuxInetDiagSKV6Only: {1}})
	if _, _, _, err := parseLinuxInetDiagMessage(nonWildcardV6Only, unix.AF_INET6, unix.IPPROTO_TCP, request); err == nil {
		t.Fatal("parseLinuxInetDiagMessage(non-wildcard SKV6ONLY) error = nil")
	}
	if _, err := parseLinuxInetDiagAddress(make([]byte, 16), unix.AF_UNSPEC); err == nil {
		t.Fatal("parseLinuxInetDiagAddress(unsupported) error = nil")
	}
}

// TestObserveLinuxSocketsQueriesEachFamilyAndProtocol covers bounded aggregation and truncated dumps.
func TestObserveLinuxSocketsQueriesEachFamilyAndProtocol(t *testing.T) {
	request, err := NewPreAssignmentRequest(testCandidate, []SocketRequirement{
		{Transport: TransportTCP4, Port: 443},
		{Transport: TransportUDP4, Port: 53},
	})
	if err != nil {
		t.Fatalf("NewPreAssignmentRequest() error = %v", err)
	}
	script := &scriptedLinuxNetlink{replies: []linuxNetlinkReply{
		{messages: []linuxNetlinkMessage{{messageType: unix.SOCK_DIAG_BY_FAMILY, payload: linuxTestInetDiagPayload(unix.AF_INET, linuxTCPListenState, 443, netip.IPv4Unspecified(), nil)}}},
		{},
		{messages: []linuxNetlinkMessage{{messageType: unix.SOCK_DIAG_BY_FAMILY, payload: linuxTestInetDiagPayload(unix.AF_INET, 7, 53, testCandidate, nil)}}},
		{truncated: true},
	}}
	snapshot, err := observeLinuxSockets(context.Background(), script, request)
	if err != nil {
		t.Fatalf("observeLinuxSockets() error = %v", err)
	}
	if snapshot.Complete || !snapshot.Truncated || len(snapshot.Endpoints) != 2 || len(script.calls) != 4 {
		t.Fatalf("observeLinuxSockets() = %#v, calls %d", snapshot, len(script.calls))
	}
	if script.calls[0].payload[0] != unix.AF_INET || script.calls[1].payload[0] != unix.AF_INET6 || script.calls[2].payload[1] != unix.IPPROTO_UDP {
		t.Fatalf("socket calls = %#v", script.calls)
	}
}

// TestObserveLinuxSocketsRejectsIncompleteAndMalformedDumps exercises every aggregation failure boundary.
func TestObserveLinuxSocketsRejectsIncompleteAndMalformedDumps(t *testing.T) {
	request, err := NewPreAssignmentRequest(testCandidate, []SocketRequirement{{Transport: TransportTCP4, Port: 443}})
	if err != nil {
		t.Fatalf("NewPreAssignmentRequest() error = %v", err)
	}
	sentinel := errors.New("socket fixture failure")
	if _, err := observeLinuxSockets(context.Background(), &scriptedLinuxNetlink{errors: []error{sentinel}}, request); !errors.Is(err, sentinel) {
		t.Fatalf("observeLinuxSockets(exchange failure) error = %v", err)
	}
	for name, message := range map[string]linuxNetlinkMessage{
		"wrong type": {messageType: unix.RTM_NEWROUTE},
		"short":      {messageType: unix.SOCK_DIAG_BY_FAMILY, payload: []byte{1}},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := observeLinuxSockets(context.Background(), &scriptedLinuxNetlink{replies: []linuxNetlinkReply{{messages: []linuxNetlinkMessage{message}}}}, request)
			if err == nil {
				t.Fatal("observeLinuxSockets() error = nil")
			}
		})
	}

	missingV6Only := linuxTestInetDiagPayload(unix.AF_INET6, linuxTCPListenState, 443, netip.IPv6Unspecified(), nil)
	snapshot, err := observeLinuxSockets(context.Background(), &scriptedLinuxNetlink{replies: []linuxNetlinkReply{{}, {messages: []linuxNetlinkMessage{{messageType: unix.SOCK_DIAG_BY_FAMILY, payload: missingV6Only}}}}}, request)
	if err != nil || snapshot.Complete || snapshot.Truncated || len(snapshot.Endpoints) != 1 {
		t.Fatalf("observeLinuxSockets(missing SKV6ONLY) = %#v, error %v", snapshot, err)
	}

	messages := make([]linuxNetlinkMessage, maximumSocketFacts+1)
	payload := linuxTestInetDiagPayload(unix.AF_INET, linuxTCPListenState, 443, netip.IPv4Unspecified(), nil)
	for index := range messages {
		messages[index] = linuxNetlinkMessage{messageType: unix.SOCK_DIAG_BY_FAMILY, payload: payload}
	}
	snapshot, err = observeLinuxSockets(context.Background(), &scriptedLinuxNetlink{replies: []linuxNetlinkReply{{messages: messages}, {}}}, request)
	if err != nil || snapshot.Complete || !snapshot.Truncated || len(snapshot.Endpoints) != maximumSocketFacts {
		t.Fatalf("observeLinuxSockets(limit) = %#v, error %v", snapshot, err)
	}

	if _, err := parseLinuxInetDiagAddress([]byte{1}, unix.AF_INET); err == nil {
		t.Fatal("parseLinuxInetDiagAddress(short) error = nil")
	}
}

// TestAssembleLinuxPolicyComputesEffectiveValues covers all/default semantics and incomplete interface dumps.
func TestAssembleLinuxPolicyComputesEffectiveValues(t *testing.T) {
	interfaces := linuxTestInterfaces()
	values := map[string]bool{"all": false, "default": false, "lo": true, "eth0": false}
	read := func(name string) (bool, error) { return values[name], nil }
	facts, err := assembleLinuxPolicy(interfaces, true, read)
	if err != nil {
		t.Fatalf("assembleLinuxPolicy() error = %v", err)
	}
	if !facts.Complete || !facts.IPNonlocalBind || !facts.RouteLocalnet[0].Enabled || facts.RouteLocalnet[1].Enabled {
		t.Fatalf("assembleLinuxPolicy() = %#v", facts)
	}

	values["all"] = true
	facts, err = assembleLinuxPolicy(interfaces, false, read)
	if err != nil || facts.Complete || !facts.RouteLocalnet[0].Enabled || !facts.RouteLocalnet[1].Enabled {
		t.Fatalf("assembleLinuxPolicy(all) = %#v, %v", facts, err)
	}
	values["all"] = false
	values["default"] = true
	facts, err = assembleLinuxPolicy(interfaces, false, read)
	if err != nil || facts.Complete {
		t.Fatalf("assembleLinuxPolicy(default) = %#v, %v", facts, err)
	}

	interfaces.complete = false
	interfaces.truncated = true
	values["default"] = false
	facts, err = assembleLinuxPolicy(interfaces, false, read)
	if err != nil || facts.Complete || !facts.Truncated {
		t.Fatalf("assembleLinuxPolicy(truncated) = %#v, %v", facts, err)
	}

	badInterfaces := linuxTestInterfaces()
	badInterfaces.ordered[0].identity.Name = "../lo"
	if _, err := assembleLinuxPolicy(badInterfaces, false, read); err == nil {
		t.Fatal("assembleLinuxPolicy(unsafe name) error = nil")
	}
	sentinel := errors.New("policy fixture failure")
	if _, err := assembleLinuxPolicy(linuxTestInterfaces(), false, func(string) (bool, error) { return false, sentinel }); !errors.Is(err, sentinel) {
		t.Fatalf("assembleLinuxPolicy(read failure) error = %v", err)
	}
}

// TestObserveLinuxPolicyHonorsCancellation avoids starting procfs traversal for an abandoned admission.
func TestObserveLinuxPolicyHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := observeLinuxPolicy(ctx, linuxTestInterfaces()); !errors.Is(err, context.Canceled) {
		t.Fatalf("observeLinuxPolicy(canceled) error = %v", err)
	}
}

// TestObserveLinuxPolicyWithCoversProcTraversalFailures exercises each authority-bearing path boundary.
func TestObserveLinuxPolicyWithCoversProcTraversalFailures(t *testing.T) {
	interfaces := linuxTestInterfaces()
	fixture := &linuxTestProcFixture{}
	facts, err := observeLinuxPolicyWith(context.Background(), interfaces, linuxTestProcOperations(fixture))
	if err != nil {
		t.Fatalf("observeLinuxPolicyWith() error = %v", err)
	}
	if !facts.Complete || len(facts.RouteLocalnet) != 2 || fixture.closed == 0 {
		t.Fatalf("observeLinuxPolicyWith() = %#v, closes %d", facts, fixture.closed)
	}

	for _, failure := range []struct {
		name      string
		failAt    string
		wrongFSAt string
	}{
		{name: "root open", failAt: "open:/proc"},
		{name: "root statfs", failAt: "fs:/proc"},
		{name: "root foreign fs", wrongFSAt: "fs:/proc"},
		{name: "sys open", failAt: "open:/proc/sys"},
		{name: "sys foreign fs", wrongFSAt: "fs:/proc/sys"},
		{name: "net open", failAt: "open:/proc/sys/net"},
		{name: "ipv4 open", failAt: "open:/proc/sys/net/ipv4"},
		{name: "ip nonlocal open", failAt: "open:/proc/sys/net/ipv4/ip_nonlocal_bind"},
		{name: "ip nonlocal read", failAt: "read:/proc/sys/net/ipv4/ip_nonlocal_bind"},
		{name: "conf open", failAt: "open:/proc/sys/net/ipv4/conf"},
		{name: "all interface open", failAt: "open:/proc/sys/net/ipv4/conf/all"},
		{name: "default value open", failAt: "open:/proc/sys/net/ipv4/conf/default/route_localnet"},
		{name: "link value read", failAt: "read:/proc/sys/net/ipv4/conf/lo/route_localnet"},
	} {
		t.Run(failure.name, func(t *testing.T) {
			fixture := &linuxTestProcFixture{failAt: failure.failAt, wrongFSAt: failure.wrongFSAt}
			if _, err := observeLinuxPolicyWith(context.Background(), interfaces, linuxTestProcOperations(fixture)); err == nil {
				t.Fatal("observeLinuxPolicyWith() error = nil")
			}
		})
	}
}

// TestObserveLinuxPolicyWithChecksCancellationBetweenOperations covers each explicit context boundary.
func TestObserveLinuxPolicyWithChecksCancellationBetweenOperations(t *testing.T) {
	for _, cancellation := range []string{
		"fs:/proc",
		"fs:/proc/sys",
		"fs:/proc/sys/net",
		"fs:/proc/sys/net/ipv4",
		"read:/proc/sys/net/ipv4/ip_nonlocal_bind",
		"fs:/proc/sys/net/ipv4/conf",
	} {
		t.Run(cancellation, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			fixture := &linuxTestProcFixture{cancelAt: cancellation, cancel: cancel}
			_, err := observeLinuxPolicyWith(ctx, linuxTestInterfaces(), linuxTestProcOperations(fixture))
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("observeLinuxPolicyWith() error = %v", err)
			}
		})
	}
}

// TestReadLinuxProcBooleanWithRejectsMalformedAndUntrustedValues covers bounded scalar parsing.
func TestReadLinuxProcBooleanWithRejectsMalformedAndUntrustedValues(t *testing.T) {
	const parentDescriptor = 1
	const valuePath = "/proc/test/value"
	for name, fixture := range map[string]*linuxTestProcFixture{
		"open failure":    {paths: map[int]string{parentDescriptor: "/proc/test"}, failAt: "open:" + valuePath},
		"foreign fs":      {paths: map[int]string{parentDescriptor: "/proc/test"}, wrongFSAt: "fs:" + valuePath},
		"stat failure":    {paths: map[int]string{parentDescriptor: "/proc/test"}, failAt: "stat:" + valuePath},
		"nonregular":      {paths: map[int]string{parentDescriptor: "/proc/test"}, nonregularAt: "stat:" + valuePath},
		"read failure":    {paths: map[int]string{parentDescriptor: "/proc/test"}, failAt: "read:" + valuePath},
		"oversized":       {paths: map[int]string{parentDescriptor: "/proc/test"}, contents: map[string]string{valuePath: strings.Repeat("1", linuxMaximumProcValueBytes+1)}},
		"invalid boolean": {paths: map[int]string{parentDescriptor: "/proc/test"}, contents: map[string]string{valuePath: "2\n"}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := readLinuxProcBoolean(linuxTestProcOperations(fixture), parentDescriptor, "value"); err == nil {
				t.Fatal("readLinuxProcBoolean() error = nil")
			}
		})
	}
	fixture := &linuxTestProcFixture{
		paths:       map[int]string{parentDescriptor: "/proc/test"},
		contents:    map[string]string{valuePath: "1\n"},
		interruptAt: "read:" + valuePath,
	}
	value, err := readLinuxProcBoolean(linuxTestProcOperations(fixture), parentDescriptor, "value")
	if err != nil || !value || !fixture.interrupted {
		t.Fatalf("readLinuxProcBoolean(EINTR then true) = %t, interrupted %t, error %v", value, fixture.interrupted, err)
	}
}

// TestObserveLinuxNativeRouteOnly proves the read-only production path on every Linux test worker.
func TestObserveLinuxNativeRouteOnly(t *testing.T) {
	request, err := NewPreAssignmentRequest(netip.MustParseAddr("127.253.254.253"), nil)
	if err != nil {
		t.Fatalf("NewPreAssignmentRequest() error = %v", err)
	}
	linuxTestObserve(t, request)
}

// TestObserveLinuxNativeSockets exercises live inet_diag with temporary owner-process sockets.
func TestObserveLinuxNativeSockets(t *testing.T) {
	tcpListener, err := net.Listen("tcp6", "[::]:0")
	if err != nil {
		t.Skipf("IPv6 listener is unavailable: %v", err)
	}
	defer func() { _ = tcpListener.Close() }()
	udpListener, err := net.ListenPacket("udp6", "[::]:0")
	if err != nil {
		t.Skipf("IPv6 packet listener is unavailable: %v", err)
	}
	defer func() { _ = udpListener.Close() }()
	tcpPort := uint16(tcpListener.Addr().(*net.TCPAddr).Port)
	udpPort := uint16(udpListener.LocalAddr().(*net.UDPAddr).Port)
	request, err := NewPreAssignmentRequest(netip.MustParseAddr("127.253.254.253"), []SocketRequirement{
		{Transport: TransportTCP4, Port: tcpPort},
		{Transport: TransportUDP4, Port: udpPort},
	})
	if err != nil {
		t.Fatalf("NewPreAssignmentRequest() error = %v", err)
	}
	observation, assessment := linuxTestObserve(t, request)
	foundTCP := false
	foundUDP := false
	for _, endpoint := range observation.Sockets.Endpoints {
		if endpoint.Address != netip.IPv6Unspecified() || endpoint.IPv6Only != IPv6OnlyEnabled {
			continue
		}
		foundTCP = foundTCP || endpoint.Protocol == SocketProtocolTCP && endpoint.Port == tcpPort
		foundUDP = foundUDP || endpoint.Protocol == SocketProtocolUDP && endpoint.Port == udpPort
	}
	if !foundTCP || !foundUDP {
		t.Fatalf("ObserveLinux() IPv6-only endpoints = %#v", observation.Sockets.Endpoints)
	}
	if assessment.Sockets != StateSafe {
		t.Fatalf("IPv6-only socket assessment = %s, want safe", assessment.Sockets)
	}
}

// TestObserveLinuxNativeIPv4WildcardConflicts proves held TCP and UDP wildcards block the candidate.
func TestObserveLinuxNativeIPv4WildcardConflicts(t *testing.T) {
	tcpListener, err := net.Listen("tcp4", "0.0.0.0:0")
	if err != nil {
		t.Fatalf("net.Listen(tcp4 wildcard) error = %v", err)
	}
	defer func() { _ = tcpListener.Close() }()
	udpListener, err := net.ListenPacket("udp4", "0.0.0.0:0")
	if err != nil {
		t.Fatalf("net.ListenPacket(udp4 wildcard) error = %v", err)
	}
	defer func() { _ = udpListener.Close() }()
	tcpPort := uint16(tcpListener.Addr().(*net.TCPAddr).Port)
	udpPort := uint16(udpListener.LocalAddr().(*net.UDPAddr).Port)
	request, err := NewPreAssignmentRequest(netip.MustParseAddr("127.253.254.253"), []SocketRequirement{
		{Transport: TransportTCP4, Port: tcpPort},
		{Transport: TransportUDP4, Port: udpPort},
	})
	if err != nil {
		t.Fatalf("NewPreAssignmentRequest() error = %v", err)
	}
	observation, assessment := linuxTestObserve(t, request)
	if assessment.Sockets != StateConflict {
		t.Fatalf("IPv4 wildcard socket assessment = %s, endpoints %#v", assessment.Sockets, observation.Sockets.Endpoints)
	}
	foundTCP := false
	foundUDP := false
	for _, endpoint := range observation.Sockets.Endpoints {
		foundTCP = foundTCP || endpoint.Protocol == SocketProtocolTCP && endpoint.Port == tcpPort && endpoint.Address == netip.IPv4Unspecified()
		foundUDP = foundUDP || endpoint.Protocol == SocketProtocolUDP && endpoint.Port == udpPort && endpoint.Address == netip.IPv4Unspecified()
	}
	if !foundTCP || !foundUDP {
		t.Fatalf("ObserveLinux() IPv4 wildcard endpoints = %#v", observation.Sockets.Endpoints)
	}
}

// TestObserveLinuxNativeSpecificOtherAddressStaysSafe proves filtering does not invent a candidate conflict.
func TestObserveLinuxNativeSpecificOtherAddressStaysSafe(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen(tcp4 specific) error = %v", err)
	}
	defer func() { _ = listener.Close() }()
	port := uint16(listener.Addr().(*net.TCPAddr).Port)
	request, err := NewPreAssignmentRequest(netip.MustParseAddr("127.253.254.253"), []SocketRequirement{{Transport: TransportTCP4, Port: port}})
	if err != nil {
		t.Fatalf("NewPreAssignmentRequest() error = %v", err)
	}
	observation, assessment := linuxTestObserve(t, request)
	if assessment.Sockets != StateSafe {
		t.Fatalf("specific other-address socket assessment = %s, endpoints %#v", assessment.Sockets, observation.Sockets.Endpoints)
	}
}

// TestObserveLinuxNativeDualStackWildcardConflicts proves SKV6ONLY=0 consumes the IPv4 capability.
func TestObserveLinuxNativeDualStackWildcardConflicts(t *testing.T) {
	fileDescriptor, err := unix.Socket(unix.AF_INET6, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, unix.IPPROTO_TCP)
	if errors.Is(err, unix.EAFNOSUPPORT) || errors.Is(err, unix.EPROTONOSUPPORT) {
		t.Skipf("IPv6 sockets are unavailable: %v", err)
	}
	if err != nil {
		t.Fatalf("unix.Socket(AF_INET6) error = %v", err)
	}
	defer func() { _ = unix.Close(fileDescriptor) }()
	if err := unix.SetsockoptInt(fileDescriptor, unix.IPPROTO_IPV6, unix.IPV6_V6ONLY, 0); err != nil {
		t.Fatalf("SetsockoptInt(IPV6_V6ONLY=0) error = %v", err)
	}
	if err := unix.Bind(fileDescriptor, &unix.SockaddrInet6{}); err != nil {
		t.Fatalf("unix.Bind(IPv6 wildcard) error = %v", err)
	}
	if err := unix.Listen(fileDescriptor, 1); err != nil {
		t.Fatalf("unix.Listen(IPv6 wildcard) error = %v", err)
	}
	address, err := unix.Getsockname(fileDescriptor)
	if err != nil {
		t.Fatalf("unix.Getsockname(IPv6 wildcard) error = %v", err)
	}
	port := uint16(address.(*unix.SockaddrInet6).Port)
	request, err := NewPreAssignmentRequest(netip.MustParseAddr("127.253.254.253"), []SocketRequirement{{Transport: TransportTCP4, Port: port}})
	if err != nil {
		t.Fatalf("NewPreAssignmentRequest() error = %v", err)
	}
	observation, assessment := linuxTestObserve(t, request)
	if assessment.Sockets != StateConflict {
		t.Fatalf("dual-stack wildcard socket assessment = %s, endpoints %#v", assessment.Sockets, observation.Sockets.Endpoints)
	}
	found := false
	for _, endpoint := range observation.Sockets.Endpoints {
		found = found || endpoint.Protocol == SocketProtocolTCP && endpoint.Port == port && endpoint.Address == netip.IPv6Unspecified() && endpoint.IPv6Only == IPv6OnlyDisabled
	}
	if !found {
		t.Fatalf("ObserveLinux() dual-stack endpoint = %#v", observation.Sockets.Endpoints)
	}
}

// linuxTestObserve runs the live adapter and always validates and classifies its returned evidence.
func linuxTestObserve(t *testing.T, request Request) (Observation, Assessment) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	observation, err := ObserveLinux(ctx, request, uint32(os.Getuid()))
	if err != nil {
		t.Fatalf("ObserveLinux() error = %v", err)
	}
	if err := observation.Validate(); err != nil {
		t.Fatalf("Observation.Validate() error = %v", err)
	}
	assessment, err := observation.Classify()
	if err != nil {
		t.Fatalf("Observation.Classify() error = %v", err)
	}
	return observation, assessment
}

// linuxTestInterfaces returns the two-interface namespace used by route and policy fixtures.
func linuxTestInterfaces() linuxInterfaceSnapshot {
	loopback := linuxInterface{identity: InterfaceIdentity{Name: "lo", Index: 1}, hardware: unix.ARPHRD_LOOPBACK, flags: unix.IFF_UP | unix.IFF_RUNNING | unix.IFF_LOOPBACK}
	ethernet := linuxInterface{identity: InterfaceIdentity{Name: "eth0", Index: 2}, hardware: unix.ARPHRD_ETHER, flags: unix.IFF_UP | unix.IFF_RUNNING}
	return linuxInterfaceSnapshot{
		byIndex:   map[uint32]linuxInterface{1: loopback, 2: ethernet},
		ordered:   []linuxInterface{loopback, ethernet},
		loopback:  LoopbackIdentity{Interface: loopback.identity, Kind: LoopbackKindLinuxNative},
		complete:  true,
		truncated: false,
	}
}

// linuxTestLinkPayload constructs one raw ifinfomsg fixture with an IFLA_IFNAME attribute.
func linuxTestLinkPayload(index uint32, name string, flags uint32, hardware uint16) []byte {
	payload := make([]byte, unix.SizeofIfInfomsg)
	binary.NativeEndian.PutUint16(payload[2:4], hardware)
	binary.NativeEndian.PutUint32(payload[4:8], index)
	binary.NativeEndian.PutUint32(payload[8:12], flags)
	return marshalLinuxNetlinkAttribute(payload, unix.IFLA_IFNAME, append([]byte(name), 0))
}

// linuxTestRoutePayload constructs one raw rtmsg fixture with optional additional attributes.
func linuxTestRoutePayload(destination netip.Prefix, routeType uint8, interfaceIndex uint32, gateway netip.Addr, additional map[uint16][]byte) []byte {
	payload := marshalLinuxRouteMessage(unix.AF_INET, uint8(destination.Bits()), 0)
	payload[7] = routeType
	if destination == linuxOrdinaryLoopbackPrefix && routeType == unix.RTN_LOCAL && interfaceIndex == 1 {
		payload[4] = unix.RT_TABLE_LOCAL
		payload[5] = unix.RTPROT_KERNEL
		payload[6] = unix.RT_SCOPE_HOST
	}
	if destination.Bits() != 0 {
		address := destination.Addr().As4()
		payload = marshalLinuxNetlinkAttribute(payload, unix.RTA_DST, address[:])
	}
	if interfaceIndex != 0 {
		encoded := make([]byte, 4)
		binary.NativeEndian.PutUint32(encoded, interfaceIndex)
		payload = marshalLinuxNetlinkAttribute(payload, unix.RTA_OIF, encoded)
	}
	if gateway.IsValid() {
		encoded := gateway.As4()
		payload = marshalLinuxNetlinkAttribute(payload, unix.RTA_GATEWAY, encoded[:])
	}
	for attributeType, value := range additional {
		payload = marshalLinuxNetlinkAttribute(payload, attributeType, value)
	}
	return payload
}

// linuxTestInetDiagPayload constructs one inet_diag_msg fixture with source address and attributes.
func linuxTestInetDiagPayload(family uint8, state uint8, port uint16, address netip.Addr, additional map[uint16][]byte) []byte {
	payload := make([]byte, linuxInetDiagMessageBytes)
	payload[0] = family
	payload[1] = state
	binary.BigEndian.PutUint16(payload[4:6], port)
	if family == unix.AF_INET {
		encoded := address.As4()
		copy(payload[8:12], encoded[:])
	} else {
		encoded := address.As16()
		copy(payload[8:24], encoded[:])
	}
	for attributeType, value := range additional {
		payload = marshalLinuxNetlinkAttribute(payload, attributeType, value)
	}
	return payload
}
