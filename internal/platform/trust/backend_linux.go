//go:build linux

package trust

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const (
	ubuntuTrustSourceDirectory = "/usr/local/share/ca-certificates"
	ubuntuTrustSourceName      = "goforj-harbor.crt"
	ubuntuTrustPendingName     = "goforj-harbor.crt.harbor-release"
	ubuntuTrustMarkerDirectory = "/var/lib/goforj/harbor"
	ubuntuTrustMarkerName      = "trust-owner.v1"
	ubuntuTrustMarkerMode      = 0o644
	ubuntuTrustBundlePath      = "/etc/ssl/certs/ca-certificates.crt"
	ubuntuTrustUpdateCommand   = "/usr/sbin/update-ca-certificates"
	maximumUbuntuTrustBundle   = 16 << 20
	maximumUbuntuCommandOutput = 16 << 10
	ubuntuTrustCommandTimeout  = 30 * time.Second
	ubuntuTrustExecutableFD    = "/proc/self/fd/3"
)

// ubuntuSystemTrust owns one fixed local-CA source, protected owner sidecar, and Debian system-bundle refresh.
type ubuntuSystemTrust struct{}

// linuxTrustFile contains one no-follow regular-file observation.
type linuxTrustFile struct {
	Present bool
	Exact   bool
	Content []byte
}

// New returns the reviewed Ubuntu 24.04 system trust adapter.
func New() (*Adapter, error) {
	return newAdapter(newUbuntuTrustBackend(ubuntuSystemTrust{})), nil
}

// snapshot observes the fixed source, recovery source, owner sidecar, and active system bundle.
func (ubuntuSystemTrust) snapshot(ctx context.Context, request Request) (ubuntuTrustSnapshot, error) {
	if err := validateUbuntuTrustRequest(request); err != nil {
		return ubuntuTrustSnapshot{}, err
	}
	if err := ctx.Err(); err != nil {
		return ubuntuTrustSnapshot{}, err
	}
	marker, err := readLinuxTrustFile(filepath.Join(ubuntuTrustMarkerDirectory, ubuntuTrustMarkerName), maximumNativeIDLength, ubuntuTrustMarkerMode)
	if err != nil {
		return ubuntuTrustSnapshot{}, fmt.Errorf("observe Ubuntu trust owner marker: %w", err)
	}
	source, err := readLinuxTrustFile(filepath.Join(ubuntuTrustSourceDirectory, ubuntuTrustSourceName), maximumCertificatePEMBytes, 0o644)
	if err != nil {
		return ubuntuTrustSnapshot{}, fmt.Errorf("observe Ubuntu trust source: %w", err)
	}
	pending, err := readLinuxTrustFile(filepath.Join(ubuntuTrustSourceDirectory, ubuntuTrustPendingName), maximumCertificatePEMBytes, 0o600)
	if err != nil {
		return ubuntuTrustSnapshot{}, fmt.Errorf("observe Ubuntu trust recovery source: %w", err)
	}
	bundle, err := readLinuxTrustFile(ubuntuTrustBundlePath, maximumUbuntuTrustBundle, 0o644)
	if err != nil {
		return ubuntuTrustSnapshot{}, fmt.Errorf("observe Ubuntu trust bundle: %w", err)
	}
	if !bundle.Present || !bundle.Exact {
		return ubuntuTrustSnapshot{}, errors.New("Ubuntu trust bundle is absent or unsafe")
	}
	activeMatches, err := countUbuntuBundleMatches(bundle.Content, request.AuthorityFingerprint())
	if err != nil {
		return ubuntuTrustSnapshot{}, err
	}
	snapshot := ubuntuTrustSnapshot{
		MarkerPresent:  marker.Present,
		MarkerExact:    marker.Exact,
		SourcePresent:  source.Present,
		SourceExact:    source.Exact && bytes.Equal(source.Content, request.Root().CertificatePEM),
		PendingPresent: pending.Present,
		PendingExact:   pending.Exact && bytes.Equal(pending.Content, request.Root().CertificatePEM),
		ActiveMatches:  activeMatches,
	}
	if marker.Present {
		parsed, ok := parseUbuntuTrustOwner(string(marker.Content))
		if ok {
			snapshot.Marker = &parsed
			snapshot.MarkerExact = snapshot.MarkerExact && parsed == request.OwnerMarker()
		} else {
			snapshot.MarkerExact = false
		}
	}
	if source.Present {
		snapshot.SourceFingerprint = ubuntuCertificateFingerprint(source.Content)
	}
	if pending.Present {
		snapshot.PendingFingerprint = ubuntuCertificateFingerprint(pending.Content)
	}
	return snapshot, nil
}

// ensure installs a new fixed source or repairs an interrupted bundle refresh.
func (store ubuntuSystemTrust) ensure(ctx context.Context, request Request) error {
	if err := validateUbuntuTrustRequest(request); err != nil {
		return err
	}
	snapshot, err := store.snapshot(ctx, request)
	if err != nil {
		return err
	}
	if snapshot.PendingPresent {
		return fmt.Errorf("Ubuntu trust recovery source exists during ensure: %w", errNativeMutationConflict)
	}
	createdMarker := false
	createdSource := false
	switch {
	case !snapshot.MarkerPresent && !snapshot.SourcePresent:
		if err := createLinuxTrustFile(ubuntuTrustMarkerDirectory, ubuntuTrustMarkerName, []byte(ubuntuTrustOwnerText(request)), ubuntuTrustMarkerMode); err != nil {
			return fmt.Errorf("create Ubuntu trust owner marker: %w", err)
		}
		createdMarker = true
		if err := createLinuxTrustFile(ubuntuTrustSourceDirectory, ubuntuTrustSourceName, request.Root().CertificatePEM, 0o644); err != nil {
			rollbackErr := removeExactLinuxTrustFile(ubuntuTrustMarkerDirectory, ubuntuTrustMarkerName, []byte(ubuntuTrustOwnerText(request)), ubuntuTrustMarkerMode)
			return errors.Join(fmt.Errorf("create Ubuntu trust source: %w", err), rollbackErr)
		}
		createdSource = true
	case snapshot.Marker != nil && snapshot.MarkerExact && !snapshot.SourcePresent:
		if err := createLinuxTrustFile(ubuntuTrustSourceDirectory, ubuntuTrustSourceName, request.Root().CertificatePEM, 0o644); err != nil {
			return fmt.Errorf("recover Ubuntu trust source: %w", err)
		}
		createdSource = true
	case snapshot.Marker != nil && snapshot.MarkerExact && snapshot.SourcePresent && snapshot.SourceExact:
	default:
		return fmt.Errorf("Ubuntu trust fixed paths changed before ensure: %w", errNativeMutationConflict)
	}
	if err := runUbuntuTrustUpdate(ctx); err != nil {
		var rollbackErr error
		if createdSource {
			rollbackErr = removeExactLinuxTrustFile(ubuntuTrustSourceDirectory, ubuntuTrustSourceName, request.Root().CertificatePEM, 0o644)
		}
		if rollbackErr == nil && createdMarker {
			rollbackErr = errors.Join(rollbackErr, removeExactLinuxTrustFile(ubuntuTrustMarkerDirectory, ubuntuTrustMarkerName, []byte(ubuntuTrustOwnerText(request)), ubuntuTrustMarkerMode))
		}
		rollbackErr = errors.Join(rollbackErr, runUbuntuTrustUpdate(context.Background()))
		return errors.Join(err, rollbackErr)
	}
	return nil
}

// release retires the source before refreshing the bundle and preserves a recoverable owner marker until success.
func (store ubuntuSystemTrust) release(ctx context.Context, request Request) error {
	if err := validateUbuntuTrustRequest(request); err != nil {
		return err
	}
	snapshot, err := store.snapshot(ctx, request)
	if err != nil {
		return err
	}
	if snapshot.Marker == nil || !snapshot.MarkerExact {
		return fmt.Errorf("Ubuntu trust owner marker changed before release: %w", errNativeMutationConflict)
	}
	switch {
	case snapshot.SourcePresent && snapshot.SourceExact && !snapshot.PendingPresent:
		if err := renameLinuxTrustFile(ubuntuTrustSourceDirectory, ubuntuTrustSourceName, ubuntuTrustPendingName, 0o600); err != nil {
			return fmt.Errorf("retire Ubuntu trust source: %w", err)
		}
	case !snapshot.SourcePresent && snapshot.PendingPresent && snapshot.PendingExact:
	case !snapshot.SourcePresent && !snapshot.PendingPresent && snapshot.ActiveMatches == 0:
		return removeExactLinuxTrustFile(ubuntuTrustMarkerDirectory, ubuntuTrustMarkerName, []byte(ubuntuTrustOwnerText(request)), ubuntuTrustMarkerMode)
	default:
		return fmt.Errorf("Ubuntu trust fixed paths changed before release: %w", errNativeMutationConflict)
	}
	if err := runUbuntuTrustUpdate(ctx); err != nil {
		restoreErr := renameLinuxTrustFile(ubuntuTrustSourceDirectory, ubuntuTrustPendingName, ubuntuTrustSourceName, 0o644)
		restoreErr = errors.Join(restoreErr, runUbuntuTrustUpdate(context.Background()))
		return errors.Join(err, restoreErr)
	}
	if err := removeExactLinuxTrustFile(ubuntuTrustSourceDirectory, ubuntuTrustPendingName, request.Root().CertificatePEM, 0o600); err != nil {
		return err
	}
	return removeExactLinuxTrustFile(ubuntuTrustMarkerDirectory, ubuntuTrustMarkerName, []byte(ubuntuTrustOwnerText(request)), ubuntuTrustMarkerMode)
}

// readLinuxTrustFile reads one fixed regular file without following its final component.
func readLinuxTrustFile(path string, maximum int64, mode os.FileMode) (linuxTrustFile, error) {
	descriptor, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if errors.Is(err, unix.ENOENT) {
		return linuxTrustFile{}, nil
	}
	if err != nil {
		return linuxTrustFile{}, err
	}
	file := os.NewFile(uintptr(descriptor), path)
	info, statErr := file.Stat()
	content, readErr := io.ReadAll(io.LimitReader(file, maximum+1))
	closeErr := file.Close()
	if statErr != nil || readErr != nil || closeErr != nil {
		return linuxTrustFile{}, errors.Join(statErr, readErr, closeErr)
	}
	status, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || status.Nlink != 1 || int64(len(content)) > maximum {
		return linuxTrustFile{}, errors.New("Ubuntu trust path is not one bounded regular file")
	}
	return linuxTrustFile{
		Present: true,
		Exact:   status.Uid == 0 && status.Gid == 0 && info.Mode().Perm() == mode,
		Content: content,
	}, nil
}

// createLinuxTrustFile creates one absent fixed file through a retained secure parent directory.
func createLinuxTrustFile(directoryPath string, name string, content []byte, mode uint32) error {
	parent, err := openLinuxTrustDirectory(directoryPath)
	if err != nil {
		return err
	}
	defer parent.Close()
	descriptor, err := unix.Openat(int(parent.Fd()), name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, mode)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(descriptor), filepath.Join(directoryPath, name))
	_, writeErr := file.Write(content)
	ownerErr := unix.Fchown(int(file.Fd()), 0, 0)
	modeErr := unix.Fchmod(int(file.Fd()), mode)
	syncErr := file.Sync()
	closeErr := file.Close()
	if err := errors.Join(writeErr, ownerErr, modeErr, syncErr, closeErr); err != nil {
		_ = unix.Unlinkat(int(parent.Fd()), name, 0)
		return err
	}
	return parent.Sync()
}

// removeExactLinuxTrustFile removes only one exact no-follow fixed regular file and synchronizes its parent.
func removeExactLinuxTrustFile(directoryPath string, name string, expected []byte, mode os.FileMode) error {
	parent, err := openLinuxTrustDirectory(directoryPath)
	if err != nil {
		return err
	}
	defer parent.Close()
	file, err := readLinuxTrustFile(filepath.Join(directoryPath, name), int64(len(expected)), mode)
	if err != nil {
		return err
	}
	if !file.Present {
		return nil
	}
	if !file.Exact || !bytes.Equal(file.Content, expected) {
		return fmt.Errorf("Ubuntu trust path %q changed before removal: %w", name, errNativeMutationConflict)
	}
	if err := unix.Unlinkat(int(parent.Fd()), name, 0); err != nil {
		return err
	}
	return parent.Sync()
}

// renameLinuxTrustFile atomically moves one exact fixed regular file inside the local-CA directory.
func renameLinuxTrustFile(directoryPath string, oldName string, newName string, mode uint32) error {
	parent, err := openLinuxTrustDirectory(directoryPath)
	if err != nil {
		return err
	}
	defer parent.Close()
	if err := unix.Fstatat(int(parent.Fd()), newName, &unix.Stat_t{}, unix.AT_SYMLINK_NOFOLLOW); err == nil {
		return fmt.Errorf("destination %q already exists", newName)
	} else if !errors.Is(err, unix.ENOENT) {
		return err
	}
	if err := unix.Renameat(int(parent.Fd()), oldName, int(parent.Fd()), newName); err != nil {
		return err
	}
	descriptor, err := unix.Openat(int(parent.Fd()), newName, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	modeErr := unix.Fchmod(descriptor, mode)
	closeErr := unix.Close(descriptor)
	return errors.Join(modeErr, closeErr, parent.Sync())
}

// openLinuxTrustDirectory retains one root-owned non-writable fixed parent.
func openLinuxTrustDirectory(path string) (*os.File, error) {
	descriptor, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	parent := os.NewFile(uintptr(descriptor), path)
	info, err := parent.Stat()
	if err != nil {
		_ = parent.Close()
		return nil, err
	}
	status, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || status.Uid != 0 || status.Gid != 0 || info.Mode().Perm()&0o022 != 0 {
		_ = parent.Close()
		return nil, errors.New("Ubuntu trust parent is not root-owned and protected")
	}
	return parent, nil
}

// countUbuntuBundleMatches parses the bounded certificate bundle and counts the requested DER fingerprint.
func countUbuntuBundleMatches(content []byte, fingerprint string) (int, error) {
	matches := 0
	remaining := content
	for len(bytes.TrimSpace(remaining)) > 0 {
		block, rest := pem.Decode(remaining)
		if block == nil || block.Type != "CERTIFICATE" || len(block.Headers) != 0 {
			return 0, errors.New("Ubuntu trust bundle contains malformed certificate data")
		}
		digest := sha256.Sum256(block.Bytes)
		if hex.EncodeToString(digest[:]) == fingerprint {
			matches++
			if matches > maximumUbuntuActiveRootMatches {
				return 0, fmt.Errorf("Ubuntu trust bundle exceeds matching-root limit %d", maximumUbuntuActiveRootMatches)
			}
		}
		remaining = rest
	}
	return matches, nil
}

// ubuntuCertificateFingerprint returns the canonical DER digest or a safe raw-content digest for malformed fixed state.
func ubuntuCertificateFingerprint(content []byte) string {
	block, rest := pem.Decode(content)
	if block != nil && block.Type == "CERTIFICATE" && len(block.Headers) == 0 && len(bytes.TrimSpace(rest)) == 0 {
		digest := sha256.Sum256(block.Bytes)
		return hex.EncodeToString(digest[:])
	}
	digest := sha256.Sum256(append([]byte("goforj.harbor.ubuntu-malformed-root.v1\x00"), content...))
	return hex.EncodeToString(digest[:])
}

// runUbuntuTrustUpdate executes only Ubuntu's fixed root-owned system-bundle refresh command.
func runUbuntuTrustUpdate(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	commandContext, cancel := context.WithTimeout(ctx, ubuntuTrustCommandTimeout)
	defer cancel()
	executable, err := openUbuntuTrustUpdateCommand()
	if err != nil {
		return err
	}
	outputReader, outputWriter, err := os.Pipe()
	if err != nil {
		_ = executable.Close()
		return err
	}
	process, err := os.StartProcess(ubuntuTrustExecutableFD, []string{"update-ca-certificates"}, &os.ProcAttr{
		Dir: "/",
		Env: []string{
			"LANG=C",
			"LC_ALL=C",
			"PATH=/usr/sbin:/usr/bin:/sbin:/bin",
		},
		Files: []*os.File{nil, outputWriter, outputWriter, executable},
	})
	closeErr := errors.Join(outputWriter.Close(), executable.Close())
	if err != nil {
		_ = outputReader.Close()
		return errors.Join(err, closeErr)
	}
	output := &ubuntuLimitedBuffer{maximum: maximumUbuntuCommandOutput}
	readResult := make(chan error, 1)
	go func() {
		_, copyErr := io.Copy(output, outputReader)
		readResult <- errors.Join(copyErr, outputReader.Close())
	}()
	waitResult := make(chan ubuntuTrustProcessResult, 1)
	go func() {
		state, waitErr := process.Wait()
		waitResult <- ubuntuTrustProcessResult{state: state, err: waitErr}
	}()
	var result ubuntuTrustProcessResult
	select {
	case result = <-waitResult:
	case <-commandContext.Done():
		killErr := process.Kill()
		result = <-waitResult
		result.err = errors.Join(commandContext.Err(), killErr, result.err)
	}
	readErr := <-readResult
	runErr := errors.Join(closeErr, readErr, result.err)
	if runErr == nil && (result.state == nil || !result.state.Success()) {
		if result.state == nil {
			runErr = errors.New("Ubuntu trust refresh exited without process state")
		} else {
			runErr = fmt.Errorf("Ubuntu trust refresh exited with %s", result.state.String())
		}
	}
	if runErr != nil {
		return fmt.Errorf("refresh Ubuntu system trust: %w: %s", runErr, output.String())
	}
	return nil
}

// openUbuntuTrustUpdateCommand retains the reviewed executable inode across process launch.
func openUbuntuTrustUpdateCommand() (*os.File, error) {
	descriptor, err := unix.Open(ubuntuTrustUpdateCommand, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(descriptor), ubuntuTrustUpdateCommand)
	var status unix.Stat_t
	if err := unix.Fstat(descriptor, &status); err != nil {
		return nil, errors.Join(err, file.Close())
	}
	if status.Mode&unix.S_IFMT != unix.S_IFREG || status.Uid != 0 || status.Gid != 0 || status.Mode&0o022 != 0 {
		return nil, errors.Join(errors.New("Ubuntu trust update command is not root-owned and protected"), file.Close())
	}
	return file, nil
}

// ubuntuTrustProcessResult retains the narrow completion evidence needed by the fixed runner.
type ubuntuTrustProcessResult struct {
	state *os.ProcessState
	err   error
}

// ubuntuLimitedBuffer bounds diagnostic output from the fixed trust refresh command.
type ubuntuLimitedBuffer struct {
	data    []byte
	maximum int
}

// Write retains only the configured bounded prefix while allowing the child to drain.
func (buffer *ubuntuLimitedBuffer) Write(content []byte) (int, error) {
	remaining := buffer.maximum - len(buffer.data)
	if remaining > 0 {
		buffer.data = append(buffer.data, content[:min(remaining, len(content))]...)
	}
	return len(content), nil
}

// String returns the bounded diagnostic output.
func (buffer *ubuntuLimitedBuffer) String() string {
	return string(bytes.TrimSpace(buffer.data))
}
