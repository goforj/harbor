//go:build linux

package linuxnetlink

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

const (
	netlinkAlignment      = 4
	maximumDatagramBytes  = 1 << 20
	maximumReplyBytes     = 16 << 20
	maximumReplyMessages  = 65536
	maximumReplyDatagrams = 4096
	netlinkPollInterval   = 100 * time.Millisecond
)

var (
	// ErrInterrupted reports a kernel transaction whose complete snapshot cannot be trusted.
	ErrInterrupted = errors.New("Linux netlink transaction was interrupted")
	// ErrReplyLimit reports a kernel reply that exceeded Harbor's memory or message bounds.
	ErrReplyLimit = errors.New("Linux netlink reply exceeded its transaction bound")
)

// Completion identifies the kernel response that finishes one request.
type Completion uint8

const (
	// CompletionDump requires an explicit NLMSG_DONE after every multipart data message.
	CompletionDump Completion = iota + 1
	// CompletionData requires one non-multipart data response or a complete multipart response.
	CompletionData
	// CompletionAck requires an explicit zero-status NLMSG_ERROR acknowledgement.
	CompletionAck
)

// Message contains one payload whose header was bound to the active request.
type Message struct {
	Type    uint16
	Flags   uint16
	Payload []byte
}

// Reply contains the bounded data messages from one complete transaction.
type Reply struct {
	Messages []Message
}

// Client owns one kernel netlink port so replies cannot cross request boundaries.
type Client struct {
	fileDescriptor int
	portID         uint32
	sequence       uint32
	closeFile      func(int) error
	sendRequest    func(context.Context, int, []byte) error
	receiveReply   func(context.Context, int, uint32, uint32, Completion) (Reply, error)
	mutex          sync.Mutex
	closed         bool
	closeErr       error
	poisoned       bool
}

// completedResponseError marks a kernel error that also terminated its request stream cleanly.
type completedResponseError struct {
	cause error
}

// Error returns the bounded kernel failure collected from the terminal response.
func (err *completedResponseError) Error() string {
	return err.cause.Error()
}

// Unwrap preserves errno classification without exposing transport state to callers.
func (err *completedResponseError) Unwrap() error {
	return err.cause
}

// openOperations isolates socket setup so every partial-open cleanup path is testable.
type openOperations struct {
	socket          func(int, int, int) (int, error)
	bind            func(int, unix.Sockaddr) error
	setSocketOption func(int, int, int, int) error
	localAddress    func(int) (unix.Sockaddr, error)
	close           func(int) error
}

// datagramSource supplies one exact kernel datagram to the completion state machine.
type datagramSource func(context.Context, int) ([]byte, *unix.SockaddrNetlink, bool, error)

// recvmsgCall matches recvmsg(2) while allowing deterministic truncation fixtures.
type recvmsgCall func(int, []byte, []byte, int) (int, int, int, unix.Sockaddr, error)

// poller waits for one requested readiness class.
type poller func(context.Context, int, int16) error

// sendtoCall matches the one-shot datagram publication syscall.
type sendtoCall func(int, []byte, int, unix.Sockaddr) error

// pollCall matches poll(2) while allowing deterministic cancellation fixtures.
type pollCall func([]unix.PollFd, int) (int, error)

// OpenRoute opens a strict, nonblocking NETLINK_ROUTE client.
func OpenRoute() (*Client, error) {
	return open(unix.NETLINK_ROUTE)
}

// OpenSocketDiag opens a strict, nonblocking NETLINK_SOCK_DIAG client.
func OpenSocketDiag() (*Client, error) {
	return open(unix.NETLINK_SOCK_DIAG)
}

// open configures every strict option before exposing a live kernel port.
func open(protocol int) (*Client, error) {
	operations := openOperations{
		socket:          unix.Socket,
		bind:            unix.Bind,
		setSocketOption: unix.SetsockoptInt,
		localAddress:    unix.Getsockname,
		close:           unix.Close,
	}
	return openWith(protocol, operations)
}

// openWith closes partial state whenever strict unicast setup cannot be proven.
func openWith(protocol int, operations openOperations) (*Client, error) {
	fileDescriptor, err := operations.socket(unix.AF_NETLINK, unix.SOCK_RAW|unix.SOCK_CLOEXEC|unix.SOCK_NONBLOCK, protocol)
	if err != nil {
		return nil, fmt.Errorf("Linux netlink socket: %w", err)
	}
	closeOnError := true
	defer func() {
		if closeOnError {
			_ = operations.close(fileDescriptor)
		}
	}()

	if err := operations.bind(fileDescriptor, &unix.SockaddrNetlink{Family: unix.AF_NETLINK}); err != nil {
		return nil, fmt.Errorf("Linux netlink bind: %w", err)
	}
	for _, option := range []int{unix.NETLINK_EXT_ACK, unix.NETLINK_CAP_ACK, unix.NETLINK_GET_STRICT_CHK} {
		if err := operations.setSocketOption(fileDescriptor, unix.SOL_NETLINK, option, 1); err != nil {
			return nil, fmt.Errorf("Linux netlink enable option %d: %w", option, err)
		}
	}
	address, err := operations.localAddress(fileDescriptor)
	if err != nil {
		return nil, fmt.Errorf("Linux netlink local address: %w", err)
	}
	netlinkAddress, ok := address.(*unix.SockaddrNetlink)
	if !ok || netlinkAddress.Family != unix.AF_NETLINK || netlinkAddress.Pid == 0 || netlinkAddress.Groups != 0 {
		return nil, fmt.Errorf("Linux netlink local address is not an unicast port")
	}

	closeOnError = false
	return &Client{
		fileDescriptor: fileDescriptor,
		portID:         netlinkAddress.Pid,
		closeFile:      operations.close,
		sendRequest:    send,
		receiveReply:   receive,
	}, nil
}

// Close releases the client port before another transaction may reuse its numeric identity.
func (client *Client) Close() error {
	client.mutex.Lock()
	defer client.mutex.Unlock()
	if client.closed {
		return client.closeErr
	}
	client.closed = true
	if err := client.closeFile(client.fileDescriptor); err != nil {
		client.closeErr = fmt.Errorf("Linux netlink close: %w", err)
		return client.closeErr
	}
	return nil
}

// Exchange publishes one request and accepts only its exact kernel completion shape.
func (client *Client) Exchange(ctx context.Context, messageType uint16, flags uint16, payload []byte, completion Completion) (Reply, error) {
	if err := validateCompletion(completion); err != nil {
		return Reply{}, err
	}
	const responseOnlyFlags = unix.NLM_F_MULTI | unix.NLM_F_ACK | unix.NLM_F_ECHO | unix.NLM_F_DUMP_INTR | unix.NLM_F_DUMP_FILTERED
	if flags&(unix.NLM_F_REQUEST|responseOnlyFlags) != 0 {
		return Reply{}, fmt.Errorf("Linux netlink client owns request and response-shaping flags")
	}
	if len(payload) > maximumDatagramBytes-unix.SizeofNlMsghdr {
		return Reply{}, fmt.Errorf("Linux netlink request payload exceeds its transaction bound")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return Reply{}, err
	}
	client.mutex.Lock()
	defer client.mutex.Unlock()
	if client.closed {
		return Reply{}, fmt.Errorf("Linux netlink client is closed")
	}
	if client.poisoned {
		return Reply{}, fmt.Errorf("Linux netlink client cannot reuse an incomplete transaction")
	}
	if err := ctx.Err(); err != nil {
		return Reply{}, err
	}
	client.sequence++
	if client.sequence == 0 {
		client.sequence++
	}
	requestFlags := flags | unix.NLM_F_REQUEST
	if completion == CompletionAck {
		requestFlags |= unix.NLM_F_ACK
	}
	request := marshalRequest(messageType, requestFlags, client.sequence, client.portID, payload)
	if err := client.sendRequest(ctx, client.fileDescriptor, request); err != nil {
		return Reply{}, err
	}
	reply, err := client.receiveReply(ctx, client.fileDescriptor, client.portID, client.sequence, completion)
	if err != nil {
		var completed *completedResponseError
		if !errors.As(err, &completed) {
			client.poisoned = true
		}
	}
	return reply, err
}

// validateCompletion rejects zero-value or future modes before publishing a request.
func validateCompletion(completion Completion) error {
	switch completion {
	case CompletionDump, CompletionData, CompletionAck:
		return nil
	default:
		return fmt.Errorf("Linux netlink completion mode %d is unsupported", completion)
	}
}

// marshalRequest creates one aligned native-endian message accepted by the Linux ABI.
func marshalRequest(messageType uint16, flags uint16, sequence uint32, portID uint32, payload []byte) []byte {
	messageLength := unix.SizeofNlMsghdr + len(payload)
	message := make([]byte, align(messageLength))
	binary.NativeEndian.PutUint32(message[0:4], uint32(messageLength))
	binary.NativeEndian.PutUint16(message[4:6], messageType)
	binary.NativeEndian.PutUint16(message[6:8], flags)
	binary.NativeEndian.PutUint32(message[8:12], sequence)
	binary.NativeEndian.PutUint32(message[12:16], portID)
	copy(message[unix.SizeofNlMsghdr:], payload)
	return message
}

// send waits in bounded intervals so cancellation remains effective on socket backpressure.
func send(ctx context.Context, fileDescriptor int, request []byte) error {
	return sendWith(ctx, fileDescriptor, request, unix.Sendto, poll)
}

// sendWith retries only interruption and backpressure because other errors are authoritative.
func sendWith(ctx context.Context, fileDescriptor int, request []byte, sendto sendtoCall, wait poller) error {
	for {
		err := sendto(fileDescriptor, request, unix.MSG_DONTWAIT, &unix.SockaddrNetlink{Family: unix.AF_NETLINK})
		if err == nil {
			return nil
		}
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if !errors.Is(err, unix.EAGAIN) && !errors.Is(err, unix.EWOULDBLOCK) {
			return fmt.Errorf("Linux netlink send: %w", err)
		}
		if err := wait(ctx, fileDescriptor, unix.POLLOUT); err != nil {
			return err
		}
	}
}

// receive consumes one bounded completion and rejects a stream that could poison the next request.
func receive(ctx context.Context, fileDescriptor int, portID uint32, sequence uint32, completion Completion) (Reply, error) {
	return receiveWith(ctx, fileDescriptor, portID, sequence, completion, maximumReplyDatagrams, receiveDatagram)
}

// receiveWith enforces request-specific termination independently from recvmsg mechanics.
func receiveWith(ctx context.Context, fileDescriptor int, portID uint32, sequence uint32, completion Completion, maximumDatagrams int, source datagramSource) (Reply, error) {
	reply := Reply{}
	totalBytes := uint64(0)
	sawData := false
	sawAck := false
	multipart := completion == CompletionDump
	for datagramIndex := 0; datagramIndex < maximumDatagrams; datagramIndex++ {
		datagram, address, oversized, err := source(ctx, fileDescriptor)
		if err != nil {
			if errors.Is(err, unix.ENOBUFS) {
				return Reply{}, ErrInterrupted
			}
			return Reply{}, err
		}
		if address == nil || address.Family != unix.AF_NETLINK || address.Pid != 0 || address.Groups != 0 {
			return Reply{}, fmt.Errorf("Linux netlink reply did not originate from the kernel")
		}
		if oversized {
			return Reply{}, ErrReplyLimit
		}
		totalBytes += uint64(len(datagram))
		if totalBytes > uint64(maximumReplyBytes) {
			return Reply{}, ErrReplyLimit
		}

		messages, err := parseDatagram(datagram, portID, sequence)
		if err != nil {
			return Reply{}, err
		}
		done := false
		for _, message := range messages {
			if done || sawAck {
				return Reply{}, fmt.Errorf("Linux netlink reply contains data after completion")
			}
			if message.Flags&unix.NLM_F_MULTI != 0 {
				multipart = true
			}
			if message.Flags&unix.NLM_F_DUMP_INTR != 0 {
				return Reply{}, ErrInterrupted
			}
			switch message.Type {
			case unix.NLMSG_NOOP:
				continue
			case unix.NLMSG_OVERRUN:
				return Reply{}, ErrInterrupted
			case unix.NLMSG_ERROR:
				if err := parseError(message.Payload); err != nil {
					return Reply{}, err
				}
				sawAck = true
			case unix.NLMSG_DONE:
				if err := parseDone(message.Payload); err != nil {
					return Reply{}, err
				}
				done = true
			default:
				sawData = true
				if len(reply.Messages) >= maximumReplyMessages {
					return Reply{}, ErrReplyLimit
				}
				reply.Messages = append(reply.Messages, message)
			}
		}
		switch completion {
		case CompletionDump:
			if sawAck {
				return Reply{}, fmt.Errorf("Linux netlink dump ended with an ACK instead of DONE")
			}
			if done {
				return reply, nil
			}
		case CompletionData:
			if sawAck && !sawData {
				return Reply{}, fmt.Errorf("Linux netlink data request returned only an ACK")
			}
			if done && !sawData {
				return Reply{}, fmt.Errorf("Linux netlink data request completed without data")
			}
			if done || sawData && !multipart {
				return reply, nil
			}
		case CompletionAck:
			if sawData || done {
				return Reply{}, fmt.Errorf("Linux netlink mutation returned an unexpected data completion")
			}
			if sawAck {
				return reply, nil
			}
		}
	}
	return Reply{}, ErrReplyLimit
}

// receiveDatagram peeks the kernel-reported size before making a bounded allocation.
func receiveDatagram(ctx context.Context, fileDescriptor int) ([]byte, *unix.SockaddrNetlink, bool, error) {
	return receiveDatagramWith(ctx, fileDescriptor, poll, unix.Recvmsg)
}

// receiveDatagramWith proves the peeked size still matches the consumed datagram.
func receiveDatagramWith(ctx context.Context, fileDescriptor int, wait poller, recvmsg recvmsgCall) ([]byte, *unix.SockaddrNetlink, bool, error) {
	for {
		if err := wait(ctx, fileDescriptor, unix.POLLIN); err != nil {
			return nil, nil, false, err
		}
		probe := []byte{0}
		length, _, _, _, err := recvmsg(fileDescriptor, probe, nil, unix.MSG_PEEK|unix.MSG_TRUNC|unix.MSG_DONTWAIT)
		if errors.Is(err, unix.EINTR) || errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EWOULDBLOCK) {
			continue
		}
		if err != nil {
			return nil, nil, false, fmt.Errorf("Linux netlink peek: %w", err)
		}
		if length <= 0 {
			return nil, nil, false, fmt.Errorf("Linux netlink returned an empty datagram")
		}

		bufferLength := length
		oversized := length > maximumDatagramBytes
		if oversized {
			bufferLength = 1
		}
		buffer := make([]byte, bufferLength)
		received, _, receiveFlags, source, err := recvmsg(fileDescriptor, buffer, nil, unix.MSG_DONTWAIT)
		if errors.Is(err, unix.EINTR) || errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EWOULDBLOCK) {
			continue
		}
		if err != nil {
			return nil, nil, false, fmt.Errorf("Linux netlink receive: %w", err)
		}
		address, ok := source.(*unix.SockaddrNetlink)
		if !ok || address.Family != unix.AF_NETLINK {
			return nil, nil, false, fmt.Errorf("Linux netlink reply has an unexpected address family")
		}
		if oversized {
			if receiveFlags&unix.MSG_TRUNC == 0 {
				return nil, nil, false, fmt.Errorf("Linux netlink oversized datagram was not truncated")
			}
			return nil, address, true, nil
		}
		if receiveFlags&unix.MSG_TRUNC != 0 || received != length {
			return nil, nil, false, fmt.Errorf("Linux netlink datagram size changed during receive")
		}
		return buffer[:received], address, false, nil
	}
}

// poll uses short waits because context cancellation is not a kernel netlink event.
func poll(ctx context.Context, fileDescriptor int, events int16) error {
	return pollWith(ctx, fileDescriptor, events, time.Now, unix.Poll)
}

// pollWith bounds waits against both caller deadlines and periodic cancellation checks.
func pollWith(ctx context.Context, fileDescriptor int, events int16, now func() time.Time, pollSystem pollCall) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		timeout := netlinkPollInterval
		if deadline, ok := ctx.Deadline(); ok {
			remaining := deadline.Sub(now())
			if remaining <= 0 {
				return context.DeadlineExceeded
			}
			if remaining < timeout {
				timeout = remaining
			}
		}
		milliseconds := int(timeout.Milliseconds())
		if milliseconds < 1 {
			milliseconds = 1
		}
		descriptors := []unix.PollFd{{Fd: int32(fileDescriptor), Events: events | unix.POLLERR | unix.POLLHUP}}
		ready, err := pollSystem(descriptors, milliseconds)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return fmt.Errorf("Linux netlink poll: %w", err)
		}
		if ready == 0 {
			continue
		}
		if descriptors[0].Revents&unix.POLLNVAL != 0 {
			return fmt.Errorf("Linux netlink poll reported an invalid descriptor")
		}
		return nil
	}
}

// parseDatagram rejects trailing fragments and replies for another local port or sequence.
func parseDatagram(datagram []byte, portID uint32, sequence uint32) ([]Message, error) {
	messages := make([]Message, 0, 8)
	for len(datagram) > 0 {
		if len(datagram) < unix.SizeofNlMsghdr {
			return nil, fmt.Errorf("Linux netlink datagram contains a short header")
		}
		messageLength := int(binary.NativeEndian.Uint32(datagram[0:4]))
		if messageLength < unix.SizeofNlMsghdr || messageLength > len(datagram) {
			return nil, fmt.Errorf("Linux netlink message length %d is invalid", messageLength)
		}
		alignedLength := align(messageLength)
		if alignedLength > len(datagram) {
			return nil, fmt.Errorf("Linux netlink message padding exceeds its datagram")
		}
		if binary.NativeEndian.Uint32(datagram[8:12]) != sequence || binary.NativeEndian.Uint32(datagram[12:16]) != portID {
			return nil, fmt.Errorf("Linux netlink reply does not match its request")
		}
		messages = append(messages, Message{
			Type:    binary.NativeEndian.Uint16(datagram[4:6]),
			Flags:   binary.NativeEndian.Uint16(datagram[6:8]),
			Payload: append([]byte(nil), datagram[unix.SizeofNlMsghdr:messageLength]...),
		})
		datagram = datagram[alignedLength:]
	}
	if len(messages) == 0 {
		return nil, fmt.Errorf("Linux netlink datagram contains no messages")
	}
	return messages, nil
}

// parseError translates the signed kernel errno while recognizing a zero-status ACK.
func parseError(payload []byte) error {
	if len(payload) < 4 {
		return fmt.Errorf("Linux netlink error reply is truncated")
	}
	code := int32(binary.NativeEndian.Uint32(payload[:4]))
	if code == 0 {
		return nil
	}
	if code > 0 {
		return fmt.Errorf("Linux netlink error reply has invalid code %d", code)
	}
	return &completedResponseError{cause: fmt.Errorf("Linux netlink request: %w", unix.Errno(-code))}
}

// parseDone accepts the optional zero status carried by modern multipart replies.
func parseDone(payload []byte) error {
	if len(payload) == 0 {
		return nil
	}
	if len(payload) < 4 {
		return fmt.Errorf("Linux netlink done reply is truncated")
	}
	code := int32(binary.NativeEndian.Uint32(payload[:4]))
	if code == 0 {
		return nil
	}
	if code > 0 {
		return fmt.Errorf("Linux netlink done reply has invalid code %d", code)
	}
	return &completedResponseError{cause: fmt.Errorf("Linux netlink dump: %w", unix.Errno(-code))}
}

// align applies the four-byte alignment shared by netlink messages and attributes.
func align(length int) int {
	return (length + netlinkAlignment - 1) & ^(netlinkAlignment - 1)
}
