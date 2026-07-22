package control

import (
	"testing"

	"github.com/goforj/harbor/internal/host/networkpolicy"
)

// TestValidNetworkDataPlaneTrustMechanismAllowsOnlyCompleteProfiles keeps setup and release on one exact platform profile.
func TestValidNetworkDataPlaneTrustMechanismAllowsOnlyCompleteProfiles(t *testing.T) {
	tests := []struct {
		name      string
		mechanism networkpolicy.TrustMechanism
		want      bool
	}{
		{
			name:      "administrator",
			mechanism: networkpolicy.DarwinAdministratorTrust,
			want:      true,
		},
		{
			name:      "unknown",
			mechanism: "unsupported",
			want:      false,
		},
		{
			name: "mixed",
			mechanism: networkpolicy.TrustMechanism(
				string(networkpolicy.DarwinAdministratorTrust) + "," + string(networkpolicy.DarwinCurrentUserTrust),
			),
			want: false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := validNetworkDataPlaneTrustMechanism(test.mechanism); got != test.want {
				t.Fatalf("validNetworkDataPlaneTrustMechanism(%q) = %t, want %t", test.mechanism, got, test.want)
			}
		})
	}
}
