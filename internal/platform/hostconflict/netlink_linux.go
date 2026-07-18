//go:build linux

package hostconflict

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"time"

	"golang.org/x/sys/unix"
)

const (
	linuxNetlinkAlignment      = 4
	linuxMaximumDatagramBytes  = 1 << 20
	linuxMaximumReplyBytes     = 16 << 20
	linuxMaximumReplyMessages  = 65536
	linuxMaximumReplyDatagrams = 4096
	linuxNetlinkPollInterval   = 100 * time.Millisecond
)

var errLinuxNetlinkInterrupted = errors.New("host conflict Linux netlink transaction was interrupted")
var errLinuxNetlinkReplyLimit = errors.New("host conflict Linux netlink reply exceeded its transaction bound")

// linuxNetlinkMessage contains one payload whose header was verified against the active request.
type linuxNetlinkMessage struct {
	messageType uint16
	flags       uint16
	payload     []byte
}

// linuxNetlinkAttribute retains NLA encoding flags until a field-specific codec validates them.
type linuxNetlinkAttribute struct {
	flags   uint16
	payload []byte
}

// linuxNetlinkReply preserves useful facts while marking kernel dumps that exceeded a safety bound.
type linuxNetlinkReply struct {
	messages  []linuxNetlinkMessage
	truncated bool
}

// linuxNetlinkDatagramSource supplies one exact kernel datagram to the transaction state machine.
type linuxNetlinkDatagramSource func(context.Context, int) ([]byte, *unix.SockaddrNetlink, bool, error)

// linuxNetlinkRecvmsg matches the Linux syscall while allowing deterministic truncation fixtures.
type linuxNetlinkRecvmsg func(int, []byte, []byte, int) (int, int, int, unix.Sockaddr, error)

// linuxNetlinkPoller waits for one requested readiness class.
type linuxNetlinkPoller func(context.Context, int, int16) error

// linuxNetlinkSendto matches the one-shot datagram syscall used by request publication.
type linuxNetlinkSendto func(int, []byte, int, unix.Sockaddr) error

// linuxPollCall matches poll(2) while permitting deterministic readiness and error fixtures.
type linuxPollCall func([]unix.PollFd, int) (int, error)

// linuxNetlinkExchanger isolates codecs and completeness policy from live kernel sockets in tests.
type linuxNetlinkExchanger interface {
	exchange(context.Context, uint16, uint16, []byte, bool) (linuxNetlinkReply, error)
}

// linuxNetlinkClient owns one kernel netlink port so every reply can be bound to its request sequence.
type linuxNetlinkClient struct {
	fileDescriptor int
	portID         uint32
	sequence       uint32
	closeFile      func(int) error
}

// linuxNetlinkOpenOperations isolates setup failures without weakening the live socket configuration.
type linuxNetlinkOpenOperations struct {
	socket          func(int, int, int) (int, error)
	bind            func(int, unix.Sockaddr) error
	setSocketOption func(int, int, int, int) error
	localAddress    func(int) (unix.Sockaddr, error)
	close           func(int) error
}

// openLinuxNetlink opens a nonblocking kernel-only netlink client for one protocol.
func openLinuxNetlink(protocol int) (*linuxNetlinkClient, error) {
	operations := linuxNetlinkOpenOperations{
		socket:          unix.Socket,
		bind:            unix.Bind,
		setSocketOption: unix.SetsockoptInt,
		localAddress:    unix.Getsockname,
		close:           unix.Close,
	}
	return openLinuxNetlinkWith(protocol, operations)
}

// openLinuxNetlinkWith applies every strict option before exposing a usable netlink port.
func openLinuxNetlinkWith(protocol int, operations linuxNetlinkOpenOperations) (*linuxNetlinkClient, error) {
	fileDescriptor, err := operations.socket(unix.AF_NETLINK, unix.SOCK_RAW|unix.SOCK_CLOEXEC|unix.SOCK_NONBLOCK, protocol)
	if err != nil {
		return nil, fmt.Errorf("host conflict Linux netlink socket: %w", err)
	}
	closeOnError := true
	defer func() {
		if closeOnError {
			_ = operations.close(fileDescriptor)
		}
	}()

	if err := operations.bind(fileDescriptor, &unix.SockaddrNetlink{Family: unix.AF_NETLINK}); err != nil {
		return nil, fmt.Errorf("host conflict Linux netlink bind: %w", err)
	}
	for _, option := range []int{unix.NETLINK_EXT_ACK, unix.NETLINK_CAP_ACK, unix.NETLINK_GET_STRICT_CHK} {
		if err := operations.setSocketOption(fileDescriptor, unix.SOL_NETLINK, option, 1); err != nil {
			return nil, fmt.Errorf("host conflict Linux enable netlink option %d: %w", option, err)
		}
	}
	address, err := operations.localAddress(fileDescriptor)
	if err != nil {
		return nil, fmt.Errorf("host conflict Linux netlink local address: %w", err)
	}
	netlinkAddress, ok := address.(*unix.SockaddrNetlink)
	if !ok || netlinkAddress.Pid == 0 || netlinkAddress.Groups != 0 {
		return nil, fmt.Errorf("host conflict Linux netlink local address is not an unicast port")
	}

	closeOnError = false
	return &linuxNetlinkClient{fileDescriptor: fileDescriptor, portID: netlinkAddress.Pid, closeFile: operations.close}, nil
}

// close releases the netlink port before another observation pass starts.
func (client *linuxNetlinkClient) close() error {
	if err := client.closeFile(client.fileDescriptor); err != nil {
		return fmt.Errorf("host conflict Linux netlink close: %w", err)
	}
	return nil
}

// exchange sends one request and accepts only the kernel replies for its exact port and sequence.
func (client *linuxNetlinkClient) exchange(ctx context.Context, messageType uint16, flags uint16, payload []byte, multipart bool) (linuxNetlinkReply, error) {
	client.sequence++
	if client.sequence == 0 {
		client.sequence++
	}
	request := marshalLinuxNetlinkRequest(messageType, flags|unix.NLM_F_REQUEST, client.sequence, client.portID, payload)
	if err := sendLinuxNetlink(ctx, client.fileDescriptor, request); err != nil {
		return linuxNetlinkReply{}, err
	}
	return receiveLinuxNetlink(ctx, client.fileDescriptor, client.portID, client.sequence, multipart)
}

// marshalLinuxNetlinkRequest creates an aligned native-endian message accepted by the Linux kernel ABI.
func marshalLinuxNetlinkRequest(messageType uint16, flags uint16, sequence uint32, portID uint32, payload []byte) []byte {
	messageLength := unix.SizeofNlMsghdr + len(payload)
	message := make([]byte, alignLinuxNetlink(messageLength))
	binary.NativeEndian.PutUint32(message[0:4], uint32(messageLength))
	binary.NativeEndian.PutUint16(message[4:6], messageType)
	binary.NativeEndian.PutUint16(message[6:8], flags)
	binary.NativeEndian.PutUint32(message[8:12], sequence)
	binary.NativeEndian.PutUint32(message[12:16], portID)
	copy(message[unix.SizeofNlMsghdr:], payload)
	return message
}

// sendLinuxNetlink waits in bounded intervals so cancellation remains effective on a saturated socket.
func sendLinuxNetlink(ctx context.Context, fileDescriptor int, request []byte) error {
	return sendLinuxNetlinkWith(ctx, fileDescriptor, request, unix.Sendto, pollLinuxNetlink)
}

// sendLinuxNetlinkWith retries only interruption and backpressure while preserving cancellation.
func sendLinuxNetlinkWith(ctx context.Context, fileDescriptor int, request []byte, sendto linuxNetlinkSendto, poll linuxNetlinkPoller) error {
	for {
		err := sendto(fileDescriptor, request, unix.MSG_DONTWAIT, &unix.SockaddrNetlink{Family: unix.AF_NETLINK})
		if err == nil {
			return nil
		}
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if !errors.Is(err, unix.EAGAIN) && !errors.Is(err, unix.EWOULDBLOCK) {
			return fmt.Errorf("host conflict Linux netlink send: %w", err)
		}
		if err := poll(ctx, fileDescriptor, unix.POLLOUT); err != nil {
			return err
		}
	}
}

// receiveLinuxNetlink rejects bounds terminally so no later request can consume a poisoned multipart stream.
func receiveLinuxNetlink(ctx context.Context, fileDescriptor int, portID uint32, sequence uint32, multipart bool) (linuxNetlinkReply, error) {
	return receiveLinuxNetlinkWith(ctx, fileDescriptor, portID, sequence, multipart, linuxMaximumReplyDatagrams, receiveLinuxNetlinkDatagram)
}

// receiveLinuxNetlinkWith keeps transaction completeness independently testable from recvmsg mechanics.
func receiveLinuxNetlinkWith(ctx context.Context, fileDescriptor int, portID uint32, sequence uint32, multipart bool, maximumDatagrams int, source linuxNetlinkDatagramSource) (linuxNetlinkReply, error) {
	reply := linuxNetlinkReply{}
	totalBytes := uint64(0)
	sawData := false
	replyMultipart := multipart
	for datagramIndex := 0; datagramIndex < maximumDatagrams; datagramIndex++ {
		datagram, address, oversized, err := source(ctx, fileDescriptor)
		if err != nil {
			if errors.Is(err, unix.ENOBUFS) {
				return linuxNetlinkReply{}, errLinuxNetlinkInterrupted
			}
			return linuxNetlinkReply{}, err
		}
		if address == nil || address.Pid != 0 || address.Groups != 0 {
			return linuxNetlinkReply{}, fmt.Errorf("host conflict Linux netlink reply did not originate from the kernel")
		}
		if oversized {
			return linuxNetlinkReply{}, errLinuxNetlinkReplyLimit
		}
		totalBytes += uint64(len(datagram))
		if totalBytes > uint64(linuxMaximumReplyBytes) {
			return linuxNetlinkReply{}, errLinuxNetlinkReplyLimit
		}

		messages, err := parseLinuxNetlinkDatagram(datagram, portID, sequence)
		if err != nil {
			return linuxNetlinkReply{}, err
		}
		done := false
		for _, message := range messages {
			if done {
				return linuxNetlinkReply{}, fmt.Errorf("host conflict Linux netlink reply contains data after DONE")
			}
			if message.flags&unix.NLM_F_MULTI != 0 {
				replyMultipart = true
			}
			if message.flags&unix.NLM_F_DUMP_INTR != 0 {
				return linuxNetlinkReply{}, errLinuxNetlinkInterrupted
			}
			switch message.messageType {
			case unix.NLMSG_NOOP:
				continue
			case unix.NLMSG_OVERRUN:
				return linuxNetlinkReply{}, errLinuxNetlinkInterrupted
			case unix.NLMSG_ERROR:
				if err := parseLinuxNetlinkError(message.payload); err != nil {
					return linuxNetlinkReply{}, err
				}
			case unix.NLMSG_DONE:
				if err := parseLinuxNetlinkDone(message.payload); err != nil {
					return linuxNetlinkReply{}, err
				}
				done = true
			default:
				sawData = true
				if len(reply.messages) >= linuxMaximumReplyMessages {
					return linuxNetlinkReply{}, errLinuxNetlinkReplyLimit
				}
				reply.messages = append(reply.messages, message)
			}
		}
		if done || (!replyMultipart && sawData) {
			return reply, nil
		}
	}
	return linuxNetlinkReply{}, errLinuxNetlinkReplyLimit
}

// receiveLinuxNetlinkDatagram peeks the kernel-reported datagram size before allocating bounded storage.
func receiveLinuxNetlinkDatagram(ctx context.Context, fileDescriptor int) ([]byte, *unix.SockaddrNetlink, bool, error) {
	return receiveLinuxNetlinkDatagramWith(ctx, fileDescriptor, pollLinuxNetlink, unix.Recvmsg)
}

// receiveLinuxNetlinkDatagramWith proves the peeked size still matches the consumed datagram.
func receiveLinuxNetlinkDatagramWith(ctx context.Context, fileDescriptor int, poll linuxNetlinkPoller, recvmsg linuxNetlinkRecvmsg) ([]byte, *unix.SockaddrNetlink, bool, error) {
	for {
		if err := poll(ctx, fileDescriptor, unix.POLLIN); err != nil {
			return nil, nil, false, err
		}
		probe := []byte{0}
		length, _, _, _, err := recvmsg(fileDescriptor, probe, nil, unix.MSG_PEEK|unix.MSG_TRUNC|unix.MSG_DONTWAIT)
		if errors.Is(err, unix.EINTR) || errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EWOULDBLOCK) {
			continue
		}
		if err != nil {
			return nil, nil, false, fmt.Errorf("host conflict Linux netlink peek: %w", err)
		}
		if length <= 0 {
			return nil, nil, false, fmt.Errorf("host conflict Linux netlink returned an empty datagram")
		}

		bufferLength := length
		oversized := length > linuxMaximumDatagramBytes
		if oversized {
			bufferLength = 1
		}
		buffer := make([]byte, bufferLength)
		received, _, receiveFlags, source, err := recvmsg(fileDescriptor, buffer, nil, unix.MSG_DONTWAIT)
		if errors.Is(err, unix.EINTR) || errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EWOULDBLOCK) {
			continue
		}
		if err != nil {
			return nil, nil, false, fmt.Errorf("host conflict Linux netlink receive: %w", err)
		}
		address, ok := source.(*unix.SockaddrNetlink)
		if !ok {
			return nil, nil, false, fmt.Errorf("host conflict Linux netlink reply has an unexpected address family")
		}
		if oversized {
			if receiveFlags&unix.MSG_TRUNC == 0 {
				return nil, nil, false, fmt.Errorf("host conflict Linux netlink oversized datagram was not truncated")
			}
			return nil, address, true, nil
		}
		if receiveFlags&unix.MSG_TRUNC != 0 || received != length {
			return nil, nil, false, fmt.Errorf("host conflict Linux netlink datagram size changed during receive")
		}
		return buffer[:received], address, false, nil
	}
}

// pollLinuxNetlink uses short polls because context cancellation is not a kernel netlink event.
func pollLinuxNetlink(ctx context.Context, fileDescriptor int, events int16) error {
	return pollLinuxNetlinkWith(ctx, fileDescriptor, events, time.Now, unix.Poll)
}

// pollLinuxNetlinkWith bounds waits against both the context deadline and periodic cancellation checks.
func pollLinuxNetlinkWith(ctx context.Context, fileDescriptor int, events int16, now func() time.Time, poll linuxPollCall) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		timeout := linuxNetlinkPollInterval
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
		pollDescriptors := []unix.PollFd{{Fd: int32(fileDescriptor), Events: events | unix.POLLERR | unix.POLLHUP}}
		ready, err := poll(pollDescriptors, milliseconds)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return fmt.Errorf("host conflict Linux netlink poll: %w", err)
		}
		if ready == 0 {
			continue
		}
		if pollDescriptors[0].Revents&unix.POLLNVAL != 0 {
			return fmt.Errorf("host conflict Linux netlink poll reported an invalid descriptor")
		}
		return nil
	}
}

// parseLinuxNetlinkDatagram rejects trailing fragments and replies for another local netlink port.
func parseLinuxNetlinkDatagram(datagram []byte, portID uint32, sequence uint32) ([]linuxNetlinkMessage, error) {
	messages := make([]linuxNetlinkMessage, 0, 8)
	for len(datagram) > 0 {
		if len(datagram) < unix.SizeofNlMsghdr {
			return nil, fmt.Errorf("host conflict Linux netlink datagram contains a short header")
		}
		messageLength := int(binary.NativeEndian.Uint32(datagram[0:4]))
		if messageLength < unix.SizeofNlMsghdr || messageLength > len(datagram) {
			return nil, fmt.Errorf("host conflict Linux netlink message length %d is invalid", messageLength)
		}
		alignedLength := alignLinuxNetlink(messageLength)
		if alignedLength > len(datagram) {
			return nil, fmt.Errorf("host conflict Linux netlink message padding exceeds its datagram")
		}
		messageSequence := binary.NativeEndian.Uint32(datagram[8:12])
		messagePortID := binary.NativeEndian.Uint32(datagram[12:16])
		if messageSequence != sequence || messagePortID != portID {
			return nil, fmt.Errorf("host conflict Linux netlink reply does not match its request")
		}
		payload := append([]byte(nil), datagram[unix.SizeofNlMsghdr:messageLength]...)
		messages = append(messages, linuxNetlinkMessage{
			messageType: binary.NativeEndian.Uint16(datagram[4:6]),
			flags:       binary.NativeEndian.Uint16(datagram[6:8]),
			payload:     payload,
		})
		datagram = datagram[alignedLength:]
	}
	if len(messages) == 0 {
		return nil, fmt.Errorf("host conflict Linux netlink datagram contains no messages")
	}
	return messages, nil
}

// parseLinuxNetlinkError translates the signed kernel errno while permitting an explicit zero ACK.
func parseLinuxNetlinkError(payload []byte) error {
	if len(payload) < 4 {
		return fmt.Errorf("host conflict Linux netlink error reply is truncated")
	}
	code := int32(binary.NativeEndian.Uint32(payload[:4]))
	if code == 0 {
		return nil
	}
	if code > 0 {
		return fmt.Errorf("host conflict Linux netlink error reply has invalid code %d", code)
	}
	return fmt.Errorf("host conflict Linux netlink request: %w", unix.Errno(-code))
}

// parseLinuxNetlinkDone accepts the optional zero status carried by modern multipart replies.
func parseLinuxNetlinkDone(payload []byte) error {
	if len(payload) == 0 {
		return nil
	}
	if len(payload) < 4 {
		return fmt.Errorf("host conflict Linux netlink done reply is truncated")
	}
	code := int32(binary.NativeEndian.Uint32(payload[:4]))
	if code == 0 {
		return nil
	}
	if code > 0 {
		return fmt.Errorf("host conflict Linux netlink done reply has invalid code %d", code)
	}
	return fmt.Errorf("host conflict Linux netlink dump: %w", unix.Errno(-code))
}

// marshalLinuxNetlinkAttribute appends one padded native Linux rtattr/nlattr value.
func marshalLinuxNetlinkAttribute(destination []byte, attributeType uint16, payload []byte) []byte {
	attributeLength := unix.SizeofRtAttr + len(payload)
	start := len(destination)
	destination = append(destination, make([]byte, alignLinuxNetlink(attributeLength))...)
	binary.NativeEndian.PutUint16(destination[start:start+2], uint16(attributeLength))
	binary.NativeEndian.PutUint16(destination[start+2:start+4], attributeType)
	copy(destination[start+unix.SizeofRtAttr:start+attributeLength], payload)
	return destination
}

// parseLinuxNetlinkAttributes preserves repeated attributes so critical duplicates cannot be hidden.
func parseLinuxNetlinkAttributes(payload []byte) (map[uint16][]linuxNetlinkAttribute, error) {
	attributes := make(map[uint16][]linuxNetlinkAttribute)
	for len(payload) > 0 {
		if len(payload) < unix.SizeofRtAttr {
			return nil, fmt.Errorf("host conflict Linux netlink attribute has a short header")
		}
		attributeLength := int(binary.NativeEndian.Uint16(payload[0:2]))
		if attributeLength < unix.SizeofRtAttr || attributeLength > len(payload) {
			return nil, fmt.Errorf("host conflict Linux netlink attribute length %d is invalid", attributeLength)
		}
		alignedLength := alignLinuxNetlink(attributeLength)
		if alignedLength > len(payload) {
			return nil, fmt.Errorf("host conflict Linux netlink attribute padding exceeds its message")
		}
		rawAttributeType := binary.NativeEndian.Uint16(payload[2:4])
		attributeType := rawAttributeType & 0x3fff
		value := append([]byte(nil), payload[unix.SizeofRtAttr:attributeLength]...)
		attributes[attributeType] = append(attributes[attributeType], linuxNetlinkAttribute{flags: rawAttributeType & 0xc000, payload: value})
		payload = payload[alignedLength:]
	}
	return attributes, nil
}

// alignLinuxNetlink applies the four-byte alignment shared by messages and attributes.
func alignLinuxNetlink(length int) int {
	return (length + linuxNetlinkAlignment - 1) & ^(linuxNetlinkAlignment - 1)
}
