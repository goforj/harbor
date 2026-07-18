package localca

import (
	"strings"
	"testing"
)

// FuzzCanonicalDomain ensures arbitrary SAN input never panics or escapes Harbor's exact .test zone.
func FuzzCanonicalDomain(fuzzer *testing.F) {
	for _, seed := range []string{"orders.test", "ADMIN.ORDERS.TEST.", "", "*.test", "orders.test:443", "ordérs.test"} {
		fuzzer.Add(seed)
	}
	fuzzer.Fuzz(func(t *testing.T, raw string) {
		host, err := canonicalDomain(raw)
		if err != nil {
			return
		}
		if len(host) < len("a.test") || !strings.HasSuffix(host, ".test") {
			t.Fatalf("canonicalDomain(%q) escaped .test as %q", raw, host)
		}
	})
}
