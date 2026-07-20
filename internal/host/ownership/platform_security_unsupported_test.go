//go:build !darwin && !linux && !windows

package ownership

import (
	"errors"
	"os"
	"testing"
)

// prepareTestStoreDirectory keeps unsupported-platform tests aligned with the shared fixture contract.
func prepareTestStoreDirectory(t *testing.T, directory string) {
	t.Helper()
	file, err := os.Open(directory)
	if err != nil {
		t.Fatalf("os.Open(%q) error = %v", directory, err)
	}
	secureErr := securePlatformFile(file, true)
	closeErr := file.Close()
	if err := errors.Join(secureErr, closeErr); err != nil {
		t.Fatalf("secure test store directory %q: %v", directory, err)
	}
}
