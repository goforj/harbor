package projectdiscovery

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const managedRuntimeContractTestSource = `package http

type RuntimeConfig struct {
	Host string
	Port string
}

type Runtime struct {
	config RuntimeConfig
}

func (runtime *Runtime) RunWithConfig(cfg RuntimeConfig) error {
	cfg = inheritRuntimeConfig(cfg, runtime.config)
	_ = cfg
	return nil
}

func inheritRuntimeConfig(cfg RuntimeConfig, defaults RuntimeConfig) RuntimeConfig {
	if strings.TrimSpace(cfg.Host) == "" {
		cfg.Host = defaults.Host
	}
	if strings.TrimSpace(cfg.Port) == "" {
		cfg.Port = defaults.Port
	}
	return normalizeRuntimeConfig(cfg)
}

func normalizeRuntimeConfig(cfg RuntimeConfig) RuntimeConfig {
	return cfg
}
`

// TestValidateManagedHTTPRuntimeContractAcceptsCurrentGeneratedBehavior proves formatting and receiver names are not provenance signals.
func TestValidateManagedHTTPRuntimeContractAcceptsCurrentGeneratedBehavior(t *testing.T) {
	tests := map[string]string{
		"current generator": managedRuntimeContractTestSource,
		"equivalent parameter names": strings.NewReplacer(
			"func inheritRuntimeConfig(cfg RuntimeConfig, defaults RuntimeConfig)", "func inheritRuntimeConfig(candidate RuntimeConfig, inherited RuntimeConfig)",
			"strings.TrimSpace(cfg.Host)", "strings.TrimSpace(candidate.Host)",
			"cfg.Host = defaults.Host", "candidate.Host = inherited.Host",
			"strings.TrimSpace(cfg.Port)", "strings.TrimSpace(candidate.Port)",
			"cfg.Port = defaults.Port", "candidate.Port = inherited.Port",
			"return normalizeRuntimeConfig(cfg)", "return normalizeRuntimeConfig(candidate)",
		).Replace(managedRuntimeContractTestSource),
		"reversed empty comparison": strings.Replace(
			managedRuntimeContractTestSource,
			"strings.TrimSpace(cfg.Host) == \"\"",
			"\"\" == strings.TrimSpace(cfg.Host)",
			1,
		),
	}
	for name, source := range tests {
		t.Run(name, func(t *testing.T) {
			root := managedRuntimeContractTestProject(t, source)
			if err := validateManagedHTTPRuntimeContract(root); err != nil {
				t.Fatalf("validateManagedHTTPRuntimeContract() error = %v", err)
			}
		})
	}
}

// TestValidateManagedHTTPRuntimeContractRequiresListenerInheritance rejects every known false-positive shape around the old generator bug.
func TestValidateManagedHTTPRuntimeContractRequiresListenerInheritance(t *testing.T) {
	tests := []struct {
		name   string
		source string
	}{
		{name: "missing source"},
		{name: "old sparse normalization", source: strings.Replace(
			managedRuntimeContractTestSource,
			"cfg = inheritRuntimeConfig(cfg, runtime.config)",
			"cfg = normalizeRuntimeConfig(cfg)",
			1,
		)},
		{name: "wrong runtime defaults", source: strings.Replace(
			managedRuntimeContractTestSource,
			"runtime.config)",
			"RuntimeConfig{})",
			1,
		)},
		{name: "wrong method receiver", source: strings.Replace(
			managedRuntimeContractTestSource,
			"func (runtime *Runtime) RunWithConfig",
			"func (runtime Runtime) RunWithConfig",
			1,
		)},
		{name: "unnamed runtime config", source: strings.Replace(
			managedRuntimeContractTestSource,
			"func (runtime *Runtime) RunWithConfig(cfg RuntimeConfig)",
			"func (runtime *Runtime) RunWithConfig(RuntimeConfig)",
			1,
		)},
		{name: "helper receiver", source: strings.Replace(
			managedRuntimeContractTestSource,
			"func inheritRuntimeConfig",
			"func (runtime *Runtime) inheritRuntimeConfig",
			1,
		)},
		{name: "helper parameter type", source: strings.Replace(
			managedRuntimeContractTestSource,
			"defaults RuntimeConfig",
			"defaults string",
			1,
		)},
		{name: "unconditional host default", source: strings.Replace(
			managedRuntimeContractTestSource,
			"\tif strings.TrimSpace(cfg.Host) == \"\" {\n\t\tcfg.Host = defaults.Host\n\t}\n",
			"\tcfg.Host = defaults.Host\n",
			1,
		)},
		{name: "nonempty host guard", source: strings.Replace(
			managedRuntimeContractTestSource,
			"strings.TrimSpace(cfg.Host) == \"\"",
			"strings.TrimSpace(cfg.Host) == \"localhost\"",
			1,
		)},
		{name: "wrong host guard operator", source: strings.Replace(
			managedRuntimeContractTestSource,
			"strings.TrimSpace(cfg.Host) == \"\"",
			"strings.TrimSpace(cfg.Host) != \"\"",
			1,
		)},
		{name: "guarded host assignment missing", source: strings.Replace(
			managedRuntimeContractTestSource,
			"cfg.Host = defaults.Host",
			"_ = defaults.Host",
			1,
		)},
		{name: "host default missing", source: strings.Replace(
			managedRuntimeContractTestSource,
			"\tif strings.TrimSpace(cfg.Host) == \"\" {\n\t\tcfg.Host = defaults.Host\n\t}\n",
			"",
			1,
		)},
		{name: "port default missing", source: strings.Replace(
			managedRuntimeContractTestSource,
			"\tif strings.TrimSpace(cfg.Port) == \"\" {\n\t\tcfg.Port = defaults.Port\n\t}\n",
			"",
			1,
		)},
		{name: "normalization missing", source: strings.Replace(
			managedRuntimeContractTestSource,
			"return normalizeRuntimeConfig(cfg)",
			"return cfg",
			1,
		)},
		{name: "invalid Go", source: "package http\nfunc ("},
		{name: "runtime path is directory", source: managedRuntimeContractTestSource},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := managedRuntimeContractTestProject(t, test.source)
			if test.name == "runtime path is directory" {
				filename := filepath.Join(root, filepath.FromSlash(managedHTTPRuntimePath))
				if err := os.Remove(filename); err != nil {
					t.Fatalf("remove generated runtime fixture: %v", err)
				}
				if err := os.Mkdir(filename, 0o700); err != nil {
					t.Fatalf("replace generated runtime with directory: %v", err)
				}
			}
			err := validateManagedHTTPRuntimeContract(root)
			var invalid *InvalidProjectError
			if !errors.As(err, &invalid) {
				t.Fatalf("validateManagedHTTPRuntimeContract() error = %T / %v, want InvalidProjectError", err, err)
			}
			if test.name != "invalid Go" && test.name != "runtime path is directory" {
				var updateRequired *RenderUpdateRequiredError
				if !errors.As(err, &updateRequired) {
					t.Fatalf("validateManagedHTTPRuntimeContract() error = %T / %v, want RenderUpdateRequiredError", err, err)
				}
			}
		})
	}
}

// TestRenderUpdateRequiredErrorKeepsGuidanceStable protects the durable problem's actionable correction.
func TestRenderUpdateRequiredErrorKeepsGuidanceStable(t *testing.T) {
	if message := (&RenderUpdateRequiredError{}).Error(); !strings.Contains(message, "run forj render") {
		t.Fatalf("RenderUpdateRequiredError.Error() = %q", message)
	}
}

// managedRuntimeContractTestProject creates only the canonical generated-source location under one project root.
func managedRuntimeContractTestProject(t *testing.T, source string) string {
	t.Helper()
	root := t.TempDir()
	if source == "" {
		return root
	}
	directory := filepath.Join(root, "internal", "http")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatalf("create generated HTTP runtime directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(directory, "runtime.go"), []byte(source), 0o600); err != nil {
		t.Fatalf("write generated HTTP runtime: %v", err)
	}
	return root
}
