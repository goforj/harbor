// Package managedsession defines the bounded request/response contract used by
// Harbor and a managed GoForj development process.
package managedsession

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/harbordruntime"
	"github.com/goforj/harbor/internal/rpc"
)

const (
	// SchemaVersion identifies the first managed-session message schema.
	SchemaVersion uint16 = 1
	// CapabilityV1 identifies the first bounded managed-session feature set.
	CapabilityV1 rpc.Capability = "managed-session.v1"
	// CapabilityLaunchContextV1 identifies the optional inherited launch-ticket proof.
	CapabilityLaunchContextV1 rpc.Capability = "managed-session.launch-context.v1"
	// CapabilityEventsV1 identifies the optional ordered state and output event stream.
	CapabilityEventsV1 rpc.Capability = "managed-session.events.v1"
	// CapabilityRuntimePlanV1 identifies the optional semantic endpoint-assignment plan.
	CapabilityRuntimePlanV1 rpc.Capability = "managed-session.runtime-plan.v1"
	// MethodRegister attaches one authenticated GoForj process to a Harbor session.
	MethodRegister = "managed-session.v1.register"
	// MethodReplacePublications replaces every private publication observed by a session.
	MethodReplacePublications = "managed-session.v1.publications.replace"
	// MethodBarrier asks Harbor whether a named lifecycle barrier has been acknowledged.
	MethodBarrier = "managed-session.v1.barrier"
	// MethodRuntimePlan asks Harbor for one authenticated semantic runtime assignment plan.
	MethodRuntimePlan = "managed-session.v1.runtime-plan"

	maximumManagedSessionPayloadBytes = 1 << 20
	maximumManagedSessionApps         = 256
	maximumManagedSessionRuntimes     = 256
	maximumManagedSessionCapabilities = 128
	maximumManagedSessionTokenBytes   = 512
	maximumManagedSessionRootBytes    = 4096
	maximumManagedPublications        = 256
	maximumManagedSessionEventText    = 64 * 1024
)

// ActiveApp identifies one App and the runtime IDs GoForj selected for this session.
type ActiveApp struct {
	ID         domain.AppID `json:"id"`
	RuntimeIDs []string     `json:"runtime_ids"`
}

// Validate reports whether the App/runtime set is a complete deterministic identity projection.
func (app ActiveApp) Validate() error {
	if err := app.ID.Validate(); err != nil {
		return err
	}
	if app.RuntimeIDs == nil {
		return fmt.Errorf("managed session App %q runtime IDs must be initialized", app.ID)
	}
	if len(app.RuntimeIDs) > maximumManagedSessionRuntimes {
		return fmt.Errorf("managed session App %q contains more than %d runtimes", app.ID, maximumManagedSessionRuntimes)
	}
	for index, runtimeID := range app.RuntimeIDs {
		if err := validateManagedSessionToken(fmt.Sprintf("managed session App %q runtime %d", app.ID, index+1), runtimeID, maximumManagedSessionTokenBytes); err != nil {
			return err
		}
		if index > 0 && app.RuntimeIDs[index-1] >= runtimeID {
			return fmt.Errorf("managed session App %q runtime IDs must be sorted and unique", app.ID)
		}
	}
	return nil
}

// RegisterRequest carries the non-secret identity and proposed runtime set for one GoForj attachment.
type RegisterRequest struct {
	SchemaVersion             uint16              `json:"schema_version"`
	ProjectID                 domain.ProjectID    `json:"project_id"`
	SessionID                 domain.SessionID    `json:"session_id"`
	ProjectRoot               string              `json:"project_root"`
	ExpectedSessionGeneration uint64              `json:"expected_session_generation"`
	DescriptorDigest          string              `json:"descriptor_digest"`
	ClientNonce               string              `json:"client_nonce"`
	Owner                     domain.SessionOwner `json:"owner"`
	Capabilities              []rpc.Capability    `json:"capabilities"`
	ActiveApps                []ActiveApp         `json:"active_apps"`
	// LaunchTicket is present only when the negotiated launch-context capability is selected.
	LaunchTicket string `json:"launch_ticket,omitempty"`
}

// Validate reports whether a registration can be joined to one exact durable project session.
func (request RegisterRequest) Validate() error {
	if request.SchemaVersion != SchemaVersion {
		return fmt.Errorf("managed session registration schema version %d is unsupported", request.SchemaVersion)
	}
	if err := request.ProjectID.Validate(); err != nil {
		return err
	}
	if err := request.SessionID.Validate(); err != nil {
		return err
	}
	if err := validateManagedSessionRoot(request.ProjectRoot); err != nil {
		return err
	}
	if request.ExpectedSessionGeneration == 0 || request.ExpectedSessionGeneration >= rpc.MaximumSequence {
		return fmt.Errorf("managed session expected generation must be between 1 and %d", rpc.MaximumSequence-1)
	}
	if err := validateManagedSessionDigest(request.DescriptorDigest); err != nil {
		return err
	}
	if err := validateManagedSessionToken("managed session client nonce", request.ClientNonce, maximumManagedSessionTokenBytes); err != nil {
		return err
	}
	if err := request.Owner.Validate(); err != nil {
		return err
	}
	if err := validateManagedSessionCapabilities(request.Capabilities); err != nil {
		return err
	}
	if request.ActiveApps == nil {
		return errors.New("managed session active Apps must be initialized")
	}
	if len(request.ActiveApps) > maximumManagedSessionApps {
		return fmt.Errorf("managed session contains more than %d Apps", maximumManagedSessionApps)
	}
	for index, app := range request.ActiveApps {
		if err := app.Validate(); err != nil {
			return fmt.Errorf("managed session active App %d: %w", index+1, err)
		}
		if index > 0 && request.ActiveApps[index-1].ID >= app.ID {
			return errors.New("managed session active Apps must be sorted and unique")
		}
	}
	launchCapability := containsManagedSessionCapability(request.Capabilities, CapabilityLaunchContextV1)
	if launchCapability && request.Owner == domain.SessionOwnerHarbor && request.LaunchTicket == "" {
		return errors.New("managed session launch-context capability requires a launch ticket")
	}
	if request.LaunchTicket != "" {
		if request.Owner != domain.SessionOwnerHarbor {
			return errors.New("managed session launch ticket requires Harbor ownership")
		}
		if !containsManagedSessionCapability(request.Capabilities, CapabilityLaunchContextV1) {
			return errors.New("managed session launch ticket requires the launch-context capability")
		}
		if err := validateManagedSessionToken("managed session launch ticket", request.LaunchTicket, maximumManagedSessionTokenBytes); err != nil {
			return err
		}
	}
	return nil
}

// RegisterResponse returns the attached-session fence and one short-lived credential.
type RegisterResponse struct {
	SchemaVersion    uint16                                 `json:"schema_version"`
	Fence            harbordruntime.ManagedPublicationFence `json:"fence"`
	AttachmentTicket string                                 `json:"attachment_ticket"`
}

// Validate reports whether a registration response contains only ephemeral attachment authority.
func (response RegisterResponse) Validate() error {
	if response.SchemaVersion != SchemaVersion {
		return fmt.Errorf("managed session registration response schema version %d is unsupported", response.SchemaVersion)
	}
	if err := response.Fence.Validate(); err != nil {
		return err
	}
	return validateManagedSessionToken("managed session attachment ticket", response.AttachmentTicket, maximumManagedSessionTokenBytes)
}

// ValidateRegisterCorrelation binds an attachment response to the requested project, session, and next generation.
func ValidateRegisterCorrelation(request RegisterRequest, response RegisterResponse) error {
	if err := request.Validate(); err != nil {
		return fmt.Errorf("validate managed session registration request: %w", err)
	}
	if err := response.Validate(); err != nil {
		return fmt.Errorf("validate managed session registration response: %w", err)
	}
	if response.Fence.ProjectID != request.ProjectID || response.Fence.SessionID != request.SessionID {
		return errors.New("managed session registration response does not match the requested project and session")
	}
	if request.ExpectedSessionGeneration == rpc.MaximumSequence || response.Fence.SessionGeneration != request.ExpectedSessionGeneration+1 {
		return errors.New("managed session registration response does not match the requested next generation")
	}
	return nil
}

// ReplacePublicationsRequest carries a complete replacement set for one attached session.
type ReplacePublicationsRequest struct {
	SchemaVersion uint16                                      `json:"schema_version"`
	Fence         harbordruntime.ManagedPublicationFence      `json:"fence"`
	Publications  []harbordruntime.ManagedEndpointPublication `json:"publications"`
}

// Validate reports whether every publication is bounded by the exact attached-session fence.
func (request ReplacePublicationsRequest) Validate() error {
	if request.SchemaVersion != SchemaVersion {
		return fmt.Errorf("managed session publication schema version %d is unsupported", request.SchemaVersion)
	}
	if err := request.Fence.Validate(); err != nil {
		return err
	}
	if request.Publications == nil {
		return errors.New("managed session publications must be initialized")
	}
	if len(request.Publications) > maximumManagedPublications {
		return fmt.Errorf("managed session contains more than %d publications", maximumManagedPublications)
	}
	seen := make(map[string]struct{}, len(request.Publications))
	for index, publication := range request.Publications {
		if err := publication.Validate(); err != nil {
			return fmt.Errorf("managed session publication %d: %w", index+1, err)
		}
		if publication.Fence != request.Fence {
			return fmt.Errorf("managed session publication %q does not match the request fence", publication.EndpointID)
		}
		if _, duplicate := seen[publication.EndpointID]; duplicate {
			return fmt.Errorf("managed session publication endpoint %q is duplicated", publication.EndpointID)
		}
		seen[publication.EndpointID] = struct{}{}
	}
	return nil
}

// ReplacePublicationsResponse acknowledges one complete publication replacement.
type ReplacePublicationsResponse struct {
	SchemaVersion    uint16                                 `json:"schema_version"`
	Fence            harbordruntime.ManagedPublicationFence `json:"fence"`
	Accepted         bool                                   `json:"accepted"`
	PublicationCount uint16                                 `json:"publication_count"`
}

// Validate reports whether a publication acknowledgement is tied to one attached-session fence.
func (response ReplacePublicationsResponse) Validate() error {
	if response.SchemaVersion != SchemaVersion {
		return fmt.Errorf("managed session publication response schema version %d is unsupported", response.SchemaVersion)
	}
	if err := response.Fence.Validate(); err != nil {
		return err
	}
	if response.PublicationCount > maximumManagedPublications {
		return fmt.Errorf("managed session publication count exceeds %d", maximumManagedPublications)
	}
	if !response.Accepted && response.PublicationCount != 0 {
		return errors.New("rejected managed session publication acknowledgement must not report publications")
	}
	return nil
}

// ValidateReplacePublicationsCorrelation binds an acknowledgement to one exact replacement request.
func ValidateReplacePublicationsCorrelation(request ReplacePublicationsRequest, response ReplacePublicationsResponse) error {
	if err := request.Validate(); err != nil {
		return fmt.Errorf("validate managed session publication request: %w", err)
	}
	if err := response.Validate(); err != nil {
		return fmt.Errorf("validate managed session publication response: %w", err)
	}
	if response.Fence != request.Fence {
		return errors.New("managed session publication response does not match the request fence")
	}
	if response.Accepted && int(response.PublicationCount) != len(request.Publications) {
		return errors.New("managed session publication response count does not match the replacement")
	}
	return nil
}

// EventKind identifies one bounded managed-session event shape.
type EventKind string

const (
	// EventKindLogChunk carries pre-decoration output from one App or watcher.
	EventKindLogChunk EventKind = "log.chunk"
	// EventKindOutputGap records an inclusive range omitted by bounded event backpressure.
	EventKindOutputGap EventKind = "output.gap"
)

// Validate reports whether an event kind is understood by this protocol version.
func (kind EventKind) Validate() error {
	switch kind {
	case EventKindLogChunk, EventKindOutputGap:
		return nil
	default:
		return fmt.Errorf("unsupported managed session event kind %q", kind)
	}
}

// EventStream identifies the honest source stream for a managed-session output event.
type EventStream string

const (
	// EventStreamStdout identifies bytes captured from a process stdout pipe.
	EventStreamStdout EventStream = "stdout"
	// EventStreamStderr identifies bytes captured from a process stderr pipe.
	EventStreamStderr EventStream = "stderr"
	// EventStreamPTYCombined identifies a terminal-owned stream whose channels cannot be separated.
	EventStreamPTYCombined EventStream = "pty/combined"
)

// Validate reports whether an output stream preserves its source provenance.
func (stream EventStream) Validate() error {
	switch stream {
	case EventStreamStdout, EventStreamStderr, EventStreamPTYCombined:
		return nil
	default:
		return fmt.Errorf("unsupported managed session event stream %q", stream)
	}
}

// Event carries one ordered, stable-session state or pre-decoration output record.
// Sequence is assigned before send and is the only ordering authority; timestamps are diagnostic metadata.
type Event struct {
	SchemaVersion uint16           `json:"schema_version"`
	ProjectID     domain.ProjectID `json:"project_id"`
	SessionID     domain.SessionID `json:"session_id"`
	Sequence      uint64           `json:"sequence"`
	Timestamp     string           `json:"timestamp"`
	Kind          EventKind        `json:"kind"`
	AppID         string           `json:"app_id,omitempty"`
	WatcherID     string           `json:"watcher_id,omitempty"`
	Stream        EventStream      `json:"stream,omitempty"`
	// Text is normalized valid UTF-8; byte-exact binary output is outside this v1 contract.
	Text         string `json:"text,omitempty"`
	DroppedFrom  uint64 `json:"dropped_from,omitempty"`
	DroppedTo    uint64 `json:"dropped_to,omitempty"`
	DroppedCount uint64 `json:"dropped_count,omitempty"`
}

// Validate reports whether an event contains complete bounded identity, ordering, and gap semantics.
func (event Event) Validate() error {
	if event.SchemaVersion != SchemaVersion {
		return fmt.Errorf("managed session event schema version %d is unsupported", event.SchemaVersion)
	}
	if err := event.ProjectID.Validate(); err != nil {
		return err
	}
	if err := event.SessionID.Validate(); err != nil {
		return err
	}
	if event.Sequence == 0 || event.Sequence > rpc.MaximumSequence {
		return fmt.Errorf("managed session event sequence must be between 1 and %d", rpc.MaximumSequence)
	}
	if err := validateManagedSessionEventTimestamp(event.Timestamp); err != nil {
		return err
	}
	if err := event.Kind.Validate(); err != nil {
		return err
	}
	if event.AppID == "" && event.WatcherID == "" {
		return errors.New("managed session event source identity is required")
	}
	if event.AppID != "" {
		if err := validateManagedSessionToken("managed session event App ID", event.AppID, maximumManagedSessionTokenBytes); err != nil {
			return err
		}
	}
	if event.WatcherID != "" {
		if err := validateManagedSessionToken("managed session event watcher ID", event.WatcherID, maximumManagedSessionTokenBytes); err != nil {
			return err
		}
	}
	switch event.Kind {
	case EventKindLogChunk:
		if err := event.Stream.Validate(); err != nil {
			return err
		}
		if event.Text == "" {
			return errors.New("managed session log event text is required")
		}
		if !utf8.ValidString(event.Text) || len(event.Text) > maximumManagedSessionEventText {
			return fmt.Errorf("managed session log event text must be valid UTF-8 of at most %d bytes", maximumManagedSessionEventText)
		}
		if event.DroppedFrom != 0 || event.DroppedTo != 0 || event.DroppedCount != 0 {
			return errors.New("managed session log event must not carry a dropped range")
		}
	case EventKindOutputGap:
		if err := event.Stream.Validate(); err != nil {
			return err
		}
		if event.Text != "" {
			return errors.New("managed session output gap must not carry text")
		}
		if event.DroppedFrom == 0 || event.DroppedTo < event.DroppedFrom || event.DroppedTo >= event.Sequence {
			return errors.New("managed session output gap range must precede its event sequence")
		}
		if event.DroppedCount == 0 || event.DroppedCount != event.DroppedTo-event.DroppedFrom+1 {
			return errors.New("managed session output gap dropped count does not match its range")
		}
	}
	return nil
}

// MarshalEvent validates and encodes one managed-session event object.
func MarshalEvent(event Event) ([]byte, error) {
	return marshalManagedSessionObject("managed session event", event, event.Validate)
}

// DecodeEvent strictly decodes and validates one managed-session event object.
func DecodeEvent(payload []byte) (Event, error) {
	var event Event
	if err := decodeManagedSessionObject(payload, "managed session event", &event); err != nil {
		return Event{}, err
	}
	if err := event.Validate(); err != nil {
		return Event{}, err
	}
	return event, nil
}

// BarrierPhase identifies a lifecycle synchronization point understood by this protocol version.
type BarrierPhase string

const (
	// BarrierPhaseCompose is the point after Compose starts and before setup or migration continues.
	BarrierPhaseCompose BarrierPhase = "compose"
)

// Validate reports whether the barrier phase is known to this protocol version.
func (phase BarrierPhase) Validate() error {
	if phase != BarrierPhaseCompose {
		return fmt.Errorf("unsupported managed session barrier phase %q", phase)
	}
	return nil
}

// BarrierRequest asks Harbor to acknowledge a GoForj lifecycle barrier after independent observation.
type BarrierRequest struct {
	SchemaVersion           uint16                                 `json:"schema_version"`
	Fence                   harbordruntime.ManagedPublicationFence `json:"fence"`
	Phase                   BarrierPhase                           `json:"phase"`
	AcceptedProjectIdentity string                                 `json:"accepted_project_identity"`
}

// Validate reports whether a barrier request contains a bounded Compose identity and exact session fence.
func (request BarrierRequest) Validate() error {
	if request.SchemaVersion != SchemaVersion {
		return fmt.Errorf("managed session barrier schema version %d is unsupported", request.SchemaVersion)
	}
	if err := request.Fence.Validate(); err != nil {
		return err
	}
	if err := request.Phase.Validate(); err != nil {
		return err
	}
	return validateManagedSessionToken("managed session accepted project identity", request.AcceptedProjectIdentity, maximumManagedSessionTokenBytes)
}

// BarrierResponse acknowledges whether Harbor has completed the requested route barrier.
type BarrierResponse struct {
	SchemaVersion uint16                                 `json:"schema_version"`
	Fence         harbordruntime.ManagedPublicationFence `json:"fence"`
	Phase         BarrierPhase                           `json:"phase"`
	Acknowledged  bool                                   `json:"acknowledged"`
}

// Validate reports whether a barrier response correlates to the requested session and phase.
func (response BarrierResponse) Validate() error {
	if response.SchemaVersion != SchemaVersion {
		return fmt.Errorf("managed session barrier response schema version %d is unsupported", response.SchemaVersion)
	}
	if err := response.Fence.Validate(); err != nil {
		return err
	}
	return response.Phase.Validate()
}

// ValidateBarrierCorrelation binds a barrier acknowledgement to one exact request fence and phase.
func ValidateBarrierCorrelation(request BarrierRequest, response BarrierResponse) error {
	if err := request.Validate(); err != nil {
		return fmt.Errorf("validate managed session barrier request: %w", err)
	}
	if err := response.Validate(); err != nil {
		return fmt.Errorf("validate managed session barrier response: %w", err)
	}
	if response.Fence != request.Fence || response.Phase != request.Phase {
		return errors.New("managed session barrier response does not match the request")
	}
	return nil
}

// DecodeRegisterRequest strictly decodes one registration object and validates its complete shape.
func DecodeRegisterRequest(payload []byte) (RegisterRequest, error) {
	var request RegisterRequest
	if err := decodeManagedSessionObject(payload, "managed session registration", &request); err != nil {
		return RegisterRequest{}, err
	}
	if err := request.Validate(); err != nil {
		return RegisterRequest{}, err
	}
	return request, nil
}

// DecodeRegisterResponse strictly decodes one registration response and validates its ephemeral authority.
func DecodeRegisterResponse(payload []byte) (RegisterResponse, error) {
	var response RegisterResponse
	if err := decodeManagedSessionObject(payload, "managed session registration response", &response); err != nil {
		return RegisterResponse{}, err
	}
	if err := response.Validate(); err != nil {
		return RegisterResponse{}, err
	}
	return response, nil
}

// DecodeReplacePublicationsRequest strictly decodes one complete publication replacement.
func DecodeReplacePublicationsRequest(payload []byte) (ReplacePublicationsRequest, error) {
	var request ReplacePublicationsRequest
	if err := decodeManagedSessionObject(payload, "managed session publication replacement", &request); err != nil {
		return ReplacePublicationsRequest{}, err
	}
	if err := request.Validate(); err != nil {
		return ReplacePublicationsRequest{}, err
	}
	return request, nil
}

// DecodeReplacePublicationsResponse strictly decodes one publication acknowledgement.
func DecodeReplacePublicationsResponse(payload []byte) (ReplacePublicationsResponse, error) {
	var response ReplacePublicationsResponse
	if err := decodeManagedSessionObject(payload, "managed session publication response", &response); err != nil {
		return ReplacePublicationsResponse{}, err
	}
	if err := response.Validate(); err != nil {
		return ReplacePublicationsResponse{}, err
	}
	return response, nil
}

// DecodeBarrierRequest strictly decodes one lifecycle barrier request.
func DecodeBarrierRequest(payload []byte) (BarrierRequest, error) {
	var request BarrierRequest
	if err := decodeManagedSessionObject(payload, "managed session barrier", &request); err != nil {
		return BarrierRequest{}, err
	}
	if err := request.Validate(); err != nil {
		return BarrierRequest{}, err
	}
	return request, nil
}

// DecodeBarrierResponse strictly decodes one lifecycle barrier acknowledgement.
func DecodeBarrierResponse(payload []byte) (BarrierResponse, error) {
	var response BarrierResponse
	if err := decodeManagedSessionObject(payload, "managed session barrier response", &response); err != nil {
		return BarrierResponse{}, err
	}
	if err := response.Validate(); err != nil {
		return BarrierResponse{}, err
	}
	return response, nil
}

// MarshalRegisterRequest validates and encodes one registration object.
func MarshalRegisterRequest(request RegisterRequest) ([]byte, error) {
	return marshalManagedSessionObject("managed session registration", request, request.Validate)
}

// MarshalRegisterResponse validates and encodes one registration response.
func MarshalRegisterResponse(response RegisterResponse) ([]byte, error) {
	return marshalManagedSessionObject("managed session registration response", response, response.Validate)
}

// MarshalReplacePublicationsRequest validates and encodes one complete publication replacement.
func MarshalReplacePublicationsRequest(request ReplacePublicationsRequest) ([]byte, error) {
	return marshalManagedSessionObject("managed session publication replacement", request, request.Validate)
}

// MarshalReplacePublicationsResponse validates and encodes one publication acknowledgement.
func MarshalReplacePublicationsResponse(response ReplacePublicationsResponse) ([]byte, error) {
	return marshalManagedSessionObject("managed session publication response", response, response.Validate)
}

// MarshalBarrierRequest validates and encodes one lifecycle barrier request.
func MarshalBarrierRequest(request BarrierRequest) ([]byte, error) {
	return marshalManagedSessionObject("managed session barrier", request, request.Validate)
}

// MarshalBarrierResponse validates and encodes one lifecycle barrier acknowledgement.
func MarshalBarrierResponse(response BarrierResponse) ([]byte, error) {
	return marshalManagedSessionObject("managed session barrier response", response, response.Validate)
}

// decodeManagedSessionObject enforces a bounded object, duplicate-free JSON shape before semantic validation.
func decodeManagedSessionObject(payload []byte, label string, target any) error {
	if len(payload) == 0 || len(payload) > maximumManagedSessionPayloadBytes {
		return fmt.Errorf("%s exceeds its bounded object shape", label)
	}
	if err := rejectDuplicateManagedSessionFields(payload, label); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode %s: %w", label, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("decode %s: trailing JSON value", label)
		}
		return fmt.Errorf("decode %s trailing data: %w", label, err)
	}
	return nil
}

// marshalManagedSessionObject validates a message before it can become a wire payload.
func marshalManagedSessionObject(label string, value any, validate func() error) ([]byte, error) {
	if err := validate(); err != nil {
		return nil, fmt.Errorf("validate %s: %w", label, err)
	}
	payload, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encode %s: %w", label, err)
	}
	if len(payload) > maximumManagedSessionPayloadBytes {
		return nil, fmt.Errorf("encode %s exceeds its bounded object shape", label)
	}
	return payload, nil
}

// rejectDuplicateManagedSessionFields scans nested JSON objects so duplicate keys cannot be hidden by decoding.
func rejectDuplicateManagedSessionFields(payload []byte, label string) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	if err := inspectManagedSessionJSONValue(decoder, label, true); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("decode %s: trailing JSON value", label)
		}
		return fmt.Errorf("decode %s trailing data: %w", label, err)
	}
	return nil
}

// inspectManagedSessionJSONValue walks one JSON value and rejects duplicate object members.
func inspectManagedSessionJSONValue(decoder *json.Decoder, label string, requireObject bool) error {
	token, err := decoder.Token()
	if err != nil {
		return fmt.Errorf("decode %s: %w", label, err)
	}
	delimiter, isDelimiter := token.(json.Delim)
	if !isDelimiter {
		if requireObject {
			return fmt.Errorf("%s must be an object", label)
		}
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			fieldToken, err := decoder.Token()
			if err != nil {
				return fmt.Errorf("decode %s field: %w", label, err)
			}
			field, ok := fieldToken.(string)
			if !ok {
				return fmt.Errorf("%s field name must be a string", label)
			}
			if _, duplicate := seen[field]; duplicate {
				return fmt.Errorf("%s contains duplicate field %q", label, field)
			}
			seen[field] = struct{}{}
			if err := inspectManagedSessionJSONValue(decoder, label, false); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil {
			return fmt.Errorf("decode %s object end: %w", label, err)
		}
		if closing != json.Delim('}') {
			return fmt.Errorf("decode %s object is not terminated", label)
		}
	case '[':
		if requireObject {
			return fmt.Errorf("%s must be an object", label)
		}
		for decoder.More() {
			if err := inspectManagedSessionJSONValue(decoder, label, false); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil {
			return fmt.Errorf("decode %s array end: %w", label, err)
		}
		if closing != json.Delim(']') {
			return fmt.Errorf("decode %s array is not terminated", label)
		}
	default:
		return fmt.Errorf("decode %s contains unsupported JSON delimiter %q", label, delimiter)
	}
	return nil
}

// validateManagedSessionCapabilities requires deterministic capability order without constraining future names.
func validateManagedSessionCapabilities(capabilities []rpc.Capability) error {
	if capabilities == nil {
		return errors.New("managed session capabilities must be initialized")
	}
	if len(capabilities) > maximumManagedSessionCapabilities {
		return fmt.Errorf("managed session contains more than %d capabilities", maximumManagedSessionCapabilities)
	}
	canonical, err := rpc.CanonicalCapabilities(capabilities)
	if err != nil {
		return err
	}
	if !slices.Equal(canonical, capabilities) {
		return errors.New("managed session capabilities must be sorted and unique")
	}
	return nil
}

// containsManagedSessionCapability reports whether one capability was negotiated into a request.
func containsManagedSessionCapability(capabilities []rpc.Capability, target rpc.Capability) bool {
	for _, capability := range capabilities {
		if capability == target {
			return true
		}
	}
	return false
}

// validateManagedSessionRoot keeps path authority canonical without resolving filesystem state during decoding.
func validateManagedSessionRoot(root string) error {
	if root == "" {
		return errors.New("managed session project root is required")
	}
	if !utf8.ValidString(root) || len(root) > maximumManagedSessionRootBytes {
		return fmt.Errorf("managed session project root must be valid UTF-8 of at most %d bytes", maximumManagedSessionRootBytes)
	}
	for _, character := range root {
		if unicode.IsControl(character) {
			return errors.New("managed session project root must not contain control characters")
		}
	}
	if !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return errors.New("managed session project root must be a canonical absolute path")
	}
	return nil
}

// validateManagedSessionDigest accepts the bare lowercase SHA-256 form persisted by Harbor.
func validateManagedSessionDigest(digest string) error {
	if len(digest) != 64 {
		return errors.New("managed session descriptor digest must be 64 lowercase hexadecimal characters")
	}
	if _, err := hex.DecodeString(digest); err != nil || strings.ToLower(digest) != digest {
		return errors.New("managed session descriptor digest must be 64 lowercase hexadecimal characters")
	}
	return nil
}

// validateManagedSessionEventTimestamp accepts only an RFC3339 timestamp with an explicit UTC zone.
func validateManagedSessionEventTimestamp(timestamp string) error {
	parsed, err := time.Parse(time.RFC3339Nano, timestamp)
	if err != nil {
		return errors.New("managed session event timestamp must be RFC3339 UTC")
	}
	_, offset := parsed.Zone()
	if offset != 0 {
		return errors.New("managed session event timestamp must be RFC3339 UTC")
	}
	return nil
}

// validateManagedSessionToken rejects whitespace and control text before it reaches logs or authority decisions.
func validateManagedSessionToken(name, value string, maximum int) error {
	if value == "" {
		return fmt.Errorf("%s is required", name)
	}
	if !utf8.ValidString(value) || len(value) > maximum {
		return fmt.Errorf("%s must be valid UTF-8 of at most %d bytes", name, maximum)
	}
	for _, character := range value {
		if unicode.IsSpace(character) || unicode.IsControl(character) {
			return fmt.Errorf("%s must not contain whitespace or control characters", name)
		}
	}
	return nil
}
