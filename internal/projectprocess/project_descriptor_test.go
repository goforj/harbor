package projectprocess

import (
	"strings"
	"testing"
)

// TestAcceptedGoForjExecutableRejectsRelativeDescriptorBinding prevents a preflight from selecting a PATH-relative replacement.
func TestAcceptedGoForjExecutableRejectsRelativeDescriptorBinding(t *testing.T) {
	supervisor := NewWithExecutableVerifier(Options{}, func(string) error {
		t.Fatal("relative descriptor binding reached executable verification")
		return nil
	})
	_, err := supervisor.acceptedGoForjExecutable("forj")
	if err == nil || !strings.Contains(err.Error(), "absolute path") {
		t.Fatalf("acceptedGoForjExecutable() error = %v, want absolute-path rejection", err)
	}
}
