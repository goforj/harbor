package control

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/rpc"
)

const maximumRegistrationPathBytes = 32 << 10

// RegisterProjectRequest identifies one canonical checkout selected by a human-facing client.
type RegisterProjectRequest struct {
	Path string `json:"path"`
}

// Validate reports whether the request contains one bounded absolute local path.
func (request RegisterProjectRequest) Validate() error {
	if request.Path == "" || strings.TrimSpace(request.Path) != request.Path {
		return errors.New("project path must be non-empty without surrounding whitespace")
	}
	if !utf8.ValidString(request.Path) {
		return errors.New("project path must be valid UTF-8")
	}
	if len(request.Path) > maximumRegistrationPathBytes {
		return fmt.Errorf("project path exceeds %d bytes", maximumRegistrationPathBytes)
	}
	for _, character := range request.Path {
		if unicode.IsControl(character) {
			return errors.New("project path must not contain control characters")
		}
	}
	if !filepath.IsAbs(request.Path) {
		return errors.New("project path must be absolute")
	}
	return nil
}

// ProjectRegistration is the authoritative result of creating or replaying one project registration.
type ProjectRegistration struct {
	Project  domain.ProjectSnapshot `json:"project"`
	Revision domain.Sequence        `json:"revision"`
	Created  bool                   `json:"created"`
}

// Validate reports whether a creation is inert while allowing replays to return the current project projection.
func (registration ProjectRegistration) Validate() error {
	if err := registration.Project.Validate(); err != nil {
		return err
	}
	if registration.Revision == 0 || uint64(registration.Revision) > rpc.MaximumSequence {
		return fmt.Errorf("project registration revision %d is outside the supported range", registration.Revision)
	}
	if registration.Created && (registration.Project.State != domain.ProjectStopped ||
		registration.Project.Favorite ||
		len(registration.Project.Apps) != 0 ||
		len(registration.Project.Services) != 0 ||
		len(registration.Project.Resources) != 0) {
		return errors.New("new project registration must be stopped, not favorite, and contain no runtime entities or resources")
	}
	return nil
}

// projectRegistrationResponse keeps the method result extensible around its reviewed registration object.
type projectRegistrationResponse struct {
	Registration ProjectRegistration `json:"registration"`
}
