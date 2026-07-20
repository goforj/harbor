//go:build darwin

package projectprocess

import (
	"encoding/binary"
	"errors"
	"math/bits"
	"net/netip"
	"syscall"
	"testing"
)

// TestValidateDarwinRuntimeRepairCallResultAllowsEmptyFDInventory preserves zero as valid only for descriptor census reads.
func TestValidateDarwinRuntimeRepairCallResultAllowsEmptyFDInventory(t *testing.T) {
	if got, err := validateDarwinRuntimeRepairCallResultAllowEmpty(0, 0, darwinRuntimeRepairFDInfoBytes); err != nil || got != 0 {
		t.Fatalf("empty descriptor result = %d, %v", got, err)
	}
	if _, err := validateDarwinRuntimeRepairCallResult(0, 0, darwinRuntimeRepairFDInfoBytes); !errors.Is(err, errDarwinRuntimeRepairUnreadable) {
		t.Fatalf("empty non-descriptor result error = %v", err)
	}
	if _, err := validateDarwinRuntimeRepairCallResultAllowEmpty(0, syscall.EIO, darwinRuntimeRepairFDInfoBytes); !errors.Is(err, syscall.EIO) {
		t.Fatalf("empty descriptor syscall failure = %v", err)
	}
	if _, err := validateDarwinRuntimeRepairCallResultAllowEmpty(darwinRuntimeRepairFDInfoBytes+1, 0, darwinRuntimeRepairFDInfoBytes); !errors.Is(err, errDarwinRuntimeRepairUnreadable) {
		t.Fatalf("oversized descriptor result error = %v", err)
	}
}

// TestDarwinRuntimeRepairFDReadBounds preserves a stable descriptor census without admitting an exhausted buffer.
func TestDarwinRuntimeRepairFDReadBounds(t *testing.T) {
	maximumBytes := darwinRuntimeRepairMaximumFDs * darwinRuntimeRepairFDInfoBytes
	bufferBytes, err := darwinRuntimeRepairFDReadBufferBytes()
	if err != nil || bufferBytes != maximumBytes+darwinRuntimeRepairFDReadSpareRecords*darwinRuntimeRepairFDInfoBytes {
		t.Fatalf("darwinRuntimeRepairFDReadBufferBytes() = %d, %v", bufferBytes, err)
	}

	tests := []struct {
		name        string
		written     int
		bufferBytes int
		want        error
	}{
		{name: "stable", written: darwinRuntimeRepairFDInfoBytes, bufferBytes: bufferBytes},
		{name: "empty", bufferBytes: bufferBytes},
		{name: "partial record", written: darwinRuntimeRepairFDInfoBytes - 1, bufferBytes: bufferBytes, want: errDarwinRuntimeRepairUnstable},
		{name: "saturated", written: bufferBytes, bufferBytes: bufferBytes, want: errDarwinRuntimeRepairUnstable},
		{name: "over limit", written: maximumBytes + darwinRuntimeRepairFDInfoBytes, bufferBytes: maximumBytes + 2*darwinRuntimeRepairFDInfoBytes, want: errDarwinRuntimeRepairUnreadable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := validateDarwinRuntimeRepairFDReadLength(test.written, test.bufferBytes); !errors.Is(err, test.want) {
				t.Fatalf("validateDarwinRuntimeRepairFDReadLength() error = %v, want %v", err, test.want)
			}
		})
	}
}

// TestSameDarwinRuntimeRepairSessionMembersRejectsBirthOrScopeDrift keeps the final signal gate exact.
func TestSameDarwinRuntimeRepairSessionMembersRejectsBirthOrScopeDrift(t *testing.T) {
	expected := []runtimeRepairProcessFact{
		{PID: 10, BirthToken: "darwin:10:1"},
		{PID: 11, BirthToken: "darwin:11:1"},
	}
	observed := []unixProcessMember{
		{PID: 10, BirthToken: "darwin:10:1"},
		{PID: 11, BirthToken: "darwin:11:1"},
	}
	if !sameDarwinRuntimeRepairSessionMembers(observed, expected) {
		t.Fatal("unchanged session members were rejected")
	}
	observed[1].BirthToken = "darwin:11:2"
	if sameDarwinRuntimeRepairSessionMembers(observed, expected) {
		t.Fatal("birth drift was accepted")
	}
	observed = append(observed, unixProcessMember{PID: 12, BirthToken: "darwin:12:1"})
	if sameDarwinRuntimeRepairSessionMembers(observed, expected) {
		t.Fatal("new session member was accepted")
	}
}

// darwinRuntimeRepairTestSocketRecord builds the exact reviewed listener envelope for parser mutations.
func darwinRuntimeRepairTestSocketRecord(endpoint netip.AddrPort) []byte {
	raw := make([]byte, darwinRuntimeRepairSocketFDInfoBytes)
	binary.LittleEndian.PutUint64(raw[darwinRuntimeRepairSocketHandleOffset:], 11)
	binary.LittleEndian.PutUint64(raw[darwinRuntimeRepairPCBHandleOffset:], 12)
	binary.LittleEndian.PutUint32(raw[darwinRuntimeRepairSocketFamilyOffset:], uint32(2))
	binary.LittleEndian.PutUint32(raw[darwinRuntimeRepairSocketKindOffset:], darwinRuntimeRepairSocketInfoTCP)
	binary.LittleEndian.PutUint32(raw[darwinRuntimeRepairLocalPortOffset:], uint32(bits.ReverseBytes16(endpoint.Port())))
	binary.LittleEndian.PutUint64(raw[darwinRuntimeRepairGenerationOffset:], 13)
	raw[darwinRuntimeRepairIPv4FlagOffset] = darwinRuntimeRepairIPv4Flag
	address := endpoint.Addr().As4()
	copy(raw[darwinRuntimeRepairIPv4AddressOffset:], address[:])
	binary.LittleEndian.PutUint32(raw[darwinRuntimeRepairTCPStateOffset:], darwinRuntimeRepairTCPStateListen)
	return raw
}

// TestParseDarwinRuntimeRepairFDsRejectsPartialAndAmbiguousRecords verifies descriptor enumeration fails closed.
func TestParseDarwinRuntimeRepairFDsRejectsPartialAndAmbiguousRecords(t *testing.T) {
	raw := make([]byte, darwinRuntimeRepairFDInfoBytes*2)
	binary.LittleEndian.PutUint32(raw[0:], 7)
	binary.LittleEndian.PutUint32(raw[4:], darwinRuntimeRepairFDTypeSocket)
	binary.LittleEndian.PutUint32(raw[8:], 8)
	binary.LittleEndian.PutUint32(raw[12:], 1)
	fds, err := parseDarwinRuntimeRepairFDs(raw)
	if err != nil || len(fds) != 2 || fds[0].FileDescriptor != 7 || fds[0].Type != darwinRuntimeRepairFDTypeSocket {
		t.Fatalf("parseDarwinRuntimeRepairFDs() = %#v, %v", fds, err)
	}
	if _, err := parseDarwinRuntimeRepairFDs(raw[:len(raw)-1]); err == nil {
		t.Fatal("partial descriptor record passed parsing")
	}
	binary.LittleEndian.PutUint32(raw[8:], 7)
	if _, err := parseDarwinRuntimeRepairFDs(raw); err == nil {
		t.Fatal("duplicate descriptor passed parsing")
	}
	binary.LittleEndian.PutUint32(raw[8:], ^uint32(0))
	if _, err := parseDarwinRuntimeRepairFDs(raw); err == nil {
		t.Fatal("negative descriptor passed parsing")
	}
}

// TestParseDarwinRuntimeRepairSocketFDRequiresExactOpaqueListener covers endpoint, state, and kernel identity checks.
func TestParseDarwinRuntimeRepairSocketFDRequiresExactOpaqueListener(t *testing.T) {
	endpoint := netip.MustParseAddrPort("127.0.0.42:38473")
	raw := darwinRuntimeRepairTestSocketRecord(endpoint)
	fact, matches, err := parseDarwinRuntimeRepairSocketFD(raw, 100, 7, endpoint)
	if err != nil || !matches {
		t.Fatalf("parseDarwinRuntimeRepairSocketFD() = %#v, %t, %v", fact, matches, err)
	}
	if fact.OwnerPID != 100 || fact.FileDescriptor != 7 || fact.SocketHandle != 11 || fact.PCBHandle != 12 || fact.Generation != 13 || fact.Endpoint != endpoint {
		t.Fatalf("socket fact = %#v", fact)
	}

	notListening := append([]byte(nil), raw...)
	binary.LittleEndian.PutUint32(notListening[darwinRuntimeRepairTCPStateOffset:], 4)
	if _, matches, err := parseDarwinRuntimeRepairSocketFD(notListening, 100, 7, endpoint); err != nil || matches {
		t.Fatalf("non-listener = %t, %v", matches, err)
	}
	wrongAddress := append([]byte(nil), raw...)
	wrongAddress[darwinRuntimeRepairIPv4AddressOffset+3]++
	if _, matches, err := parseDarwinRuntimeRepairSocketFD(wrongAddress, 100, 7, endpoint); err != nil || matches {
		t.Fatalf("wrong address = %t, %v", matches, err)
	}
	missingIdentity := append([]byte(nil), raw...)
	binary.LittleEndian.PutUint64(missingIdentity[darwinRuntimeRepairPCBHandleOffset:], 0)
	if _, _, err := parseDarwinRuntimeRepairSocketFD(missingIdentity, 100, 7, endpoint); err == nil {
		t.Fatal("listener without PCB identity passed parsing")
	}
	if _, _, err := parseDarwinRuntimeRepairSocketFD(raw[:len(raw)-1], 100, 7, endpoint); err == nil {
		t.Fatal("short socket record passed parsing")
	}
}

// TestParseDarwinRuntimeRepairWorkingDirectoryRequiresTerminatedFullRecord verifies bounded path extraction.
func TestParseDarwinRuntimeRepairWorkingDirectoryRequiresTerminatedFullRecord(t *testing.T) {
	raw := make([]byte, darwinRuntimeRepairVnodePathInfoBytes)
	copy(raw[darwinRuntimeRepairWorkingDirectoryOffset:], "/tmp/project\x00")
	path, err := parseDarwinRuntimeRepairWorkingDirectory(raw)
	if err != nil || path != "/tmp/project" {
		t.Fatalf("parseDarwinRuntimeRepairWorkingDirectory() = %q, %v", path, err)
	}
	unterminated := make([]byte, darwinRuntimeRepairVnodePathInfoBytes)
	for index := darwinRuntimeRepairWorkingDirectoryOffset; index < darwinRuntimeRepairWorkingDirectoryOffset+darwinRuntimeRepairMaximumPathBytes; index++ {
		unterminated[index] = 'x'
	}
	if _, err := parseDarwinRuntimeRepairWorkingDirectory(unterminated); err == nil {
		t.Fatal("unterminated working directory passed parsing")
	}
	if _, err := parseDarwinRuntimeRepairWorkingDirectory(raw[:len(raw)-1]); err == nil {
		t.Fatal("short vnode path record passed parsing")
	}
}

// TestParseDarwinRuntimeRepairArgumentsExcludesEnvironment verifies argc controls the retained transient vector.
func TestParseDarwinRuntimeRepairArgumentsExcludesEnvironment(t *testing.T) {
	raw := make([]byte, 4)
	binary.LittleEndian.PutUint32(raw, 2)
	raw = append(raw, []byte("/opt/goforj/bin/forj\x00\x00/usr/local/bin/forj\x00dev\x00SECRET=value\x00")...)
	arguments, err := parseDarwinRuntimeRepairArguments(raw)
	if err != nil {
		t.Fatalf("parseDarwinRuntimeRepairArguments() error = %v", err)
	}
	if len(arguments) != 2 || arguments[0] != "/usr/local/bin/forj" || arguments[1] != "dev" {
		t.Fatalf("arguments = %#v", arguments)
	}
	if _, err := parseDarwinRuntimeRepairArguments(raw[:len(raw)-len("dev\x00SECRET=value\x00")]); err == nil {
		t.Fatal("truncated argv passed parsing")
	}
	badCount := append([]byte(nil), raw...)
	binary.LittleEndian.PutUint32(badCount, runtimeRepairMaximumArguments+1)
	if _, err := parseDarwinRuntimeRepairArguments(badCount); err == nil {
		t.Fatal("excessive argc passed parsing")
	}
}
