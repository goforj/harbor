package projectdiscovery

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/joho/godotenv"

	"github.com/goforj/harbor/internal/domain"
)

const defaultAppHTTPPort uint16 = 3000

// RuntimeTarget identifies the local default App listener Harbor can prove ready without trusting a public project URL.
type RuntimeTarget struct {
	AppID       domain.AppID
	Name        string
	Port        uint16
	ResourceURL string
	ReadyURL    string
}

// Validate reports whether the target is one internally consistent local HTTP runtime.
func (target RuntimeTarget) Validate() error {
	if err := target.AppID.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(target.Name) == "" || strings.TrimSpace(target.Name) != target.Name {
		return fmt.Errorf("runtime target name must be non-empty without surrounding whitespace")
	}
	if target.Port == 0 {
		return fmt.Errorf("runtime target port must be positive")
	}
	resourceURL := runtimeLocalURL(target.Port, "")
	readyURL := runtimeLocalURL(target.Port, "/-/ready")
	if target.ResourceURL != resourceURL {
		return fmt.Errorf("runtime target resource URL must be %q", resourceURL)
	}
	if target.ReadyURL != readyURL {
		return fmt.Errorf("runtime target readiness URL must be %q", readyURL)
	}
	return nil
}

// DiscoverDefaultRuntime derives the default App's loopback probe from allowlisted port assignments only.
func (discoverer *Discoverer) DiscoverDefaultRuntime(ctx context.Context, selectedPath string) (RuntimeTarget, error) {
	if discoverer == nil {
		panic("projectdiscovery.Discoverer.DiscoverDefaultRuntime requires a non-nil receiver")
	}
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return RuntimeTarget{}, err
	}
	root, err := canonicalProjectRoot(selectedPath)
	if err != nil {
		return RuntimeTarget{}, err
	}
	if err := validateProjectMarker(root); err != nil {
		return RuntimeTarget{}, err
	}

	port, err := discoverDefaultAppHTTPPort(root)
	if err != nil {
		return RuntimeTarget{}, err
	}
	target := RuntimeTarget{
		AppID:       "app",
		Name:        "App",
		Port:        port,
		ResourceURL: runtimeLocalURL(port, ""),
		ReadyURL:    runtimeLocalURL(port, "/-/ready"),
	}
	if err := target.Validate(); err != nil {
		return RuntimeTarget{}, fmt.Errorf("default App runtime target: %w", err)
	}
	return target, nil
}

// discoverDefaultAppHTTPPort gives the real project environment precedence while retaining generated example defaults.
func discoverDefaultAppHTTPPort(root string) (uint16, error) {
	for _, name := range []string{".env", ".env.example"} {
		values, err := readRuntimePortAssignments(filepath.Join(root, name))
		if err != nil {
			return 0, err
		}
		for _, key := range []string{"API_HTTP_PORT", "PORT"} {
			if value, found := values[key]; found {
				return parseRuntimePort(name, key, value)
			}
		}
	}
	return defaultAppHTTPPort, nil
}

// readRuntimePortAssignments parses only the two generated listener keys and discards every unrelated value immediately.
func readRuntimePortAssignments(filename string) (map[string]string, error) {
	values := make(map[string]string, 2)
	err := scanMetadataLines(filename, func(line string) (bool, error) {
		candidate := strings.TrimSpace(line)
		if strings.HasPrefix(candidate, "export ") {
			candidate = strings.TrimSpace(strings.TrimPrefix(candidate, "export "))
		}
		key, _, found := strings.Cut(candidate, "=")
		key = strings.TrimSpace(key)
		if !found || (key != "API_HTTP_PORT" && key != "PORT") {
			return false, nil
		}
		parsed, err := godotenv.Unmarshal(candidate)
		if err != nil {
			return false, invalidProjectError(fmt.Errorf("parse %s in %s: %w", key, filename, err))
		}
		values[key] = parsed[key]
		return false, nil
	})
	if err != nil {
		return nil, err
	}
	return values, nil
}

// parseRuntimePort keeps invalid explicit listener configuration visible instead of silently probing a different port.
func parseRuntimePort(filename string, key string, value string) (uint16, error) {
	normalized := strings.TrimSpace(value)
	port, err := strconv.ParseUint(normalized, 10, 16)
	if err != nil || port == 0 {
		return 0, invalidProjectError(fmt.Errorf("%s in %s must be an integer from 1 through 65535", key, filename))
	}
	return uint16(port), nil
}

// runtimeLocalURL uses an IP literal so public domains, proxies, and host DNS cannot produce a false readiness result.
func runtimeLocalURL(port uint16, path string) string {
	return (&url.URL{
		Scheme: "http",
		Host:   net.JoinHostPort("127.0.0.1", strconv.FormatUint(uint64(port), 10)),
		Path:   path,
	}).String()
}
