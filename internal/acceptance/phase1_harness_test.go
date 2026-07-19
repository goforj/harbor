//go:build phase1acceptance

package acceptance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/control"
	"github.com/goforj/harbor/internal/daemon"
	"github.com/goforj/harbor/internal/platform/runtimepath"
	"github.com/goforj/harbor/internal/platform/userpaths"
)

const (
	phase1CLIBinaryEnvironment         = "HARBOR_PHASE1_CLI_BINARY"
	phase1DaemonBinaryEnvironment      = "HARBOR_PHASE1_DAEMON_BINARY"
	phase1EvidenceDirectoryEnvironment = "HARBOR_PHASE1_EVIDENCE_DIRECTORY"
	phase1FirstDaemonLogName           = "daemon-generation-1-hard-kill"
	phase1SecondDaemonLogName          = "daemon-generation-2-restart"
	phase1ThirdDaemonLogName           = "daemon-generation-3-startup-recovery"
	phase1MaximumLogBytes              = 256 << 10
	phase1ProbeInterval                = 25 * time.Millisecond
	phase1CommandTimeout               = 20 * time.Second
	phase1StartupTimeout               = 45 * time.Second
	phase1ShutdownTimeout              = 45 * time.Second
)

var phase1InheritedEnvironmentAllowlist = []string{
	"COMSPEC",
	"LANG",
	"LC_ALL",
	"LC_CTYPE",
	"PATH",
	"PATHEXT",
	"SSL_CERT_DIR",
	"SSL_CERT_FILE",
	"SYSTEMROOT",
	"TZ",
	"WINDIR",
}

var phase1EvidenceLogNames = []string{
	phase1FirstDaemonLogName,
	phase1SecondDaemonLogName,
	phase1ThirdDaemonLogName,
}

// phase1Config identifies binaries built independently of the acceptance test and its safe artifact destination.
type phase1Config struct {
	cliBinary         string
	daemonBinary      string
	evidenceDirectory string
}

// phase1Sandbox records only the paths needed to prove standard-path isolation and cleanup.
type phase1Sandbox struct {
	root             string
	dataDirectory    string
	runtimeDirectory string
	databasePath     string
	endpointPath     string
	intentDirectory  string
	environment      []string
}

// phase1CommandResult keeps bounded stdout and stderr separate so JSON output cannot be polluted by diagnostics.
type phase1CommandResult struct {
	stdout string
	stderr string
	err    error
}

// phase1DaemonProcess owns one real harbord generation and its asynchronously collected exit result.
type phase1DaemonProcess struct {
	command *exec.Cmd
	log     *phase1BoundedLog
	done    chan struct{}

	mutex   sync.Mutex
	waitErr error
}

// phase1BoundedLog retains only the newest diagnostic bytes while always draining child-process pipes.
type phase1BoundedLog struct {
	mutex     sync.Mutex
	contents  []byte
	truncated bool
}

// phase1Evidence writes a deliberately small, redacted acceptance record outside Harbor's state tree.
type phase1Evidence struct {
	directory    string
	mutex        sync.Mutex
	checks       []string
	logs         map[string]*phase1BoundedLog
	replacements []string
}

// phase1EvidenceSummary is the bounded non-secret record uploaded by CI.
type phase1EvidenceSummary struct {
	SchemaVersion   int      `json:"schema_version"`
	OperatingSystem string   `json:"operating_system"`
	Checks          []string `json:"checks"`
}

// phase1LoadConfig requires explicit prebuilt production binaries so this test cannot silently exercise package-local substitutes.
func phase1LoadConfig(t *testing.T) phase1Config {
	t.Helper()

	configuration := phase1Config{
		cliBinary:         strings.TrimSpace(os.Getenv(phase1CLIBinaryEnvironment)),
		daemonBinary:      strings.TrimSpace(os.Getenv(phase1DaemonBinaryEnvironment)),
		evidenceDirectory: strings.TrimSpace(os.Getenv(phase1EvidenceDirectoryEnvironment)),
	}
	for name, path := range map[string]string{
		phase1CLIBinaryEnvironment:    configuration.cliBinary,
		phase1DaemonBinaryEnvironment: configuration.daemonBinary,
	} {
		if path == "" {
			t.Fatalf("%s must identify a prebuilt production binary", name)
		}
		if !filepath.IsAbs(path) {
			t.Fatalf("%s path %q must be absolute", name, path)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("inspect %s: %v", name, err)
		}
		if !info.Mode().IsRegular() {
			t.Fatalf("%s path %q is not a regular file", name, path)
		}
	}
	return configuration
}

// phase1ConfigureSandbox redirects every production path resolver before either in-process clients or child binaries run.
func phase1ConfigureSandbox(t *testing.T, configuration phase1Config) phase1Sandbox {
	t.Helper()

	root := phase1TemporaryRoot(t)
	environment := phase1PlatformEnvironment(t, root)
	for key, value := range map[string]string{
		"APP_NAME":                        "harbor",
		"APP_KEY":                         "",
		"APP_ENV":                         "testing",
		"APP_DEBUG":                       "0",
		"APP_LOG_FORMAT":                  "json",
		"APP_LOG_TIME":                    "0",
		"APP_LOG_CALLER":                  "0",
		"DB_DRIVER":                       "sqlite",
		"DB_SUPPORTED_DRIVERS":            "sqlite",
		"DB_HARBORD_DRIVER":               "",
		"DB_HARBORD_DSN":                  "",
		"DB_HARBORD_SQLITE_DATABASE":      "",
		"DB_HARBORD_DATABASE":             "",
		"DB_HARBORD_MAX_OPEN_CONNECTIONS": "",
		"DB_HARBORD_MAX_IDLE_CONNECTIONS": "",
		"FORJ_DEBUG":                      "",
		"GORACE":                          "halt_on_error=1",
	} {
		environment[key] = value
	}
	for key, value := range environment {
		t.Setenv(key, value)
	}

	dataDirectory, err := userpaths.DataDirectory()
	if err != nil {
		t.Fatalf("resolve phase 1 data directory: %v", err)
	}
	databasePath, err := userpaths.DatabasePath()
	if err != nil {
		t.Fatalf("resolve phase 1 database path: %v", err)
	}
	runtimeDirectory, err := runtimepath.Directory()
	if err != nil {
		t.Fatalf("resolve phase 1 runtime directory: %v", err)
	}
	endpointPath, err := phase1EndpointPath()
	if err != nil {
		t.Fatalf("resolve phase 1 endpoint path: %v", err)
	}

	evidenceDirectory := configuration.evidenceDirectory
	if evidenceDirectory == "" {
		evidenceDirectory = filepath.Join(root, "evidence")
	}
	if !filepath.IsAbs(evidenceDirectory) {
		t.Fatalf("%s path %q must be absolute", phase1EvidenceDirectoryEnvironment, evidenceDirectory)
	}
	if phase1PathContains(dataDirectory, evidenceDirectory) || phase1PathContains(evidenceDirectory, dataDirectory) {
		t.Fatalf("phase 1 evidence directory must not overlap Harbor's durable data directory")
	}
	return phase1Sandbox{
		root:             root,
		dataDirectory:    filepath.Clean(dataDirectory),
		runtimeDirectory: filepath.Clean(runtimeDirectory),
		databasePath:     filepath.Clean(databasePath),
		endpointPath:     endpointPath,
		intentDirectory:  filepath.Join(filepath.Clean(dataDirectory), "project-removal-intents"),
		environment:      phase1ChildEnvironment(os.Environ(), environment),
	}
}

// phase1ChildEnvironment retains only reviewed inherited process settings before applying explicit sandbox values.
func phase1ChildEnvironment(base []string, overrides map[string]string) []string {
	return phase1ChildEnvironmentForPlatform(base, overrides, runtime.GOOS == "windows")
}

// phase1ChildEnvironmentForPlatform keeps Windows key folding directly testable on every CI host.
func phase1ChildEnvironmentForPlatform(base []string, overrides map[string]string, caseInsensitive bool) []string {
	allowed := make(map[string]struct{}, len(phase1InheritedEnvironmentAllowlist))
	for _, key := range phase1InheritedEnvironmentAllowlist {
		allowed[phase1EnvironmentKey(key, caseInsensitive)] = struct{}{}
	}
	filtered := make([]string, 0, len(phase1InheritedEnvironmentAllowlist))
	for _, entry := range base {
		key, _, found := strings.Cut(entry, "=")
		if !found || key == "" {
			continue
		}
		if _, ok := allowed[phase1EnvironmentKey(key, caseInsensitive)]; ok {
			filtered = append(filtered, entry)
		}
	}
	return phase1MergeEnvironmentForPlatform(filtered, overrides, caseInsensitive)
}

// phase1PathContains reports containment without confusing sibling paths that share a textual prefix.
func phase1PathContains(parent string, candidate string) bool {
	relative, err := filepath.Rel(filepath.Clean(parent), filepath.Clean(candidate))
	if err != nil {
		return false
	}
	return relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)))
}

// phase1MergedEnvironment replaces inherited keys deterministically, including case-insensitive Windows names.
func phase1MergedEnvironment(base []string, overrides map[string]string) []string {
	return phase1MergeEnvironmentForPlatform(base, overrides, runtime.GOOS == "windows")
}

// phase1MergeEnvironmentForPlatform replaces keys deterministically under one selected platform comparison policy.
func phase1MergeEnvironmentForPlatform(base []string, overrides map[string]string, caseInsensitive bool) []string {
	values := make(map[string]string, len(base)+len(overrides))
	names := make(map[string]string, len(base)+len(overrides))
	for _, entry := range base {
		key, value, found := strings.Cut(entry, "=")
		if !found || key == "" {
			continue
		}
		normalized := phase1EnvironmentKey(key, caseInsensitive)
		values[normalized] = value
		names[normalized] = key
	}
	for key, value := range overrides {
		normalized := phase1EnvironmentKey(key, caseInsensitive)
		values[normalized] = value
		names[normalized] = key
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, names[key]+"="+values[key])
	}
	return result
}

// phase1EnvironmentKey applies Windows environment-key equality without changing emitted override names.
func phase1EnvironmentKey(key string, caseInsensitive bool) string {
	if caseInsensitive {
		return strings.ToUpper(key)
	}
	return key
}

// phase1NewEvidence creates a safe artifact writer and registers it after sandbox cleanup so diagnostics survive failures.
func phase1NewEvidence(t *testing.T, configuration phase1Config, sandbox phase1Sandbox) *phase1Evidence {
	t.Helper()

	directory := strings.TrimSpace(configuration.evidenceDirectory)
	if directory == "" {
		directory = filepath.Join(sandbox.root, "evidence")
	} else {
		directory = filepath.Clean(directory)
	}
	if err := phase1PrepareEvidenceDirectory(directory); err != nil {
		t.Fatalf("prepare phase 1 evidence directory: %v", err)
	}
	evidence := &phase1Evidence{
		directory: directory,
		logs:      make(map[string]*phase1BoundedLog),
	}
	evidence.addRedaction(sandbox.root)
	evidence.addRedaction(sandbox.dataDirectory)
	evidence.addRedaction(sandbox.runtimeDirectory)
	for _, value := range phase1EndpointRedactions(sandbox.endpointPath) {
		evidence.addRedaction(value)
	}
	t.Cleanup(func() {
		if err := evidence.write(); err != nil {
			t.Errorf("write phase 1 evidence: %v", err)
		}
	})
	return evidence
}

// phase1EndpointRedactions expands a Windows pipe into both its full endpoint and embedded user SID.
func phase1EndpointRedactions(endpoint string) []string {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return nil
	}
	redactions := []string{endpoint}
	normalized := strings.ReplaceAll(endpoint, `\`, "/")
	const pipeMarker = "goforj-harbor-"
	position := strings.LastIndex(strings.ToLower(normalized), pipeMarker)
	if position >= 0 {
		sid := normalized[position+len(pipeMarker):]
		if sid != "" && !strings.Contains(sid, "/") {
			redactions = append(redactions, sid)
		}
	}
	return redactions
}

// phase1PrepareEvidenceDirectory rejects symlinked or pre-populated artifact roots that could upload unreviewed files.
func phase1PrepareEvidenceDirectory(directory string) error {
	if err := os.Mkdir(directory, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	info, err := os.Lstat(directory)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("evidence path %q is not a direct directory", directory)
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return err
	}
	if len(entries) != 0 {
		return fmt.Errorf("evidence directory %q is not empty", directory)
	}
	return nil
}

// addRedaction registers a known local path whose presence would make CI evidence machine-specific.
func (evidence *phase1Evidence) addRedaction(value string) {
	if strings.TrimSpace(value) == "" {
		return
	}
	evidence.mutex.Lock()
	evidence.replacements = append(evidence.replacements, filepath.Clean(value))
	evidence.mutex.Unlock()
}

// check records one completed lifecycle gate without retaining command payloads or machine paths.
func (evidence *phase1Evidence) check(name string) {
	evidence.mutex.Lock()
	evidence.checks = append(evidence.checks, name)
	evidence.mutex.Unlock()
}

// registerLog names one bounded daemon stream for later redacted artifact emission.
func (evidence *phase1Evidence) registerLog(name string, log *phase1BoundedLog) {
	evidence.mutex.Lock()
	evidence.logs[name] = log
	evidence.mutex.Unlock()
}

// write emits only the allowlisted summary and redacted bounded daemon logs.
func (evidence *phase1Evidence) write() error {
	evidence.mutex.Lock()
	checks := append([]string(nil), evidence.checks...)
	logs := make(map[string]*phase1BoundedLog, len(evidence.logs))
	for name, log := range evidence.logs {
		logs[name] = log
	}
	replacements := append([]string(nil), evidence.replacements...)
	evidence.mutex.Unlock()

	if err := os.MkdirAll(evidence.directory, 0o700); err != nil {
		return err
	}
	if err := phase1ValidateEvidenceDirectory(evidence.directory); err != nil {
		return err
	}
	allowlist := phase1EvidenceFilenames()
	writeErr := phase1ResetEvidenceDirectory(evidence.directory, allowlist)
	allowedLogs := make(map[string]struct{}, len(phase1EvidenceLogNames))
	for _, name := range phase1EvidenceLogNames {
		allowedLogs[name] = struct{}{}
	}
	for name := range logs {
		if _, ok := allowedLogs[name]; !ok {
			writeErr = errors.Join(writeErr, fmt.Errorf("evidence log %q is not allowlisted", name))
		}
	}
	summary := phase1EvidenceSummary{
		SchemaVersion:   1,
		OperatingSystem: runtime.GOOS,
		Checks:          checks,
	}
	contents, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		return err
	}
	contents = append(contents, '\n')
	if err := phase1WriteEvidenceFile(evidence.directory, "summary.json", contents); err != nil {
		writeErr = errors.Join(writeErr, err)
	}

	for _, name := range phase1EvidenceLogNames {
		var contents []byte
		if log := logs[name]; log != nil {
			contents = log.snapshot()
		} else if _, registered := logs[name]; registered {
			writeErr = errors.Join(writeErr, fmt.Errorf("evidence log %q is nil", name))
		}
		if bytes.Contains(contents, []byte("WARNING: DATA RACE")) {
			writeErr = errors.Join(writeErr, fmt.Errorf("daemon log %q contains a Go data race report", name))
		}
		redacted := phase1RedactLog(string(contents), replacements)
		if len(redacted) > phase1MaximumLogBytes {
			redacted = redacted[len(redacted)-phase1MaximumLogBytes:]
		}
		if err := phase1WriteEvidenceFile(evidence.directory, name+".log", []byte(redacted)); err != nil {
			writeErr = errors.Join(writeErr, err)
		}
	}
	if err := phase1VerifyEvidenceDirectory(evidence.directory, allowlist); err != nil {
		writeErr = errors.Join(writeErr, err)
	}
	return writeErr
}

// phase1EvidenceFilenames returns the complete static artifact allowlist in deterministic order.
func phase1EvidenceFilenames() []string {
	return []string{
		"summary.json",
		phase1FirstDaemonLogName + ".log",
		phase1SecondDaemonLogName + ".log",
		phase1ThirdDaemonLogName + ".log",
	}
}

// phase1ValidateEvidenceDirectory rejects a replaced artifact root before opening any allowlisted child.
func phase1ValidateEvidenceDirectory(directory string) error {
	info, err := os.Lstat(directory)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("evidence path %q is not a direct directory", directory)
	}
	return nil
}

// phase1ResetEvidenceDirectory removes prior allowlisted files and rejects while removing every unexpected entry.
func phase1ResetEvidenceDirectory(directory string, allowlist []string) error {
	allowed := make(map[string]struct{}, len(allowlist))
	for _, name := range allowlist {
		allowed[name] = struct{}{}
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return err
	}
	var resetErr error
	for _, entry := range entries {
		path := filepath.Join(directory, entry.Name())
		_, allowlisted := allowed[entry.Name()]
		info, inspectErr := os.Lstat(path)
		if inspectErr != nil {
			resetErr = errors.Join(resetErr, fmt.Errorf("inspect evidence entry %q: %w", entry.Name(), inspectErr))
		} else if !allowlisted {
			resetErr = errors.Join(resetErr, fmt.Errorf("unexpected evidence entry %q", entry.Name()))
		} else if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			resetErr = errors.Join(resetErr, fmt.Errorf("allowlisted evidence entry %q is not a direct regular file", entry.Name()))
		}
		if removeErr := os.RemoveAll(path); removeErr != nil {
			resetErr = errors.Join(resetErr, fmt.Errorf("remove evidence entry %q: %w", entry.Name(), removeErr))
		}
	}
	return resetErr
}

// phase1WriteEvidenceFile creates one direct file without following an entry introduced at an allowlisted name.
func phase1WriteEvidenceFile(directory string, name string, contents []byte) (writeErr error) {
	path := filepath.Join(directory, name)
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create evidence file %q: %w", name, err)
	}
	defer func() {
		writeErr = errors.Join(writeErr, file.Close())
	}()
	if _, err := io.Copy(file, bytes.NewReader(contents)); err != nil {
		return fmt.Errorf("write evidence file %q: %w", name, err)
	}
	return nil
}

// phase1VerifyEvidenceDirectory proves the final upload root contains exactly direct regular allowlisted files.
func phase1VerifyEvidenceDirectory(directory string, allowlist []string) error {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
		info, err := os.Lstat(filepath.Join(directory, entry.Name()))
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return fmt.Errorf("final evidence entry %q is not a direct regular file", entry.Name())
		}
	}
	sort.Strings(names)
	wanted := append([]string(nil), allowlist...)
	sort.Strings(wanted)
	if len(names) != len(wanted) {
		return fmt.Errorf("final evidence entries = %v, want %v", names, wanted)
	}
	for index := range wanted {
		if names[index] != wanted[index] {
			return fmt.Errorf("final evidence entries = %v, want %v", names, wanted)
		}
	}
	return nil
}

// phase1RedactLog removes known paths and conservative secret-shaped assignments before artifact publication.
func phase1RedactLog(contents string, replacements []string) string {
	return phase1RedactLogForPlatform(contents, replacements, runtime.GOOS == "windows")
}

// phase1RedactLogForPlatform applies platform path equality before conservative secret-shape filtering.
func phase1RedactLogForPlatform(contents string, replacements []string, caseInsensitivePaths bool) string {
	sort.Slice(replacements, func(left int, right int) bool {
		return len(replacements[left]) > len(replacements[right])
	})
	for _, replacement := range replacements {
		contents = phase1ReplacePath(contents, replacement, caseInsensitivePaths)
		contents = phase1ReplacePath(contents, filepath.ToSlash(replacement), caseInsensitivePaths)
	}
	lines := strings.Split(contents, "\n")
	redactingPEM := false
	for index, line := range lines {
		upper := strings.ToUpper(line)
		if strings.Contains(upper, "-----BEGIN ") {
			redactingPEM = true
			lines[index] = "<redacted PEM material>"
			continue
		}
		if redactingPEM {
			lines[index] = "<redacted PEM material>"
			if strings.Contains(upper, "-----END ") {
				redactingPEM = false
			}
			continue
		}
		lower := strings.ToLower(line)
		for _, marker := range []string{"app_key", "password", "secret", "token"} {
			position := strings.Index(lower, marker)
			if position < 0 {
				continue
			}
			separator := strings.IndexAny(line[position+len(marker):], ":=")
			if separator >= 0 {
				lines[index] = line[:position+len(marker)+separator+1] + " <redacted>"
			}
			break
		}
	}
	return strings.Join(lines, "\n")
}

// phase1ReplacePath redacts every path occurrence while retaining original log casing outside matches.
func phase1ReplacePath(contents string, path string, caseInsensitive bool) string {
	if path == "" {
		return contents
	}
	if !caseInsensitive {
		return strings.ReplaceAll(contents, path, "<sandbox>")
	}
	lowerContents := strings.ToLower(contents)
	lowerPath := strings.ToLower(path)
	var redacted strings.Builder
	redacted.Grow(len(contents))
	remaining := 0
	for {
		position := strings.Index(lowerContents[remaining:], lowerPath)
		if position < 0 {
			redacted.WriteString(contents[remaining:])
			break
		}
		position += remaining
		redacted.WriteString(contents[remaining:position])
		redacted.WriteString("<sandbox>")
		remaining = position + len(path)
	}
	return redacted.String()
}

// Write drains child output while retaining a bounded diagnostic tail.
func (log *phase1BoundedLog) Write(contents []byte) (int, error) {
	log.mutex.Lock()
	defer log.mutex.Unlock()

	written := len(contents)
	if written >= phase1MaximumLogBytes {
		log.contents = append(log.contents[:0], contents[written-phase1MaximumLogBytes:]...)
		log.truncated = true
		return written, nil
	}
	if len(log.contents)+written > phase1MaximumLogBytes {
		discard := len(log.contents) + written - phase1MaximumLogBytes
		copy(log.contents, log.contents[discard:])
		log.contents = log.contents[:len(log.contents)-discard]
		log.truncated = true
	}
	log.contents = append(log.contents, contents...)
	return written, nil
}

// snapshot returns an immutable log copy with an explicit truncation marker.
func (log *phase1BoundedLog) snapshot() []byte {
	log.mutex.Lock()
	defer log.mutex.Unlock()

	contents := append([]byte(nil), log.contents...)
	if log.truncated {
		prefix := []byte("[earlier output truncated]\n")
		contents = append(prefix, contents...)
	}
	return contents
}

// phase1RunCommand executes one bounded production command without merging structured output and diagnostics.
func phase1RunCommand(ctx context.Context, sandbox phase1Sandbox, binary string, args ...string) phase1CommandResult {
	stdout := new(phase1BoundedLog)
	stderr := new(phase1BoundedLog)
	command := exec.CommandContext(ctx, binary, args...)
	command.Dir = sandbox.root
	command.Env = sandbox.environment
	command.Stdout = stdout
	command.Stderr = stderr
	err := command.Run()
	return phase1CommandResult{
		stdout: string(stdout.snapshot()),
		stderr: string(stderr.snapshot()),
		err:    err,
	}
}

// decodeJSON rejects diagnostic contamination and trailing output from machine-readable CLI commands.
func (result phase1CommandResult) decodeJSON(destination any) error {
	if result.err != nil {
		return fmt.Errorf("command failed: %w: %s", result.err, strings.TrimSpace(result.stderr))
	}
	decoder := json.NewDecoder(bytes.NewBufferString(result.stdout))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err == nil {
		return errors.New("command JSON contains trailing values")
	} else if !errors.Is(err, io.EOF) {
		return fmt.Errorf("decode trailing command JSON: %w", err)
	}
	return nil
}

// phase1StartDaemon launches the real foreground daemon and retains its process exit independently of test cancellation.
func phase1StartDaemon(t *testing.T, configuration phase1Config, sandbox phase1Sandbox, evidence *phase1Evidence, name string) *phase1DaemonProcess {
	t.Helper()

	log := new(phase1BoundedLog)
	command := exec.Command(configuration.daemonBinary, "--foreground")
	command.Dir = sandbox.root
	command.Env = sandbox.environment
	command.Stdout = log
	command.Stderr = log
	if err := command.Start(); err != nil {
		t.Fatalf("start production harbord: %v", err)
	}
	process := &phase1DaemonProcess{
		command: command,
		log:     log,
		done:    make(chan struct{}),
	}
	evidence.registerLog(name, log)
	go func() {
		err := command.Wait()
		process.mutex.Lock()
		process.waitErr = err
		process.mutex.Unlock()
		close(process.done)
	}()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = process.terminate(ctx)
	})
	return process
}

// wait returns the retained process result after the caller's deterministic deadline.
func (process *phase1DaemonProcess) wait(ctx context.Context) error {
	select {
	case <-process.done:
		process.mutex.Lock()
		defer process.mutex.Unlock()
		return process.waitErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

// exited reports an already-terminal daemon without consuming its result.
func (process *phase1DaemonProcess) exited() (bool, error) {
	select {
	case <-process.done:
		process.mutex.Lock()
		defer process.mutex.Unlock()
		return true, process.waitErr
	default:
		return false, nil
	}
}

// hardKill proves the process was alive when the test successfully requested forced termination.
func (process *phase1DaemonProcess) hardKill(ctx context.Context) error {
	if exited, exitErr := process.exited(); exited {
		if exitErr == nil {
			exitErr = errors.New("harbord exited successfully before the hard-kill request")
		}
		return fmt.Errorf("harbord exited before the hard-kill request: %w", exitErr)
	}
	if err := process.command.Process.Kill(); err != nil {
		return fmt.Errorf("send hard-kill request to harbord: %w", err)
	}
	waitErr := process.wait(ctx)
	if waitErr == nil {
		return errors.New("hard-killed harbord exited without a forced-termination status")
	}
	var exitError *exec.ExitError
	if !errors.As(waitErr, &exitError) {
		return fmt.Errorf("join hard-killed harbord: %w", waitErr)
	}
	return nil
}

// terminate performs best-effort forced cleanup without claiming it as lifecycle evidence.
func (process *phase1DaemonProcess) terminate(ctx context.Context) error {
	if exited, err := process.exited(); exited {
		return err
	}
	killErr := process.command.Process.Kill()
	if errors.Is(killErr, os.ErrProcessDone) {
		killErr = nil
	}
	waitErr := process.wait(ctx)
	return errors.Join(killErr, waitErr)
}

// phase1WaitReady polls the production CLI while also failing immediately if the daemon process exits.
func phase1WaitReady(ctx context.Context, configuration phase1Config, sandbox phase1Sandbox, process *phase1DaemonProcess) (control.DaemonStatus, error) {
	var status control.DaemonStatus
	err := phase1Eventually(ctx, "daemon readiness", func() (bool, error) {
		if exited, exitErr := process.exited(); exited {
			if exitErr == nil {
				exitErr = errors.New("harbord exited successfully before opening its control endpoint")
			}
			return false, fmt.Errorf("harbord exited before readiness: %w", exitErr)
		}
		probeContext, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		result := phase1RunCommand(probeContext, sandbox, configuration.cliBinary, "daemon", "status", "--json")
		if result.err != nil {
			return false, fmt.Errorf("status probe: %w: %s", result.err, strings.TrimSpace(result.stderr))
		}
		var candidate control.DaemonStatus
		if err := result.decodeJSON(&candidate); err != nil {
			return false, err
		}
		if err := candidate.Validate(); err != nil {
			return false, err
		}
		status = candidate
		return true, nil
	})
	return status, err
}

// phase1Eventually retries an observation until it succeeds or its context supplies the only timing bound.
func phase1Eventually(ctx context.Context, description string, probe func() (bool, error)) error {
	var lastErr error
	ticker := time.NewTicker(phase1ProbeInterval)
	defer ticker.Stop()
	for {
		ready, err := probe()
		if ready && err == nil {
			return nil
		}
		if err != nil {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			if lastErr != nil {
				return fmt.Errorf("%s: %w (last observation: %v)", description, ctx.Err(), lastErr)
			}
			return fmt.Errorf("%s: %w", description, ctx.Err())
		case <-ticker.C:
		}
	}
}

// phase1WaitObserverDone proves a retained desktop session observes daemon termination instead of being mislabeled as a worker.
func phase1WaitObserverDone(ctx context.Context, observer *control.Client) error {
	select {
	case <-observer.Done():
		if observer.Err() == nil {
			return errors.New("desktop observer ended without a terminal cause")
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// phase1StopDaemon requests acknowledged shutdown through the production CLI and joins the foreground process.
func phase1StopDaemon(ctx context.Context, configuration phase1Config, sandbox phase1Sandbox, process *phase1DaemonProcess) error {
	result := phase1RunCommand(ctx, sandbox, configuration.cliBinary, "daemon", "stop")
	if result.err != nil {
		return fmt.Errorf("request daemon stop: %w: %s", result.err, strings.TrimSpace(result.stderr))
	}
	if !strings.Contains(result.stdout, "Harbor daemon is stopping.") {
		return fmt.Errorf("daemon stop output did not contain acknowledgement: %q", result.stdout)
	}
	if err := process.wait(ctx); err != nil {
		return fmt.Errorf("join gracefully stopped daemon: %w", err)
	}
	return nil
}

// phase1AssertDaemonUnavailable proves no stale IPC endpoint still accepts authenticated requests.
func phase1AssertDaemonUnavailable(t *testing.T, configuration phase1Config, sandbox phase1Sandbox) {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	result := phase1RunCommand(ctx, sandbox, configuration.cliBinary, "daemon", "status", "--json")
	if result.err == nil {
		t.Fatalf("daemon status unexpectedly succeeded after shutdown: %s", result.stdout)
	}
}

// phase1AssertCleanup proves transient process authority, SQLite sidecars, and client intents are retired without deleting durable state.
func phase1AssertCleanup(t *testing.T, sandbox phase1Sandbox) {
	t.Helper()

	phase1AssertEndpointUnavailable(t, sandbox.endpointPath)
	lock, err := daemon.AcquireProcessLock()
	if err != nil {
		t.Fatalf("reacquire daemon process lock after shutdown: %v", err)
	}
	if err := lock.Release(); err != nil {
		t.Fatalf("release cleanup probe lock: %v", err)
	}

	entries, err := os.ReadDir(sandbox.runtimeDirectory)
	if err != nil {
		t.Fatalf("read runtime directory after shutdown: %v", err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	if len(names) != 1 || names[0] != "harbord.lock" {
		t.Fatalf("runtime artifacts after shutdown = %v, want only reusable lock file", names)
	}

	entries, err = os.ReadDir(sandbox.intentDirectory)
	if err != nil {
		t.Fatalf("read project removal intent directory after terminal output: %v", err)
	}
	names = names[:0]
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	if len(names) != 1 || names[0] != ".lock" {
		t.Fatalf("project removal intent artifacts = %v, want only reusable lock file", names)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if err := phase1Eventually(ctx, "SQLite sidecar cleanup", func() (bool, error) {
		for _, path := range []string{sandbox.databasePath + "-wal", sandbox.databasePath + "-shm", sandbox.databasePath + "-journal"} {
			_, err := os.Lstat(path)
			if err == nil {
				return false, nil
			}
			if !os.IsNotExist(err) {
				return false, err
			}
		}
		return true, nil
	}); err != nil {
		t.Fatal(err)
	}
}
