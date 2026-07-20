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
	"runtime"
	"strings"

	"github.com/joho/godotenv"
)

const (
	managedHostEnvironmentBegin = "# harbor managed: begin"
	managedHostEnvironmentEnd   = "# harbor managed: end"
)

var managedHostEnvironmentKeys = map[string]struct{}{
	"API_HTTP_HOST":          {},
	"DEV_SERVICE_IP_ADDRESS": {},
	"IP_ADDRESS":             {},
	"LIGHTHOUSE_URL":         {},
}

// writeManagedHostEnvironment places Harbor's launch overrides in the project's final host dotenv layer.
func writeManagedHostEnvironment(checkoutRoot string, overrides EnvironmentOverrides) (EnvironmentOverrides, error) {
	addressValue, present := overrides["IP_ADDRESS"]
	if !present {
		return nil, nil
	}
	address, err := netip.ParseAddr(strings.TrimSpace(addressValue))
	if err != nil || !address.IsLoopback() {
		return nil, fmt.Errorf("IP_ADDRESS must contain a loopback address: %q", addressValue)
	}

	path := filepath.Join(checkoutRoot, ".env.host")
	contents, mode, err := readManagedHostEnvironment(path)
	if err != nil {
		return nil, err
	}
	base, err := removeManagedHostEnvironmentBlock(string(contents))
	if err != nil {
		return nil, fmt.Errorf("inspect %q: %w", path, err)
	}
	values, err := managedHostEnvironmentValues(base, address.String(), overrides)
	if err != nil {
		return nil, fmt.Errorf("derive %q overrides: %w", path, err)
	}
	updated, err := appendManagedHostEnvironmentBlock(base, values)
	if err != nil {
		return nil, fmt.Errorf("render %q overrides: %w", path, err)
	}
	if string(contents) == updated {
		return values, nil
	}
	if err := replaceManagedHostEnvironment(path, []byte(updated), mode); err != nil {
		return nil, fmt.Errorf("write %q: %w", path, err)
	}
	return values, nil
}

// removeManagedHostEnvironment removes only Harbor's exact final dotenv block after a settled owned shutdown.
func removeManagedHostEnvironment(checkoutRoot string) error {
	path := filepath.Join(checkoutRoot, ".env.host")
	contents, mode, err := readManagedHostEnvironment(path)
	if err != nil {
		return err
	}
	base, err := removeManagedHostEnvironmentBlock(string(contents))
	if err != nil {
		return fmt.Errorf("inspect %q: %w", path, err)
	}
	if base == string(contents) {
		return nil
	}
	if base != "" {
		if err := replaceManagedHostEnvironment(path, []byte(base), mode); err != nil {
			return fmt.Errorf("write %q: %w", path, err)
		}
		return nil
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("remove %q: %w", path, err)
	}
	if runtime.GOOS == "windows" {
		return nil
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	return errors.Join(directory.Sync(), directory.Close())
}

// readManagedHostEnvironment reads only direct regular files so Harbor cannot overwrite a linked project path.
func readManagedHostEnvironment(path string) ([]byte, fs.FileMode, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, 0o600, nil
	}
	if err != nil {
		return nil, 0, fmt.Errorf("inspect %q: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return nil, 0, fmt.Errorf("%q must be a direct regular file", path)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return nil, 0, fmt.Errorf("read %q: %w", path, err)
	}
	return contents, info.Mode().Perm(), nil
}

// managedHostEnvironmentValues derives only local network endpoints that must follow Harbor's assigned address.
func managedHostEnvironmentValues(base string, address string, overrides EnvironmentOverrides) (EnvironmentOverrides, error) {
	parsed, err := godotenv.Unmarshal(base)
	if err != nil {
		return nil, err
	}
	values := make(EnvironmentOverrides)
	for name, value := range parsed {
		if validateEnvironmentOverrideName(name) != nil {
			continue
		}
		if rewritten, changed := rewriteLocalEnvironmentValue(value, address); changed {
			values[name] = rewritten
		}
	}
	for name := range managedHostEnvironmentKeys {
		if value, present := overrides[name]; present {
			values[name] = value
		}
	}
	values["API_HTTP_HOST"] = address
	return values, nil
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

// removeManagedHostEnvironmentBlock removes one complete prior block before Harbor appends the authoritative block at EOF.
func removeManagedHostEnvironmentBlock(contents string) (string, error) {
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
		line := strings.TrimSpace(strings.TrimSuffix(contents[start:end], "\n"))
		switch line {
		case managedHostEnvironmentBegin:
			begins = append(begins, marker{start: start, end: end})
		case managedHostEnvironmentEnd:
			ends = append(ends, marker{start: start, end: end})
		}
		start = end
	}
	if len(begins) == 0 && len(ends) == 0 {
		return contents, nil
	}
	if len(begins) != 1 || len(ends) != 1 || begins[0].start >= ends[0].start {
		return "", errors.New("Harbor managed markers must contain one ordered begin/end pair")
	}
	return contents[:begins[0].start] + contents[ends[0].end:], nil
}

// appendManagedHostEnvironmentBlock appends sorted dotenv assignments so duplicate user defaults cannot win later in the file.
func appendManagedHostEnvironmentBlock(base string, values EnvironmentOverrides) (string, error) {
	encoded, err := godotenv.Marshal(map[string]string(values))
	if err != nil {
		return "", err
	}
	newline := "\n"
	if strings.Contains(base, "\r\n") {
		newline = "\r\n"
	}
	encoded = strings.ReplaceAll(encoded, "\n", newline)
	block := strings.Join([]string{
		managedHostEnvironmentBegin,
		encoded,
		managedHostEnvironmentEnd,
	}, newline)
	result := base
	if result != "" {
		if !strings.HasSuffix(result, "\n") {
			result += newline
		}
		if !strings.HasSuffix(result, newline+newline) {
			result += newline
		}
	}
	return result + block + newline, nil
}

// replaceManagedHostEnvironment stages complete contents beside the destination before publishing them.
func replaceManagedHostEnvironment(path string, contents []byte, mode fs.FileMode) (replaceErr error) {
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
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	if runtime.GOOS != "windows" {
		directory, err := os.Open(filepath.Dir(path))
		if err != nil {
			return err
		}
		syncErr := directory.Sync()
		closeErr := directory.Close()
		if syncErr != nil || closeErr != nil {
			return errors.Join(syncErr, closeErr)
		}
	}
	return nil
}

// removeManagedHostEnvironmentTemporary removes only an unpublished temporary file.
func removeManagedHostEnvironmentTemporary(path string) error {
	err := os.Remove(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}
