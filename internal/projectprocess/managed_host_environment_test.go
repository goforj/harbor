package projectprocess

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestPrepareManagedHostEnvironmentLeavesUnmarkedFileUntouched verifies normal launches never rewrite project-owned dotenv files.
func TestPrepareManagedHostEnvironmentLeavesUnmarkedFileUntouched(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, ".env.host")
	original := []byte("DB_HOST=127.0.0.1\r\nURL=http://localhost:8080/path")
	if err := os.WriteFile(path, original, 0o640); err != nil {
		t.Fatalf("write .env.host: %v", err)
	}
	values, err := prepareManagedHostEnvironment(root, EnvironmentOverrides{
		"IP_ADDRESS": "127.77.0.11",
	})
	if err != nil {
		t.Fatalf("prepareManagedHostEnvironment() error = %v", err)
	}
	actual, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(actual, original) {
		t.Fatalf(".env.host = %q, %v; want %q", actual, err, original)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat .env.host: %v", err)
		}
		if info.Mode().Perm() != 0o640 {
			t.Fatalf(".env.host mode = %v, want 0640", info.Mode())
		}
	}
	if values["DB_HOST"] != "127.77.0.11" || values["URL"] != "http://127.77.0.11:8080/path" {
		t.Fatalf("derived values = %#v", values)
	}
}

// TestCompileBuildEnvironmentOverridesLimitsChildBuildInputs verifies builds receive only assigned endpoint hosts.
func TestCompileBuildEnvironmentOverridesLimitsChildBuildInputs(t *testing.T) {
	values := EnvironmentOverrides{
		"API_HTTP_HOST":          "127.77.0.42",
		"ARBITRARY":              "127.77.0.42",
		"DATABASE_URL":           "mysql://127.77.0.42:3306/app",
		"DB_HOST":                "127.77.0.42",
		"DEV_SERVICE_IP_ADDRESS": "127.77.0.42",
		"IP_ADDRESS":             "127.77.0.42",
		"LIGHTHOUSE_URL":         "wss://127.77.0.42:03000/lighthouse/ws/agent",
		"MAIL_SMTP_HOST":         "127.77.0.42",
		"REDIS_DSN":              "redis://127.77.0.42:6379/0",
		"SECRET_TOKEN":           "127.77.0.42",
		"UNRELATED_HOST":         "127.77.0.99",
	}
	want := "API_HTTP_HOST=127.77.0.42,DB_HOST=127.77.0.42,DEV_SERVICE_IP_ADDRESS=127.77.0.42,IP_ADDRESS=127.77.0.42,LIGHTHOUSE_URL=wss://127.77.0.42:3000/lighthouse/ws/agent,MAIL_SMTP_HOST=127.77.0.42"
	if got := compileBuildEnvironmentOverrides(values); got != want {
		t.Fatalf("compileBuildEnvironmentOverrides() = %q, want %q", got, want)
	}
}

// TestCompileBuildEnvironmentOverridesPreservesCaseInsensitiveEndpointKeys verifies Windows-compatible names retain their project spelling.
func TestCompileBuildEnvironmentOverridesPreservesCaseInsensitiveEndpointKeys(t *testing.T) {
	values := EnvironmentOverrides{
		"api_http_host":          "127.77.0.42",
		"db_host":                "127.77.0.42",
		"dev_service_ip_address": "127.77.0.42",
		"ip_address":             "127.77.0.42",
		"lighthouse_url":         "ws://127.77.0.42:3000/lighthouse/ws/agent",
	}
	want := "api_http_host=127.77.0.42,db_host=127.77.0.42,dev_service_ip_address=127.77.0.42,ip_address=127.77.0.42,lighthouse_url=ws://127.77.0.42:3000/lighthouse/ws/agent"
	if got := compileBuildEnvironmentOverrides(values); got != want {
		t.Fatalf("compileBuildEnvironmentOverrides() = %q, want %q", got, want)
	}
}

// TestCompileBuildEnvironmentOverridesRejectsUnsafeLighthouseURLs verifies project values cannot alter the Harbor agent endpoint.
func TestCompileBuildEnvironmentOverridesRejectsUnsafeLighthouseURLs(t *testing.T) {
	const assignedAddress = "127.77.0.42"
	tests := []struct {
		name  string
		value string
	}{
		{
			name:  "credentials",
			value: "ws://token@127.77.0.42:3000/lighthouse/ws/agent",
		},
		{
			name:  "other host",
			value: "ws://127.77.0.43:3000/lighthouse/ws/agent",
		},
		{
			name:  "other path",
			value: "ws://127.77.0.42:3000/lighthouse/ws/other",
		},
		{
			name:  "query",
			value: "ws://127.77.0.42:3000/lighthouse/ws/agent?token=value",
		},
		{
			name:  "fragment",
			value: "ws://127.77.0.42:3000/lighthouse/ws/agent#fragment",
		},
		{
			name:  "invalid port",
			value: "ws://127.77.0.42:65536/lighthouse/ws/agent",
		},
		{
			name:  "comma equals",
			value: "ws://127.77.0.42:3000/lighthouse/ws/agent,token=value",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			values := EnvironmentOverrides{
				"IP_ADDRESS":     assignedAddress,
				"LIGHTHOUSE_URL": test.value,
			}
			if got := compileBuildEnvironmentOverrides(values); got != "IP_ADDRESS="+assignedAddress {
				t.Fatalf("compileBuildEnvironmentOverrides() = %q, want only IP_ADDRESS", got)
			}
		})
	}
}

// TestPrepareManagedHostEnvironmentExcludesReservedBuildOverrideNames verifies dotenv cannot override Harbor's build contract.
func TestPrepareManagedHostEnvironmentExcludesReservedBuildOverrideNames(t *testing.T) {
	for _, name := range []string{buildEnvironmentOverridesEnvName, "FoRj_BuIlD_EnV_OvErRiDeS"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, ".env.host")
			if err := os.WriteFile(path, []byte(name+"=localhost\n"), 0o600); err != nil {
				t.Fatalf("write .env.host: %v", err)
			}
			values, err := prepareManagedHostEnvironment(root, EnvironmentOverrides{
				"IP_ADDRESS": "127.77.0.42",
			})
			if err != nil {
				t.Fatalf("prepareManagedHostEnvironment() error = %v", err)
			}
			if _, present := values[name]; present {
				t.Fatalf("managed values contain reserved %q: %#v", name, values)
			}
		})
	}
}

// TestPrepareManagedHostEnvironmentRemovesExactLegacyBlock verifies migration preserves surrounding bytes and file mode.
func TestPrepareManagedHostEnvironmentRemovesExactLegacyBlock(t *testing.T) {
	for _, test := range []struct {
		name     string
		contents string
		want     string
	}{
		{
			name:     "LF",
			contents: "USER=kept\n" + managedHostEnvironmentBegin + "\nIP_ADDRESS=127.0.0.1\n" + managedHostEnvironmentEnd + "\nNEXT=kept\n",
			want:     "USER=kept\nNEXT=kept\n",
		},
		{
			name:     "CRLF",
			contents: "USER=kept\r\n" + managedHostEnvironmentBegin + "\r\nIP_ADDRESS=127.0.0.1\r\n" + managedHostEnvironmentEnd + "\r\nNEXT=kept\r\n",
			want:     "USER=kept\r\nNEXT=kept\r\n",
		},
		{
			name:     "restore no final newline",
			contents: "USER=kept\r\n" + managedHostEnvironmentBegin + "\r\n" + managedHostEnvironmentRestoreFinalNewline + "\r\nIP_ADDRESS=127.0.0.1\r\n" + managedHostEnvironmentEnd + "\r\n",
			want:     "USER=kept",
		},
		{
			name:     "block only",
			contents: managedHostEnvironmentBegin + "\nIP_ADDRESS=127.0.0.1\n" + managedHostEnvironmentEnd + "\n",
			want:     "",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, ".env.host")
			if err := os.WriteFile(path, []byte(test.contents), 0o640); err != nil {
				t.Fatalf("write .env.host: %v", err)
			}
			if _, err := prepareManagedHostEnvironment(root, EnvironmentOverrides{
				"IP_ADDRESS": "127.77.0.12",
			}); err != nil {
				t.Fatalf("prepareManagedHostEnvironment() error = %v", err)
			}
			actual, err := os.ReadFile(path)
			if test.want == "" {
				if !os.IsNotExist(err) {
					t.Fatalf("read removed .env.host = %q, %v; want not exist", actual, err)
				}
				return
			}
			if err != nil || string(actual) != test.want {
				t.Fatalf(".env.host = %q, %v; want %q", actual, err, test.want)
			}
			if runtime.GOOS != "windows" {
				info, err := os.Stat(path)
				if err != nil {
					t.Fatalf("stat .env.host: %v", err)
				}
				if info.Mode().Perm() != 0o640 {
					t.Fatalf(".env.host mode = %v, want 0640", info.Mode())
				}
			}
		})
	}
}

// TestRemoveManagedHostEnvironmentFileSyncsParentDirectory verifies block-only migration persists the deletion boundary.
func TestRemoveManagedHostEnvironmentFileSyncsParentDirectory(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, ".env.host")
	if err := os.WriteFile(path, []byte("legacy"), 0o600); err != nil {
		t.Fatalf("write .env.host: %v", err)
	}
	_, _, identity, exists, err := readManagedHostEnvironment(path)
	if err != nil || !exists {
		t.Fatalf("readManagedHostEnvironment() = _, _, %v, %t, %v", identity, exists, err)
	}
	originalSync := syncManagedHostEnvironmentParentDirectory
	syncCalls := 0
	syncManagedHostEnvironmentParentDirectory = func(directory string) error {
		if directory != root {
			t.Fatalf("sync directory = %q, want %q", directory, root)
		}
		syncCalls++
		return nil
	}
	t.Cleanup(func() {
		syncManagedHostEnvironmentParentDirectory = originalSync
	})
	if err := removeManagedHostEnvironmentFile(path, identity); err != nil {
		t.Fatalf("removeManagedHostEnvironmentFile() error = %v", err)
	}
	if syncCalls != 1 {
		t.Fatalf("directory sync calls = %d, want 1", syncCalls)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf(".env.host after removal = %v, want not exist", err)
	}
}

// TestPrepareManagedHostEnvironmentIgnoresMalformedDotenv verifies a malformed user file does not block child overrides.
func TestPrepareManagedHostEnvironmentIgnoresMalformedDotenv(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, ".env.host")
	contents := []byte("MALFORMED=\"unterminated\nDB_HOST=127.0.0.1\n")
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatalf("write .env.host: %v", err)
	}
	values, err := prepareManagedHostEnvironment(root, EnvironmentOverrides{
		"IP_ADDRESS": "127.77.0.14",
	})
	if err != nil {
		t.Fatalf("prepareManagedHostEnvironment() error = %v", err)
	}
	if values["IP_ADDRESS"] != "127.77.0.14" || values["API_HTTP_HOST"] != "127.77.0.14" {
		t.Fatalf("derived values = %#v", values)
	}
	actual, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(actual, contents) {
		t.Fatalf(".env.host = %q, %v; want unchanged", actual, err)
	}
}

// TestPrepareManagedHostEnvironmentLeavesUncertainMarkers verifies user marker-like content neither changes nor prevents launch preparation.
func TestPrepareManagedHostEnvironmentLeavesUncertainMarkers(t *testing.T) {
	for _, contents := range []string{
		managedHostEnvironmentBegin + " \nIP_ADDRESS=127.0.0.1\n" + managedHostEnvironmentEnd + "\n",
		managedHostEnvironmentBegin + "\nIP_ADDRESS=127.0.0.1\n",
		managedHostEnvironmentBegin + "\n" + managedHostEnvironmentEnd + "\n" + managedHostEnvironmentBegin + "\n" + managedHostEnvironmentEnd + "\n",
	} {
		root := t.TempDir()
		path := filepath.Join(root, ".env.host")
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatalf("write .env.host: %v", err)
		}
		if _, err := prepareManagedHostEnvironment(root, EnvironmentOverrides{
			"IP_ADDRESS": "127.77.0.13",
		}); err != nil {
			t.Fatalf("prepareManagedHostEnvironment() error = %v", err)
		}
		actual, err := os.ReadFile(path)
		if err != nil || string(actual) != contents {
			t.Fatalf(".env.host = %q, %v; want unchanged", actual, err)
		}
	}
}

// TestPrepareManagedHostEnvironmentRejectsSymlinks verifies legacy migration cannot follow a project-controlled link.
func TestPrepareManagedHostEnvironmentRejectsSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows symlink creation requires additional privileges")
	}
	root := t.TempDir()
	target := filepath.Join(t.TempDir(), "host.env")
	contents := []byte(managedHostEnvironmentBegin + "\nIP_ADDRESS=127.0.0.1\n" + managedHostEnvironmentEnd + "\n")
	if err := os.WriteFile(target, contents, 0o600); err != nil {
		t.Fatalf("write target: %v", err)
	}
	if err := os.Symlink(target, filepath.Join(root, ".env.host")); err != nil {
		t.Fatalf("create .env.host symlink: %v", err)
	}
	if _, err := prepareManagedHostEnvironment(root, EnvironmentOverrides{
		"IP_ADDRESS": "127.77.0.14",
	}); err == nil {
		t.Fatal("prepareManagedHostEnvironment() error = nil")
	}
	actual, err := os.ReadFile(target)
	if err != nil || !bytes.Equal(actual, contents) {
		t.Fatalf("target = %q, %v; want unchanged", actual, err)
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
		{
			name:    "bare host",
			value:   "localhost",
			want:    "127.77.0.40",
			changed: true,
		},
		{
			name:    "socket",
			value:   "127.0.0.1:6379",
			want:    "127.77.0.40:6379",
			changed: true,
		},
		{
			name:    "IPv6 socket",
			value:   "[::1]:3306",
			want:    "127.77.0.40:3306",
			changed: true,
		},
		{
			name:    "URL",
			value:   "http://localhost:9000/bucket?q=1#part",
			want:    "http://127.77.0.40:9000/bucket?q=1#part",
			changed: true,
		},
		{
			name:  "external URL",
			value: "https://example.com",
			want:  "https://example.com",
		},
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
