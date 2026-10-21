package projectprocess

import (
	"crypto/sha256"
	"encoding/hex"

	"github.com/goforj/harbor/internal/domain"
)

// sessionScopeName binds a native job name to the durable lifecycle and private runtime root rather than a mutable PID.
func sessionScopeName(projectID domain.ProjectID, sessionID domain.SessionID, directory string) string {
	digest := sha256.Sum256([]byte(string(projectID) + "\x00" + string(sessionID) + "\x00" + directory))
	return `Local\GoForj-Harbor-session-` + hex.EncodeToString(digest[:])
}
