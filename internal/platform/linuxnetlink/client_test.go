//go:build linux

package linuxnetlink

import (
	"context"
	"encoding/binary"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// testDatagramResult supplies one transaction-state fixture.
type testDatagramResult struct {
	payload   []byte
	address   *unix.SockaddrNetlink
	oversized bool
	error     error
}

// testDatagramSource consumes a bounded fixture queue and fails if the transaction over-reads it.
func testDatagramSource(t *testing.T, results []testDatagramResult, calls *int) datagramSource {
	t.Helper()
	return func(context.Context, int) ([]byte, *unix.SockaddrNetlink, bool, error) {
		if *calls >= len(results) {
			t.Fatal("receiveWith() requested an unexpected datagram")
		}
		result := results[*calls]
		*calls++
		return result.payload, result.address, result.oversized, result.error
	}
}

// TestCompletionModesRequireDeclaredTerminalShape covers data, dump, and mutation boundaries.
func TestCompletionModesRequireDeclaredTerminalShape(t *testing.T) {
	const portID = 41
	const sequence = 7
	kernel := &unix.SockaddrNetlink{Family: unix.AF_NETLINK}
	data := marshalRequest(unix.RTM_NEWLINK, 0, sequence, portID, []byte{1})
	multipart := marshalRequest(unix.RTM_NEWLINK, unix.NLM_F_MULTI, sequence, portID, []byte{1})
	done := marshalRequest(unix.NLMSG_DONE, unix.NLM_F_MULTI, sequence, portID, make([]byte, 4))
	ack := marshalRequest(unix.NLMSG_ERROR, 0, sequence, portID, make([]byte, 4))

	tests := []struct {
		name        string
		completion  Completion
		results     []testDatagramResult
		wantData    int
		wantFailure bool
		wantError   error
	}{
		{name: "data", completion: CompletionData, results: []testDatagramResult{{payload: data, address: kernel}}, wantData: 1},
		{name: "multipart data", completion: CompletionData, results: []testDatagramResult{{payload: multipart, address: kernel}, {payload: done, address: kernel}}, wantData: 1},
		{name: "dump", completion: CompletionDump, results: []testDatagramResult{{payload: multipart, address: kernel}, {payload: done, address: kernel}}, wantData: 1},
		{name: "ack", completion: CompletionAck, results: []testDatagramResult{{payload: ack, address: kernel}}},
		{name: "data rejects ack", completion: CompletionData, results: []testDatagramResult{{payload: ack, address: kernel}}, wantFailure: true},
		{name: "dump rejects ack", completion: CompletionDump, results: []testDatagramResult{{payload: ack, address: kernel}}, wantFailure: true},
		{name: "ack rejects data", completion: CompletionAck, results: []testDatagramResult{{payload: data, address: kernel}}, wantFailure: true},
		{name: "dump requires done", completion: CompletionDump, results: []testDatagramResult{{payload: multipart, address: kernel}}, wantError: ErrReplyLimit},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			reply, err := receiveWith(context.Background(), 1, portID, sequence, test.completion, len(test.results), testDatagramSource(t, test.results, &calls))
			if test.wantError != nil {
				if !errors.Is(err, test.wantError) {
					t.Fatalf("receiveWith() error = %v, want %v", err, test.wantError)
				}
				return
			}
			if test.wantFailure {
				if err == nil {
					t.Fatal("receiveWith() error = nil")
				}
				return
			}
			if err != nil || len(reply.Messages) != test.wantData || calls != len(test.results) {
				t.Fatalf("receiveWith() = %#v, calls %d, error %v", reply, calls, err)
			}
		})
	}

	calls := 0
	combined := append(append([]byte(nil), done...), data...)
	if _, err := receiveWith(context.Background(), 1, portID, sequence, CompletionDump, 1, testDatagramSource(t, []testDatagramResult{{payload: combined, address: kernel}}, &calls)); err == nil {
		t.Fatal("receiveWith(data after DONE) error = nil")
	}
}

// TestReceiveRejectsInterruptedForeignAndMalformedStreams keeps poisoned transaction evidence terminal.
func TestReceiveRejectsInterruptedForeignAndMalformedStreams(t *testing.T) {
	const portID = 41
	const sequence = 7
	kernel := &unix.SockaddrNetlink{Family: unix.AF_NETLINK}
	data := marshalRequest(unix.RTM_NEWLINK, 0, sequence, portID, []byte{1})
	tests := map[string]testDatagramResult{
		"dump interrupted": {payload: marshalRequest(unix.RTM_NEWLINK, unix.NLM_F_DUMP_INTR, sequence, portID, []byte{1}), address: kernel},
		"overrun":          {payload: marshalRequest(unix.NLMSG_OVERRUN, 0, sequence, portID, nil), address: kernel},
		"ENOBUFS":          {error: unix.ENOBUFS},
		"missing sender":   {payload: data},
		"wrong family":     {payload: data, address: &unix.SockaddrNetlink{}},
		"foreign sender":   {payload: data, address: &unix.SockaddrNetlink{Family: unix.AF_NETLINK, Pid: 9}},
		"multicast sender": {payload: data, address: &unix.SockaddrNetlink{Family: unix.AF_NETLINK, Groups: 1}},
		"oversized":        {address: kernel, oversized: true},
	}
	for name, result := range tests {
		t.Run(name, func(t *testing.T) {
			calls := 0
			_, err := receiveWith(context.Background(), 1, portID, sequence, CompletionData, 1, testDatagramSource(t, []testDatagramResult{result}, &calls))
			if err == nil {
				t.Fatal("receiveWith() error = nil")
			}
		})
	}

	status := make([]byte, 4)
	permissionDenied := -int32(unix.EPERM)
	binary.NativeEndian.PutUint32(status, uint32(permissionDenied))
	errorReply := marshalRequest(unix.NLMSG_ERROR, 0, sequence, portID, status)
	calls := 0
	_, err := receiveWith(context.Background(), 1, portID, sequence, CompletionAck, 1, testDatagramSource(t, []testDatagramResult{{payload: errorReply, address: kernel}}, &calls))
	if !errors.Is(err, unix.EPERM) {
		t.Fatalf("receiveWith(EPERM) error = %v", err)
	}
	var completed *completedResponseError
	if !errors.As(err, &completed) {
		t.Fatalf("receiveWith(EPERM) did not retain terminal completion: %T", err)
	}
}

// TestClientRejectsPoisonedReuseAndPermitsCompletedKernelErrors distinguishes framing failure from errno.
func TestClientRejectsPoisonedReuseAndPermitsCompletedKernelErrors(t *testing.T) {
	sentinel := errors.New("malformed receive fixture")
	sends := 0
	client := &Client{
		fileDescriptor: 1,
		portID:         41,
		closeFile:      func(int) error { return nil },
		sendRequest: func(context.Context, int, []byte) error {
			sends++
			return nil
		},
		receiveReply: func(context.Context, int, uint32, uint32, Completion) (Reply, error) {
			return Reply{}, sentinel
		},
	}
	if _, err := client.Exchange(context.Background(), unix.RTM_GETLINK, 0, nil, CompletionData); !errors.Is(err, sentinel) {
		t.Fatalf("Exchange(first) error = %v", err)
	}
	if _, err := client.Exchange(context.Background(), unix.RTM_GETLINK, 0, nil, CompletionData); err == nil || sends != 1 {
		t.Fatalf("Exchange(poisoned) sends = %d, error %v", sends, err)
	}

	receiveCalls := 0
	client = &Client{
		fileDescriptor: 1,
		portID:         41,
		closeFile:      func(int) error { return nil },
		sendRequest:    func(context.Context, int, []byte) error { return nil },
		receiveReply: func(context.Context, int, uint32, uint32, Completion) (Reply, error) {
			receiveCalls++
			if receiveCalls == 1 {
				return Reply{}, &completedResponseError{cause: unix.EPERM}
			}
			return Reply{}, nil
		},
	}
	if _, err := client.Exchange(context.Background(), unix.RTM_NEWADDR, 0, nil, CompletionAck); !errors.Is(err, unix.EPERM) {
		t.Fatalf("Exchange(completed error) error = %v", err)
	}
	if _, err := client.Exchange(context.Background(), unix.RTM_GETLINK, 0, nil, CompletionData); err != nil || receiveCalls != 2 {
		t.Fatalf("Exchange(after completed error) calls = %d, error %v", receiveCalls, err)
	}
}

// TestClientOwnsFlagsLifecycleAndSerialization covers caller admission, close, and concurrent use.
func TestClientOwnsFlagsLifecycleAndSerialization(t *testing.T) {
	client := &Client{
		fileDescriptor: 1,
		portID:         41,
		closeFile:      func(int) error { return nil },
		sendRequest:    func(context.Context, int, []byte) error { return nil },
		receiveReply:   func(context.Context, int, uint32, uint32, Completion) (Reply, error) { return Reply{}, nil },
	}
	for _, flags := range []uint16{
		unix.NLM_F_REQUEST,
		unix.NLM_F_MULTI,
		unix.NLM_F_ACK,
		unix.NLM_F_ECHO,
		unix.NLM_F_DUMP_INTR,
		unix.NLM_F_DUMP_FILTERED,
		unix.NLM_F_REQUEST | unix.NLM_F_ACK,
	} {
		if _, err := client.Exchange(context.Background(), unix.RTM_GETLINK, flags, nil, CompletionData); err == nil {
			t.Fatalf("Exchange(flags %#x) error = nil", flags)
		}
	}
	if _, err := client.Exchange(context.Background(), unix.RTM_GETLINK, 0, nil, 0); err == nil {
		t.Fatal("Exchange(invalid completion) error = nil")
	}
	if _, err := client.Exchange(context.Background(), unix.RTM_GETLINK, 0, make([]byte, maximumDatagramBytes), CompletionData); err == nil {
		t.Fatal("Exchange(oversized request) error = nil")
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.Exchange(canceled, unix.RTM_GETLINK, 0, nil, CompletionData); !errors.Is(err, context.Canceled) {
		t.Fatalf("Exchange(canceled) error = %v", err)
	}
	if err := client.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if err := client.Close(); err != nil {
		t.Fatalf("Close(idempotent) error = %v", err)
	}
	if _, err := client.Exchange(context.Background(), unix.RTM_GETLINK, 0, nil, CompletionData); err == nil {
		t.Fatal("Exchange(closed) error = nil")
	}

	var active int32
	var maximum int32
	client = &Client{
		fileDescriptor: 1,
		portID:         41,
		closeFile:      func(int) error { return nil },
		sendRequest: func(context.Context, int, []byte) error {
			current := atomic.AddInt32(&active, 1)
			for {
				observed := atomic.LoadInt32(&maximum)
				if current <= observed || atomic.CompareAndSwapInt32(&maximum, observed, current) {
					break
				}
			}
			return nil
		},
		receiveReply: func(context.Context, int, uint32, uint32, Completion) (Reply, error) {
			atomic.AddInt32(&active, -1)
			return Reply{}, nil
		},
	}
	var wait sync.WaitGroup
	for index := 0; index < 16; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if _, err := client.Exchange(context.Background(), unix.RTM_GETLINK, 0, nil, CompletionData); err != nil {
				t.Errorf("Exchange(concurrent) error = %v", err)
			}
		}()
	}
	wait.Wait()
	if maximum != 1 {
		t.Fatalf("concurrent transactions = %d, want 1", maximum)
	}
}

// TestClientCoversSequenceSendAndCloseFailures keeps lifecycle edge cases explicit.
func TestClientCoversSequenceSendAndCloseFailures(t *testing.T) {
	closeFailure := errors.New("close fixture failure")
	client := &Client{fileDescriptor: 1, closeFile: func(int) error { return closeFailure }}
	if err := client.Close(); !errors.Is(err, closeFailure) {
		t.Fatalf("Close() error = %v", err)
	}
	if err := client.Close(); !errors.Is(err, closeFailure) {
		t.Fatalf("Close(idempotent failure) error = %v", err)
	}

	sendFailure := errors.New("send fixture failure")
	client = &Client{
		fileDescriptor: 1,
		portID:         41,
		sequence:       ^uint32(0),
		closeFile:      func(int) error { return nil },
		sendRequest:    func(context.Context, int, []byte) error { return sendFailure },
		receiveReply:   func(context.Context, int, uint32, uint32, Completion) (Reply, error) { return Reply{}, nil },
	}
	if _, err := client.Exchange(nil, unix.RTM_GETLINK, 0, nil, CompletionData); !errors.Is(err, sendFailure) || client.sequence != 1 {
		t.Fatalf("Exchange(send failure) sequence = %d, error %v", client.sequence, err)
	}

	terminal := &completedResponseError{cause: unix.EPERM}
	if terminal.Error() == "" || !errors.Is(terminal, unix.EPERM) {
		t.Fatalf("completedResponseError = %q", terminal.Error())
	}
}

// TestReceiveCoversNoopSourceLimitsAndStatusErrors closes remaining transaction-state branches.
func TestReceiveCoversNoopSourceLimitsAndStatusErrors(t *testing.T) {
	const portID = 41
	const sequence = 7
	kernel := &unix.SockaddrNetlink{Family: unix.AF_NETLINK}
	noop := marshalRequest(unix.NLMSG_NOOP, 0, sequence, portID, nil)
	data := marshalRequest(unix.RTM_NEWLINK, 0, sequence, portID, []byte{1})
	calls := 0
	reply, err := receiveWith(context.Background(), 1, portID, sequence, CompletionData, 1, testDatagramSource(t, []testDatagramResult{{payload: append(noop, data...), address: kernel}}, &calls))
	if err != nil || len(reply.Messages) != 1 {
		t.Fatalf("receiveWith(NOOP) = %#v, %v", reply, err)
	}

	sentinel := errors.New("source fixture failure")
	calls = 0
	if _, err := receiveWith(context.Background(), 1, portID, sequence, CompletionData, 1, testDatagramSource(t, []testDatagramResult{{error: sentinel}}, &calls)); !errors.Is(err, sentinel) {
		t.Fatalf("receiveWith(source failure) error = %v", err)
	}
	calls = 0
	tooLarge := make([]byte, maximumReplyBytes+1)
	if _, err := receiveWith(context.Background(), 1, portID, sequence, CompletionData, 1, testDatagramSource(t, []testDatagramResult{{payload: tooLarge, address: kernel}}, &calls)); !errors.Is(err, ErrReplyLimit) {
		t.Fatalf("receiveWith(total bound) error = %v", err)
	}

	for name, fixture := range map[string]struct {
		parse func([]byte) error
		data  []byte
	}{
		"short error":    {parse: parseError, data: []byte{1}},
		"positive error": {parse: parseError, data: []byte{1, 0, 0, 0}},
		"short done":     {parse: parseDone, data: []byte{1}},
		"positive done":  {parse: parseDone, data: []byte{1, 0, 0, 0}},
	} {
		t.Run(name, func(t *testing.T) {
			if err := fixture.parse(fixture.data); err == nil {
				t.Fatal("status parser error = nil")
			}
		})
	}
	if err := parseDone(nil); err != nil {
		t.Fatalf("parseDone(empty) error = %v", err)
	}
	negative := make([]byte, 4)
	interrupted := -int32(unix.EINTR)
	binary.NativeEndian.PutUint32(negative, uint32(interrupted))
	if err := parseDone(negative); !errors.Is(err, unix.EINTR) {
		t.Fatalf("parseDone(EINTR) error = %v", err)
	}
}

// TestDatagramCodecRejectsEveryHeaderAmbiguity covers length, padding, identity, and empty frames.
func TestDatagramCodecRejectsEveryHeaderAmbiguity(t *testing.T) {
	frame := marshalRequest(unix.RTM_NEWLINK, 0, 7, 41, []byte{1})
	messages, err := parseDatagram(frame, 41, 7)
	if err != nil || len(messages) != 1 || messages[0].Type != unix.RTM_NEWLINK {
		t.Fatalf("parseDatagram(valid) = %#v, %v", messages, err)
	}
	tests := map[string][]byte{
		"empty":          nil,
		"short header":   frame[:unix.SizeofNlMsghdr-1],
		"short length":   append([]byte(nil), frame...),
		"long length":    append([]byte(nil), frame...),
		"wrong sequence": append([]byte(nil), frame...),
		"wrong port":     append([]byte(nil), frame...),
	}
	binary.NativeEndian.PutUint32(tests["short length"][:4], 4)
	binary.NativeEndian.PutUint32(tests["long length"][:4], uint32(len(frame)+4))
	binary.NativeEndian.PutUint32(tests["wrong sequence"][8:12], 8)
	binary.NativeEndian.PutUint32(tests["wrong port"][12:16], 42)
	padding := make([]byte, unix.SizeofNlMsghdr+1)
	binary.NativeEndian.PutUint32(padding[:4], uint32(len(padding)))
	binary.NativeEndian.PutUint32(padding[8:12], 7)
	binary.NativeEndian.PutUint32(padding[12:16], 41)
	tests["short padding"] = padding
	for name, malformed := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := parseDatagram(malformed, 41, 7); err == nil {
				t.Fatal("parseDatagram() error = nil")
			}
		})
	}
}

// TestReceiveDatagramRetriesAndRejectsOversizedConsumeFailure covers transient receive-side syscalls.
func TestReceiveDatagramRetriesAndRejectsOversizedConsumeFailure(t *testing.T) {
	frame := []byte{1, 2, 3, 4}
	kernel := &unix.SockaddrNetlink{Family: unix.AF_NETLINK}
	ready := func(context.Context, int, int16) error { return nil }
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
	if payload, _, _, err := receiveDatagramWith(context.Background(), 1, ready, recvmsg); err != nil || calls != 5 || !reflect.DeepEqual(payload, frame) {
		t.Fatalf("receiveDatagramWith(retries) = %v, calls %d, error %v", payload, calls, err)
	}

	receiveFailure := errors.New("receive fixture failure")
	for name, fixture := range map[string]recvmsgCall{
		"receive failure": func(_ int, _ []byte, _ []byte, flags int) (int, int, int, unix.Sockaddr, error) {
			if flags&unix.MSG_PEEK != 0 {
				return len(frame), 0, unix.MSG_TRUNC, kernel, nil
			}
			return 0, 0, 0, kernel, receiveFailure
		},
		"oversized without truncation": func(_ int, _ []byte, _ []byte, flags int) (int, int, int, unix.Sockaddr, error) {
			if flags&unix.MSG_PEEK != 0 {
				return maximumDatagramBytes + 1, 0, unix.MSG_TRUNC, kernel, nil
			}
			return maximumDatagramBytes + 1, 0, 0, kernel, nil
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, _, err := receiveDatagramWith(context.Background(), 1, ready, fixture); err == nil {
				t.Fatal("receiveDatagramWith() error = nil")
			}
		})
	}
}

// TestPollCoversSubmillisecondAndFatalBranches proves deadline rounding never becomes an infinite wait.
func TestPollCoversSubmillisecondAndFatalBranches(t *testing.T) {
	deadline := time.Now().Add(time.Hour)
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	if err := pollWith(ctx, 1, unix.POLLIN, func() time.Time { return deadline.Add(-time.Microsecond) }, func(descriptors []unix.PollFd, timeout int) (int, error) {
		if timeout != 1 {
			t.Fatalf("poll timeout = %d, want 1", timeout)
		}
		descriptors[0].Revents = unix.POLLIN
		return 1, nil
	}); err != nil {
		t.Fatalf("pollWith(submillisecond) error = %v", err)
	}
	sentinel := errors.New("poll fixture failure")
	if err := pollWith(context.Background(), 1, unix.POLLIN, time.Now, func([]unix.PollFd, int) (int, error) { return 0, sentinel }); !errors.Is(err, sentinel) {
		t.Fatalf("pollWith(fatal) error = %v", err)
	}
}

// TestOpenWithRequiresStrictUnicastSetup proves every partial descriptor is closed.
func TestOpenWithRequiresStrictUnicastSetup(t *testing.T) {
	closed := 0
	options := make([]int, 0, 3)
	operations := openOperations{
		socket: func(int, int, int) (int, error) { return 17, nil },
		bind:   func(int, unix.Sockaddr) error { return nil },
		setSocketOption: func(_ int, _ int, option int, _ int) error {
			options = append(options, option)
			return nil
		},
		localAddress: func(int) (unix.Sockaddr, error) { return &unix.SockaddrNetlink{Family: unix.AF_NETLINK, Pid: 22}, nil },
		close:        func(int) error { closed++; return nil },
	}
	client, err := openWith(unix.NETLINK_ROUTE, operations)
	if err != nil {
		t.Fatalf("openWith() error = %v", err)
	}
	if !reflect.DeepEqual(options, []int{unix.NETLINK_EXT_ACK, unix.NETLINK_CAP_ACK, unix.NETLINK_GET_STRICT_CHK}) {
		t.Fatalf("strict options = %v", options)
	}
	if err := client.Close(); err != nil || closed != 1 {
		t.Fatalf("Close() = %v, closes %d", err, closed)
	}

	sentinel := errors.New("setup fixture failure")
	tests := map[string]func(*openOperations){
		"socket": func(operations *openOperations) {
			operations.socket = func(int, int, int) (int, error) { return -1, sentinel }
		},
		"bind": func(operations *openOperations) { operations.bind = func(int, unix.Sockaddr) error { return sentinel } },
		"option": func(operations *openOperations) {
			operations.setSocketOption = func(int, int, int, int) error { return sentinel }
		},
		"address": func(operations *openOperations) {
			operations.localAddress = func(int) (unix.Sockaddr, error) { return nil, sentinel }
		},
		"wrong family": func(operations *openOperations) {
			operations.localAddress = func(int) (unix.Sockaddr, error) { return &unix.SockaddrInet4{}, nil }
		},
		"wrong netlink family": func(operations *openOperations) {
			operations.localAddress = func(int) (unix.Sockaddr, error) { return &unix.SockaddrNetlink{Pid: 22}, nil }
		},
		"zero port": func(operations *openOperations) {
			operations.localAddress = func(int) (unix.Sockaddr, error) { return &unix.SockaddrNetlink{Family: unix.AF_NETLINK}, nil }
		},
		"multicast": func(operations *openOperations) {
			operations.localAddress = func(int) (unix.Sockaddr, error) {
				return &unix.SockaddrNetlink{Family: unix.AF_NETLINK, Pid: 1, Groups: 1}, nil
			}
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			closes := 0
			fixture := openOperations{
				socket:          func(int, int, int) (int, error) { return 17, nil },
				bind:            func(int, unix.Sockaddr) error { return nil },
				setSocketOption: func(int, int, int, int) error { return nil },
				localAddress:    func(int) (unix.Sockaddr, error) { return &unix.SockaddrNetlink{Family: unix.AF_NETLINK, Pid: 22}, nil },
				close:           func(int) error { closes++; return nil },
			}
			mutate(&fixture)
			if _, err := openWith(unix.NETLINK_ROUTE, fixture); err == nil {
				t.Fatal("openWith() error = nil")
			}
			wantCloses := 1
			if name == "socket" {
				wantCloses = 0
			}
			if closes != wantCloses {
				t.Fatalf("closes = %d, want %d", closes, wantCloses)
			}
		})
	}
}

// TestSendAndPollRetryOnlySafeConditions covers backpressure, deadlines, and descriptor errors.
func TestSendAndPollRetryOnlySafeConditions(t *testing.T) {
	sentinel := errors.New("syscall fixture failure")
	calls := 0
	err := sendWith(context.Background(), 1, []byte{1}, func(int, []byte, int, unix.Sockaddr) error {
		calls++
		switch calls {
		case 1:
			return unix.EINTR
		case 2:
			return unix.EAGAIN
		default:
			return nil
		}
	}, func(context.Context, int, int16) error { return nil })
	if err != nil || calls != 3 {
		t.Fatalf("sendWith() calls = %d, error %v", calls, err)
	}
	if err := sendWith(context.Background(), 1, nil, func(int, []byte, int, unix.Sockaddr) error { return sentinel }, func(context.Context, int, int16) error { return nil }); !errors.Is(err, sentinel) {
		t.Fatalf("sendWith(fatal) error = %v", err)
	}
	if err := sendWith(context.Background(), 1, nil, func(int, []byte, int, unix.Sockaddr) error { return unix.EAGAIN }, func(context.Context, int, int16) error { return sentinel }); !errors.Is(err, sentinel) {
		t.Fatalf("sendWith(poll) error = %v", err)
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := pollWith(canceled, 1, unix.POLLIN, time.Now, func([]unix.PollFd, int) (int, error) { return 0, nil }); !errors.Is(err, context.Canceled) {
		t.Fatalf("pollWith(canceled) error = %v", err)
	}
	deadline := time.Unix(100, 0)
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	if err := pollWith(ctx, 1, unix.POLLIN, func() time.Time { return deadline }, func([]unix.PollFd, int) (int, error) { return 0, nil }); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("pollWith(deadline) error = %v", err)
	}
	calls = 0
	err = pollWith(context.Background(), 1, unix.POLLIN, time.Now, func(descriptors []unix.PollFd, _ int) (int, error) {
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
		t.Fatalf("pollWith() calls = %d, error %v", calls, err)
	}
	if err := pollWith(context.Background(), 1, unix.POLLIN, time.Now, func(descriptors []unix.PollFd, _ int) (int, error) {
		descriptors[0].Revents = unix.POLLNVAL
		return 1, nil
	}); err == nil {
		t.Fatal("pollWith(POLLNVAL) error = nil")
	}
}

// TestReceiveDatagramChecksPeekConsumeAndSource covers truncation and transient syscall mechanics.
func TestReceiveDatagramChecksPeekConsumeAndSource(t *testing.T) {
	frame := []byte{1, 2, 3, 4}
	kernel := &unix.SockaddrNetlink{Family: unix.AF_NETLINK}
	ready := func(context.Context, int, int16) error { return nil }
	calls := 0
	recvmsg := func(_ int, payload []byte, _ []byte, flags int) (int, int, int, unix.Sockaddr, error) {
		calls++
		if flags&unix.MSG_PEEK != 0 {
			return len(frame), 0, unix.MSG_TRUNC, kernel, nil
		}
		copy(payload, frame)
		return len(frame), 0, 0, kernel, nil
	}
	payload, address, oversized, err := receiveDatagramWith(context.Background(), 1, ready, recvmsg)
	if err != nil || oversized || calls != 2 || address.Pid != 0 || !reflect.DeepEqual(payload, frame) {
		t.Fatalf("receiveDatagramWith() = %v, %#v, %t, calls %d, error %v", payload, address, oversized, calls, err)
	}

	tests := map[string]recvmsgCall{
		"empty": func(int, []byte, []byte, int) (int, int, int, unix.Sockaddr, error) { return 0, 0, 0, kernel, nil },
		"peek failure": func(int, []byte, []byte, int) (int, int, int, unix.Sockaddr, error) {
			return 0, 0, 0, kernel, errors.New("peek failure")
		},
		"wrong source": func(_ int, _ []byte, _ []byte, flags int) (int, int, int, unix.Sockaddr, error) {
			if flags&unix.MSG_PEEK != 0 {
				return len(frame), 0, unix.MSG_TRUNC, kernel, nil
			}
			return len(frame), 0, 0, &unix.SockaddrInet4{}, nil
		},
		"wrong netlink family": func(_ int, _ []byte, _ []byte, flags int) (int, int, int, unix.Sockaddr, error) {
			if flags&unix.MSG_PEEK != 0 {
				return len(frame), 0, unix.MSG_TRUNC, kernel, nil
			}
			return len(frame), 0, 0, &unix.SockaddrNetlink{}, nil
		},
		"changed size": func(_ int, _ []byte, _ []byte, flags int) (int, int, int, unix.Sockaddr, error) {
			if flags&unix.MSG_PEEK != 0 {
				return len(frame), 0, unix.MSG_TRUNC, kernel, nil
			}
			return len(frame) - 1, 0, 0, kernel, nil
		},
		"unexpected truncation": func(_ int, _ []byte, _ []byte, flags int) (int, int, int, unix.Sockaddr, error) {
			if flags&unix.MSG_PEEK != 0 {
				return len(frame), 0, unix.MSG_TRUNC, kernel, nil
			}
			return len(frame), 0, unix.MSG_TRUNC, kernel, nil
		},
	}
	for name, fixture := range tests {
		t.Run(name, func(t *testing.T) {
			if _, _, _, err := receiveDatagramWith(context.Background(), 1, ready, fixture); err == nil {
				t.Fatal("receiveDatagramWith() error = nil")
			}
		})
	}
	if _, _, _, err := receiveDatagramWith(context.Background(), 1, func(context.Context, int, int16) error { return errors.New("poll failure") }, nil); err == nil {
		t.Fatal("receiveDatagramWith(poll) error = nil")
	}

	oversizedReceive := func(_ int, _ []byte, _ []byte, flags int) (int, int, int, unix.Sockaddr, error) {
		if flags&unix.MSG_PEEK != 0 {
			return maximumDatagramBytes + 1, 0, unix.MSG_TRUNC, kernel, nil
		}
		return maximumDatagramBytes + 1, 0, unix.MSG_TRUNC, kernel, nil
	}
	payload, _, oversized, err = receiveDatagramWith(context.Background(), 1, ready, oversizedReceive)
	if err != nil || !oversized || payload != nil {
		t.Fatalf("receiveDatagramWith(oversized) = %v, %t, %v", payload, oversized, err)
	}
}

// TestLiveClientsCompleteConcurrentKernelTransactions proves the shipping socket path on Linux CI hosts.
func TestLiveClientsCompleteConcurrentKernelTransactions(t *testing.T) {
	client, err := OpenRoute()
	if err != nil {
		t.Fatalf("OpenRoute() error = %v", err)
	}
	payload := make([]byte, unix.SizeofIfInfomsg)
	payload[0] = unix.AF_UNSPEC
	var wait sync.WaitGroup
	for index := 0; index < 4; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			reply, exchangeErr := client.Exchange(context.Background(), unix.RTM_GETLINK, unix.NLM_F_DUMP, payload, CompletionDump)
			if exchangeErr != nil || len(reply.Messages) == 0 {
				t.Errorf("Exchange(RTM_GETLINK) messages = %d, error %v", len(reply.Messages), exchangeErr)
			}
		}()
	}
	wait.Wait()
	if err := client.Close(); err != nil {
		t.Fatalf("Close(route) error = %v", err)
	}
	if err := client.Close(); err != nil {
		t.Fatalf("Close(route idempotent) error = %v", err)
	}
	if _, err := client.Exchange(context.Background(), unix.RTM_GETLINK, 0, payload, CompletionData); err == nil {
		t.Fatal("Exchange(closed route) error = nil")
	}
	diagnostic, err := OpenSocketDiag()
	if err != nil {
		t.Fatalf("OpenSocketDiag() error = %v", err)
	}
	if err := diagnostic.Close(); err != nil {
		t.Fatalf("Close(diagnostic) error = %v", err)
	}
}
