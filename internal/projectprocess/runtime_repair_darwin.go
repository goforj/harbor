//go:build darwin

package projectprocess

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"syscall"

	"github.com/goforj/harbor/internal/platform/hostconflict"
	"golang.org/x/sys/unix"
)

const (
	// darwinRuntimeRepairObservationAttempts bounds instability retries before evidence becomes unreadable.
	darwinRuntimeRepairObservationAttempts = 4
	// darwinRuntimeRepairMaximumSystemProcesses bounds the process-global session census.
	darwinRuntimeRepairMaximumSystemProcesses = 32768
)

var (
	// errDarwinRuntimeRepairUnstable marks a process or descriptor race that may disappear on a fresh pass.
	errDarwinRuntimeRepairUnstable = errors.New("Darwin runtime repair native facts changed")
	// errDarwinRuntimeRepairUnreadable marks native evidence that cannot safely authorize a signal.
	errDarwinRuntimeRepairUnreadable = errors.New("Darwin runtime repair native facts are unreadable")
)

// darwinRuntimeRepairNetworkFacts summarize process-global listener evidence without retaining unrelated endpoints.
type darwinRuntimeRepairNetworkFacts struct {
	exactListeners   int
	conflictingBinds int
}

// newRuntimeRepairControl wires the reviewed cgo-free Darwin observation and root-only signaling backend.
func newRuntimeRepairControl() runtimeRepairControl {
	return runtimeRepairControl{
		inspect:  inspectStableDarwinRuntimeRepair,
		graceful: gracefullyTerminateDarwinRuntimeRepair,
		settled:  observeDarwinRuntimeRepairSettlement,
	}
}

// inspectStableDarwinRuntimeRepair requires two consecutive equal native classifications before returning.
func inspectStableDarwinRuntimeRepair(ctx context.Context, target RuntimeRepairTarget) (runtimeRepairNativeInspection, error) {
	var previous *runtimeRepairNativeInspection
	for attempt := 0; attempt < darwinRuntimeRepairObservationAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return runtimeRepairNativeInspection{}, err
		}
		inspection, err := inspectDarwinRuntimeRepairPass(ctx, target)
		if errors.Is(err, errDarwinRuntimeRepairUnstable) {
			previous = nil
			continue
		}
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return runtimeRepairNativeInspection{}, ctxErr
			}
			return runtimeRepairNativeInspection{State: RuntimeRepairInspectionUnreadable}, nil
		}
		if previous != nil && equalDarwinRuntimeRepairInspections(*previous, inspection) {
			return inspection, nil
		}
		copy := inspection
		if inspection.Observation != nil {
			observation := inspection.Observation.clone()
			copy.Observation = &observation
		}
		previous = &copy
	}
	return runtimeRepairNativeInspection{State: RuntimeRepairInspectionUnreadable}, nil
}

// inspectDarwinRuntimeRepairPass correlates global TCP presence with one complete same-user session scope.
func inspectDarwinRuntimeRepairPass(ctx context.Context, target RuntimeRepairTarget) (runtimeRepairNativeInspection, error) {
	network, err := observeDarwinRuntimeRepairNetwork(ctx, target.Endpoint)
	if err != nil {
		return runtimeRepairNativeInspection{}, err
	}
	cardinality := runtimeRepairListenerCardinality(network.exactListeners, network.conflictingBinds)
	if cardinality != RuntimeRepairInspectionActionable {
		return runtimeRepairNativeInspection{State: cardinality}, nil
	}

	daemonUID := uint32(os.Geteuid())
	processes, err := unix.SysctlKinfoProcSlice("kern.proc.uid", int(daemonUID))
	if err != nil {
		return runtimeRepairNativeInspection{}, fmt.Errorf("enumerate daemon-user Darwin processes: %w", errDarwinRuntimeRepairUnreadable)
	}
	if len(processes) > runtimeRepairMaximumProcesses {
		return runtimeRepairNativeInspection{}, fmt.Errorf("daemon-user Darwin process count exceeds %d: %w", runtimeRepairMaximumProcesses, errDarwinRuntimeRepairUnreadable)
	}
	owners := make([]runtimeRepairSocketFact, 0, 1)
	for _, process := range processes {
		if err := ctx.Err(); err != nil {
			return runtimeRepairNativeInspection{}, err
		}
		if process.Proc.P_stat == darwinProcessStateZombie || process.Proc.P_pid <= 0 {
			continue
		}
		pid := int(process.Proc.P_pid)
		facts, err := observeDarwinRuntimeRepairSockets(pid, target.Endpoint)
		if err != nil {
			return runtimeRepairNativeInspection{}, err
		}
		owners = append(owners, facts...)
		if len(owners) > 1 {
			return runtimeRepairNativeInspection{State: RuntimeRepairInspectionAmbiguous}, nil
		}
	}
	if len(owners) == 0 {
		return runtimeRepairNativeInspection{State: RuntimeRepairInspectionForeign}, nil
	}

	observation, state, err := observeDarwinRuntimeRepairSession(ctx, target, daemonUID, owners[0])
	if err != nil {
		return runtimeRepairNativeInspection{}, err
	}
	if state != RuntimeRepairInspectionActionable {
		return runtimeRepairNativeInspection{State: state}, nil
	}
	return runtimeRepairNativeInspection{State: state, Observation: &observation}, nil
}

// observeDarwinRuntimeRepairSession proves the socket owner belongs to one bounded dedicated same-user session tree.
func observeDarwinRuntimeRepairSession(
	ctx context.Context,
	target RuntimeRepairTarget,
	daemonUID uint32,
	listener runtimeRepairSocketFact,
) (runtimeRepairNativeObservation, RuntimeRepairInspectionState, error) {
	sessionID, err := unix.Getsid(listener.OwnerPID)
	if errors.Is(err, syscall.ESRCH) {
		return runtimeRepairNativeObservation{}, "", errDarwinRuntimeRepairUnstable
	}
	if err != nil || sessionID <= 0 {
		return runtimeRepairNativeObservation{}, "", fmt.Errorf("read listener owner session: %w", errDarwinRuntimeRepairUnreadable)
	}
	allProcesses, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return runtimeRepairNativeObservation{}, "", fmt.Errorf("enumerate Darwin sessions: %w", errDarwinRuntimeRepairUnreadable)
	}
	if len(allProcesses) > darwinRuntimeRepairMaximumSystemProcesses {
		return runtimeRepairNativeObservation{}, "", fmt.Errorf("Darwin process census exceeds %d: %w", darwinRuntimeRepairMaximumSystemProcesses, errDarwinRuntimeRepairUnreadable)
	}
	members := make([]runtimeRepairProcessFact, 0)
	for _, process := range allProcesses {
		if err := ctx.Err(); err != nil {
			return runtimeRepairNativeObservation{}, "", err
		}
		if process.Proc.P_stat == darwinProcessStateZombie || process.Proc.P_pid <= 0 {
			continue
		}
		pid := int(process.Proc.P_pid)
		observedSessionID, err := unix.Getsid(pid)
		if errors.Is(err, syscall.ESRCH) {
			continue
		}
		if err != nil {
			return runtimeRepairNativeObservation{}, "", fmt.Errorf("read Darwin process session: %w", errDarwinRuntimeRepairUnreadable)
		}
		if observedSessionID != sessionID {
			continue
		}
		if process.Eproc.Ucred.Uid != daemonUID || process.Eproc.Pcred.P_ruid != daemonUID {
			return runtimeRepairNativeObservation{}, RuntimeRepairInspectionForeign, nil
		}
		fact, err := observeDarwinRuntimeRepairProcess(process, observedSessionID)
		if err != nil {
			return runtimeRepairNativeObservation{}, "", err
		}
		members = append(members, fact)
		if len(members) > runtimeRepairMaximumProcesses {
			return runtimeRepairNativeObservation{}, "", fmt.Errorf("Darwin session exceeds %d members: %w", runtimeRepairMaximumProcesses, errDarwinRuntimeRepairUnreadable)
		}
	}
	slices.SortFunc(members, func(left, right runtimeRepairProcessFact) int {
		return cmp.Compare(left.PID, right.PID)
	})
	rootIndex, found := slices.BinarySearchFunc(members, sessionID, func(member runtimeRepairProcessFact, pid int) int {
		return cmp.Compare(member.PID, pid)
	})
	if !found {
		return runtimeRepairNativeObservation{}, "", errDarwinRuntimeRepairUnstable
	}
	root := members[rootIndex]
	parentBirth, present, err := observeProcessBirthToken(root.ParentPID)
	if err != nil {
		return runtimeRepairNativeObservation{}, "", fmt.Errorf("read Darwin root parent birth: %w", errDarwinRuntimeRepairUnreadable)
	}
	if !present {
		return runtimeRepairNativeObservation{}, "", errDarwinRuntimeRepairUnstable
	}
	ownerIndex, ownerFound := slices.BinarySearchFunc(members, listener.OwnerPID, func(member runtimeRepairProcessFact, pid int) int {
		return cmp.Compare(member.PID, pid)
	})
	if !ownerFound {
		return runtimeRepairNativeObservation{}, RuntimeRepairInspectionAmbiguous, nil
	}
	listener.OwnerBirthToken = members[ownerIndex].BirthToken
	observation := runtimeRepairNativeObservation{
		Target:     target,
		DaemonUID:  daemonUID,
		Root:       root,
		RootParent: runtimeRepairParentFact{PID: root.ParentPID, BirthToken: parentBirth},
		Members:    members,
		Listener:   listener,
	}
	if err := observation.validate(); err != nil {
		return runtimeRepairNativeObservation{}, RuntimeRepairInspectionAmbiguous, nil
	}
	return observation, RuntimeRepairInspectionActionable, nil
}

// observeDarwinRuntimeRepairProcess collects every bounded identity fact before raw argv is discarded.
func observeDarwinRuntimeRepairProcess(process unix.KinfoProc, sessionID int) (runtimeRepairProcessFact, error) {
	pid := int(process.Proc.P_pid)
	started := process.Proc.P_starttime
	if started.Sec == 0 && started.Usec == 0 {
		return runtimeRepairProcessFact{}, fmt.Errorf("Darwin process birth timestamp is empty: %w", errDarwinRuntimeRepairUnreadable)
	}
	executable, err := observeDarwinRuntimeRepairExecutable(pid)
	if err != nil {
		return runtimeRepairProcessFact{}, err
	}
	arguments, err := observeDarwinRuntimeRepairArguments(pid)
	if err != nil {
		return runtimeRepairProcessFact{}, err
	}
	argumentDigest, argumentCount, commandExact, err := runtimeRepairArgumentEvidenceForExecutable(executable, arguments)
	arguments = nil
	if err != nil {
		return runtimeRepairProcessFact{}, fmt.Errorf("reduce Darwin process argv: %w", errDarwinRuntimeRepairUnreadable)
	}
	workingDirectory, err := observeDarwinRuntimeRepairWorkingDirectory(pid)
	if err != nil {
		return runtimeRepairProcessFact{}, err
	}
	fact := runtimeRepairProcessFact{
		PID:                pid,
		BirthToken:         fmt.Sprintf("darwin:%d:%d", started.Sec, started.Usec),
		ParentPID:          int(process.Eproc.Ppid),
		ProcessGroupID:     int(process.Eproc.Pgid),
		SessionID:          sessionID,
		EffectiveUID:       process.Eproc.Ucred.Uid,
		RealUID:            process.Eproc.Pcred.P_ruid,
		ExecutableIdentity: executable,
		ArgumentDigest:     argumentDigest,
		ArgumentCount:      argumentCount,
		CommandExact:       commandExact,
		WorkingDirectory:   workingDirectory,
	}
	if err := fact.validate(); err != nil {
		return runtimeRepairProcessFact{}, fmt.Errorf("validate Darwin process facts: %w", errDarwinRuntimeRepairUnreadable)
	}
	return fact, nil
}

// observeDarwinRuntimeRepairNetwork counts exact and wildcard TCP listeners from the stable global PCB snapshot.
func observeDarwinRuntimeRepairNetwork(ctx context.Context, endpoint netip.AddrPort) (darwinRuntimeRepairNetworkFacts, error) {
	request, err := hostconflict.NewPreAssignmentRequest(endpoint.Addr(), []hostconflict.SocketRequirement{{
		Transport: hostconflict.TransportTCP4,
		Port:      endpoint.Port(),
	}})
	if err != nil {
		return darwinRuntimeRepairNetworkFacts{}, err
	}
	observation, err := hostconflict.ObserveDarwin(ctx, request)
	if err != nil {
		return darwinRuntimeRepairNetworkFacts{}, fmt.Errorf(
			"observe process-global Darwin listeners: %w",
			errors.Join(errDarwinRuntimeRepairUnreadable, err),
		)
	}
	if !observation.Sockets.Complete || observation.Sockets.Truncated {
		return darwinRuntimeRepairNetworkFacts{}, fmt.Errorf("observe process-global Darwin listeners: incomplete socket snapshot: %w", errDarwinRuntimeRepairUnreadable)
	}
	facts := darwinRuntimeRepairNetworkFacts{}
	for _, socket := range observation.Sockets.Endpoints {
		if socket.Protocol != hostconflict.SocketProtocolTCP || socket.Port != endpoint.Port() || !socket.TCPAccepting {
			continue
		}
		switch {
		case socket.Address == endpoint.Addr():
			facts.exactListeners++
		case socket.Address.Is4() && socket.Address.IsUnspecified():
			facts.conflictingBinds++
		case socket.Address.Is6() && socket.Address.IsUnspecified() && socket.IPv6Only != hostconflict.IPv6OnlyEnabled:
			facts.conflictingBinds++
		}
	}
	return facts, nil
}

// equalDarwinRuntimeRepairInspections compares classifications and every actionable authority fact.
func equalDarwinRuntimeRepairInspections(left, right runtimeRepairNativeInspection) bool {
	if left.State != right.State {
		return false
	}
	if left.Observation == nil || right.Observation == nil {
		return left.Observation == nil && right.Observation == nil
	}
	return runtimeRepairObservationsEqual(*left.Observation, *right.Observation)
}

// gracefullyTerminateDarwinRuntimeRepair fully reobserves the receipt before signaling only its exact root.
func gracefullyTerminateDarwinRuntimeRepair(ctx context.Context, receipt runtimeRepairReceipt) (bool, error) {
	inspection, err := inspectStableDarwinRuntimeRepair(ctx, receipt.observation.Target)
	if err != nil {
		return false, err
	}
	if inspection.State != RuntimeRepairInspectionActionable || inspection.Observation == nil ||
		!runtimeRepairObservationsEqual(*inspection.Observation, receipt.observation) {
		return false, ErrRuntimeRepairDrift
	}
	root := receipt.observation.Root
	processes, err := unix.SysctlKinfoProcSlice("kern.proc.pid", root.PID)
	if err != nil || len(processes) != 1 {
		return false, ErrRuntimeRepairDrift
	}
	currentRoot, err := observeDarwinRuntimeRepairProcess(processes[0], root.SessionID)
	if err != nil || !reflect.DeepEqual(currentRoot, root) {
		return false, ErrRuntimeRepairDrift
	}
	currentSessionID, sessionErr := unix.Getsid(root.PID)
	currentProcessGroupID, groupErr := unix.Getpgid(root.PID)
	if sessionErr != nil || groupErr != nil || currentSessionID != root.PID || currentProcessGroupID != root.PID {
		return false, ErrRuntimeRepairDrift
	}
	currentSocket, err := observeDarwinRuntimeRepairSocket(root, receipt.observation.Listener)
	if err != nil || currentSocket != receipt.observation.Listener {
		return false, ErrRuntimeRepairDrift
	}
	parentBirth, parentPresent, err := observeProcessBirthToken(receipt.observation.RootParent.PID)
	if err != nil || !parentPresent || parentBirth != receipt.observation.RootParent.BirthToken {
		return false, ErrRuntimeRepairDrift
	}
	currentMembers, err := observeDarwinRuntimeRepairSessionProcessFacts(ctx, root.SessionID, receipt.observation.Members)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return false, ctxErr
		}
		return false, ErrRuntimeRepairDrift
	}
	if !sameDarwinRuntimeRepairProcessFacts(currentMembers, receipt.observation.Members) {
		return false, ErrRuntimeRepairDrift
	}
	finalBirth, present, err := observeProcessBirthToken(root.PID)
	if err != nil || !present || finalBirth != root.BirthToken {
		return false, ErrRuntimeRepairDrift
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if err := syscall.Kill(root.PID, syscall.SIGTERM); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return false, ErrRuntimeRepairDrift
		}
		return false, fmt.Errorf("gracefully terminate exact Darwin runtime repair root: %w", err)
	}
	return true, nil
}

// observeDarwinRuntimeRepairSessionProcessFacts rereads every captured member identity immediately before signaling.
func observeDarwinRuntimeRepairSessionProcessFacts(
	ctx context.Context,
	sessionID int,
	expected []runtimeRepairProcessFact,
) ([]runtimeRepairProcessFact, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	members, err := unixSessionMembers(sessionID)
	if err != nil {
		return nil, err
	}
	if !sameDarwinRuntimeRepairSessionMembers(members, expected) {
		return nil, errDarwinRuntimeRepairUnstable
	}
	observed := make([]runtimeRepairProcessFact, 0, len(members))
	for _, member := range members {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		processes, err := unix.SysctlKinfoProcSlice("kern.proc.pid", member.PID)
		if err != nil {
			if errors.Is(err, syscall.ESRCH) {
				return nil, errDarwinRuntimeRepairUnstable
			}
			return nil, fmt.Errorf("read Darwin process %d for runtime repair: %w", member.PID, errDarwinRuntimeRepairUnreadable)
		}
		if len(processes) == 0 {
			return nil, errDarwinRuntimeRepairUnstable
		}
		if len(processes) != 1 || processes[0].Proc.P_stat == darwinProcessStateZombie || int(processes[0].Proc.P_pid) != member.PID {
			return nil, errDarwinRuntimeRepairUnstable
		}
		observedSessionID, err := unix.Getsid(member.PID)
		if errors.Is(err, syscall.ESRCH) {
			return nil, errDarwinRuntimeRepairUnstable
		}
		if err != nil {
			return nil, fmt.Errorf("read Darwin process %d session for runtime repair: %w", member.PID, errDarwinRuntimeRepairUnreadable)
		}
		if observedSessionID != sessionID {
			return nil, errDarwinRuntimeRepairUnstable
		}
		fact, err := observeDarwinRuntimeRepairProcess(processes[0], observedSessionID)
		if err != nil {
			return nil, err
		}
		if fact.PID != member.PID || fact.BirthToken != member.BirthToken {
			return nil, errDarwinRuntimeRepairUnstable
		}
		observed = append(observed, fact)
	}
	return observed, nil
}

// sameDarwinRuntimeRepairSessionMembers proves the exact captured session scope still exists immediately before signaling.
func sameDarwinRuntimeRepairSessionMembers(observed []unixProcessMember, expected []runtimeRepairProcessFact) bool {
	if len(observed) != len(expected) {
		return false
	}
	for index, member := range observed {
		if member.PID != expected[index].PID || member.BirthToken != expected[index].BirthToken {
			return false
		}
	}
	return true
}

// sameDarwinRuntimeRepairProcessFacts compares every native identity fact, not only PID and birth.
func sameDarwinRuntimeRepairProcessFacts(observed, expected []runtimeRepairProcessFact) bool {
	if len(observed) != len(expected) {
		return false
	}
	for index := range observed {
		if !reflect.DeepEqual(observed[index], expected[index]) {
			return false
		}
	}
	return true
}

// observeDarwinRuntimeRepairSocket revalidates the captured listener descriptor and owner birth immediately before signaling.
func observeDarwinRuntimeRepairSocket(root runtimeRepairProcessFact, expected runtimeRepairSocketFact) (runtimeRepairSocketFact, error) {
	ownerBirth, present, err := observeProcessBirthToken(expected.OwnerPID)
	if err != nil || !present || ownerBirth != expected.OwnerBirthToken {
		return runtimeRepairSocketFact{}, ErrRuntimeRepairDrift
	}
	fact, matches, err := inspectDarwinRuntimeRepairSocketFD(expected.OwnerPID, expected.FileDescriptor, expected.Endpoint)
	if err != nil || !matches {
		return runtimeRepairSocketFact{}, ErrRuntimeRepairDrift
	}
	fact.OwnerBirthToken = ownerBirth
	if expected.OwnerPID == root.PID && ownerBirth != root.BirthToken {
		return runtimeRepairSocketFact{}, ErrRuntimeRepairDrift
	}
	return fact, nil
}

// observeDarwinRuntimeRepairSettlement requires captured births, the complete session, and every conflicting bind to disappear.
func observeDarwinRuntimeRepairSettlement(ctx context.Context, receipt runtimeRepairReceipt) (bool, error) {
	for _, member := range receipt.observation.Members {
		birth, present, err := observeProcessBirthToken(member.PID)
		if err != nil {
			return false, fmt.Errorf("observe Darwin runtime repair settlement: %w", err)
		}
		if present && birth == member.BirthToken {
			return false, nil
		}
	}
	remainingSession, err := unixSessionMembers(receipt.observation.Root.SessionID)
	if err != nil {
		return false, fmt.Errorf("observe Darwin runtime repair session settlement: %w", errDarwinRuntimeRepairUnreadable)
	}
	if len(remainingSession) != 0 {
		return false, nil
	}
	network, err := observeDarwinRuntimeRepairNetwork(ctx, receipt.observation.Target.Endpoint)
	if err != nil {
		return false, err
	}
	if network.exactListeners != 0 || network.conflictingBinds != 0 {
		return false, nil
	}
	return probeDarwinRuntimeRepairEndpoint(receipt.observation.Target.Endpoint)
}

// probeDarwinRuntimeRepairEndpoint proves the released endpoint accepts the same exact TCP4 bind needed by the next start.
func probeDarwinRuntimeRepairEndpoint(endpoint netip.AddrPort) (bool, error) {
	listener, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IP(endpoint.Addr().AsSlice()), Port: int(endpoint.Port())})
	if err != nil {
		if errors.Is(err, syscall.EADDRINUSE) {
			return false, nil
		}
		return false, errDarwinRuntimeRepairUnreadable
	}
	address, addressOK := listener.Addr().(*net.TCPAddr)
	if closeErr := listener.Close(); closeErr != nil {
		return false, errDarwinRuntimeRepairUnreadable
	}
	if !addressOK || address.Port <= 0 || address.Port > 65535 {
		return false, errDarwinRuntimeRepairUnreadable
	}
	observedAddress, observedOK := netip.AddrFromSlice(address.IP)
	if !observedOK || netip.AddrPortFrom(observedAddress.Unmap(), uint16(address.Port)) != endpoint {
		return false, errDarwinRuntimeRepairUnreadable
	}
	return true, nil
}

// canonicalDarwinRuntimeRepairPath rejects deleted objects and aliases that cannot be reobserved exactly.
func canonicalDarwinRuntimeRepairPath(path string, directory bool) (string, error) {
	if path == "" || len(path) > runtimeRepairMaximumTextBytes || !filepath.IsAbs(path) {
		return "", errDarwinRuntimeRepairUnreadable
	}
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", errDarwinRuntimeRepairUnreadable
	}
	canonical = filepath.Clean(canonical)
	info, err := os.Stat(canonical)
	if err != nil || info.IsDir() != directory {
		return "", errDarwinRuntimeRepairUnreadable
	}
	return canonical, nil
}
