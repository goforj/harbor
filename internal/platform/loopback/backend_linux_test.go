//go:build linux

package loopback

import (
	"context"
	"encoding/binary"
	"errors"
	"net/netip"
	"reflect"
	"strings"
	"testing"

	"github.com/goforj/harbor/internal/platform/linuxnetlink"
	"golang.org/x/sys/unix"
)

// linuxTestExchangeCall records one route request without retaining caller-owned payload memory.
type linuxTestExchangeCall struct {
	messageType uint16
	flags       uint16
	payload     []byte
	completion  linuxnetlink.Completion
}

// linuxTestRouteClient supplies deterministic route replies and close failures.
type linuxTestRouteClient struct {
	replies  []linuxnetlink.Reply
	errors   []error
	calls    []linuxTestExchangeCall
	closed   int
	closeErr error
}

// Exchange records the exact transaction before consuming its scripted result.
func (client *linuxTestRouteClient) Exchange(_ context.Context, messageType uint16, flags uint16, payload []byte, completion linuxnetlink.Completion) (linuxnetlink.Reply, error) {
	client.calls = append(client.calls, linuxTestExchangeCall{messageType: messageType, flags: flags, payload: append([]byte(nil), payload...), completion: completion})
	index := len(client.calls) - 1
	if index < len(client.errors) && client.errors[index] != nil {
		return linuxnetlink.Reply{}, client.errors[index]
	}
	if index >= len(client.replies) {
		return linuxnetlink.Reply{}, errors.New("unexpected Linux route fixture exchange")
	}
	return client.replies[index], nil
}

// Close records lifecycle cleanup while preserving an injected failure.
func (client *linuxTestRouteClient) Close() error {
	client.closed++
	return client.closeErr
}

// linuxTestRouteOpener returns scripted clients in backend call order.
type linuxTestRouteOpener struct {
	clients []linuxRouteClient
	error   error
	calls   int
}

// open returns the next client or the injected setup failure.
func (opener *linuxTestRouteOpener) open() (linuxRouteClient, error) {
	opener.calls++
	if opener.error != nil {
		return nil, opener.error
	}
	if opener.calls > len(opener.clients) {
		return nil, errors.New("unexpected Linux route fixture open")
	}
	return opener.clients[opener.calls-1], nil
}

// TestLinuxBackendMapsCompleteNativeFacts proves platform facts retain every exact-address attribute.
func TestLinuxBackendMapsCompleteNativeFacts(t *testing.T) {
	links := linuxTestLinkReply(
		linuxTestLinkPayload(1, "lo", unix.IFF_UP|unix.IFF_RUNNING|unix.IFF_LOOPBACK, unix.ARPHRD_LOOPBACK),
		linuxTestLinkPayload(2, "eth0", unix.IFF_UP|unix.IFF_RUNNING, unix.ARPHRD_ETHER),
	)
	addresses := linuxTestAddressReply(linuxTestAddressPayload(testAddress, 32, unix.RT_SCOPE_HOST, 1, unix.IFA_F_PERMANENT, true, true))
	interfaceClient := &linuxTestRouteClient{replies: []linuxnetlink.Reply{links}}
	addressClient := &linuxTestRouteClient{replies: []linuxnetlink.Reply{links, addresses}}
	opener := &linuxTestRouteOpener{clients: []linuxRouteClient{interfaceClient, addressClient}}
	backend := &platformBackend{openRoute: opener.open}

	interfaces, err := backend.interfaces(context.Background())
	if err != nil {
		t.Fatalf("interfaces() error = %v", err)
	}
	if len(interfaces) != 2 || interfaces[0] != (InterfaceFact{Name: "lo", Index: 1, Kind: InterfaceKindLinuxNative, NativeLoopback: true}) {
		t.Fatalf("interfaces() = %#v", interfaces)
	}
	assignments, err := backend.assignments(context.Background(), testAddress)
	if err != nil {
		t.Fatalf("assignments() error = %v", err)
	}
	if len(assignments) != 1 || assignments[0].Linux == nil || !exactLinuxAttributes(assignments[0].Linux, "lo") {
		t.Fatalf("assignments() = %#v", assignments)
	}
	if assignments[0].InterfaceName != "lo" || assignments[0].InterfaceIndex != 1 {
		t.Fatalf("assignment identity = %#v", assignments[0])
	}
	if interfaceClient.closed != 1 || addressClient.closed != 1 {
		t.Fatalf("client closes = %d and %d", interfaceClient.closed, addressClient.closed)
	}
}

// TestLinuxLinkCodecRejectsAmbiguousIdentity covers fixed-header, name, and duplicate table failures.
func TestLinuxLinkCodecRejectsAmbiguousIdentity(t *testing.T) {
	valid := linuxTestLinkPayload(1, "lo", unix.IFF_UP|unix.IFF_RUNNING|unix.IFF_LOOPBACK, unix.ARPHRD_LOOPBACK)
	if link, err := parseLinuxLink(valid); err != nil || !link.fact.NativeLoopback {
		t.Fatalf("parseLinuxLink() = %#v, %v", link, err)
	}
	tests := map[string][]byte{
		"short header": valid[:unix.SizeofIfInfomsg-1],
		"wrong family": append([]byte{unix.AF_INET}, valid[1:]...),
		"zero index":   linuxTestLinkPayload(0, "lo", unix.IFF_LOOPBACK, unix.ARPHRD_LOOPBACK),
		"missing name": make([]byte, unix.SizeofIfInfomsg),
	}
	unterminated := make([]byte, unix.SizeofIfInfomsg)
	binary.NativeEndian.PutUint32(unterminated[4:8], 1)
	unterminated = linuxTestMarshalAttribute(unterminated, unix.IFLA_IFNAME, []byte("lo"))
	tests["unterminated name"] = unterminated
	embedded := make([]byte, unix.SizeofIfInfomsg)
	binary.NativeEndian.PutUint32(embedded[4:8], 1)
	embedded = linuxTestMarshalAttribute(embedded, unix.IFLA_IFNAME, []byte{'l', 0, 'o', 0})
	tests["embedded terminator"] = embedded
	for name, payload := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := parseLinuxLink(payload); err == nil {
				t.Fatal("parseLinuxLink() error = nil")
			}
		})
	}

	for name, reply := range map[string]linuxnetlink.Reply{
		"wrong message": {Messages: []linuxnetlink.Message{{Type: unix.RTM_NEWADDR, Payload: valid}}},
		"duplicate index": linuxTestLinkReply(
			linuxTestLinkPayload(1, "lo", unix.IFF_LOOPBACK, unix.ARPHRD_LOOPBACK),
			linuxTestLinkPayload(1, "other", unix.IFF_UP, unix.ARPHRD_ETHER),
		),
		"duplicate name": linuxTestLinkReply(
			linuxTestLinkPayload(1, "same", unix.IFF_LOOPBACK, unix.ARPHRD_LOOPBACK),
			linuxTestLinkPayload(2, "same", unix.IFF_UP, unix.ARPHRD_ETHER),
		),
	} {
		t.Run(name, func(t *testing.T) {
			client := &linuxTestRouteClient{replies: []linuxnetlink.Reply{reply}}
			if _, err := observeLinuxLinks(context.Background(), client); err == nil {
				t.Fatal("observeLinuxLinks() error = nil")
			}
		})
	}
}

// TestLinuxAddressCodecPreservesConflicts covers fallback, attributes, bounds, and malformed records.
func TestLinuxAddressCodecPreservesConflicts(t *testing.T) {
	names := map[int]string{1: "lo"}
	valid := linuxTestAddressPayload(testAddress, 32, unix.RT_SCOPE_HOST, 1, unix.IFA_F_PERMANENT, true, true)
	assignment, matches, err := parseLinuxAddress(valid, testAddress, names)
	if err != nil || !matches || assignment.scope != unix.RT_SCOPE_HOST || !assignment.cacheInfoPresent || !assignment.addressMatches {
		t.Fatalf("parseLinuxAddress() = %#v, %t, %v", assignment, matches, err)
	}
	if assignment.validLifetime != linuxInfiniteLifetime || assignment.preferredLifetime != linuxInfiniteLifetime {
		t.Fatalf("lifetimes = %d/%d", assignment.validLifetime, assignment.preferredLifetime)
	}

	nonmatching := linuxTestAddressPayload(netip.MustParseAddr("127.77.0.11"), 32, unix.RT_SCOPE_HOST, 1, unix.IFA_F_PERMANENT, true, true)
	if _, matches, err := parseLinuxAddress(nonmatching, testAddress, names); err != nil || matches {
		t.Fatalf("parseLinuxAddress(nonmatching) matches = %t, error %v", matches, err)
	}

	localOnly := linuxTestAddressPayload(testAddress, 32, unix.RT_SCOPE_HOST, 1, unix.IFA_F_PERMANENT, false, true)
	assignment, matches, err = parseLinuxAddress(localOnly, testAddress, names)
	if err != nil || !matches || assignment.addressMatches {
		t.Fatalf("parseLinuxAddress(local-only) = %#v, %t, %v", assignment, matches, err)
	}
	addressOnly := linuxTestBareAddressPayload(1)
	addressOnly = linuxTestMarshalAttribute(addressOnly, unix.IFA_ADDRESS, testAddress.AsSlice())
	assignment, matches, err = parseLinuxAddress(addressOnly, testAddress, names)
	if err != nil || !matches || assignment.addressMatches {
		t.Fatalf("parseLinuxAddress(address-only) = %#v, %t, %v", assignment, matches, err)
	}
	otherAddress := netip.MustParseAddr("127.77.0.11")
	peerTarget := linuxTestAddressPayloadWithAttribute(valid, unix.IFA_LOCAL, otherAddress.AsSlice())
	assignment, matches, err = parseLinuxAddress(peerTarget, testAddress, names)
	if err != nil || !matches || assignment.fact.Address != testAddress || assignment.addressMatches {
		t.Fatalf("parseLinuxAddress(peer target) = %#v, %t, %v", assignment, matches, err)
	}
	peerMismatch := linuxTestAddressPayloadWithAttribute(valid, unix.IFA_ADDRESS, otherAddress.AsSlice())
	assignment, matches, err = parseLinuxAddress(peerMismatch, testAddress, names)
	if err != nil || !matches || assignment.addressMatches {
		t.Fatalf("parseLinuxAddress(peer mismatch) = %#v, %t, %v", assignment, matches, err)
	}
	customLabel := linuxTestAddressPayloadWithAttribute(valid, unix.IFA_LABEL, []byte("lo:foreign\x00"))
	assignment, matches, err = parseLinuxAddress(customLabel, testAddress, names)
	if err != nil || !matches || assignment.label != "lo:foreign" {
		t.Fatalf("parseLinuxAddress(custom label) = %#v, %t, %v", assignment, matches, err)
	}
	missingLabel := linuxTestAddressPayloadWithoutAttribute(valid, unix.IFA_LABEL)
	assignment, matches, err = parseLinuxAddress(missingLabel, testAddress, names)
	if err != nil || !matches || assignment.label != "" {
		t.Fatalf("parseLinuxAddress(missing label) = %#v, %t, %v", assignment, matches, err)
	}
	withBroadcast := linuxTestMarshalAttribute(valid, unix.IFA_BROADCAST, []byte{127, 255, 255, 255})
	assignment, matches, err = parseLinuxAddress(withBroadcast, testAddress, names)
	if err != nil || !matches || len(assignment.additional) != 64 {
		t.Fatalf("parseLinuxAddress(additional semantics) = %#v, %t, %v", assignment, matches, err)
	}

	tests := map[string][]byte{
		"short header":        valid[:unix.SizeofIfAddrmsg-1],
		"wrong family":        append([]byte{unix.AF_INET6}, valid[1:]...),
		"zero index":          linuxTestAddressPayload(testAddress, 32, unix.RT_SCOPE_HOST, 0, unix.IFA_F_PERMANENT, true, true),
		"unobserved index":    linuxTestAddressPayload(testAddress, 32, unix.RT_SCOPE_HOST, 2, unix.IFA_F_PERMANENT, true, true),
		"missing addresses":   linuxTestBareAddressPayload(1),
		"short flags":         linuxTestAddressPayloadWithAttribute(valid, unix.IFA_FLAGS, []byte{1}),
		"short cache info":    linuxTestAddressPayloadWithAttribute(valid, unix.IFA_CACHEINFO, []byte{1}),
		"duplicate local":     linuxTestMarshalAttribute(valid, unix.IFA_LOCAL, testAddress.AsSlice()),
		"duplicate peer":      linuxTestMarshalAttribute(valid, unix.IFA_ADDRESS, testAddress.AsSlice()),
		"duplicate flags":     linuxTestMarshalAttribute(valid, unix.IFA_FLAGS, make([]byte, 4)),
		"duplicate cacheinfo": linuxTestMarshalAttribute(valid, unix.IFA_CACHEINFO, make([]byte, 16)),
		"duplicate label":     linuxTestMarshalAttribute(valid, unix.IFA_LABEL, []byte{'l', 'o', 0}),
		"unterminated label":  linuxTestAddressPayloadWithAttribute(valid, unix.IFA_LABEL, []byte{'l', 'o'}),
		"embedded label":      linuxTestAddressPayloadWithAttribute(valid, unix.IFA_LABEL, []byte{'l', 0, 'o', 0}),
	}
	for name, payload := range tests {
		t.Run(name, func(t *testing.T) {
			if _, _, err := parseLinuxAddress(payload, testAddress, names); err == nil {
				t.Fatal("parseLinuxAddress() error = nil")
			}
		})
	}
}

// TestLinuxAddressObservationEnforcesMessageAndFactBounds proves incomplete dumps never authorize an address.
func TestLinuxAddressObservationEnforcesMessageAndFactBounds(t *testing.T) {
	links := []linuxLink{{fact: InterfaceFact{Name: "lo", Index: 1}}}
	wrong := &linuxTestRouteClient{replies: []linuxnetlink.Reply{{Messages: []linuxnetlink.Message{{Type: unix.RTM_NEWLINK}}}}}
	if _, err := observeLinuxAddresses(context.Background(), wrong, testAddress, links); err == nil {
		t.Fatal("observeLinuxAddresses(wrong message) error = nil")
	}
	many := make([]linuxnetlink.Message, maximumAssignmentFacts+1)
	for index := range many {
		many[index] = linuxnetlink.Message{Type: unix.RTM_NEWADDR, Payload: linuxTestAddressPayload(testAddress, 32, unix.RT_SCOPE_HOST, 1, unix.IFA_F_PERMANENT, true, true)}
	}
	client := &linuxTestRouteClient{replies: []linuxnetlink.Reply{{Messages: many}}}
	if _, err := observeLinuxAddresses(context.Background(), client, testAddress, links); err == nil {
		t.Fatal("observeLinuxAddresses(bound) error = nil")
	}
}

// TestLinuxMutationsUseRevalidatedIndexAndExactACK proves no interface name reaches the kernel effect.
func TestLinuxMutationsUseRevalidatedIndexAndExactACK(t *testing.T) {
	link := linuxTestLinkPayload(testLoopback.Index, testLoopback.Name, unix.IFF_UP|unix.IFF_RUNNING|unix.IFF_LOOPBACK, unix.ARPHRD_LOOPBACK)
	for _, test := range []struct {
		name        string
		operation   func(*platformBackend, context.Context, InterfaceFact, netip.Prefix) error
		messageType uint16
		flags       uint16
	}{
		{name: "ensure", operation: (*platformBackend).ensure, messageType: unix.RTM_NEWADDR, flags: unix.NLM_F_CREATE | unix.NLM_F_EXCL},
		{name: "release", operation: (*platformBackend).release, messageType: unix.RTM_DELADDR},
	} {
		t.Run(test.name, func(t *testing.T) {
			replies := []linuxnetlink.Reply{{Messages: []linuxnetlink.Message{{Type: unix.RTM_NEWLINK, Payload: link}}}}
			if test.messageType == unix.RTM_DELADDR {
				replies = append(replies, linuxTestExactAddressReply(testLoopback))
			}
			replies = append(replies, linuxnetlink.Reply{})
			client := &linuxTestRouteClient{replies: replies}
			backend := &platformBackend{openRoute: func() (linuxRouteClient, error) { return client, nil }}
			prefix := netip.PrefixFrom(testAddress, 32)
			if err := test.operation(backend, context.Background(), testLoopback, prefix); err != nil {
				t.Fatalf("mutation error = %v", err)
			}
			wantCalls := 2
			if test.messageType == unix.RTM_DELADDR {
				wantCalls = 3
				if client.calls[1].messageType != unix.RTM_GETADDR || client.calls[1].completion != linuxnetlink.CompletionDump {
					t.Fatalf("address revalidation call = %#v", client.calls[1])
				}
			}
			if len(client.calls) != wantCalls || client.calls[0].messageType != unix.RTM_GETLINK || client.calls[0].completion != linuxnetlink.CompletionData {
				t.Fatalf("revalidation calls = %#v", client.calls)
			}
			mutation := client.calls[len(client.calls)-1]
			if mutation.messageType != test.messageType || mutation.flags != test.flags || mutation.completion != linuxnetlink.CompletionAck {
				t.Fatalf("mutation call = %#v", mutation)
			}
			if binary.NativeEndian.Uint32(mutation.payload[4:8]) != uint32(testLoopback.Index) || mutation.payload[1] != 32 || mutation.payload[3] != unix.RT_SCOPE_HOST {
				t.Fatalf("mutation payload header = %v", mutation.payload[:unix.SizeofIfAddrmsg])
			}
			if client.closed != 1 {
				t.Fatalf("client closes = %d", client.closed)
			}
		})
	}
}

// TestLinuxMutationRaceErrorsPreserveOwnershipBoundaries distinguishes adoption from final-state-safe deletion.
func TestLinuxMutationRaceErrorsPreserveOwnershipBoundaries(t *testing.T) {
	link := linuxTestLinkPayload(testLoopback.Index, testLoopback.Name, unix.IFF_UP|unix.IFF_RUNNING|unix.IFF_LOOPBACK, unix.ARPHRD_LOOPBACK)
	for name, mutationErr := range map[string]error{"delete missing": unix.ENOENT, "address unavailable": unix.EADDRNOTAVAIL} {
		t.Run(name, func(t *testing.T) {
			client := &linuxTestRouteClient{
				replies: []linuxnetlink.Reply{{Messages: []linuxnetlink.Message{{Type: unix.RTM_NEWLINK, Payload: link}}}, linuxTestExactAddressReply(testLoopback), {}},
				errors:  []error{nil, nil, mutationErr},
			}
			backend := &platformBackend{openRoute: func() (linuxRouteClient, error) { return client, nil }}
			prefix := netip.PrefixFrom(testAddress, 32)
			err := backend.release(context.Background(), testLoopback, prefix)
			if err != nil {
				t.Fatalf("race-compatible mutation error = %v", err)
			}
		})
	}
	client := &linuxTestRouteClient{
		replies: []linuxnetlink.Reply{{Messages: []linuxnetlink.Message{{Type: unix.RTM_NEWLINK, Payload: link}}}, {}},
		errors:  []error{nil, unix.EEXIST},
	}
	backend := &platformBackend{openRoute: func() (linuxRouteClient, error) { return client, nil }}
	if err := backend.ensure(context.Background(), testLoopback, netip.PrefixFrom(testAddress, 32)); !errors.Is(err, unix.EEXIST) {
		t.Fatalf("ensure(EEXIST) error = %v", err)
	}
}

// TestLinuxMutationRejectsIdentityDriftAndFailures covers admission, setup, close, and revalidation errors.
func TestLinuxMutationRejectsIdentityDriftAndFailures(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	backend := &platformBackend{}
	if err := backend.ensure(canceled, testLoopback, netip.PrefixFrom(testAddress, 32)); !errors.Is(err, context.Canceled) {
		t.Fatalf("ensure(canceled) error = %v", err)
	}
	if err := backend.release(canceled, testLoopback, netip.PrefixFrom(testAddress, 32)); !errors.Is(err, context.Canceled) {
		t.Fatalf("release(canceled) error = %v", err)
	}
	if err := validateLinuxMutation(InterfaceFact{}, netip.PrefixFrom(testAddress, 32)); err == nil {
		t.Fatal("validateLinuxMutation(interface) error = nil")
	}
	invalidName := testLoopback
	invalidName.Name = strings.Repeat("x", maximumLinuxLabel+1)
	if err := validateLinuxMutation(invalidName, netip.PrefixFrom(testAddress, 32)); err == nil {
		t.Fatal("validateLinuxMutation(long name) error = nil")
	}
	invalidName.Name = "lo\x00foreign"
	if err := validateLinuxMutation(invalidName, netip.PrefixFrom(testAddress, 32)); err == nil {
		t.Fatal("validateLinuxMutation(nul name) error = nil")
	}
	if err := validateLinuxMutation(testLoopback, netip.MustParsePrefix("127.0.0.0/8")); err == nil {
		t.Fatal("validateLinuxMutation(prefix) error = nil")
	}

	sentinel := errors.New("Linux route fixture failure")
	opener := &linuxTestRouteOpener{error: sentinel}
	backend = &platformBackend{openRoute: opener.open}
	if _, err := backend.interfaces(context.Background()); !errors.Is(err, sentinel) {
		t.Fatalf("interfaces(open failure) error = %v", err)
	}
	if _, err := backend.assignments(context.Background(), testAddress); !errors.Is(err, sentinel) {
		t.Fatalf("assignments(open failure) error = %v", err)
	}

	changed := linuxTestLinkPayload(testLoopback.Index, "replaced", unix.IFF_UP|unix.IFF_RUNNING|unix.IFF_LOOPBACK, unix.ARPHRD_LOOPBACK)
	client := &linuxTestRouteClient{replies: []linuxnetlink.Reply{{Messages: []linuxnetlink.Message{{Type: unix.RTM_NEWLINK, Payload: changed}}}}}
	backend = &platformBackend{openRoute: func() (linuxRouteClient, error) { return client, nil }}
	if err := backend.ensure(context.Background(), testLoopback, netip.PrefixFrom(testAddress, 32)); err == nil {
		t.Fatal("ensure(identity drift) error = nil")
	}
	if len(client.calls) != 1 || client.closed != 1 {
		t.Fatalf("identity drift calls/closes = %d/%d", len(client.calls), client.closed)
	}

	closeClient := &linuxTestRouteClient{replies: []linuxnetlink.Reply{linuxTestLinkReply()}, closeErr: sentinel}
	backend = &platformBackend{openRoute: func() (linuxRouteClient, error) { return closeClient, nil }}
	if _, err := backend.interfaces(context.Background()); !errors.Is(err, sentinel) {
		t.Fatalf("interfaces(close failure) error = %v", err)
	}
}

// TestLinuxBackendFailureMatrix preserves cancellation, cleanup, and mutation error distinctions.
func TestLinuxBackendFailureMatrix(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	backend := &platformBackend{}
	if _, err := backend.interfaces(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("interfaces(canceled) error = %v", err)
	}
	if _, err := backend.assignments(canceled, testAddress); !errors.Is(err, context.Canceled) {
		t.Fatalf("assignments(canceled) error = %v", err)
	}

	sentinel := errors.New("Linux backend fixture failure")
	for name, client := range map[string]*linuxTestRouteClient{
		"link observation":    {errors: []error{sentinel}},
		"address observation": {replies: []linuxnetlink.Reply{linuxTestLinkReply()}, errors: []error{nil, sentinel}},
	} {
		t.Run(name, func(t *testing.T) {
			backend := &platformBackend{openRoute: func() (linuxRouteClient, error) { return client, nil }}
			if _, err := backend.assignments(context.Background(), testAddress); !errors.Is(err, sentinel) {
				t.Fatalf("assignments() error = %v", err)
			}
			if client.closed != 1 {
				t.Fatalf("client closes = %d", client.closed)
			}
		})
	}

	opener := &linuxTestRouteOpener{error: sentinel}
	backend = &platformBackend{openRoute: opener.open}
	for name, operation := range map[string]func(context.Context, InterfaceFact, netip.Prefix) error{
		"ensure":  backend.ensure,
		"release": backend.release,
	} {
		t.Run(name+" open", func(t *testing.T) {
			if err := operation(context.Background(), testLoopback, netip.PrefixFrom(testAddress, 32)); !errors.Is(err, sentinel) {
				t.Fatalf("mutation error = %v", err)
			}
		})
	}
	if err := backend.ensure(context.Background(), InterfaceFact{}, netip.PrefixFrom(testAddress, 32)); err == nil {
		t.Fatal("ensure(invalid interface) error = nil")
	}
	if err := backend.release(context.Background(), InterfaceFact{}, netip.PrefixFrom(testAddress, 32)); err == nil {
		t.Fatal("release(invalid interface) error = nil")
	}

	link := linuxTestLinkPayload(testLoopback.Index, testLoopback.Name, unix.IFF_UP|unix.IFF_RUNNING|unix.IFF_LOOPBACK, unix.ARPHRD_LOOPBACK)
	for name, test := range map[string]struct {
		operation func(*platformBackend, context.Context, InterfaceFact, netip.Prefix) error
		error     error
	}{
		"ensure mutation":  {operation: (*platformBackend).ensure, error: unix.EPERM},
		"release mutation": {operation: (*platformBackend).release, error: unix.EPERM},
		"ensure close":     {operation: (*platformBackend).ensure},
		"release close":    {operation: (*platformBackend).release},
	} {
		t.Run(name, func(t *testing.T) {
			replies := []linuxnetlink.Reply{{Messages: []linuxnetlink.Message{{Type: unix.RTM_NEWLINK, Payload: link}}}}
			errorsByCall := []error{nil}
			if strings.HasPrefix(name, "release") {
				replies = append(replies, linuxTestExactAddressReply(testLoopback))
				errorsByCall = append(errorsByCall, nil)
			}
			replies = append(replies, linuxnetlink.Reply{})
			errorsByCall = append(errorsByCall, test.error)
			client := &linuxTestRouteClient{
				replies:  replies,
				errors:   errorsByCall,
				closeErr: sentinel,
			}
			backend := &platformBackend{openRoute: func() (linuxRouteClient, error) { return client, nil }}
			err := test.operation(backend, context.Background(), testLoopback, netip.PrefixFrom(testAddress, 32))
			if !errors.Is(err, sentinel) || test.error != nil && !errors.Is(err, test.error) {
				t.Fatalf("mutation error = %v", err)
			}
		})
	}
}

// TestLinuxNativeObserverFailureMatrix exercises bounds, cancellation, codecs, and revalidation failures.
func TestLinuxNativeObserverFailureMatrix(t *testing.T) {
	sentinel := errors.New("Linux observer fixture failure")
	client := &linuxTestRouteClient{errors: []error{sentinel}}
	if _, err := observeLinuxLinks(context.Background(), client); !errors.Is(err, sentinel) {
		t.Fatalf("observeLinuxLinks(exchange) error = %v", err)
	}
	many := make([]linuxnetlink.Message, maximumInterfaceFacts+1)
	client = &linuxTestRouteClient{replies: []linuxnetlink.Reply{{Messages: many}}}
	if _, err := observeLinuxLinks(context.Background(), client); err == nil {
		t.Fatal("observeLinuxLinks(bound) error = nil")
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	client = &linuxTestRouteClient{replies: []linuxnetlink.Reply{linuxTestLinkReply(linuxTestLinkPayload(1, "lo", 0, unix.ARPHRD_LOOPBACK))}}
	if _, err := observeLinuxLinks(canceled, client); !errors.Is(err, context.Canceled) {
		t.Fatalf("observeLinuxLinks(canceled) error = %v", err)
	}
	client = &linuxTestRouteClient{replies: []linuxnetlink.Reply{linuxTestLinkReply([]byte{1})}}
	if _, err := observeLinuxLinks(context.Background(), client); err == nil {
		t.Fatal("observeLinuxLinks(codec) error = nil")
	}

	longName := linuxTestLinkPayload(1, string(make([]byte, unix.IFNAMSIZ)), 0, unix.ARPHRD_ETHER)
	if _, err := parseLinuxLink(longName); err == nil {
		t.Fatal("parseLinuxLink(long name) error = nil")
	}
	duplicateName := linuxTestLinkPayload(1, "lo", 0, unix.ARPHRD_LOOPBACK)
	duplicateName = linuxTestMarshalAttribute(duplicateName, unix.IFLA_IFNAME, []byte{'l', 'o', 0})
	if _, err := parseLinuxLink(duplicateName); err == nil {
		t.Fatal("parseLinuxLink(duplicate name) error = nil")
	}

	links := []linuxLink{{fact: InterfaceFact{Name: "lo", Index: 1}}}
	client = &linuxTestRouteClient{errors: []error{sentinel}}
	if _, err := observeLinuxAddresses(context.Background(), client, testAddress, links); !errors.Is(err, sentinel) {
		t.Fatalf("observeLinuxAddresses(exchange) error = %v", err)
	}
	client = &linuxTestRouteClient{replies: []linuxnetlink.Reply{linuxTestAddressReply(linuxTestAddressPayload(testAddress, 32, unix.RT_SCOPE_HOST, 1, unix.IFA_F_PERMANENT, true, true))}}
	if _, err := observeLinuxAddresses(canceled, client, testAddress, links); !errors.Is(err, context.Canceled) {
		t.Fatalf("observeLinuxAddresses(canceled) error = %v", err)
	}
	client = &linuxTestRouteClient{replies: []linuxnetlink.Reply{linuxTestAddressReply([]byte{1})}}
	if _, err := observeLinuxAddresses(context.Background(), client, testAddress, links); err == nil {
		t.Fatal("observeLinuxAddresses(codec) error = nil")
	}

	client = &linuxTestRouteClient{errors: []error{sentinel}}
	if err := revalidateLinuxLoopback(context.Background(), client, testLoopback); !errors.Is(err, sentinel) {
		t.Fatalf("revalidateLinuxLoopback(exchange) error = %v", err)
	}
	client = &linuxTestRouteClient{replies: []linuxnetlink.Reply{{}}}
	if err := revalidateLinuxLoopback(context.Background(), client, testLoopback); err == nil {
		t.Fatal("revalidateLinuxLoopback(empty) error = nil")
	}
	client = &linuxTestRouteClient{replies: []linuxnetlink.Reply{{Messages: []linuxnetlink.Message{{Type: unix.RTM_NEWLINK, Payload: []byte{1}}}}}}
	if err := revalidateLinuxLoopback(context.Background(), client, testLoopback); err == nil {
		t.Fatal("revalidateLinuxLoopback(codec) error = nil")
	}
}

// TestLinuxReleaseRevalidationRejectsAssignmentDrift keeps DELADDR behind a fresh exact-shape check.
func TestLinuxReleaseRevalidationRejectsAssignmentDrift(t *testing.T) {
	link := linuxTestLinkPayload(testLoopback.Index, testLoopback.Name, unix.IFF_UP|unix.IFF_RUNNING|unix.IFF_LOOPBACK, unix.ARPHRD_LOOPBACK)
	exact := linuxTestAddressPayloadWithLabel(testAddress, 32, unix.RT_SCOPE_HOST, testLoopback.Index, unix.IFA_F_PERMANENT, true, true, testLoopback.Name)
	finite := append([]byte(nil), exact...)
	finite = linuxTestAddressPayloadWithAttribute(finite, unix.IFA_CACHEINFO, make([]byte, 16))
	customLabel := linuxTestAddressPayloadWithAttribute(exact, unix.IFA_LABEL, []byte("native:foreign\x00"))
	peerOnly := linuxTestAddressPayloadWithAttribute(exact, unix.IFA_LOCAL, netip.MustParseAddr("127.77.0.11").AsSlice())
	additional := linuxTestMarshalAttribute(exact, unix.IFA_BROADCAST, []byte{127, 255, 255, 255})
	tests := map[string]linuxnetlink.Reply{
		"absent":              {},
		"ambiguous":           linuxTestAddressReply(exact, exact),
		"finite lifetime":     linuxTestAddressReply(finite),
		"custom label":        linuxTestAddressReply(customLabel),
		"peer target":         linuxTestAddressReply(peerOnly),
		"additional semantic": linuxTestAddressReply(additional),
	}
	for name, addressReply := range tests {
		t.Run(name, func(t *testing.T) {
			client := &linuxTestRouteClient{replies: []linuxnetlink.Reply{
				{Messages: []linuxnetlink.Message{{Type: unix.RTM_NEWLINK, Payload: link}}},
				addressReply,
			}}
			backend := &platformBackend{openRoute: func() (linuxRouteClient, error) { return client, nil }}
			if err := backend.release(context.Background(), testLoopback, netip.PrefixFrom(testAddress, 32)); err == nil {
				t.Fatal("release(drift) error = nil")
			}
			if len(client.calls) != 2 || client.calls[1].messageType != unix.RTM_GETADDR || client.closed != 1 {
				t.Fatalf("release(drift) calls/closes = %#v/%d", client.calls, client.closed)
			}
		})
	}
}

// TestLinuxMutationPayloadCarriesInfiniteLifetime verifies create and delete use only their intended attributes.
func TestLinuxMutationPayloadCarriesInfiniteLifetime(t *testing.T) {
	create, err := marshalLinuxAddressMutation(testLoopback, netip.PrefixFrom(testAddress, 32), true)
	if err != nil {
		t.Fatalf("marshalLinuxAddressMutation(create) error = %v", err)
	}
	attributes, err := linuxnetlink.ParseAttributes(create[unix.SizeofIfAddrmsg:])
	if err != nil {
		t.Fatalf("ParseAttributes(create) error = %v", err)
	}
	cache, present, err := linuxnetlink.OneAttribute(attributes, unix.IFA_CACHEINFO)
	if err != nil || !present || len(cache) != 16 {
		t.Fatalf("create cache info = %v, %t, %v", cache, present, err)
	}
	if binary.NativeEndian.Uint32(cache[0:4]) != linuxInfiniteLifetime || binary.NativeEndian.Uint32(cache[4:8]) != linuxInfiniteLifetime {
		t.Fatalf("create cache info = %v", cache)
	}
	local, _, _ := linuxnetlink.OneAttribute(attributes, unix.IFA_LOCAL)
	peer, _, _ := linuxnetlink.OneAttribute(attributes, unix.IFA_ADDRESS)
	label, _, _ := linuxnetlink.OneAttribute(attributes, unix.IFA_LABEL)
	if !reflect.DeepEqual(local, testAddress.AsSlice()) || !reflect.DeepEqual(peer, testAddress.AsSlice()) {
		t.Fatalf("create addresses = %v/%v", local, peer)
	}
	if string(label) != testLoopback.Name+"\x00" {
		t.Fatalf("create label = %q", label)
	}

	remove, err := marshalLinuxAddressMutation(testLoopback, netip.PrefixFrom(testAddress, 32), false)
	if err != nil {
		t.Fatalf("marshalLinuxAddressMutation(delete) error = %v", err)
	}
	attributes, err = linuxnetlink.ParseAttributes(remove[unix.SizeofIfAddrmsg:])
	if err != nil {
		t.Fatalf("ParseAttributes(delete) error = %v", err)
	}
	if _, present, err := linuxnetlink.OneAttribute(attributes, unix.IFA_CACHEINFO); err != nil || present {
		t.Fatalf("delete cache info present = %t, error %v", present, err)
	}
	label, present, err = linuxnetlink.OneAttribute(attributes, unix.IFA_LABEL)
	if err != nil || !present || string(label) != testLoopback.Name+"\x00" {
		t.Fatalf("delete label = %q, %t, %v", label, present, err)
	}
}

// TestLinuxAddressScopeBoundsKernelValues keeps future scope values explicit rather than accidentally exact.
func TestLinuxAddressScopeBoundsKernelValues(t *testing.T) {
	tests := map[uint8]LinuxAddressScope{
		unix.RT_SCOPE_UNIVERSE: LinuxAddressScopeUniverse,
		unix.RT_SCOPE_SITE:     LinuxAddressScopeSite,
		unix.RT_SCOPE_LINK:     LinuxAddressScopeLink,
		unix.RT_SCOPE_HOST:     LinuxAddressScopeHost,
		unix.RT_SCOPE_NOWHERE:  LinuxAddressScopeNowhere,
		42:                     LinuxAddressScopeUnknown,
	}
	for raw, want := range tests {
		if got := linuxAddressScope(raw); got != want {
			t.Errorf("linuxAddressScope(%d) = %q, want %q", raw, got, want)
		}
	}
}

// TestLinuxNativeBackendReadOnly exercises live link and address dumps without requiring privilege.
func TestLinuxNativeBackendReadOnly(t *testing.T) {
	backend := newPlatformBackend()
	interfaces, err := backend.interfaces(context.Background())
	if err != nil {
		t.Fatalf("interfaces() error = %v", err)
	}
	if _, err := selectLoopback(interfaces); err != nil {
		t.Fatalf("selectLoopback() error = %v", err)
	}
	reserved := netip.MustParseAddr("127.254.254.254")
	observation, err := newAdapter(backend).Observe(context.Background(), reserved)
	if err != nil {
		t.Fatalf("Observe(reserved) error = %v", err)
	}
	if observation.State != StateAbsent {
		t.Skipf("host already assigns the reserved read-only test identity: %+v", observation)
	}

	client, err := linuxnetlink.OpenRoute()
	if err != nil {
		t.Fatalf("OpenRoute() error = %v", err)
	}
	links, err := observeLinuxLinks(context.Background(), client)
	if err != nil {
		_ = client.Close()
		t.Fatalf("observeLinuxLinks() error = %v", err)
	}
	addresses, err := observeLinuxAddresses(context.Background(), client, netip.MustParseAddr("127.0.0.1"), links)
	closeErr := client.Close()
	if err != nil || closeErr != nil {
		t.Fatalf("observeLinuxAddresses() error = %v, close = %v", err, closeErr)
	}
	if len(addresses) == 0 {
		t.Skip("host loopback does not expose the conventional 127.0.0.1 assignment")
	}
	if addresses[0].scope != unix.RT_SCOPE_HOST || !addresses[0].cacheInfoPresent || !addresses[0].addressMatches {
		t.Fatalf("127.0.0.1 native attributes = %#v", addresses[0])
	}
	if addresses[0].flags != unix.IFA_F_PERMANENT || addresses[0].validLifetime != linuxInfiniteLifetime || addresses[0].preferredLifetime != linuxInfiniteLifetime {
		t.Fatalf("127.0.0.1 lifetime and flags = %#v", addresses[0])
	}
}

// linuxTestLinkReply wraps native link payloads in one complete fixture reply.
func linuxTestLinkReply(payloads ...[]byte) linuxnetlink.Reply {
	messages := make([]linuxnetlink.Message, len(payloads))
	for index, payload := range payloads {
		messages[index] = linuxnetlink.Message{Type: unix.RTM_NEWLINK, Payload: payload}
	}
	return linuxnetlink.Reply{Messages: messages}
}

// linuxTestAddressReply wraps native address payloads in one complete fixture reply.
func linuxTestAddressReply(payloads ...[]byte) linuxnetlink.Reply {
	messages := make([]linuxnetlink.Message, len(payloads))
	for index, payload := range payloads {
		messages[index] = linuxnetlink.Message{Type: unix.RTM_NEWADDR, Payload: payload}
	}
	return linuxnetlink.Reply{Messages: messages}
}

// linuxTestExactAddressReply returns the canonical address record required immediately before deletion.
func linuxTestExactAddressReply(interf InterfaceFact) linuxnetlink.Reply {
	return linuxTestAddressReply(linuxTestAddressPayloadWithLabel(testAddress, 32, unix.RT_SCOPE_HOST, interf.Index, unix.IFA_F_PERMANENT, true, true, interf.Name))
}

// linuxTestLinkPayload encodes the fixed identity fields used by link-parser fixtures.
func linuxTestLinkPayload(index int, name string, flags uint32, hardware uint16) []byte {
	payload := make([]byte, unix.SizeofIfInfomsg)
	payload[0] = unix.AF_UNSPEC
	binary.NativeEndian.PutUint16(payload[2:4], hardware)
	binary.NativeEndian.PutUint32(payload[4:8], uint32(index))
	binary.NativeEndian.PutUint32(payload[8:12], flags)
	return linuxTestMarshalAttribute(payload, unix.IFLA_IFNAME, append([]byte(name), 0))
}

// linuxTestBareAddressPayload encodes only the fixed address header for missing-attribute fixtures.
func linuxTestBareAddressPayload(index int) []byte {
	payload := make([]byte, unix.SizeofIfAddrmsg)
	payload[0] = unix.AF_INET
	payload[1] = 32
	payload[3] = unix.RT_SCOPE_HOST
	binary.NativeEndian.PutUint32(payload[4:8], uint32(index))
	return payload
}

// linuxTestAddressPayload encodes one local address with optional peer and complete cache facts.
func linuxTestAddressPayload(address netip.Addr, prefixLength int, scope uint8, index int, flags uint32, includePeer bool, includeCache bool) []byte {
	return linuxTestAddressPayloadWithLabel(address, prefixLength, scope, index, flags, includePeer, includeCache, "lo")
}

// linuxTestAddressPayloadWithLabel encodes one address while permitting alias-conflict fixtures.
func linuxTestAddressPayloadWithLabel(address netip.Addr, prefixLength int, scope uint8, index int, flags uint32, includePeer bool, includeCache bool, label string) []byte {
	payload := linuxTestBareAddressPayload(index)
	payload[1] = uint8(prefixLength)
	payload[3] = scope
	value := address.As4()
	payload = linuxTestMarshalAttribute(payload, unix.IFA_LOCAL, value[:])
	if includePeer {
		payload = linuxTestMarshalAttribute(payload, unix.IFA_ADDRESS, value[:])
	}
	payload = linuxTestMarshalAttribute(payload, unix.IFA_LABEL, append([]byte(label), 0))
	encodedFlags := make([]byte, 4)
	binary.NativeEndian.PutUint32(encodedFlags, flags)
	payload = linuxTestMarshalAttribute(payload, unix.IFA_FLAGS, encodedFlags)
	if includeCache {
		cache := make([]byte, 16)
		binary.NativeEndian.PutUint32(cache[0:4], linuxInfiniteLifetime)
		binary.NativeEndian.PutUint32(cache[4:8], linuxInfiniteLifetime)
		payload = linuxTestMarshalAttribute(payload, unix.IFA_CACHEINFO, cache)
	}
	return payload
}

// linuxTestAddressPayloadWithAttribute replaces one singleton attribute with a malformed fixture value.
func linuxTestAddressPayloadWithAttribute(payload []byte, attributeType uint16, replacement []byte) []byte {
	attributes, err := linuxnetlink.ParseAttributes(payload[unix.SizeofIfAddrmsg:])
	if err != nil {
		panic(err)
	}
	rebuilt := append([]byte(nil), payload[:unix.SizeofIfAddrmsg]...)
	for _, candidate := range []uint16{unix.IFA_LOCAL, unix.IFA_ADDRESS, unix.IFA_LABEL, unix.IFA_FLAGS, unix.IFA_CACHEINFO} {
		values := attributes[candidate]
		if len(values) == 0 {
			continue
		}
		value := values[0].Payload
		if candidate == attributeType {
			value = replacement
		}
		rebuilt = linuxTestMarshalAttribute(rebuilt, candidate, value)
	}
	return rebuilt
}

// linuxTestAddressPayloadWithoutAttribute removes one singleton attribute from a valid fixture.
func linuxTestAddressPayloadWithoutAttribute(payload []byte, excluded uint16) []byte {
	attributes, err := linuxnetlink.ParseAttributes(payload[unix.SizeofIfAddrmsg:])
	if err != nil {
		panic(err)
	}
	rebuilt := append([]byte(nil), payload[:unix.SizeofIfAddrmsg]...)
	for _, candidate := range []uint16{unix.IFA_LOCAL, unix.IFA_ADDRESS, unix.IFA_LABEL, unix.IFA_FLAGS, unix.IFA_CACHEINFO} {
		if candidate == excluded {
			continue
		}
		for _, value := range attributes[candidate] {
			rebuilt = linuxTestMarshalAttribute(rebuilt, candidate, value.Payload)
		}
	}
	return rebuilt
}

// linuxTestMarshalAttribute keeps compact fixtures readable while failing on an impossible test-size error.
func linuxTestMarshalAttribute(destination []byte, attributeType uint16, payload []byte) []byte {
	encoded, err := linuxnetlink.MarshalAttribute(destination, attributeType, payload)
	if err != nil {
		panic(err)
	}
	return encoded
}
