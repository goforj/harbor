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

	"github.com/goforj/harbor/internal/platform/linuxnetlink"
	"golang.org/x/sys/unix"
)

// linuxExchangeCall records one native request without exposing mutable slices to a test.
type linuxExchangeCall struct {
	messageType uint16
	flags       uint16
	payload     []byte
	completion  linuxnetlink.Completion
}

// scriptedLinuxNetlink returns fixture replies in request order.
type scriptedLinuxNetlink struct {
	replies []linuxnetlink.Reply
	errors  []error
	calls   []linuxExchangeCall
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

// Exchange fails because pass-orchestration fixtures replace every native fact collector.
func (session *linuxTestObservationSession) Exchange(context.Context, uint16, uint16, []byte, linuxnetlink.Completion) (linuxnetlink.Reply, error) {
	return linuxnetlink.Reply{}, errors.New("unexpected pass fixture exchange")
}

// Close records that a successful or poisoned pass session cannot be reused.
func (session *linuxTestObservationSession) Close() error {
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

// Exchange records a defensive payload copy before consuming its scripted result.
func (script *scriptedLinuxNetlink) Exchange(_ context.Context, messageType uint16, flags uint16, payload []byte, completion linuxnetlink.Completion) (linuxnetlink.Reply, error) {
	script.calls = append(script.calls, linuxExchangeCall{messageType: messageType, flags: flags, payload: append([]byte(nil), payload...), completion: completion})
	index := len(script.calls) - 1
	if index < len(script.errors) && script.errors[index] != nil {
		return linuxnetlink.Reply{}, script.errors[index]
	}
	if index >= len(script.replies) {
		return linuxnetlink.Reply{}, errors.New("unexpected fixture exchange")
	}
	return script.replies[index], nil
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
		openRoute: func() (linuxObservationSession, error) {
			if fixture.failAt == "route-open" {
				return nil, fixture.error
			}
			session := &linuxTestObservationSession{}
			if fixture.failAt == "route-close" {
				session.closeErr = fixture.error
			}
			fixture.sessions = append(fixture.sessions, session)
			return session, nil
		},
		openSocketDiag: func() (linuxObservationSession, error) {
			if fixture.failAt == "socket-open" {
				return nil, fixture.error
			}
			session := &linuxTestObservationSession{}
			if fixture.failAt == "socket-close" {
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
	for name, retryable := range map[string]error{
		"interrupted": linuxnetlink.ErrInterrupted,
		"reply limit": linuxnetlink.ErrReplyLimit,
	} {
		t.Run(name, func(t *testing.T) {
			calls := 0
			observation, err := observeStableLinux(context.Background(), reference.Request, 1000, func(context.Context, Request, uint32) (Observation, error) {
				calls++
				if calls == 1 {
					return Observation{}, retryable
				}
				return reference, nil
			})
			if err != nil || calls != 3 || observation.Scope.Platform != PlatformLinux {
				t.Fatalf("observeStableLinux() = %#v, calls %d, error %v", observation, calls, err)
			}
		})
	}
	if _, err := observeStableLinux(context.Background(), reference.Request, 1000, func(context.Context, Request, uint32) (Observation, error) {
		return Observation{}, nil
	}); err == nil || !strings.Contains(err.Error(), "invalid native facts") {
		t.Fatalf("observeStableLinux(invalid facts) error = %v", err)
	}
	if _, err := observeStableLinux(context.Background(), reference.Request, 1000, func(context.Context, Request, uint32) (Observation, error) {
		return Observation{}, linuxnetlink.ErrInterrupted
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

// TestObserveLinuxInterfacesSelectsKernelLoopback exercises raw link parsing and strict loopback cardinality.
func TestObserveLinuxInterfacesSelectsKernelLoopback(t *testing.T) {
	script := &scriptedLinuxNetlink{replies: []linuxnetlink.Reply{{Messages: []linuxnetlink.Message{
		{Type: unix.RTM_NEWLINK, Payload: linuxTestLinkPayload(1, "loop", unix.IFF_UP|unix.IFF_RUNNING|unix.IFF_LOOPBACK, unix.ARPHRD_LOOPBACK)},
		{Type: unix.RTM_NEWLINK, Payload: linuxTestLinkPayload(2, "eth0", unix.IFF_UP|unix.IFF_RUNNING, unix.ARPHRD_ETHER)},
	}}}}
	snapshot, err := observeLinuxInterfaces(context.Background(), script)
	if err != nil {
		t.Fatalf("observeLinuxInterfaces() error = %v", err)
	}
	if snapshot.loopback.Interface != (InterfaceIdentity{Name: "loop", Index: 1}) || len(snapshot.ordered) != 2 {
		t.Fatalf("observeLinuxInterfaces() = %#v", snapshot)
	}
	if len(script.calls) != 1 || script.calls[0].messageType != unix.RTM_GETLINK || script.calls[0].completion != linuxnetlink.CompletionDump || len(script.calls[0].payload) != unix.SizeofIfInfomsg {
		t.Fatalf("observeLinuxInterfaces() calls = %#v", script.calls)
	}

	missing := &scriptedLinuxNetlink{replies: []linuxnetlink.Reply{{Messages: []linuxnetlink.Message{{Type: unix.RTM_NEWLINK, Payload: linuxTestLinkPayload(2, "eth0", unix.IFF_UP, unix.ARPHRD_ETHER)}}}}}
	if _, err := observeLinuxInterfaces(context.Background(), missing); err == nil {
		t.Fatal("observeLinuxInterfaces(no loopback) error = nil")
	}
}

// TestObserveLinuxInterfacesRejectsAmbiguousDumps covers repeated identities and malformed facts.
func TestObserveLinuxInterfacesRejectsAmbiguousDumps(t *testing.T) {
	loopback := linuxTestLinkPayload(1, "lo", unix.IFF_UP|unix.IFF_RUNNING|unix.IFF_LOOPBACK, unix.ARPHRD_LOOPBACK)
	tests := []struct {
		name     string
		reply    linuxnetlink.Reply
		wantFail bool
	}{
		{name: "unexpected type", reply: linuxnetlink.Reply{Messages: []linuxnetlink.Message{{Type: unix.RTM_NEWROUTE, Payload: loopback}}}, wantFail: true},
		{name: "duplicate index", reply: linuxnetlink.Reply{Messages: []linuxnetlink.Message{{Type: unix.RTM_NEWLINK, Payload: loopback}, {Type: unix.RTM_NEWLINK, Payload: linuxTestLinkPayload(1, "other", unix.IFF_UP, unix.ARPHRD_ETHER)}}}, wantFail: true},
		{name: "duplicate name", reply: linuxnetlink.Reply{Messages: []linuxnetlink.Message{{Type: unix.RTM_NEWLINK, Payload: loopback}, {Type: unix.RTM_NEWLINK, Payload: linuxTestLinkPayload(2, "lo", unix.IFF_UP, unix.ARPHRD_ETHER)}}}, wantFail: true},
		{name: "multiple native", reply: linuxnetlink.Reply{Messages: []linuxnetlink.Message{{Type: unix.RTM_NEWLINK, Payload: loopback}, {Type: unix.RTM_NEWLINK, Payload: linuxTestLinkPayload(2, "lo2", unix.IFF_UP|unix.IFF_RUNNING|unix.IFF_LOOPBACK, unix.ARPHRD_LOOPBACK)}}}, wantFail: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := observeLinuxInterfaces(context.Background(), &scriptedLinuxNetlink{replies: []linuxnetlink.Reply{test.reply}})
			if test.wantFail && err == nil {
				t.Fatal("observeLinuxInterfaces() error = nil")
			}
			if !test.wantFail && err != nil {
				t.Fatalf("observeLinuxInterfaces() error = %v", err)
			}
		})
	}
	messages := make([]linuxnetlink.Message, 0, maximumPolicyInterfaces+2)
	messages = append(messages, linuxnetlink.Message{Type: unix.RTM_NEWLINK, Payload: loopback})
	for index := 2; index <= maximumPolicyInterfaces+1; index++ {
		messages = append(messages, linuxnetlink.Message{Type: unix.RTM_NEWLINK, Payload: linuxTestLinkPayload(uint32(index), "e"+strconv.Itoa(index), unix.IFF_UP, unix.ARPHRD_ETHER)})
	}
	messages = append(messages, linuxnetlink.Message{Type: unix.RTM_NEWLINK, Payload: linuxTestLinkPayload(maximumPolicyInterfaces+1, "duplicate", unix.IFF_UP, unix.ARPHRD_ETHER)})
	if _, err := observeLinuxInterfaces(context.Background(), &scriptedLinuxNetlink{replies: []linuxnetlink.Reply{{Messages: messages}}}); err == nil {
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
		"unterminated":   linuxTestMarshalAttribute(make([]byte, unix.SizeofIfInfomsg), unix.IFLA_IFNAME, []byte("lo")),
		"embedded null":  linuxTestMarshalAttribute(make([]byte, unix.SizeofIfInfomsg), unix.IFLA_IFNAME, []byte{'l', 0, 'o', 0}),
		"duplicate name": linuxTestMarshalAttribute(valid, unix.IFLA_IFNAME, []byte("other\x00")),
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
	if _, err := observeLinuxInterfaces(context.Background(), &scriptedLinuxNetlink{replies: []linuxnetlink.Reply{{Messages: []linuxnetlink.Message{{Type: unix.RTM_NEWLINK, Payload: []byte{1}}}}}}); err == nil {
		t.Fatal("observeLinuxInterfaces(parse failure) error = nil")
	}
	messages := make([]linuxnetlink.Message, 0, maximumPolicyInterfaces+1)
	for index := 1; index <= maximumPolicyInterfaces; index++ {
		messages = append(messages, linuxnetlink.Message{Type: unix.RTM_NEWLINK, Payload: linuxTestLinkPayload(uint32(index), "e"+strconv.Itoa(index), unix.IFF_UP, unix.ARPHRD_ETHER)})
	}
	messages = append(messages, linuxnetlink.Message{Type: unix.RTM_NEWLINK, Payload: linuxTestLinkPayload(maximumPolicyInterfaces+1, "lo", unix.IFF_UP|unix.IFF_RUNNING|unix.IFF_LOOPBACK, unix.ARPHRD_LOOPBACK)})
	if _, err := observeLinuxInterfaces(context.Background(), &scriptedLinuxNetlink{replies: []linuxnetlink.Reply{{Messages: messages}}}); err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("observeLinuxInterfaces(loopback outside bound) error = %v", err)
	}
}

// TestLinuxRouteLookupBindsUIDProtocolAndPort verifies policy-routing selectors use native and network byte order correctly.
func TestLinuxRouteLookupBindsUIDProtocolAndPort(t *testing.T) {
	payload, err := marshalLinuxRouteLookup(testCandidate, 501, SocketRequirement{Transport: TransportUDP4, Port: 5353})
	if err != nil {
		t.Fatalf("marshalLinuxRouteLookup() error = %v", err)
	}
	if binary.NativeEndian.Uint32(payload[8:12]) != unix.RTM_F_LOOKUP_TABLE|unix.RTM_F_FIB_MATCH {
		t.Fatalf("route flags = %#x", binary.NativeEndian.Uint32(payload[8:12]))
	}
	attributes, err := linuxnetlink.ParseAttributes(payload[unix.SizeofRtMsg:])
	if err != nil {
		t.Fatalf("linuxnetlink.ParseAttributes() error = %v", err)
	}
	if got := binary.NativeEndian.Uint32(attributes[unix.RTA_UID][0].Payload); got != 501 {
		t.Fatalf("RTA_UID = %d, want 501", got)
	}
	if got := attributes[unix.RTA_IP_PROTO][0].Payload; !reflect.DeepEqual(got, []byte{unix.IPPROTO_UDP}) {
		t.Fatalf("RTA_IP_PROTO = %v", got)
	}
	if got := binary.BigEndian.Uint16(attributes[unix.RTA_DPORT][0].Payload); got != 5353 {
		t.Fatalf("RTA_DPORT = %d, want 5353", got)
	}

	generic, err := marshalLinuxRouteLookup(testCandidate, 501, SocketRequirement{})
	if err != nil {
		t.Fatalf("marshalLinuxRouteLookup(generic) error = %v", err)
	}
	genericAttributes, err := linuxnetlink.ParseAttributes(generic[unix.SizeofRtMsg:])
	if err != nil {
		t.Fatalf("linuxnetlink.ParseAttributes(generic) error = %v", err)
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
	noncanonical = linuxTestMarshalAttribute(noncanonical, unix.RTA_DST, []byte{127, 1, 0, 0})
	oif := make([]byte, 4)
	binary.NativeEndian.PutUint32(oif, 1)
	noncanonical = linuxTestMarshalAttribute(noncanonical, unix.RTA_OIF, oif)
	duplicateDestination := linuxTestMarshalAttribute(append([]byte(nil), valid...), unix.RTA_DST, []byte{127, 0, 0, 0})
	duplicateInterface := linuxTestMarshalAttribute(append([]byte(nil), valid...), unix.RTA_OIF, oif)
	duplicateGateway := linuxTestMarshalAttribute(append([]byte(nil), valid...), unix.RTA_GATEWAY, []byte{192, 0, 2, 1})
	duplicateGateway = linuxTestMarshalAttribute(duplicateGateway, unix.RTA_GATEWAY, []byte{192, 0, 2, 2})
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
	script := &scriptedLinuxNetlink{replies: []linuxnetlink.Reply{
		{Messages: []linuxnetlink.Message{{Type: unix.RTM_NEWROUTE, Payload: baseline}, {Type: unix.RTM_NEWROUTE, Payload: defaultRoute}}},
		{Messages: []linuxnetlink.Message{{Type: unix.RTM_NEWROUTE, Payload: baseline}}},
		{Messages: []linuxnetlink.Message{{Type: unix.RTM_NEWROUTE, Payload: baseline}}},
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
	if script.calls[0].completion != linuxnetlink.CompletionDump || script.calls[1].completion != linuxnetlink.CompletionData || script.calls[2].completion != linuxnetlink.CompletionData {
		t.Fatalf("route completions = %#v", script.calls)
	}

	different := linuxTestRoutePayload(netip.MustParsePrefix("0.0.0.0/0"), unix.RTN_UNICAST, 2, netip.MustParseAddr("192.0.2.1"), nil)
	script = &scriptedLinuxNetlink{replies: []linuxnetlink.Reply{
		{Messages: []linuxnetlink.Message{{Type: unix.RTM_NEWROUTE, Payload: baseline}, {Type: unix.RTM_NEWROUTE, Payload: defaultRoute}}},
		{Messages: []linuxnetlink.Message{{Type: unix.RTM_NEWROUTE, Payload: baseline}}},
		{Messages: []linuxnetlink.Message{{Type: unix.RTM_NEWROUTE, Payload: different}}},
	}}
	snapshot, err = observeLinuxRoutes(context.Background(), script, request, 1000, interfaces)
	if err != nil {
		t.Fatalf("observeLinuxRoutes(inconsistent) error = %v", err)
	}
	if snapshot.Complete || snapshot.Selected != nil {
		t.Fatalf("observeLinuxRoutes(inconsistent) = %#v", snapshot)
	}
}

// TestObserveLinuxRoutesPreservesIncompleteAndTruncatedEvidence covers unrepresentable facts and retention bounds.
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
	for name, payload := range map[string]linuxnetlink.Message{
		"unexpected type": {Type: unix.RTM_NEWLINK, Payload: baseline},
		"parse failure":   {Type: unix.RTM_NEWROUTE, Payload: []byte{1}},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := observeLinuxRoutes(context.Background(), &scriptedLinuxNetlink{replies: []linuxnetlink.Reply{{Messages: []linuxnetlink.Message{payload}}}}, request, 1000, interfaces)
			if err == nil {
				t.Fatal("observeLinuxRoutes() error = nil")
			}
		})
	}
	nonmatching := linuxTestRoutePayload(netip.MustParsePrefix("192.0.2.0/24"), unix.RTN_UNICAST, 2, netip.Addr{}, nil)
	unrepresentable := linuxTestRoutePayload(linuxOrdinaryLoopbackPrefix, unix.RTN_BLACKHOLE, 1, netip.Addr{}, nil)
	script := &scriptedLinuxNetlink{replies: []linuxnetlink.Reply{
		{Messages: []linuxnetlink.Message{{Type: unix.RTM_NEWROUTE, Payload: nonmatching}, {Type: unix.RTM_NEWROUTE, Payload: unrepresentable}, {Type: unix.RTM_NEWROUTE, Payload: baseline}}},
		{Messages: []linuxnetlink.Message{{Type: unix.RTM_NEWROUTE, Payload: baseline}}},
	}}
	snapshot, err := observeLinuxRoutes(context.Background(), script, request, 1000, interfaces)
	if err != nil || snapshot.Complete || snapshot.Selected == nil || snapshot.Truncated {
		t.Fatalf("observeLinuxRoutes(unrepresentable) = %#v, error %v", snapshot, err)
	}

	many := make([]linuxnetlink.Message, 0, maximumRouteFacts+1)
	defaultRoute := linuxTestRoutePayload(netip.MustParsePrefix("0.0.0.0/0"), unix.RTN_UNICAST, 2, netip.MustParseAddr("192.0.2.1"), nil)
	for index := 0; index < maximumRouteFacts; index++ {
		many = append(many, linuxnetlink.Message{Type: unix.RTM_NEWROUTE, Payload: defaultRoute})
	}
	many = append(many, linuxnetlink.Message{Type: unix.RTM_NEWROUTE, Payload: baseline})
	script = &scriptedLinuxNetlink{replies: []linuxnetlink.Reply{
		{Messages: many},
		{Messages: []linuxnetlink.Message{{Type: unix.RTM_NEWROUTE, Payload: baseline}}},
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
	if _, _, err := observeLinuxSelectedRoutes(context.Background(), &scriptedLinuxNetlink{errors: []error{sentinel}}, request, 1000, interfaces); !errors.Is(err, sentinel) {
		t.Fatalf("observeLinuxSelectedRoutes(exchange failure) error = %v", err)
	}
	for name, reply := range map[string]linuxnetlink.Reply{
		"missing message": {},
		"wrong type":      {Messages: []linuxnetlink.Message{{Type: unix.RTM_NEWLINK, Payload: baseline}}},
		"nonmatching":     {Messages: []linuxnetlink.Message{{Type: unix.RTM_NEWROUTE, Payload: linuxTestRoutePayload(netip.MustParsePrefix("192.0.2.0/24"), unix.RTN_UNICAST, 2, netip.Addr{}, nil)}}},
		"unrepresentable": {Messages: []linuxnetlink.Message{{Type: unix.RTM_NEWROUTE, Payload: linuxTestRoutePayload(linuxOrdinaryLoopbackPrefix, unix.RTN_BLACKHOLE, 1, netip.Addr{}, nil)}}},
	} {
		t.Run(name, func(t *testing.T) {
			selected, complete, err := observeLinuxSelectedRoutes(context.Background(), &scriptedLinuxNetlink{replies: []linuxnetlink.Reply{reply}}, request, 1000, interfaces)
			if err != nil || selected != nil || complete {
				t.Fatalf("observeLinuxSelectedRoutes() = %#v, complete %t error %v", selected, complete, err)
			}
		})
	}
	if _, _, err := observeLinuxSelectedRoutes(context.Background(), &scriptedLinuxNetlink{replies: []linuxnetlink.Reply{{Messages: []linuxnetlink.Message{{Type: unix.RTM_NEWROUTE, Payload: []byte{1}}}}}}, request, 1000, interfaces); err == nil {
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
	duplicateAttribute = linuxTestMarshalAttribute(duplicateAttribute, linuxInetDiagSKV6Only, []byte{1})
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

// TestObserveLinuxSocketsQueriesEachFamilyAndProtocol covers bounded aggregation across complete dumps.
func TestObserveLinuxSocketsQueriesEachFamilyAndProtocol(t *testing.T) {
	request, err := NewPreAssignmentRequest(testCandidate, []SocketRequirement{
		{Transport: TransportTCP4, Port: 443},
		{Transport: TransportUDP4, Port: 53},
	})
	if err != nil {
		t.Fatalf("NewPreAssignmentRequest() error = %v", err)
	}
	script := &scriptedLinuxNetlink{replies: []linuxnetlink.Reply{
		{Messages: []linuxnetlink.Message{{Type: unix.SOCK_DIAG_BY_FAMILY, Payload: linuxTestInetDiagPayload(unix.AF_INET, linuxTCPListenState, 443, netip.IPv4Unspecified(), nil)}}},
		{},
		{Messages: []linuxnetlink.Message{{Type: unix.SOCK_DIAG_BY_FAMILY, Payload: linuxTestInetDiagPayload(unix.AF_INET, 7, 53, testCandidate, nil)}}},
		{},
	}}
	snapshot, err := observeLinuxSockets(context.Background(), script, request)
	if err != nil {
		t.Fatalf("observeLinuxSockets() error = %v", err)
	}
	if !snapshot.Complete || snapshot.Truncated || len(snapshot.Endpoints) != 2 || len(script.calls) != 4 {
		t.Fatalf("observeLinuxSockets() = %#v, calls %d", snapshot, len(script.calls))
	}
	if script.calls[0].payload[0] != unix.AF_INET || script.calls[1].payload[0] != unix.AF_INET6 || script.calls[2].payload[1] != unix.IPPROTO_UDP {
		t.Fatalf("socket calls = %#v", script.calls)
	}
	for _, call := range script.calls {
		if call.completion != linuxnetlink.CompletionDump {
			t.Fatalf("socket completion = %d, want dump", call.completion)
		}
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
	for name, message := range map[string]linuxnetlink.Message{
		"wrong type": {Type: unix.RTM_NEWROUTE},
		"short":      {Type: unix.SOCK_DIAG_BY_FAMILY, Payload: []byte{1}},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := observeLinuxSockets(context.Background(), &scriptedLinuxNetlink{replies: []linuxnetlink.Reply{{Messages: []linuxnetlink.Message{message}}}}, request)
			if err == nil {
				t.Fatal("observeLinuxSockets() error = nil")
			}
		})
	}

	missingV6Only := linuxTestInetDiagPayload(unix.AF_INET6, linuxTCPListenState, 443, netip.IPv6Unspecified(), nil)
	snapshot, err := observeLinuxSockets(context.Background(), &scriptedLinuxNetlink{replies: []linuxnetlink.Reply{{}, {Messages: []linuxnetlink.Message{{Type: unix.SOCK_DIAG_BY_FAMILY, Payload: missingV6Only}}}}}, request)
	if err != nil || snapshot.Complete || snapshot.Truncated || len(snapshot.Endpoints) != 1 {
		t.Fatalf("observeLinuxSockets(missing SKV6ONLY) = %#v, error %v", snapshot, err)
	}

	messages := make([]linuxnetlink.Message, maximumSocketFacts+1)
	payload := linuxTestInetDiagPayload(unix.AF_INET, linuxTCPListenState, 443, netip.IPv4Unspecified(), nil)
	for index := range messages {
		messages[index] = linuxnetlink.Message{Type: unix.SOCK_DIAG_BY_FAMILY, Payload: payload}
	}
	snapshot, err = observeLinuxSockets(context.Background(), &scriptedLinuxNetlink{replies: []linuxnetlink.Reply{{Messages: messages}, {}}}, request)
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

// linuxTestMarshalAttribute appends one bounded fixture attribute whose encoding cannot fail.
func linuxTestMarshalAttribute(destination []byte, attributeType uint16, payload []byte) []byte {
	encoded, err := linuxnetlink.MarshalAttribute(destination, attributeType, payload)
	if err != nil {
		panic(err)
	}
	return encoded
}

// linuxTestLinkPayload constructs one raw ifinfomsg fixture with an IFLA_IFNAME attribute.
func linuxTestLinkPayload(index uint32, name string, flags uint32, hardware uint16) []byte {
	payload := make([]byte, unix.SizeofIfInfomsg)
	binary.NativeEndian.PutUint16(payload[2:4], hardware)
	binary.NativeEndian.PutUint32(payload[4:8], index)
	binary.NativeEndian.PutUint32(payload[8:12], flags)
	return linuxTestMarshalAttribute(payload, unix.IFLA_IFNAME, append([]byte(name), 0))
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
		payload = linuxTestMarshalAttribute(payload, unix.RTA_DST, address[:])
	}
	if interfaceIndex != 0 {
		encoded := make([]byte, 4)
		binary.NativeEndian.PutUint32(encoded, interfaceIndex)
		payload = linuxTestMarshalAttribute(payload, unix.RTA_OIF, encoded)
	}
	if gateway.IsValid() {
		encoded := gateway.As4()
		payload = linuxTestMarshalAttribute(payload, unix.RTA_GATEWAY, encoded[:])
	}
	for attributeType, value := range additional {
		payload = linuxTestMarshalAttribute(payload, attributeType, value)
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
		payload = linuxTestMarshalAttribute(payload, attributeType, value)
	}
	return payload
}
