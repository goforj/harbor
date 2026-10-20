//go:build windows

package main

import "testing"

// TestSetupArguments permits the ordinary and quiet installer entrypoints while refusing all redirection inputs before machine access.
func TestSetupArguments(t *testing.T) {
	for _, arguments := range [][]string{nil, {"--silent"}} {
		if err := admitSetupArguments(arguments); err != nil {
			t.Fatal(err)
		}
	}
	for _, arguments := range [][]string{{"/S"}, {"--silent", "--silent"}, {"--root=C:\\Foreign"}, {"--sid=S-1-5-18"}, {"--command=anything"}} {
		if err := runSetup(arguments); err == nil {
			t.Fatalf("installer input admitted: %v", arguments)
		}
	}
}
