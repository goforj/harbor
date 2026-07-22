//go:build darwin

package lowport

import (
	"strings"
	"testing"
)

// TestDarwinServiceContractRejectsStaleLoadedConfiguration proves a matching
// plist path alone cannot authorize an old relay service for release.
func TestDarwinServiceContractRejectsStaleLoadedConfiguration(t *testing.T) {
	request := testRequest(t)
	username := "harbor-user"
	output := darwinServicePrint(request, username)
	if !matchesDarwinServiceContract([]byte(output), request, username) {
		t.Fatal("matchesDarwinServiceContract() rejected canonical service")
	}
	for _, stale := range []string{"--https-upstream", "HARBOR_INSTALLATION_ID", username, "127.0.0.1"} {
		t.Run(stale, func(t *testing.T) {
			if matchesDarwinServiceContract([]byte(strings.Replace(output, stale, "stale", 1)), request, username) {
				t.Fatalf("matchesDarwinServiceContract() accepted stale %q", stale)
			}
		})
	}
}

// TestDarwinServiceContractRejectsDuplicateAndMisplacedFacts rejects values that merely appear elsewhere in output.
func TestDarwinServiceContractRejectsDuplicateAndMisplacedFacts(t *testing.T) {
	request := testRequest(t)
	username := "harbor-user"
	for _, output := range []string{
		strings.Replace(darwinServicePrint(request, username), "--owner-uid\n501", "--owner-uid\n0\n501", 1),
		strings.Replace(darwinServicePrint(request, username), "SockServiceName = 80", "SockServiceName = 81\n\tcanonical = 80", 1),
		strings.Replace(darwinServicePrint(request, username), "HARBOR_INSTALLATION_ID = "+request.InstallationID(), "HARBOR_INSTALLATION_ID = foreign\n\tcanonical = "+request.InstallationID(), 1),
		strings.Replace(darwinServicePrint(request, username), "--https-upstream\n"+request.HTTPSUpstream().String(), "--https-upstream\n127.0.0.1:29999\n"+request.HTTPSUpstream().String(), 1),
	} {
		if matchesDarwinServiceContract([]byte(output), request, username) {
			t.Fatal("matchesDarwinServiceContract() accepted duplicate, misplaced, or extra fact")
		}
	}
}

// TestDarwinServiceContractRejectsDecoyAndExtraSocketBlocks keeps names bound to their parent tree.
func TestDarwinServiceContractRejectsDecoyAndExtraSocketBlocks(t *testing.T) {
	request := testRequest(t)
	username := "harbor-user"
	for _, output := range []string{
		strings.Replace(darwinServicePrint(request, username), "HTTP = {", "actualHTTP = {", 1),
		strings.Replace(darwinServicePrint(request, username), "sockets = {", "sockets = {\nEXTERNAL = {\nSockNodeName = 0.0.0.0\nSockServiceName = 8080\n}", 1),
		strings.Replace(darwinServicePrint(request, username), "arguments = {", "decoy = {\narguments = {", 1),
	} {
		if matchesDarwinServiceContract([]byte(output), request, username) {
			t.Fatal("matchesDarwinServiceContract() accepted a decoy or extra socket block")
		}
	}
}

// TestDarwinServiceContractRejectsPrivilegeScope rejects static launchd fields absent from Harbor's canonical plist.
func TestDarwinServiceContractRejectsPrivilegeScope(t *testing.T) {
	request := testRequest(t)
	for _, field := range []string{"GroupName = admin", "RootDirectory = /tmp/root", "WorkingDirectory = /tmp", "Umask = 0"} {
		output := strings.Replace(darwinServicePrint(request, "harbor-user"), "path = ", field+"\npath = ", 1)
		if matchesDarwinServiceContract([]byte(output), request, "harbor-user") {
			t.Fatalf("matchesDarwinServiceContract() accepted %q", field)
		}
	}
}

// darwinServicePrint returns the bounded strict launchctl grammar accepted by the parser.
func darwinServicePrint(request Request, username string) string {
	return strings.Join([]string{
		"system/" + darwinLabel + " = {",
		"path = " + darwinPlistPath,
		"program = /Library/PrivilegedHelperTools/com.goforj.harbor.launchdrelay",
		"username = " + username,
		"arguments = {", "/Library/PrivilegedHelperTools/com.goforj.harbor.launchdrelay", "--owner-uid", "501", "--policy-fingerprint", request.PolicyFingerprint(), "--http-upstream", request.HTTPUpstream().String(), "--https-upstream", request.HTTPSUpstream().String(), "}",
		"environment = {", "HARBOR_INSTALLATION_ID = " + request.InstallationID(), "}",
		// This fixture mirrors the reviewed launchctl print shape: the two TCP
		// fields are launchd-derived, while node and service remain plist authority.
		"sockets = {", "HTTP = {", "SockNodeName = 127.0.0.1", "SockServiceName = 80", "SockType = stream", "SockProtocol = tcp", "}", "HTTPS = {", "SockNodeName = 127.0.0.1", "SockServiceName = 443", "SockType = stream", "SockProtocol = tcp", "}", "}", "}",
	}, "\n")
}

// TestCanonicalServiceFingerprintIgnoresDynamicLaunchctlFacts keeps stable CAS evidence independent of PID churn.
func TestCanonicalServiceFingerprintIgnoresDynamicLaunchctlFacts(t *testing.T) {
	request := testRequest(t)
	if canonicalServiceFingerprint(request, "harbor-user") != canonicalServiceFingerprint(request, "harbor-user") {
		t.Fatal("canonicalServiceFingerprint() is unstable")
	}
}

// TestDarwinMutationVectorsAcceptOnlyFixedOperations prevents later callers from expanding the helper's launchctl authority.
func TestDarwinMutationVectorsAcceptOnlyFixedOperations(t *testing.T) {
	if !isDarwinMutationVector([]string{"bootstrap", "system", darwinPlistPath}) || !isDarwinMutationVector([]string{"bootout", "system/" + darwinLabel}) {
		t.Fatal("isDarwinMutationVector() rejected fixed vectors")
	}
	if isDarwinMutationVector([]string{"kickstart", "system/" + darwinLabel}) {
		t.Fatal("isDarwinMutationVector() accepted unreviewed vector")
	}
}
