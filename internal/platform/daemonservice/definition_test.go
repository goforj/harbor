package daemonservice

import (
	"bytes"
	"testing"
)

// TestDarwinDefinitionUsesStandardScheduling keeps user-requested project builds out of background policy.
func TestDarwinDefinitionUsesStandardScheduling(t *testing.T) {
	if !bytes.Contains(DarwinDefinition(), []byte("<key>ProcessType</key>\n  <string>Standard</string>")) {
		t.Fatal("new LaunchAgent does not use Standard scheduling")
	}
}

// TestMatchesDarwinDefinitionRejectsDrift bounds legacy compatibility to the exact previously shipped identity.
func TestMatchesDarwinDefinitionRejectsDrift(t *testing.T) {
	legacy := bytes.Replace(DarwinDefinition(), []byte("<string>Standard</string>"), []byte("<string>Background</string>"), 1)
	for _, definition := range [][]byte{DarwinDefinition(), legacy} {
		if !MatchesDarwinDefinition(definition) {
			t.Fatal("owned definition rejected")
		}
		for _, drift := range [][]byte{
			append(append([]byte(nil), definition...), '\n'),
			bytes.Replace(definition, []byte(DarwinLauncher), []byte("/foreign/harbord"), 1),
			bytes.Replace(definition, []byte("<integer>5</integer>"), []byte("<integer>6</integer>"), 1),
			bytes.Replace(definition, []byte("<key>ProcessType</key>"), []byte("<key>ForeignPolicy</key>"), 1),
		} {
			if MatchesDarwinDefinition(drift) {
				t.Fatalf("drift admitted: %s", drift)
			}
		}
	}
	if MatchesDarwinDefinition(nil) || MatchesDarwinDefinition(darwinDefinition("Interactive")) {
		t.Fatal("unshipped definition admitted")
	}
}
