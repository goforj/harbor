package control

import (
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/goforj/harbor/internal/domain"
)

const (
	maximumProjectEnvironmentFiles          = 32
	maximumProjectEnvironmentFileBytes      = 1024 * 1024
	maximumProjectEnvironmentTotalFileBytes = 4 * 1024 * 1024
	maximumProjectEnvironmentOverrides      = 128
	maximumProjectEnvironmentValueBytes     = 1024 * 1024
	maximumProjectEnvironmentErrorBytes     = 1024
	maximumProjectEnvironmentFilenameBytes  = 128
	projectEnvironmentRevisionBytes         = 32
)

// ProjectEnvironmentRequest selects one registered project's runtime environment inputs.
type ProjectEnvironmentRequest struct {
	ProjectID domain.ProjectID `json:"project_id"`
}

// Validate reports whether the environment request selects one valid project.
func (request ProjectEnvironmentRequest) Validate() error {
	return request.ProjectID.Validate()
}

// SaveProjectEnvironmentFileRequest replaces one displayed environment file revision.
type SaveProjectEnvironmentFileRequest struct {
	ProjectID domain.ProjectID `json:"project_id"`
	Name      string           `json:"name"`
	Contents  string           `json:"contents"`
	Revision  string           `json:"revision"`
}

// Validate reports whether the save request remains inside the bounded provider file surface.
func (request SaveProjectEnvironmentFileRequest) Validate() error {
	if err := request.ProjectID.Validate(); err != nil {
		return err
	}
	if err := validateProjectEnvironmentFilename(request.Name); err != nil {
		return err
	}
	if !utf8.ValidString(request.Contents) {
		return errors.New("project environment file contents must be valid UTF-8")
	}
	if len(request.Contents) > maximumProjectEnvironmentFileBytes {
		return fmt.Errorf("project environment file exceeds %d bytes", maximumProjectEnvironmentFileBytes)
	}
	return validateProjectEnvironmentRevision(request.Revision, false)
}

// ProjectEnvironmentVariable is one read-only value Harbor supplies to the project process.
type ProjectEnvironmentVariable struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Validate reports whether the environment variable has a portable name and bounded UTF-8 value.
func (variable ProjectEnvironmentVariable) Validate() error {
	if err := validateProjectEnvironmentVariableName(variable.Name); err != nil {
		return err
	}
	if !utf8.ValidString(variable.Value) {
		return fmt.Errorf("project environment override %q must be valid UTF-8", variable.Name)
	}
	if len(variable.Value) > maximumProjectEnvironmentValueBytes {
		return fmt.Errorf("project environment override %q exceeds %d bytes", variable.Name, maximumProjectEnvironmentValueBytes)
	}
	return nil
}

// ProjectEnvironmentFile is one editable provider-recognized file in the registered checkout.
type ProjectEnvironmentFile struct {
	Name     string `json:"name"`
	Contents string `json:"contents"`
	Revision string `json:"revision"`
}

// Validate reports whether the file can safely cross the desktop editing boundary.
func (file ProjectEnvironmentFile) Validate() error {
	if err := validateProjectEnvironmentFilename(file.Name); err != nil {
		return err
	}
	if !utf8.ValidString(file.Contents) {
		return fmt.Errorf("project environment file %q contents must be valid UTF-8", file.Name)
	}
	if len(file.Contents) > maximumProjectEnvironmentFileBytes {
		return fmt.Errorf("project environment file %q exceeds %d bytes", file.Name, maximumProjectEnvironmentFileBytes)
	}
	return validateProjectEnvironmentRevision(file.Revision, false)
}

// ProjectEnvironment is the complete read-only override view and editable provider file set.
type ProjectEnvironment struct {
	ProjectID          domain.ProjectID             `json:"project_id"`
	OverridesAvailable bool                         `json:"overrides_available"`
	OverrideError      string                       `json:"override_error,omitempty"`
	Overrides          []ProjectEnvironmentVariable `json:"overrides"`
	Files              []ProjectEnvironmentFile     `json:"files"`
}

// Validate reports whether the environment response is bounded, canonical, and internally consistent.
func (environment ProjectEnvironment) Validate() error {
	if err := environment.ProjectID.Validate(); err != nil {
		return err
	}
	if environment.Overrides == nil || environment.Files == nil {
		return errors.New("project environment collections must be initialized")
	}
	if len(environment.Overrides) > maximumProjectEnvironmentOverrides {
		return fmt.Errorf("project environment contains more than %d overrides", maximumProjectEnvironmentOverrides)
	}
	if !utf8.ValidString(environment.OverrideError) || len(environment.OverrideError) > maximumProjectEnvironmentErrorBytes {
		return fmt.Errorf("project environment override error exceeds its bounded UTF-8 shape")
	}
	if environment.OverridesAvailable && environment.OverrideError != "" {
		return errors.New("available project environment overrides must not contain an error")
	}
	if !environment.OverridesAvailable && len(environment.Overrides) != 0 {
		return errors.New("unavailable project environment overrides must be empty")
	}
	previousOverride := ""
	for index, override := range environment.Overrides {
		if err := override.Validate(); err != nil {
			return err
		}
		if index > 0 && override.Name <= previousOverride {
			return errors.New("project environment overrides must be sorted without duplicates")
		}
		previousOverride = override.Name
	}
	if len(environment.Files) > maximumProjectEnvironmentFiles {
		return fmt.Errorf("project environment contains more than %d files", maximumProjectEnvironmentFiles)
	}
	totalBytes := 0
	previousFile := ""
	for index, file := range environment.Files {
		if err := file.Validate(); err != nil {
			return err
		}
		if index > 0 && file.Name <= previousFile {
			return errors.New("project environment files must be sorted without duplicates")
		}
		previousFile = file.Name
		totalBytes += len(file.Contents)
	}
	if totalBytes > maximumProjectEnvironmentTotalFileBytes {
		return fmt.Errorf("project environment files exceed %d total bytes", maximumProjectEnvironmentTotalFileBytes)
	}
	return nil
}

// validateProjectEnvironmentVariableName keeps displayed overrides aligned with portable process environment syntax.
func validateProjectEnvironmentVariableName(name string) error {
	if name == "" || len(name) > 128 {
		return errors.New("project environment override name must contain between 1 and 128 bytes")
	}
	for index := range len(name) {
		character := name[index]
		if (character >= 'A' && character <= 'Z') ||
			(character >= 'a' && character <= 'z') ||
			character == '_' ||
			(index > 0 && character >= '0' && character <= '9') {
			continue
		}
		return fmt.Errorf("project environment override name %q is not portable", name)
	}
	return nil
}

// validateProjectEnvironmentFilename permits a direct provider filename without granting a path selector.
func validateProjectEnvironmentFilename(name string) error {
	if name == "" || len(name) > maximumProjectEnvironmentFilenameBytes {
		return fmt.Errorf("project environment filename must contain between 1 and %d bytes", maximumProjectEnvironmentFilenameBytes)
	}
	if !utf8.ValidString(name) || strings.TrimSpace(name) != name || strings.ContainsAny(name, `/\`) || name == "." || name == ".." {
		return errors.New("project environment filename must be one canonical direct filename")
	}
	if name != ".env" && (!strings.HasPrefix(name, ".env.") || len(name) == len(".env.")) {
		return errors.New(`project environment filename must be ".env" or a direct ".env.*" variant`)
	}
	return nil
}

// validateProjectEnvironmentRevision requires the SHA-256 digest displayed with a file.
func validateProjectEnvironmentRevision(revision string, allowEmpty bool) error {
	if revision == "" && allowEmpty {
		return nil
	}
	if len(revision) != hex.EncodedLen(projectEnvironmentRevisionBytes) {
		return errors.New("project environment file revision must be a SHA-256 digest")
	}
	if _, err := hex.DecodeString(revision); err != nil || strings.ToLower(revision) != revision {
		return errors.New("project environment file revision must be lowercase hexadecimal")
	}
	return nil
}

// projectEnvironmentResponse keeps the method result extensible around one project environment.
type projectEnvironmentResponse struct {
	Environment ProjectEnvironment `json:"environment"`
}

// projectEnvironmentFileResponse keeps the save result extensible around one file.
type projectEnvironmentFileResponse struct {
	File ProjectEnvironmentFile `json:"file"`
}
