//go:build !darwin && !linux

package projectdiscovery

import "os"

// openMetadataFile opens metadata after its path has passed the native regular-file check.
func openMetadataFile(filename string) (*os.File, error) {
	return os.Open(filename)
}
