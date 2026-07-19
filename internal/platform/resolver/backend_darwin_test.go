//go:build darwin

package resolver

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goforj/harbor/internal/host/networkpolicy"
	"golang.org/x/sys/unix"
)

// TestSecureDarwinResolverCreatedAccessRequiresVerifiedACLRemoval covers every privilege-sensitive boundary.
func TestSecureDarwinResolverCreatedAccessRequiresVerifiedACLRemoval(t *testing.T) {
	file, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open Darwin resolver ACL fixture: %v", err)
	}
	defer file.Close()

	tests := []struct {
		name             string
		observations     []bool
		inspectionErrors []error
		removalError     error
		wantCause        error
		wantMessage      string
		wantInspections  int
		wantRemovals     int
	}{
		{
			name:            "absent",
			observations:    []bool{false},
			wantInspections: 1,
		},
		{
			name:            "removed",
			observations:    []bool{true, false},
			wantInspections: 2,
			wantRemovals:    1,
		},
		{
			name:             "initial inspection failure",
			observations:     []bool{false},
			inspectionErrors: []error{unix.EPERM},
			wantCause:        unix.EPERM,
			wantMessage:      "inspect inherited",
			wantInspections:  1,
		},
		{
			name:            "removal failure",
			observations:    []bool{true},
			removalError:    unix.EPERM,
			wantCause:       unix.EPERM,
			wantMessage:     "remove inherited",
			wantInspections: 1,
			wantRemovals:    1,
		},
		{
			name:             "verification failure",
			observations:     []bool{true, false},
			inspectionErrors: []error{nil, unix.EIO},
			wantCause:        unix.EIO,
			wantMessage:      "reinspect",
			wantInspections:  2,
			wantRemovals:     1,
		},
		{
			name:            "ACL remains",
			observations:    []bool{true, true},
			wantMessage:     "retains an access control list",
			wantInspections: 2,
			wantRemovals:    1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			inspections := 0
			removals := 0
			present := func(*os.File) (bool, error) {
				index := inspections
				inspections++
				if index < len(test.inspectionErrors) && test.inspectionErrors[index] != nil {
					return false, test.inspectionErrors[index]
				}
				if index >= len(test.observations) {
					t.Fatalf("unexpected ACL inspection %d", inspections)
				}
				return test.observations[index], nil
			}
			remove := func(*os.File) error {
				removals++
				return test.removalError
			}

			err := secureDarwinResolverCreatedAccessWith(file, present, remove)
			if test.wantMessage == "" && err != nil {
				t.Fatalf("secureDarwinResolverCreatedAccessWith() error = %v", err)
			}
			if test.wantMessage != "" && (err == nil || !strings.Contains(err.Error(), test.wantMessage)) {
				t.Fatalf("secureDarwinResolverCreatedAccessWith() error = %v, want message %q", err, test.wantMessage)
			}
			if test.wantCause != nil && !errors.Is(err, test.wantCause) {
				t.Fatalf("secureDarwinResolverCreatedAccessWith() error = %v, want cause %v", err, test.wantCause)
			}
			if inspections != test.wantInspections {
				t.Fatalf("ACL inspections = %d, want %d", inspections, test.wantInspections)
			}
			if removals != test.wantRemovals {
				t.Fatalf("ACL removals = %d, want %d", removals, test.wantRemovals)
			}
		})
	}
}

// TestDarwinResolverAtomicCapturePreservesRacingIdentities exercises every former check-then-name-mutation window.
func TestDarwinResolverAtomicCapturePreservesRacingIdentities(t *testing.T) {
	tests := []struct {
		name       string
		sourceName string
		childName  string
	}{
		{name: "release", sourceName: fixedDarwinResolverName, childName: fixedDarwinResolverName},
		{name: "orphan recovery", sourceName: darwinResolverTestOrphanName("0"), childName: darwinResolverTestOrphanName("0")},
		{name: "deferred staging cleanup", sourceName: darwinResolverTestOrphanName("1"), childName: darwinResolverTestOrphanName("1")},
		{name: "rollback published capture", sourceName: fixedDarwinResolverName, childName: darwinResolverTestOrphanName("2")},
		{name: "rollback displaced capture", sourceName: darwinResolverTestOrphanName("3"), childName: fixedDarwinResolverName},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			parentPath := t.TempDir()
			parent, err := os.Open(parentPath)
			if err != nil {
				t.Fatal(err)
			}
			defer parent.Close()

			sourcePath := filepath.Join(parentPath, test.sourceName)
			backupPath := filepath.Join(parentPath, "admitted-backup")
			foreignPath := filepath.Join(parentPath, "foreign-candidate")
			if err := os.WriteFile(sourcePath, []byte("admitted"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(foreignPath, []byte("foreign"), 0o600); err != nil {
				t.Fatal(err)
			}
			expected, exists, err := statDarwinResolverEntry(parent, test.sourceName)
			if err != nil || !exists {
				t.Fatalf("stat admitted identity = (%t, %v)", exists, err)
			}

			injected := false
			fileUnlinks := 0
			syscalls := darwinResolverMutationSyscalls{
				rename: func(fromFD int, from string, toFD int, to string, flags uint32) error {
					if !injected && fromFD == int(parent.Fd()) && from == test.sourceName {
						injected = true
						if err := os.Rename(sourcePath, backupPath); err != nil {
							return fmt.Errorf("preserve admitted fixture: %w", err)
						}
						if err := os.Rename(foreignPath, sourcePath); err != nil {
							return fmt.Errorf("install racing fixture: %w", err)
						}
					}
					return unix.RenameatxNp(fromFD, from, toFD, to, flags)
				},
				unlink: func(directoryFD int, name string, flags int) error {
					if flags == 0 {
						fileUnlinks++
					}
					return unix.Unlinkat(directoryFD, name, flags)
				},
			}

			_, captured, err := captureDarwinResolverIdentity(
				parent,
				test.sourceName,
				test.childName,
				expected,
				false,
				syscalls,
			)
			if err == nil || captured || !strings.Contains(err.Error(), "different identity") {
				t.Fatalf("captureDarwinResolverIdentity() = (%t, %v), want rejected replacement", captured, err)
			}
			if !injected {
				t.Fatal("capture race seam did not inject the replacement")
			}
			if fileUnlinks != 0 {
				t.Fatalf("capture unlinked %d file identities", fileUnlinks)
			}
			if got := darwinResolverTestReadFile(t, sourcePath); got != "foreign" {
				t.Fatalf("racing identity = %q, want preserved foreign content", got)
			}
			if got := darwinResolverTestReadFile(t, backupPath); got != "admitted" {
				t.Fatalf("admitted identity = %q, want preserved admitted content", got)
			}
		})
	}
}

// TestDarwinResolverDeletionOccursOnlyInsidePrivateQuarantine proves shared resolver names are never passed to file unlink.
func TestDarwinResolverDeletionOccursOnlyInsidePrivateQuarantine(t *testing.T) {
	parentPath := t.TempDir()
	parent, err := os.Open(parentPath)
	if err != nil {
		t.Fatal(err)
	}
	defer parent.Close()

	if err := os.WriteFile(filepath.Join(parentPath, fixedDarwinResolverName), []byte("admitted"), 0o600); err != nil {
		t.Fatal(err)
	}
	expected, exists, err := statDarwinResolverEntry(parent, fixedDarwinResolverName)
	if err != nil || !exists {
		t.Fatalf("stat admitted identity = (%t, %v)", exists, err)
	}
	fileUnlinks := 0
	syscalls := darwinResolverMutationSyscalls{
		rename: unix.RenameatxNp,
		unlink: func(directoryFD int, name string, flags int) error {
			if flags == 0 {
				fileUnlinks++
				if directoryFD == int(parent.Fd()) {
					t.Fatalf("shared directory received file unlink for %q", name)
				}
			}
			return unix.Unlinkat(directoryFD, name, flags)
		},
	}
	if err := removeDarwinResolverIdentity(
		parent,
		fixedDarwinResolverName,
		fixedDarwinResolverName,
		expected,
		false,
		syscalls,
	); err != nil {
		t.Fatalf("removeDarwinResolverIdentity() error = %v", err)
	}
	if fileUnlinks != 1 {
		t.Fatalf("private file unlinks = %d, want 1", fileUnlinks)
	}
	if _, err := os.Stat(filepath.Join(parentPath, fixedDarwinResolverName)); !os.IsNotExist(err) {
		t.Fatalf("removed identity stat error = %v, want not-exist", err)
	}
}

// TestDarwinResolverRestoreNeverOverwritesARacingDestination pins fail-closed rollback publication.
func TestDarwinResolverRestoreNeverOverwritesARacingDestination(t *testing.T) {
	parentPath := t.TempDir()
	parent, err := os.Open(parentPath)
	if err != nil {
		t.Fatal(err)
	}
	defer parent.Close()

	sourcePath := filepath.Join(parentPath, fixedDarwinResolverName)
	if err := os.WriteFile(sourcePath, []byte("admitted"), 0o600); err != nil {
		t.Fatal(err)
	}
	expected, exists, err := statDarwinResolverEntry(parent, fixedDarwinResolverName)
	if err != nil || !exists {
		t.Fatalf("stat admitted identity = (%t, %v)", exists, err)
	}
	quarantine, captured, err := captureDarwinResolverIdentity(
		parent,
		fixedDarwinResolverName,
		fixedDarwinResolverName,
		expected,
		false,
		darwinResolverNativeMutationSyscalls,
	)
	if err != nil || !captured {
		t.Fatalf("captureDarwinResolverIdentity() = (%t, %v)", captured, err)
	}
	quarantinePath := filepath.Join(parentPath, quarantine.Name, fixedDarwinResolverName)
	injected := false
	syscalls := darwinResolverMutationSyscalls{
		rename: func(fromFD int, from string, toFD int, to string, flags uint32) error {
			if !injected && fromFD == int(quarantine.Directory.Fd()) && toFD == int(parent.Fd()) {
				injected = true
				if err := os.WriteFile(sourcePath, []byte("foreign"), 0o600); err != nil {
					return fmt.Errorf("install racing destination: %w", err)
				}
			}
			return unix.RenameatxNp(fromFD, from, toFD, to, flags)
		},
		unlink: unix.Unlinkat,
	}
	err = restoreDarwinResolverPrivateCapture(
		parent,
		quarantine,
		fixedDarwinResolverName,
		fixedDarwinResolverName,
		expected,
		syscalls,
	)
	if err == nil || !injected {
		t.Fatalf("restoreDarwinResolverPrivateCapture() error = %v, injected = %t", err, injected)
	}
	if got := darwinResolverTestReadFile(t, sourcePath); got != "foreign" {
		t.Fatalf("racing destination = %q, want preserved foreign content", got)
	}
	if got := darwinResolverTestReadFile(t, quarantinePath); got != "admitted" {
		t.Fatalf("retained quarantine = %q, want admitted content", got)
	}
}

// TestDarwinResolverPublicationAndRollbackPreserveContinuousDestinationBinding covers both native publication shapes.
func TestDarwinResolverPublicationAndRollbackPreserveContinuousDestinationBinding(t *testing.T) {
	for _, test := range []struct {
		name     string
		existing bool
	}{
		{name: "absent destination"},
		{name: "atomic replacement", existing: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			parentPath := t.TempDir()
			parent, err := os.Open(parentPath)
			if err != nil {
				t.Fatal(err)
			}
			defer parent.Close()

			stagingName := darwinResolverTestOrphanName("4")
			stagingPath := filepath.Join(parentPath, stagingName)
			fixedPath := filepath.Join(parentPath, fixedDarwinResolverName)
			if err := os.WriteFile(stagingPath, []byte("published"), 0o600); err != nil {
				t.Fatal(err)
			}
			stagedStatus, exists, err := statDarwinResolverEntry(parent, stagingName)
			if err != nil || !exists {
				t.Fatalf("stat staged identity = (%t, %v)", exists, err)
			}
			guard := darwinResolverGuard{Name: fixedDarwinResolverName}
			if test.existing {
				if err := os.WriteFile(fixedPath, []byte("original"), 0o600); err != nil {
					t.Fatal(err)
				}
				original, exists, err := statDarwinResolverEntry(parent, fixedDarwinResolverName)
				if err != nil || !exists {
					t.Fatalf("stat original identity = (%t, %v)", exists, err)
				}
				guard.Exists = true
				guard.Device = uint64(original.Dev)
				guard.Inode = uint64(original.Ino)
				guard.Generation = uint32(original.Gen)
			}

			swapObserved := false
			syscalls := darwinResolverMutationSyscalls{
				rename: func(fromFD int, from string, toFD int, to string, flags uint32) error {
					if flags&unix.RENAME_SWAP != 0 {
						swapObserved = true
						if flags != uint32(unix.RENAME_SWAP|unix.RENAME_NOFOLLOW_ANY) {
							t.Fatalf("replacement flags = %#x", flags)
						}
						if _, err := os.Stat(stagingPath); err != nil {
							t.Fatalf("staging before swap: %v", err)
						}
						if _, err := os.Stat(fixedPath); err != nil {
							t.Fatalf("destination before swap: %v", err)
						}
					} else if flags != uint32(unix.RENAME_EXCL|unix.RENAME_NOFOLLOW_ANY) {
						t.Fatalf("exclusive publication flags = %#x", flags)
					}
					err := unix.RenameatxNp(fromFD, from, toFD, to, flags)
					if err == nil && flags&unix.RENAME_SWAP != 0 {
						if _, statErr := os.Stat(stagingPath); statErr != nil {
							t.Fatalf("staging after swap: %v", statErr)
						}
						if _, statErr := os.Stat(fixedPath); statErr != nil {
							t.Fatalf("destination after swap: %v", statErr)
						}
					}
					return err
				},
				unlink: unix.Unlinkat,
			}
			publication, err := publishDarwinResolverStaging(parent, stagingName, stagedStatus, guard, syscalls)
			if err != nil {
				t.Fatalf("publishDarwinResolverStaging() error = %v", err)
			}
			if !publication.Published || publication.Replaced != test.existing {
				t.Fatalf("publishDarwinResolverStaging() = %#v", publication)
			}
			if swapObserved != test.existing {
				t.Fatalf("atomic swap observed = %t, want %t", swapObserved, test.existing)
			}
			if got := darwinResolverTestReadFile(t, fixedPath); got != "published" {
				t.Fatalf("published identity = %q", got)
			}
			if test.existing {
				if got := darwinResolverTestReadFile(t, stagingPath); got != "original" {
					t.Fatalf("displaced identity = %q", got)
				}
			} else if _, err := os.Stat(stagingPath); !os.IsNotExist(err) {
				t.Fatalf("published staging stat error = %v, want not-exist", err)
			}

			if err := rollbackDarwinResolverPublication(
				parent,
				stagingName,
				stagedStatus,
				publication,
				syscalls,
			); err != nil {
				t.Fatalf("rollbackDarwinResolverPublication() error = %v", err)
			}
			if got := darwinResolverTestReadFile(t, stagingPath); got != "published" {
				t.Fatalf("rolled-back staging identity = %q", got)
			}
			if test.existing {
				if got := darwinResolverTestReadFile(t, fixedPath); got != "original" {
					t.Fatalf("rolled-back destination identity = %q", got)
				}
			} else if _, err := os.Stat(fixedPath); !os.IsNotExist(err) {
				t.Fatalf("rolled-back destination stat error = %v, want not-exist", err)
			}
		})
	}
}

// TestDarwinResolverPublicationRejectsUnsupportedSwapWithoutFallback proves replacement never opens a destination gap.
func TestDarwinResolverPublicationRejectsUnsupportedSwapWithoutFallback(t *testing.T) {
	parentPath := t.TempDir()
	parent, err := os.Open(parentPath)
	if err != nil {
		t.Fatal(err)
	}
	defer parent.Close()
	stagingName := darwinResolverTestOrphanName("6")
	stagingPath := filepath.Join(parentPath, stagingName)
	fixedPath := filepath.Join(parentPath, fixedDarwinResolverName)
	if err := os.WriteFile(stagingPath, []byte("published"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fixedPath, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	staged, _, err := statDarwinResolverEntry(parent, stagingName)
	if err != nil {
		t.Fatal(err)
	}
	original, _, err := statDarwinResolverEntry(parent, fixedDarwinResolverName)
	if err != nil {
		t.Fatal(err)
	}
	guard := darwinResolverGuard{
		Exists:     true,
		Name:       fixedDarwinResolverName,
		Device:     uint64(original.Dev),
		Inode:      uint64(original.Ino),
		Generation: uint32(original.Gen),
	}
	renames := 0
	publication, err := publishDarwinResolverStaging(
		parent,
		stagingName,
		staged,
		guard,
		darwinResolverMutationSyscalls{
			rename: func(_ int, _ string, _ int, _ string, flags uint32) error {
				renames++
				if flags != uint32(unix.RENAME_SWAP|unix.RENAME_NOFOLLOW_ANY) {
					t.Fatalf("replacement flags = %#x", flags)
				}
				return unix.ENOTSUP
			},
		},
	)
	if !errors.Is(err, unix.ENOTSUP) || publication.Published {
		t.Fatalf("publishDarwinResolverStaging() = (%#v, %v), want unsupported unchanged publication", publication, err)
	}
	if renames != 1 {
		t.Fatalf("publication rename calls = %d, want 1", renames)
	}
	if got := darwinResolverTestReadFile(t, fixedPath); got != "original" {
		t.Fatalf("destination after unsupported swap = %q", got)
	}
	if got := darwinResolverTestReadFile(t, stagingPath); got != "published" {
		t.Fatalf("staging after unsupported swap = %q", got)
	}
}

// TestDarwinResolverRollbackRejectsChangedIdentities preserves every object when privileged state races verification.
func TestDarwinResolverRollbackRejectsChangedIdentities(t *testing.T) {
	parentPath := t.TempDir()
	parent, err := os.Open(parentPath)
	if err != nil {
		t.Fatal(err)
	}
	defer parent.Close()
	stagingName := darwinResolverTestOrphanName("7")
	stagingPath := filepath.Join(parentPath, stagingName)
	fixedPath := filepath.Join(parentPath, fixedDarwinResolverName)
	backupPath := filepath.Join(parentPath, "displaced-backup")
	if err := os.WriteFile(stagingPath, []byte("published"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fixedPath, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	staged, _, err := statDarwinResolverEntry(parent, stagingName)
	if err != nil {
		t.Fatal(err)
	}
	original, _, err := statDarwinResolverEntry(parent, fixedDarwinResolverName)
	if err != nil {
		t.Fatal(err)
	}
	publication, err := publishDarwinResolverStaging(
		parent,
		stagingName,
		staged,
		darwinResolverGuard{
			Exists:     true,
			Name:       fixedDarwinResolverName,
			Device:     uint64(original.Dev),
			Inode:      uint64(original.Ino),
			Generation: uint32(original.Gen),
		},
		darwinResolverNativeMutationSyscalls,
	)
	if err != nil {
		t.Fatalf("publishDarwinResolverStaging() error = %v", err)
	}
	if err := os.Rename(stagingPath, backupPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stagingPath, []byte("foreign"), 0o600); err != nil {
		t.Fatal(err)
	}
	renames := 0
	err = rollbackDarwinResolverPublication(
		parent,
		stagingName,
		staged,
		publication,
		darwinResolverMutationSyscalls{
			rename: func(fromFD int, from string, toFD int, to string, flags uint32) error {
				renames++
				return unix.RenameatxNp(fromFD, from, toFD, to, flags)
			},
		},
	)
	if err == nil || !strings.Contains(err.Error(), "displaced Darwin resolver identity changed") {
		t.Fatalf("rollbackDarwinResolverPublication() error = %v", err)
	}
	if renames != 0 {
		t.Fatalf("rollback rename calls = %d, want 0", renames)
	}
	if got := darwinResolverTestReadFile(t, fixedPath); got != "published" {
		t.Fatalf("published identity after rejected rollback = %q", got)
	}
	if got := darwinResolverTestReadFile(t, stagingPath); got != "foreign" {
		t.Fatalf("racing staging identity = %q", got)
	}
	if got := darwinResolverTestReadFile(t, backupPath); got != "original" {
		t.Fatalf("preserved displaced identity = %q", got)
	}
}

// TestPrivilegedDarwinResolverReplacementCrashRecovery proves every stable swap and cleanup boundary converges safely.
func TestPrivilegedDarwinResolverReplacementCrashRecovery(t *testing.T) {
	if os.Getenv("HARBOR_PRIVILEGED_RESOLVER_TEST") != "1" {
		t.Skip("set HARBOR_PRIVILEGED_RESOLVER_TEST=1 and run as root to exercise native recovery")
	}
	if os.Geteuid() != 0 {
		t.Fatal("privileged Darwin resolver recovery test requires root")
	}
	request := resolverTestRequest(t, networkpolicy.DarwinResolverFile)
	replacementRequest, err := NewRequest("installation-replacement", request.Policy())
	if err != nil {
		t.Fatalf("NewRequest() replacement fixture error = %v", err)
	}
	oldContent := marshalDarwinResolverValidated(request)
	newContent := marshalDarwinResolverValidated(replacementRequest)

	for _, test := range []struct {
		name  string
		setup func(*testing.T, *os.File, string, string)
	}{
		{
			name: "staging durable before publication",
			setup: func(t *testing.T, _ *os.File, fixedPath string, stagingPath string) {
				darwinResolverTestWriteCanonicalFile(t, fixedPath, oldContent)
				darwinResolverTestWriteCanonicalFile(t, stagingPath, newContent)
			},
		},
		{
			name: "swap durable before displaced cleanup",
			setup: func(t *testing.T, _ *os.File, fixedPath string, stagingPath string) {
				darwinResolverTestWriteCanonicalFile(t, fixedPath, newContent)
				darwinResolverTestWriteCanonicalFile(t, stagingPath, oldContent)
			},
		},
		{
			name: "displaced orphan captured for deletion",
			setup: func(t *testing.T, parent *os.File, fixedPath string, stagingPath string) {
				darwinResolverTestWriteCanonicalFile(t, fixedPath, newContent)
				darwinResolverTestWriteCanonicalFile(t, stagingPath, oldContent)
				staged, exists, err := statDarwinResolverEntry(parent, filepath.Base(stagingPath))
				if err != nil || !exists {
					t.Fatalf("stat displaced recovery fixture = (%t, %v)", exists, err)
				}
				quarantine, captured, err := captureDarwinResolverIdentity(
					parent,
					filepath.Base(stagingPath),
					filepath.Base(stagingPath),
					staged,
					false,
					darwinResolverNativeMutationSyscalls,
				)
				if err != nil || !captured {
					t.Fatalf("capture displaced recovery fixture = (%t, %v)", captured, err)
				}
				if err := quarantine.Directory.Close(); err != nil {
					t.Fatalf("close simulated crash quarantine: %v", err)
				}
			},
		},
		{
			name: "displaced orphan deleted before quarantine removal",
			setup: func(t *testing.T, parent *os.File, fixedPath string, _ string) {
				darwinResolverTestWriteCanonicalFile(t, fixedPath, newContent)
				quarantine, err := createDarwinResolverPrivateQuarantine(parent)
				if err != nil {
					t.Fatalf("create empty recovery quarantine: %v", err)
				}
				if err := quarantine.Directory.Close(); err != nil {
					t.Fatalf("close empty recovery quarantine: %v", err)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			parentPath := t.TempDir()
			parent, err := os.Open(parentPath)
			if err != nil {
				t.Fatal(err)
			}
			defer parent.Close()
			stagingName := darwinResolverTestOrphanName("5")
			fixedPath := filepath.Join(parentPath, fixedDarwinResolverName)
			stagingPath := filepath.Join(parentPath, stagingName)
			test.setup(t, parent, fixedPath, stagingPath)

			if err := recoverDarwinResolverOrphans(t.Context(), parent); err != nil {
				t.Fatalf("recoverDarwinResolverOrphans() error = %v", err)
			}
			expectedContent := newContent
			if test.name == "staging durable before publication" {
				expectedContent = oldContent
			}
			if got := darwinResolverTestReadFile(t, fixedPath); got != string(expectedContent) {
				t.Fatalf("recovered destination content = %q", got)
			}
			darwinResolverTestRequireNoTransactions(t, parentPath)
		})
	}
}

// darwinResolverTestWriteCanonicalFile creates one root-owned resolver recovery fixture.
func darwinResolverTestWriteCanonicalFile(t *testing.T, path string, content []byte) {
	t.Helper()
	if err := os.WriteFile(path, content, os.FileMode(darwinResolverFileMode)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(path, 0, 0); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, os.FileMode(darwinResolverFileMode)); err != nil {
		t.Fatal(err)
	}
}

// darwinResolverTestRequireNoTransactions proves recovery removed every Harbor-private staging and quarantine name.
func darwinResolverTestRequireNoTransactions(t *testing.T, parentPath string) {
	t.Helper()
	entries, err := os.ReadDir(parentPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if isDarwinResolverTransactionName(entry.Name()) {
			t.Fatalf("recovery retained transaction name %q", entry.Name())
		}
	}
}

// darwinResolverTestOrphanName returns one deterministic valid staging name for native mutation tests.
func darwinResolverTestOrphanName(character string) string {
	return darwinResolverOrphanPrefix + strings.Repeat(character, darwinResolverOrphanHexBytes*2)
}

// darwinResolverTestReadFile reads one fixture whose continued existence is part of the security assertion.
func darwinResolverTestReadFile(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}
