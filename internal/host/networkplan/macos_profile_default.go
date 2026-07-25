//go:build !hostedacceptance

package networkplan

import "github.com/goforj/harbor/internal/host/networkpolicy"

// macOSMechanismsForBuild keeps normal Harbor builds on administrator trust.
func macOSMechanismsForBuild() networkpolicy.Mechanisms {
	return networkpolicy.MacOSMechanisms()
}
