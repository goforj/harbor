package control

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/buildinfo"
	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/rpc"
	"github.com/goforj/harbor/internal/rpc/local"
	"github.com/goforj/harbor/internal/rpc/session"
)

// TestPublicClientConstructorUsesCurrentBuildPolicy verifies the public path validates before default transport work.
func TestPublicClientConstructorUsesCurrentBuildPolicy(t *testing.T) {
	if _, err := NewClient(context.Background(), ClientConfig{Role: rpc.RoleGoForjSession}); err == nil {
		t.Fatal("NewClient accepted a GoForj session role")
	}
}

// TestClientConnectionFailuresAreSpecific verifies dial, identity, handshake, and capability failures stay distinct.
func TestClientConnectionFailuresAreSpecific(t *testing.T) {
	t.Run("build", func(t *testing.T) {
		_, err := newClient(context.Background(), ClientConfig{Role: rpc.RoleCLI}, testBuildWithVersion("bad version"))
		if err == nil || !strings.Contains(err.Error(), "client build") {
			t.Fatalf("build error = %v, want client build failure", err)
		}
	})
	t.Run("dial", func(t *testing.T) {
		want := errors.New("endpoint missing")
		_, err := newClient(context.Background(), ClientConfig{
			Role: rpc.RoleCLI,
			Dial: func(context.Context) (local.Conn, error) { return nil, want },
		}, testBuild)
		if !errors.Is(err, want) {
			t.Fatalf("dial error = %v, want %v", err, want)
		}
	})
	t.Run("peer identity", func(t *testing.T) {
		clientStream, serverStream := net.Pipe()
		defer serverStream.Close()
		_, err := newClient(context.Background(), ClientConfig{
			Role: rpc.RoleCLI,
			Dial: func(context.Context) (local.Conn, error) {
				return &testLocalConn{Conn: clientStream}, nil
			},
		}, testBuild)
		if err == nil || !strings.Contains(err.Error(), "authenticate Harbor daemon") {
			t.Fatalf("peer error = %v, want authentication failure", err)
		}
	})
	t.Run("handshake", func(t *testing.T) {
		clientStream, serverStream := net.Pipe()
		_ = serverStream.Close()
		_, err := newClient(context.Background(), ClientConfig{
			Role: rpc.RoleCLI,
			Dial: func(context.Context) (local.Conn, error) {
				return &testLocalConn{Conn: clientStream, peer: testDaemonPeer}, nil
			},
		}, testBuild)
		if err == nil || !strings.Contains(err.Error(), "negotiate Harbor control session") {
			t.Fatalf("handshake error = %v, want negotiation failure", err)
		}
	})
	t.Run("missing negotiated capability", func(t *testing.T) {
		clientStream, serverStream := net.Pipe()
		server, err := session.NewServer(session.ServerConfig{
			DaemonVersion:  testBuild.Version,
			ProtocolRanges: protocolRanges(),
			Handlers:       map[string]session.Handler{},
		})
		if err != nil {
			t.Fatalf("construct session server: %v", err)
		}
		serverDone := make(chan error, 1)
		go func() { serverDone <- server.Serve(context.Background(), serverStream) }()
		_, err = newClient(context.Background(), ClientConfig{
			Role: rpc.RoleCLI,
			Dial: func(context.Context) (local.Conn, error) {
				return &testLocalConn{Conn: clientStream, peer: testDaemonPeer}, nil
			},
		}, testBuild)
		if err == nil || !strings.Contains(err.Error(), "did not select control.v1") {
			t.Fatalf("capability error = %v, want control.v1 failure", err)
		}
		select {
		case <-serverDone:
		case <-time.After(time.Second):
			t.Fatal("capability test server did not stop")
		}
	})
}

// testBuildWithVersion returns product metadata with one version override.
func testBuildWithVersion(version string) buildinfo.Info {
	build := testBuild
	build.Version = version
	return build
}

// TestClientRejectsMalformedTypedResponses verifies a negotiated peer cannot bypass product response validation.
func TestClientRejectsMalformedTypedResponses(t *testing.T) {
	versionContradiction := testStatus()
	versionContradiction.Build.Version = "v9.0.0"
	capabilityContradiction := testStatus()
	capabilityContradiction.Capabilities = []rpc.Capability{CapabilityV1, "events.v1"}
	tests := []struct {
		name    string
		method  string
		payload any
		call    func(*Client) error
		want    string
	}{
		{
			name:    "status JSON",
			method:  methodDaemonStatus,
			payload: "wrong shape",
			call: func(client *Client) error {
				_, err := client.Status(context.Background())
				return err
			},
			want: "decode daemon status",
		},
		{
			name:    "status value",
			method:  methodDaemonStatus,
			payload: statusResponse{},
			call: func(client *Client) error {
				_, err := client.Status(context.Background())
				return err
			},
			want: "validate daemon status",
		},
		{
			name:    "status handshake version",
			method:  methodDaemonStatus,
			payload: statusResponse{Status: versionContradiction},
			call: func(client *Client) error {
				_, err := client.Status(context.Background())
				return err
			},
			want: "negotiated daemon",
		},
		{
			name:    "status handshake capabilities",
			method:  methodDaemonStatus,
			payload: statusResponse{Status: capabilityContradiction},
			call: func(client *Client) error {
				_, err := client.Status(context.Background())
				return err
			},
			want: "negotiated session",
		},
		{
			name:    "snapshot JSON",
			method:  methodSnapshot,
			payload: "wrong shape",
			call: func(client *Client) error {
				_, err := client.Snapshot(context.Background())
				return err
			},
			want: "decode daemon snapshot",
		},
		{
			name:    "snapshot value",
			method:  methodSnapshot,
			payload: snapshotResponse{},
			call: func(client *Client) error {
				_, err := client.Snapshot(context.Background())
				return err
			},
			want: "validate daemon snapshot",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := newTypedResponseTestClient(t, test.method, test.payload)
			if err := test.call(client); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("typed response error = %v, want %q", err, test.want)
			}
		})
	}
}

// newTypedResponseTestClient starts a negotiated daemon that returns one deliberately invalid product shape.
func newTypedResponseTestClient(t *testing.T, method string, payload any) *Client {
	t.Helper()
	clientStream, serverStream := net.Pipe()
	server, err := session.NewServer(session.ServerConfig{
		DaemonVersion:  testBuild.Version,
		ProtocolRanges: protocolRanges(),
		Capabilities:   capabilities(),
		Handlers: map[string]session.Handler{
			method: func(context.Context, session.Request) (any, error) { return payload, nil },
		},
	})
	if err != nil {
		t.Fatalf("construct typed response server: %v", err)
	}
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.Serve(context.Background(), serverStream) }()
	client, err := newClient(context.Background(), ClientConfig{
		Role: rpc.RoleCLI,
		Dial: func(context.Context) (local.Conn, error) {
			return &testLocalConn{Conn: clientStream, peer: testDaemonPeer}, nil
		},
	}, testBuild)
	if err != nil {
		t.Fatalf("construct typed response client: %v", err)
	}
	t.Cleanup(func() {
		_ = client.Close()
		select {
		case <-serverDone:
		case <-time.After(time.Second):
			t.Error("typed response server did not stop")
		}
	})

	return client
}

// TestHandlersRejectInvalidAuthorityOutput verifies daemon-owned malformed state becomes a reviewed internal error.
func TestHandlersRejectInvalidAuthorityOutput(t *testing.T) {
	authority := &recordingAuthority{
		status:   DaemonStatus{},
		snapshot: domain.Snapshot{},
	}
	running := newRunningControlClient(t, rpc.RoleCLI, authority, nil)
	for _, call := range []func() error{
		func() error { _, err := running.client.Status(context.Background()); return err },
		func() error { _, err := running.client.Snapshot(context.Background()); return err },
	} {
		var wireError rpc.WireError
		if err := call(); !errors.As(err, &wireError) || wireError.Code != rpc.ErrorCodeInternal {
			t.Fatalf("invalid authority output error = %#v, want internal", err)
		}
	}
}

// TestServerRejectsAuthorityStatusContradictingItsBuild verifies full build metadata is checked before serialization.
func TestServerRejectsAuthorityStatusContradictingItsBuild(t *testing.T) {
	status := testStatus()
	status.Build.Revision = "different"
	running := newRunningControlClient(t, rpc.RoleCLI, &recordingAuthority{status: status}, nil)

	_, err := running.client.Status(context.Background())
	var wireError rpc.WireError
	if !errors.As(err, &wireError) || wireError.Code != rpc.ErrorCodeInternal {
		t.Fatalf("contradictory authority status error = %#v, want internal", err)
	}
}

// TestServerConstructionAndNilConnectionFailuresAreImmediate verifies startup errors precede application reads.
func TestServerConstructionAndNilConnectionFailuresAreImmediate(t *testing.T) {
	if _, err := newServer(ServerConfig{Authority: &recordingAuthority{}, RequestShutdown: func() {}}, testBuildWithVersion("bad version")); err == nil {
		t.Fatal("newServer accepted an invalid build")
	}
	server, err := newServer(ServerConfig{Authority: &recordingAuthority{}, RequestShutdown: func() {}}, testBuild)
	if err != nil {
		t.Fatalf("construct control server: %v", err)
	}
	if err := server.Serve(context.Background(), nil); err == nil {
		t.Fatal("Serve accepted a nil connection")
	}
}
