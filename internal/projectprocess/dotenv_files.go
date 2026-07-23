package projectprocess

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const maximumDotenvFileBytes = 1024 * 1024

var (
	// ErrDotenvFileChanged reports that an edit no longer matches the displayed file revision.
	ErrDotenvFileChanged = errors.New("dotenv file changed outside Harbor; reload before saving")
)

// DotenvFile is one direct, regular dotenv file in a registered checkout root.
// Revision fences an edit against the precise bytes that were displayed.
type DotenvFile struct {
	Name     string
	Contents string
	Revision string
}

// ListDotenvFiles reads the direct dotenv files from a checkout root. It never
// descends into the checkout and ignores symlinks, directories, and unrelated
// dotfiles so callers cannot use this surface as a file browser.
func ListDotenvFiles(checkoutRoot string) ([]DotenvFile, error) {
	entries, err := os.ReadDir(checkoutRoot)
	if err != nil {
		return nil, fmt.Errorf("read project root: %w", err)
	}
	result := make([]DotenvFile, 0)
	for _, entry := range entries {
		if !isDotenvFilename(entry.Name()) || entry.Type()&fs.ModeSymlink != 0 || !entry.Type().IsRegular() {
			continue
		}
		file, err := readDotenvFile(checkoutRoot, entry.Name())
		if err != nil {
			return nil, err
		}
		result = append(result, file)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, nil
}

// SaveDotenvFile replaces one direct dotenv file only if its displayed revision
// still matches.
func SaveDotenvFile(checkoutRoot string, name string, contents string, revision string) (DotenvFile, error) {
	if !isDotenvFilename(name) {
		return DotenvFile{}, errors.New("dotenv filename is not allowed")
	}
	if len(contents) > maximumDotenvFileBytes {
		return DotenvFile{}, fmt.Errorf("dotenv file exceeds %d bytes", maximumDotenvFileBytes)
	}
	current, err := readDotenvFile(checkoutRoot, name)
	if err != nil {
		return DotenvFile{}, err
	}
	if revision != current.Revision {
		return DotenvFile{}, ErrDotenvFileChanged
	}
	if err := replaceDotenvFile(checkoutRoot, name, []byte(contents), revision); err != nil {
		return DotenvFile{}, err
	}
	return readDotenvFile(checkoutRoot, name)
}

// replaceDotenvFile stages bytes beside the selected root file, then rechecks
// the digest fence immediately before publication.
func replaceDotenvFile(checkoutRoot string, name string, contents []byte, revision string) (replaceErr error) {
	mode := fs.FileMode(0o600)
	if info, err := os.Lstat(filepath.Join(checkoutRoot, name)); err == nil {
		if !info.Mode().IsRegular() {
			return errors.New("dotenv entry must be a regular file")
		}
		mode = info.Mode().Perm()
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	temporary, err := os.CreateTemp(checkoutRoot, ".harbor-dotenv-*")
	if err != nil {
		return fmt.Errorf("stage dotenv file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() {
		if removeErr := os.Remove(temporaryPath); removeErr != nil && !errors.Is(removeErr, fs.ErrNotExist) && replaceErr == nil {
			replaceErr = removeErr
		}
	}()
	if err := temporary.Chmod(mode); err != nil {
		return fmt.Errorf("set staged dotenv mode: %w", err)
	}
	if _, err := temporary.Write(contents); err != nil {
		return fmt.Errorf("write staged dotenv file: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync staged dotenv file: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close staged dotenv file: %w", err)
	}
	_, err = readDotenvFile(checkoutRoot, name)
	if errors.Is(err, fs.ErrNotExist) {
		return ErrDotenvFileChanged
	} else if err != nil {
		return err
	} else {
		current, err := readDotenvFile(checkoutRoot, name)
		if err != nil || current.Revision != revision {
			return ErrDotenvFileChanged
		}
	}
	if err := os.Rename(temporaryPath, filepath.Join(checkoutRoot, name)); err != nil {
		return fmt.Errorf("publish dotenv file: %w", err)
	}
	if err := syncManagedHostEnvironmentParentDirectory(checkoutRoot); err != nil {
		return fmt.Errorf("sync dotenv directory: %w", err)
	}
	return nil
}

// isDotenvFilename admits `.env` and direct dotenv variants such as `.env.local`.
func isDotenvFilename(name string) bool {
	return name == ".env" || (strings.HasPrefix(name, ".env.") && len(name) > len(".env.") && filepath.Base(name) == name)
}

// readDotenvFile confirms that the selected direct root entry remains a regular file before returning its bounded bytes.
func readDotenvFile(checkoutRoot string, name string) (DotenvFile, error) {
	if !isDotenvFilename(name) {
		return DotenvFile{}, errors.New("dotenv filename is not allowed")
	}
	path := filepath.Join(checkoutRoot, name)
	info, err := os.Lstat(path)
	if err != nil {
		return DotenvFile{}, err
	}
	if !info.Mode().IsRegular() {
		return DotenvFile{}, errors.New("dotenv entry must be a regular file")
	}
	if info.Size() > maximumDotenvFileBytes {
		return DotenvFile{}, fmt.Errorf("dotenv file exceeds %d bytes", maximumDotenvFileBytes)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return DotenvFile{}, err
	}
	if len(contents) > maximumDotenvFileBytes {
		return DotenvFile{}, fmt.Errorf("dotenv file exceeds %d bytes", maximumDotenvFileBytes)
	}
	digest := sha256.Sum256(contents)
	return DotenvFile{Name: name, Contents: string(contents), Revision: hex.EncodeToString(digest[:])}, nil
}
