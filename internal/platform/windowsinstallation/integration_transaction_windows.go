//go:build windows

package windowsinstallation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

const integrationReceiptName = "installation-integration.json"
const integrationReceiptSDDL = "O:BAD:P(A;;FA;;;BA)(A;;FA;;;SY)(A;;0x1200a9;;;BU)"

// integrationReceipt commits the exact machine projections only after every creation has succeeded.
type integrationReceipt struct {
	SchemaVersion  int                        `json:"schema_version"`
	InstallationID string                     `json:"installation_id"`
	UserSID        string                     `json:"user_sid"`
	BundleDigest   string                     `json:"bundle_digest"`
	Spool          []integrationObject        `json:"spool"`
	Shortcut       integrationShortcutReceipt `json:"shortcut"`
}

// machineIntegration keeps projection authority and helper storage locks alive through the complete removal.
type machineIntegration struct {
	Spool    *privilegedSpool
	Shortcut *integrationShortcut
	Registry *integrationRegistry
	Receipt  *os.File
	State    *integrationState
}

// InstallMachineIntegration creates the fixed machine projections under the caller's full machine transaction lease.
func (installation *Installation) InstallMachineIntegration(ctx context.Context) (result error) {
	if err := installation.requireIntegrationAuthority(ctx); err != nil {
		return err
	}
	receiptPath := filepath.Join(installation.root, integrationReceiptName)
	if _, err := os.Lstat(receiptPath); err == nil {
		return installation.ValidateMachineIntegration(ctx)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	programData, err := windows.KnownFolderPath(windows.FOLDERID_ProgramData, windows.KF_FLAG_DEFAULT)
	if err != nil {
		return err
	}
	spec, err := integrationShortcutPlan(installation)
	if err != nil {
		return err
	}
	values, err := integrationRegistryValues(installation)
	if err != nil {
		return err
	}
	spool, err := preparePrivilegedSpool(programData, installation.selection.UserSID)
	if err != nil {
		return fmt.Errorf("install helper spool: %w", err)
	}
	owned := &machineIntegration{Spool: spool}
	committed := false
	defer func() {
		if result != nil && !committed {
			if owned.Registry != nil {
				result = errors.Join(result, owned.Registry.retire())
			}
			if owned.Shortcut != nil {
				result = errors.Join(result, owned.Shortcut.retire())
			}
			result = errors.Join(result, spool.rollback())
		}
		result = errors.Join(result, owned.Close())
	}()
	owned.Registry, err = integrationCreateRegistry(values)
	if err != nil {
		return fmt.Errorf("install uninstall registration: %w", err)
	}
	owned.Shortcut, err = integrationCreateShortcut(spec)
	if err != nil {
		return fmt.Errorf("install Start Menu shortcut: %w", err)
	}
	receipt := integrationReceipt{SchemaVersion: 1, InstallationID: installation.selection.InstallationID, UserSID: installation.selection.UserSID, BundleDigest: installation.selection.BundleDigest, Shortcut: owned.Shortcut.Receipt}
	for _, directory := range spool.Directories {
		receipt.Spool = append(receipt.Spool, directory.Object)
	}
	data, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	committed, err = commitIntegrationReceipt(receiptPath, data)
	return err
}

// ValidateMachineIntegration reads the exact protected receipt and projections without repairing or changing machine state.
func (installation *Installation) ValidateMachineIntegration(ctx context.Context) error {
	if err := installation.requireIntegrationAuthority(ctx); err != nil {
		return err
	}
	owned, err := installation.admitMachineIntegration(false)
	if err != nil {
		return err
	}
	return owned.Close()
}

// RemoveMachineIntegration retires admitted machine projections while the caller holds machine and stopped-runtime leases.
func (installation *Installation) RemoveMachineIntegration(ctx context.Context, check func() error) (result error) {
	if err := installation.requireIntegrationAuthority(ctx); err != nil {
		return err
	}
	owned, err := installation.admitMachineIntegration(true)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, owned.Close()) }()
	guarded := func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		return check()
	}
	if err := guarded(); err != nil {
		return err
	}
	if err := owned.Registry.retire(); err != nil {
		return err
	}
	if err := owned.Shortcut.retireChecked(guarded); err != nil {
		return err
	}
	if err := owned.State.retireChecked(guarded); err != nil {
		return err
	}
	if err := owned.State.Close(); err != nil {
		return err
	}
	owned.State = nil
	if err := owned.Spool.retireChecked(guarded); err != nil {
		return err
	}
	if err := guarded(); err != nil {
		return err
	}
	return retireIntegrationFile(owned.Receipt)
}

// requireIntegrationAuthority refuses use after admission closes or outside the selected same-user elevated installer session.
func (installation *Installation) requireIntegrationAuthority(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := installation.Path("desktop-launcher"); err != nil {
		return err
	}
	if err := requireInstallerAuthority(); err != nil {
		return err
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	if user.User.Sid.String() != installation.selection.UserSID {
		return errors.New("machine integration belongs to another interactive account")
	}
	return nil
}

// admitMachineIntegration retains every exact projection before granting repair or removal authority.
func (installation *Installation) admitMachineIntegration(removal bool) (_ *machineIntegration, result error) {
	owned := &machineIntegration{}
	defer func() {
		if result != nil {
			result = errors.Join(result, owned.Close())
		}
	}()
	file, err := openIntegrationStateFile(filepath.Join(installation.root, integrationReceiptName), integrationReceiptSDDL, windows.FILE_SHARE_READ)
	if err != nil {
		return nil, err
	}
	owned.Receipt = file
	data, err := readIntegrationState(file, MetadataLimit)
	if err != nil {
		return nil, err
	}
	var receipt integrationReceipt
	if err := Decode(bytes.NewReader(data), &receipt); err != nil {
		return nil, err
	}
	if receipt.SchemaVersion != 1 || receipt.InstallationID != installation.selection.InstallationID || receipt.UserSID != installation.selection.UserSID || receipt.BundleDigest != installation.selection.BundleDigest {
		return nil, errors.New("machine integration receipt differs from selected installation")
	}
	programData, err := windows.KnownFolderPath(windows.FOLDERID_ProgramData, windows.KF_FLAG_DEFAULT)
	if err != nil {
		return nil, err
	}
	owned.Spool, err = admitPrivilegedSpool(programData, receipt.UserSID, receipt.Spool)
	if err != nil {
		return nil, err
	}
	spec, err := integrationShortcutPlan(installation)
	if err != nil {
		return nil, err
	}
	owned.Shortcut, err = integrationAdmitShortcut(spec, receipt.Shortcut)
	if err != nil {
		return nil, err
	}
	values, err := integrationRegistryValues(installation)
	if err != nil {
		return nil, err
	}
	owned.Registry, err = integrationAdmitRegistry(values)
	if err != nil {
		return nil, err
	}
	if removal {
		owned.State, err = admitIntegrationState(owned.Spool, receipt.UserSID)
		if err != nil {
			return nil, err
		}
	}
	return owned, nil
}

// retire removes owned empty directories deepest first while preserving shared foreign neighbors.
func (spool *privilegedSpool) retire() error {
	return spool.retireChecked(func() error { return nil })
}

// retireChecked rechecks the required runtime lease before each directory retirement request.
func (spool *privilegedSpool) retireChecked(check func() error) error {
	for index := len(spool.Directories) - 1; index >= 0; index-- {
		directory := &spool.Directories[index]
		if !directory.Object.Created {
			continue
		}
		if err := check(); err != nil {
			return err
		}
		err := retireIntegrationFile(directory.File)
		if index < 2 && errors.Is(err, windows.ERROR_DIR_NOT_EMPTY) {
			continue
		}
		if err != nil {
			return err
		}
		if err := directory.File.Close(); err != nil {
			return err
		}
		directory.File = nil
	}
	return nil
}

// Close releases optional successfully acquired projections after a failed or complete transaction.
func (owned *machineIntegration) Close() error {
	var result error
	if owned.State != nil {
		result = errors.Join(result, owned.State.Close())
		owned.State = nil
	}
	if owned.Registry != nil {
		result = errors.Join(result, owned.Registry.Close())
		owned.Registry = nil
	}
	if owned.Shortcut != nil {
		result = errors.Join(result, owned.Shortcut.Close())
		owned.Shortcut = nil
	}
	if owned.Spool != nil {
		result = errors.Join(result, owned.Spool.Close())
		owned.Spool = nil
	}
	if owned.Receipt != nil {
		result = errors.Join(result, owned.Receipt.Close())
		owned.Receipt = nil
	}
	return result
}

// commitIntegrationReceipt removes a failed newly created document through its exact retained handle.
func commitIntegrationReceipt(path string, data []byte) (committed bool, result error) {
	descriptor, err := windows.SecurityDescriptorFromString(integrationReceiptSDDL)
	if err != nil {
		return false, err
	}
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return false, err
	}
	attributes := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: descriptor}
	handle, err := windows.CreateFile(pointer, windows.GENERIC_READ|windows.GENERIC_WRITE|windows.READ_CONTROL|windows.DELETE, windows.FILE_SHARE_READ, &attributes, windows.CREATE_NEW, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return false, err
	}
	file := os.NewFile(uintptr(handle), path)
	defer func() {
		if !committed {
			result = errors.Join(result, retireIntegrationFile(file))
		}
		result = errors.Join(result, file.Close())
	}()
	if _, err := io.Copy(file, bytes.NewReader(data)); err != nil {
		return false, err
	}
	if err := file.Sync(); err != nil {
		return false, err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return false, err
	}
	observed, err := readIntegrationState(file, MetadataLimit)
	if err != nil {
		return false, err
	}
	if !bytes.Equal(observed, data) {
		return false, errors.New("machine integration receipt did not commit exact bytes")
	}
	if _, err := inspectIntegrationObject(file, integrationObject{Path: path, SDDL: integrationReceiptSDDL}); err != nil {
		return false, err
	}
	return true, nil
}
