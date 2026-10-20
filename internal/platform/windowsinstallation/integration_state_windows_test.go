//go:build windows

package windowsinstallation

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unsafe"

	"github.com/goforj/harbor/internal/host/ownershipreleaseproof"
	"golang.org/x/sys/windows"
)

// writeIntegrationTestState authors only disposable native fixture files with the same at-birth helper policies.
func writeIntegrationTestState(t *testing.T, path, policy string, content []byte) {
	t.Helper()
	descriptor, err := windows.SecurityDescriptorFromString(policy)
	if err != nil {
		t.Fatal(err)
	}
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	attributes := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: descriptor}
	handle, err := windows.CreateFile(pointer, windows.GENERIC_WRITE, windows.FILE_SHARE_READ, &attributes, windows.CREATE_NEW, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		t.Fatal(err)
	}
	file := os.NewFile(uintptr(handle), path)
	if _, err := file.Write(content); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

// integrationTestReplay mirrors a real durable tombstone, including the production domain and canonical filename.
func integrationTestReplay(t *testing.T) (string, []byte) {
	t.Helper()
	record := integrationReplayRecord{Version: 1, InstallationID: "harbor-test-installation", OwnershipGeneration: 1, Nonce: strings.Repeat("n", 32), ExpiresAt: "2026-10-20T01:00:00Z"}
	content, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.New()
	hash.Write([]byte("goforj.harbor/helper-replay:v1\x00"))
	fmt.Fprintf(hash, "%d:%s\x00%d:%s\x00", len(record.InstallationID), record.InstallationID, record.OwnershipGeneration, record.Nonce)
	return hex.EncodeToString(hash.Sum(nil)) + ".json", append(content, '\n')
}

// TestNativeIntegrationState admits released history and preserves every unsupported or still-active state.
func TestNativeIntegrationState(t *testing.T) {
	for _, name := range []string{"empty", "released", "pending", "foreign", "active ownership", "unreleased history", "bad replay", "wrong account proof", "lock content"} {
		t.Run(name, func(t *testing.T) {
			anchor := integrationTestAnchor(t)
			user, err := windows.GetCurrentProcessToken().GetTokenUser()
			if err != nil {
				t.Fatal(err)
			}
			sid := user.User.Sid.String()
			spool, err := preparePrivilegedSpool(anchor, sid)
			if err != nil {
				t.Fatal(err)
			}
			defer spool.Close()
			root := spool.Directories[2].Object.Path
			machine := "O:BAD:P(A;;FA;;;BA)(A;;FA;;;SY)"
			if name != "empty" {
				lockData := []byte{}
				if name == "lock content" {
					lockData = []byte("foreign")
				}
				for _, relative := range []string{"ownership-release-proof.lock", `state\ownership.json.lock`, `state\host-projection.json.lock`} {
					writeIntegrationTestState(t, filepath.Join(root, relative), machine, lockData)
				}
			}
			switch name {
			case "pending":
				writeIntegrationTestState(t, filepath.Join(root, "tickets", "pending", strings.Repeat("a", 64)), machine, []byte("pending"))
			case "foreign":
				writeIntegrationTestState(t, filepath.Join(root, "foreign.txt"), machine, []byte("preserve"))
			case "active ownership":
				writeIntegrationTestState(t, filepath.Join(root, "state", "ownership.json"), machine, []byte("active"))
			case "released", "unreleased history", "bad replay", "wrong account proof":
				replayName, content := integrationTestReplay(t)
				if name == "bad replay" {
					content = []byte("{}\n")
				}
				writeIntegrationTestState(t, filepath.Join(root, "state", "replay", replayName), machine, content)
				writeIntegrationTestState(t, filepath.Join(root, "tickets", "claims", strings.Repeat("b", 64)), machine, []byte("invalid but consumed"))
				if name != "unreleased history" {
					proof := ownershipreleaseproof.Proof{TicketReferenceHash: strings.Repeat("a", 64), NonceHash: strings.Repeat("b", 64), ReleaseOperationID: "release", OperationRevision: 1, CheckpointRevision: 2, RequesterIdentity: sid, TargetOwnershipFingerprint: strings.Repeat("c", 64), State: ownershipreleaseproof.StateReleased, VerifiedAt: time.Date(2026, 10, 20, 1, 0, 0, 0, time.UTC)}
					if name == "wrong account proof" {
						proof.RequesterIdentity = "S-1-5-21-1-2-3-1001"
					}
					data, err := json.Marshal(proof)
					if err != nil {
						t.Fatal(err)
					}
					writeIntegrationTestState(t, filepath.Join(root, "ownership-release-proof.json"), machine+"(A;;0x120089;;;"+sid+")", data)
				}
			}
			state, err := admitIntegrationState(spool, sid)
			valid := name == "empty" || name == "released"
			if !valid {
				if err == nil {
					state.Close()
					t.Fatal("accepted unsafe helper state")
				}
				if _, err := os.Stat(root); err != nil {
					t.Fatalf("refusal changed helper root: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := state.retire(); err != nil {
				state.Close()
				t.Fatal(err)
			}
			if err := state.Close(); err != nil {
				t.Fatal(err)
			}
			if err := spool.retire(); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(root); !os.IsNotExist(err) {
				t.Fatalf("owned spool survived removal: %v", err)
			}
		})
	}
}

// TestIntegrationReplayRejectsDrift checks canonical bytes and claim-bound filenames without touching machine state.
func TestIntegrationReplayRejectsDrift(t *testing.T) {
	name, content := integrationTestReplay(t)
	if err := admitIntegrationReplay(name, content); err != nil {
		t.Fatal(err)
	}
	for _, data := range [][]byte{content[:len(content)-1], append(append([]byte(nil), content...), '\n'), []byte("{}\n"), []byte(strings.Replace(string(content), `"version":1`, `"version":2`, 1))} {
		if err := admitIntegrationReplay(name, data); err == nil {
			t.Fatal("accepted changed replay bytes")
		}
	}
	if err := admitIntegrationReplay(strings.Repeat("0", 64)+".json", content); err == nil {
		t.Fatal("accepted filename for a different claim")
	}
}
