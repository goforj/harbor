//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unsafe"

	"github.com/goforj/harbor/internal/control"
	"github.com/goforj/harbor/internal/platform/windowsinstallation"
	"github.com/goforj/harbor/internal/rpc"
	"golang.org/x/sys/windows"
)

// observeInstalledDaemon binds the authenticated pipe peer to the selected signed installed daemon.
func observeInstalledDaemon(ctx context.Context, peer control.DaemonPeer) (daemonIdentity, error) {
	if err := ctx.Err(); err != nil {
		return daemonIdentity{}, err
	}
	installation, err := windowsinstallation.Admit()
	if err != nil {
		return daemonIdentity{}, err
	}
	defer installation.Close()
	process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, peer.Transport.ProcessID)
	if err != nil {
		return daemonIdentity{}, err
	}
	defer windows.CloseHandle(process)
	return identifyInstalledDaemon(process, installation, peer)
}

// identifyInstalledDaemon reads native birth, token and image from one retained handle while the signed installation is retained.
func identifyInstalledDaemon(process windows.Handle, installation *windowsinstallation.Installation, peer control.DaemonPeer) (daemonIdentity, error) {
	identity, err := nativeDaemonIdentity(process, peer.Transport.ProcessID)
	if err != nil {
		return daemonIdentity{}, err
	}
	image, err := installation.Path("daemon")
	if err != nil {
		return daemonIdentity{}, err
	}
	current, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return daemonIdentity{}, err
	}
	if err := admitDaemonIdentity(identity, peer, image, installation.Selection().UserSID, current.User.Sid.String()); err != nil {
		return daemonIdentity{}, err
	}
	return identity, nil
}

// nativeDaemonIdentity refuses exited or elevated processes and obtains native identity without PID-based signaling.
func nativeDaemonIdentity(process windows.Handle, pid uint32) (daemonIdentity, error) {
	identity := daemonIdentity{PID: pid}
	nativePID, err := windows.GetProcessId(process)
	if err != nil {
		return identity, err
	}
	if nativePID != pid {
		return identity, errors.New("retained process handle differs from authenticated PID")
	}
	state, err := windows.WaitForSingleObject(process, 0)
	if err != nil {
		return identity, err
	}
	if state != uint32(windows.WAIT_TIMEOUT) {
		return identity, errors.New("installed daemon is not live")
	}
	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(process, &created, &exited, &kernel, &user); err != nil {
		return identity, err
	}
	identity.Birth = strconv.FormatUint(uint64(created.HighDateTime)<<32|uint64(created.LowDateTime), 10)
	image := make([]uint16, 32768)
	size := uint32(len(image))
	if err := windows.QueryFullProcessImageName(process, 0, &image[0], &size); err != nil {
		return identity, err
	}
	identity.Executable = windows.UTF16ToString(image[:size])
	var token windows.Token
	if err := windows.OpenProcessToken(process, windows.TOKEN_QUERY, &token); err != nil {
		return identity, err
	}
	defer token.Close()
	owner, err := token.GetTokenUser()
	if err != nil {
		return identity, err
	}
	identity.UserID = owner.User.Sid.String()
	if token.IsElevated() {
		return identity, errors.New("installed daemon is elevated")
	}
	var daemonSession, currentSession uint32
	var length uint32
	if err := windows.GetTokenInformation(token, windows.TokenSessionId, (*byte)(unsafe.Pointer(&daemonSession)), 4, &length); err != nil {
		return identity, err
	}
	if err := windows.ProcessIdToSessionId(windows.GetCurrentProcessId(), &currentSession); err != nil {
		return identity, err
	}
	if daemonSession == 0 || daemonSession != currentSession {
		return identity, errors.New("installed daemon is outside the controller's interactive session")
	}
	return identity, nil
}

// admitDaemonIdentity rejects foreign, incomplete or ambiguous pipe and installed process identities.
func admitDaemonIdentity(identity daemonIdentity, peer control.DaemonPeer, image, selectedUser, currentUser string) error {
	birth, err := strconv.ParseUint(identity.Birth, 10, 64)
	if err != nil || birth == 0 || identity.PID == 0 || identity.Executable == "" || image == "" || selectedUser == "" ||
		identity.PID != peer.Transport.ProcessID || identity.UserID != peer.Transport.UserID ||
		identity.UserID != selectedUser || identity.UserID != currentUser || !strings.EqualFold(identity.Executable, image) {
		return errors.New("native daemon identity differs from authenticated peer or admitted installation")
	}
	return nil
}

// crashInstalledDaemon reauthenticates and terminates only the same retained and immediately revalidated native handle.
func crashInstalledDaemon(ctx context.Context, before daemonIdentity) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	installation, err := windowsinstallation.Admit()
	if err != nil {
		return err
	}
	defer installation.Close()
	client, err := control.NewClient(ctx, control.ClientConfig{Role: rpc.RoleDesktop, RequestTimeout: 10 * time.Second})
	if err != nil {
		return err
	}
	defer client.Close()
	peer := client.Peer()
	if peer.Transport.ProcessID != before.PID || peer.Transport.UserID != before.UserID {
		return errors.New("authenticated daemon changed before crash")
	}
	process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE|windows.PROCESS_TERMINATE, false, before.PID)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(process)
	for attempt := 0; attempt < 2; attempt++ {
		observed, err := identifyInstalledDaemon(process, installation, peer)
		if err != nil {
			return err
		}
		if observed != before {
			return errors.New("retained daemon identity changed before crash")
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := windows.TerminateProcess(process, 137); err != nil {
		return fmt.Errorf("terminate exact installed daemon: %w", err)
	}
	for {
		state, err := windows.WaitForSingleObject(process, 100)
		if err != nil {
			return err
		}
		if state == windows.WAIT_OBJECT_0 {
			return nil
		}
		if state != uint32(windows.WAIT_TIMEOUT) {
			return fmt.Errorf("unexpected daemon wait result: %d", state)
		}
		if err := ctx.Err(); err != nil {
			return err
		}
	}
}

// ensureInstalledDaemon starts only the fully admitted installed scheduled-task policy.
func ensureInstalledDaemon(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	installation, err := windowsinstallation.Admit()
	if err != nil {
		return err
	}
	defer installation.Close()
	return installation.Task(ctx, "start")
}

// installedCLI selects the admitted fixed signed launcher rather than a PATH or source-built CLI.
func installedCLI() (string, error) {
	installation, err := windowsinstallation.Admit()
	if err != nil {
		return "", err
	}
	defer installation.Close()
	return installation.Path("cli-launcher")
}
