package session

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/rpc"
)

// TestRoleScopedHandlersRouteExclusively proves negotiated roles select their own method surface.
func TestRoleScopedHandlersRouteExclusively(t *testing.T) {
	serverConfig := testServerConfig(map[string]Handler{
		"shared": func(context.Context, Request) (any, error) { return "human", nil },
	})
	serverConfig.RoleHandlers = map[rpc.Role]map[string]Handler{
		rpc.RoleGoForjSession: {
			"shared": func(context.Context, Request) (any, error) { return "managed", nil },
		},
	}
	serverConfig.Authorize = func(context.Context, rpc.Hello) error { return nil }

	cliPair := newTestPair(t, serverConfig, testClientConfig())
	humanPayload, err := cliPair.client.Call(t.Context(), "shared", struct{}{})
	if err != nil {
		t.Fatalf("human handler call: %v", err)
	}
	if string(humanPayload) != `"human"` {
		t.Fatalf("human handler payload = %s, want %q", humanPayload, `"human"`)
	}

	managedConfig := testClientConfig()
	managedConfig.Role = rpc.RoleGoForjSession
	managedPair := newTestPair(t, serverConfig, managedConfig)
	managedPayload, err := managedPair.client.Call(t.Context(), "shared", struct{}{})
	if err != nil {
		t.Fatalf("managed handler call: %v", err)
	}
	if string(managedPayload) != `"managed"` {
		t.Fatalf("managed handler payload = %s, want %q", managedPayload, `"managed"`)
	}
}

// TestRoleScopedHandlersDoNotFallBackToHumanMethods prevents role isolation from leaking default handlers.
func TestRoleScopedHandlersDoNotFallBackToHumanMethods(t *testing.T) {
	serverConfig := testServerConfig(map[string]Handler{
		"human-only": func(context.Context, Request) (any, error) { return "human", nil },
	})
	serverConfig.RoleHandlers = map[rpc.Role]map[string]Handler{
		rpc.RoleGoForjSession: {},
	}
	serverConfig.Authorize = func(context.Context, rpc.Hello) error { return nil }
	managedConfig := testClientConfig()
	managedConfig.Role = rpc.RoleGoForjSession
	pair := newTestPair(t, serverConfig, managedConfig)

	_, err := pair.client.Call(t.Context(), "human-only", struct{}{})
	assertWireErrorCode(t, err, rpc.ErrorCodeNotFound)
}

// TestRoleScopedHandlerConfigurationIsFrozen prevents caller mutations from changing live policy.
func TestRoleScopedHandlerConfigurationIsFrozen(t *testing.T) {
	managed := func(context.Context, Request) (any, error) { return "managed", nil }
	replacement := func(context.Context, Request) (any, error) { return "replacement", nil }
	configured := map[rpc.Role]map[string]Handler{
		rpc.RoleGoForjSession: {"shared": managed},
	}
	serverConfig := testServerConfig(nil)
	serverConfig.RoleHandlers = configured
	server, err := NewServer(serverConfig)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	configured[rpc.RoleGoForjSession]["shared"] = replacement
	configured[rpc.RoleGoForjSession]["replacement"] = replacement
	delete(configured[rpc.RoleGoForjSession], "shared")

	handler, exists := server.config.RoleHandlers[rpc.RoleGoForjSession]["shared"]
	if !exists {
		t.Fatal("normalized role handler was removed by caller mutation")
	}
	payload, err := handler(context.Background(), Request{})
	if err != nil {
		t.Fatalf("normalized role handler: %v", err)
	}
	if payload != "managed" {
		t.Fatalf("normalized role handler payload = %v, want managed", payload)
	}
	if _, exists := server.config.RoleHandlers[rpc.RoleGoForjSession]["replacement"]; exists {
		t.Fatal("caller-added role handler appeared in normalized policy")
	}
}

// TestRoleScopedHandlerConfigurationValidatesRolesAndMethods keeps invalid policy out of live servers.
func TestRoleScopedHandlerConfigurationValidatesRolesAndMethods(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*ServerConfig)
	}{
		{
			name: "daemon role",
			configure: func(config *ServerConfig) {
				config.RoleHandlers = map[rpc.Role]map[string]Handler{
					rpc.RoleDaemon: {"managed": func(context.Context, Request) (any, error) { return nil, nil }},
				}
			},
		},
		{
			name: "unknown role",
			configure: func(config *ServerConfig) {
				config.RoleHandlers = map[rpc.Role]map[string]Handler{
					rpc.Role("unknown"): {"managed": func(context.Context, Request) (any, error) { return nil, nil }},
				}
			},
		},
		{
			name: "nil handler",
			configure: func(config *ServerConfig) {
				config.RoleHandlers = map[rpc.Role]map[string]Handler{
					rpc.RoleGoForjSession: {"managed": nil},
				}
			},
		},
		{
			name: "invalid method",
			configure: func(config *ServerConfig) {
				config.RoleHandlers = map[rpc.Role]map[string]Handler{
					rpc.RoleGoForjSession: {"": func(context.Context, Request) (any, error) { return nil, nil }},
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := testServerConfig(nil)
			test.configure(&config)
			if _, err := NewServer(config); err == nil {
				t.Fatal("NewServer accepted invalid role handler configuration")
			}
		})
	}
}

// TestRoleScopedHandlersDoNotBypassManagedAdmission preserves explicit session authorization.
func TestRoleScopedHandlersDoNotBypassManagedAdmission(t *testing.T) {
	serverConfig := testServerConfig(nil)
	serverConfig.RoleHandlers = map[rpc.Role]map[string]Handler{
		rpc.RoleGoForjSession: {"managed": func(context.Context, Request) (any, error) { return nil, nil }},
	}
	server, err := NewServer(serverConfig)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	serverConnection, clientConnection := net.Pipe()
	defer serverConnection.Close()
	defer clientConnection.Close()
	serverDone := make(chan error, 1)
	go func() {
		serverDone <- server.Serve(t.Context(), serverConnection)
	}()
	managedConfig := testClientConfig()
	managedConfig.Role = rpc.RoleGoForjSession
	_, err = NewClient(t.Context(), clientConnection, managedConfig)
	var handshakeError *HandshakeError
	if !errors.As(err, &handshakeError) {
		t.Fatalf("managed client error = %T %v, want HandshakeError", err, err)
	}
	if handshakeError.Failure.Code != rpc.ErrorCodePermissionDenied {
		t.Fatalf("managed rejection code = %q, want permission denied", handshakeError.Failure.Code)
	}
	select {
	case <-serverDone:
	case <-time.After(time.Second):
		t.Fatal("rejected managed session did not stop")
	}
}
