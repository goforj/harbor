package control

import (
	"errors"
	"fmt"
	"path"
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
	if err := validateProjectPathText(request.Path); err != nil {
		return err
	}
	if !filepath.IsAbs(request.Path) {
		return errors.New("project path must be absolute")
	}
	return nil
}

// validateProjectPathText enforces the transport-safe bounds shared by native requests and portable projections.
func validateProjectPathText(projectPath string) error {
	if projectPath == "" || strings.TrimSpace(projectPath) != projectPath {
		return errors.New("project path must be non-empty without surrounding whitespace")
	}
	if !utf8.ValidString(projectPath) {
		return errors.New("project path must be valid UTF-8")
	}
	if len(projectPath) > maximumRegistrationPathBytes {
		return fmt.Errorf("project path exceeds %d bytes", maximumRegistrationPathBytes)
	}
	for _, character := range projectPath {
		if unicode.IsControl(character) {
			return errors.New("project path must not contain control characters")
		}
	}
	return nil
}

// validatePortableAbsoluteProjectPath accepts a daemon-authored path independently of the desktop decoder's OS.
func validatePortableAbsoluteProjectPath(projectPath string) error {
	if err := validateProjectPathText(projectPath); err != nil {
		return err
	}
	if filepath.IsAbs(projectPath) || path.IsAbs(projectPath) {
		return nil
	}
	if len(projectPath) >= 3 &&
		((projectPath[0] >= 'a' && projectPath[0] <= 'z') || (projectPath[0] >= 'A' && projectPath[0] <= 'Z')) &&
		projectPath[1] == ':' && (projectPath[2] == '\\' || projectPath[2] == '/') {
		return nil
	}
	if strings.HasPrefix(projectPath, `\\`) {
		return nil
	}
	return errors.New("project path must be absolute")
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
