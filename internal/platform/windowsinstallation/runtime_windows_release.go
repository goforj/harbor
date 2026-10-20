//go:build windows && release

package windowsinstallation

import (
	"errors"
	"os"
	"strings"
)

// CheckRuntimeRole rejects direct elevated or unselected payload execution before any application data is opened.
func CheckRuntimeRole(role string) (result error) {
	if role != "desktop" && role != "daemon" && role != "cli" {
		return errors.New("installed runtime role is not admitted")
	}
	facts, err := InspectCurrentToken()
	if err != nil {
		return err
	}
	installation, err := Admit()
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, installation.Close()) }()
	selected, err := installation.Path(role)
	if err != nil {
		return err
	}
	current, err := os.Executable()
	if err != nil {
		return err
	}
	return admitRuntimeSelection(current, selected, facts.UserSID, installation.selection.UserSID)
}

// admitRuntimeSelection binds an already effective-token-admitted process to its immutable selected role and interactive identity.
func admitRuntimeSelection(current, selected, user, selectedUser string) error {
	if !strings.EqualFold(current, selected) {
		return errors.New("runtime image is not the selected installed role")
	}
	if user != selectedUser {
		return errors.New("runtime identity differs from the selected installation user")
	}
	return nil
}
