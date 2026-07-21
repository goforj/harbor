package control

import (
	"encoding/json"
	"errors"
	"fmt"
	"unicode/utf8"

	"github.com/goforj/harbor/internal/domain"
)

const maximumProjectOutputChunkBytes = 64 * 1024

const (
	// MaximumProjectActivityResponseBytes bounds one complete project activity JSON response.
	MaximumProjectActivityResponseBytes = 64 * 1024
	// MaximumProjectActivityWaitMilliseconds leaves response headroom beneath the control transport deadline.
	MaximumProjectActivityWaitMilliseconds uint32 = 25_000
)

// ProjectActivityRequest selects the current session output cursor for one durable project.
type ProjectActivityRequest struct {
	ProjectID        domain.ProjectID `json:"project_id"`
	SessionID        domain.SessionID `json:"session_id,omitempty"`
	Cursor           uint64           `json:"cursor"`
	WaitMilliseconds uint32           `json:"wait_milliseconds,omitempty"`
}

// Validate reports whether the request can continue one current-session output stream.
func (request ProjectActivityRequest) Validate() error {
	if err := request.ProjectID.Validate(); err != nil {
		return err
	}
	if request.WaitMilliseconds > MaximumProjectActivityWaitMilliseconds {
		return fmt.Errorf("project activity wait exceeds %d milliseconds", MaximumProjectActivityWaitMilliseconds)
	}
	if request.SessionID == "" {
		if request.Cursor != 0 {
			return errors.New("project activity cursor requires a session ID")
		}
		return nil
	}
	if err := request.SessionID.Validate(); err != nil {
		return err
	}
	if request.Cursor > uint64(domain.MaximumSequence) {
		return fmt.Errorf("project activity cursor exceeds %d", domain.MaximumSequence)
	}
	return nil
}

// ProjectActivity is the current durable session activity for one registered project.
type ProjectActivity struct {
	ProjectID domain.ProjectID        `json:"project_id"`
	Session   *ProjectSessionActivity `json:"session,omitempty"`
}

// Validate reports whether activity contains only one valid current-session projection.
func (activity ProjectActivity) Validate() error {
	if err := activity.ProjectID.Validate(); err != nil {
		return err
	}
	if activity.Session == nil {
		return nil
	}
	return activity.Session.Validate()
}

// ProjectSessionActivity describes current durable session state and bounded process output.
type ProjectSessionActivity struct {
	ID         domain.SessionID    `json:"id"`
	State      domain.SessionState `json:"state"`
	Generation uint64              `json:"generation"`
	Output     ProjectOutputChunk  `json:"output"`
}

// Validate reports whether the session activity is safe for JavaScript and cursor transport.
func (activity ProjectSessionActivity) Validate() error {
	if err := activity.ID.Validate(); err != nil {
		return err
	}
	if err := activity.State.Validate(); err != nil {
		return err
	}
	if activity.Generation == 0 || activity.Generation > uint64(domain.MaximumSequence) {
		return fmt.Errorf("project session generation must be between 1 and %d", domain.MaximumSequence)
	}
	return activity.Output.Validate()
}

// ProjectOutputChunk is one bounded cursor-addressed view of current process output.
type ProjectOutputChunk struct {
	Available  bool   `json:"available"`
	Historical bool   `json:"historical,omitempty"`
	Reset      bool   `json:"reset"`
	Truncated  bool   `json:"truncated"`
	HasMore    bool   `json:"has_more"`
	NextCursor uint64 `json:"next_cursor"`
	Text       string `json:"text"`
}

// Validate reports whether the output chunk preserves its bounded cursor contract.
func (chunk ProjectOutputChunk) Validate() error {
	if len(chunk.Text) > maximumProjectOutputChunkBytes {
		return fmt.Errorf("project output exceeds %d bytes", maximumProjectOutputChunkBytes)
	}
	if !utf8.ValidString(chunk.Text) {
		return errors.New("project output must be valid UTF-8")
	}
	if chunk.NextCursor > uint64(domain.MaximumSequence) {
		return fmt.Errorf("project output cursor exceeds %d", domain.MaximumSequence)
	}
	if !chunk.Available {
		if !chunk.Historical && (chunk.Truncated || chunk.HasMore || chunk.NextCursor != 0 || chunk.Text != "") {
			return errors.New("unavailable project output must not contain transcript state")
		}
	} else if chunk.Historical {
		return errors.New("live project output must not be marked historical")
	}
	if chunk.NextCursor < uint64(len(chunk.Text)) {
		return errors.New("project output cursor cannot precede returned transcript bytes")
	}
	if chunk.HasMore && chunk.Text == "" {
		return errors.New("project output with more retained data must advance the cursor")
	}
	return nil
}

// projectActivityResponse keeps the method result extensible around current activity.
type projectActivityResponse struct {
	Activity ProjectActivity `json:"activity"`
}

// BoundProjectActivityResponse clips output at a UTF-8 boundary so the complete JSON response remains bounded.
func BoundProjectActivityResponse(activity ProjectActivity) (ProjectActivity, error) {
	if err := activity.Validate(); err != nil {
		return ProjectActivity{}, err
	}
	if err := validateProjectActivityResponseSize(activity); err == nil {
		return activity, nil
	}
	if activity.Session == nil || activity.Session.Output.Text == "" {
		return ProjectActivity{}, errors.New("project activity metadata exceeds its response bound")
	}

	session := *activity.Session
	activity.Session = &session
	output := session.Output
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
		activity.Session.Output.Text = output.Text[:cut]
		activity.Session.Output.NextCursor = startCursor + uint64(cut)
		activity.Session.Output.HasMore = output.HasMore || cut < len(output.Text)
		if validateProjectActivityResponseSize(activity) == nil {
			best = middle
			low = middle + 1
		} else {
			high = middle - 1
		}
	}
	if best < 0 {
		return ProjectActivity{}, errors.New("project activity metadata exceeds its response bound")
	}
	cut := boundaries[best]
	activity.Session.Output.Text = output.Text[:cut]
	activity.Session.Output.NextCursor = startCursor + uint64(cut)
	activity.Session.Output.HasMore = output.HasMore || cut < len(output.Text)
	if err := activity.Validate(); err != nil {
		return ProjectActivity{}, err
	}
	return activity, nil
}

// validateProjectActivityResponseSize proves the exact encoded result fits one bounded control response.
func validateProjectActivityResponseSize(activity ProjectActivity) error {
	payload, err := json.Marshal(projectActivityResponse{Activity: activity})
	if err != nil {
		return fmt.Errorf("encode project activity response: %w", err)
	}
	if len(payload) > MaximumProjectActivityResponseBytes {
		return fmt.Errorf("project activity response exceeds %d bytes", MaximumProjectActivityResponseBytes)
	}
	return nil
}

// validateProjectActivityCorrelation binds a valid response to the requested project and cursor generation.
func validateProjectActivityCorrelation(request ProjectActivityRequest, activity ProjectActivity) error {
	if activity.ProjectID != request.ProjectID {
		return errors.New("project activity does not match the requested project")
	}
	if request.SessionID != "" && activity.Session != nil && activity.Session.ID != request.SessionID && !activity.Session.Output.Reset {
		return errors.New("project activity changed sessions without resetting the output cursor")
	}
	return nil
}
