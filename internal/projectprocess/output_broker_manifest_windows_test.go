//go:build windows

package projectprocess

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

// TestSessionManifestRequiresProtectedNativePrivacy rejects foreign grants and aliases despite Windows's emulated mode bits.
func TestSessionManifestRequiresProtectedNativePrivacy(t *testing.T) {
	directory := canonicalTestPath(t, t.TempDir())
	endpoint, err := newOutputBrokerEndpointReference()
	if err != nil {
		t.Fatal(err)
	}
	config := OutputBrokerLaunchConfig{Version: OutputBrokerProtocolVersion, ProjectID: "project-private", SessionID: "session-private", OutputDirectory: directory, EndpointReference: endpoint, AttachmentTicket: "test-fixture-ticket"}
	for _, mutation := range []string{"unchanged", "foreign grant", "unprotected", "hardlink"} {
		t.Run(mutation, func(t *testing.T) {
			path, err := writeOutputBrokerLaunchConfig(directory, config)
			if err != nil {
				t.Fatal(err)
			}
			defer os.Remove(path)
			loaded, err := ReadOutputBrokerLaunchConfig(path)
			if err != nil || loaded != config {
				t.Fatalf("native private manifest did not round trip: %v", err)
			}
			user, err := windows.GetCurrentProcessToken().GetTokenUser()
			if err != nil {
				t.Fatal(err)
			}
			sid := user.User.Sid.String()
			switch mutation {
			case "unchanged":
				return
			case "foreign grant", "unprotected":
				sddl := "D:P(A;;FA;;;" + sid + ")(A;;GR;;;WD)"
				descriptor, err := windows.SecurityDescriptorFromString(sddl)
				if err != nil {
					t.Fatal(err)
				}
				dacl, _, err := descriptor.DACL()
				if err != nil {
					t.Fatal(err)
				}
				flags := windows.SECURITY_INFORMATION(windows.DACL_SECURITY_INFORMATION | windows.PROTECTED_DACL_SECURITY_INFORMATION)
				if mutation == "unprotected" {
					flags = windows.DACL_SECURITY_INFORMATION | windows.UNPROTECTED_DACL_SECURITY_INFORMATION
				}
				if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, flags, nil, nil, dacl, nil); err != nil {
					t.Fatal(err)
				}
			case "hardlink":
				alias := filepath.Join(directory, filepath.Base(path)+"-alias")
				if err := os.Link(path, alias); err != nil {
					t.Fatal(err)
				}
				defer os.Remove(alias)
			}
			if _, err := ReadOutputBrokerLaunchConfig(path); err == nil {
				t.Fatal("unsafe native manifest was accepted")
			}
		})
	}
}
