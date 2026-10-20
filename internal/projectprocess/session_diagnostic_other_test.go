//go:build !windows

package projectprocess

import "github.com/goforj/harbor/internal/domain"

// sessionTestProcessDiagnostics keeps the temporary native-handle diagnostic specific to Windows.
func sessionTestProcessDiagnostics(domain.ProjectSession) (func() string, func()) {
	return func() string { return "non-Windows diagnostic" }, func() {}
}
