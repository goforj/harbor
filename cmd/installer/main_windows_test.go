//go:build windows

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"unsafe"

	"github.com/goforj/harbor/internal/platform/windowsinstallation"
	"golang.org/x/sys/windows"
)

// TestWindowsMaintenanceDispatch rejects additional authority arguments before session admission.
func TestWindowsMaintenanceDispatch(t *testing.T) {
	for _, arguments := range [][]string{{"--unknown"}, {"--register-task", "--sid=S-1-5-18"}, {"--remove-task", "extra"}, {"--uninstall", "C:\\foreign"}, {"--finish-uninstall"}, {"--finish-uninstall", "--parent-handle=0"}, {"--finish-uninstall", "--parent-handle=04"}, {"--finish-uninstall", "--parent-handle=4", "extra"}} {
		if err := runMaintenance(context.Background(), arguments, maintenanceOperations{}); err == nil {
			t.Fatalf("admitted arguments %q", arguments)
		}
	}
	for _, action := range []string{"--register-task", "--complete-install", "--remove-task", "--uninstall", "--finish-uninstall"} {
		t.Run(action, func(t *testing.T) {
			var events []string
			operations := maintenanceOperations{
				verify:    func(bool) error { events = append(events, "verify"); return nil },
				register:  func(context.Context) error { events = append(events, "register"); return nil },
				complete:  func(context.Context) error { events = append(events, "complete"); return nil },
				remove:    func(context.Context) error { events = append(events, "remove"); return nil },
				bootstrap: func(context.Context) error { events = append(events, "bootstrap"); return nil },
				finish: func(_ context.Context, parent windows.Handle) error {
					if parent != 4 {
						t.Fatalf("parent=%d", parent)
					}
					events = append(events, "finish")
					return nil
				},
			}
			arguments := []string{action}
			expected := ""
			switch action {
			case "--register-task":
				expected = "register"
			case "--complete-install":
				expected = "complete"
			case "--remove-task":
				expected = "remove"
			case "--uninstall":
				expected = "bootstrap"
			case "--finish-uninstall":
				arguments = append(arguments, "--parent-handle=4")
				expected = "finish"
			}
			if err := runMaintenance(context.Background(), arguments, operations); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(events, []string{"verify", expected}) {
				t.Fatal(events)
			}
			denied := errors.New("different interactive administrator")
			operations.verify = func(bool) error { return denied }
			if err := runMaintenance(context.Background(), arguments, operations); !errors.Is(err, denied) {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(events, []string{"verify", expected}) {
				t.Fatal("denied authority performed an operation")
			}
		})
	}
}

// TestWindowsMaintenanceParentSyntax permits only canonical positive internal handle values.
func TestWindowsMaintenanceParentSyntax(t *testing.T) {
	if handle, err := parseMaintenanceParent("--parent-handle=4"); err != nil || handle != 4 {
		t.Fatalf("handle=%d,error=%v", handle, err)
	}
	for _, text := range []string{"", "4", "--parent-handle=", "--parent-handle=-1", "--parent-handle=+4", "--parent-handle=04", "--parent-handle=0x4", "--parent-handle=18446744073709551615", "--parent-handle=18446744073709551616", "--parent-handle=4 --target=foreign"} {
		if _, err := parseMaintenanceParent(text); err == nil {
			t.Fatalf("admitted handle %q", text)
		}
	}
}

// TestWindowsMaintenanceStagePaths refuses mutable caller Temp paths and noncanonical continuation names.
func TestWindowsMaintenanceStagePaths(t *testing.T) {
	vendor := `C:\Program Files\GoForj`
	stage := `C:\Program Files\GoForj\HarborUninstall-` + strings.Repeat("a", 32) + `\HarborUninstall.exe`
	if err := admitMaintenanceStagePath(stage, vendor); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{`C:\Windows\Temp\HarborUninstall-` + strings.Repeat("a", 32) + `\HarborUninstall.exe`, `C:\Users\runner\Temp\HarborUninstall-` + strings.Repeat("a", 32) + `\HarborUninstall.exe`, strings.Replace(stage, "HarborUninstall.exe", "foreign.exe", 1), strings.Replace(stage, "HarborUninstall-", "foreign-", 1), strings.Replace(stage, strings.Repeat("a", 32), strings.Repeat("A", 32), 1), strings.Replace(stage, `GoForj\`, `GoForj\..\GoForj\`, 1), `relative\HarborUninstall.exe`, stage + `:alternate`} {
		if err := admitMaintenanceStagePath(path, vendor); err == nil {
			t.Fatalf("admitted staging path %q", path)
		}
	}
}

// TestWindowsMaintenanceStageSecurity exercises every staged ACL and mandatory-label rejection without host mutation.
func TestWindowsMaintenanceStageSecurity(t *testing.T) {
	const userSID = "S-1-5-21-1-2-3-1000"
	maintenanceStageSDDL := maintenanceStagePolicy(userSID)
	exact, err := windows.SecurityDescriptorFromString(maintenanceStageSDDL)
	if err != nil {
		t.Fatal(err)
	}
	if err := admitMaintenanceDescriptor(exact, true, userSID); err != nil {
		t.Fatal(err)
	}
	fileDescriptor, err := windows.SecurityDescriptorFromString(strings.Replace(maintenanceStageSDDL, "(ML;OICI;", "(ML;;", 1))
	if err != nil {
		t.Fatal(err)
	}
	if err := admitMaintenanceDescriptor(fileDescriptor, false, userSID); err != nil {
		t.Fatal(err)
	}
	if err := admitMaintenanceDescriptor(fileDescriptor, true, userSID); err == nil {
		t.Fatal("nonpropagating directory label admitted")
	}
	if err := admitMaintenanceDescriptor(exact, false, userSID); err == nil {
		t.Fatal("propagating regular-file label admitted")
	}

	for _, descriptor := range []string{
		strings.Replace(maintenanceStageSDDL, "0x1200a9", "FA", 1),
		strings.Replace(maintenanceStageSDDL, userSID, "S-1-5-21-1-2-3-1001", 1),
		strings.Replace(maintenanceStageSDDL, "(A;OICI;0x1200a9;;;"+userSID+")", "", 1),
		strings.Replace(maintenanceStageSDDL, "(A;OICI;0x1200a9;;;"+userSID+")", "(A;;0x1200a9;;;"+userSID+")", 1),
		strings.Replace(maintenanceStageSDDL, "O:BA", "O:SY", 1),
		strings.Replace(maintenanceStageSDDL, "D:P", "D:", 1),
		strings.Replace(maintenanceStageSDDL, "(A;OICI;FA;;;SY)", "", 1),
		strings.Replace(maintenanceStageSDDL, "S:P", "(A;;FA;;;BU)S:P", 1),
		strings.Replace(maintenanceStageSDDL, "(A;OICI;FA;;;SY)", "(A;OICI;FA;;;BA)", 1),
		strings.Replace(maintenanceStageSDDL, "(A;OICI;FA;;;SY)", "(A;OICI;FR;;;SY)", 1),
		strings.Replace(maintenanceStageSDDL, "(A;OICI;FA;;;SY)", "(A;;FA;;;SY)", 1),
		strings.Split(maintenanceStageSDDL, "S:P")[0],
		strings.Replace(maintenanceStageSDDL, ";;;HI)", ";;;ME)", 1),
		strings.Replace(maintenanceStageSDDL, ";NW;", ";NR;", 1),
		strings.Replace(maintenanceStageSDDL, "(ML;OICI;", "(ML;;", 1),
	} {
		native, err := windows.SecurityDescriptorFromString(descriptor)
		if err != nil {
			t.Fatalf("fixture descriptor %q: %v", descriptor, err)
		}
		if err := admitMaintenanceDescriptor(native, true, userSID); err == nil {
			t.Fatalf("admitted descriptor %q", descriptor)
		}
	}
}

// TestNativeMaintenanceMappedImage binds the actual executable mapping to a native file identity and refuses a different file.
func TestNativeMaintenanceMappedImage(t *testing.T) {
	current, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	current, err = filepath.EvalSymlinks(current)
	if err != nil {
		t.Fatal(err)
	}
	file, err := openMaintenanceObject(current, false)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := admitMaintenanceImage(file); err != nil {
		t.Fatal(err)
	}
	foreign, err := os.CreateTemp(t.TempDir(), "foreign-*.exe")
	if err != nil {
		t.Fatal(err)
	}
	defer foreign.Close()
	if err := admitMaintenanceImage(foreign); err == nil {
		t.Fatal("different executing image identity admitted")
	}
}

// TestNativeMaintenanceParentRefusal rejects absent, noninherited, and overprivileged receipts without signaling a process.
func TestNativeMaintenanceParentRefusal(t *testing.T) {
	if err := admitMaintenanceParent(0, `C:\foreign.exe`, "S-1-5-21-1-2-3-1000"); err == nil {
		t.Fatal("zero parent handle admitted")
	}
	for _, fixture := range []struct {
		rights  uint32
		inherit bool
	}{{maintenanceParentRights, false}, {windows.PROCESS_ALL_ACCESS, true}, {maintenanceParentRights, true}} {
		var handle windows.Handle
		if err := windows.DuplicateHandle(windows.CurrentProcess(), windows.CurrentProcess(), windows.CurrentProcess(), &handle, fixture.rights, fixture.inherit, 0); err != nil {
			t.Fatal(err)
		}
		err := admitMaintenanceParent(handle, `C:\foreign.exe`, "S-1-5-21-1-2-3-1000")
		windows.CloseHandle(handle)
		if err == nil {
			t.Fatalf("foreign parent receipt admitted: %+v", fixture)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := waitMaintenanceParent(ctx, windows.CurrentProcess()); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

// TestNativeMaintenanceProtectedStage verifies the actual protected vendor ACL, label, and replacement-denying handles in disposable CI.
func TestNativeMaintenanceProtectedStage(t *testing.T) {
	if os.Getenv("HARBOR_ADMISSION_FIXTURE_ROOT") == "" {
		t.Skip("disposable native CI fixture was not configured")
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	userSID := user.User.Sid.String()
	maintenanceStageSDDL := maintenanceStagePolicy(userSID)
	parents, err := windowsinstallation.RetainUninstallStageParent()
	if err != nil {
		t.Fatal(err)
	}
	defer closeMaintenanceFiles(parents)
	vendor := parents[len(parents)-1].Name()
	root, err := os.MkdirTemp(vendor, "HarborMaintenanceFixture-")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(root); err != nil {
		t.Fatal(err)
	}
	descriptor, err := windows.SecurityDescriptorFromString(strings.Split(maintenanceStageSDDL, "S:")[0])
	if err != nil {
		t.Fatal(err)
	}
	attributes := &windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: descriptor}
	if err := windows.CreateDirectory(windows.StringToUTF16Ptr(root), attributes); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.Remove(root); err != nil {
			t.Error(err)
		}
	}()
	directory, err := labelMaintenanceDirectory(root)
	if err != nil {
		t.Fatal(err)
	}
	defer directory.Close()
	if err := admitMaintenanceStage(directory, userSID); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "fixture.exe")
	handle, err := windows.CreateFile(windows.StringToUTF16Ptr(path), windows.GENERIC_READ|windows.GENERIC_WRITE|windows.READ_CONTROL|windows.WRITE_OWNER, 0, attributes, windows.CREATE_NEW, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		t.Fatal(err)
	}
	labelErr := labelMaintenanceObject(handle)
	windows.CloseHandle(handle)
	if labelErr != nil {
		t.Fatal(labelErr)
	}
	defer func() {
		if err := os.Remove(path); err != nil {
			t.Error(err)
		}
	}()
	file, err := openMaintenanceObject(path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := admitMaintenanceStage(file, userSID); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path, path+".foreign"); err == nil {
		t.Fatal("retained staged agent allowed replacement")
	}
}

// TestWindowsMaintenanceReceipt binds observational identities and never records failed cleanup as complete.
func TestWindowsMaintenanceReceipt(t *testing.T) {
	selection := windowsinstallation.Selection{InstallationID: strings.Repeat("a", 32), BundleDigest: strings.Repeat("b", 64)}
	original := &maintenanceProcessIdentity{PID: 10, NativeBirth: "134000000000000001", UserSID: "S-1-5-21-1-2-3-1000", Session: 2}
	continuation := &maintenanceProcessIdentity{PID: 11, NativeBirth: "134000000000000002", UserSID: original.UserSID, Session: 2}
	complete := maintenanceReceipt(selection, original, continuation, nil)
	if complete.Status != "complete" || complete.InstallationID != selection.InstallationID || complete.BundleDigest != selection.BundleDigest || complete.OriginalParent != original || complete.Continuation != continuation || complete.Error != "" {
		t.Fatalf("receipt=%+v", complete)
	}
	failed := maintenanceReceipt(selection, original, continuation, errors.New(strings.Repeat("x", 5000)))
	if failed.Status != "failed" || len(failed.Error) != 4096 || failed.InstallationID != complete.InstallationID || failed.Continuation.NativeBirth != continuation.NativeBirth {
		t.Fatalf("failed receipt=%+v", failed)
	}
}

// TestWindowsMaintenanceUserEntrypoints accepts only the two conventional uninstall aliases and preserves quiet errors.
func TestWindowsMaintenanceUserEntrypoints(t *testing.T) {
	for _, arguments := range [][]string{nil, {"--silent"}} {
		called := false
		operations := maintenanceOperations{verify: func(allowElevation bool) error {
			if !allowElevation {
				t.Fatal("uninstall entry did not permit explicit elevation")
			}
			return nil
		}, bootstrap: func(context.Context) error { called = true; return nil }}
		if err := runMaintenance(context.Background(), arguments, operations); err != nil || !called {
			t.Fatalf("arguments=%q,called=%v,error=%v", arguments, called, err)
		}
	}
	if !interactiveMaintenance(nil) || !interactiveMaintenance([]string{"--uninstall"}) {
		t.Fatal("interactive uninstall error would be hidden")
	}
	for _, arguments := range [][]string{{"--silent"}, {"--finish-uninstall"}, {"--complete-install"}, {"--register-task"}, {"--remove-task"}, {"--hold-stopped"}} {
		if interactiveMaintenance(arguments) {
			t.Fatalf("internal/quiet role would display modal error: %q", arguments)
		}
	}
	if unsafe.Sizeof(maintenanceShellExecuteInfo{}) != 112 {
		t.Fatalf("SHELLEXECUTEINFOW layout=%d", unsafe.Sizeof(maintenanceShellExecuteInfo{}))
	}
}

// TestWindowsUninstallGuardDispatch grants only the fixed restricted-worker role and its distinct typed event capabilities.
func TestWindowsUninstallGuardDispatch(t *testing.T) {
	for _, arguments := range [][]string{{"--hold-stopped"}, {"--hold-stopped", "--ready-handle=4"}, {"--hold-stopped", "--release-handle=4", "--ready-handle=8"}, {"--hold-stopped", "--ready-handle=04", "--release-handle=8"}, {"--hold-stopped", "--ready-handle=4", "--release-handle=4"}, {"--hold-stopped", "--ready-handle=4", "--release-handle=8", "extra"}} {
		if err := runMaintenance(context.Background(), arguments, maintenanceOperations{}); err == nil {
			t.Fatalf("admitted guard arguments %q", arguments)
		}
	}
	failure := errors.New("actual guard token was elevated")
	operations := maintenanceOperations{verify: func(bool) error { t.Fatal("restricted worker reached elevated maintenance admission"); return nil }, guard: func(ready, release windows.Handle) error {
		if ready != 4 || release != 8 {
			t.Fatalf("ready=%d,release=%d", ready, release)
		}
		return failure
	}}
	if err := runMaintenance(context.Background(), []string{"--hold-stopped", "--ready-handle=4", "--release-handle=8"}, operations); !errors.Is(err, failure) {
		t.Fatal(err)
	}
}

// TestWindowsCompleteInstallOrdering preserves integration and registration failures without creating a task for failed machine state.
func TestWindowsCompleteInstallOrdering(t *testing.T) {
	denied := errors.New("foreign machine integration")
	if err := completeMaintenanceSteps(func() error { return denied }, func() error { t.Fatal("task registered before integration admission"); return nil }); !errors.Is(err, denied) {
		t.Fatal(err)
	}
	taskFailure := errors.New("foreign daemon task")
	integrated := false
	if err := completeMaintenanceSteps(func() error { integrated = true; return nil }, func() error {
		if !integrated {
			t.Fatal("task preceded machine integration")
		}
		return taskFailure
	}); !errors.Is(err, taskFailure) {
		t.Fatal(err)
	}
	if err := completeMaintenanceSteps(func() error { return nil }, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
}
