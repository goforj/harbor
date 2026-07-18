//go:build linux

package linuxnetlink

import (
	"encoding/binary"
	"fmt"

	"golang.org/x/sys/unix"
)

// Attribute preserves both a bounded payload and the nested/network-byte-order encoding flags.
type Attribute struct {
	Flags   uint16
	Payload []byte
}

// MarshalAttribute appends one padded native Linux rtattr/nlattr value within the ABI's uint16 bound.
func MarshalAttribute(destination []byte, attributeType uint16, payload []byte) ([]byte, error) {
	if len(payload) > int(^uint16(0))-unix.SizeofRtAttr {
		return nil, fmt.Errorf("Linux netlink attribute %d exceeds its uint16 length bound", attributeType)
	}
	attributeLength := unix.SizeofRtAttr + len(payload)
	start := len(destination)
	destination = append(destination, make([]byte, align(attributeLength))...)
	binary.NativeEndian.PutUint16(destination[start:start+2], uint16(attributeLength))
	binary.NativeEndian.PutUint16(destination[start+2:start+4], attributeType)
	copy(destination[start+unix.SizeofRtAttr:start+attributeLength], payload)
	return destination, nil
}

// ParseAttributes preserves repeated attributes so a duplicate authority field cannot be hidden.
func ParseAttributes(payload []byte) (map[uint16][]Attribute, error) {
	attributes := make(map[uint16][]Attribute)
	for len(payload) > 0 {
		if len(payload) < unix.SizeofRtAttr {
			return nil, fmt.Errorf("Linux netlink attribute has a short header")
		}
		attributeLength := int(binary.NativeEndian.Uint16(payload[0:2]))
		if attributeLength < unix.SizeofRtAttr || attributeLength > len(payload) {
			return nil, fmt.Errorf("Linux netlink attribute length %d is invalid", attributeLength)
		}
		alignedLength := align(attributeLength)
		if alignedLength > len(payload) {
			return nil, fmt.Errorf("Linux netlink attribute padding exceeds its message")
		}
		rawType := binary.NativeEndian.Uint16(payload[2:4])
		attributeType := rawType & 0x3fff
		value := append([]byte(nil), payload[unix.SizeofRtAttr:attributeLength]...)
		attributes[attributeType] = append(attributes[attributeType], Attribute{Flags: rawType & 0xc000, Payload: value})
		payload = payload[alignedLength:]
	}
	return attributes, nil
}

// OneAttribute returns one unflagged value and rejects duplicate or encoded authority fields.
func OneAttribute(attributes map[uint16][]Attribute, attributeType uint16) ([]byte, bool, error) {
	values := attributes[attributeType]
	if len(values) == 0 {
		return nil, false, nil
	}
	if len(values) != 1 {
		return nil, false, fmt.Errorf("Linux netlink attribute %d appears %d times", attributeType, len(values))
	}
	if values[0].Flags != 0 {
		return nil, false, fmt.Errorf("Linux netlink attribute %d uses unsupported encoding flags", attributeType)
	}
	return values[0].Payload, true, nil
}
