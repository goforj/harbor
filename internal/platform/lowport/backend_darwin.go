//go:build darwin

package lowport

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/goforj/harbor/internal/platform/launchdrelaypath"
	"golang.org/x/sys/unix"
)

const (
	darwinLaunchDaemonDirectory = "/Library/LaunchDaemons"
	darwinLabel                 = "com.goforj.harbor.launchdrelay"
	darwinPlistPath             = darwinLaunchDaemonDirectory + "/com.goforj.harbor.launchdrelay.plist"
	maximumLaunchctlOutput      = 16 << 10
	maximumPlistBytes           = 64 << 10
	launchctlTimeout            = 5 * time.Second
)

// darwinBackend owns only Harbor's compiled launchd plist and fixed launchctl vectors.
type darwinBackend struct {
	run        func(context.Context, ...string) error
	print      func(context.Context, ...string) ([]byte, error)
	lookupUser func(string) (*user.User, error)
}

// New constructs the reviewed Darwin low-port launchd adapter.
func New() (*Adapter, error) {
	return newAdapter(darwinBackend{run: runDarwinLaunchctl, print: printDarwinLaunchctl, lookupUser: user.LookupId}), nil
}

// observe inspects the one fixed direct plist without following links.
func (b darwinBackend) observe(ctx context.Context, request Request) (Observation, error) {
	if err := ctx.Err(); err != nil {
		return Observation{}, err
	}
	parent, err := openDarwinParent()
	if err != nil {
		return Observation{}, err
	}
	defer parent.Close()
	content, err := b.plist(request)
	if err != nil {
		return Observation{}, err
	}
	plist, err := observeDarwinPlist(parent, content)
	if err != nil {
		return Observation{}, err
	}
	service, err := b.observeService(ctx, request)
	if err != nil {
		return Observation{}, err
	}
	return Observation{Request: request, Complete: true, Artifacts: []Artifact{plist, service}}, nil
}

// ensure writes only the fixed owned plist and bootstraps its fixed system label.
func (b darwinBackend) ensure(ctx context.Context, request Request, before Observation) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	parent, err := openDarwinParent()
	if err != nil {
		return err
	}
	defer parent.Close()
	plist, service := findArtifacts(before)
	if plist == nil || service == nil {
		return fmt.Errorf("low-port ensure lacks complete native facts")
	}
	created := false
	if !plist.Present {
		content, err := b.plist(request)
		if err != nil {
			return err
		}
		file, err := openCreatedDarwinPlist(parent, content)
		if err != nil {
			return err
		}
		if err := file.Close(); err != nil {
			return err
		}
		if err := parent.Sync(); err != nil {
			return err
		}
		created = true
	}
	if err := b.run(ctx, "bootstrap", "system", darwinPlistPath); err != nil {
		if created {
			return errors.Join(err, b.rollbackCreatedService(ctx, parent, request))
		}
		return err
	}
	return nil
}

// release bootouts the fixed label then removes only an exact owned direct plist.
func (b darwinBackend) release(ctx context.Context, request Request, before Observation) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	parent, err := openDarwinParent()
	if err != nil {
		return err
	}
	defer parent.Close()
	plist, service := findArtifacts(before)
	if plist == nil || service == nil || !plist.Exact || !service.Exact {
		return fmt.Errorf("low-port release lacks exact owned native facts")
	}
	if err := b.run(ctx, "bootout", "system/"+darwinLabel); err != nil {
		return err
	}
	return b.removeExactPlist(parent, request)
}

// openCreatedDarwinPlist creates the absent fixed plist through its retained parent without following a final symlink.
func openCreatedDarwinPlist(parent *os.File, content []byte) (*os.File, error) {
	name := "com.goforj.harbor.launchdrelay.plist"
	descriptor, err := unix.Openat(int(parent.Fd()), name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o644)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(descriptor), darwinPlistPath)
	if _, err := file.Write(content); err != nil {
		_ = file.Close()
		_ = unix.Unlinkat(int(parent.Fd()), name, 0)
		return nil, err
	}
	if err := unix.Fchown(int(file.Fd()), 0, 0); err != nil {
		_ = file.Close()
		_ = unix.Unlinkat(int(parent.Fd()), name, 0)
		return nil, err
	}
	if err := unix.Fchmod(int(file.Fd()), 0o644); err != nil {
		_ = file.Close()
		_ = unix.Unlinkat(int(parent.Fd()), name, 0)
		return nil, err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		_ = unix.Unlinkat(int(parent.Fd()), name, 0)
		return nil, err
	}
	return file, nil
}

// openDarwinParent retains a no-follow descriptor for the fixed privileged directory.
func openDarwinParent() (*os.File, error) {
	descriptor, err := unix.Open(darwinLaunchDaemonDirectory, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	parent := os.NewFile(uintptr(descriptor), darwinLaunchDaemonDirectory)
	info, err := parent.Stat()
	if err != nil {
		_ = parent.Close()
		return nil, err
	}
	status, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || status.Uid != 0 || info.Mode().Perm()&0o022 != 0 {
		_ = parent.Close()
		return nil, fmt.Errorf("Darwin LaunchDaemons parent is not secure")
	}
	return parent, nil
}

// observeDarwinPlist reads the direct fixed name through a retained no-follow parent descriptor.
func observeDarwinPlist(parent *os.File, expected []byte) (Artifact, error) {
	artifact := Artifact{Kind: ArtifactKindPlist, Fingerprint: emptyArtifactFingerprint()}
	descriptor, err := unix.Openat(int(parent.Fd()), darwinPlistName(), unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if errors.Is(err, unix.ENOENT) {
		return artifact, nil
	}
	if err != nil {
		return Artifact{}, err
	}
	file := os.NewFile(uintptr(descriptor), darwinPlistPath)
	info, statErr := file.Stat()
	content, readErr := io.ReadAll(io.LimitReader(file, maximumPlistBytes+1))
	closeErr := file.Close()
	if statErr != nil || readErr != nil || closeErr != nil {
		return Artifact{}, errors.Join(statErr, readErr, closeErr)
	}
	artifact.Present = true
	status, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || status.Nlink != 1 || len(content) > maximumPlistBytes {
		artifact.Fingerprint = malformedArtifactFingerprint(info.Mode().String())
		return artifact, nil
	}
	digest := sha256.Sum256(content)
	artifact.Fingerprint = hex.EncodeToString(digest[:])
	artifact.Owned = bytes.Equal(content, expected)
	artifact.Exact = artifact.Owned && status.Uid == 0 && status.Gid == 0 && info.Mode().Perm() == 0o644
	return artifact, nil
}

// observeService proves the matching service is loaded in launchd's system domain.
func (b darwinBackend) observeService(ctx context.Context, request Request) (Artifact, error) {
	artifact := Artifact{Kind: ArtifactKindService, Fingerprint: emptyArtifactFingerprint()}
	print := b.print
	if print == nil {
		print = printDarwinLaunchctl
	}
	output, err := print(ctx, "print", "system/"+darwinLabel)
	if err != nil {
		if isDarwinLaunchctlNotFound(err) {
			return artifact, nil
		}
		return Artifact{}, err
	}
	artifact.Present = true
	username, err := b.username(request)
	if err != nil {
		return Artifact{}, err
	}
	artifact.Exact = matchesDarwinServiceContract(output, request, username)
	artifact.Owned = artifact.Exact
	if artifact.Exact {
		artifact.Fingerprint = canonicalServiceFingerprint(request, username)
	} else {
		artifact.Fingerprint = malformedArtifactFingerprint("loaded-service-contract-mismatch")
	}
	return artifact, nil
}

// matchesDarwinServiceContract accepts only a loaded job which reports every
// immutable relay argument, owner, socket endpoint, and installation marker.
func matchesDarwinServiceContract(output []byte, request Request, username string) bool {
	text, ok := launchctlRoot(string(output))
	if !ok {
		return false
	}
	return exactlyOneScalar(text, "path", darwinPlistPath) &&
		exactlyOneScalar(text, "program", launchdrelaypath.Executable()) &&
		exactlyOneScalar(text, "username", username) &&
		hasOnlyReviewedTopLevelFields(text) &&
		matchesOrderedDarwinArguments(text, request) &&
		matchesDarwinEnvironment(text, request) &&
		matchesDarwinSockets(text)
}

// hasOnlyReviewedTopLevelFields rejects plist-derived privilege and process-scope settings not in Harbor's contract.
func hasOnlyReviewedTopLevelFields(text string) bool {
	allowed := map[string]bool{
		"path": true, "program": true, "username": true, "arguments": true, "environment": true, "sockets": true,
		"state": true, "active count": true, "pid": true, "last exit code": true, "runs": true, "spawn count": true,
	}
	depth := 0
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if depth == 0 && trimmed != "" {
			key, _, found := strings.Cut(trimmed, " =")
			if !found || !allowed[key] {
				return false
			}
		}
		depth += strings.Count(trimmed, "{") - strings.Count(trimmed, "}")
	}
	return depth == 0
}

// launchctlRoot requires one outer print object so decoy nested fields cannot become service facts.
func launchctlRoot(text string) (string, bool) {
	lines := strings.Split(text, "\n")
	if len(lines) < 2 || !strings.HasSuffix(strings.TrimSpace(lines[0]), "= {") {
		return "", false
	}
	return launchctlBlock(text, strings.TrimSuffix(strings.TrimSpace(lines[0]), " = {"))
}

// exactlyOneScalar requires one top-level launchctl scalar with no duplicate key or value substitution.
func exactlyOneScalar(text, key, expected string) bool {
	lines := strings.Split(text, "\n")
	found, depth := 0, 0
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if depth == 0 && strings.HasPrefix(trimmed, key+" =") {
			found++
			if strings.TrimSpace(strings.TrimPrefix(trimmed, key+" =")) != expected {
				return false
			}
		}
		depth += strings.Count(trimmed, "{") - strings.Count(trimmed, "}")
	}
	return found == 1
}

// matchesOrderedDarwinArguments binds every relay flag to its adjacent value and rejects extras or reordering.
func matchesOrderedDarwinArguments(text string, request Request) bool {
	values, ok := launchctlBlockValues(text, "arguments")
	if !ok {
		return false
	}
	want := []string{launchdrelaypath.Executable(), "--owner-uid", strconv.FormatUint(uint64(request.OwnerUID()), 10), "--policy-fingerprint", request.PolicyFingerprint(), "--http-upstream", request.HTTPUpstream().String(), "--https-upstream", request.HTTPSUpstream().String()}
	return slices.Equal(values, want)
}

// matchesDarwinEnvironment requires the exact sole installation marker rather than a string elsewhere in print output.
func matchesDarwinEnvironment(text string, request Request) bool {
	values, ok := launchctlBlockPairs(text, "environment")
	return ok && len(values) == 1 && values["HARBOR_INSTALLATION_ID"] == request.InstallationID()
}

// matchesDarwinSocket requires one named socket block with only the fixed loopback node and service port.
func matchesDarwinSockets(text string) bool {
	sockets, ok := launchctlBlock(text, "sockets")
	if !ok || countLaunchctlHeaders(sockets) != 2 {
		return false
	}
	for name, port := range map[string]string{"HTTP": "80", "HTTPS": "443"} {
		values, found := launchctlBlockPairs(sockets, name)
		if !found || !matchesDarwinSocketValues(values, port) {
			return false
		}
	}
	return true
}

// matchesDarwinSocketValues permits only launchd's reviewed derived TCP facts beside Harbor's fixed endpoint.
func matchesDarwinSocketValues(values map[string]string, port string) bool {
	if values["SockNodeName"] != "127.0.0.1" || values["SockServiceName"] != port {
		return false
	}
	for key, value := range values {
		switch key {
		case "SockNodeName", "SockServiceName":
		case "SockType":
			if value != "stream" {
				return false
			}
		case "SockProtocol":
			if value != "tcp" {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// countLaunchctlHeaders counts only direct child blocks in a parsed parent.
func countLaunchctlHeaders(text string) int {
	count, depth := 0, 0
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if depth == 0 && strings.HasSuffix(trimmed, " = {") {
			count++
		}
		depth += strings.Count(trimmed, "{") - strings.Count(trimmed, "}")
	}
	return count
}

// launchctlBlockValues parses a single brace-delimited list whose members must be bare ordered values.
func launchctlBlockValues(text, name string) ([]string, bool) {
	block, ok := launchctlBlock(text, name)
	if !ok {
		return nil, false
	}
	lines := nonemptyBlockLines(block)
	values := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.Contains(line, "=") || strings.Contains(line, "{") || strings.Contains(line, "}") {
			return nil, false
		}
		values = append(values, line)
	}
	return values, true
}

// launchctlBlockPairs parses a single brace-delimited key/value block and rejects duplicate or malformed entries.
func launchctlBlockPairs(text, name string) (map[string]string, bool) {
	block, ok := launchctlBlock(text, name)
	if !ok {
		return nil, false
	}
	values := make(map[string]string)
	for _, line := range nonemptyBlockLines(block) {
		key, value, found := strings.Cut(line, " = ")
		if !found || key == "" || value == "" || strings.Contains(value, "{") || strings.Contains(value, "}") {
			return nil, false
		}
		if _, duplicate := values[key]; duplicate {
			return nil, false
		}
		values[key] = value
	}
	return values, true
}

// launchctlBlock finds exactly one named balanced block and refuses trailing text in its header.
func launchctlBlock(text, name string) (string, bool) {
	header := name + " = {"
	lines, found, depth := strings.Split(text, "\n"), -1, 0
	for index, line := range lines {
		trimmed := strings.TrimSpace(line)
		if depth == 0 && trimmed == header {
			if found >= 0 {
				return "", false
			}
			found = index
		}
		depth += strings.Count(trimmed, "{") - strings.Count(trimmed, "}")
	}
	if found < 0 {
		return "", false
	}
	depth = 1
	var block []string
	for _, line := range lines[found+1:] {
		trimmed := strings.TrimSpace(line)
		depth += strings.Count(trimmed, "{") - strings.Count(trimmed, "}")
		if depth == 0 {
			return strings.Join(block, "\n"), true
		}
		block = append(block, line)
	}
	return "", false
}

// nonemptyBlockLines returns normalized members without accepting hidden indentation-only entries.
func nonemptyBlockLines(block string) []string {
	var lines []string
	for _, line := range strings.Split(block, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			lines = append(lines, trimmed)
		}
	}
	return lines
}

// canonicalServiceFingerprint excludes volatile launchctl state such as PID and restart counters.
func canonicalServiceFingerprint(request Request, username string) string {
	digest := sha256.Sum256([]byte("goforj.harbor.lowport.launchd-service.v1\x00" + request.InstallationID() + "\x00" + username + "\x00" + request.PolicyFingerprint() + "\x00" + request.HTTPUpstream().String() + "\x00" + request.HTTPSUpstream().String()))
	return hex.EncodeToString(digest[:])
}

// findArtifacts returns the two required typed observations without trusting their order.
func findArtifacts(observation Observation) (plist, service *Artifact) {
	for index := range observation.Artifacts {
		artifact := &observation.Artifacts[index]
		switch artifact.Kind {
		case ArtifactKindPlist:
			plist = artifact
		case ArtifactKindService:
			service = artifact
		}
	}
	return plist, service
}

// removeExactDarwinPlist rechecks the direct descriptor before unlinking the fixed name and syncing its parent.
func (b darwinBackend) removeExactPlist(parent *os.File, request Request) error {
	expected, err := b.plist(request)
	if err != nil {
		return err
	}
	plist, err := observeDarwinPlist(parent, expected)
	if err != nil {
		return err
	}
	if !plist.Exact {
		return fmt.Errorf("fixed low-port plist changed before release")
	}
	if err := unix.Unlinkat(int(parent.Fd()), darwinPlistName(), 0); err != nil {
		return err
	}
	return parent.Sync()
}

// plist resolves the numeric schema owner to launchd's canonical real account name.
func (b darwinBackend) plist(request Request) ([]byte, error) {
	username, err := b.username(request)
	if err != nil {
		return nil, err
	}
	return darwinPlist(request, username), nil
}

// username resolves and validates the canonical real account name for launchd.
func (b darwinBackend) username(request Request) (string, error) {
	lookup := b.lookupUser
	if lookup == nil {
		lookup = user.LookupId
	}
	uid := strconv.FormatUint(uint64(request.OwnerUID()), 10)
	account, err := lookup(uid)
	if err != nil {
		return "", fmt.Errorf("resolve Darwin low-port owner UID %q: %w", uid, err)
	}
	if account == nil || account.Uid != uid || account.Username == "" {
		return "", fmt.Errorf("resolve Darwin low-port owner UID %q: invalid account", uid)
	}
	return account.Username, nil
}

// rollbackCreatedService avoids leaving a just-created plist behind after an uncertain bootstrap result.
func (b darwinBackend) rollbackCreatedService(ctx context.Context, parent *os.File, request Request) error {
	service, err := b.observeService(ctx, request)
	if err != nil {
		return err
	}
	if service.Present {
		if !service.Exact {
			return fmt.Errorf("bootstrap outcome is indeterminate")
		}
		if err := b.run(ctx, "bootout", "system/"+darwinLabel); err != nil {
			return err
		}
	}
	return b.removeExactPlist(parent, request)
}

// darwinPlistName keeps all parent-relative operations bound to one fixed leaf.
func darwinPlistName() string { return "com.goforj.harbor.launchdrelay.plist" }

// darwinPlist emits the sole canonical service definition accepted as Harbor-owned.
func darwinPlist(request Request, username string) []byte {
	args := []string{launchdrelaypath.Executable(), "--owner-uid", strconv.FormatUint(uint64(request.OwnerUID()), 10), "--policy-fingerprint", request.PolicyFingerprint(), "--http-upstream", request.HTTPUpstream().String(), "--https-upstream", request.HTTPSUpstream().String()}
	var value bytes.Buffer
	value.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<!DOCTYPE plist PUBLIC \"-//Apple//DTD PLIST 1.0//EN\" \"http://www.apple.com/DTDs/PropertyList-1.0.dtd\">\n<plist version=\"1.0\"><dict><key>Label</key><string>")
	value.WriteString(darwinLabel)
	value.WriteString("</string><key>ProgramArguments</key><array>")
	for _, argument := range args {
		value.WriteString("<string>")
		writeDarwinPlistText(&value, argument)
		value.WriteString("</string>")
	}
	value.WriteString("</array><key>Sockets</key><dict><key>HTTP</key><dict><key>SockNodeName</key><string>127.0.0.1</string><key>SockServiceName</key><string>80</string></dict><key>HTTPS</key><dict><key>SockNodeName</key><string>127.0.0.1</string><key>SockServiceName</key><string>443</string></dict></dict><key>UserName</key><string>")
	writeDarwinPlistText(&value, username)
	value.WriteString("</string><key>EnvironmentVariables</key><dict><key>HARBOR_INSTALLATION_ID</key><string>")
	writeDarwinPlistText(&value, request.InstallationID())
	value.WriteString("</string></dict><key>RunAtLoad</key><true/></dict></plist>\n")
	return value.Bytes()
}

// writeDarwinPlistText escapes local account and installation metadata as plist element text.
func writeDarwinPlistText(value *bytes.Buffer, text string) {
	_ = xml.EscapeText(value, []byte(text))
}

// runDarwinLaunchctl executes only direct fixed-path launchctl vectors and retains bounded diagnostic output.
func runDarwinLaunchctl(ctx context.Context, arguments ...string) error {
	if !isDarwinMutationVector(arguments) {
		return fmt.Errorf("unreviewed launchctl mutation vector")
	}
	_, err := runDarwinLaunchctlOutput(ctx, arguments...)
	return err
}

// isDarwinMutationVector recognizes the sole fixed bootstrap and bootout operations.
func isDarwinMutationVector(arguments []string) bool {
	return len(arguments) == 3 && arguments[0] == "bootstrap" && arguments[1] == "system" && arguments[2] == darwinPlistPath || len(arguments) == 2 && arguments[0] == "bootout" && arguments[1] == "system/"+darwinLabel
}

// printDarwinLaunchctl runs only the fixed system-label inspection vector.
func printDarwinLaunchctl(ctx context.Context, arguments ...string) ([]byte, error) {
	if len(arguments) != 2 || arguments[0] != "print" || arguments[1] != "system/"+darwinLabel {
		return nil, fmt.Errorf("unreviewed launchctl inspection vector")
	}
	return runDarwinLaunchctlOutput(ctx, arguments...)
}

// runDarwinLaunchctlOutput captures bounded direct launchctl diagnostics for typed absence recognition.
func runDarwinLaunchctlOutput(ctx context.Context, arguments ...string) ([]byte, error) {
	commandContext, cancel := context.WithTimeout(normalizedContext(ctx), launchctlTimeout)
	defer cancel()
	command := exec.CommandContext(commandContext, "/bin/launchctl", arguments...)
	output := &limitedBuffer{remaining: maximumLaunchctlOutput}
	command.Stdout = output
	command.Stderr = output
	if err := command.Run(); err != nil {
		return nil, &darwinLaunchctlError{cause: err, output: append([]byte(nil), output.value...)}
	}
	if output.saturated {
		return nil, fmt.Errorf("launchctl output exceeded bound")
	}
	return append([]byte(nil), output.value...), nil
}

// darwinLaunchctlError retains only bounded output for the exact absent-service classifier.
type darwinLaunchctlError struct {
	cause  error
	output []byte
}

// Error avoids exposing launchctl diagnostics through normal helper responses.
func (e *darwinLaunchctlError) Error() string { return "launchctl failed" }

// Unwrap retains the native exit failure for callers that need cancellation semantics.
func (e *darwinLaunchctlError) Unwrap() error { return e.cause }

// isDarwinLaunchctlNotFound admits only launchctl's known missing-service diagnostic.
func isDarwinLaunchctlNotFound(err error) bool {
	var launchctlError *darwinLaunchctlError
	if !errors.As(err, &launchctlError) {
		return false
	}
	var exitError *exec.ExitError
	return errors.As(launchctlError.cause, &exitError) && strings.Contains(strings.ToLower(string(launchctlError.output)), "could not find service")
}

// limitedBuffer bounds command diagnostics so a privileged child cannot force unbounded allocation.
type limitedBuffer struct {
	remaining int
	value     []byte
	saturated bool
}

// Write retains at most the fixed diagnostic bound while reporting consumption to exec.
func (b *limitedBuffer) Write(value []byte) (int, error) {
	accepted := min(len(value), b.remaining)
	b.value = append(b.value, value[:accepted]...)
	b.remaining -= accepted
	b.saturated = b.saturated || accepted != len(value)
	return len(value), nil
}

var _ io.Writer = (*limitedBuffer)(nil)

// emptyArtifactFingerprint provides canonical evidence for an absent fixed plist.
func emptyArtifactFingerprint() string {
	digest := sha256.Sum256([]byte("absent"))
	return hex.EncodeToString(digest[:])
}

// malformedArtifactFingerprint records a bounded malformed shape without exposing paths or content.
func malformedArtifactFingerprint(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}
