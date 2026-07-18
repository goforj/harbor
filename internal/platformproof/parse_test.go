package platformproof

import "testing"

// TestParseAddresses validates the command boundary independently from network mutation.
func TestParseAddresses(t *testing.T) {
	t.Parallel()

	addresses, err := ParseAddresses(" 127.77.254.10,127.77.254.11 ")
	if err != nil {
		t.Fatalf("parse addresses: %v", err)
	}
	if got := addresses[1].String(); got != "127.77.254.11" {
		t.Fatalf("expected second address 127.77.254.11, got %s", got)
	}

	for _, value := range []string{"", "127.0.0.1", "127.0.0.1,not-an-address", "127.0.0.1,127.0.0.1"} {
		if _, err := ParseAddresses(value); err == nil {
			t.Fatalf("expected %q to fail", value)
		}
	}
}
