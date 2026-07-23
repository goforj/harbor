//go:build !linux && (!darwin || !cgo)

package projectterminal

// accountLoginShell reports no platform account shell when native lookup is unavailable.
func accountLoginShell(string) (string, bool, error) {
	return "", false, nil
}
