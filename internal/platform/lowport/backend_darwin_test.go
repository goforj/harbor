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
		strings.Replace(darwinServicePrint(request, username), "service name = 80", "service name = 81\n\tcanonical = 80", 1),
		strings.Replace(darwinServicePrint(request, username), "HARBOR_INSTALLATION_ID => "+request.InstallationID(), "HARBOR_INSTALLATION_ID => foreign\n\tcanonical => "+request.InstallationID(), 1),
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
		strings.Replace(darwinServicePrint(request, username), "\"HTTP\" = {", "\"actualHTTP\" = {", 1),
		strings.Replace(darwinServicePrint(request, username), "\"HTTP\" = {", "\"EXTERNAL\" = {\n\ttype = stream\n\tnode name = 0.0.0.0\n\tservice name = 8080\n}\n\"HTTP\" = {", 1),
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
	for _, field := range []string{
		"GroupName = admin",
		"group name = admin",
		"InitGroups = true",
		"RootDirectory = /tmp/root",
		"WorkingDirectory = /tmp",
		"Umask = 0",
		"StandardErrorPath = /tmp/relay.log",
		"MachServices = enabled",
		"KeepAlive = true",
		"StartInterval = 10",
		"StartCalendarInterval = enabled",
		"ThrottleInterval = 1",
		"WatchPaths = enabled",
		"QueueDirectories = enabled",
		"StartOnMount = true",
		"ProcessType = Interactive",
		"Nice = -10",
		"SoftResourceLimits = enabled",
		"HardResourceLimits = enabled",
		"AbandonProcessGroup = true",
		"SessionCreate = true",
		"LimitLoadToHosts = enabled",
		"LimitLoadFromHosts = enabled",
		"LimitLoadToSessionType = Aqua",
		"LaunchOnlyOnce = true",
		"EnableTransactions = true",
		"LaunchEvents = enabled",
		"SecureSocketWithKey = enabled",
		"inetdCompatibility = enabled",
	} {
		output := strings.Replace(darwinServicePrint(request, "harbor-user"), "path = ", field+"\npath = ", 1)
		if matchesDarwinServiceContract([]byte(output), request, "harbor-user") {
			t.Fatalf("matchesDarwinServiceContract() accepted %q", field)
		}
	}
}

// TestDarwinServiceContractRejectsChangedCanonicalFacts keeps installation, socket, and launchd identity facts bound to their exact values.
func TestDarwinServiceContractRejectsChangedCanonicalFacts(t *testing.T) {
	request := testRequest(t)
	username := "harbor-user"
	output := darwinServicePrint(request, username)
	for name, malformed := range map[string]string{
		"changed installation ID": strings.Replace(output, "HARBOR_INSTALLATION_ID => "+request.InstallationID(), "HARBOR_INSTALLATION_ID => foreign", 1),
		"missing installation ID": strings.Replace(output, "HARBOR_INSTALLATION_ID => "+request.InstallationID()+"\n", "", 1),
		"changed XPC name":        strings.Replace(output, "XPC_SERVICE_NAME => "+darwinLabel, "XPC_SERVICE_NAME => com.example.foreign", 1),
		"changed process type":    strings.Replace(output, "spawn type = daemon", "spawn type = interactive (1)", 1),
		"wrong node":              strings.Replace(output, "node name = 127.0.0.1", "node name = 0.0.0.0", 1),
		"wrong port":              strings.Replace(output, "service name = 80", "service name = 8080", 1),
		"duplicate endpoint":      strings.Replace(output, "service name = 80", "service name = 80\nservice name = 80", 1),
	} {
		t.Run(name, func(t *testing.T) {
			if matchesDarwinServiceContract([]byte(malformed), request, username) {
				t.Fatal("matchesDarwinServiceContract() accepted a changed canonical fact")
			}
		})
	}
}

// TestDarwinServiceContractAcceptsRuntimeFacts keeps launchd telemetry outside Harbor's ownership contract.
func TestDarwinServiceContractAcceptsRuntimeFacts(t *testing.T) {
	request := testRequest(t)
	output := strings.Replace(
		darwinServicePrint(request, "harbor-user"),
		"state = not running",
		"state = running\npid = 42\nspawn count = 1\nimmediate reason = speculative\nfuture launchd telemetry = enabled",
		1,
	)
	output = strings.Replace(
		output,
		"PATH => /usr/bin:/bin:/usr/sbin:/sbin",
		"PATH => /usr/bin:/bin:/usr/sbin:/sbin\nlaunchd default telemetry => enabled",
		1,
	)
	output = strings.Replace(
		output,
		"receive_packet_info = 0",
		"receive_packet_info = 0\nfuture socket telemetry = enabled\nruntime details = {\nvalue = 1\n}",
		1,
	)
	if !matchesDarwinServiceContract([]byte(output), request, "harbor-user") {
		t.Fatal("matchesDarwinServiceContract() rejected unrelated launchd runtime facts")
	}
}

// TestDarwinServiceContractRejectsForeignEnvironment keeps pre-main process configuration exact.
func TestDarwinServiceContractRejectsForeignEnvironment(t *testing.T) {
	request := testRequest(t)
	output := strings.Replace(
		darwinServicePrint(request, "harbor-user"),
		"XPC_SERVICE_NAME => "+darwinLabel,
		"XPC_SERVICE_NAME => "+darwinLabel+"\nHOME => /var/empty",
		1,
	)
	if matchesDarwinServiceContract([]byte(output), request, "harbor-user") {
		t.Fatal("matchesDarwinServiceContract() accepted foreign process environment")
	}
}

// darwinServicePrint returns the macOS 15 launchctl print shape accepted by the parser.
func darwinServicePrint(request Request, username string) string {
	return strings.Join([]string{
		"system/" + darwinLabel + " = {",
		"active count = 0",
		"path = " + darwinPlistPath,
		"type = LaunchDaemon",
		"state = not running",
		"program = /Library/PrivilegedHelperTools/com.goforj.harbor.launchdrelay",
		"username = " + username,
		"arguments = {",
		"/Library/PrivilegedHelperTools/com.goforj.harbor.launchdrelay",
		"--owner-uid",
		"501",
		"--policy-fingerprint",
		request.PolicyFingerprint(),
		"--http-upstream",
		request.HTTPUpstream().String(),
		"--https-upstream",
		request.HTTPSUpstream().String(),
		"}",
		"default environment = {",
		"PATH => /usr/bin:/bin:/usr/sbin:/sbin",
		"}",
		"environment = {",
		"HARBOR_INSTALLATION_ID => " + request.InstallationID(),
		"XPC_SERVICE_NAME => " + darwinLabel,
		"}",
		"domain = system",
		"minimum runtime = 10",
		"exit timeout = 5",
		"runs = 0",
		"last exit code = 0",
		"sockets = {",
		"\"HTTP\" = {",
		"type = stream",
		"node name = 127.0.0.1",
		"service name = 80",
		"sockets = {",
		"40 (no bytes to read)",
		"}",
		"active = 0",
		"passive = 0",
		"bonjour = 0",
		"ipv4v6 = 0",
		"receive_packet_info = 0",
		"}",
		"\"HTTPS\" = {",
		"type = stream",
		"node name = 127.0.0.1",
		"service name = 443",
		"sockets = {",
		"41 (no bytes to read)",
		"}",
		"active = 0",
		"passive = 0",
		"bonjour = 0",
		"ipv4v6 = 0",
		"receive_packet_info = 0",
		"}",
		"}",
		"spawn type = daemon",
		"jetsam priority = 40",
		"jetsam memory limit (active) = (unlimited)",
		"jetsam memory limit (inactive) = (unlimited)",
		"jetsamproperties category = daemon",
		"jetsam thread limit = 32",
		"cpumon = default",
		"probabilistic guard malloc policy = {",
		"}",
		"properties = runatload | inferred program | system service | tle system",
		"}",
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
