//go:build darwin || linux

package projectprocess

import (
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestRecoverableOwnedUnixSessionBirthRejectsFabricatedMarker prevents an unproved SID scan from malformed durable evidence.
func TestRecoverableOwnedUnixSessionBirthRejectsFabricatedMarker(t *testing.T) {
	_, err := recoverableOwnedUnixSessionBirth(424242, ownedUnixSessionBirthTokenPrefix)
	if err == nil || !strings.Contains(err.Error(), "versioned Unix session birth token") {
		t.Fatalf("recoverableOwnedUnixSessionBirth() error = %v", err)
	}
}

// TestRecoverableOwnedUnixSessionBirthRejectsLegacyBroadSession prevents recovery from targeting Harbor's inherited login session.
func TestRecoverableOwnedUnixSessionBirthRejectsLegacyBroadSession(t *testing.T) {
	command := exec.Command("sleep", "10")
	if err := command.Start(); err != nil {
		t.Fatalf("start legacy-scope fixture: %v", err)
	}
	t.Cleanup(func() {
		_ = command.Process.Kill()
		_ = command.Wait()
	})
	birthToken, err := processBirthToken(command.Process.Pid)
	if err != nil {
		t.Fatalf("processBirthToken() error = %v", err)
	}
	_, err = recoverableOwnedUnixSessionBirth(command.Process.Pid, birthToken)
	if err == nil || !strings.Contains(err.Error(), "does not prove a dedicated Unix session") {
		t.Fatalf("recoverableOwnedUnixSessionBirth() error = %v", err)
	}
}

// TestObserveOwnedProcessSessionSettlesExactZombieLeader proves an exited leader cannot cause permanent recovery quarantine.
func TestObserveOwnedProcessSessionSettlesExactZombieLeader(t *testing.T) {
	command := exec.Command("sh", "-c", "exit 0")
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := command.Start(); err != nil {
		t.Fatalf("start zombie session fixture: %v", err)
	}
	defer func() { _ = command.Wait() }()
	birthToken, err := processBirthToken(command.Process.Pid)
	if err != nil {
		t.Fatalf("processBirthToken() error = %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		zombie, present, err := unixProcessZombie(command.Process.Pid)
		if err != nil {
			t.Fatalf("unixProcessZombie() error = %v", err)
		}
		if present && zombie {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("session leader did not become a zombie")
		}
		time.Sleep(10 * time.Millisecond)
	}

	state, members, err := observeOwnedProcessSession(command.Process.Pid, birthToken)
	if err != nil || state != PriorProcessAbsent || len(members) != 0 {
		t.Fatalf("observeOwnedProcessSession(zombie) = %q, %#v, %v", state, members, err)
	}
}

// TestClassifyEmptyOwnedUnixSessionRejectsLiveLeader preserves fail-closed behavior when enumeration omits active authority.
func TestClassifyEmptyOwnedUnixSessionRejectsLiveLeader(t *testing.T) {
	command := exec.Command("sleep", "10")
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := command.Start(); err != nil {
		t.Fatalf("start live session fixture: %v", err)
	}
	t.Cleanup(func() {
		_ = command.Process.Kill()
		_ = command.Wait()
	})
	birthToken, err := processBirthToken(command.Process.Pid)
	if err != nil {
		t.Fatalf("processBirthToken() error = %v", err)
	}
	_, _, err = classifyEmptyOwnedUnixSession(command.Process.Pid, birthToken)
	if err == nil || !strings.Contains(err.Error(), "omitted its live leader") {
		t.Fatalf("classifyEmptyOwnedUnixSession(live) error = %v", err)
	}
}
