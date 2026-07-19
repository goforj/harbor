//go:build !darwin && !linux && !windows

package ticketkey

import (
	"fmt"
	"os"
)

// openPlatformFileNoFollow rejects targets whose direct-open semantics have not been proved.
func openPlatformFileNoFollow(string, bool) (*os.File, error) {
	return nil, fmt.Errorf("secure helper ticket key persistence is unsupported on this platform")
}

// preparePlatformRoot rejects targets whose owner-private persistence semantics have not been proved.
func preparePlatformRoot(string) error {
	return fmt.Errorf("secure helper ticket key persistence is unsupported on this platform")
}

// platformSecureCreatedFile rejects targets whose owner-private creation semantics have not been proved.
func platformSecureCreatedFile(*os.File, bool) error {
	return fmt.Errorf("secure helper ticket key persistence is unsupported on this platform")
}

// validatePlatformPath rejects targets whose owner and permission semantics have not been proved.
func validatePlatformPath(string, bool) error {
	return fmt.Errorf("secure helper ticket key persistence is unsupported on this platform")
}

// validatePlatformFile rejects targets whose handle-level owner and permission semantics have not been proved.
func validatePlatformFile(*os.File, bool) error {
	return fmt.Errorf("secure helper ticket key persistence is unsupported on this platform")
}

// platformSameFile rejects identity comparisons whose handle semantics have not been proved.
func platformSameFile(*os.File, *os.File) (bool, error) {
	return false, fmt.Errorf("secure helper ticket key persistence is unsupported on this platform")
}

// platformSyncDirectory rejects targets whose metadata durability semantics have not been proved.
func platformSyncDirectory(*os.File) error {
	return fmt.Errorf("secure helper ticket key persistence is unsupported on this platform")
}
