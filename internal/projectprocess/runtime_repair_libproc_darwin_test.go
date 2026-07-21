//go:build darwin

package projectprocess

import (
	"encoding/binary"
	"errors"
	"math/bits"
	"net/netip"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
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

// TestSameDarwinRuntimeRepairProcessFactsRejectsNonRootIdentityDrift keeps the final signal gate bound to every captured member fact.
func TestSameDarwinRuntimeRepairProcessFactsRejectsNonRootIdentityDrift(t *testing.T) {
	expected := []runtimeRepairProcessFact{
		{
			PID:                10,
			BirthToken:         "darwin:10:1",
			ParentPID:          1,
			ProcessGroupID:     10,
			SessionID:          10,
			EffectiveUID:       501,
			RealUID:            501,
			ExecutableIdentity: "/opt/forj",
			ArgumentDigest:     "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			ArgumentCount:      2,
			CommandExact:       true,
			WorkingDirectory:   "/tmp/project",
		},
		{
			PID:                11,
			BirthToken:         "darwin:11:1",
			ParentPID:          10,
			ProcessGroupID:     10,
			SessionID:          10,
			EffectiveUID:       501,
			RealUID:            501,
			ExecutableIdentity: "/usr/bin/watcher",
			ArgumentDigest:     "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			ArgumentCount:      1,
			CommandExact:       false,
			WorkingDirectory:   "/tmp/project",
		},
	}
	observed := append([]runtimeRepairProcessFact(nil), expected...)
	if !sameDarwinRuntimeRepairProcessFacts(observed, expected) {
		t.Fatal("unchanged process facts were rejected")
	}
	mutations := []struct {
		name   string
		mutate func(*runtimeRepairProcessFact)
	}{
		{name: "birth", mutate: func(fact *runtimeRepairProcessFact) { fact.BirthToken = "darwin:11:2" }},
		{name: "parent", mutate: func(fact *runtimeRepairProcessFact) { fact.ParentPID = 12 }},
		{name: "process group", mutate: func(fact *runtimeRepairProcessFact) { fact.ProcessGroupID = 12 }},
		{name: "session", mutate: func(fact *runtimeRepairProcessFact) { fact.SessionID = 12 }},
		{name: "effective uid", mutate: func(fact *runtimeRepairProcessFact) { fact.EffectiveUID = 502 }},
		{name: "real uid", mutate: func(fact *runtimeRepairProcessFact) { fact.RealUID = 502 }},
		{name: "executable", mutate: func(fact *runtimeRepairProcessFact) { fact.ExecutableIdentity = "/usr/bin/other-watcher" }},
		{name: "argv digest", mutate: func(fact *runtimeRepairProcessFact) {
			fact.ArgumentDigest = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
		}},
		{name: "argv count", mutate: func(fact *runtimeRepairProcessFact) { fact.ArgumentCount = 2 }},
		{name: "command exactness", mutate: func(fact *runtimeRepairProcessFact) { fact.CommandExact = true }},
		{name: "working directory", mutate: func(fact *runtimeRepairProcessFact) { fact.WorkingDirectory = "/tmp/other-project" }},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			observed := append([]runtimeRepairProcessFact(nil), expected...)
			mutation.mutate(&observed[1])
			if sameDarwinRuntimeRepairProcessFacts(observed, expected) {
				t.Fatalf("non-root %s drift was accepted", mutation.name)
			}
		})
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

// TestParseDarwinRuntimeRepairSocketFDRequiresOwnedOpaqueListener covers exact and wildcard endpoint, state, and kernel identity checks.
func TestParseDarwinRuntimeRepairSocketFDRequiresOwnedOpaqueListener(t *testing.T) {
	endpoint := netip.MustParseAddrPort("127.0.0.42:38473")
	raw := darwinRuntimeRepairTestSocketRecord(endpoint)
	fact, matches, err := parseDarwinRuntimeRepairSocketFD(raw, 100, 7, endpoint)
	if err != nil || !matches {
		t.Fatalf("parseDarwinRuntimeRepairSocketFD() = %#v, %t, %v", fact, matches, err)
	}
	if fact.OwnerPID != 100 || fact.FileDescriptor != 7 || fact.SocketHandle != 11 || fact.PCBHandle != 12 || fact.Generation != 13 || fact.Endpoint != endpoint {
		t.Fatalf("socket fact = %#v", fact)
	}
	wildcard := append([]byte(nil), raw...)
	for index := 0; index < 4; index++ {
		wildcard[darwinRuntimeRepairIPv4AddressOffset+index] = 0
	}
	wildcardFact, wildcardMatches, wildcardErr := parseDarwinRuntimeRepairSocketFD(wildcard, 100, 7, endpoint)
	if wildcardErr != nil || !wildcardMatches || wildcardFact.Endpoint != endpoint {
		t.Fatalf("wildcard socket = %#v, %t, %v; want normalized target endpoint", wildcardFact, wildcardMatches, wildcardErr)
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

// TestParseDarwinRuntimeRepairSocketFDAdmitsIPv4CapableIPv6Listeners proves wildcard dual-stack binds cannot strand a project-owned port.
func TestParseDarwinRuntimeRepairSocketFDAdmitsIPv4CapableIPv6Listeners(t *testing.T) {
	endpoint := netip.MustParseAddrPort("127.0.0.42:38473")
	tests := []struct {
		name     string
		vflag    uint8
		wildcard bool
	}{
		{name: "IPv4 capable exact", vflag: darwinRuntimeRepairIPv4Flag},
		{name: "IPv4 capable wildcard", vflag: darwinRuntimeRepairIPv4Flag, wildcard: true},
		{name: "dual stack wildcard", vflag: darwinRuntimeRepairIPv4Flag | darwinRuntimeRepairIPv6Flag, wildcard: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			raw := darwinRuntimeRepairTestSocketRecord(endpoint)
			binary.LittleEndian.PutUint32(raw[darwinRuntimeRepairSocketFamilyOffset:], uint32(unix.AF_INET6))
			raw[darwinRuntimeRepairIPv4FlagOffset] = test.vflag
			if test.wildcard {
				for index := 0; index < 4; index++ {
					raw[darwinRuntimeRepairIPv4AddressOffset+index] = 0
				}
			}
			fact, matches, err := parseDarwinRuntimeRepairSocketFD(raw, 100, 7, endpoint)
			if err != nil || !matches || fact.Endpoint != endpoint {
				t.Fatalf("IPv4-capable listener = %#v, %t, %v; want normalized target endpoint", fact, matches, err)
			}
		})
	}

	noncanonical := darwinRuntimeRepairTestSocketRecord(endpoint)
	binary.LittleEndian.PutUint32(noncanonical[darwinRuntimeRepairSocketFamilyOffset:], uint32(unix.AF_INET6))
	noncanonical[darwinRuntimeRepairIPv4FlagOffset] = darwinRuntimeRepairIPv4Flag
	noncanonical[darwinRuntimeRepairIPv4AddressPrefixOffset] = 1
	if _, _, err := parseDarwinRuntimeRepairSocketFD(noncanonical, 100, 7, endpoint); err == nil {
		t.Fatal("AF_INET6 listener with nonzero IPv4 padding passed parsing")
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
