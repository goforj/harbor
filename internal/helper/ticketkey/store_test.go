package ticketkey

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
)

// TestLoadOrCreateRetainsIdentityAcrossRestart verifies the durable key is generated once and reloaded exactly.
func TestLoadOrCreateRetainsIdentityAcrossRestart(t *testing.T) {
	t.Parallel()
	directory := filepath.Join(t.TempDir(), "helper-ticket-key")
	store := mustStore(t, directory)
	first, err := store.LoadOrCreate(context.Background())
	if err != nil {
		t.Fatalf("LoadOrCreate(first) error = %v", err)
	}
	second, err := store.LoadOrCreate(nil)
	if err != nil {
		t.Fatalf("LoadOrCreate(second) error = %v", err)
	}
	if !first.Equal(second) {
		t.Fatal("LoadOrCreate() changed the signing identity within one process")
	}
	if &first[0] == &second[0] {
		t.Fatal("LoadOrCreate() shared caller-mutable key bytes")
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	reopened := mustStore(t, directory)
	t.Cleanup(func() { _ = reopened.Close() })
	third, err := reopened.LoadOrCreate(context.Background())
	if err != nil {
		t.Fatalf("LoadOrCreate(reopened) error = %v", err)
	}
	if !first.Equal(third) {
		t.Fatal("LoadOrCreate() changed the signing identity after restart")
	}

	first[0] ^= 0xff
	again, err := reopened.LoadOrCreate(context.Background())
	if err != nil {
		t.Fatalf("LoadOrCreate(after caller mutation) error = %v", err)
	}
	if !third.Equal(again) {
		t.Fatal("caller mutation changed persisted or retained signing-key bytes")
	}
	assertStoreLayout(t, directory)
}

// TestLoadRequiresEstablishedIdentity proves ticket issuance cannot rotate missing authority after machine ownership exists.
func TestLoadRequiresEstablishedIdentity(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "helper-ticket-key")
	store := mustStore(t, directory)
	defer store.Close()

	if key, err := store.Load(context.Background()); key != nil || !errors.Is(err, ErrKeyNotEstablished) {
		t.Fatalf("Load(absent) = %x, %v, want ErrKeyNotEstablished", key, err)
	}
	if entries, err := os.ReadDir(directory); err != nil || len(entries) != 0 {
		t.Fatalf("Load(absent) entries = %#v, error = %v, want empty store", entries, err)
	}

	created, err := store.LoadOrCreate(context.Background())
	if err != nil {
		t.Fatalf("LoadOrCreate() error = %v", err)
	}
	loaded, err := store.Load(nil)
	if err != nil {
		t.Fatalf("Load(established) error = %v", err)
	}
	if !created.Equal(loaded) {
		t.Fatal("Load() returned a different established identity")
	}
	if &created[0] == &loaded[0] {
		t.Fatal("Load() shared caller-mutable key bytes")
	}

	if err := store.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if key, err := store.Load(context.Background()); key != nil || !errors.Is(err, ErrStoreClosed) {
		t.Fatalf("Load(closed) = %x, %v, want ErrStoreClosed", key, err)
	}
}

// TestConcurrentFirstCreationUsesOneWinner verifies independent daemon instances converge on one atomic publication.
func TestConcurrentFirstCreationUsesOneWinner(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "helper-ticket-key")
	stores := make([]*Store, 8)
	for index := range stores {
		stores[index] = mustStore(t, directory)
		defer stores[index].Close()
	}
	start := make(chan struct{})
	results := make(chan ed25519.PrivateKey, len(stores))
	errorsChannel := make(chan error, len(stores))
	var workers sync.WaitGroup
	for _, store := range stores {
		workers.Add(1)
		go func(store *Store) {
			defer workers.Done()
			<-start
			key, err := store.LoadOrCreate(context.Background())
			if err != nil {
				errorsChannel <- err
				return
			}
			results <- key
		}(store)
	}
	close(start)
	workers.Wait()
	close(results)
	close(errorsChannel)
	for err := range errorsChannel {
		t.Fatalf("LoadOrCreate(concurrent) error = %v", err)
	}
	var winner ed25519.PrivateKey
	for key := range results {
		if winner == nil {
			winner = key
			continue
		}
		if !winner.Equal(key) {
			t.Fatal("concurrent creators returned different signing identities")
		}
	}
	if winner == nil {
		t.Fatal("concurrent creation returned no signing identity")
	}
	assertStoreLayout(t, directory)
}

// TestLoadOrCreateRejectsCorruptDocuments verifies the bounded canonical representation fails closed.
func TestLoadOrCreateRejectsCorruptDocuments(t *testing.T) {
	validKey := make(ed25519.PrivateKey, ed25519.PrivateKeySize)
	_, validKey, _ = ed25519.GenerateKey(rand.Reader)
	valid, err := encodePrivateKey(validKey)
	if err != nil {
		t.Fatalf("encodePrivateKey() error = %v", err)
	}
	secretSentinel := "SECRET-SENTINEL-DO-NOT-LOG"
	tests := []struct {
		name    string
		content []byte
		want    string
	}{
		{name: "empty", content: nil, want: "size"},
		{name: "whitespace", content: append(append([]byte(nil), valid...), '\n'), want: "canonically"},
		{name: "unknown field", content: bytes.Replace(valid, []byte("}"), []byte(",\"extra\":true}"), 1), want: "canonically"},
		{name: "duplicate field", content: bytes.Replace(valid, []byte("\"version\":1"), []byte("\"version\":1,\"version\":1"), 1), want: "canonically"},
		{name: "wrong version", content: bytes.Replace(valid, []byte("\"version\":1"), []byte("\"version\":2"), 1), want: "version"},
		{name: "wrong algorithm", content: bytes.Replace(valid, []byte("ed25519"), []byte("rsa"), 1), want: "algorithm"},
		{name: "noncanonical base64", content: bytes.Replace(valid, validSeedField(valid), []byte("\"seed\":\""+secretSentinel+"\""), 1), want: "seed"},
		{name: "oversized", content: bytes.Repeat([]byte("x"), maximumKeyDocumentBytes+1), want: "exceeds"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			directory := filepath.Join(t.TempDir(), "helper-ticket-key")
			store := mustStore(t, directory)
			if _, err := store.LoadOrCreate(context.Background()); err != nil {
				t.Fatalf("LoadOrCreate(seed) error = %v", err)
			}
			path := filepath.Join(directory, activeDirectory, keyFilename)
			if err := os.WriteFile(path, test.content, privateFileMode); err != nil {
				t.Fatalf("WriteFile(corrupt) error = %v", err)
			}
			_, err := store.LoadOrCreate(context.Background())
			var corruption *CorruptionError
			if !errors.As(err, &corruption) || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("LoadOrCreate(corrupt) error = %v, want corruption containing %q", err, test.want)
			}
			if strings.Contains(err.Error(), secretSentinel) {
				t.Fatalf("corruption error leaked signing-key content: %v", err)
			}
			_ = store.Close()
		})
	}
}

// TestEncodingRejectsInvalidPrivateKey verifies only complete Ed25519 identities enter the durable encoder.
func TestEncodingRejectsInvalidPrivateKey(t *testing.T) {
	if _, err := encodePrivateKey(ed25519.PrivateKey("short")); err == nil || !strings.Contains(err.Error(), "invalid size") {
		t.Fatalf("encodePrivateKey(short) error = %v", err)
	}
	if _, err := decodePrivateKey([]byte("not-json")); err == nil || !strings.Contains(err.Error(), "decode") {
		t.Fatalf("decodePrivateKey(malformed JSON) error = %v", err)
	}
}

// TestLoadOrCreateRejectsMalformedActiveLayout verifies partial and ambiguous durable state cannot trigger rotation.
func TestLoadOrCreateRejectsMalformedActiveLayout(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, string)
		want   string
	}{
		{
			name: "active is a regular file",
			mutate: func(t *testing.T, directory string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(directory, activeDirectory), []byte("invalid"), privateFileMode); err != nil {
					t.Fatalf("WriteFile(active) error = %v", err)
				}
			},
			want: "direct directory",
		},
		{
			name: "key is a directory",
			mutate: func(t *testing.T, directory string) {
				t.Helper()
				active := filepath.Join(directory, activeDirectory)
				if err := os.Mkdir(active, privateDirectoryMode); err != nil {
					t.Fatalf("Mkdir(active) error = %v", err)
				}
				if err := os.Mkdir(filepath.Join(active, keyFilename), privateDirectoryMode); err != nil {
					t.Fatalf("Mkdir(key) error = %v", err)
				}
			},
			want: "unexpected entry",
		},
		{
			name: "unexpected second entry",
			mutate: func(t *testing.T, directory string) {
				t.Helper()
				store := mustStore(t, directory)
				if _, err := store.LoadOrCreate(context.Background()); err != nil {
					t.Fatalf("LoadOrCreate(seed) error = %v", err)
				}
				_ = store.Close()
				if err := os.WriteFile(filepath.Join(directory, activeDirectory, "unexpected"), []byte("x"), privateFileMode); err != nil {
					t.Fatalf("WriteFile(unexpected) error = %v", err)
				}
			},
			want: "contains 2 entries",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			directory := filepath.Join(t.TempDir(), "helper-ticket-key")
			base := mustStore(t, directory)
			if err := base.Close(); err != nil {
				t.Fatalf("Close(base) error = %v", err)
			}
			test.mutate(t, directory)
			store := mustStore(t, directory)
			defer store.Close()
			_, err := store.LoadOrCreate(context.Background())
			var corruption *CorruptionError
			if !errors.As(err, &corruption) || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("LoadOrCreate(malformed layout) error = %v, want corruption containing %q", err, test.want)
			}
		})
	}
}

// TestLoadOrCreateHonorsLifecycleAndEntropyFailures verifies rejected work cannot publish active material.
func TestLoadOrCreateHonorsLifecycleAndEntropyFailures(t *testing.T) {
	t.Run("cancelled", func(t *testing.T) {
		directory := filepath.Join(t.TempDir(), "helper-ticket-key")
		store := mustStore(t, directory)
		defer store.Close()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := store.LoadOrCreate(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("LoadOrCreate(cancelled) error = %v", err)
		}
		if _, err := os.Stat(filepath.Join(directory, activeDirectory)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("cancelled call published active material: %v", err)
		}
	})

	t.Run("closed", func(t *testing.T) {
		store := mustStore(t, filepath.Join(t.TempDir(), "helper-ticket-key"))
		if err := store.Close(); err != nil {
			t.Fatalf("Close(first) error = %v", err)
		}
		if err := store.Close(); err != nil {
			t.Fatalf("Close(second) error = %v", err)
		}
		if _, err := store.LoadOrCreate(context.Background()); !errors.Is(err, ErrStoreClosed) {
			t.Fatalf("LoadOrCreate(closed) error = %v", err)
		}
	})

	t.Run("entropy", func(t *testing.T) {
		directory := filepath.Join(t.TempDir(), "helper-ticket-key")
		want := errors.New("entropy unavailable")
		store, err := openStore(directory, storeDependencies{random: errorReader{err: want}})
		if err != nil {
			t.Fatalf("openStore() error = %v", err)
		}
		defer store.Close()
		if _, err := store.LoadOrCreate(context.Background()); !errors.Is(err, want) {
			t.Fatalf("LoadOrCreate(entropy failure) error = %v, want %v", err, want)
		}
		if _, err := os.Stat(filepath.Join(directory, activeDirectory)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("entropy failure published active material: %v", err)
		}
	})

	t.Run("cancelled before publication", func(t *testing.T) {
		directory := filepath.Join(t.TempDir(), "helper-ticket-key")
		ctx, cancel := context.WithCancel(context.Background())
		store, err := openStore(directory, storeDependencies{
			random: &cancellingReader{cancel: cancel},
		})
		if err != nil {
			t.Fatalf("openStore() error = %v", err)
		}
		defer store.Close()
		if _, err := store.LoadOrCreate(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("LoadOrCreate(cancelled before publication) error = %v", err)
		}
		entries, err := os.ReadDir(directory)
		if err != nil {
			t.Fatalf("ReadDir(root) error = %v", err)
		}
		if len(entries) != 0 {
			t.Fatalf("cancelled publication left entries = %#v", directoryEntryNames(entries))
		}
	})
}

// TestOpenRejectsInvalidInputsAndRootSwap verifies a mutable pathname cannot substitute another root after validation.
func TestOpenRejectsInvalidInputsAndRootSwap(t *testing.T) {
	if _, err := Open(""); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("Open(empty) error = %v", err)
	}
	if _, err := Open("relative"); err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Fatalf("Open(relative) error = %v", err)
	}
	if _, err := openStore(filepath.Join(t.TempDir(), "helper-ticket-key"), storeDependencies{}); err == nil || !strings.Contains(err.Error(), "random source") {
		t.Fatalf("openStore(nil random) error = %v", err)
	}

	workspace := t.TempDir()
	directory := filepath.Join(workspace, "helper-ticket-key")
	_, err := openStore(directory, storeDependencies{
		random: rand.Reader,
		afterValidation: func() {
			original := filepath.Join(workspace, "original")
			if renameErr := os.Rename(directory, original); renameErr != nil {
				t.Fatalf("Rename(root) error = %v", renameErr)
			}
			if mkdirErr := os.Mkdir(directory, privateDirectoryMode); mkdirErr != nil {
				t.Fatalf("Mkdir(replacement) error = %v", mkdirErr)
			}
		},
	})
	if err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("openStore(swapped root) error = %v", err)
	}
}

// TestLoadRejectsKeyPathSwap verifies validation and reading remain bound to one exact key object.
func TestLoadRejectsKeyPathSwap(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "helper-ticket-key")
	base := mustStore(t, directory)
	if _, err := base.LoadOrCreate(context.Background()); err != nil {
		t.Fatalf("LoadOrCreate(seed) error = %v", err)
	}
	if err := base.Close(); err != nil {
		t.Fatalf("Close(base) error = %v", err)
	}

	keyPath := filepath.Join(directory, activeDirectory, keyFilename)
	originalPath := filepath.Join(directory, activeDirectory, "original.json")
	triggered := false
	store, err := openStore(directory, storeDependencies{
		random: rand.Reader,
		beforeOpen: func(path string) {
			if triggered || path != filepath.Join(activeDirectory, keyFilename) {
				return
			}
			triggered = true
			if renameErr := os.Rename(keyPath, originalPath); renameErr != nil {
				t.Fatalf("Rename(key) error = %v", renameErr)
			}
			if writeErr := os.WriteFile(keyPath, []byte("replacement"), privateFileMode); writeErr != nil {
				t.Fatalf("WriteFile(replacement) error = %v", writeErr)
			}
		},
	})
	if err != nil {
		t.Fatalf("openStore() error = %v", err)
	}
	defer store.Close()
	_, err = store.LoadOrCreate(context.Background())
	var corruption *CorruptionError
	if !triggered || !errors.As(err, &corruption) || !strings.Contains(err.Error(), "changed while") {
		t.Fatalf("LoadOrCreate(swapped key) error = %v, triggered = %t", err, triggered)
	}
}

// TestStoreWritesThroughRetainedRoot verifies later pathname replacement cannot redirect key publication.
func TestStoreWritesThroughRetainedRoot(t *testing.T) {
	workspace := t.TempDir()
	directory := filepath.Join(workspace, "helper-ticket-key")
	store := mustStore(t, directory)
	defer store.Close()
	original := filepath.Join(workspace, "original")
	if err := os.Rename(directory, original); err != nil {
		if runtime.GOOS == "windows" {
			if _, loadErr := store.LoadOrCreate(context.Background()); loadErr != nil {
				t.Fatalf("LoadOrCreate() after Windows root rename refusal: %v", loadErr)
			}
			assertStoreLayout(t, directory)
			return
		}
		t.Fatalf("Rename(root) error = %v", err)
	}
	if err := os.Mkdir(directory, privateDirectoryMode); err != nil {
		t.Fatalf("Mkdir(replacement) error = %v", err)
	}
	if _, err := store.LoadOrCreate(context.Background()); err != nil {
		t.Fatalf("LoadOrCreate() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(original, activeDirectory, keyFilename)); err != nil {
		t.Fatalf("original-tree key error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(directory, activeDirectory)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("replacement tree was touched: %v", err)
	}
}

// validSeedField returns the canonical seed field fragment for corruption fixtures.
func validSeedField(encoded []byte) []byte {
	start := bytes.Index(encoded, []byte("\"seed\":"))
	if start < 0 {
		return nil
	}
	return encoded[start : len(encoded)-1]
}

// mustStore opens a test store or terminates the current test.
func mustStore(t *testing.T, directory string) *Store {
	t.Helper()
	store, err := Open(directory)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	return store
}

// assertStoreLayout verifies publication leaves exactly one complete active identity.
func assertStoreLayout(t *testing.T, directory string) {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatalf("ReadDir(root) error = %v", err)
	}
	if got := directoryEntryNames(entries); !reflect.DeepEqual(got, []string{activeDirectory}) {
		t.Fatalf("root entries = %#v, want [%q]", got, activeDirectory)
	}
	entries, err = os.ReadDir(filepath.Join(directory, activeDirectory))
	if err != nil {
		t.Fatalf("ReadDir(active) error = %v", err)
	}
	if got := directoryEntryNames(entries); !reflect.DeepEqual(got, []string{keyFilename}) {
		t.Fatalf("active entries = %#v, want [%q]", got, keyFilename)
	}
}

// directoryEntryNames returns deterministic entry names from an already sorted os.ReadDir result.
func directoryEntryNames(entries []os.DirEntry) []string {
	names := make([]string, len(entries))
	for index, entry := range entries {
		names[index] = entry.Name()
	}
	return names
}

// errorReader makes entropy failures deterministic without intercepting crypto packages globally.
type errorReader struct {
	err error
}

// Read returns the configured failure before writing any bytes.
func (reader errorReader) Read([]byte) (int, error) {
	return 0, reader.err
}

var _ io.Reader = errorReader{}

// cancellingReader cancels after serving key entropy so create reaches its pre-publication boundary.
type cancellingReader struct {
	cancel context.CancelFunc
	calls  int
}

// Read returns deterministic bytes and cancels after the staging-name read has completed.
func (reader *cancellingReader) Read(buffer []byte) (int, error) {
	for index := range buffer {
		buffer[index] = byte(index + reader.calls + 1)
	}
	reader.calls++
	if reader.calls == 2 {
		reader.cancel()
	}
	return len(buffer), nil
}

var _ io.Reader = (*cancellingReader)(nil)
