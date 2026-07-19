//go:build darwin

package ownership

import (
	"errors"
	"io/fs"
	"os"

	"golang.org/x/sys/unix"
)

// createPlatformFile creates an owner-private file exclusively beneath the retained protected directory.
func createPlatformFile(root *os.Root, _ string, name string) (*os.File, error) {
	file, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_RDWR, privateFileMode)
	if err != nil {
		return nil, err
	}
	if err := securePlatformFile(file, false); err != nil {
		return nil, errors.Join(err, file.Close(), root.Remove(name))
	}
	return file, nil
}

// platformRenameNoReplace makes one directory-entry transition durable without exposing a hard-link publication window.
func platformRenameNoReplace(root *os.Root, _ string, source string, destination string) (bool, error) {
	directory, err := root.Open(".")
	if err != nil {
		return false, err
	}
	err = unix.RenameatxNp(int(directory.Fd()), source, int(directory.Fd()), destination, unix.RENAME_EXCL)
	if errors.Is(err, unix.EEXIST) {
		err = fs.ErrExist
	}
	if err != nil {
		_ = directory.Close()
		return false, err
	}
	syncErr := directory.Sync()
	_ = directory.Close()
	return true, syncErr
}

// platformRenameReplace atomically overwrites one entry beneath the retained directory and confirms the name transition.
func platformRenameReplace(root *os.Root, _ string, source string, destination string) (bool, error) {
	directory, err := root.Open(".")
	if err != nil {
		return false, err
	}
	err = unix.Renameat(int(directory.Fd()), source, int(directory.Fd()), destination)
	if err != nil {
		return false, errors.Join(err, directory.Close())
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	return true, errors.Join(syncErr, closeErr)
}

// platformConfirmEntry records an existing entry at the Unix directory durability boundary.
func platformConfirmEntry(root *os.Root, _ string, _ string) error {
	directory, err := root.Open(".")
	if err != nil {
		return err
	}
	syncErr := directory.Sync()
	_ = directory.Close()
	return syncErr
}

// platformConfirmCleanup records best-effort retired-entry removal without weakening the earlier release boundary.
func platformConfirmCleanup(root *os.Root) error {
	return platformConfirmEntry(root, "", "")
}
