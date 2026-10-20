//go:build windows

package windowsinstallation

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unsafe"

	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/helper/ticketauth"
	"github.com/goforj/harbor/internal/host/ownershipreleaseproof"
	"golang.org/x/sys/windows"
)

// integrationState retains only admitted helper artifacts while all machine storage locks exclude future writers.
type integrationState struct {
	Files []*os.File
}

// integrationReplayRecord mirrors the canonical version-one helper replay format for retirement admission.
type integrationReplayRecord struct {
	Version             uint16 `json:"version"`
	InstallationID      string `json:"installation_id"`
	OwnershipGeneration uint64 `json:"ownership_generation"`
	Nonce               string `json:"nonce"`
	ExpiresAt           string `json:"expires_at"`
}

// admitIntegrationState refuses active ownership, pending requests, unknown entries, or an incomplete release history.
func admitIntegrationState(spool *privilegedSpool, userSID string) (_ *integrationState, result error) {
	state := &integrationState{}
	defer func() {
		if result != nil {
			result = errors.Join(result, state.Close())
		}
	}()
	root := spool.Directories[2].Object.Path
	machine := "O:BAD:P(A;;FA;;;BA)(A;;FA;;;SY)"
	proofPolicy := machine + "(A;;0x120089;;;" + userSID + ")"
	// Retain every persistent storage lock with no sharing before inspecting ownership absence or helper history.
	for _, relative := range []string{"ownership-release-proof.lock", `state\ownership.json.lock`, `state\host-projection.json.lock`} {
		file, err := openIntegrationStateFile(filepath.Join(root, relative), machine, 0)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		state.Files = append(state.Files, file)
		if data, err := readIntegrationState(file, 0); err != nil || len(data) != 0 {
			return nil, errors.Join(errors.New("helper lock contains foreign data"), err)
		}
	}
	history := false
	released := false
	for index := 2; index < len(spool.Directories); index++ {
		directory := &spool.Directories[index]
		entries, err := integrationDirectoryEntries(directory)
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			path := filepath.Join(directory.Object.Path, entry.Name())
			expectedDirectory := false
			for _, known := range spool.Directories[index+1:] {
				if path == known.Object.Path {
					expectedDirectory = true
					break
				}
			}
			if expectedDirectory {
				if !entry.IsDir() {
					return nil, errors.New("helper directory type changed")
				}
				continue
			}
			if entry.IsDir() {
				return nil, fmt.Errorf("preserve unrecognized helper directory %s", path)
			}
			relative, err := filepath.Rel(root, path)
			if err != nil {
				return nil, err
			}
			switch relative {
			case "ownership-release-proof.lock", `state\ownership.json.lock`, `state\host-projection.json.lock`:
				// The exact file is already retained without sharing, so no helper can replace or acquire it.
				found := false
				for _, file := range state.Files {
					if file.Name() == path {
						found = true
						break
					}
				}
				if !found {
					return nil, errors.New("helper lock appeared after lock admission")
				}
				continue
			case "ownership-release-proof.json":
				file, err := openIntegrationStateFile(path, proofPolicy, windows.FILE_SHARE_READ)
				if err != nil {
					return nil, err
				}
				state.Files = append(state.Files, file)
				content, err := readIntegrationState(file, 16*1024)
				if err != nil {
					return nil, err
				}
				var proof ownershipreleaseproof.Proof
				if err := Decode(bytes.NewReader(content), &proof); err != nil {
					return nil, err
				}
				if _, err := proof.Fingerprint(); err != nil {
					return nil, err
				}
				if proof.State != ownershipreleaseproof.StateReleased || proof.RequesterIdentity != userSID {
					return nil, errors.New("helper ownership release is incomplete or belongs to another account")
				}
				released = true
			default:
				if index != 5 && index != 7 {
					return nil, fmt.Errorf("preserve unrecognized or active helper state %s", relative)
				}
				if index == 5 {
					if err := helper.TicketReference(entry.Name()).Validate(); err != nil {
						return nil, err
					}
				} else if !strings.HasSuffix(entry.Name(), ".json") || !digestPattern.MatchString(strings.TrimSuffix(entry.Name(), ".json")) {
					return nil, errors.New("preserve unrecognized replay tombstone")
				}
				file, err := openIntegrationStateFile(path, machine, windows.FILE_SHARE_READ)
				if err != nil {
					return nil, err
				}
				state.Files = append(state.Files, file)
				maximum := int64(ticketauth.MaxEnvelopeBytes)
				if index == 7 {
					maximum = 4096
				}
				content, err := readIntegrationState(file, maximum)
				if err != nil {
					return nil, err
				}
				// Claims remain consumed even when ticket authentication failed; their protected canonical reference is the ownership boundary.
				if index == 7 {
					if err := admitIntegrationReplay(entry.Name(), content); err != nil {
						return nil, err
					}
				}
				history = true
			}
		}
	}
	if history && !released {
		return nil, errors.New("helper history has no complete ownership release proof")
	}
	return state, nil
}

// integrationDirectoryEntries bounds enumeration and never descends into an unrecognized directory.
func integrationDirectoryEntries(directory *integrationDirectory) ([]os.DirEntry, error) {
	var result []os.DirEntry
	for {
		entries, err := directory.File.ReadDir(256)
		result = append(result, entries...)
		if len(result) > 100000 {
			return nil, errors.New("helper storage exceeds bounded uninstall inventory")
		}
		if errors.Is(err, io.EOF) {
			return result, nil
		}
		if err != nil {
			return nil, err
		}
	}
}

// openIntegrationStateFile admits one direct protected machine file before retaining deletion authority.
func openIntegrationStateFile(path, policy string, sharing uint32) (*os.File, error) {
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(pointer, windows.GENERIC_READ|windows.READ_CONTROL|windows.DELETE, sharing, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, &os.PathError{Op: "admit helper state", Path: path, Err: err}
	}
	file := os.NewFile(uintptr(handle), path)
	fail := func(err error) (*os.File, error) { return nil, errors.Join(err, file.Close()) }
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		return fail(err)
	}
	if info.FileAttributes&(windows.FILE_ATTRIBUTE_REPARSE_POINT|windows.FILE_ATTRIBUTE_DIRECTORY) != 0 || info.NumberOfLinks != 1 {
		return fail(errors.New("helper state has a reparse, type, or hard-link conflict"))
	}
	final, err := finalIntegrationPath(handle)
	if err != nil || !strings.EqualFold(final, `\\?\`+path) {
		return fail(errors.Join(errors.New("helper state native path differs"), err))
	}
	if _, err := inspectIntegrationObject(file, integrationObject{Path: path, SDDL: policy}); err != nil {
		return fail(err)
	}
	return file, nil
}

// readIntegrationState rejects oversize or changing records before bounded decoding.
func readIntegrationState(file *os.File, maximum int64) ([]byte, error) {
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() < 0 || info.Size() > maximum {
		return nil, errors.New("helper state exceeds its fixed record bound")
	}
	content, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil || int64(len(content)) != info.Size() {
		return nil, errors.Join(errors.New("helper state size changed"), err)
	}
	return content, nil
}

// admitIntegrationReplay verifies canonical helper format and its domain-separated filename before retiring a tombstone.
func admitIntegrationReplay(name string, content []byte) error {
	var record integrationReplayRecord
	if err := Decode(bytes.NewReader(bytes.TrimSuffix(content, []byte{'\n'})), &record); err != nil {
		return err
	}
	canonical, err := json.Marshal(record)
	if err != nil {
		return err
	}
	if !bytes.Equal(content, append(canonical, '\n')) {
		return errors.New("helper replay record is not canonical")
	}
	key := helper.ReplayKey{InstallationID: record.InstallationID, OwnershipGeneration: record.OwnershipGeneration, Nonce: record.Nonce}
	if err := key.Validate(); err != nil {
		return err
	}
	expires, err := time.Parse(time.RFC3339Nano, record.ExpiresAt)
	if record.Version != 1 || err != nil || expires.Location() != time.UTC || expires.Format(time.RFC3339Nano) != record.ExpiresAt {
		return errors.New("helper replay record has an unknown version or expiry")
	}
	hash := sha256.New()
	hash.Write([]byte("goforj.harbor/helper-replay:v1\x00"))
	fmt.Fprintf(hash, "%d:%s\x00%d:%s\x00", len(key.InstallationID), key.InstallationID, key.OwnershipGeneration, key.Nonce)
	if hex.EncodeToString(hash.Sum(nil))+".json" != name {
		return errors.New("helper replay filename differs from its claim")
	}
	return nil
}

// retire removes only already admitted exact file handles after all histories have passed validation.
func (state *integrationState) retire() error {
	return state.retireChecked(func() error { return nil })
}

// retireChecked refuses each next helper artifact as soon as its required runtime lease is lost.
func (state *integrationState) retireChecked(check func() error) error {
	for _, file := range state.Files {
		if err := check(); err != nil {
			return err
		}
		if err := retireIntegrationFile(file); err != nil {
			return err
		}
	}
	return nil
}

// Close releases storage locks only after the complete machine-removal operation settles.
func (state *integrationState) Close() error {
	var result error
	for index := len(state.Files) - 1; index >= 0; index-- {
		result = errors.Join(result, state.Files[index].Close())
	}
	state.Files = nil
	return result
}

// integrationIdentity checks the complete identity without widening machine-store admission.
func integrationIdentity(file *os.File) (integrationFileID, error) {
	var identity integrationFileID
	err := windows.GetFileInformationByHandleEx(windows.Handle(file.Fd()), windows.FileIdInfo, (*byte)(unsafe.Pointer(&identity)), uint32(unsafe.Sizeof(identity)))
	return identity, err
}
