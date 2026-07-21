//go:build darwin

package projectprocess

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"syscall"

	"golang.org/x/sys/unix"
)

// newUnattributedRuntimeControl wires Darwin's read-only same-user process-tree observer.
func newUnattributedRuntimeControl() unattributedRuntimeControl {
	return unattributedRuntimeControl{
		inspect:  inspectStableDarwinUnattributedRuntime,
		graceful: gracefullyTerminateDarwinUnattributedRuntime,
		force:    forcefullyTerminateDarwinUnattributedRuntime,
		settled:  observeDarwinUnattributedRuntimeSettlement,
	}
}

// inspectStableDarwinUnattributedRuntime requires two consecutive equal scopes before returning a candidate.
func inspectStableDarwinUnattributedRuntime(ctx context.Context, target RuntimeRepairTarget) (unattributedRuntimeNativeInspection, error) {
	var previous *unattributedRuntimeNativeInspection
	for attempt := 0; attempt < darwinRuntimeRepairObservationAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return unattributedRuntimeNativeInspection{}, err
		}
		inspection, err := inspectDarwinUnattributedRuntimePass(ctx, target)
		if errors.Is(err, errDarwinRuntimeRepairUnstable) {
			previous = nil
			continue
		}
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return unattributedRuntimeNativeInspection{}, ctxErr
			}
			return unattributedRuntimeNativeInspection{State: RuntimeRepairInspectionUnreadable}, nil
		}
		if previous != nil && equalDarwinUnattributedRuntimeInspections(*previous, inspection) {
			return inspection, nil
		}
		copy := inspection
		if inspection.Observation != nil {
			observation := inspection.Observation.clone()
			copy.Observation = &observation
		}
		previous = &copy
	}
	return unattributedRuntimeNativeInspection{State: RuntimeRepairInspectionUnreadable}, nil
}

// inspectDarwinUnattributedRuntimePass correlates one exact or wildcard listener with one same-user project scope.
func inspectDarwinUnattributedRuntimePass(ctx context.Context, target RuntimeRepairTarget) (unattributedRuntimeNativeInspection, error) {
	network, err := observeDarwinRuntimeRepairNetwork(ctx, target.Endpoint)
	if err != nil {
		return unattributedRuntimeNativeInspection{}, err
	}
	cardinality := runtimeRepairProjectListenerCardinality(network.exactListeners, network.conflictingBinds)
	if cardinality != RuntimeRepairInspectionActionable {
		return unattributedRuntimeNativeInspection{State: cardinality}, nil
	}

	daemonUID := uint32(os.Geteuid())
	userProcesses, err := unix.SysctlKinfoProcSlice("kern.proc.uid", int(daemonUID))
	if err != nil {
		return unattributedRuntimeNativeInspection{}, fmt.Errorf("enumerate daemon-user Darwin processes: %w", errDarwinRuntimeRepairUnreadable)
	}
	if len(userProcesses) > runtimeRepairMaximumProcesses {
		return unattributedRuntimeNativeInspection{}, fmt.Errorf("daemon-user Darwin process count exceeds %d: %w", runtimeRepairMaximumProcesses, errDarwinRuntimeRepairUnreadable)
	}
	slices.SortFunc(userProcesses, func(left, right unix.KinfoProc) int {
		return cmp.Compare(left.Proc.P_pid, right.Proc.P_pid)
	})
	owners := make([]runtimeRepairSocketFact, 0, 1)
	for _, process := range userProcesses {
		if process.Proc.P_stat == darwinProcessStateZombie || process.Proc.P_pid <= 0 {
			continue
		}
		facts, err := observeDarwinRuntimeRepairSockets(int(process.Proc.P_pid), target.Endpoint)
		if err != nil {
			return unattributedRuntimeNativeInspection{}, err
		}
		owners = append(owners, facts...)
		if len(owners) > 1 {
			return unattributedRuntimeNativeInspection{State: RuntimeRepairInspectionAmbiguous}, nil
		}
	}
	if len(owners) == 0 {
		return unattributedRuntimeNativeInspection{State: RuntimeRepairInspectionForeign}, nil
	}

	observation, state, err := observeDarwinUnattributedRuntimeScope(ctx, target, daemonUID, owners[0])
	if err != nil {
		return unattributedRuntimeNativeInspection{}, err
	}
	if state != RuntimeRepairInspectionActionable {
		return unattributedRuntimeNativeInspection{State: state}, nil
	}
	return unattributedRuntimeNativeInspection{State: state, Observation: &observation}, nil
}

// observeDarwinUnattributedRuntimeScope proves one exact same-user project scope owns the listener.
func observeDarwinUnattributedRuntimeScope(
	ctx context.Context,
	target RuntimeRepairTarget,
	daemonUID uint32,
	listener runtimeRepairSocketFact,
) (unattributedRuntimeNativeObservation, RuntimeRepairInspectionState, error) {
	allProcesses, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return unattributedRuntimeNativeObservation{}, "", fmt.Errorf("enumerate Darwin process scope: %w", errDarwinRuntimeRepairUnreadable)
	}
	if len(allProcesses) > darwinRuntimeRepairMaximumSystemProcesses {
		return unattributedRuntimeNativeObservation{}, "", fmt.Errorf("Darwin process census exceeds %d: %w", darwinRuntimeRepairMaximumSystemProcesses, errDarwinRuntimeRepairUnreadable)
	}
	processByPID := make(map[int]unix.KinfoProc, len(allProcesses))
	for _, process := range allProcesses {
		if process.Proc.P_stat == darwinProcessStateZombie || process.Proc.P_pid <= 0 {
			continue
		}
		processByPID[int(process.Proc.P_pid)] = process
	}
	if _, found := processByPID[listener.OwnerPID]; !found {
		return unattributedRuntimeNativeObservation{}, "", errDarwinRuntimeRepairUnstable
	}

	roots, err := findDarwinUnattributedRuntimeRoots(ctx, target, daemonUID, listener.OwnerPID, processByPID)
	if err != nil {
		return unattributedRuntimeNativeObservation{}, "", err
	}
	if len(roots) == 0 {
		return unattributedRuntimeNativeObservation{}, RuntimeRepairInspectionAmbiguous, nil
	}
	if len(roots) != 1 {
		return unattributedRuntimeNativeObservation{}, RuntimeRepairInspectionAmbiguous, nil
	}
	selectedRoot := roots[0]

	memberProcesses := make([]unix.KinfoProc, 0, 1)
	for pid, process := range processByPID {
		descendant, stable := darwinProcessDescendsFromRoot(pid, selectedRoot.PID, processByPID)
		if !stable {
			continue
		}
		if !descendant {
			continue
		}
		if process.Eproc.Ucred.Uid != daemonUID || process.Eproc.Pcred.P_ruid != daemonUID {
			return unattributedRuntimeNativeObservation{}, RuntimeRepairInspectionForeign, nil
		}
		memberProcesses = append(memberProcesses, process)
		if len(memberProcesses) > runtimeRepairMaximumProcesses {
			return unattributedRuntimeNativeObservation{}, "", fmt.Errorf("unattributed Darwin process scope exceeds %d: %w", runtimeRepairMaximumProcesses, errDarwinRuntimeRepairUnreadable)
		}
	}
	slices.SortFunc(memberProcesses, func(left, right unix.KinfoProc) int {
		return cmp.Compare(left.Proc.P_pid, right.Proc.P_pid)
	})
	if len(memberProcesses) == 0 {
		return unattributedRuntimeNativeObservation{}, "", errDarwinRuntimeRepairUnstable
	}

	members := make([]runtimeRepairProcessFact, 0, len(memberProcesses))
	for _, process := range memberProcesses {
		if err := ctx.Err(); err != nil {
			return unattributedRuntimeNativeObservation{}, "", err
		}
		pid := int(process.Proc.P_pid)
		sessionID, err := unix.Getsid(pid)
		if errors.Is(err, syscall.ESRCH) {
			return unattributedRuntimeNativeObservation{}, "", errDarwinRuntimeRepairUnstable
		}
		if err != nil || sessionID <= 0 {
			return unattributedRuntimeNativeObservation{}, "", fmt.Errorf("read Darwin process %d session: %w", pid, errDarwinRuntimeRepairUnreadable)
		}
		fact, err := observeDarwinRuntimeRepairProcess(process, sessionID)
		if err != nil {
			return unattributedRuntimeNativeObservation{}, "", err
		}
		members = append(members, fact)
	}
	rootIndex, found := slices.BinarySearchFunc(members, selectedRoot.PID, func(member runtimeRepairProcessFact, pid int) int {
		return cmp.Compare(member.PID, pid)
	})
	if !found {
		return unattributedRuntimeNativeObservation{}, "", errDarwinRuntimeRepairUnstable
	}
	rootFact := members[rootIndex]
	parentBirth, present, err := observeProcessBirthToken(rootFact.ParentPID)
	if err != nil {
		return unattributedRuntimeNativeObservation{}, "", fmt.Errorf("read Darwin unattributed runtime root parent birth: %w", errDarwinRuntimeRepairUnreadable)
	}
	if !present {
		return unattributedRuntimeNativeObservation{}, "", errDarwinRuntimeRepairUnstable
	}
	ownerIndex, ownerFound := slices.BinarySearchFunc(members, listener.OwnerPID, func(member runtimeRepairProcessFact, pid int) int {
		return cmp.Compare(member.PID, pid)
	})
	if !ownerFound {
		return unattributedRuntimeNativeObservation{}, RuntimeRepairInspectionAmbiguous, nil
	}
	listener.OwnerBirthToken = members[ownerIndex].BirthToken
	observation := unattributedRuntimeNativeObservation{
		Target:     target,
		DaemonUID:  daemonUID,
		RootKind:   selectedRoot.Kind,
		Root:       rootFact,
		RootParent: runtimeRepairParentFact{PID: rootFact.ParentPID, BirthToken: parentBirth},
		Members:    members,
		Listener:   listener,
	}
	if err := observation.validate(); err != nil {
		return unattributedRuntimeNativeObservation{}, RuntimeRepairInspectionAmbiguous, nil
	}
	return observation, RuntimeRepairInspectionActionable, nil
}

// darwinUnattributedRuntimeRoot binds one selected root PID to the reason its checkout scope is trusted.
type darwinUnattributedRuntimeRoot struct {
	PID  int
	Kind unattributedRuntimeRootKind
}

// findDarwinUnattributedRuntimeRoots prefers one exact forj dev ancestor and otherwise selects the exact leased listener.
func findDarwinUnattributedRuntimeRoots(
	ctx context.Context,
	target RuntimeRepairTarget,
	daemonUID uint32,
	ownerPID int,
	processByPID map[int]unix.KinfoProc,
) ([]darwinUnattributedRuntimeRoot, error) {
	forjRoots := make([]darwinUnattributedRuntimeRoot, 0, 1)
	forjAddressRoots := make([]darwinUnattributedRuntimeRoot, 0, 1)
	var projectListenerRoot *darwinUnattributedRuntimeRoot
	var projectAddressRoot *darwinUnattributedRuntimeRoot
	seen := make(map[int]struct{}, len(processByPID))
	for pid := ownerPID; pid > 0; {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if _, duplicate := seen[pid]; duplicate {
			return nil, errDarwinRuntimeRepairUnstable
		}
		seen[pid] = struct{}{}
		process, found := processByPID[pid]
		if !found {
			return nil, errDarwinRuntimeRepairUnstable
		}
		if process.Eproc.Ucred.Uid == daemonUID && process.Eproc.Pcred.P_ruid == daemonUID {
			sessionID, err := unix.Getsid(pid)
			if errors.Is(err, syscall.ESRCH) {
				return nil, errDarwinRuntimeRepairUnstable
			}
			if err != nil || sessionID <= 0 {
				return nil, fmt.Errorf("read Darwin process %d session: %w", pid, errDarwinRuntimeRepairUnreadable)
			}
			fact, err := observeDarwinRuntimeRepairProcess(process, sessionID)
			if err != nil {
				return nil, err
			}
			if fact.CommandExact && filepath.Base(fact.ExecutableIdentity) == "forj" {
				if fact.WorkingDirectory == target.CheckoutRoot {
					forjRoots = append(forjRoots, darwinUnattributedRuntimeRoot{PID: pid, Kind: unattributedRuntimeRootForjDev})
				} else {
					forjAddressRoots = append(forjAddressRoots, darwinUnattributedRuntimeRoot{PID: pid, Kind: unattributedRuntimeRootProjectForj})
				}
			}
			if pid == ownerPID && fact.WorkingDirectory == target.CheckoutRoot {
				candidate := darwinUnattributedRuntimeRoot{PID: pid, Kind: unattributedRuntimeRootProjectListener}
				projectListenerRoot = &candidate
			} else if pid == ownerPID {
				// The exact primary lease is the Harbor-side project identity. When a generated
				// listener has changed directory or detached from its forj ancestor, retain only
				// the listener owner's same-user scope rather than leaving Start stuck on a stale port.
				candidate := darwinUnattributedRuntimeRoot{PID: pid, Kind: unattributedRuntimeRootProjectAddress}
				projectAddressRoot = &candidate
			}
		}
		parentPID := int(process.Eproc.Ppid)
		if parentPID <= 0 || parentPID == pid {
			break
		}
		pid = parentPID
	}
	if len(forjRoots) != 0 {
		return forjRoots, nil
	}
	if len(forjAddressRoots) != 0 {
		return forjAddressRoots, nil
	}
	if projectListenerRoot != nil {
		return []darwinUnattributedRuntimeRoot{*projectListenerRoot}, nil
	}
	if projectAddressRoot != nil {
		return []darwinUnattributedRuntimeRoot{*projectAddressRoot}, nil
	}
	return nil, nil
}

// darwinProcessDescendsFromRoot walks one current parent chain without treating a missing parent as stable ownership.
func darwinProcessDescendsFromRoot(pid, rootPID int, processByPID map[int]unix.KinfoProc) (bool, bool) {
	seen := make(map[int]struct{}, len(processByPID))
	for pid != rootPID {
		if _, duplicate := seen[pid]; duplicate {
			return false, false
		}
		seen[pid] = struct{}{}
		process, found := processByPID[pid]
		if !found || process.Eproc.Ppid <= 0 {
			return false, false
		}
		pid = int(process.Eproc.Ppid)
	}
	return true, true
}

// equalDarwinUnattributedRuntimeInspections compares classifications and every actionable scope fact.
func equalDarwinUnattributedRuntimeInspections(left, right unattributedRuntimeNativeInspection) bool {
	if left.State != right.State {
		return false
	}
	if left.Observation == nil || right.Observation == nil {
		return left.Observation == nil && right.Observation == nil
	}
	return unattributedRuntimeObservationsEqual(*left.Observation, *right.Observation)
}

// gracefullyTerminateDarwinUnattributedRuntime revalidates the exact scope before signaling only its displayed root.
func gracefullyTerminateDarwinUnattributedRuntime(ctx context.Context, receipt unattributedRuntimeReceipt) (bool, error) {
	inspection, err := inspectStableDarwinUnattributedRuntime(ctx, receipt.observation.Target)
	if err != nil {
		return false, err
	}
	if inspection.State != RuntimeRepairInspectionActionable || inspection.Observation == nil ||
		!unattributedRuntimeObservationsEqual(*inspection.Observation, receipt.observation) {
		return false, ErrRuntimeRepairDrift
	}
	root := receipt.observation.Root
	birth, present, err := observeProcessBirthToken(root.PID)
	if err != nil || !present || birth != root.BirthToken {
		return false, ErrRuntimeRepairDrift
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if err := syscall.Kill(root.PID, syscall.SIGTERM); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return false, ErrRuntimeRepairDrift
		}
		return false, fmt.Errorf("gracefully terminate exact Darwin unattributed runtime root: %w", err)
	}
	return true, nil
}

// forcefullyTerminateDarwinUnattributedRuntime revalidates the captured descendant births before SIGKILL escalation.
func forcefullyTerminateDarwinUnattributedRuntime(ctx context.Context, receipt unattributedRuntimeReceipt) (bool, error) {
	root := receipt.observation.Root
	rootBirth, present, err := observeProcessBirthToken(root.PID)
	if err != nil {
		return false, fmt.Errorf("revalidate Darwin unattributed runtime root before forceful termination: %w", err)
	}
	if present && rootBirth != root.BirthToken {
		return false, ErrRuntimeRepairDrift
	}
	if present {
		parentBirth, parentPresent, err := observeProcessBirthToken(receipt.observation.RootParent.PID)
		if err != nil || !parentPresent || parentBirth != receipt.observation.RootParent.BirthToken {
			return false, ErrRuntimeRepairDrift
		}
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}

	forced := false
	for _, member := range receipt.observation.Members {
		if err := ctx.Err(); err != nil {
			return forced, err
		}
		birth, present, err := observeProcessBirthToken(member.PID)
		if err != nil {
			return forced, fmt.Errorf("revalidate Darwin unattributed runtime member %d before forceful termination: %w", member.PID, err)
		}
		if !present || birth != member.BirthToken {
			continue
		}
		actualSessionID, err := unix.Getsid(member.PID)
		if errors.Is(err, syscall.ESRCH) {
			continue
		}
		if err != nil {
			return forced, fmt.Errorf("revalidate Darwin unattributed runtime member %d session: %w", member.PID, err)
		}
		if actualSessionID != member.SessionID {
			return forced, ErrRuntimeRepairDrift
		}
		if err := syscall.Kill(member.PID, syscall.SIGKILL); err != nil {
			if errors.Is(err, syscall.ESRCH) {
				continue
			}
			return forced, fmt.Errorf("forcefully terminate exact Darwin unattributed runtime member %d: %w", member.PID, err)
		}
		forced = true
	}
	return forced, nil
}

// observeDarwinUnattributedRuntimeSettlement proves captured births and the exact target socket have disappeared.
func observeDarwinUnattributedRuntimeSettlement(ctx context.Context, receipt unattributedRuntimeReceipt) (bool, error) {
	for _, member := range receipt.observation.Members {
		birth, present, err := observeProcessBirthToken(member.PID)
		if err != nil {
			return false, fmt.Errorf("observe Darwin unattributed runtime settlement: %w", err)
		}
		if present && birth == member.BirthToken {
			return false, nil
		}
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
