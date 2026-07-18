package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/goforj/harbor/internal/buildinfo"
	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/rpc"
	"github.com/goforj/harbor/internal/rpc/local"
	"github.com/goforj/harbor/internal/rpc/session"
)

// DialFunc opens an operating-system-authenticated connection to the current user's daemon.
type DialFunc func(context.Context) (local.Conn, error)

// ClientConfig defines one CLI or desktop connection policy.
type ClientConfig struct {
	// Role identifies whether the caller is the Harbor CLI or desktop backend.
	Role rpc.Role
	// Dial optionally replaces the platform transport for composition or tests; nil uses local.Dial.
	Dial DialFunc
}

// DaemonPeer combines the daemon identity authenticated by the operating system and session negotiation.
type DaemonPeer struct {
	// Transport is the operating-system identity authenticated by the local socket or pipe.
	Transport local.PeerIdentity
	// Session is the daemon build and exact protocol selected during negotiation.
	Session session.Peer
}

// Client is a reusable typed CLI or desktop connection to harbord.
type Client struct {
	session *session.Client
	peer    DaemonPeer
}

// NewClient dials and negotiates the current user's typed Harbor control endpoint.
func NewClient(ctx context.Context, config ClientConfig) (*Client, error) {
	return newClient(ctx, config, buildinfo.Current())
}

// newClient keeps product build metadata deterministic in protocol tests.
func newClient(ctx context.Context, config ClientConfig, build buildinfo.Info) (*Client, error) {
	ctx = normalizeContext(ctx)
	if config.Role != rpc.RoleCLI && config.Role != rpc.RoleDesktop {
		return nil, fmt.Errorf("control client role must be %q or %q", rpc.RoleCLI, rpc.RoleDesktop)
	}
	if err := validateBuild(buildFromInfo(build)); err != nil {
		return nil, fmt.Errorf("control client build: %w", err)
	}
	dial := config.Dial
	if dial == nil {
		dial = local.Dial
	}

	connection, err := dial(ctx)
	if err != nil {
		return nil, fmt.Errorf("dial Harbor daemon: %w", err)
	}
	if connection == nil {
		return nil, errors.New("dial Harbor daemon: transport returned a nil connection")
	}
	if err := validateTransportPeer(connection.Peer()); err != nil {
		_ = connection.Close()
		return nil, fmt.Errorf("authenticate Harbor daemon: %w", err)
	}

	controlSession, err := session.NewClient(ctx, connection, session.ClientConfig{
		Role:           config.Role,
		ClientVersion:  build.Version,
		ProtocolRanges: protocolRanges(),
		Capabilities:   capabilities(),
	})
	if err != nil {
		return nil, fmt.Errorf("negotiate Harbor control session: %w", err)
	}
	peer := controlSession.Peer()
	if peer.Role != rpc.RoleDaemon || peer.Protocol.Compare(protocolV1) != 0 || !containsCapability(peer.Capabilities, CapabilityV1) {
		_ = controlSession.Close()
		return nil, errors.New("negotiate Harbor control session: daemon did not select control.v1")
	}

	return &Client{
		session: controlSession,
		peer: DaemonPeer{
			Transport: connection.Peer(),
			Session:   peer,
		},
	}, nil
}

// Peer returns immutable transport and negotiated daemon identity.
func (client *Client) Peer() DaemonPeer {
	peer := client.peer
	peer.Session.Capabilities = append([]rpc.Capability(nil), peer.Session.Capabilities...)

	return peer
}

// Status fetches the ready daemon's standalone product diagnostic.
func (client *Client) Status(ctx context.Context) (DaemonStatus, error) {
	payload, err := client.session.Call(ctx, methodDaemonStatus, struct{}{})
	if err != nil {
		return DaemonStatus{}, err
	}
	var response statusResponse
	if err := json.Unmarshal(payload, &response); err != nil {
		return DaemonStatus{}, fmt.Errorf("decode daemon status response: %w", err)
	}
	if err := validateReceivedStatus(response.Status, client.peer.Session); err != nil {
		return DaemonStatus{}, fmt.Errorf("validate daemon status response: %w", err)
	}

	return response.Status, nil
}

// Snapshot fetches a complete authoritative replacement of client-visible daemon state.
func (client *Client) Snapshot(ctx context.Context) (domain.Snapshot, error) {
	payload, err := client.session.Call(ctx, methodSnapshot, struct{}{})
	if err != nil {
		return domain.Snapshot{}, err
	}
	var response snapshotResponse
	if err := json.Unmarshal(payload, &response); err != nil {
		return domain.Snapshot{}, fmt.Errorf("decode daemon snapshot response: %w", err)
	}
	if err := validateControlSnapshot(response.Snapshot); err != nil {
		return domain.Snapshot{}, fmt.Errorf("validate daemon snapshot response: %w", err)
	}

	return response.Snapshot, nil
}

// Done closes when the daemon connection becomes terminal.
func (client *Client) Done() <-chan struct{} {
	return client.session.Done()
}

// Err returns nil while connected and the first terminal cause after Done closes.
func (client *Client) Err() error {
	return client.session.Err()
}

// Close terminates the underlying session and wakes pending calls.
func (client *Client) Close() error {
	return client.session.Close()
}
