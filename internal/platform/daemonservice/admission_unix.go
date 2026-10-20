//go:build darwin || linux

package daemonservice

import (
	"bytes"
	"errors"
	"os"
	"syscall"
)

// AdmitExecutable rejects package components that could redirect or elevate the unprivileged daemon.
func AdmitExecutable(information os.FileInfo) error {
	if err := admitPackageFile(information); err != nil {
		return err
	}
	if information.Mode().Perm()&0o111 == 0 {
		return errors.New("installed Harbor component is not executable")
	}
	return nil
}

// AdmitUnit requires the exact root-owned systemd definition shipped with this release.
func AdmitUnit(information os.FileInfo, content []byte) error {
	if err := admitPackageFile(information); err != nil {
		return err
	}
	if information.Mode().Perm() != 0o644 || !bytes.Equal(content, LinuxDefinition()) {
		return errors.New("installed Harbor systemd unit differs from the package contract")
	}
	return nil
}

// admitPackageFile excludes links, mutable files, and set-ID programs from installed daemon authority.
func admitPackageFile(information os.FileInfo) error {
	status, ok := information.Sys().(*syscall.Stat_t)
	if !ok || !information.Mode().IsRegular() || status.Uid != 0 || status.Nlink != 1 || information.Mode().Perm()&0o022 != 0 || information.Mode()&(os.ModeSetuid|os.ModeSetgid) != 0 {
		return errors.New("component is not a direct immutable root-owned package file")
	}
	return nil
}
