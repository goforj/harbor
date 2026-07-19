package cmd

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/goforj/harbor/internal/control"
	"github.com/goforj/harbor/internal/domain"
)

// fakeDaemonControlClient records one-shot control calls and their cleanup.
type fakeDaemonControlClient struct {
	status                 control.DaemonStatus
	snapshot               domain.Snapshot
	registration           control.ProjectRegistration
	unregistration         control.ProjectUnregistration
	statusErr              error
	snapshotErr            error
	registrationErr        error
	unregistrationErr      error
	closeErr               error
	statusCalls            int
	snapshotCalls          int
	registrationCalls      int
	unregistrationCalls    int
	registrationRequests   []control.RegisterProjectRequest
	unregistrationRequests []control.UnregisterProjectRequest
	closeCalls             int
}

// RegisterProject returns the configured registration and records the request.
func (client *fakeDaemonControlClient) RegisterProject(
	_ context.Context,
	request control.RegisterProjectRequest,
) (control.ProjectRegistration, error) {
	client.registrationCalls++
	client.registrationRequests = append(client.registrationRequests, request)
	return client.registration, client.registrationErr
}

// UnregisterProject returns the configured operation and records the stable client intent.
func (client *fakeDaemonControlClient) UnregisterProject(
	_ context.Context,
	request control.UnregisterProjectRequest,
) (control.ProjectUnregistration, error) {
	client.unregistrationCalls++
	client.unregistrationRequests = append(client.unregistrationRequests, request)
	return client.unregistration, client.unregistrationErr
}

// Status returns the configured daemon status and records the request.
func (client *fakeDaemonControlClient) Status(context.Context) (control.DaemonStatus, error) {
	client.statusCalls++
	return client.status, client.statusErr
}

// Snapshot returns the configured daemon snapshot and records the request.
func (client *fakeDaemonControlClient) Snapshot(context.Context) (domain.Snapshot, error) {
	client.snapshotCalls++
	return client.snapshot, client.snapshotErr
}

// Close records deterministic connection cleanup and returns its configured failure.
func (client *fakeDaemonControlClient) Close() error {
	client.closeCalls++
	return client.closeErr
}

// TestDaemonClientConnectsOnlyWhenRequested verifies application wiring cannot contact harbord during construction.
func TestDaemonClientConnectsOnlyWhenRequested(t *testing.T) {
	productionClient := NewDaemonClient()
	if productionClient == nil || productionClient.connect == nil {
		t.Fatal("production constructor returned an unusable lazy client")
	}

	connection := &fakeDaemonControlClient{status: daemonTestStatus()}
	connectCalls := 0
	client := newDaemonClient(func(context.Context) (daemonControlClient, error) {
		connectCalls++
		return connection, nil
	})

	if connectCalls != 0 {
		t.Fatalf("construction opened %d connections, want 0", connectCalls)
	}
	status, err := client.Status(t.Context())
	if err != nil {
		t.Fatalf("read status: %v", err)
	}
	if !reflect.DeepEqual(status, connection.status) {
		t.Fatalf("status = %#v, want %#v", status, connection.status)
	}
	if connectCalls != 1 || connection.statusCalls != 1 || connection.closeCalls != 1 {
		t.Fatalf("calls = connect:%d status:%d close:%d, want 1 each", connectCalls, connection.statusCalls, connection.closeCalls)
	}
}

// TestDaemonClientReturnsCloseFailureAfterSuccessfulRequest verifies cleanup errors remain visible without a request failure.
func TestDaemonClientReturnsCloseFailureAfterSuccessfulRequest(t *testing.T) {
	closeErr := errors.New("close failed")
	connection := &fakeDaemonControlClient{status: daemonTestStatus(), closeErr: closeErr}
	client := newDaemonClient(func(context.Context) (daemonControlClient, error) {
		return connection, nil
	})

	_, err := client.Status(t.Context())
	if !errors.Is(err, closeErr) {
		t.Fatalf("error = %v, want %v", err, closeErr)
	}
	if connection.closeCalls != 1 {
		t.Fatalf("close calls = %d, want 1", connection.closeCalls)
	}
}

// TestDaemonClientSnapshotUsesASeparateOneShotConnection verifies every command owns its complete connection lifetime.
func TestDaemonClientSnapshotUsesASeparateOneShotConnection(t *testing.T) {
	first := &fakeDaemonControlClient{status: daemonTestStatus()}
	second := &fakeDaemonControlClient{snapshot: daemonTestSnapshot()}
	connections := []*fakeDaemonControlClient{first, second}
	client := newDaemonClient(func(context.Context) (daemonControlClient, error) {
		connection := connections[0]
		connections = connections[1:]
		return connection, nil
	})

	if _, err := client.Status(t.Context()); err != nil {
		t.Fatalf("read status: %v", err)
	}
	snapshot, err := client.Snapshot(t.Context())
	if err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	if snapshot.Sequence != second.snapshot.Sequence {
		t.Fatalf("snapshot sequence = %d, want %d", snapshot.Sequence, second.snapshot.Sequence)
	}
	if first.closeCalls != 1 || second.closeCalls != 1 || second.snapshotCalls != 1 {
		t.Fatalf("calls = first close:%d second snapshot:%d close:%d, want 1 each", first.closeCalls, second.snapshotCalls, second.closeCalls)
	}
}

// TestDaemonClientRegistrationUsesASeparateOneShotConnection verifies project mutations share the CLI cleanup contract.
func TestDaemonClientRegistrationUsesASeparateOneShotConnection(t *testing.T) {
	registration := addTestRegistration(t.TempDir(), true)
	connection := &fakeDaemonControlClient{registration: registration}
	client := newDaemonClient(func(context.Context) (daemonControlClient, error) { return connection, nil })
	request := control.RegisterProjectRequest{Path: registration.Project.Path}

	got, err := client.RegisterProject(t.Context(), request)
	if err != nil {
		t.Fatalf("register project: %v", err)
	}
	if !reflect.DeepEqual(got, registration) {
		t.Fatalf("registration = %#v, want %#v", got, registration)
	}
	if connection.registrationCalls != 1 || connection.closeCalls != 1 || len(connection.registrationRequests) != 1 || connection.registrationRequests[0] != request {
		t.Fatalf("calls = registration:%d close:%d requests:%#v", connection.registrationCalls, connection.closeCalls, connection.registrationRequests)
	}
}

// TestDaemonClientUnregistrationUsesASeparateOneShotConnection verifies removal shares the CLI cleanup contract without opening a second transport.
func TestDaemonClientUnregistrationUsesASeparateOneShotConnection(t *testing.T) {
	unregistration := removeTestUnregistration(t, domain.OperationSucceeded)
	connection := &fakeDaemonControlClient{unregistration: unregistration}
	client := newDaemonClient(func(context.Context) (daemonControlClient, error) { return connection, nil })
	request := control.UnregisterProjectRequest{
		ProjectID: unregistration.Operation.ProjectID,
		IntentID:  unregistration.Operation.IntentID,
	}

	got, err := client.UnregisterProject(t.Context(), request)
	if err != nil {
		t.Fatalf("unregister project: %v", err)
	}
	if !reflect.DeepEqual(got, unregistration) {
		t.Fatalf("unregistration = %#v, want %#v", got, unregistration)
	}
	if connection.unregistrationCalls != 1 || connection.closeCalls != 1 || len(connection.unregistrationRequests) != 1 || connection.unregistrationRequests[0] != request {
		t.Fatalf("calls = unregistration:%d close:%d requests:%#v", connection.unregistrationCalls, connection.closeCalls, connection.unregistrationRequests)
	}
}

// TestDaemonClientPreservesRequestAndCloseFailures verifies cleanup never hides the actionable request cause.
func TestDaemonClientPreservesRequestAndCloseFailures(t *testing.T) {
	requestErr := errors.New("request failed")
	closeErr := errors.New("close failed")

	for _, test := range []struct {
		name           string
		call           func(*DaemonClient) error
		makeConnection func() *fakeDaemonControlClient
	}{
		{
			name: "status",
			call: func(client *DaemonClient) error {
				_, err := client.Status(t.Context())
				return err
			},
			makeConnection: func() *fakeDaemonControlClient {
				return &fakeDaemonControlClient{statusErr: requestErr, closeErr: closeErr}
			},
		},
		{
			name: "snapshot",
			call: func(client *DaemonClient) error {
				_, err := client.Snapshot(t.Context())
				return err
			},
			makeConnection: func() *fakeDaemonControlClient {
				return &fakeDaemonControlClient{snapshotErr: requestErr, closeErr: closeErr}
			},
		},
		{
			name: "unregister project",
			call: func(client *DaemonClient) error {
				_, err := client.UnregisterProject(t.Context(), control.UnregisterProjectRequest{
					ProjectID: "project-orders",
					IntentID:  "intent-remove",
				})
				return err
			},
			makeConnection: func() *fakeDaemonControlClient {
				return &fakeDaemonControlClient{unregistrationErr: requestErr, closeErr: closeErr}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			connection := test.makeConnection()
			client := newDaemonClient(func(context.Context) (daemonControlClient, error) {
				return connection, nil
			})

			err := test.call(client)
			if !errors.Is(err, requestErr) || !errors.Is(err, closeErr) {
				t.Fatalf("error = %v, want request and close causes", err)
			}
			if connection.closeCalls != 1 {
				t.Fatalf("close calls = %d, want 1", connection.closeCalls)
			}
		})
	}
}

// TestDaemonClientReturnsConnectionFailureWithoutCleanup verifies a failed dial is not treated as an owned connection.
func TestDaemonClientReturnsConnectionFailureWithoutCleanup(t *testing.T) {
	connectErr := errors.New("connect failed")
	client := newDaemonClient(func(context.Context) (daemonControlClient, error) {
		return nil, connectErr
	})

	_, err := client.Status(t.Context())
	if !errors.Is(err, connectErr) {
		t.Fatalf("error = %v, want %v", err, connectErr)
	}
}
