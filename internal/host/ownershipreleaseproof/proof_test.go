//go:build darwin || linux

package ownershipreleaseproof

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// TestCompletePersistsTerminalProof proves one lock-held transaction preserves root-authored terminal evidence.
func TestCompletePersistsTerminalProof(t *testing.T) {
	writer, observer := fixture(t)
	mutations := 0
	proof, err := writer.Complete(
		context.Background(),
		request("a", "b"),
		Transaction{
			CompareAndSwap: func(context.Context) error {
				mutations++
				return nil
			},
			ObserveOwnership: func(context.Context) (bool, error) {
				return false, nil
			},
		},
		testTime(),
	)
	if err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	if proof.State != StateReleased || mutations != 1 {
		t.Fatalf("Complete() = %#v, mutations %d", proof, mutations)
	}
	confirmed, err := observer.ConfirmReleased(
		context.Background(),
		request("x", "y").Authority(),
	)
	if err != nil || confirmed.TicketReferenceHash != proof.TicketReferenceHash {
		t.Fatalf("ConfirmReleased() = (%#v, %v)", confirmed, err)
	}
}

// TestCompleteRecoversPendingAbsentWithoutMutation proves a crash after release but before proof promotion cannot repeat mutation.
func TestCompleteRecoversPendingAbsentWithoutMutation(t *testing.T) {
	writer, _ := fixture(t)
	pending := requestProof(request("a", "b"), testTime())
	if err := writeProof(writer.path, writer.owner, pending); err != nil {
		t.Fatalf("write pending: %v", err)
	}
	called := false
	proof, err := writer.Complete(
		context.Background(),
		request("new", "nonce"),
		Transaction{
			CompareAndSwap: func(context.Context) error {
				called = true
				return nil
			},
			ObserveOwnership: func(context.Context) (bool, error) {
				return false, nil
			},
		},
		testTime().Add(time.Second),
	)
	if err != nil || called || proof.State != StateReleased {
		t.Fatalf("Complete(recover absent) = (%#v, %v), callback %t", proof, err, called)
	}
}

// TestCompleteRejectsPendingProofFromDifferentAuthorityWhenOwnershipIsAbsent preserves the authority needed to finish an already-applied release.
func TestCompleteRejectsPendingProofFromDifferentAuthorityWhenOwnershipIsAbsent(t *testing.T) {
	writer, _ := fixture(t)
	pending := requestProof(request("first", "first-nonce"), testTime())
	if err := writeProof(writer.path, writer.owner, pending); err != nil {
		t.Fatalf("write pending: %v", err)
	}
	different := request("second", "second-nonce")
	different.ReleaseOperationID = "operation-second"
	different.OperationRevision = 6
	different.CheckpointRevision = 7
	different.TargetOwnershipFingerprint = hashValue("second-target")
	mutated := false
	_, err := writer.Complete(
		context.Background(),
		different,
		Transaction{
			CompareAndSwap: func(context.Context) error {
				mutated = true
				return nil
			},
			ObserveOwnership: func(context.Context) (bool, error) {
				return false, nil
			},
		},
		testTime().Add(time.Second),
	)
	if !errors.Is(err, ErrAbsentProof) || mutated {
		t.Fatalf("Complete(different pending authority) error = %v, mutation called = %t", err, mutated)
	}
	observed, exists, err := readProof(writer.path, writer.owner)
	if err != nil || !exists || observed != pending {
		t.Fatalf("readProof() = (%#v, %t, %v), want (%#v, true, nil)", observed, exists, err, pending)
	}
}

// TestCompleteReplacesFailedPendingProofForPresentOwnership lets a later authenticated lifecycle recover from a failed prior mutation.
func TestCompleteReplacesFailedPendingProofForPresentOwnership(t *testing.T) {
	writer, observer := fixture(t)
	pending := requestProof(request("first", "first-nonce"), testTime())
	if err := writeProof(writer.path, writer.owner, pending); err != nil {
		t.Fatalf("write pending: %v", err)
	}
	different := request("second", "second-nonce")
	different.ReleaseOperationID = "operation-second"
	different.OperationRevision = 6
	different.CheckpointRevision = 7
	different.TargetOwnershipFingerprint = hashValue("second-target")
	present := true
	proof, err := writer.Complete(
		context.Background(),
		different,
		Transaction{
			CompareAndSwap: func(context.Context) error {
				present = false
				return nil
			},
			ObserveOwnership: func(context.Context) (bool, error) {
				return present, nil
			},
		},
		testTime().Add(time.Second),
	)
	if err != nil || proof.State != StateReleased {
		t.Fatalf("Complete(different pending authority) = (%#v, %v)", proof, err)
	}
	if proof.TicketReferenceHash != different.TicketReferenceHash ||
		proof.NonceHash != different.NonceHash {
		t.Fatalf("Complete(different pending authority) retained stale audit hashes: %#v", proof)
	}
	if _, err := observer.ConfirmReleased(context.Background(), different.Authority()); err != nil {
		t.Fatalf("ConfirmReleased(different authority) error = %v", err)
	}
}

// TestCompleteResumesPendingPresentWithReissuedReference proves same authority may resume safely without replacing audit hashes.
func TestCompleteResumesPendingPresentWithReissuedReference(t *testing.T) {
	writer, _ := fixture(t)
	pending := requestProof(request("a", "b"), testTime())
	if err := writeProof(writer.path, writer.owner, pending); err != nil {
		t.Fatalf("write pending: %v", err)
	}
	present := true
	proof, err := writer.Complete(
		context.Background(),
		request("new", "nonce"),
		Transaction{
			CompareAndSwap: func(context.Context) error {
				present = false
				return nil
			},
			ObserveOwnership: func(context.Context) (bool, error) {
				return present, nil
			},
		},
		testTime().Add(time.Second),
	)
	if err != nil || proof.TicketReferenceHash != pending.TicketReferenceHash || proof.State != StateReleased {
		t.Fatalf("Complete(resume) = (%#v, %v)", proof, err)
	}
}

// TestConfirmReleasedRejectsPending proves unprivileged terminal confirmation never treats intent as completion.
func TestConfirmReleasedRejectsPending(t *testing.T) {
	writer, observer := fixture(t)
	if err := writeProof(writer.path, writer.owner, requestProof(request("a", "b"), testTime())); err != nil {
		t.Fatalf("write pending: %v", err)
	}
	_, err := observer.ConfirmReleased(
		context.Background(),
		request("a", "b").Authority(),
	)
	if !errors.Is(err, ErrAbsentProof) {
		t.Fatalf("ConfirmReleased() error = %v, want ErrAbsentProof", err)
	}
}

// TestCompleteRollsTerminalProofIntoALaterReleaseCycle proves setup and cleanup can repeat with new durable authority.
func TestCompleteRollsTerminalProofIntoALaterReleaseCycle(t *testing.T) {
	writer, observer := fixture(t)
	present := true
	transaction := Transaction{
		CompareAndSwap: func(context.Context) error {
			present = false
			return nil
		},
		ObserveOwnership: func(context.Context) (bool, error) {
			return present, nil
		},
	}
	first := request("first", "first-nonce")
	if _, err := writer.Complete(context.Background(), first, transaction, testTime()); err != nil {
		t.Fatalf("Complete(first) error = %v", err)
	}
	present = true
	second := request("second", "second-nonce")
	second.ReleaseOperationID = "operation-second"
	second.OperationRevision = 6
	second.CheckpointRevision = 7
	second.TargetOwnershipFingerprint = hashValue("second-target")
	if _, err := writer.Complete(
		context.Background(),
		second,
		transaction,
		testTime().Add(time.Second),
	); err != nil {
		t.Fatalf("Complete(second) error = %v", err)
	}
	if _, err := observer.ConfirmReleased(context.Background(), second.Authority()); err != nil {
		t.Fatalf("ConfirmReleased(second) error = %v", err)
	}
	if _, err := observer.ConfirmReleased(context.Background(), first.Authority()); !errors.Is(err, ErrAbsentProof) {
		t.Fatalf("ConfirmReleased(first) error = %v, want ErrAbsentProof", err)
	}
}

// TestReadProofRejectsDuplicateAndNoncanonicalJSON proves durable evidence cannot use parser-dependent spellings.
func TestReadProofRejectsDuplicateAndNoncanonicalJSON(t *testing.T) {
	writer, observer := fixture(t)
	if err := os.WriteFile(writer.path, []byte(`{"ticket_reference_hash":"`+hashValue("a")+`","ticket_reference_hash":"`+hashValue("a")+`"}`), proofFileMode); err != nil {
		t.Fatal(err)
	}
	_, _, err := observer.Observe(context.Background())
	if err == nil {
		t.Fatal("Observe(duplicate) error = nil")
	}
	if err := os.WriteFile(writer.path, []byte("{}\n"), proofFileMode); err != nil {
		t.Fatal(err)
	}
	_, _, err = observer.Observe(context.Background())
	if err == nil {
		t.Fatal("Observe(noncanonical) error = nil")
	}
}

// TestProofStorageRejectsInsecurePermissions proves proof storage remains inaccessible after a permissive mode change.
func TestProofStorageRejectsInsecurePermissions(t *testing.T) {
	t.Run("directory", func(t *testing.T) {
		directory := t.TempDir()
		if err := os.Chmod(directory, 0o755); err != nil {
			t.Fatal(err)
		}
		_, err := newObserverForOwner(filepath.Join(directory, "ownership-release-proof.json"), uint32(os.Geteuid()))
		if !errors.Is(err, ErrUnsafePath) {
			t.Fatalf("newObserverForOwner() error = %v, want ErrUnsafePath", err)
		}
	})

	t.Run("lock", func(t *testing.T) {
		writer, _ := fixture(t)
		if err := os.Chmod(writer.lockPath, proofLockMode|0o044); err != nil {
			t.Fatal(err)
		}
		_, err := newWriterForOwner(writer.path, writer.lockPath, writer.owner)
		if !errors.Is(err, ErrUnsafePath) {
			t.Fatalf("newWriterForOwner() error = %v, want ErrUnsafePath", err)
		}
	})

	t.Run("proof", func(t *testing.T) {
		writer, observer := fixture(t)
		if err := writeProof(writer.path, writer.owner, requestProof(request("a", "b"), testTime())); err != nil {
			t.Fatalf("write proof: %v", err)
		}
		if err := os.Chmod(writer.path, proofFileMode|0o022); err != nil {
			t.Fatal(err)
		}
		_, _, err := observer.Observe(context.Background())
		if !errors.Is(err, ErrUnsafePath) {
			t.Fatalf("Observe() error = %v, want ErrUnsafePath", err)
		}
	})
}

// TestCompleteSerializesConcurrentWriters proves only one held proof lock can invoke ownership mutation.
func TestCompleteSerializesConcurrentWriters(t *testing.T) {
	writer, _ := fixture(t)
	var mutex sync.Mutex
	present := true
	calls := 0
	transaction := Transaction{
		CompareAndSwap: func(context.Context) error {
			mutex.Lock()
			defer mutex.Unlock()
			calls++
			present = false
			return nil
		},
		ObserveOwnership: func(context.Context) (bool, error) {
			mutex.Lock()
			defer mutex.Unlock()
			return present, nil
		},
	}
	var group sync.WaitGroup
	errorsSeen := make(chan error, 2)
	for range 2 {
		group.Add(1)
		go func() {
			defer group.Done()
			_, err := writer.Complete(context.Background(), request("a", "b"), transaction, testTime())
			errorsSeen <- err
		}()
	}
	group.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		if err != nil {
			t.Fatalf("concurrent Complete() error = %v", err)
		}
	}
	if calls != 1 {
		t.Fatalf("mutation calls = %d, want 1", calls)
	}
}

// fixture creates a daemon-readable machine-root-shaped directory and explicit test-owner writer.
func fixture(t *testing.T) (*RootWriter, *Observer) {
	t.Helper()
	directory := t.TempDir()
	if err := os.Chmod(directory, proofDirectoryMode); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "ownership-release-proof.json")
	lock := filepath.Join(directory, "ownership-release-proof.lock")
	owner := uint32(os.Geteuid())
	writer, err := newWriterForOwner(path, lock, owner)
	if err != nil {
		t.Fatal(err)
	}
	observer, err := newObserverForOwner(path, owner)
	if err != nil {
		t.Fatal(err)
	}
	return writer, observer
}

// request supplies a complete authority with intentionally variable original ticket hashes.
func request(ticket, nonce string) Request {
	return Request{
		TicketReferenceHash:        hashValue(ticket),
		NonceHash:                  hashValue(nonce),
		ReleaseOperationID:         "operation",
		OperationRevision:          4,
		CheckpointRevision:         5,
		RequesterIdentity:          "1000",
		TargetOwnershipFingerprint: hashValue("c"),
	}
}

// hashValue produces a fixed-width lower-case hash-shaped value.
func hashValue(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

// testTime supplies a stable UTC time.
func testTime() time.Time {
	return time.Date(2026, time.July, 23, 1, 2, 3, 0, time.UTC)
}
