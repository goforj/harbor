//go:build linux

package trust

import (
	"bytes"
	"encoding/pem"
	"strings"
	"testing"
)

// TestUbuntuBundleMatchCountingBindsDERAndBoundsDuplicates covers the active system-store parser.
func TestUbuntuBundleMatchCountingBindsDERAndBoundsDuplicates(t *testing.T) {
	request := ubuntuTrustTestRequest(t)
	root := request.Root()
	matches, err := countUbuntuBundleMatches(root.CertificatePEM, request.AuthorityFingerprint())
	if err != nil || matches != 1 {
		t.Fatalf("countUbuntuBundleMatches() = (%d, %v)", matches, err)
	}
	block, _ := pem.Decode(root.CertificatePEM)
	other := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: append([]byte(nil), block.Bytes...)})
	other[len(other)-2] ^= 1
	if _, err := countUbuntuBundleMatches(other, request.AuthorityFingerprint()); err == nil {
		t.Fatal("countUbuntuBundleMatches() accepted malformed PEM")
	}
	tooMany := bytes.Repeat(root.CertificatePEM, maximumUbuntuActiveRootMatches+1)
	if _, err := countUbuntuBundleMatches(tooMany, request.AuthorityFingerprint()); err == nil {
		t.Fatal("countUbuntuBundleMatches() accepted excessive matching roots")
	}
	if got := ubuntuCertificateFingerprint([]byte("malformed")); len(got) != canonicalFingerprintLength || strings.Trim(got, "0123456789abcdef") != "" {
		t.Fatalf("ubuntuCertificateFingerprint(malformed) = %q", got)
	}
}

// TestUbuntuLimitedBufferDrainsWhileRetainingOnlyItsBound proves child output cannot exceed evidence bounds.
func TestUbuntuLimitedBufferDrainsWhileRetainingOnlyItsBound(t *testing.T) {
	buffer := &ubuntuLimitedBuffer{maximum: 4}
	written, err := buffer.Write([]byte("abcdef"))
	if err != nil || written != 6 || buffer.String() != "abcd" {
		t.Fatalf("Write() = (%d, %v), output = %q", written, err, buffer.String())
	}
}
