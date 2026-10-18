package main

import (
	"os"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestDesktopReloadIncludesBothModules keeps approval and control changes inside the native rebuild boundary.
func TestDesktopReloadIncludesBothModules(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile("build/config.yml")
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Dev struct {
			Root     string `yaml:"root_path"`
			Executes []struct {
				Command   string `yaml:"cmd"`
				Directory string `yaml:"dir"`
			} `yaml:"executes"`
		} `yaml:"dev_mode"`
	}
	if err := yaml.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	if config.Dev.Root != ".." || len(config.Dev.Executes) != 3 {
		t.Fatal("Wails must watch root and desktop modules")
	}
	for _, step := range config.Dev.Executes {
		if step.Directory != "desktop" {
			t.Fatalf("native command %q runs outside desktop", step.Command)
		}
	}
}
