//go:build windows

package windowsinstallation

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

// TestNativeIntegrationRetirementStopsOnGuardLoss proves each loop stops before its next native deletion.
func TestNativeIntegrationRetirementStopsOnGuardLoss(t *testing.T) {
	lost := errors.New("retained worker exited")
	t.Run("helper files", func(t *testing.T) {
		root := integrationTestAnchor(t)
		policy := "O:BAD:P(A;;FA;;;BA)(A;;FA;;;SY)"
		state := &integrationState{}
		defer state.Close()
		paths := []string{filepath.Join(root, "first.bin"), filepath.Join(root, "second.bin")}
		for _, path := range paths {
			writeIntegrationTestState(t, path, policy, []byte("preserve until admitted"))
			file, err := openIntegrationStateFile(path, policy, windows.FILE_SHARE_READ)
			if err != nil {
				t.Fatal(err)
			}
			state.Files = append(state.Files, file)
		}
		calls := 0
		err := state.retireChecked(func() error {
			calls++
			if calls == 2 {
				return lost
			}
			return nil
		})
		if !errors.Is(err, lost) || calls != 2 {
			t.Fatalf("guard observation = %v (%d)", err, calls)
		}
		if err := state.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(paths[0]); !os.IsNotExist(err) {
			t.Fatalf("first admitted deletion did not settle: %v", err)
		}
		if data, err := os.ReadFile(paths[1]); err != nil || string(data) != "preserve until admitted" {
			t.Fatalf("next unadmitted file changed: %q, %v", data, err)
		}
	})
	t.Run("spool directories", func(t *testing.T) {
		root := integrationTestAnchor(t)
		user, err := windows.GetCurrentProcessToken().GetTokenUser()
		if err != nil {
			t.Fatal(err)
		}
		spool, err := preparePrivilegedSpool(root, user.User.Sid.String())
		if err != nil {
			t.Fatal(err)
		}
		defer spool.Close()
		calls := 0
		err = spool.retireChecked(func() error {
			calls++
			if calls == 2 {
				return lost
			}
			return nil
		})
		if !errors.Is(err, lost) || calls != 2 {
			t.Fatalf("guard observation = %v (%d)", err, calls)
		}
		if err := spool.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(spool.Directories[7].Object.Path); !os.IsNotExist(err) {
			t.Fatalf("first admitted directory survived: %v", err)
		}
		if _, err := os.Stat(spool.Directories[6].Object.Path); err != nil {
			t.Fatalf("next unadmitted directory changed: %v", err)
		}
	})
	t.Run("shortcut directory", func(t *testing.T) {
		root := integrationTestAnchor(t)
		spec := integrationShortcutSpec{Programs: root, Target: filepath.Join(root, "desktop-launcher.exe"), WorkingDirectory: root, Icon: filepath.Join(root, "desktop.exe")}
		shortcut, err := integrationCreateShortcut(spec)
		if err != nil {
			t.Fatal(err)
		}
		defer shortcut.Close()
		calls := 0
		err = shortcut.retireChecked(func() error {
			calls++
			if calls == 2 {
				return lost
			}
			return nil
		})
		if !errors.Is(err, lost) || calls != 2 {
			t.Fatalf("guard observation = %v (%d)", err, calls)
		}
		if err := shortcut.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(shortcut.Receipt.File.Path); !os.IsNotExist(err) {
			t.Fatalf("first admitted link survived: %v", err)
		}
		if _, err := os.Stat(shortcut.Receipt.Directory.Path); err != nil {
			t.Fatalf("next unadmitted directory changed: %v", err)
		}
	})
}
