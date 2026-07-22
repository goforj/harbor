package runtimepath

import (
	"os"

	"github.com/goforj/harbor/internal/platform/userpaths"
)

// environmentLookup keeps platform environment policy testable without mutating process-wide state.
type environmentLookup func(string) (string, bool)

// dataDirectoryLookup resolves the durable per-user fallback when an operating system has no runtime root.
type dataDirectoryLookup func() (string, error)

// temporaryDirectoryLookup resolves the operating system's preferred transient root.
type temporaryDirectoryLookup func() string

// Directory returns Harbor's platform-standard per-user runtime directory.
func Directory() (string, error) {
	return platformDirectory(os.LookupEnv, userpaths.DataDirectory, os.TempDir)
}

// OutputBrokerDirectory returns Harbor's per-user runtime directory for output-broker endpoints.
func OutputBrokerDirectory() (string, error) {
	return outputBrokerDirectory(os.TempDir)
}
