// Package daemonservice defines Harbor's fixed installed per-user daemon services.
package daemonservice

import (
	"bytes"
	_ "embed"
)

const (
	// DarwinLabel identifies the per-user daemon independently of the desktop.
	DarwinLabel = "com.goforj.harbor.daemon"
	// DarwinLauncher is the package-owned stable release selector.
	DarwinLauncher = "/Library/Application Support/GoForj/Harbor/daemon-launcher"
	// DarwinWorkingDirectory keeps daemon startup outside any project checkout.
	DarwinWorkingDirectory = "/Library/Application Support/GoForj/Harbor/current"
	// LinuxUnit identifies the package-owned systemd user service.
	LinuxUnit = "harbord.service"
	// LinuxUnitPath prevents user overrides from silently becoming Harbor authority.
	LinuxUnitPath = "/usr/lib/systemd/user/harbord.service"
	// LinuxDaemonPath is the fixed executable shipped by the Debian package.
	LinuxDaemonPath = "/usr/lib/goforj-harbor/harbord"
	// LinuxLauncherPath prepares the user's database before handing ownership to harbord.
	LinuxLauncherPath = "/usr/lib/goforj-harbor/daemon-launcher"
)

// linuxDefinition is also installed verbatim by the native package workflow.
//
//go:embed harbord.service
var linuxDefinition string

// LinuxDefinition returns the exact unit admitted by installed desktop activation.
func LinuxDefinition() []byte {
	return []byte(linuxDefinition)
}

// DarwinDefinition is shared by first launch and ownership-aware uninstall.
func DarwinDefinition() []byte {
	// User-requested builds inherit the daemon's scheduling policy and must not be background-throttled.
	return darwinDefinition("Standard")
}

// MatchesDarwinDefinition admits only current or exact previously shipped bytes for safe service reuse and removal.
func MatchesDarwinDefinition(content []byte) bool {
	return bytes.Equal(content, DarwinDefinition()) || bytes.Equal(content, darwinDefinition("Background"))
}

// darwinDefinition keeps the legacy identity identical except for its previously shipped scheduling policy.
func darwinDefinition(processType string) []byte {
	return []byte(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>` + DarwinLabel + `</string>
  <key>ProgramArguments</key>
  <array>
    <string>` + DarwinLauncher + `</string>
  </array>
  <key>WorkingDirectory</key>
  <string>` + DarwinWorkingDirectory + `</string>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <dict>
    <key>SuccessfulExit</key>
    <false/>
  </dict>
  <key>ProcessType</key>
  <string>` + processType + `</string>
  <key>ThrottleInterval</key>
  <integer>5</integer>
</dict>
</plist>
`)
}
