//go:build windows

package windowsinstallation

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// deletionFixture snapshots only newly authored protected fixture objects, without installing a product or touching user data.
func deletionFixture(t *testing.T, name string) (string, []deletionObject) {
	t.Helper()
	base := os.Getenv("HARBOR_ADMISSION_FIXTURE_ROOT")
	if base == "" {
		t.Skip("disposable native fixture was not configured")
	}
	root := filepath.Join(base, "remove-"+name)
	if err := createMachineDirectoryNew(root); err != nil {
		t.Fatal(err)
	}
	if err := writeMachineBytes(filepath.Join(root, "owned.txt"), []byte("owned bytes")); err != nil {
		t.Fatal(err)
	}
	installation := &Installation{root: root}
	if _, err := installation.retain("owned.txt", false); err != nil {
		t.Fatal(err)
	}
	if err := installation.retainDirectoryChain(root); err != nil {
		t.Fatal(err)
	}
	objects, err := snapshotDeletion(installation)
	if err != nil {
		t.Fatal(err)
	}
	if err := installation.Close(); err != nil {
		t.Fatal(err)
	}
	return root, objects
}

// TestNativePayloadRemovalPreservesForeignState validates all known objects before effects and never recursively deletes neighbors.
func TestNativePayloadRemovalPreservesForeignState(t *testing.T) {
	for _, name := range []string{"exact", "modified", "replaced", "hardlink", "reparse", "acl", "directory", "guard"} {
		t.Run(name, func(t *testing.T) {
			root, objects := deletionFixture(t, name)
			file := filepath.Join(root, "owned.txt")
			if err := os.WriteFile(filepath.Join(root, "foreign.txt"), []byte("preserve"), 0600); err != nil {
				t.Fatal(err)
			}
			switch name {
			case "modified":
				if err := os.WriteFile(file, []byte("foreign bytes"), 0600); err != nil {
					t.Fatal(err)
				}
			case "replaced":
				if err := os.Rename(file, file+".original"); err != nil {
					t.Fatal(err)
				}
				if err := writeMachineBytes(file, []byte("owned bytes")); err != nil {
					t.Fatal(err)
				}
			case "hardlink":
				if err := os.Link(file, file+".alias"); err != nil {
					t.Fatal(err)
				}
			case "reparse":
				if err := os.Rename(file, file+".original"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(file+".original", file); err != nil {
					t.Fatal(err)
				}
			case "acl":
				setFixtureSecurity(t, file, "O:BAD:P(A;;FA;;;BA)(A;;FA;;;SY)(A;;GW;;;BU)")
			case "directory":
				if err := os.Rename(root, root+".original"); err != nil {
					t.Fatal(err)
				}
				if err := createMachineDirectoryNew(root); err != nil {
					t.Fatal(err)
				}
				if err := writeMachineBytes(file, []byte("owned bytes")); err != nil {
					t.Fatal(err)
				}
				if err := writeMachineBytes(filepath.Join(root, "foreign.txt"), []byte("preserve")); err != nil {
					t.Fatal(err)
				}
			}
			check := func() error { return nil }
			if name == "guard" {
				check = func() error { return errors.New("guard exited") }
			}
			err := deletePayload(context.Background(), objects, check)
			if name == "exact" {
				if err != nil {
					t.Fatal(err)
				}
				if _, err := os.Stat(file); !os.IsNotExist(err) {
					t.Fatal("owned file remains")
				}
			} else {
				if err == nil {
					t.Fatal("drifted payload or lost guard accepted")
				}
				if _, err := os.Lstat(file); err != nil {
					t.Fatalf("foreign/drifted object removed: %v", err)
				}
			}
			if data, err := os.ReadFile(filepath.Join(root, "foreign.txt")); err != nil || string(data) != "preserve" {
				t.Fatalf("foreign neighbor changed: %q %v", data, err)
			}
		})
	}
}

// TestNativePayloadRemovalJoinsBootstrapUnloading rechecks exact identities after a transient owned read pin settles.
func TestNativePayloadRemovalJoinsBootstrapUnloading(t *testing.T) {
	root, objects := deletionFixture(t, "unloading")
	file, err := openRetained(filepath.Join(root, "owned.txt"), false)
	if err != nil {
		t.Fatal(err)
	}
	closed := make(chan error, 1)
	go func() { time.Sleep(100 * time.Millisecond); closed <- file.Close() }()
	files, err := awaitDeletion(context.Background(), objects)
	if err != nil {
		t.Fatal(err)
	}
	for _, retained := range files {
		if err := retained.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := awaitDeletion(ctx, objects); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

// TestNativePayloadRemovalPrevalidatesEveryObject refuses a later directory replacement before deleting the earlier admitted file.
func TestNativePayloadRemovalPrevalidatesEveryObject(t *testing.T) {
	root, objects := deletionFixture(t, "all-before-effects")
	for index := range objects {
		if objects[index].directory && objects[index].path == root {
			objects[index].identity = fileIdentity{}
		}
	}
	if err := deletePayload(context.Background(), objects, func() error { return nil }); err == nil {
		t.Fatal("replaced directory admitted")
	}
	if _, err := os.Stat(filepath.Join(root, "owned.txt")); err != nil {
		t.Fatal("earlier file deleted before full admission")
	}
}
