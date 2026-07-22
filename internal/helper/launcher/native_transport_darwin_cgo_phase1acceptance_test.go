//go:build darwin && cgo && phase1acceptance

package launcher

import (
	"os"
	"testing"
)

const darwinAuthorizationPreflightEnvironment = "HARBOR_DARWIN_AUTHORIZATION_PREFLIGHT"

// TestDarwinAuthorizationPreflight obtains and destroys production execute authorization on an admitted disposable host.
func TestDarwinAuthorizationPreflight(t *testing.T) {
	if os.Getenv(darwinAuthorizationPreflightEnvironment) != "1" {
		t.Skipf("set %s=1 to run the Darwin Authorization Services preflight", darwinAuthorizationPreflightEnvironment)
	}

	grant, err := preauthorizeDarwinExecute()
	if err != nil {
		t.Fatalf("preauthorize Darwin execute authorization: %v", err)
	}
	if grant.declined {
		t.Fatal("Darwin execute authorization preflight was declined")
	}
	if grant.release == nil {
		t.Fatal("Darwin execute authorization preflight returned no release authority")
	}
	t.Cleanup(func() {
		if err := grant.release(); err != nil {
			t.Errorf("destroy Darwin execute authorization: %v", err)
		}
	})
}
