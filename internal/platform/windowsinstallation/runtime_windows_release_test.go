//go:build windows && release

package windowsinstallation

import "testing"

// TestRuntimeSelectionRejectsUnselectedImageAndUser prevents a signed copied payload or another user from opening installed application state.
func TestRuntimeSelectionRejectsUnselectedImageAndUser(t *testing.T) {
	for _, input := range []struct {
		name, current, selected, user, selectedUser string
		valid                                       bool
	}{
		{"exact", `C:\Program Files\GoForj\Harbor\releases\1\harbord.exe`, `C:\Program Files\GoForj\Harbor\releases\1\harbord.exe`, "user", "user", true},
		{"case", `C:\Harbor\Harbord.exe`, `c:\harbor\harbord.exe`, "user", "user", true},
		{"copied", `C:\Temp\harbord.exe`, `C:\Harbor\harbord.exe`, "user", "user", false},
		{"user", `C:\Harbor\harbord.exe`, `C:\Harbor\harbord.exe`, "other", "user", false},
	} {
		t.Run(input.name, func(t *testing.T) {
			if err := admitRuntimeSelection(input.current, input.selected, input.user, input.selectedUser); (err == nil) != input.valid {
				t.Fatal(err)
			}
		})
	}
	if err := CheckRuntimeRole("installer"); err == nil {
		t.Fatal("maintenance role admitted as user runtime")
	}
}
