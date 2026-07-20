//go:build darwin

package projectprocess

import (
	"encoding/binary"
	"fmt"
	"math/bits"
	"net/netip"
	"runtime"
	"slices"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

const (
	// darwinRuntimeRepairPIDListFDs selects XNU's fixed proc_fdinfo array.
	darwinRuntimeRepairPIDListFDs = 1
	// darwinRuntimeRepairPIDVnodePathInfo selects the current and root vnode paths.
	darwinRuntimeRepairPIDVnodePathInfo = 9
	// darwinRuntimeRepairPIDFDSocketInfo selects XNU's socket_fdinfo record.
	darwinRuntimeRepairPIDFDSocketInfo = 3
	// darwinRuntimeRepairFDTypeSocket identifies descriptors accepted by proc_pidfdinfo.
	darwinRuntimeRepairFDTypeSocket = 2
	// darwinRuntimeRepairSocketInfoTCP identifies the TCP member of socket_info's protocol union.
	darwinRuntimeRepairSocketInfoTCP = 2
	// darwinRuntimeRepairTCPStateListen identifies a TCP socket accepting new connections.
	darwinRuntimeRepairTCPStateListen = 1
	// darwinRuntimeRepairIPv4Flag proves in_sockinfo contains an IPv4 address.
	darwinRuntimeRepairIPv4Flag = 1
	// darwinRuntimeRepairMaximumFDs bounds one process descriptor census.
	darwinRuntimeRepairMaximumFDs = 4096
	// darwinRuntimeRepairFDReadSpareRecords lets proc_pidinfo reveal a growing descriptor table without treating a complete read as a race.
	darwinRuntimeRepairFDReadSpareRecords = 20
	// darwinRuntimeRepairMaximumArgumentBytes bounds kern.procargs2 before parsing.
	darwinRuntimeRepairMaximumArgumentBytes = 1 << 20
	// darwinRuntimeRepairProcessPathBytes matches PROC_PIDPATHINFO_MAXSIZE.
	darwinRuntimeRepairProcessPathBytes = 4096
	// darwinRuntimeRepairMaximumPathBytes matches XNU's MAXPATHLEN.
	darwinRuntimeRepairMaximumPathBytes = 1024
	// darwinRuntimeRepairFDInfoBytes pins struct proc_fdinfo on Darwin LP64.
	darwinRuntimeRepairFDInfoBytes = 8
	// darwinRuntimeRepairVnodePathInfoBytes pins struct proc_vnodepathinfo on Darwin LP64.
	darwinRuntimeRepairVnodePathInfoBytes = 2352
	// darwinRuntimeRepairSocketFDInfoBytes pins struct socket_fdinfo on Darwin LP64.
	darwinRuntimeRepairSocketFDInfoBytes = 792
	// darwinRuntimeRepairWorkingDirectoryOffset pins pvi_cdir.vip_path.
	darwinRuntimeRepairWorkingDirectoryOffset = 152
	// darwinRuntimeRepairSocketHandleOffset pins socket_info.soi_so in socket_fdinfo.
	darwinRuntimeRepairSocketHandleOffset = 160
	// darwinRuntimeRepairPCBHandleOffset pins socket_info.soi_pcb in socket_fdinfo.
	darwinRuntimeRepairPCBHandleOffset = 168
	// darwinRuntimeRepairSocketFamilyOffset pins socket_info.soi_family in socket_fdinfo.
	darwinRuntimeRepairSocketFamilyOffset = 184
	// darwinRuntimeRepairSocketKindOffset pins socket_info.soi_kind in socket_fdinfo.
	darwinRuntimeRepairSocketKindOffset = 256
	// darwinRuntimeRepairLocalPortOffset pins in_sockinfo.insi_lport in socket_fdinfo.
	darwinRuntimeRepairLocalPortOffset = 268
	// darwinRuntimeRepairGenerationOffset pins in_sockinfo.insi_gencnt in socket_fdinfo.
	darwinRuntimeRepairGenerationOffset = 272
	// darwinRuntimeRepairIPv4FlagOffset pins in_sockinfo.insi_vflag in socket_fdinfo.
	darwinRuntimeRepairIPv4FlagOffset = 288
	// darwinRuntimeRepairIPv4AddressOffset pins the IPv4 suffix of in_sockinfo's local address union.
	darwinRuntimeRepairIPv4AddressOffset = 324
	// darwinRuntimeRepairTCPStateOffset pins tcp_sockinfo.tcpsi_state in socket_fdinfo.
	darwinRuntimeRepairTCPStateOffset = 344
)

// darwinRuntimeRepairFDInfoABI pins the complete proc_fdinfo record.
type darwinRuntimeRepairFDInfoABI struct {
	FileDescriptor int32
	Type           uint32
}

// darwinRuntimeRepairVnodePathInfoABI pins both vnode_info_path records and the current-directory path offset.
type darwinRuntimeRepairVnodePathInfoABI struct {
	CurrentPrefix [darwinRuntimeRepairWorkingDirectoryOffset]byte
	CurrentPath   [darwinRuntimeRepairMaximumPathBytes]byte
	RootPrefix    [darwinRuntimeRepairWorkingDirectoryOffset]byte
	RootPath      [darwinRuntimeRepairMaximumPathBytes]byte
}

// darwinRuntimeRepairSocketFDInfoABI pins every socket field retained in the opaque receipt.
type darwinRuntimeRepairSocketFDInfoABI struct {
	BeforeSocket       [darwinRuntimeRepairSocketHandleOffset]byte
	SocketHandle       uint64
	PCBHandle          uint64
	BeforeFamily       [8]byte
	Family             int32
	BeforeKind         [68]byte
	Kind               int32
	BeforeForeignPort  [4]byte
	ForeignPort        int32
	LocalPort          int32
	Generation         uint64
	BeforeIPv4Flag     [8]byte
	IPv4Flag           uint8
	BeforeIPv4Address  [35]byte
	IPv4Address        [4]byte
	BeforeTCPState     [16]byte
	TCPState           int32
	BeforeOpaqueTCPPCB [28]byte
	OpaqueTCPPCB       uint64
	Tail               [408]byte
}

var (
	_ [darwinRuntimeRepairFDInfoBytes - int(unsafe.Sizeof(darwinRuntimeRepairFDInfoABI{}))]byte
	_ [int(unsafe.Sizeof(darwinRuntimeRepairFDInfoABI{})) - darwinRuntimeRepairFDInfoBytes]byte
	_ [darwinRuntimeRepairVnodePathInfoBytes - int(unsafe.Sizeof(darwinRuntimeRepairVnodePathInfoABI{}))]byte
	_ [int(unsafe.Sizeof(darwinRuntimeRepairVnodePathInfoABI{})) - darwinRuntimeRepairVnodePathInfoBytes]byte
	_ [darwinRuntimeRepairSocketFDInfoBytes - int(unsafe.Sizeof(darwinRuntimeRepairSocketFDInfoABI{}))]byte
	_ [int(unsafe.Sizeof(darwinRuntimeRepairSocketFDInfoABI{})) - darwinRuntimeRepairSocketFDInfoBytes]byte

	_ [darwinRuntimeRepairWorkingDirectoryOffset - int(unsafe.Offsetof(darwinRuntimeRepairVnodePathInfoABI{}.CurrentPath))]byte
	_ [int(unsafe.Offsetof(darwinRuntimeRepairVnodePathInfoABI{}.CurrentPath)) - darwinRuntimeRepairWorkingDirectoryOffset]byte
	_ [darwinRuntimeRepairSocketHandleOffset - int(unsafe.Offsetof(darwinRuntimeRepairSocketFDInfoABI{}.SocketHandle))]byte
	_ [int(unsafe.Offsetof(darwinRuntimeRepairSocketFDInfoABI{}.SocketHandle)) - darwinRuntimeRepairSocketHandleOffset]byte
	_ [darwinRuntimeRepairPCBHandleOffset - int(unsafe.Offsetof(darwinRuntimeRepairSocketFDInfoABI{}.PCBHandle))]byte
	_ [int(unsafe.Offsetof(darwinRuntimeRepairSocketFDInfoABI{}.PCBHandle)) - darwinRuntimeRepairPCBHandleOffset]byte
	_ [darwinRuntimeRepairSocketFamilyOffset - int(unsafe.Offsetof(darwinRuntimeRepairSocketFDInfoABI{}.Family))]byte
	_ [int(unsafe.Offsetof(darwinRuntimeRepairSocketFDInfoABI{}.Family)) - darwinRuntimeRepairSocketFamilyOffset]byte
	_ [darwinRuntimeRepairSocketKindOffset - int(unsafe.Offsetof(darwinRuntimeRepairSocketFDInfoABI{}.Kind))]byte
	_ [int(unsafe.Offsetof(darwinRuntimeRepairSocketFDInfoABI{}.Kind)) - darwinRuntimeRepairSocketKindOffset]byte
	_ [darwinRuntimeRepairLocalPortOffset - int(unsafe.Offsetof(darwinRuntimeRepairSocketFDInfoABI{}.LocalPort))]byte
	_ [int(unsafe.Offsetof(darwinRuntimeRepairSocketFDInfoABI{}.LocalPort)) - darwinRuntimeRepairLocalPortOffset]byte
	_ [darwinRuntimeRepairGenerationOffset - int(unsafe.Offsetof(darwinRuntimeRepairSocketFDInfoABI{}.Generation))]byte
	_ [int(unsafe.Offsetof(darwinRuntimeRepairSocketFDInfoABI{}.Generation)) - darwinRuntimeRepairGenerationOffset]byte
	_ [darwinRuntimeRepairIPv4FlagOffset - int(unsafe.Offsetof(darwinRuntimeRepairSocketFDInfoABI{}.IPv4Flag))]byte
	_ [int(unsafe.Offsetof(darwinRuntimeRepairSocketFDInfoABI{}.IPv4Flag)) - darwinRuntimeRepairIPv4FlagOffset]byte
	_ [darwinRuntimeRepairIPv4AddressOffset - int(unsafe.Offsetof(darwinRuntimeRepairSocketFDInfoABI{}.IPv4Address))]byte
	_ [int(unsafe.Offsetof(darwinRuntimeRepairSocketFDInfoABI{}.IPv4Address)) - darwinRuntimeRepairIPv4AddressOffset]byte
	_ [darwinRuntimeRepairTCPStateOffset - int(unsafe.Offsetof(darwinRuntimeRepairSocketFDInfoABI{}.TCPState))]byte
	_ [int(unsafe.Offsetof(darwinRuntimeRepairSocketFDInfoABI{}.TCPState)) - darwinRuntimeRepairTCPStateOffset]byte
)

// observeDarwinRuntimeRepairSockets returns every exact target listener held by one process.
func observeDarwinRuntimeRepairSockets(pid int, endpoint netip.AddrPort) ([]runtimeRepairSocketFact, error) {
	fds, err := observeDarwinRuntimeRepairFDs(pid)
	if err != nil {
		return nil, err
	}
	facts := make([]runtimeRepairSocketFact, 0, 1)
	for _, fd := range fds {
		if fd.Type != darwinRuntimeRepairFDTypeSocket {
			continue
		}
		fact, matches, err := inspectDarwinRuntimeRepairSocketFD(pid, int(fd.FileDescriptor), endpoint)
		if err != nil {
			return nil, err
		}
		if matches {
			facts = append(facts, fact)
		}
	}
	slices.SortFunc(facts, func(left, right runtimeRepairSocketFact) int {
		return left.FileDescriptor - right.FileDescriptor
	})
	return facts, nil
}

// observeDarwinRuntimeRepairFDs obtains one bounded, structurally complete proc_fdinfo array.
func observeDarwinRuntimeRepairFDs(pid int) ([]darwinRuntimeRepairFDInfoABI, error) {
	readBytes, err := darwinRuntimeRepairFDReadBufferBytes()
	if err != nil {
		return nil, err
	}
	raw := make([]byte, readBytes)
	written, err := callDarwinRuntimeRepairPIDInfoAllowEmpty(pid, darwinRuntimeRepairPIDListFDs, 0, raw)
	if err != nil {
		return nil, classifyDarwinRuntimeRepairLibprocFailure(pid)
	}
	if err := validateDarwinRuntimeRepairFDReadLength(written, len(raw)); err != nil {
		return nil, err
	}
	fds, err := parseDarwinRuntimeRepairFDs(raw[:written])
	if err != nil {
		return nil, err
	}
	return fds, nil
}

// darwinRuntimeRepairFDReadBufferBytes uses a fixed upper bound because PROC_PIDLISTFDS does not provide a reliable zero-buffer size query.
func darwinRuntimeRepairFDReadBufferBytes() (int, error) {
	maximumBytes := darwinRuntimeRepairMaximumFDs * darwinRuntimeRepairFDInfoBytes
	bufferBytes := maximumBytes + darwinRuntimeRepairFDReadSpareRecords*darwinRuntimeRepairFDInfoBytes
	if bufferBytes <= maximumBytes || bufferBytes%darwinRuntimeRepairFDInfoBytes != 0 {
		return 0, fmt.Errorf("Darwin descriptor census bound is invalid: %w", errDarwinRuntimeRepairUnreadable)
	}
	return bufferBytes, nil
}

// validateDarwinRuntimeRepairFDReadLength rejects truncated, growing, and over-limit descriptor evidence.
func validateDarwinRuntimeRepairFDReadLength(written, bufferBytes int) error {
	maximumBytes := darwinRuntimeRepairMaximumFDs * darwinRuntimeRepairFDInfoBytes
	if written < 0 || written > bufferBytes || written%darwinRuntimeRepairFDInfoBytes != 0 || written == bufferBytes {
		return errDarwinRuntimeRepairUnstable
	}
	if written > maximumBytes {
		return errDarwinRuntimeRepairUnreadable
	}
	return nil
}

// parseDarwinRuntimeRepairFDs decodes unique non-negative descriptors from a fixed-stride native result.
func parseDarwinRuntimeRepairFDs(raw []byte) ([]darwinRuntimeRepairFDInfoABI, error) {
	if len(raw)%darwinRuntimeRepairFDInfoBytes != 0 || len(raw) > darwinRuntimeRepairMaximumFDs*darwinRuntimeRepairFDInfoBytes {
		return nil, fmt.Errorf("Darwin descriptor list has invalid length: %w", errDarwinRuntimeRepairUnreadable)
	}
	fds := make([]darwinRuntimeRepairFDInfoABI, 0, len(raw)/darwinRuntimeRepairFDInfoBytes)
	seen := make(map[int32]struct{}, len(raw)/darwinRuntimeRepairFDInfoBytes)
	for offset := 0; offset < len(raw); offset += darwinRuntimeRepairFDInfoBytes {
		fd := int32(binary.LittleEndian.Uint32(raw[offset : offset+4]))
		fdType := binary.LittleEndian.Uint32(raw[offset+4 : offset+8])
		if fd < 0 {
			return nil, fmt.Errorf("Darwin descriptor list contains a negative descriptor: %w", errDarwinRuntimeRepairUnreadable)
		}
		if _, duplicate := seen[fd]; duplicate {
			return nil, fmt.Errorf("Darwin descriptor list contains duplicate descriptor %d: %w", fd, errDarwinRuntimeRepairUnreadable)
		}
		seen[fd] = struct{}{}
		fds = append(fds, darwinRuntimeRepairFDInfoABI{FileDescriptor: fd, Type: fdType})
	}
	return fds, nil
}

// inspectDarwinRuntimeRepairSocketFD decodes one full socket_fdinfo record and filters it to the exact listener.
func inspectDarwinRuntimeRepairSocketFD(pid, fd int, endpoint netip.AddrPort) (runtimeRepairSocketFact, bool, error) {
	raw := make([]byte, darwinRuntimeRepairSocketFDInfoBytes)
	written, err := callDarwinRuntimeRepairPIDFDInfo(pid, fd, darwinRuntimeRepairPIDFDSocketInfo, raw)
	if err != nil {
		return runtimeRepairSocketFact{}, false, classifyDarwinRuntimeRepairLibprocFailure(pid)
	}
	if written != len(raw) {
		return runtimeRepairSocketFact{}, false, errDarwinRuntimeRepairUnstable
	}
	return parseDarwinRuntimeRepairSocketFD(raw, pid, fd, endpoint)
}

// parseDarwinRuntimeRepairSocketFD accepts only one exact IPv4 TCP listening endpoint with opaque kernel identity.
func parseDarwinRuntimeRepairSocketFD(raw []byte, pid, fd int, endpoint netip.AddrPort) (runtimeRepairSocketFact, bool, error) {
	if len(raw) != darwinRuntimeRepairSocketFDInfoBytes || pid <= 0 || fd < 0 {
		return runtimeRepairSocketFact{}, false, fmt.Errorf("Darwin socket record has invalid envelope: %w", errDarwinRuntimeRepairUnreadable)
	}
	family := int32(binary.LittleEndian.Uint32(raw[darwinRuntimeRepairSocketFamilyOffset : darwinRuntimeRepairSocketFamilyOffset+4]))
	kind := int32(binary.LittleEndian.Uint32(raw[darwinRuntimeRepairSocketKindOffset : darwinRuntimeRepairSocketKindOffset+4]))
	state := int32(binary.LittleEndian.Uint32(raw[darwinRuntimeRepairTCPStateOffset : darwinRuntimeRepairTCPStateOffset+4]))
	vflag := raw[darwinRuntimeRepairIPv4FlagOffset]
	if family != unix.AF_INET || kind != darwinRuntimeRepairSocketInfoTCP || state != darwinRuntimeRepairTCPStateListen || vflag != darwinRuntimeRepairIPv4Flag {
		return runtimeRepairSocketFact{}, false, nil
	}
	localPort := binary.LittleEndian.Uint32(raw[darwinRuntimeRepairLocalPortOffset : darwinRuntimeRepairLocalPortOffset+4])
	if localPort>>16 != 0 {
		return runtimeRepairSocketFact{}, false, fmt.Errorf("Darwin listener port contains unsupported high bits: %w", errDarwinRuntimeRepairUnreadable)
	}
	port := bits.ReverseBytes16(uint16(localPort))
	address := netip.AddrFrom4([4]byte(raw[darwinRuntimeRepairIPv4AddressOffset : darwinRuntimeRepairIPv4AddressOffset+4]))
	observedEndpoint := netip.AddrPortFrom(address, port)
	if observedEndpoint != endpoint {
		return runtimeRepairSocketFact{}, false, nil
	}
	socketHandle := binary.LittleEndian.Uint64(raw[darwinRuntimeRepairSocketHandleOffset : darwinRuntimeRepairSocketHandleOffset+8])
	pcbHandle := binary.LittleEndian.Uint64(raw[darwinRuntimeRepairPCBHandleOffset : darwinRuntimeRepairPCBHandleOffset+8])
	generation := binary.LittleEndian.Uint64(raw[darwinRuntimeRepairGenerationOffset : darwinRuntimeRepairGenerationOffset+8])
	if socketHandle == 0 || pcbHandle == 0 || generation == 0 {
		return runtimeRepairSocketFact{}, false, fmt.Errorf("Darwin listener lacks opaque identity: %w", errDarwinRuntimeRepairUnreadable)
	}
	return runtimeRepairSocketFact{
		OwnerPID:       pid,
		FileDescriptor: fd,
		SocketHandle:   socketHandle,
		PCBHandle:      pcbHandle,
		Generation:     generation,
		Endpoint:       observedEndpoint,
	}, true, nil
}

// observeDarwinRuntimeRepairExecutable reads and canonicalizes proc_pidpath without trusting argv zero.
func observeDarwinRuntimeRepairExecutable(pid int) (string, error) {
	raw := make([]byte, darwinRuntimeRepairProcessPathBytes)
	written, err := callDarwinRuntimeRepairPIDPath(pid, raw)
	if err != nil {
		return "", classifyDarwinRuntimeRepairLibprocFailure(pid)
	}
	if written <= 0 || written >= len(raw) {
		return "", errDarwinRuntimeRepairUnreadable
	}
	pathBytes := raw[:written]
	if nul := slices.Index(pathBytes, byte(0)); nul >= 0 {
		for _, trailing := range pathBytes[nul+1:] {
			if trailing != 0 {
				return "", errDarwinRuntimeRepairUnreadable
			}
		}
		pathBytes = pathBytes[:nul]
	}
	if len(pathBytes) == 0 {
		return "", errDarwinRuntimeRepairUnreadable
	}
	return canonicalDarwinRuntimeRepairPath(string(pathBytes), false)
}

// observeDarwinRuntimeRepairWorkingDirectory reads only pvi_cdir.vip_path from the full vnode-path record.
func observeDarwinRuntimeRepairWorkingDirectory(pid int) (string, error) {
	raw := make([]byte, darwinRuntimeRepairVnodePathInfoBytes)
	written, err := callDarwinRuntimeRepairPIDInfo(pid, darwinRuntimeRepairPIDVnodePathInfo, 0, raw)
	if err != nil {
		return "", classifyDarwinRuntimeRepairLibprocFailure(pid)
	}
	if written != len(raw) {
		return "", errDarwinRuntimeRepairUnstable
	}
	path, err := parseDarwinRuntimeRepairWorkingDirectory(raw)
	if err != nil {
		return "", err
	}
	return canonicalDarwinRuntimeRepairPath(path, true)
}

// parseDarwinRuntimeRepairWorkingDirectory requires one non-empty NUL-terminated current-directory path.
func parseDarwinRuntimeRepairWorkingDirectory(raw []byte) (string, error) {
	if len(raw) != darwinRuntimeRepairVnodePathInfoBytes {
		return "", fmt.Errorf("Darwin vnode path record has invalid length: %w", errDarwinRuntimeRepairUnreadable)
	}
	pathBytes := raw[darwinRuntimeRepairWorkingDirectoryOffset : darwinRuntimeRepairWorkingDirectoryOffset+darwinRuntimeRepairMaximumPathBytes]
	nul := slices.Index(pathBytes, byte(0))
	if nul <= 0 {
		return "", fmt.Errorf("Darwin current-directory path is not terminated: %w", errDarwinRuntimeRepairUnreadable)
	}
	return string(pathBytes[:nul]), nil
}

// observeDarwinRuntimeRepairArguments parses kern.procargs2 and returns argv only until it is immediately reduced.
func observeDarwinRuntimeRepairArguments(pid int) ([]string, error) {
	raw, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil {
		return nil, classifyDarwinRuntimeRepairLibprocFailure(pid)
	}
	return parseDarwinRuntimeRepairArguments(raw)
}

// parseDarwinRuntimeRepairArguments decodes argc strings while excluding executable-prefix and environment bytes.
func parseDarwinRuntimeRepairArguments(raw []byte) ([]string, error) {
	if len(raw) < 6 || len(raw) > darwinRuntimeRepairMaximumArgumentBytes {
		return nil, fmt.Errorf("Darwin process argument buffer has invalid length: %w", errDarwinRuntimeRepairUnreadable)
	}
	argumentCount := int(int32(binary.LittleEndian.Uint32(raw[:4])))
	if argumentCount <= 0 || argumentCount > runtimeRepairMaximumArguments {
		return nil, fmt.Errorf("Darwin process argument count is invalid: %w", errDarwinRuntimeRepairUnreadable)
	}
	cursor := 4
	executableEnd := slices.Index(raw[cursor:], byte(0))
	if executableEnd <= 0 || executableEnd > runtimeRepairMaximumTextBytes {
		return nil, fmt.Errorf("Darwin process executable prefix is malformed: %w", errDarwinRuntimeRepairUnreadable)
	}
	cursor += executableEnd + 1
	for cursor < len(raw) && raw[cursor] == 0 {
		cursor++
	}
	arguments := make([]string, 0, argumentCount)
	for len(arguments) < argumentCount {
		if cursor >= len(raw) {
			return nil, fmt.Errorf("Darwin process argv is truncated: %w", errDarwinRuntimeRepairUnreadable)
		}
		end := slices.Index(raw[cursor:], byte(0))
		if end <= 0 || end > runtimeRepairMaximumTextBytes {
			return nil, fmt.Errorf("Darwin process argv entry is malformed: %w", errDarwinRuntimeRepairUnreadable)
		}
		arguments = append(arguments, string(raw[cursor:cursor+end]))
		cursor += end + 1
	}
	return arguments, nil
}

// classifyDarwinRuntimeRepairLibprocFailure distinguishes process exit from persistent unreadability without errno leakage.
func classifyDarwinRuntimeRepairLibprocFailure(pid int) error {
	processes, err := unix.SysctlKinfoProcSlice("kern.proc.pid", pid)
	if err != nil {
		return errDarwinRuntimeRepairUnreadable
	}
	if len(processes) == 0 {
		return errDarwinRuntimeRepairUnstable
	}
	return errDarwinRuntimeRepairUnreadable
}

// callDarwinRuntimeRepairPIDPath invokes public proc_pidpath through a cgo-free libSystem trampoline.
func callDarwinRuntimeRepairPIDPath(pid int, buffer []byte) (int, error) {
	pointer := runtimeRepairBufferPointer(buffer)
	result, _, callErr := runtimeRepairDarwinSystemCall6(
		darwinRuntimeRepairPIDPathTrampolineAddress,
		uintptr(pid),
		pointer,
		uintptr(len(buffer)),
		0,
		0,
		0,
	)
	runtime.KeepAlive(buffer)
	return validateDarwinRuntimeRepairCallResult(result, callErr, len(buffer))
}

// callDarwinRuntimeRepairPIDInfo invokes public proc_pidinfo through a cgo-free libSystem trampoline.
func callDarwinRuntimeRepairPIDInfo(pid, flavor int, argument uint64, buffer []byte) (int, error) {
	pointer := runtimeRepairBufferPointer(buffer)
	result, _, callErr := runtimeRepairDarwinSystemCall6(
		darwinRuntimeRepairPIDInfoTrampolineAddress,
		uintptr(pid),
		uintptr(flavor),
		uintptr(argument),
		pointer,
		uintptr(len(buffer)),
		0,
	)
	runtime.KeepAlive(buffer)
	return validateDarwinRuntimeRepairCallResult(result, callErr, darwinRuntimeRepairPIDInfoResultLimit(buffer))
}

// callDarwinRuntimeRepairPIDInfoAllowEmpty preserves a valid empty descriptor inventory while retaining bounded native reads.
func callDarwinRuntimeRepairPIDInfoAllowEmpty(pid, flavor int, argument uint64, buffer []byte) (int, error) {
	pointer := runtimeRepairBufferPointer(buffer)
	result, _, callErr := runtimeRepairDarwinSystemCall6(
		darwinRuntimeRepairPIDInfoTrampolineAddress,
		uintptr(pid),
		uintptr(flavor),
		uintptr(argument),
		pointer,
		uintptr(len(buffer)),
		0,
	)
	runtime.KeepAlive(buffer)
	return validateDarwinRuntimeRepairCallResultAllowEmpty(result, callErr, darwinRuntimeRepairPIDInfoResultLimit(buffer))
}

// darwinRuntimeRepairPIDInfoResultLimit keeps native wrappers from accepting more bytes than their caller-provided buffer.
func darwinRuntimeRepairPIDInfoResultLimit(buffer []byte) int {
	if len(buffer) != 0 {
		return len(buffer)
	}
	// The wrapper is also used for a small number of non-list fd-info calls.
	return (darwinRuntimeRepairMaximumFDs + darwinRuntimeRepairFDReadSpareRecords) * darwinRuntimeRepairFDInfoBytes
}

// callDarwinRuntimeRepairPIDFDInfo invokes public proc_pidfdinfo through a cgo-free libSystem trampoline.
func callDarwinRuntimeRepairPIDFDInfo(pid, fd, flavor int, buffer []byte) (int, error) {
	pointer := runtimeRepairBufferPointer(buffer)
	result, _, callErr := runtimeRepairDarwinSystemCall6(
		darwinRuntimeRepairPIDFDInfoTrampolineAddress,
		uintptr(pid),
		uintptr(fd),
		uintptr(flavor),
		pointer,
		uintptr(len(buffer)),
		0,
	)
	runtime.KeepAlive(buffer)
	return validateDarwinRuntimeRepairCallResult(result, callErr, len(buffer))
}

// validateDarwinRuntimeRepairCallResult rejects libproc's zero-on-failure wrapper convention and oversized results.
func validateDarwinRuntimeRepairCallResult(result uintptr, callErr syscall.Errno, limit int) (int, error) {
	if result == 0 && callErr == 0 {
		return 0, errDarwinRuntimeRepairUnreadable
	}
	if callErr != 0 {
		return 0, callErr
	}
	if result > uintptr(limit) {
		return 0, errDarwinRuntimeRepairUnreadable
	}
	return int(result), nil
}

// validateDarwinRuntimeRepairCallResultAllowEmpty accepts zero only for APIs whose empty result is valid evidence.
func validateDarwinRuntimeRepairCallResultAllowEmpty(result uintptr, callErr syscall.Errno, limit int) (int, error) {
	if callErr != 0 {
		return 0, callErr
	}
	if result > uintptr(limit) {
		return 0, errDarwinRuntimeRepairUnreadable
	}
	return int(result), nil
}

// runtimeRepairBufferPointer returns zero for a native size query and a stable pointer otherwise.
func runtimeRepairBufferPointer(buffer []byte) uintptr {
	if len(buffer) == 0 {
		return 0
	}
	return uintptr(unsafe.Pointer(&buffer[0]))
}

// runtimeRepairDarwinSystemCall6 invokes one libSystem function with at most six arguments.
func runtimeRepairDarwinSystemCall6(
	function uintptr,
	argument1 uintptr,
	argument2 uintptr,
	argument3 uintptr,
	argument4 uintptr,
	argument5 uintptr,
	argument6 uintptr,
) (result1 uintptr, result2 uintptr, callErr syscall.Errno)

//go:linkname runtimeRepairDarwinSystemCall6 syscall.syscall6

var (
	// darwinRuntimeRepairPIDPathTrampolineAddress points at the cgo-free proc_pidpath trampoline.
	darwinRuntimeRepairPIDPathTrampolineAddress uintptr
	// darwinRuntimeRepairPIDInfoTrampolineAddress points at the cgo-free proc_pidinfo trampoline.
	darwinRuntimeRepairPIDInfoTrampolineAddress uintptr
	// darwinRuntimeRepairPIDFDInfoTrampolineAddress points at the cgo-free proc_pidfdinfo trampoline.
	darwinRuntimeRepairPIDFDInfoTrampolineAddress uintptr
)

//go:cgo_import_dynamic libc_runtime_repair_proc_pidpath proc_pidpath "/usr/lib/libSystem.B.dylib"
//go:cgo_import_dynamic libc_runtime_repair_proc_pidinfo proc_pidinfo "/usr/lib/libSystem.B.dylib"
//go:cgo_import_dynamic libc_runtime_repair_proc_pidfdinfo proc_pidfdinfo "/usr/lib/libSystem.B.dylib"
