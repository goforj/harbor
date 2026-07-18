// Package platformproof contains executable proofs for Harbor's operating-system contracts.
package platformproof

import (
	"bufio"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"runtime"
	"strconv"
	"time"
)

const (
	// EvidenceSchemaVersion identifies the machine-readable platform-proof schema.
	EvidenceSchemaVersion    = 1
	proofTimeout             = 10 * time.Second
	maximumPayloadBytes      = 4096
	hostNetworkAPISmokeScope = "host_network_api_smoke"
)

// ProjectIdentityRequest describes the exact same-port identity proof to execute.
type ProjectIdentityRequest struct {
	Addresses          []netip.Addr
	Port               uint16
	Commit             string
	RunnerName         string
	RunnerImage        string
	RunnerImageVersion string
}

// RuntimeEvidence identifies the environment that executed a platform proof.
type RuntimeEvidence struct {
	GOOS               string `json:"goos"`
	GOARCH             string `json:"goarch"`
	Commit             string `json:"commit,omitempty"`
	RunnerName         string `json:"runner_name,omitempty"`
	RunnerImage        string `json:"runner_image,omitempty"`
	RunnerImageVersion string `json:"runner_image_version,omitempty"`
}

// AssertionEvidence records one product invariant proved by the harness.
type AssertionEvidence struct {
	ID     string `json:"id"`
	Passed bool   `json:"passed"`
	Detail string `json:"detail"`
}

// IdentityEvidence records the endpoint and authenticated response for one identity.
type IdentityEvidence struct {
	Address           string `json:"address"`
	Endpoint          string `json:"endpoint"`
	PayloadDigest     string `json:"payload_digest"`
	InterfaceName     string `json:"interface_name"`
	InterfaceIndex    int    `json:"interface_index"`
	InterfaceLoopback bool   `json:"interface_loopback"`
	PrefixLength      int    `json:"prefix_length"`
}

// ProjectIdentityEvidence is the machine-readable result of an API-level same-port smoke proof.
// It does not claim installer elevation, privilege prompts, reboot persistence, or released-product behavior.
type ProjectIdentityEvidence struct {
	SchemaVersion int                 `json:"schema_version"`
	Capability    string              `json:"capability"`
	Scope         string              `json:"scope"`
	Runtime       RuntimeEvidence     `json:"runtime"`
	Port          uint16              `json:"port"`
	Identities    []IdentityEvidence  `json:"identities"`
	Assertions    []AssertionEvidence `json:"assertions"`
}

// CleanupEvidence proves that explicit API-smoke identities are no longer assigned to an interface.
type CleanupEvidence struct {
	SchemaVersion int                 `json:"schema_version"`
	Capability    string              `json:"capability"`
	Scope         string              `json:"scope"`
	Runtime       RuntimeEvidence     `json:"runtime"`
	Addresses     []string            `json:"addresses"`
	Assertions    []AssertionEvidence `json:"assertions"`
}

// signedPayload prevents crossed listeners from satisfying the proof with a repeated static response.
type signedPayload struct {
	Address   string `json:"address"`
	Port      uint16 `json:"port"`
	Nonce     string `json:"nonce"`
	Signature string `json:"signature"`
}

// interfaceAssignment binds one exact address to the interface facts observed from the host.
type interfaceAssignment struct {
	address      netip.Addr
	name         string
	index        int
	loopback     bool
	prefixLength int
}

// ProveProjectIdentities proves that two loopback identities can serve distinct authenticated responses on one native port.
func ProveProjectIdentities(ctx context.Context, request ProjectIdentityRequest) (ProjectIdentityEvidence, error) {
	if err := validateProjectIdentityRequest(request); err != nil {
		return ProjectIdentityEvidence{}, err
	}
	if ctx == nil {
		ctx = context.Background()
	}

	assignments, err := explicitIdentityAssignments(request.Addresses)
	if err != nil {
		return ProjectIdentityEvidence{}, err
	}

	proofContext, cancel := context.WithTimeout(ctx, proofTimeout)
	defer cancel()

	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return ProjectIdentityEvidence{}, fmt.Errorf("generate proof secret: %w", err)
	}

	listeners, err := listenOnIdentities(proofContext, request.Addresses, request.Port)
	if err != nil {
		return ProjectIdentityEvidence{}, err
	}
	defer closeListeners(listeners)

	if err := proveDuplicateListenerRejected(proofContext, request.Addresses[0], request.Port); err != nil {
		return ProjectIdentityEvidence{}, err
	}

	payloads := make([]signedPayload, len(request.Addresses))
	serverErrors := make(chan error, len(listeners))
	for index, address := range request.Addresses {
		payload, payloadErr := newSignedPayload(secret, address, request.Port)
		if payloadErr != nil {
			return ProjectIdentityEvidence{}, payloadErr
		}
		payloads[index] = payload
		go servePayload(proofContext, listeners[index], payload, serverErrors)
	}

	identities := make([]IdentityEvidence, 0, len(request.Addresses))
	for index, address := range request.Addresses {
		received, receiveErr := receivePayload(proofContext, address, request.Port)
		if receiveErr != nil {
			return ProjectIdentityEvidence{}, receiveErr
		}
		if err := verifyPayload(secret, received, address, request.Port); err != nil {
			return ProjectIdentityEvidence{}, err
		}
		if received.Nonce != payloads[index].Nonce {
			return ProjectIdentityEvidence{}, fmt.Errorf("identity %s returned another listener's payload", address)
		}
		identities = append(identities, IdentityEvidence{
			Address:           address.String(),
			Endpoint:          net.JoinHostPort(address.String(), strconv.Itoa(int(request.Port))),
			PayloadDigest:     payloadDigest(received),
			InterfaceName:     assignments[index].name,
			InterfaceIndex:    assignments[index].index,
			InterfaceLoopback: assignments[index].loopback,
			PrefixLength:      assignments[index].prefixLength,
		})
	}

	for range listeners {
		if err := <-serverErrors; err != nil {
			return ProjectIdentityEvidence{}, err
		}
	}
	confirmedAssignments, err := explicitIdentityAssignments(request.Addresses)
	if err != nil {
		return ProjectIdentityEvidence{}, fmt.Errorf("confirm explicit identity assignments: %w", err)
	}
	if err := confirmIdentityAssignments(request.Addresses, assignments, confirmedAssignments); err != nil {
		return ProjectIdentityEvidence{}, err
	}

	return ProjectIdentityEvidence{
		SchemaVersion: EvidenceSchemaVersion,
		Capability:    "project_loopback_identity",
		Scope:         hostNetworkAPISmokeScope,
		Runtime:       runtimeEvidence(request),
		Port:          request.Port,
		Identities:    identities,
		Assertions: []AssertionEvidence{
			{ID: "network.loopback.explicit_assignment", Passed: true, Detail: "both /32 identities remained on their observed loopback interfaces for the socket proof"},
			{ID: "network.loopback.distinct_identities", Passed: true, Detail: "two distinct IPv4 loopback identities accepted listeners"},
			{ID: "network.loopback.same_native_port", Passed: true, Detail: fmt.Sprintf("both identities bound native port %d", request.Port)},
			{ID: "network.loopback.distinct_payloads", Passed: true, Detail: "each identity returned its own authenticated payload"},
			{ID: "network.loopback.duplicate_rejected", Passed: true, Detail: "a duplicate listener was rejected without translating the port"},
		},
	}, nil
}

// ProveIdentitiesAbsent proves that cleanup removed the explicit addresses from every network interface.
func ProveIdentitiesAbsent(request ProjectIdentityRequest) (CleanupEvidence, error) {
	if err := validateAddresses(request.Addresses); err != nil {
		return CleanupEvidence{}, err
	}

	assigned, err := assignedAddresses()
	if err != nil {
		return CleanupEvidence{}, err
	}

	addresses := make([]string, 0, len(request.Addresses))
	for _, address := range request.Addresses {
		addresses = append(addresses, address.String())
		if _, exists := assigned[address]; exists {
			return CleanupEvidence{}, fmt.Errorf("loopback identity %s remains assigned after cleanup", address)
		}
	}

	return CleanupEvidence{
		SchemaVersion: EvidenceSchemaVersion,
		Capability:    "project_loopback_identity_cleanup",
		Scope:         hostNetworkAPISmokeScope,
		Runtime:       runtimeEvidence(request),
		Addresses:     addresses,
		Assertions: []AssertionEvidence{
			{ID: "network.loopback.explicit_cleanup", Passed: true, Detail: "proof identities are absent from all network interfaces"},
		},
	}, nil
}

// confirmIdentityAssignments prevents a host-side assignment change from being hidden by an open listener.
func confirmIdentityAssignments(addresses []netip.Addr, initial []interfaceAssignment, confirmed []interfaceAssignment) error {
	if len(initial) != len(addresses) || len(confirmed) != len(addresses) {
		return errors.New("explicit identity assignment count changed during the socket proof")
	}
	for index := range addresses {
		if initial[index] != confirmed[index] {
			return fmt.Errorf("proof identity %s changed interface assignment during the socket proof", addresses[index])
		}
	}
	return nil
}

// explicitIdentityAssignments requires each requested identity to be an exact /32 on one loopback interface.
func explicitIdentityAssignments(addresses []netip.Addr) ([]interfaceAssignment, error) {
	observed, err := observeInterfaceAssignments()
	if err != nil {
		return nil, err
	}
	return selectExplicitIdentityAssignments(addresses, observed)
}

// observeInterfaceAssignments captures exact interface addresses instead of inferring assignment from routability.
func observeInterfaceAssignments() ([]interfaceAssignment, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil, fmt.Errorf("list network interfaces: %w", err)
	}

	var observed []interfaceAssignment
	for _, networkInterface := range interfaces {
		addresses, addressErr := networkInterface.Addrs()
		if addressErr != nil {
			return nil, fmt.Errorf("list addresses for interface %s: %w", networkInterface.Name, addressErr)
		}
		for _, address := range addresses {
			prefix, parseErr := netip.ParsePrefix(address.String())
			if parseErr != nil {
				continue
			}
			observed = append(observed, interfaceAssignment{
				address:      prefix.Addr().Unmap(),
				name:         networkInterface.Name,
				index:        networkInterface.Index,
				loopback:     networkInterface.Flags&net.FlagLoopback != 0,
				prefixLength: prefix.Bits(),
			})
		}
	}
	return observed, nil
}

// selectExplicitIdentityAssignments rejects absent, ambiguous, non-loopback, and non-host assignments.
func selectExplicitIdentityAssignments(addresses []netip.Addr, observed []interfaceAssignment) ([]interfaceAssignment, error) {
	selected := make([]interfaceAssignment, 0, len(addresses))
	for _, address := range addresses {
		var matches []interfaceAssignment
		for _, assignment := range observed {
			if assignment.address == address {
				matches = append(matches, assignment)
			}
		}
		if len(matches) == 0 {
			return nil, fmt.Errorf("proof identity %s is not explicitly assigned to a network interface", address)
		}
		if len(matches) != 1 {
			return nil, fmt.Errorf("proof identity %s is ambiguously assigned to %d network interfaces", address, len(matches))
		}
		match := matches[0]
		if match.name == "" || match.index <= 0 {
			return nil, fmt.Errorf("proof identity %s has incomplete interface identity", address)
		}
		if !match.loopback {
			return nil, fmt.Errorf("proof identity %s is assigned to non-loopback interface %s", address, match.name)
		}
		if match.prefixLength != 32 {
			return nil, fmt.Errorf("proof identity %s has prefix length %d instead of 32", address, match.prefixLength)
		}
		selected = append(selected, match)
	}
	return selected, nil
}

// validateProjectIdentityRequest rejects requests that cannot prove the same-port loopback contract.
func validateProjectIdentityRequest(request ProjectIdentityRequest) error {
	if err := validateAddresses(request.Addresses); err != nil {
		return err
	}
	if request.Port == 0 {
		return errors.New("proof port must be a fixed non-zero native port")
	}
	return nil
}

// validateAddresses enforces two distinct unicast IPv4 loopback identities.
func validateAddresses(addresses []netip.Addr) error {
	if len(addresses) != 2 {
		return fmt.Errorf("project identity proof requires exactly two addresses, got %d", len(addresses))
	}
	if addresses[0] == addresses[1] {
		return errors.New("project identity proof requires distinct addresses")
	}
	for _, address := range addresses {
		if !address.IsValid() || !address.Is4() || !address.IsLoopback() || address.IsUnspecified() {
			return fmt.Errorf("proof address %q must be a unicast IPv4 loopback address", address)
		}
	}
	return nil
}

// listenOnIdentities opens the same port on every requested identity without a wildcard bind.
func listenOnIdentities(ctx context.Context, addresses []netip.Addr, port uint16) ([]net.Listener, error) {
	listeners := make([]net.Listener, 0, len(addresses))
	listenConfig := net.ListenConfig{}
	for _, address := range addresses {
		endpoint := net.JoinHostPort(address.String(), strconv.Itoa(int(port)))
		listener, err := listenConfig.Listen(ctx, "tcp4", endpoint)
		if err != nil {
			closeListeners(listeners)
			return nil, fmt.Errorf("listen on project identity %s: %w", endpoint, err)
		}
		listeners = append(listeners, listener)
	}
	return listeners, nil
}

// proveDuplicateListenerRejected confirms Harbor cannot satisfy a conflict by silently choosing another port.
func proveDuplicateListenerRejected(ctx context.Context, address netip.Addr, port uint16) error {
	listenConfig := net.ListenConfig{}
	endpoint := net.JoinHostPort(address.String(), strconv.Itoa(int(port)))
	duplicate, err := listenConfig.Listen(ctx, "tcp4", endpoint)
	if err != nil {
		return nil
	}
	_ = duplicate.Close()
	return fmt.Errorf("duplicate listener unexpectedly acquired %s", endpoint)
}

// newSignedPayload creates an unpredictable identity-specific response for one proof run.
func newSignedPayload(secret []byte, address netip.Addr, port uint16) (signedPayload, error) {
	nonceBytes := make([]byte, 16)
	if _, err := rand.Read(nonceBytes); err != nil {
		return signedPayload{}, fmt.Errorf("generate identity nonce: %w", err)
	}
	payload := signedPayload{
		Address: address.String(),
		Port:    port,
		Nonce:   hex.EncodeToString(nonceBytes),
	}
	payload.Signature = signPayload(secret, payload)
	return payload, nil
}

// signPayload authenticates the identity, port, and nonce without exposing the ephemeral key.
func signPayload(secret []byte, payload signedPayload) string {
	mac := hmac.New(sha256.New, secret)
	_, _ = io.WriteString(mac, payload.Address)
	_, _ = io.WriteString(mac, "\x00")
	_, _ = io.WriteString(mac, strconv.Itoa(int(payload.Port)))
	_, _ = io.WriteString(mac, "\x00")
	_, _ = io.WriteString(mac, payload.Nonce)
	return hex.EncodeToString(mac.Sum(nil))
}

// verifyPayload rejects a response from the wrong listener or one changed in transit.
func verifyPayload(secret []byte, payload signedPayload, address netip.Addr, port uint16) error {
	if payload.Address != address.String() || payload.Port != port {
		return fmt.Errorf("identity %s:%d returned endpoint %s:%d", address, port, payload.Address, payload.Port)
	}
	expected := signPayload(secret, signedPayload{Address: payload.Address, Port: payload.Port, Nonce: payload.Nonce})
	expectedBytes, expectedErr := hex.DecodeString(expected)
	actualBytes, actualErr := hex.DecodeString(payload.Signature)
	if expectedErr != nil || actualErr != nil || !hmac.Equal(expectedBytes, actualBytes) {
		return fmt.Errorf("identity %s:%d returned an invalid proof signature", address, port)
	}
	return nil
}

// servePayload accepts one bounded proof connection and writes one response.
func servePayload(ctx context.Context, listener net.Listener, payload signedPayload, result chan<- error) {
	if tcpListener, ok := listener.(*net.TCPListener); ok {
		_ = tcpListener.SetDeadline(deadlineFromContext(ctx))
	}
	connection, err := listener.Accept()
	if err != nil {
		result <- fmt.Errorf("accept proof connection on %s: %w", listener.Addr(), err)
		return
	}
	defer connection.Close()
	_ = connection.SetWriteDeadline(deadlineFromContext(ctx))
	if err := json.NewEncoder(connection).Encode(payload); err != nil {
		result <- fmt.Errorf("write proof payload on %s: %w", listener.Addr(), err)
		return
	}
	result <- nil
}

// receivePayload connects to one exact identity and reads one bounded response.
func receivePayload(ctx context.Context, address netip.Addr, port uint16) (signedPayload, error) {
	endpoint := net.JoinHostPort(address.String(), strconv.Itoa(int(port)))
	dialer := net.Dialer{}
	connection, err := dialer.DialContext(ctx, "tcp4", endpoint)
	if err != nil {
		return signedPayload{}, fmt.Errorf("connect to project identity %s: %w", endpoint, err)
	}
	defer connection.Close()
	_ = connection.SetReadDeadline(deadlineFromContext(ctx))

	reader := bufio.NewReader(io.LimitReader(connection, maximumPayloadBytes))
	var payload signedPayload
	if err := json.NewDecoder(reader).Decode(&payload); err != nil {
		return signedPayload{}, fmt.Errorf("read proof payload from %s: %w", endpoint, err)
	}
	return payload, nil
}

// payloadDigest provides stable evidence without retaining the ephemeral signing secret.
func payloadDigest(payload signedPayload) string {
	encoded, _ := json.Marshal(payload)
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

// deadlineFromContext keeps listener and connection operations inside the proof's fixed budget.
func deadlineFromContext(ctx context.Context) time.Time {
	if deadline, ok := ctx.Deadline(); ok {
		return deadline
	}
	return time.Now().Add(proofTimeout)
}

// assignedAddresses returns exact addresses assigned to all visible network interfaces.
func assignedAddresses() (map[netip.Addr]struct{}, error) {
	assignments, err := observeInterfaceAssignments()
	if err != nil {
		return nil, err
	}
	assigned := make(map[netip.Addr]struct{}, len(assignments))
	for _, assignment := range assignments {
		assigned[assignment.address] = struct{}{}
	}
	return assigned, nil
}

// runtimeEvidence records only non-secret CI and runtime identity fields.
func runtimeEvidence(request ProjectIdentityRequest) RuntimeEvidence {
	return RuntimeEvidence{
		GOOS:               runtime.GOOS,
		GOARCH:             runtime.GOARCH,
		Commit:             request.Commit,
		RunnerName:         request.RunnerName,
		RunnerImage:        request.RunnerImage,
		RunnerImageVersion: request.RunnerImageVersion,
	}
}

// closeListeners releases every listener after success or partial setup failure.
func closeListeners(listeners []net.Listener) {
	for _, listener := range listeners {
		_ = listener.Close()
	}
}
