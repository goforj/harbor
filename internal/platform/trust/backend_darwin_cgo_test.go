//go:build darwin && cgo

package trust

import (
	"errors"
	"testing"
)

// TestExecuteDarwinTrustReleaseClassifiesNativeResult proves stale rechecks remain distinct from native failures.
func TestExecuteDarwinTrustReleaseClassifiesNativeResult(t *testing.T) {
	nativeErr := errors.New("native release failed")
	for _, test := range []struct {
		name      string
		stale     bool
		nativeErr error
		want      error
	}{
		{
			name: "success",
		},
		{
			name:  "stale observation",
			stale: true,
			want:  errNativeObservationChanged,
		},
		{
			name:      "native failure",
			nativeErr: nativeErr,
			want:      nativeErr,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			err := executeDarwinTrustRelease(
				[]byte{1, 2, 3},
				"owner-account",
				"authority-fingerprint",
				func(root []byte, account string, fingerprint string) (bool, error) {
					calls++
					if len(root) != 3 || account != "owner-account" || fingerprint != "authority-fingerprint" {
						t.Fatalf("effect input = %v, %q, %q", root, account, fingerprint)
					}
					return test.stale, test.nativeErr
				},
			)
			if calls != 1 {
				t.Fatalf("effect calls = %d, want 1", calls)
			}
			if test.want == nil && err != nil {
				t.Fatalf("executeDarwinTrustRelease() error = %v", err)
			}
			if test.want != nil && !errors.Is(err, test.want) {
				t.Fatalf("executeDarwinTrustRelease() error = %v, want %v", err, test.want)
			}
		})
	}
}
