package projectprocess

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestDevelopmentBuildCacheLeasesRemoveOnlyAfterLastSettlement verifies concurrent projects share one bounded cache lifetime.
func TestDevelopmentBuildCacheLeasesRemoveOnlyAfterLastSettlement(t *testing.T) {
	directory := t.TempDir()
	supervisor := newTestSupervisor(Options{Environment: Environment{"PATH="}})
	supervisor.developmentArtifactDirectory = directory
	first, err := supervisor.acquireDevelopmentBuildCache()
	if err != nil {
		t.Fatalf("acquire first development build cache: %v", err)
	}
	second, err := supervisor.acquireDevelopmentBuildCache()
	if err != nil {
		_ = first.release(true)
		t.Fatalf("acquire second development build cache: %v", err)
	}
	if first.path != second.path {
		t.Fatalf("development build cache paths = %q and %q", first.path, second.path)
	}
	if err := os.WriteFile(filepath.Join(first.path, "entry"), []byte("cache"), 0o600); err != nil {
		t.Fatalf("write development build cache entry: %v", err)
	}
	if err := first.release(true); err != nil {
		t.Fatalf("release first development build cache: %v", err)
	}
	if _, err := os.Stat(first.path); err != nil {
		t.Fatalf("shared cache disappeared with an active lease: %v", err)
	}
	if err := second.release(true); err != nil {
		t.Fatalf("release final development build cache: %v", err)
	}
	if _, err := os.Stat(first.path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("development build cache after final release error = %v, want not exist", err)
	}
}

// TestDevelopmentBuildCacheRetainsUncertainScope verifies cleanup never removes cache files beneath a possibly live child.
func TestDevelopmentBuildCacheRetainsUncertainScope(t *testing.T) {
	directory := t.TempDir()
	supervisor := newTestSupervisor(Options{Environment: Environment{"PATH="}})
	supervisor.developmentArtifactDirectory = directory
	lease, err := supervisor.acquireDevelopmentBuildCache()
	if err != nil {
		t.Fatalf("acquire development build cache: %v", err)
	}
	if err := lease.release(false); err != nil {
		t.Fatalf("retain uncertain development build cache: %v", err)
	}
	if _, err := os.Stat(lease.path); err != nil {
		t.Fatalf("uncertain development build cache disappeared: %v", err)
	}
	t.Cleanup(func() {
		supervisor.developmentCacheMu.Lock()
		defer supervisor.developmentCacheMu.Unlock()
		_ = supervisor.developmentCache.remove()
		supervisor.developmentCache = nil
	})
}

// TestDevelopmentBuildCacheUsesCapturedExternalCache verifies Harbor never deletes caller-selected Go cache state.
func TestDevelopmentBuildCacheUsesCapturedExternalCache(t *testing.T) {
	cache := filepath.Join(t.TempDir(), "external-cache")
	supervisor := newTestSupervisor(Options{Environment: Environment{"GOCACHE=" + cache}})
	lease, err := supervisor.acquireDevelopmentBuildCache()
	if err != nil {
		t.Fatalf("acquire external development build cache: %v", err)
	}
	if lease.path != cache || lease.owned {
		t.Fatalf("external development build cache lease = %#v", lease)
	}
	if err := os.MkdirAll(cache, 0o700); err != nil {
		t.Fatalf("create external development build cache: %v", err)
	}
	if err := lease.release(true); err != nil {
		t.Fatalf("release external development build cache: %v", err)
	}
	if _, err := os.Stat(cache); err != nil {
		t.Fatalf("external development build cache was removed: %v", err)
	}
}
