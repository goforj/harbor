package control

import (
	"encoding/json"
	"errors"
	"fmt"
	"unicode/utf8"

	"github.com/goforj/harbor/internal/domain"
)

const maximumServiceLogOutputBytes = 64 * 1024

const (
	// MaximumServiceLogsResponseBytes bounds one complete service-log JSON response.
	MaximumServiceLogsResponseBytes = 64 * 1024
	// MaximumServiceLogsWaitMilliseconds bounds one held service-log read.
	MaximumServiceLogsWaitMilliseconds uint32 = 25_000
)

// ServiceLogsRequest selects one current-session Compose service output cursor.
type ServiceLogsRequest struct {
	ProjectID        domain.ProjectID `json:"project_id"`
	SessionID        domain.SessionID `json:"session_id,omitempty"`
	ServiceID        domain.ServiceID `json:"service_id"`
	Cursor           uint64           `json:"cursor"`
	WaitMilliseconds uint32           `json:"wait_milliseconds,omitempty"`
}

// Validate reports whether the request contains only bounded durable identities.
func (request ServiceLogsRequest) Validate() error {
	if err := request.ProjectID.Validate(); err != nil {
		return err
	}
	if err := request.ServiceID.Validate(); err != nil {
		return err
	}
	if request.WaitMilliseconds > MaximumServiceLogsWaitMilliseconds {
		return fmt.Errorf("service log wait exceeds %d milliseconds", MaximumServiceLogsWaitMilliseconds)
	}
	if request.SessionID == "" {
		if request.Cursor != 0 {
			return errors.New("service log cursor requires a session ID")
		}
		return nil
	}
	if err := request.SessionID.Validate(); err != nil {
		return err
	}
	if request.Cursor > uint64(domain.MaximumSequence) {
		return fmt.Errorf("service log cursor exceeds %d", domain.MaximumSequence)
	}
	return nil
}

// ServiceLogs is one bounded current-session Compose service output view.
type ServiceLogs struct {
	ProjectID domain.ProjectID      `json:"project_id"`
	ServiceID domain.ServiceID      `json:"service_id"`
	SessionID domain.SessionID      `json:"session_id,omitempty"`
	Supported bool                  `json:"supported"`
	Available bool                  `json:"available"`
	Problem   *ServiceLogProblem    `json:"problem,omitempty"`
	Output    ServiceLogOutputChunk `json:"output"`
	Ports     []ServicePort         `json:"ports"`
}

// ServicePort is one non-secret current Compose port mapping.
type ServicePort struct {
	Address  string `json:"address,omitempty"`
	Private  uint16 `json:"private"`
	Public   uint16 `json:"public,omitempty"`
	Protocol string `json:"protocol"`
	Replica  int    `json:"replica"`
}

// ServiceLogProblem describes a bounded runtime or stream failure.
type ServiceLogProblem struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

// Validate reports whether a problem is safe for local desktop rendering.
func (problem ServiceLogProblem) Validate() error {
	return (domain.Problem{
		Code:      domain.ProblemCode(problem.Code),
		Message:   problem.Message,
		Retryable: problem.Retryable,
	}).Validate()
}

// ServiceLogOutputChunk is one bounded UTF-8 cursor-addressed transcript chunk.
type ServiceLogOutputChunk struct {
	Available  bool   `json:"available"`
	Reset      bool   `json:"reset"`
	Truncated  bool   `json:"truncated"`
	HasMore    bool   `json:"has_more"`
	NextCursor uint64 `json:"next_cursor"`
	Text       string `json:"text"`
}

// Validate reports whether the transcript preserves its bounded cursor contract.
func (chunk ServiceLogOutputChunk) Validate() error {
	if len(chunk.Text) > maximumServiceLogOutputBytes || !utf8.ValidString(chunk.Text) {
		return errors.New("service log output must be bounded valid UTF-8")
	}
	if chunk.NextCursor > uint64(domain.MaximumSequence) {
		return fmt.Errorf("service log output cursor exceeds %d", domain.MaximumSequence)
	}
	if !chunk.Available && (chunk.Reset || chunk.Truncated || chunk.HasMore || chunk.NextCursor != 0 || chunk.Text != "") {
		if chunk.Truncated || chunk.HasMore || chunk.NextCursor != 0 || chunk.Text != "" {
			return errors.New("unavailable service log output must not contain transcript data")
		}
		return nil
	}
	if chunk.Available && chunk.NextCursor < uint64(len(chunk.Text)) {
		return errors.New("service log output cursor cannot precede returned transcript bytes")
	}
	if chunk.Available && chunk.HasMore && chunk.Text == "" {
		return errors.New("service log output with more retained data must advance the cursor")
	}
	return nil
}

// Validate reports whether service output contains one safe current-session projection.
func (logs ServiceLogs) Validate() error {
	if err := logs.ProjectID.Validate(); err != nil {
		return err
	}
	if err := logs.ServiceID.Validate(); err != nil {
		return err
	}
	if logs.SessionID != "" {
		if err := logs.SessionID.Validate(); err != nil {
			return err
		}
	}
	if logs.Problem != nil {
		if err := logs.Problem.Validate(); err != nil {
			return err
		}
		if logs.Available {
			return errors.New("available service logs must not contain a problem")
		}
	}
	if logs.Available {
		if !logs.Supported || logs.SessionID == "" || !logs.Output.Available {
			return errors.New("available service logs require support, a session, and available output")
		}
	} else if logs.Output.Available {
		return errors.New("unavailable service logs must not contain available output")
	}
	for _, port := range logs.Ports {
		if port.Private == 0 || port.Protocol == "" || port.Replica <= 0 {
			return errors.New("service port must contain a private port, protocol, and replica")
		}
	}
	return logs.Output.Validate()
}

// serviceLogsResponse keeps the method result extensible around one current service stream.
type serviceLogsResponse struct {
	Logs ServiceLogs `json:"logs"`
}

// BoundServiceLogsResponse clips output at a UTF-8 boundary so the complete JSON response remains bounded.
func BoundServiceLogsResponse(logs ServiceLogs) (ServiceLogs, error) {
	if err := logs.Validate(); err != nil {
		return ServiceLogs{}, err
	}
	if err := validateServiceLogsResponseSize(logs); err == nil {
		return logs, nil
	}
	if logs.Output.Text == "" {
		return ServiceLogs{}, errors.New("service log metadata exceeds its response bound")
	}

	output := logs.Output
	startCursor := output.NextCursor - uint64(len(output.Text))
	boundaries := make([]int, 1, len(output.Text)+1)
	for index := range output.Text {
		if index != 0 {
			boundaries = append(boundaries, index)
		}
	}
	boundaries = append(boundaries, len(output.Text))
	best := -1
	low, high := 0, len(boundaries)-1
	for low <= high {
		middle := low + (high-low)/2
		cut := boundaries[middle]
		logs.Output.Text = output.Text[:cut]
		logs.Output.NextCursor = startCursor + uint64(cut)
		logs.Output.HasMore = output.HasMore || cut < len(output.Text)
		if validateServiceLogsResponseSize(logs) == nil {
			best = middle
			low = middle + 1
		} else {
			high = middle - 1
		}
	}
	if best < 0 {
		return ServiceLogs{}, errors.New("service log metadata exceeds its response bound")
	}
	cut := boundaries[best]
	logs.Output.Text = output.Text[:cut]
	logs.Output.NextCursor = startCursor + uint64(cut)
	logs.Output.HasMore = output.HasMore || cut < len(output.Text)
	if err := logs.Validate(); err != nil {
		return ServiceLogs{}, err
	}
	return logs, nil
}

// validateServiceLogsCorrelation binds a response to the requested project, service, and session generation.
func validateServiceLogsCorrelation(request ServiceLogsRequest, logs ServiceLogs) error {
	if logs.ProjectID != request.ProjectID || logs.ServiceID != request.ServiceID {
		return errors.New("service logs do not match the requested project and service")
	}
	if request.SessionID != "" && logs.SessionID != "" && logs.SessionID != request.SessionID && !logs.Output.Reset {
		return errors.New("service logs changed sessions without resetting the output cursor")
	}
	return nil
}

// validateServiceLogsResponseSize proves the exact encoded result fits one bounded control response.
func validateServiceLogsResponseSize(logs ServiceLogs) error {
	payload, err := json.Marshal(serviceLogsResponse{Logs: logs})
	if err != nil {
		return fmt.Errorf("encode service logs response: %w", err)
	}
	if len(payload) > MaximumServiceLogsResponseBytes {
		return fmt.Errorf("service logs response exceeds %d bytes", MaximumServiceLogsResponseBytes)
	}
	return nil
}
