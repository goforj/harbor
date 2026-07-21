package projectprocess

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/rpc/local"
)

// outputBrokerPeerTestEvidence returns one complete broker identity for transport-boundary tests.
func outputBrokerPeerTestEvidence(t *testing.T) domain.ProcessEvidence {
	t.Helper()
	return domain.ProcessEvidence{
		PID:                8123,
		BirthToken:         "broker-birth-8123",
		ExecutableIdentity: filepath.Join(t.TempDir(), "harbor-output-broker"),
		ArgumentDigest:     strings.Repeat("a", 64),
	}
}

// outputBrokerPeerTestProof returns one exact project/session broker proof.
func outputBrokerPeerTestProof(t *testing.T) OutputBrokerPeer {
	t.Helper()
	return OutputBrokerPeer{
		ProjectID:         "project-broker-peer",
		SessionID:         "session-broker-peer",
		EndpointReference: filepath.Join(t.TempDir(), "output-broker.sock"),
		Process:           outputBrokerPeerTestEvidence(t),
	}
}

// outputBrokerPeerTestConnection supplies a kernel-authenticated identity without opening a platform socket.
type outputBrokerPeerTestConnection struct {
	local.Conn
	peer     local.PeerIdentity
	endpoint string
}

// Peer returns the controlled operating-system identity for the test transport.
func (connection outputBrokerPeerTestConnection) Peer() local.PeerIdentity {
	return connection.peer
}

// EndpointReference returns the controlled local endpoint for the test transport.
func (connection outputBrokerPeerTestConnection) EndpointReference() string {
	return connection.endpoint
}

// TestAuthenticateOutputBrokerPeerRequiresEveryIdentityBoundary proves PID-only or payload-only claims cannot attach output.
func TestAuthenticateOutputBrokerPeerRequiresEveryIdentityBoundary(t *testing.T) {
	proof := outputBrokerPeerTestProof(t)
	connection := outputBrokerPeerTestConnection{peer: local.PeerIdentity{UserID: "501", ProcessID: uint32(proof.Process.PID)}, endpoint: proof.EndpointReference}
	if err := AuthenticateOutputBrokerPeer(connection, proof, proof.Process); err != nil {
		t.Fatalf("AuthenticateOutputBrokerPeer(valid) error = %v", err)
	}

	tests := []struct {
		name    string
		mutate  func(*OutputBrokerPeer, *domain.ProcessEvidence, *outputBrokerPeerTestConnection)
		wantErr error
	}{
		{
			name: "kernel PID drift",
			mutate: func(_ *OutputBrokerPeer, _ *domain.ProcessEvidence, connection *outputBrokerPeerTestConnection) {
				connection.peer.ProcessID++
			},
			wantErr: ErrOutputBrokerPeerMismatch,
		},
		{
			name: "birth drift",
			mutate: func(_ *OutputBrokerPeer, observed *domain.ProcessEvidence, _ *outputBrokerPeerTestConnection) {
				observed.BirthToken += "-changed"
			},
			wantErr: ErrOutputBrokerPeerMismatch,
		},
		{
			name: "executable drift",
			mutate: func(_ *OutputBrokerPeer, observed *domain.ProcessEvidence, _ *outputBrokerPeerTestConnection) {
				observed.ExecutableIdentity = filepath.Join(filepath.Dir(observed.ExecutableIdentity), "other-broker")
			},
			wantErr: ErrOutputBrokerPeerMismatch,
		},
		{
			name: "argument drift",
			mutate: func(_ *OutputBrokerPeer, observed *domain.ProcessEvidence, _ *outputBrokerPeerTestConnection) {
				observed.ArgumentDigest = strings.Repeat("b", 64)
			},
			wantErr: ErrOutputBrokerPeerMismatch,
		},
		{
			name: "missing transport identity",
			mutate: func(_ *OutputBrokerPeer, _ *domain.ProcessEvidence, connection *outputBrokerPeerTestConnection) {
				connection.peer = local.PeerIdentity{}
			},
			wantErr: ErrOutputBrokerPeerMismatch,
		},
		{
			name: "endpoint drift",
			mutate: func(_ *OutputBrokerPeer, _ *domain.ProcessEvidence, connection *outputBrokerPeerTestConnection) {
				connection.endpoint += ".other"
			},
			wantErr: ErrOutputBrokerPeerMismatch,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := proof
			observed := proof.Process
			candidateConnection := connection
			test.mutate(&candidate, &observed, &candidateConnection)
			if err := AuthenticateOutputBrokerPeer(candidateConnection, candidate, observed); !errors.Is(err, test.wantErr) {
				t.Fatalf("AuthenticateOutputBrokerPeer() error = %v, want %v", err, test.wantErr)
			}
		})
	}
}

// TestOutputBrokerPeerValidationRejectsUnsafeEndpointShapes keeps endpoint text from becoming a transport authority.
func TestOutputBrokerPeerValidationRejectsUnsafeEndpointShapes(t *testing.T) {
	proof := outputBrokerPeerTestProof(t)
	tests := []struct {
		name   string
		mutate func(*OutputBrokerPeer)
	}{
		{name: "relative", mutate: func(value *OutputBrokerPeer) { value.EndpointReference = "broker.sock" }},
		{name: "unclean", mutate: func(value *OutputBrokerPeer) { value.EndpointReference += string(filepath.Separator) + ".." }},
		{name: "control", mutate: func(value *OutputBrokerPeer) { value.EndpointReference += "\n" }},
		{name: "empty pipe", mutate: func(value *OutputBrokerPeer) { value.EndpointReference = `\\.\pipe\` }},
		{name: "nested pipe", mutate: func(value *OutputBrokerPeer) { value.EndpointReference = `\\.\pipe\harbor\broker` }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := proof
			test.mutate(&candidate)
			if err := candidate.Validate(); !errors.Is(err, ErrOutputBrokerEndpointMismatch) {
				t.Fatalf("OutputBrokerPeer.Validate() error = %v, want endpoint mismatch", err)
			}
		})
	}
}

// TestOutputBrokerPeerValidationKeepsProjectAndSessionBindingExplicit proves the broker proof cannot drift across lifecycles.
func TestOutputBrokerPeerValidationKeepsProjectAndSessionBindingExplicit(t *testing.T) {
	proof := outputBrokerPeerTestProof(t)
	for name, mutate := range map[string]func(*OutputBrokerPeer){
		"project": func(value *OutputBrokerPeer) { value.ProjectID = "" },
		"session": func(value *OutputBrokerPeer) { value.SessionID = "" },
		"process": func(value *OutputBrokerPeer) { value.Process.PID = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := proof
			mutate(&candidate)
			if err := candidate.Validate(); err == nil {
				t.Fatal("OutputBrokerPeer.Validate() accepted incomplete lifecycle proof")
			}
		})
	}
}

// TestOutputBrokerPeerValidationRequiresCompleteReattachMetadata prevents a durable session from pointing at half an authority.
func TestOutputBrokerPeerValidationRequiresCompleteReattachMetadata(t *testing.T) {
	proof := outputBrokerPeerTestProof(t)
	proof.ManifestPath = filepath.Join(t.TempDir(), "broker.json")
	proof.TicketDigest = DigestOutputBrokerTicket("broker-ticket")
	if err := proof.Validate(); err != nil {
		t.Fatalf("OutputBrokerPeer.Validate() complete metadata error = %v", err)
	}
	for name, mutate := range map[string]func(*OutputBrokerPeer){
		"missing digest":    func(value *OutputBrokerPeer) { value.TicketDigest = "" },
		"missing manifest":  func(value *OutputBrokerPeer) { value.ManifestPath = "" },
		"invalid digest":    func(value *OutputBrokerPeer) { value.TicketDigest = strings.Repeat("A", 64) },
		"relative manifest": func(value *OutputBrokerPeer) { value.ManifestPath = "broker.json" },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := proof
			mutate(&candidate)
			if err := candidate.Validate(); err == nil {
				t.Fatal("OutputBrokerPeer.Validate() accepted incomplete reattach metadata")
			}
		})
	}
}
