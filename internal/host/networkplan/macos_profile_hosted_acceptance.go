//go:build hostedacceptance

package networkplan

import "github.com/goforj/harbor/internal/host/networkpolicy"

// macOSMechanismsForBuild uses current-user trust where hosted macOS runners cannot authorize administrator trust deletion.
func macOSMechanismsForBuild() networkpolicy.Mechanisms {
	return networkpolicy.LegacyMacOSMechanisms()
}
