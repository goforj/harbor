package projectprocess

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/joho/godotenv"
)

// TestWriteManagedHostEnvironmentLayersLocalEndpoints verifies the managed block and child environment share one address.
func TestWriteManagedHostEnvironmentLayersLocalEndpoints(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, ".env.host")
	original := strings.Join([]string{
		"# Project-owned host values stay readable above Harbor's block.",
		"DB_HOST=127.0.0.1",
		"DB_PORT=3306",
		"REDIS_HOST=localhost",
		"MAIL_SMTP_HOST=0.0.0.0",
		"RAPIDOCR_URL=http://127.0.0.1:9003/ocr",
		"EXTERNAL_URL=https://example.com/service",
		"",
	}, "\n")
	if err := os.WriteFile(path, []byte(original), 0o640); err != nil {
		t.Fatalf("write .env.host: %v", err)
	}

	values, err := writeManagedHostEnvironment(root, EnvironmentOverrides{
		"API_HTTP_HOST":          "127.77.0.11",
		"DEV_SERVICE_IP_ADDRESS": "127.77.0.11",
		"IP_ADDRESS":             "127.77.0.11",
		"LIGHTHOUSE_URL":         "ws://127.77.0.11:3000/lighthouse/ws/agent",
		"PROJECT_SECRET":         "must-not-be-persisted",
	})
	if err != nil {
		t.Fatalf("writeManagedHostEnvironment() error = %v", err)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read .env.host: %v", err)
	}
	text := string(contents)
	if !strings.HasPrefix(text, original) {
		t.Fatalf("project-owned .env.host content changed:\n%s", text)
	}
	if strings.Count(text, managedHostEnvironmentBegin) != 1 || strings.Count(text, managedHostEnvironmentEnd) != 1 {
		t.Fatalf("managed marker counts are not singular:\n%s", text)
	}
	if strings.Contains(text, "PROJECT_SECRET") || values["PROJECT_SECRET"] != "" {
		t.Fatalf("arbitrary launch override was persisted: values=%#v\n%s", values, text)
	}

	parsed, err := godotenv.Unmarshal(text)
	if err != nil {
		t.Fatalf("parse resulting .env.host: %v", err)
	}
	want := map[string]string{
		"API_HTTP_HOST":          "127.77.0.11",
		"DB_HOST":                "127.77.0.11",
		"DEV_SERVICE_IP_ADDRESS": "127.77.0.11",
		"IP_ADDRESS":             "127.77.0.11",
		"LIGHTHOUSE_URL":         "ws://127.77.0.11:3000/lighthouse/ws/agent",
		"MAIL_SMTP_HOST":         "127.77.0.11",
		"RAPIDOCR_URL":           "http://127.77.0.11:9003/ocr",
		"REDIS_HOST":             "127.77.0.11",
	}
	for name, expected := range want {
		if values[name] != expected || parsed[name] != expected {
			t.Fatalf("%s values = returned %q, parsed %q; want %q", name, values[name], parsed[name], expected)
		}
	}
	if parsed["DB_PORT"] != "3306" || parsed["EXTERNAL_URL"] != "https://example.com/service" {
		t.Fatalf("unrelated values changed: %#v", parsed)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat .env.host: %v", err)
		}
		if info.Mode().Perm() != 0o640 {
			t.Fatalf(".env.host mode = %o, want 640", info.Mode().Perm())
		}
	}

	before := append([]byte(nil), contents...)
	if _, err := writeManagedHostEnvironment(root, EnvironmentOverrides{
		"API_HTTP_HOST":          "127.77.0.11",
		"DEV_SERVICE_IP_ADDRESS": "127.77.0.11",
		"IP_ADDRESS":             "127.77.0.11",
		"LIGHTHOUSE_URL":         "ws://127.77.0.11:3000/lighthouse/ws/agent",
	}); err != nil {
		t.Fatalf("repeat writeManagedHostEnvironment() error = %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read repeated .env.host: %v", err)
	}
	if string(after) != string(before) {
		t.Fatalf("identical managed update changed bytes:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

// TestWriteManagedHostEnvironmentMovesPriorBlockToEOF ensures assignments below an old block cannot retain precedence.
func TestWriteManagedHostEnvironmentMovesPriorBlockToEOF(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, ".env.host")
	contents := strings.Join([]string{
		managedHostEnvironmentBegin,
		`IP_ADDRESS="127.77.0.8"`,
		`STALE_VALUE="remove-me"`,
		managedHostEnvironmentEnd,
		"DB_HOST=localhost",
		"USER_SETTING=preserved",
		"",
	}, "\r\n")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write prior .env.host: %v", err)
	}

	if _, err := writeManagedHostEnvironment(root, EnvironmentOverrides{
		"IP_ADDRESS":             "127.77.0.16",
		"DEV_SERVICE_IP_ADDRESS": "127.77.0.16",
	}); err != nil {
		t.Fatalf("writeManagedHostEnvironment() error = %v", err)
	}
	updated, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read updated .env.host: %v", err)
	}
	text := string(updated)
	if strings.Contains(text, "STALE_VALUE") || strings.Contains(text, "127.77.0.8") {
		t.Fatalf("prior managed values remain:\n%s", text)
	}
	if strings.Index(text, "USER_SETTING=preserved") > strings.Index(text, managedHostEnvironmentBegin) {
		t.Fatalf("managed block was not moved to EOF:\n%s", text)
	}
	if strings.Contains(strings.ReplaceAll(text, "\r\n", ""), "\n") {
		t.Fatalf("managed update mixed newline styles: %q", text)
	}
}

// TestWriteManagedHostEnvironmentRejectsMalformedMarkers preserves user files when Harbor cannot identify its ownership boundary.
func TestWriteManagedHostEnvironmentRejectsMalformedMarkers(t *testing.T) {
	tests := map[string]string{
		"missing end":      managedHostEnvironmentBegin + "\nIP_ADDRESS=127.0.0.1\n",
		"end before begin": managedHostEnvironmentEnd + "\n" + managedHostEnvironmentBegin + "\n",
		"duplicate begin":  managedHostEnvironmentBegin + "\n" + managedHostEnvironmentBegin + "\n" + managedHostEnvironmentEnd + "\n",
	}
	for name, contents := range tests {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, ".env.host")
			if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
				t.Fatalf("write malformed .env.host: %v", err)
			}
			_, err := writeManagedHostEnvironment(root, EnvironmentOverrides{"IP_ADDRESS": "127.77.0.24"})
			if err == nil || !strings.Contains(err.Error(), "one ordered begin/end pair") {
				t.Fatalf("writeManagedHostEnvironment() error = %v", err)
			}
			unchanged, readErr := os.ReadFile(path)
			if readErr != nil {
				t.Fatalf("read malformed .env.host: %v", readErr)
			}
			if string(unchanged) != contents {
				t.Fatalf("malformed .env.host changed:\n%s", unchanged)
			}
		})
	}
}

// TestWriteManagedHostEnvironmentCreatesPrivateFile verifies an absent host layer is safe and immediately usable.
func TestWriteManagedHostEnvironmentCreatesPrivateFile(t *testing.T) {
	root := t.TempDir()
	values, err := writeManagedHostEnvironment(root, EnvironmentOverrides{"IP_ADDRESS": "127.77.0.32"})
	if err != nil {
		t.Fatalf("writeManagedHostEnvironment() error = %v", err)
	}
	if values["IP_ADDRESS"] != "127.77.0.32" || values["API_HTTP_HOST"] != "127.77.0.32" {
		t.Fatalf("managed values = %#v", values)
	}
	info, err := os.Stat(filepath.Join(root, ".env.host"))
	if err != nil {
		t.Fatalf("stat created .env.host: %v", err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("created .env.host mode = %o, want 600", info.Mode().Perm())
	}
}

// TestRemoveManagedHostEnvironmentPreservesProjectContent verifies a settled shutdown removes only Harbor's owned final block.
func TestRemoveManagedHostEnvironmentPreservesProjectContent(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, ".env.host")
	original := "USER_SETTING=preserved\n"
	if err := os.WriteFile(path, []byte(original), 0o640); err != nil {
		t.Fatalf("write project environment: %v", err)
	}
	if _, err := writeManagedHostEnvironment(root, EnvironmentOverrides{"IP_ADDRESS": "127.77.0.32"}); err != nil {
		t.Fatalf("write managed environment: %v", err)
	}
	if err := removeManagedHostEnvironment(root); err != nil {
		t.Fatalf("remove managed environment: %v", err)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read project environment: %v", err)
	}
	if string(contents) != original+"\n" {
		t.Fatalf("contents = %q, want preserved project assignment plus separator", contents)
	}
}

// TestRemoveManagedHostEnvironmentDeletesHarborOnlyFile verifies Harbor removes an otherwise empty tactical bridge.
func TestRemoveManagedHostEnvironmentDeletesHarborOnlyFile(t *testing.T) {
	root := t.TempDir()
	if _, err := writeManagedHostEnvironment(root, EnvironmentOverrides{"IP_ADDRESS": "127.77.0.32"}); err != nil {
		t.Fatalf("write managed environment: %v", err)
	}
	if err := removeManagedHostEnvironment(root); err != nil {
		t.Fatalf("remove managed environment: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(root, ".env.host")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("managed-only file stat error = %v, want not exist", err)
	}
}

// TestRemoveManagedHostEnvironmentPreservesMalformedFiles verifies uncertain marker ownership never alters project content.
func TestRemoveManagedHostEnvironmentPreservesMalformedFiles(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, ".env.host")
	contents := managedHostEnvironmentBegin + "\nIP_ADDRESS=127.77.0.32\n"
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write malformed environment: %v", err)
	}
	if err := removeManagedHostEnvironment(root); err == nil || !strings.Contains(err.Error(), "one ordered begin/end pair") {
		t.Fatalf("remove malformed environment error = %v", err)
	}
	unchanged, err := os.ReadFile(path)
	if err != nil || string(unchanged) != contents {
		t.Fatalf("malformed environment changed: %q, %v", unchanged, err)
	}
}

// TestRewriteLocalEnvironmentValueLimitsMutation covers the literal endpoint shapes Harbor may safely relocate.
func TestRewriteLocalEnvironmentValueLimitsMutation(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		want    string
		changed bool
	}{
		{name: "host", value: "localhost", want: "127.77.0.40", changed: true},
		{name: "wildcard", value: "0.0.0.0", want: "127.77.0.40", changed: true},
		{name: "socket", value: "127.0.0.1:6379", want: "127.77.0.40:6379", changed: true},
		{name: "IPv6 socket", value: "[::1]:3306", want: "127.77.0.40:3306", changed: true},
		{name: "URL", value: "http://localhost:9000/bucket?q=1#part", want: "http://127.77.0.40:9000/bucket?q=1#part", changed: true},
		{name: "URL credentials", value: "mysql://user:pass@127.0.0.1:3306/app", want: "mysql://user:pass@127.77.0.40:3306/app", changed: true},
		{name: "external URL", value: "https://example.com", want: "https://example.com"},
		{name: "other loopback", value: "127.0.0.2", want: "127.0.0.2"},
		{name: "embedded localhost", value: "prefix-localhost", want: "prefix-localhost"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, changed := rewriteLocalEnvironmentValue(test.value, "127.77.0.40")
			if got != test.want || changed != test.changed {
				t.Fatalf("rewriteLocalEnvironmentValue(%q) = %q, %t; want %q, %t", test.value, got, changed, test.want, test.changed)
			}
		})
	}
}
