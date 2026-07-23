//go:build windows

package projectprocess

// syncManagedHostEnvironmentDirectory leaves directory durability to Windows rename and deletion semantics.
func syncManagedHostEnvironmentDirectory(string) error {
	return nil
}
