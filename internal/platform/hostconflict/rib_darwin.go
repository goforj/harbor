//go:build darwin

package hostconflict

import (
	"encoding/binary"
	"fmt"
)

const (
	// XNU routing messages expose RTM_VERSION in every frame, unlike the private PCB records.
	darwinRoutingMessageVersion = 5
	darwinRIBAlignment          = 4
	maximumDarwinRIBMessage     = 1 << 20
	maximumDarwinRIBMessages    = 65536
	maximumDarwinInterfaceRIB   = 16 << 20
	maximumDarwinRouteRIB       = 32 << 20
)

// validateDarwinRIBFrames rejects messages that x/net/route would otherwise skip as unknown.
func validateDarwinRIBFrames(raw []byte, maximumBytes int, allowedTypes map[uint8]struct{}) (int, error) {
	if len(raw) > maximumBytes {
		return 0, fmt.Errorf("host conflict Darwin RIB exceeds %d bytes", maximumBytes)
	}
	messages := 0
	for len(raw) > 0 {
		if len(raw) < 4 {
			return 0, fmt.Errorf("host conflict Darwin RIB contains a truncated header")
		}
		length := int(binary.NativeEndian.Uint16(raw[:2]))
		if length < 4 || length > len(raw) || length > maximumDarwinRIBMessage || length%darwinRIBAlignment != 0 {
			return 0, fmt.Errorf("host conflict Darwin RIB message has invalid length %d", length)
		}
		if raw[2] != darwinRoutingMessageVersion {
			return 0, fmt.Errorf("host conflict Darwin RIB message has unsupported version %d", raw[2])
		}
		if _, allowed := allowedTypes[raw[3]]; !allowed {
			return 0, fmt.Errorf("host conflict Darwin RIB message has unsupported type %d", raw[3])
		}
		messages++
		if messages > maximumDarwinRIBMessages {
			return 0, fmt.Errorf("host conflict Darwin RIB exceeds %d messages", maximumDarwinRIBMessages)
		}
		raw = raw[length:]
	}
	return messages, nil
}
