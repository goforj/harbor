//go:build linux

package linuxnetlink

import (
	"reflect"
	"testing"

	"golang.org/x/sys/unix"
)

// TestAttributeCodecPreservesDuplicatesFlagsAndPadding proves no authority field can be hidden.
func TestAttributeCodecPreservesDuplicatesFlagsAndPadding(t *testing.T) {
	payload, err := MarshalAttribute(nil, unix.IFA_LOCAL, []byte{1, 2, 3, 4})
	if err != nil {
		t.Fatalf("MarshalAttribute(first) error = %v", err)
	}
	payload, err = MarshalAttribute(payload, unix.IFA_LOCAL, []byte{5, 6, 7, 8})
	if err != nil {
		t.Fatalf("MarshalAttribute(second) error = %v", err)
	}
	attributes, err := ParseAttributes(payload)
	if err != nil {
		t.Fatalf("ParseAttributes() error = %v", err)
	}
	if len(attributes[unix.IFA_LOCAL]) != 2 {
		t.Fatalf("IFA_LOCAL values = %d", len(attributes[unix.IFA_LOCAL]))
	}
	if _, _, err := OneAttribute(attributes, unix.IFA_LOCAL); err == nil {
		t.Fatal("OneAttribute(duplicate) error = nil")
	}
	if value, present, err := OneAttribute(attributes, unix.IFA_ADDRESS); err != nil || present || value != nil {
		t.Fatalf("OneAttribute(missing) = %v, %t, %v", value, present, err)
	}
	single := map[uint16][]Attribute{unix.IFA_ADDRESS: {{Payload: []byte{1, 2, 3, 4}}}}
	if value, present, err := OneAttribute(single, unix.IFA_ADDRESS); err != nil || !present || !reflect.DeepEqual(value, []byte{1, 2, 3, 4}) {
		t.Fatalf("OneAttribute(single) = %v, %t, %v", value, present, err)
	}

	flagged, err := MarshalAttribute(nil, unix.IFA_LOCAL|0x4000, []byte{1, 2, 3, 4})
	if err != nil {
		t.Fatalf("MarshalAttribute(flagged) error = %v", err)
	}
	attributes, err = ParseAttributes(flagged)
	if err != nil {
		t.Fatalf("ParseAttributes(flagged) error = %v", err)
	}
	if attributes[unix.IFA_LOCAL][0].Flags != 0x4000 || !reflect.DeepEqual(attributes[unix.IFA_LOCAL][0].Payload, []byte{1, 2, 3, 4}) {
		t.Fatalf("flagged attribute = %#v", attributes)
	}
	if _, _, err := OneAttribute(attributes, unix.IFA_LOCAL); err == nil {
		t.Fatal("OneAttribute(flagged) error = nil")
	}
	if encoded, err := MarshalAttribute(nil, unix.IFA_LOCAL, make([]byte, int(^uint16(0))-unix.SizeofRtAttr+1)); err == nil || encoded != nil {
		t.Fatalf("MarshalAttribute(oversized) = %d bytes, %v", len(encoded), err)
	}

	for name, malformed := range map[string][]byte{
		"short header":  {1, 2, 3},
		"short length":  {3, 0, 1, 0},
		"long length":   {8, 0, 1, 0},
		"short padding": {5, 0, 1, 0, 1},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseAttributes(malformed); err == nil {
				t.Fatal("ParseAttributes() error = nil")
			}
		})
	}
}
