package projectprocess

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/goforj/harbor/internal/containerruntime"
	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/projectruntime"
)

const maximumStartupProgressBytes = 32 * 1024

// removeStartupProgress retires only the exact session's diagnostic path after an owned stop.
func removeStartupProgress(directory string, project domain.ProjectID, session domain.SessionID) error {
	path, err := startupProgressPath(directory, project, session)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// startupProgressPath isolates diagnostic files by the same opaque project/session identity as output history.
func startupProgressPath(directory string, project domain.ProjectID, session domain.SessionID) (string, error) {
	path, err := outputSpoolPath(directory, project, session)
	if err != nil {
		return "", err
	}
	return path + ".startup.jsonl", nil
}

// prepareStartupProgress creates an empty exclusive stream without replacing prior session diagnostics.
func prepareStartupProgress(directory string, project domain.ProjectID, session domain.SessionID) (string, error) {
	parent, err := prepareOutputSpoolDirectory(directory)
	if err != nil {
		return "", err
	}
	parent, err = filepath.EvalSymlinks(parent)
	if err != nil {
		return "", err
	}
	path, err := startupProgressPath(directory, project, session)
	if err != nil {
		return "", err
	}
	path = filepath.Join(parent, filepath.Base(path))
	root, err := os.OpenRoot(parent)
	if err != nil {
		return "", err
	}
	defer root.Close()
	file, err := root.OpenFile(filepath.Base(path), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return "", err
	}
	if err := file.Close(); err != nil {
		return "", err
	}
	return path, nil
}

// ReadStartupProgress reads bounded diagnostics only for the caller's already-selected durable session.
func (supervisor *Supervisor) ReadStartupProgress(project domain.ProjectID, session domain.SessionID) *projectruntime.StartupProgress {
	path, err := startupProgressPath(supervisor.outputSpoolDirectory, project, session)
	if err != nil {
		return nil
	}
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return nil
	}
	defer root.Close()
	info, err := root.Lstat(filepath.Base(path))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	invalid := &projectruntime.StartupProgress{Events: []projectruntime.StartupEvent{}, Invalid: true}
	if err != nil || !info.Mode().IsRegular() || info.Size() > maximumStartupProgressBytes || (runtime.GOOS != "windows" && info.Mode().Perm() != 0600) {
		return invalid
	}
	file, err := root.Open(filepath.Base(path))
	if err != nil {
		return invalid
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return invalid
	}
	data, err := io.ReadAll(io.LimitReader(file, maximumStartupProgressBytes+1))
	if err != nil || len(data) > maximumStartupProgressBytes {
		return invalid
	}
	return decodeStartupProgress(data, string(session))
}

// decodeStartupProgress ignores an incomplete final write and rejects every invalid complete record.
func decodeStartupProgress(data []byte, session string) *projectruntime.StartupProgress {
	progress := &projectruntime.StartupProgress{Events: []projectruntime.StartupEvent{}}
	for _, line := range bytes.SplitAfter(data, []byte{'\n'}) {
		if !bytes.HasSuffix(line, []byte{'\n'}) {
			break
		}
		if len(progress.Events) >= 256 {
			progress.Invalid = true
			break
		}
		var event projectruntime.StartupEvent
		decoder := json.NewDecoder(bytes.NewReader(line))
		decoder.DisallowUnknownFields()
		err := decoder.Decode(&event)
		if err != nil || decoder.Decode(new(any)) != io.EOF || event.Validate() != nil || event.Session != session || event.Sequence != uint64(len(progress.Events)+1) {
			progress.Invalid = true
			break
		}
		progress.Events = append(progress.Events, event)
	}
	return progress
}

// startupOutputWaitContext refreshes quiet startup diagnostics without changing output cursors.
func (supervisor *Supervisor) startupOutputWaitContext(ctx context.Context, project domain.ProjectID, session domain.SessionID) (context.Context, context.CancelFunc) {
	progress := supervisor.ReadStartupProgress(project, session)
	if progress != nil && !progress.Invalid {
		finished := false
		for _, event := range progress.Events {
			if event.Phase == "startup" && event.Status != "started" {
				finished = true
			}
		}
		if !finished {
			return context.WithTimeout(ctx, 500*time.Millisecond)
		}
	}
	return ctx, func() {}
}

// withoutStartupProgressEnvironment prevents another process's optional telemetry destination from leaking into a child.
func withoutStartupProgressEnvironment(environment []string) []string {
	result := make([]string, 0, len(environment))
	for _, value := range environment {
		key, _, _ := strings.Cut(value, "=")
		if strings.EqualFold(key, "FORJ_DEV_EVENTS_FILE") || strings.EqualFold(key, "FORJ_DEV_EVENTS_SESSION") {
			continue
		}
		result = append(result, value)
	}
	return result
}

// ObserveLaunchDiagnostics reads only container publications, preserving every neighboring workload.
func (supervisor *Supervisor) ObserveLaunchDiagnostics(ctx context.Context, checkout string) (*projectruntime.LaunchDiagnostics, error) {
	observer, ok := supervisor.containerRuntime.(containerruntime.LaunchDiagnosticObserver)
	if !ok {
		return nil, nil
	}
	diagnostics, err := observer.ObserveLaunchDiagnostics(ctx, checkout)
	if err != nil {
		return nil, err
	}
	if err := diagnostics.Validate(); err != nil {
		return nil, err
	}
	return &diagnostics, nil
}
