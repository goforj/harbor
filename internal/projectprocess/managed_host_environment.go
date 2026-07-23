package projectprocess

import (
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
)

const (
	managedHostEnvironmentBegin               = "# harbor managed: begin"
	managedHostEnvironmentEnd                 = "# harbor managed: end"
	managedHostEnvironmentRestoreFinalNewline = "# harbor managed: restore final newline"
	lighthouseAgentPath                       = "/lighthouse/ws/agent"
)

var managedHostEnvironmentKeys = map[string]struct{}{
	"API_HTTP_HOST":          {},
	"DEV_SERVICE_IP_ADDRESS": {},
	"IP_ADDRESS":             {},
	"LIGHTHOUSE_URL":         {},
}

// syncManagedHostEnvironmentParentDirectory provides the platform durability boundary for dotenv publication tests.
var syncManagedHostEnvironmentParentDirectory = syncManagedHostEnvironmentDirectory

// prepareManagedHostEnvironment retires one exact legacy Harbor block and derives all child endpoint overrides.
func prepareManagedHostEnvironment(checkoutRoot string, overrides EnvironmentOverrides) (EnvironmentOverrides, error) {
	addressValue, present := overrides["IP_ADDRESS"]
	if !present {
		return cloneEnvironmentOverrides(overrides), nil
	}
	address, err := netip.ParseAddr(strings.TrimSpace(addressValue))
	if err != nil || !address.IsLoopback() {
		return nil, fmt.Errorf("IP_ADDRESS must contain a loopback address: %q", addressValue)
	}

	path := filepath.Join(checkoutRoot, ".env.host")
	contents, mode, identity, exists, err := readManagedHostEnvironment(path)
	if err != nil {
		return nil, err
	}
	cleaned, removed := removeManagedHostEnvironmentBlock(string(contents))
	if removed {
		if cleaned == "" {
			if err := removeManagedHostEnvironmentFile(path, identity); err != nil {
				return nil, fmt.Errorf("remove legacy Harbor block from %q: %w", path, err)
			}
		} else if err := replaceManagedHostEnvironment(path, []byte(cleaned), mode, identity); err != nil {
			return nil, fmt.Errorf("remove legacy Harbor block from %q: %w", path, err)
		}
		contents = []byte(cleaned)
	}
	if !exists {
		contents = nil
	}
	values := managedHostEnvironmentValues(string(contents), address.String(), overrides)
	return values, nil
}

// managedHostEnvironmentValues derives local endpoints from readable project dotenv content without making malformed user content fatal.
func managedHostEnvironmentValues(contents string, address string, overrides EnvironmentOverrides) EnvironmentOverrides {
	parsed, err := godotenv.Unmarshal(contents)
	if err != nil {
		parsed = nil
	}
	values := make(EnvironmentOverrides, len(parsed)+len(overrides)+1)
	for name, value := range parsed {
		if validateEnvironmentOverrideName(name) != nil {
			continue
		}
		if rewritten, changed := rewriteLocalEnvironmentValue(value, address); changed {
			values[name] = rewritten
		}
	}
	for name, value := range overrides {
		values[name] = value
	}
	values["API_HTTP_HOST"] = address
	return values
}

// compileBuildEnvironmentOverrides limits child build overrides to assigned loopback endpoint hosts.
func compileBuildEnvironmentOverrides(values EnvironmentOverrides) string {
	names := sortedEnvironmentOverrideNames(values)
	addressValue := ""
	for _, name := range names {
		if strings.EqualFold(name, "IP_ADDRESS") {
			addressValue = values[name]
			break
		}
	}
	address, err := netip.ParseAddr(strings.TrimSpace(addressValue))
	if err != nil || !address.IsLoopback() {
		return ""
	}
	assignedAddress := address.String()
	assignments := make([]string, 0, len(values))
	for _, name := range names {
		value := values[name]
		if validateEnvironmentOverrideName(name) != nil {
			continue
		}
		if isBuildEndpointHostName(name) && value == assignedAddress {
			assignments = append(assignments, name+"="+assignedAddress)
			continue
		}
		if strings.EqualFold(name, "LIGHTHOUSE_URL") {
			if endpoint, safe := canonicalLighthouseAgentURL(value, assignedAddress); safe {
				assignments = append(assignments, name+"="+endpoint)
			}
		}
	}
	return strings.Join(assignments, ",")
}

// isBuildEndpointHostName recognizes the case-insensitive host assignments passed to child builds.
func isBuildEndpointHostName(name string) bool {
	return strings.EqualFold(name, "IP_ADDRESS") ||
		strings.EqualFold(name, "DEV_SERVICE_IP_ADDRESS") ||
		strings.HasSuffix(strings.ToUpper(name), "_HOST")
}

// canonicalLighthouseAgentURL accepts only Harbor's delimiter-safe agent endpoint and serializes it canonically.
func canonicalLighthouseAgentURL(value string, assignedAddress string) (string, bool) {
	parsed, err := url.Parse(value)
	if err != nil {
		return "", false
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "ws" && scheme != "wss" {
		return "", false
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", false
	}
	if parsed.Path != lighthouseAgentPath || parsed.RawPath != "" {
		return "", false
	}
	if parsed.Hostname() != assignedAddress {
		return "", false
	}
	portValue := parsed.Port()
	if !isDecimalPort(portValue) {
		return "", false
	}
	port, err := strconv.Atoi(portValue)
	if err != nil || port < 1 || port > 65535 {
		return "", false
	}
	endpoint := url.URL{
		Scheme: scheme,
		Host:   net.JoinHostPort(assignedAddress, strconv.Itoa(port)),
		Path:   lighthouseAgentPath,
	}
	return endpoint.String(), true
}

// isDecimalPort rejects URL port syntax that cannot be represented unambiguously in the child contract.
func isDecimalPort(value string) bool {
	if value == "" {
		return false
	}
	for index := range value {
		if value[index] < '0' || value[index] > '9' {
			return false
		}
	}
	return true
}

// rewriteLocalEnvironmentValue moves literal localhost endpoints onto the project's assigned loopback address.
func rewriteLocalEnvironmentValue(value string, address string) (string, bool) {
	if isLocalEnvironmentHost(value) {
		return address, true
	}
	if parsed, err := url.Parse(value); err == nil && parsed.Scheme != "" && parsed.Host != "" && isLocalEnvironmentHost(parsed.Hostname()) {
		parsed.Host = environmentHostPort(address, parsed.Port())
		return parsed.String(), true
	}
	host, port, err := net.SplitHostPort(value)
	if err == nil && isLocalEnvironmentHost(host) {
		return net.JoinHostPort(address, port), true
	}
	return value, false
}

// isLocalEnvironmentHost recognizes literal addresses that generated host configuration uses for local services.
func isLocalEnvironmentHost(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "localhost", "127.0.0.1", "0.0.0.0", "::1", "[::1]":
		return true
	default:
		return false
	}
}

// environmentHostPort formats a URL authority without adding an empty port delimiter.
func environmentHostPort(address string, port string) string {
	if port != "" {
		return net.JoinHostPort(address, port)
	}
	if strings.Contains(address, ":") {
		return "[" + address + "]"
	}
	return address
}

// removeManagedHostEnvironmentBlock removes exactly one complete, byte-exact Harbor legacy block.
func removeManagedHostEnvironmentBlock(contents string) (string, bool) {
	type marker struct {
		start int
		end   int
	}
	var begins []marker
	var ends []marker
	for start := 0; start < len(contents); {
		end := len(contents)
		if newline := strings.IndexByte(contents[start:], '\n'); newline >= 0 {
			end = start + newline + 1
		}
		lineEnd := end
		if end > start && contents[end-1] == '\n' {
			lineEnd--
		}
		if lineEnd > start && contents[lineEnd-1] == '\r' {
			lineEnd--
		}
		switch contents[start:lineEnd] {
		case managedHostEnvironmentBegin:
			begins = append(begins, marker{
				start: start,
				end:   end,
			})
		case managedHostEnvironmentEnd:
			ends = append(ends, marker{
				start: start,
				end:   end,
			})
		}
		start = end
	}
	if len(begins) != 1 || len(ends) != 1 || begins[0].start >= ends[0].start {
		return contents, false
	}
	prefix := contents[:begins[0].start]
	suffix := contents[ends[0].end:]
	block := contents[begins[0].end:ends[0].start]
	if managedHostEnvironmentNeedsFinalNewlineRestore(block) {
		newline := "\n"
		if strings.HasSuffix(prefix, "\r\n") {
			newline = "\r\n"
		}
		if !strings.HasSuffix(prefix, newline) {
			return contents, false
		}
		prefix = strings.TrimSuffix(prefix, newline)
	}
	return prefix + suffix, true
}

// managedHostEnvironmentNeedsFinalNewlineRestore recognizes Harbor's exact no-final-newline restoration marker.
func managedHostEnvironmentNeedsFinalNewlineRestore(block string) bool {
	lineEnd := strings.IndexByte(block, '\n')
	if lineEnd < 0 {
		lineEnd = len(block)
	}
	line := strings.TrimSuffix(block[:lineEnd], "\r")
	return line == managedHostEnvironmentRestoreFinalNewline
}

// replaceManagedHostEnvironment stages complete contents beside the destination before publishing them.
func replaceManagedHostEnvironment(path string, contents []byte, mode fs.FileMode, expected fs.FileInfo) (replaceErr error) {
	temporary, err := os.CreateTemp(filepath.Dir(path), ".env.host.harbor-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	closed := false
	defer func() {
		if !closed {
			replaceErr = errors.Join(replaceErr, temporary.Close())
		}
		replaceErr = errors.Join(replaceErr, removeManagedHostEnvironmentTemporary(temporaryPath))
	}()
	if err := temporary.Chmod(mode); err != nil {
		return err
	}
	if _, err := temporary.Write(contents); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		closed = true
		return err
	}
	closed = true
	// Supervisor.reserveLaunch serializes Harbor writers for one checkout. Project-controlled files are same-user state,
	// so this identity check deliberately avoids introducing a broader filesystem locking protocol for legacy migration.
	if err := verifyManagedHostEnvironmentIdentity(path, expected); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	return syncManagedHostEnvironmentParentDirectory(filepath.Dir(path))
}

// removeManagedHostEnvironmentFile removes the exact legacy file after proving it still names the inspected inode.
func removeManagedHostEnvironmentFile(path string, expected fs.FileInfo) error {
	if err := verifyManagedHostEnvironmentIdentity(path, expected); err != nil {
		return err
	}
	return errors.Join(os.Remove(path), syncManagedHostEnvironmentParentDirectory(filepath.Dir(path)))
}

// removeManagedHostEnvironmentTemporary removes only an unpublished temporary file.
func removeManagedHostEnvironmentTemporary(path string) error {
	err := os.Remove(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}
