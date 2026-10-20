//go:build windows

package windowsinstallation

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// TestNativeIntegrationReceipt rolls back an incomplete document through its retained native handle.
func TestNativeIntegrationReceipt(t *testing.T) {
	root := integrationTestAnchor(t)
	path := filepath.Join(root, integrationReceiptName)
	if committed, err := commitIntegrationReceipt(path, bytes.Repeat([]byte("x"), MetadataLimit+1)); err == nil || committed {
		t.Fatal("accepted oversized receipt")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("failed receipt poisoned retry: %v", err)
	}
	if committed, err := commitIntegrationReceipt(path, []byte("{}")); err != nil || !committed {
		t.Fatalf("valid retry = %v, %v", committed, err)
	}
	if committed, err := commitIntegrationReceipt(path, []byte("changed")); err == nil || committed {
		t.Fatal("replaced committed receipt")
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "{}" {
		t.Fatalf("committed receipt changed: %q, %v", data, err)
	}
}

// TestNativeMachineIntegrationTransaction exercises real fixed OS projections on this explicitly disposable hosted worker.
func TestNativeMachineIntegrationTransaction(t *testing.T) {
	if os.Getenv("HARBOR_ADMISSION_FIXTURE_ROOT") == "" {
		t.Skip("requires the disposable native installation workflow")
	}
	anchor := integrationTestAnchor(t)
	publisher, payload := signedInstallPayload(t)
	installation, err := installAt(filepath.Join(anchor, "Product", "Harbor"), publisher, payload)
	if err != nil {
		t.Fatal(err)
	}
	defer installation.Close()
	if err := installation.InstallMachineIntegration(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := installation.InstallMachineIntegration(context.Background()); err != nil {
		t.Fatalf("exact repair refused: %v", err)
	}
	if err := installation.ValidateMachineIntegration(context.Background()); err != nil {
		t.Fatal(err)
	}
	lost := errors.New("runtime guard exited")
	if err := installation.RemoveMachineIntegration(context.Background(), func() error { return lost }); !errors.Is(err, lost) {
		t.Fatalf("guard loss = %v", err)
	}
	if err := installation.ValidateMachineIntegration(context.Background()); err != nil {
		t.Fatalf("guard rejection changed machine projections: %v", err)
	}
	programs, err := windows.KnownFolderPath(windows.FOLDERID_CommonPrograms, windows.KF_FLAG_DEFAULT)
	if err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(programs, "GoForj", "Foreign.txt")
	if err := os.WriteFile(sentinel, []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(sentinel); os.Remove(filepath.Dir(sentinel)) })
	if err := installation.RemoveMachineIntegration(context.Background(), context.Background().Err); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(sentinel); err != nil || string(data) != "preserve" {
		t.Fatalf("shared Start Menu neighbor changed: %q, %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(programs, "GoForj", "Harbor.lnk")); !os.IsNotExist(err) {
		t.Fatalf("shortcut survived removal: %v", err)
	}
	key, err := registry.OpenKey(registry.LOCAL_MACHINE, integrationUninstallKey, registry.READ|registry.WOW64_64KEY)
	if err == nil {
		key.Close()
		t.Fatal("uninstall registration survived removal")
	}
	if err != registry.ErrNotExist {
		t.Fatalf("uninstall absence observation: %v", err)
	}
}
