//go:build linux

package hostconflict

import (
	"context"
	"errors"
	"fmt"
	"runtime"

	"golang.org/x/sys/unix"
)

const linuxObservationRetries = 3

// linuxObservationPass supplies one complete kernel pass so stability policy can be tested without host races.
type linuxObservationPass func(context.Context, Request, uint32) (Observation, error)

// linuxObservationSession combines one sequenced netlink port with explicit lifecycle cleanup.
type linuxObservationSession interface {
	linuxNetlinkExchanger
	close() error
}

// linuxPassOperations isolates orchestration failures from native fact codecs.
type linuxPassOperations struct {
	namespace  func() (NetworkScope, error)
	open       func(int) (linuxObservationSession, error)
	interfaces func(context.Context, linuxNetlinkExchanger) (linuxInterfaceSnapshot, error)
	routes     func(context.Context, linuxNetlinkExchanger, Request, uint32, linuxInterfaceSnapshot) (RouteSnapshot, error)
	sockets    func(context.Context, linuxNetlinkExchanger, Request) (SocketSnapshot, error)
	policy     func(context.Context, linuxInterfaceSnapshot) (*LinuxPolicyFacts, error)
}

// ObserveLinux returns two consecutive matching observations from the caller's current network namespace.
//
// requesterUID is included in every selected-route lookup because Linux policy
// routing may choose a different FIB entry for the user that will run Harbor.
func ObserveLinux(ctx context.Context, request Request, requesterUID uint32) (Observation, error) {
	if err := request.Validate(); err != nil {
		return Observation{}, fmt.Errorf("observe Linux host conflicts: %w", err)
	}
	ctx = normalizeLinuxObservationContext(ctx)
	if err := ctx.Err(); err != nil {
		return Observation{}, fmt.Errorf("observe Linux host conflicts: %w", err)
	}

	// setns is thread-scoped; pinning prevents one pass from accidentally spanning
	// two namespaces even when the caller manages namespaces elsewhere.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	return observeStableLinux(ctx, request, requesterUID, observeLinuxPass)
}

// observeStableLinux requires consecutive equality so a repeating A-B-A race cannot authorize either state.
func observeStableLinux(ctx context.Context, request Request, requesterUID uint32, observe linuxObservationPass) (Observation, error) {
	previousFingerprint := ""
	transactionRetries := 0
	for attempt := 0; attempt <= linuxObservationRetries; attempt++ {
		if err := ctx.Err(); err != nil {
			return Observation{}, fmt.Errorf("observe Linux host conflicts: %w", err)
		}
		observation, err := observe(ctx, request, requesterUID)
		if err != nil {
			if errors.Is(err, errLinuxNetlinkInterrupted) || errors.Is(err, errLinuxNetlinkReplyLimit) {
				transactionRetries++
				previousFingerprint = ""
				continue
			}
			return Observation{}, fmt.Errorf("observe Linux host conflicts: %w", err)
		}
		fingerprint, err := observation.Fingerprint()
		if err != nil {
			return Observation{}, fmt.Errorf("observe Linux host conflicts: invalid native facts: %w", err)
		}
		if previousFingerprint != "" && fingerprint == previousFingerprint {
			return observation, nil
		}
		previousFingerprint = fingerprint
	}
	if transactionRetries > 0 {
		return Observation{}, fmt.Errorf("observe Linux host conflicts: netlink transaction could not complete after %d retries", transactionRetries)
	}
	return Observation{}, fmt.Errorf("observe Linux host conflicts: kernel facts did not stabilize after %d passes", linuxObservationRetries+1)
}

// observeLinuxPass gathers scope, routes, sockets, and policy before proving the thread stayed in that scope.
func observeLinuxPass(ctx context.Context, request Request, requesterUID uint32) (Observation, error) {
	operations := linuxPassOperations{
		namespace: observeLinuxNamespace,
		open: func(protocol int) (linuxObservationSession, error) {
			return openLinuxNetlink(protocol)
		},
		interfaces: observeLinuxInterfaces,
		routes:     observeLinuxRoutes,
		sockets:    observeLinuxSockets,
		policy:     observeLinuxPolicy,
	}
	return observeLinuxPassWith(ctx, request, requesterUID, operations)
}

// observeLinuxPassWith closes a poisoned session on every failure before another pass may start.
func observeLinuxPassWith(ctx context.Context, request Request, requesterUID uint32, operations linuxPassOperations) (Observation, error) {
	scopeBefore, err := operations.namespace()
	if err != nil {
		return Observation{}, err
	}
	routeClient, err := operations.open(unix.NETLINK_ROUTE)
	if err != nil {
		return Observation{}, err
	}
	interfaces, err := operations.interfaces(ctx, routeClient)
	if err != nil {
		_ = routeClient.close()
		return Observation{}, err
	}
	routes, err := operations.routes(ctx, routeClient, request, requesterUID, interfaces)
	if err != nil {
		_ = routeClient.close()
		return Observation{}, err
	}
	if err := routeClient.close(); err != nil {
		return Observation{}, err
	}

	sockets := SocketSnapshot{Complete: true}
	if len(request.Requirements()) > 0 {
		diagnosticClient, err := operations.open(unix.NETLINK_SOCK_DIAG)
		if err != nil {
			return Observation{}, err
		}
		sockets, err = operations.sockets(ctx, diagnosticClient, request)
		if err != nil {
			_ = diagnosticClient.close()
			return Observation{}, err
		}
		if err := diagnosticClient.close(); err != nil {
			return Observation{}, err
		}
	}
	policy, err := operations.policy(ctx, interfaces)
	if err != nil {
		return Observation{}, err
	}
	scopeAfter, err := operations.namespace()
	if err != nil {
		return Observation{}, err
	}
	if scopeBefore.Platform != scopeAfter.Platform || *scopeBefore.LinuxNamespace != *scopeAfter.LinuxNamespace {
		return Observation{}, fmt.Errorf("host conflict Linux network namespace changed during observation")
	}

	observation := Observation{
		Request:  request,
		Scope:    scopeBefore,
		Loopback: interfaces.loopback,
		Routes:   routes,
		Sockets:  sockets,
		Policy:   PolicyFacts{Linux: policy},
	}
	if err := observation.Validate(); err != nil {
		return Observation{}, fmt.Errorf("host conflict Linux native observation: %w", err)
	}
	return observation, nil
}

// observeLinuxNamespace identifies the pinned thread's namespace by its nsfs device and inode.
func observeLinuxNamespace() (NetworkScope, error) {
	fileDescriptor, err := unix.Open("/proc/thread-self/ns/net", unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return NetworkScope{}, fmt.Errorf("host conflict Linux open network namespace: %w", err)
	}
	defer func() { _ = unix.Close(fileDescriptor) }()
	var fileSystem unix.Statfs_t
	if err := unix.Fstatfs(fileDescriptor, &fileSystem); err != nil {
		return NetworkScope{}, fmt.Errorf("host conflict Linux inspect network namespace filesystem: %w", err)
	}
	if uint64(fileSystem.Type) != uint64(unix.NSFS_MAGIC) {
		return NetworkScope{}, fmt.Errorf("host conflict Linux network namespace handle is not nsfs")
	}
	var status unix.Stat_t
	if err := unix.Fstat(fileDescriptor, &status); err != nil {
		return NetworkScope{}, fmt.Errorf("host conflict Linux inspect network namespace identity: %w", err)
	}
	scope, err := NewLinuxScope(uint64(status.Dev), status.Ino)
	if err != nil {
		return NetworkScope{}, err
	}
	return scope, nil
}

// normalizeLinuxObservationContext makes a nil context cancellable by the same code path as a live caller.
func normalizeLinuxObservationContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}
