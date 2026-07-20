//go:build darwin || linux

package projectprocess

import (
	"errors"
	"fmt"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// unixProcessMember captures enough immutable identity to revalidate one session member at signal time.
type unixProcessMember struct {
	PID        int
	BirthToken string
}

// hostProcessBirthToken removes Harbor's ownership-scope marker before comparing the kernel birth identity.
func hostProcessBirthToken(persistedBirthToken string) string {
	return strings.TrimPrefix(persistedBirthToken, ownedUnixSessionBirthTokenPrefix)
}

// observePersistedOwnedProcessSession proves that persisted evidence names a dedicated session before enumerating it.
func observePersistedOwnedProcessSession(
	pid int,
	persistedBirthToken string,
) (PriorProcessState, []unixProcessMember, error) {
	rawBirthToken, err := recoverableOwnedUnixSessionBirth(pid, persistedBirthToken)
	if err != nil {
		return "", nil, err
	}
	return observeOwnedProcessSession(pid, rawBirthToken)
}

// recoverableOwnedUnixSessionBirth rejects legacy process-group evidence that cannot bound separate watcher groups.
func recoverableOwnedUnixSessionBirth(pid int, persistedBirthToken string) (string, error) {
	rawBirthToken := hostProcessBirthToken(persistedBirthToken)
	if strings.HasPrefix(persistedBirthToken, ownedUnixSessionBirthTokenPrefix) {
		if err := validateUnixProcessBirthToken(rawBirthToken); err != nil {
			return "", fmt.Errorf("validate versioned Unix session birth token: %w", err)
		}
		return rawBirthToken, nil
	}
	state, err := observePriorProcessState(pid, rawBirthToken, observeProcessBirthToken)
	if err != nil {
		return "", err
	}
	if state != PriorProcessPresent {
		return "", legacyUnixProcessScopeError(pid, state)
	}
	sessionID, err := unix.Getsid(pid)
	if errors.Is(err, syscall.ESRCH) {
		return "", legacyUnixProcessScopeError(pid, PriorProcessAbsent)
	}
	if err != nil {
		return "", fmt.Errorf("inspect legacy Unix process %d session: %w", pid, err)
	}
	if sessionID != pid {
		return "", legacyUnixProcessScopeError(pid, PriorProcessPresent)
	}
	return rawBirthToken, nil
}

// legacyUnixProcessScopeError retains evidence when a pre-session launch cannot prove its complete descendant boundary.
func legacyUnixProcessScopeError(pid int, state PriorProcessState) error {
	return fmt.Errorf(
		"legacy Harbor process PID %d state %q does not prove a dedicated Unix session; stop any remaining project processes outside Harbor before retrying recovery",
		pid,
		state,
	)
}

// observeOwnedProcessSession classifies the launch PID and every live descendant that still shares its session ID.
func observeOwnedProcessSession(
	sessionID int,
	expectedRootBirth string,
) (PriorProcessState, []unixProcessMember, error) {
	rootState, err := observePriorProcessState(sessionID, expectedRootBirth, observeProcessBirthToken)
	if err != nil || rootState == PriorProcessReplaced {
		return rootState, nil, err
	}
	members, err := unixSessionMembers(sessionID)
	if err != nil {
		return "", nil, err
	}
	rootState, err = observePriorProcessState(sessionID, expectedRootBirth, observeProcessBirthToken)
	if err != nil || rootState == PriorProcessReplaced {
		return rootState, nil, err
	}
	if len(members) == 0 {
		return classifyEmptyOwnedUnixSession(sessionID, expectedRootBirth)
	}
	return PriorProcessPresent, members, nil
}

// classifyEmptyOwnedUnixSession distinguishes an exited zombie leader from an unexpectedly omitted live leader.
func classifyEmptyOwnedUnixSession(
	sessionID int,
	expectedRootBirth string,
) (PriorProcessState, []unixProcessMember, error) {
	zombie, present, err := unixProcessZombie(sessionID)
	if err != nil {
		return "", nil, fmt.Errorf("inspect owned Unix session %d leader state: %w", sessionID, err)
	}
	rootState, err := observePriorProcessState(sessionID, expectedRootBirth, observeProcessBirthToken)
	if err != nil || rootState != PriorProcessPresent {
		return rootState, nil, err
	}
	if !present {
		return "", nil, fmt.Errorf("owned Unix session %d leader appeared after live-member enumeration", sessionID)
	}
	if zombie {
		return PriorProcessAbsent, nil, nil
	}
	return "", nil, fmt.Errorf("owned Unix session %d omitted its live leader", sessionID)
}

// signalOwnedProcessSession signals only members whose birth and session identity still match the observed scope.
func signalOwnedProcessSession(
	sessionID int,
	expectedRootBirth string,
	signal syscall.Signal,
) (PriorProcessState, error) {
	state, members, err := observeOwnedProcessSession(sessionID, expectedRootBirth)
	if err != nil || state != PriorProcessPresent {
		return state, err
	}
	for _, member := range members {
		if err := signalExactUnixSessionMember(sessionID, member, signal); err != nil {
			return PriorProcessPresent, err
		}
	}
	return PriorProcessPresent, nil
}

// forceOwnedProcessSession closes short fork races by rescanning the session until no live member remains or observation times out.
func forceOwnedProcessSession(sessionID int, expectedRootBirth string) (PriorProcessState, error) {
	initialState := PriorProcessState("")
	deadline := time.Now().Add(forceSettlementPeriod)
	for {
		state, err := signalOwnedProcessSession(sessionID, expectedRootBirth, syscall.SIGKILL)
		if err != nil {
			return state, err
		}
		if initialState == "" {
			initialState = state
		}
		if state != PriorProcessPresent {
			return initialState, nil
		}
		state, _, err = observeOwnedProcessSession(sessionID, expectedRootBirth)
		if err != nil {
			return PriorProcessPresent, err
		}
		if state != PriorProcessPresent || time.Now().After(deadline) {
			if state != PriorProcessPresent {
				return PriorProcessPresent, nil
			}
			return PriorProcessPresent, fmt.Errorf(
				"owned Unix session %d remained active %s after forceful termination",
				sessionID,
				forceSettlementPeriod,
			)
		}
		time.Sleep(forceSettlementPoll)
	}
}

// forceUnattachedProcessSession uses the unreaped child PID as safe session authority before birth capture completes.
func forceUnattachedProcessSession(sessionID int) error {
	deadline := time.Now().Add(forceSettlementPeriod)
	for {
		members, err := unixSessionMembers(sessionID)
		if err != nil {
			return err
		}
		if len(members) == 0 {
			return nil
		}
		for _, member := range members {
			if err := signalExactUnixSessionMember(sessionID, member, syscall.SIGKILL); err != nil {
				return err
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf(
				"unattached Unix session %d remained active %s after forceful termination",
				sessionID,
				forceSettlementPeriod,
			)
		}
		time.Sleep(forceSettlementPoll)
	}
}

// signalExactUnixSessionMember prevents PID reuse or session movement from widening Harbor's signal target.
func signalExactUnixSessionMember(sessionID int, member unixProcessMember, signal syscall.Signal) error {
	birthToken, present, err := observeProcessBirthToken(member.PID)
	if err != nil {
		return fmt.Errorf("revalidate Unix session member %d birth: %w", member.PID, err)
	}
	if !present || birthToken != member.BirthToken {
		return nil
	}
	actualSessionID, err := unix.Getsid(member.PID)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("revalidate Unix session member %d scope: %w", member.PID, err)
	}
	if actualSessionID != sessionID {
		return nil
	}
	err = syscall.Kill(member.PID, signal)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("signal Unix session member %d: %w", member.PID, err)
	}
	return nil
}
