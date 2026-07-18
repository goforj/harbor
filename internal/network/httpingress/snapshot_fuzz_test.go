package httpingress

import "testing"

// FuzzCanonicalDomain ensures arbitrary client names never panic or escape the .test suffix.
func FuzzCanonicalDomain(fuzzer *testing.F) {
	for _, seed := range []string{"orders.test", "ADMIN.ORDERS.TEST.", "", "*.test", "orders.test:443", "ordérs.test"} {
		fuzzer.Add(seed)
	}
	fuzzer.Fuzz(func(t *testing.T, raw string) {
		host, err := canonicalDomain(raw)
		if err != nil {
			return
		}
		if len(host) < len("a.test") || host[len(host)-len(".test"):] != ".test" {
			t.Fatalf("canonicalDomain(%q) escaped .test as %q", raw, host)
		}
	})
}
