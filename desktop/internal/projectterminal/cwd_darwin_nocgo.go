//go:build darwin && !cgo

package projectterminal

import (
	"fmt"
	"os"
)

// verifyProcessDirectory fails closed when Darwin cannot inspect the child cwd identity.
func verifyProcessDirectory(int, *os.File) error {
	return fmt.Errorf("Darwin project terminal directory verification requires cgo")
}
