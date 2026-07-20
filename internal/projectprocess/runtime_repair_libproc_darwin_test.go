//go:build darwin

package projectprocess

import (
	"encoding/binary"
	"math/bits"
	"net/netip"
	"syscall"
	"testing"
)

// TestDarwinRuntimeRepairCallResultAllowsEmptyDescriptorLists keeps zero-FD processes observable without weakening other libproc calls.
func TestDarwinRuntimeRepairCallResultAllowsEmptyDescriptorLists(t *testing.T) {
	if got, err := validateDarwinRuntimeRepairCallResultAllowEmpty(0, 0, darwinRuntimeRepairMaximumFDs*darwinRuntimeRepairFDInfoBytes); err != nil || got != 0 {
		t.Fatalf("allow-empty result = %d, %v", got, err)
	}
	if _, err := validateDarwinRuntimeRepairCallResult(0, 0, darwinRuntimeRepairMaximumFDs*darwinRuntimeRepairFDInfoBytes); err == nil {
		t.Fatal("generic zero result was accepted")
	}
	if _, err := validateDarwinRuntimeRepairCallResultAllowEmpty(0, syscall.EIO, darwinRuntimeRepairMaximumFDs*darwinRuntimeRepairFDInfoBytes); err == nil {
		t.Fatal("libproc error was swallowed for an empty result")
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
