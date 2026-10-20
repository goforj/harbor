//go:build windows

package windowsinstallation

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// TestUninstallGuardAdmission refuses missing images and cancelled acquisition before native staging or user state access.
func TestUninstallGuardAdmission(t *testing.T) {
	installation := &Installation{}
	if _, err := installation.HoldUninstallGuard(context.Background(), nil); err == nil {
		t.Fatal("missing staged image admitted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := installation.HoldUninstallGuard(ctx, os.Stdin); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	for _, path := range []string{`C:\Users\runner\Temp\HarborUninstall-` + strings.Repeat("a", 32) + `\HarborUninstall.exe`, `C:\Windows\Temp\foreign\HarborUninstall.exe`, `C:\Windows\Temp\HarborUninstall-` + strings.Repeat("a", 32) + `\foreign.exe`, `C:\Windows\Temp\HarborUninstall-` + strings.Repeat("A", 32) + `\HarborUninstall.exe`, `C:\Windows\Temp\..\Temp\HarborUninstall-` + strings.Repeat("a", 32) + `\HarborUninstall.exe`} {
		if err := admitUninstallGuardStagePath(path, `C:\Windows`); err == nil {
			t.Fatalf("admitted guard stage %q", path)
		}
	}
	if err := admitUninstallGuardStagePath(`C:\Windows\Temp\HarborUninstall-`+strings.Repeat("a", 32)+`\HarborUninstall.exe`, `C:\Windows`); err != nil {
		t.Fatal(err)
	}
}

// TestUninstallGuardStageDescriptor refuses foreign grants, owners, or weakened mandatory labels without changing native state.
func TestUninstallGuardStageDescriptor(t *testing.T) {
	const userSID = "S-1-5-21-1-2-3-1000"
	uninstallGuardStageSDDL := uninstallGuardStagePolicy(userSID)
	exact, err := windows.SecurityDescriptorFromString(uninstallGuardStageSDDL)
	if err != nil {
		t.Fatal(err)
	}
	if err := admitUninstallGuardStageDescriptor(exact, true, userSID); err != nil {
		t.Fatal(err)
	}
	fileDescriptor, err := windows.SecurityDescriptorFromString(strings.Replace(uninstallGuardStageSDDL, "(ML;OICI;", "(ML;;", 1))
	if err != nil {
		t.Fatal(err)
	}
	if err := admitUninstallGuardStageDescriptor(fileDescriptor, false, userSID); err != nil {
		t.Fatal(err)
	}
	if err := admitUninstallGuardStageDescriptor(fileDescriptor, true, userSID); err == nil {
		t.Fatal("nonpropagating directory label admitted")
	}
	if err := admitUninstallGuardStageDescriptor(exact, false, userSID); err == nil {
		t.Fatal("propagating regular-file label admitted")
	}

	for _, text := range []string{
		strings.Replace(uninstallGuardStageSDDL, "0x1200a9", "FA", 1),
		strings.Replace(uninstallGuardStageSDDL, userSID, "S-1-5-21-1-2-3-1001", 1),
		strings.Replace(uninstallGuardStageSDDL, "(A;OICI;0x1200a9;;;"+userSID+")", "", 1),
		strings.Replace(uninstallGuardStageSDDL, "(A;OICI;0x1200a9;;;"+userSID+")", "(A;;0x1200a9;;;"+userSID+")", 1),
		strings.Replace(uninstallGuardStageSDDL, "O:BA", "O:SY", 1),
		strings.Replace(uninstallGuardStageSDDL, "D:P", "D:", 1),
		strings.Replace(uninstallGuardStageSDDL, "S:P", "(A;;FA;;;BU)S:P", 1),
		strings.Replace(uninstallGuardStageSDDL, "(A;OICI;FA;;;SY)", "", 1),
		strings.Replace(uninstallGuardStageSDDL, "(A;OICI;FA;;;SY)", "(A;OICI;FA;;;BA)", 1),
		strings.Replace(uninstallGuardStageSDDL, "(A;OICI;FA;;;SY)", "(A;;FA;;;SY)", 1),
		strings.Replace(uninstallGuardStageSDDL, "(A;OICI;FA;;;SY)", "(A;OICI;FR;;;SY)", 1),
		strings.Split(uninstallGuardStageSDDL, "S:P")[0],
		strings.Replace(uninstallGuardStageSDDL, ";;;HI)", ";;;ME)", 1),
		strings.Replace(uninstallGuardStageSDDL, ";NW;", ";NR;", 1),
		strings.Replace(uninstallGuardStageSDDL, "(ML;OICI;", "(ML;;", 1),
	} {
		descriptor, err := windows.SecurityDescriptorFromString(text)
		if err != nil {
			t.Fatalf("descriptor fixture %q: %v", text, err)
		}
		if err := admitUninstallGuardStageDescriptor(descriptor, true, userSID); err == nil {
			t.Fatalf("admitted guard descriptor %q", text)
		}
	}
}

// TestNativeUninstallGuardEventAdmission proves native object typing and exact inherited capability directions.
func TestNativeUninstallGuardEventAdmission(t *testing.T) {
	event, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(event)
	for _, rights := range []uint32{windows.EVENT_MODIFY_STATE, windows.SYNCHRONIZE} {
		var inherited windows.Handle
		if err := windows.DuplicateHandle(windows.CurrentProcess(), event, windows.CurrentProcess(), &inherited, rights, true, 0); err != nil {
			t.Fatal(err)
		}
		if err := admitUninstallGuardEvent(inherited, rights); err != nil {
			windows.CloseHandle(inherited)
			t.Fatal(err)
		}
		if err := admitUninstallGuardEvent(inherited, rights|windows.READ_CONTROL); err == nil {
			t.Fatal("event with a different grant was admitted")
		}
		if err := windows.SetHandleInformation(inherited, windows.HANDLE_FLAG_INHERIT, 0); err != nil {
			t.Fatal(err)
		}
		if err := admitUninstallGuardEvent(inherited, rights); err == nil {
			t.Fatal("noninherited event was admitted")
		}
		windows.CloseHandle(inherited)
	}
	var process windows.Handle
	if err := windows.DuplicateHandle(windows.CurrentProcess(), windows.CurrentProcess(), windows.CurrentProcess(), &process, windows.SYNCHRONIZE, true, 0); err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(process)
	if err := admitUninstallGuardEvent(process, windows.SYNCHRONIZE); err == nil {
		t.Fatal("process handle was admitted as release event")
	}
	if err := admitUninstallGuardEvent(0, windows.SYNCHRONIZE); err == nil {
		t.Fatal("absent event admitted")
	}
}

// TestNativeUninstallGuardHandshake verifies that readiness occurs inside the held callback and only the release capability ends it.
func TestNativeUninstallGuardHandshake(t *testing.T) {
	ready, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(ready)
	release, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(release)
	defer windows.SetEvent(release)
	var lease sync.Mutex
	finished := make(chan error, 1)
	go func() {
		finished <- holdUninstallGuardEvents(ready, release, func(callback func() error) error { lease.Lock(); defer lease.Unlock(); return callback() })
	}()
	if state, err := windows.WaitForSingleObject(ready, 5000); err != nil || state != windows.WAIT_OBJECT_0 {
		t.Fatalf("readiness=%d,error=%v", state, err)
	}
	if lease.TryLock() {
		lease.Unlock()
		t.Fatal("readiness was published outside the held lease")
	}
	select {
	case err := <-finished:
		t.Fatalf("lease ended without release: %v", err)
	default:
	}
	if err := windows.SetEvent(release); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("release did not settle the lease")
	}
	if !lease.TryLock() {
		t.Fatal("callback lease remained held after release")
	}
	lease.Unlock()
	denied := errors.New("active daemon")
	if err := holdUninstallGuardEvents(ready, release, func(func() error) error { return denied }); !errors.Is(err, denied) {
		t.Fatal(err)
	}
}

// TestUninstallGuardContext rejects cancellation and released guards without accessing a stale native process handle.
func TestUninstallGuardContext(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	guard := &UninstallGuard{context: ctx, cancel: cancel}
	cause := errors.New("caller stopped cleanup")
	cancel(cause)
	if guard.Context() != ctx || !errors.Is(guard.Check(), cause) {
		t.Fatal("guard cancellation cause was lost")
	}
	guard.closed = true
	if err := guard.Check(); err == nil {
		t.Fatal("released guard admitted more cleanup")
	}
	if err := guard.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestNativeUninstallGuardWorker proves a real signed E0 worker holds the daemon lock, releases it, and reports exact owned-worker loss.
func TestNativeUninstallGuardWorker(t *testing.T) {
	root, agent := os.Getenv("HARBOR_ADMISSION_FIXTURE_ROOT"), os.Getenv("HARBOR_ADMISSION_MAINTENANCE_AGENT")
	if root == "" || agent == "" {
		t.Skip("signed native maintenance agent fixture was not configured")
	}
	facts, factsErr := inspectRuntimeToken(windows.GetCurrentProcessToken())
	t.Logf("native guard staging creator facts %+v (inspection error %v)", facts, factsErr)
	publisher := os.Getenv("HARBOR_ADMISSION_PUBLISHER")
	payloads := map[string][]byte{".txt": []byte(publisher)}
	for extension, path := range map[string]string{".exe": agent, ".ps1": os.Getenv("HARBOR_ADMISSION_SIGNED_SCRIPT"), ".cer": os.Getenv("HARBOR_ADMISSION_CERTIFICATE")} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		payloads[extension] = data
	}
	machine := filepath.Join(root, "guard-machine")
	_, selection := writeSignedFixture(t, machine, publisher, payloads)
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	selection.UserSID = user.User.Sid.String()
	writeFixtureJSON(t, filepath.Join(machine, "installation.json"), selection)
	installation, err := admitAt(machine, publisher)
	if err != nil {
		t.Fatal(err)
	}
	defer installation.Close()
	staged := writeNativeGuardStage(t, payloads[".exe"])
	if err := probeNativeGuardStage(staged.Name()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	guard, err := installation.HoldUninstallGuard(ctx, staged)
	if err != nil {
		t.Fatal(err)
	}
	defer guard.Close()
	t.Logf("actual guard PID %d and effective facts %+v", guard.child.PID, guard.child.Facts)
	if err := guard.Check(); err != nil {
		t.Fatal(err)
	}
	if second, err := installation.HoldUninstallGuard(ctx, staged); err == nil {
		second.Close()
		t.Fatal("second worker acquired the same daemon lock")
	}
	if err := guard.Close(); err != nil {
		t.Fatal(err)
	}
	next, err := installation.HoldUninstallGuard(ctx, staged)
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close()
	if err := next.child.abort(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-next.Context().Done():
	case <-time.After(5 * time.Second):
		t.Fatal("owned worker loss did not cancel cleanup")
	}
	if err := next.Check(); err == nil {
		t.Fatal("lost worker admitted another cleanup effect")
	}
	if err := next.Close(); err == nil {
		t.Fatal("unexpected owned worker termination was reported successful")
	}
	spontaneous, err := installation.HoldUninstallGuard(ctx, staged)
	if err != nil {
		t.Fatal(err)
	}
	defer spontaneous.Close()
	if err := windows.SetEvent(spontaneous.release); err != nil {
		t.Fatal(err)
	}
	select {
	case <-spontaneous.Context().Done():
	case <-time.After(5 * time.Second):
		t.Fatal("unexpected zero-exit guard did not cancel cleanup")
	}
	if err := spontaneous.Close(); err == nil {
		t.Fatal("unexpected zero-exit guard was reported successful")
	}
	final, err := installation.HoldUninstallGuard(ctx, staged)
	if err != nil {
		t.Fatal(err)
	}
	if err := final.Close(); err != nil {
		t.Fatal(err)
	}
}

// writeNativeGuardStage creates only a unique protected Windows Temp fixture and preserves any unknown contents during cleanup.
func writeNativeGuardStage(t *testing.T, payload []byte) *os.File {
	t.Helper()
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	uninstallGuardStageSDDL := uninstallGuardStagePolicy(user.User.Sid.String())
	system, err := windows.GetWindowsDirectory()
	if err != nil {
		t.Fatalf("resolve native guard Windows directory: %v", err)
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		t.Fatalf("generate native guard stage nonce: %v", err)
	}
	root := filepath.Join(system, "Temp", "HarborUninstall-"+hex.EncodeToString(nonce[:]))
	descriptor, err := windows.SecurityDescriptorFromString(strings.Split(uninstallGuardStageSDDL, "S:")[0])
	if err != nil {
		t.Fatalf("construct native guard creation DACL: %v", err)
	}
	attributes := &windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: descriptor}
	if err := windows.CreateDirectory(windows.StringToUTF16Ptr(root), attributes); err != nil {
		t.Fatalf("create native guard stage directory %q: %v", root, err)
	}
	labelNativeGuardStage(t, root, true)
	t.Cleanup(func() {
		if err := os.Remove(root); err != nil {
			t.Error(err)
		}
	})
	path := filepath.Join(root, "HarborUninstall.exe")
	handle, err := windows.CreateFile(windows.StringToUTF16Ptr(path), windows.GENERIC_WRITE, 0, attributes, windows.CREATE_NEW, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatalf("create native guard stage file %q: %v", path, err)
	}
	file := os.NewFile(uintptr(handle), path)
	_, writeErr := file.Write(payload)
	if err := errors.Join(writeErr, file.Sync(), file.Close()); err != nil {
		t.Fatalf("write/sync/close native guard stage file %q: %v", path, err)
	}
	labelNativeGuardStage(t, path, false)
	t.Cleanup(func() {
		if err := os.Remove(path); err != nil {
			t.Error(err)
		}
	})
	retained, err := openUninstallGuardObject(path, false)
	if err != nil {
		t.Fatalf("retain native guard stage file %q: %v", path, err)
	}
	t.Cleanup(func() {
		if err := retained.Close(); err != nil {
			t.Error(err)
		}
	})
	return retained
}

// labelNativeGuardStage exercises the production label-only policy without requiring the audit SACL privilege in the elevated fixture process.
func labelNativeGuardStage(t *testing.T, path string, directory bool) {
	t.Helper()
	flags := uint32(windows.FILE_FLAG_OPEN_REPARSE_POINT)
	if directory {
		flags |= windows.FILE_FLAG_BACKUP_SEMANTICS
	}
	handle, err := windows.CreateFile(windows.StringToUTF16Ptr(path), windows.WRITE_OWNER|windows.READ_CONTROL|windows.FILE_READ_ATTRIBUTES, windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING, flags, 0)
	if err != nil {
		t.Fatalf("open native guard label target %q (directory=%t): %v", path, directory, err)
	}
	defer windows.CloseHandle(handle)
	descriptor, err := windows.SecurityDescriptorFromString(uninstallGuardStageSDDL)
	if err != nil {
		t.Fatalf("construct native guard mandatory label: %v", err)
	}
	label, _, err := descriptor.SACL()
	if err != nil {
		t.Fatalf("extract native guard mandatory label: %v", err)
	}
	if err := windows.SetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.LABEL_SECURITY_INFORMATION, nil, nil, nil, label); err != nil {
		t.Fatalf("set native guard mandatory label %q: %v", path, err)
	}
}

// probeNativeGuardStage proves the actual restricted token can load both stage objects but cannot mutate their bytes, names, or security.
func probeNativeGuardStage(path string) (result error) {
	var source windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY|windows.TOKEN_DUPLICATE, &source); err != nil {
		return err
	}
	defer source.Close()
	restricted, err := restrictedRuntimeToken(source)
	if err != nil {
		return err
	}
	defer restricted.Close()
	var token windows.Token
	if err := windows.DuplicateTokenEx(restricted, windows.TOKEN_IMPERSONATE|windows.TOKEN_QUERY, nil, windows.SecurityImpersonation, windows.TokenImpersonation, &token); err != nil {
		return err
	}
	defer token.Close()
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := windows.SetThreadToken(nil, token); err != nil {
		return err
	}
	defer func() { result = errors.Join(result, windows.RevertToSelf()) }()
	for _, target := range []string{filepath.Dir(path), path} {
		flags := uint32(windows.FILE_FLAG_OPEN_REPARSE_POINT)
		if target != path {
			flags |= windows.FILE_FLAG_BACKUP_SEMANTICS
		}
		handle, err := windows.CreateFile(windows.StringToUTF16Ptr(target), windows.GENERIC_READ|windows.GENERIC_EXECUTE, windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING, flags, 0)
		if err != nil {
			return fmt.Errorf("actual restricted loader read/execute %q: %w", target, err)
		}
		if err := windows.CloseHandle(handle); err != nil {
			return err
		}
		for _, access := range []uint32{windows.GENERIC_WRITE, windows.DELETE, windows.WRITE_DAC, windows.WRITE_OWNER} {
			handle, err := windows.CreateFile(windows.StringToUTF16Ptr(target), access, windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING, flags, 0)
			if err == nil {
				windows.CloseHandle(handle)
				return fmt.Errorf("actual restricted loader gained stage mutation %#x on %q", access, target)
			}
			if !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
				return fmt.Errorf("actual restricted loader mutation %#x on %q failed ambiguously: %w", access, target, err)
			}
		}
	}
	return nil
}
