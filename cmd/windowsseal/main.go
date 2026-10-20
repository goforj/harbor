// Package main seals signed Windows components into the fixed embedded setup payload.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/goforj/harbor/internal/platform/windowsinstallation"
)

// options describes build-time inputs without introducing caller-selected installation paths.
type options struct {
	components, output, version, revision, publisher string
	sequence                                         uint64
}

// main seals only the repository's compiled role plan after every executable and policy has been signed.
func main() {
	var input options
	flag.StringVar(&input.components, "components", "", "directory containing signed build outputs")
	flag.StringVar(&input.output, "output", "", "embedded payload output directory")
	flag.StringVar(&input.version, "version", "", "development or candidate package version")
	flag.StringVar(&input.revision, "revision", "", "exact product source revision")
	flag.StringVar(&input.publisher, "publisher", "", "expected signing certificate thumbprint")
	flag.Uint64Var(&input.sequence, "sequence", 0, "immutable release sequence")
	flag.Parse()
	if err := seal(input); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// seal hashes the exact copied signed bytes and commits one canonical complete manifest after all copies succeed.
func seal(input options) error {
	if input.components == "" || input.output == "" {
		return errors.New("components and output directories are required")
	}
	manifest := windowsinstallation.Manifest{Architecture: "amd64", SchemaVersion: windowsinstallation.SchemaVersion, ProductID: windowsinstallation.ProductID, Version: input.version, SourceRevision: input.revision, ReleaseSequence: input.sequence, SignerThumbprint: input.publisher, Components: windowsinstallation.Plans(input.sequence)}
	// Validate identity before creating output by using temporary nonempty component commitments.
	for index := range manifest.Components {
		manifest.Components[index].Size = 1
		manifest.Components[index].SHA256 = strings.Repeat("a", 64)
	}
	if err := validateManifest(manifest); err != nil {
		return err
	}
	for index := range manifest.Components {
		component := &manifest.Components[index]
		source := filepath.Join(input.components, componentSource(component.Role, component.Path))
		information, err := os.Lstat(source)
		if err != nil {
			return err
		}
		if !information.Mode().IsRegular() || information.Size() == 0 {
			return fmt.Errorf("signed %s input must be a nonempty regular file", component.Role)
		}
		path := filepath.Join(input.output, filepath.FromSlash(component.Path))
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return err
		}
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return err
		}
		signed, err := os.Open(source)
		if err != nil {
			return errors.Join(err, file.Close())
		}
		hash := sha256.New()
		size, copyErr := io.Copy(io.MultiWriter(file, hash), signed)
		if err := errors.Join(copyErr, signed.Close(), file.Sync(), file.Close()); err != nil {
			return err
		}
		if size != information.Size() {
			return fmt.Errorf("signed %s input changed while sealing", component.Role)
		}
		component.Size = size
		component.SHA256 = hex.EncodeToString(hash.Sum(nil))
	}
	digest, err := windowsinstallation.Digest(manifest)
	if err != nil {
		return err
	}
	manifest.BundleDigest = digest
	if err := validateManifest(manifest); err != nil {
		return err
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(filepath.Join(input.output, "release-manifest.json"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(data)
	return errors.Join(writeErr, file.Sync(), file.Close())
}

// componentSource keeps stable wrappers and duplicated maintenance roles distinct from selected client filenames.
func componentSource(role, destination string) string {
	switch role {
	case "cli-launcher":
		return "cli-launcher.exe"
	case "uninstaller":
		return "harbor-installer.exe"
	default:
		return filepath.Base(destination)
	}
}

// validateManifest reuses runtime metadata admission without inventing a second build-time schema.
func validateManifest(manifest windowsinstallation.Manifest) error {
	digest, err := windowsinstallation.Digest(manifest)
	if err != nil {
		return err
	}
	manifest.BundleDigest = digest
	selection := windowsinstallation.Selection{SchemaVersion: windowsinstallation.SchemaVersion, ProductID: windowsinstallation.ProductID, InstallationID: strings.Repeat("a", 32), UserSID: "S-1-5-21-1-2-3-1000", ReleaseSequence: manifest.ReleaseSequence, BundleDigest: digest, SignerThumbprint: manifest.SignerThumbprint}
	return windowsinstallation.ValidateManifest(manifest, selection)
}
