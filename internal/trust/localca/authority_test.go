package localca

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var fixedTime = time.Date(2026, time.July, 18, 12, 0, 0, 0, time.UTC)

// TestNewCreatesConstrainedRoot verifies generated CA policy and defensive material ownership.
func TestNewCreatesConstrainedRoot(t *testing.T) {
	t.Parallel()
	authority := mustAuthority(t, Config{Now: func() time.Time { return fixedTime }})
	certificate := authority.Certificate()
	if !certificate.IsCA || !certificate.BasicConstraintsValid || !certificate.MaxPathLenZero || certificate.MaxPathLen != 0 {
		t.Fatalf("root constraints = %#v", certificate)
	}
	if certificate.Subject.CommonName != defaultCommonName || !reflect.DeepEqual(certificate.Subject.Organization, []string{"GoForj Harbor"}) {
		t.Fatalf("root subject = %#v", certificate.Subject)
	}
	if certificate.KeyUsage != x509.KeyUsageCertSign|x509.KeyUsageCRLSign || len(certificate.ExtKeyUsage) != 0 || len(certificate.DNSNames) != 0 {
		t.Fatalf("root usages = key %v, ext %v, names %v", certificate.KeyUsage, certificate.ExtKeyUsage, certificate.DNSNames)
	}
	if !certificate.NotBefore.Equal(fixedTime.Add(-defaultBackdate)) || !certificate.NotAfter.Equal(fixedTime.Add(defaultCAValidity)) {
		t.Fatalf("root validity = %s..%s", certificate.NotBefore, certificate.NotAfter)
	}
	if err := certificate.CheckSignatureFrom(certificate); err != nil {
		t.Fatalf("CheckSignatureFrom() error = %v", err)
	}
	material := authority.Material()
	if material.NotBefore != certificate.NotBefore || material.NotAfter != certificate.NotAfter {
		t.Fatalf("material validity = %#v", material)
	}
	digest := sha256.Sum256(certificate.Raw)
	if material.Fingerprint != hex.EncodeToString(digest[:]) {
		t.Fatalf("fingerprint = %q", material.Fingerprint)
	}
	if _, err := tls.X509KeyPair(material.CertificatePEM, material.PrivateKeyPEM); err != nil {
		t.Fatalf("X509KeyPair() error = %v", err)
	}

	material.CertificatePEM[0] = 'X'
	material.PrivateKeyPEM[0] = 'X'
	fresh := authority.Material()
	if fresh.CertificatePEM[0] == 'X' || fresh.PrivateKeyPEM[0] == 'X' {
		t.Fatal("Material() exposed authority-owned bytes")
	}
	certificate.Subject.CommonName = "mutated"
	if authority.Certificate().Subject.CommonName != defaultCommonName {
		t.Fatal("Certificate() exposed authority-owned certificate")
	}
}

// TestCertificateReturnsIndependentDER proves callers cannot corrupt retained trust through parsed raw views.
func TestCertificateReturnsIndependentDER(t *testing.T) {
	t.Parallel()
	authority := mustAuthority(t, Config{Now: func() time.Time { return fixedTime }})
	material := authority.Material()
	certificate := authority.Certificate()
	certificate.Raw[0] ^= 0xff
	certificate.RawTBSCertificate[0] ^= 0xff

	fresh := authority.Certificate()
	if err := fresh.CheckSignatureFrom(fresh); err != nil {
		t.Fatalf("CheckSignatureFrom() error = %v", err)
	}
	if !reflect.DeepEqual(authority.Material(), material) {
		t.Fatal("Certificate() exposed retained certificate material")
	}
	if _, err := authority.Issue(context.Background(), []string{"orders.test"}); err != nil {
		t.Fatalf("Issue() after returned DER mutation error = %v", err)
	}
}

// TestIssueCreatesExactServerCertificate verifies deterministic SANs, constraints, and TLS trust.
func TestIssueCreatesExactServerCertificate(t *testing.T) {
	t.Parallel()
	authority := mustAuthority(t, Config{Now: func() time.Time { return fixedTime }})
	leaf, err := authority.Issue(context.Background(), []string{"Admin.Orders.TEST.", "orders.test"})
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}
	wantHosts := []string{"admin.orders.test", "orders.test"}
	if !reflect.DeepEqual(leaf.Hosts, wantHosts) {
		t.Fatalf("Issue() hosts = %#v, want %#v", leaf.Hosts, wantHosts)
	}
	certificate := leaf.TLSCertificate.Leaf
	if certificate == nil || certificate.IsCA || !certificate.BasicConstraintsValid {
		t.Fatalf("leaf constraints = %#v", certificate)
	}
	if certificate.Subject.CommonName != wantHosts[0] || !reflect.DeepEqual(certificate.DNSNames, wantHosts) {
		t.Fatalf("leaf names = subject %#v, SANs %#v", certificate.Subject, certificate.DNSNames)
	}
	if certificate.KeyUsage != x509.KeyUsageDigitalSignature || !reflect.DeepEqual(certificate.ExtKeyUsage, []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}) {
		t.Fatalf("leaf usages = key %v, ext %v", certificate.KeyUsage, certificate.ExtKeyUsage)
	}
	if !certificate.NotBefore.Equal(fixedTime.Add(-defaultBackdate)) || !certificate.NotAfter.Equal(fixedTime.Add(defaultLeafValidity)) {
		t.Fatalf("leaf validity = %s..%s", certificate.NotBefore, certificate.NotAfter)
	}
	if leaf.Material.Fingerprint != certificateFingerprint(certificate) {
		t.Fatalf("leaf fingerprint = %q", leaf.Material.Fingerprint)
	}
	if _, err := tls.X509KeyPair(leaf.Material.CertificatePEM, leaf.Material.PrivateKeyPEM); err != nil {
		t.Fatalf("X509KeyPair() error = %v", err)
	}
	if err := certificate.VerifyHostname("unknown.test"); err == nil {
		t.Fatal("leaf trusted an unregistered host")
	}
	assertTLSHandshake(t, authority, leaf, "orders.test", true)
	assertTLSHandshake(t, authority, leaf, "unknown.test", false)
}

// TestLoadRoundTripRetainsIdentity verifies restart reload does not rotate public trust unexpectedly.
func TestLoadRoundTripRetainsIdentity(t *testing.T) {
	t.Parallel()
	config := Config{Now: func() time.Time { return fixedTime }}
	original := mustAuthority(t, config)
	material := original.Material()
	loaded, err := Load(config, material.CertificatePEM, material.PrivateKeyPEM)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got := loaded.Material().Fingerprint; got != material.Fingerprint {
		t.Fatalf("loaded fingerprint = %q, want %q", got, material.Fingerprint)
	}
	leaf, err := loaded.Issue(context.Background(), []string{"orders.test"})
	if err != nil {
		t.Fatalf("loaded Issue() error = %v", err)
	}
	assertTLSHandshake(t, loaded, leaf, "orders.test", true)
}

// TestLoadValidatesPolicyAndClock verifies syntactically valid persisted material still fails semantic admission.
func TestLoadValidatesPolicyAndClock(t *testing.T) {
	t.Parallel()
	base := mustAuthority(t, Config{Now: func() time.Time { return fixedTime }})
	invalidCertificate := *base.certificate
	invalidCertificate.IsCA = false
	invalidCertificate.MaxPathLen = -1
	invalidCertificate.MaxPathLenZero = false
	der, err := x509.CreateCertificate(rand.Reader, &invalidCertificate, &invalidCertificate, &base.privateKey.PublicKey, base.privateKey)
	if err != nil {
		t.Fatalf("CreateCertificate() error = %v", err)
	}
	invalidCertificate, err = parseCertificateDER(der)
	if err != nil {
		t.Fatalf("parseCertificateDER() error = %v", err)
	}
	material, err := encodeMaterial(&invalidCertificate, base.privateKey)
	if err != nil {
		t.Fatalf("encodeMaterial() error = %v", err)
	}
	if _, err := Load(Config{Now: func() time.Time { return fixedTime }}, material.CertificatePEM, material.PrivateKeyPEM); err == nil || !strings.Contains(err.Error(), "constrained CA") {
		t.Fatalf("Load(invalid policy) error = %v", err)
	}
	rootMaterial := base.Material()
	if _, err := Load(Config{Now: func() time.Time { return time.Time{} }}, rootMaterial.CertificatePEM, rootMaterial.PrivateKeyPEM); err == nil || !strings.Contains(err.Error(), "zero time") {
		t.Fatalf("Load(zero clock) error = %v", err)
	}
	if _, err := Load(Config{CommonName: " invalid "}, rootMaterial.CertificatePEM, rootMaterial.PrivateKeyPEM); err == nil || !strings.Contains(err.Error(), "common name") {
		t.Fatalf("Load(invalid config) error = %v", err)
	}
}

// TestLoadRejectsPersistedRootPolicyExtensions proves parsed extension policy is enforced after reload.
func TestLoadRejectsPersistedRootPolicyExtensions(t *testing.T) {
	t.Parallel()
	config := Config{Now: func() time.Time { return fixedTime }}
	base := mustAuthority(t, config)
	tests := []struct {
		name    string
		mutate  func(*x509.Certificate)
		parsed  func(*x509.Certificate) bool
		message string
	}{
		{
			name: "unknown extended usage",
			mutate: func(certificate *x509.Certificate) {
				certificate.UnknownExtKeyUsage = []asn1.ObjectIdentifier{{1, 2, 3, 4}}
			},
			parsed:  func(certificate *x509.Certificate) bool { return len(certificate.UnknownExtKeyUsage) == 1 },
			message: "leaf usages",
		},
		{
			name: "excluded DNS name constraint",
			mutate: func(certificate *x509.Certificate) {
				certificate.ExcludedDNSDomains = []string{".test"}
			},
			parsed: func(certificate *x509.Certificate) bool {
				return reflect.DeepEqual(certificate.ExcludedDNSDomains, []string{".test"})
			},
			message: "name constraints",
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			template := *base.certificate
			test.mutate(&template)
			der, err := x509.CreateCertificate(rand.Reader, &template, &template, &base.privateKey.PublicKey, base.privateKey)
			if err != nil {
				t.Fatalf("CreateCertificate() error = %v", err)
			}
			certificate, err := x509.ParseCertificate(der)
			if err != nil {
				t.Fatalf("ParseCertificate() error = %v", err)
			}
			if !test.parsed(certificate) {
				t.Fatalf("parsed certificate did not retain %s", test.name)
			}
			material, err := encodeMaterial(certificate, base.privateKey)
			if err != nil {
				t.Fatalf("encodeMaterial() error = %v", err)
			}
			if _, err := Load(config, material.CertificatePEM, material.PrivateKeyPEM); err == nil || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("Load() error = %v, want containing %q", err, test.message)
			}
		})
	}
}

// TestConfigValidation covers every public policy bound before entropy is consumed.
func TestConfigValidation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		config  Config
		message string
	}{
		{name: "whitespace common name", config: Config{CommonName: " Harbor "}, message: "common name"},
		{name: "long common name", config: Config{CommonName: strings.Repeat("a", maximumCommonName+1)}, message: "common name"},
		{name: "short CA", config: Config{CAValidity: time.Minute}, message: "CA validity"},
		{name: "long CA", config: Config{CAValidity: maximumCAValidity + time.Second}, message: "CA validity"},
		{name: "short leaf", config: Config{LeafValidity: time.Second}, message: "certificate validity"},
		{name: "long leaf", config: Config{LeafValidity: maximumLeafValidity + time.Second}, message: "certificate validity"},
		{name: "negative backdate", config: Config{Backdate: -time.Second}, message: "backdate"},
		{name: "long backdate", config: Config{Backdate: maximumBackdate + time.Second}, message: "backdate"},
		{name: "backdate exceeds leaf", config: Config{LeafValidity: time.Minute, Backdate: time.Minute}, message: "shorter"},
		{name: "zero clock", config: Config{Now: func() time.Time { return time.Time{} }}, message: "zero time"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if _, err := New(test.config); err == nil || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("New() error = %v, want containing %q", err, test.message)
			}
		})
	}
}

// TestIssueRejectsInvalidHostsAndContext covers all exact-name admission branches.
func TestIssueRejectsInvalidHostsAndContext(t *testing.T) {
	t.Parallel()
	authority := mustAuthority(t, Config{Now: func() time.Time { return fixedTime }})
	tooMany := make([]string, maximumLeafHosts+1)
	for index := range tooMany {
		tooMany[index] = "host" + strings.Repeat("a", index%10) + ".test"
	}
	tests := []struct {
		name    string
		hosts   []string
		message string
	}{
		{name: "none", message: "at least one"},
		{name: "too many", hosts: tooMany, message: "more than"},
		{name: "duplicate", hosts: []string{"orders.test", "ORDERS.TEST."}, message: "duplicated"},
		{name: "empty", hosts: []string{""}, message: "nonempty"},
		{name: "whitespace", hosts: []string{" orders.test"}, message: "nonempty"},
		{name: "outside", hosts: []string{"orders.local"}, message: "beneath .test"},
		{name: "zone root", hosts: []string{".test"}, message: "beneath .test"},
		{name: "empty label", hosts: []string{"admin..orders.test"}, message: "invalid label"},
		{name: "long label", hosts: []string{strings.Repeat("a", 64) + ".test"}, message: "invalid label"},
		{name: "leading hyphen", hosts: []string{"-orders.test"}, message: "invalid label"},
		{name: "trailing hyphen", hosts: []string{"orders-.test"}, message: "invalid label"},
		{name: "wildcard", hosts: []string{"*.orders.test"}, message: "only ASCII"},
		{name: "unicode", hosts: []string{"ordérs.test"}, message: "only ASCII"},
		{name: "long domain", hosts: []string{strings.Repeat("a.", 125) + "aaa.test"}, message: "must not exceed"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if _, err := authority.Issue(context.Background(), test.hosts); err == nil || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("Issue() error = %v, want containing %q", err, test.message)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := authority.Issue(ctx, []string{"orders.test"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Issue(cancelled) error = %v", err)
	}
	if _, err := authority.Issue(nil, []string{"orders.test"}); err != nil {
		t.Fatalf("Issue(nil context) error = %v", err)
	}
	if _, err := authority.Issue(&sequencedContext{}, []string{"orders.test"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Issue(context cancelled while queued) error = %v", err)
	}
}

// TestIssueClampsToRootExpirationAndRejectsExpiredRoot verifies leaf lifetimes cannot outlive trust.
func TestIssueClampsToRootExpirationAndRejectsExpiredRoot(t *testing.T) {
	t.Parallel()
	now := fixedTime
	authority := mustAuthority(t, Config{
		Now:          func() time.Time { return now },
		CAValidity:   2 * time.Hour,
		LeafValidity: 90 * time.Minute,
	})
	now = fixedTime.Add(time.Hour)
	leaf, err := authority.Issue(context.Background(), []string{"orders.test"})
	if err != nil {
		t.Fatalf("Issue() near expiration error = %v", err)
	}
	if !leaf.Material.NotAfter.Equal(authority.Certificate().NotAfter) {
		t.Fatalf("leaf expiration = %s, root = %s", leaf.Material.NotAfter, authority.Certificate().NotAfter)
	}
	now = authority.Certificate().NotAfter
	if _, err := authority.Issue(context.Background(), []string{"orders.test"}); err == nil || !strings.Contains(err.Error(), "not currently valid") {
		t.Fatalf("Issue() expired root error = %v", err)
	}
}

// TestIssueReportsClockAndEntropyFailures verifies runtime dependencies fail before material publication.
func TestIssueReportsClockAndEntropyFailures(t *testing.T) {
	t.Parallel()
	authority := mustAuthority(t, Config{Now: func() time.Time { return fixedTime }})
	authority.config.Now = func() time.Time { return time.Time{} }
	if _, err := authority.Issue(context.Background(), []string{"orders.test"}); err == nil || !strings.Contains(err.Error(), "zero time") {
		t.Fatalf("Issue(zero clock) error = %v", err)
	}
	sentinel := errors.New("leaf entropy unavailable")
	authority.config.Now = func() time.Time { return fixedTime }
	authority.config.Random = errorReader{err: sentinel}
	if _, err := authority.Issue(context.Background(), []string{"orders.test"}); !errors.Is(err, sentinel) {
		t.Fatalf("Issue(entropy failure) error = %v", err)
	}
}

// TestConcurrentIssueProducesUniqueSerials verifies authority entropy is safely serialized for any reader.
func TestConcurrentIssueProducesUniqueSerials(t *testing.T) {
	authority := mustAuthority(t, Config{Now: func() time.Time { return fixedTime }})
	const count = 32
	serials := make(chan string, count)
	errorsChannel := make(chan error, count)
	var workers sync.WaitGroup
	for index := 0; index < count; index++ {
		index := index
		workers.Add(1)
		go func() {
			defer workers.Done()
			leaf, err := authority.Issue(context.Background(), []string{fmtHost(index)})
			if err != nil {
				errorsChannel <- err
				return
			}
			serials <- leaf.TLSCertificate.Leaf.SerialNumber.String()
		}()
	}
	workers.Wait()
	close(serials)
	close(errorsChannel)
	for err := range errorsChannel {
		t.Fatalf("Issue() error = %v", err)
	}
	seen := make(map[string]struct{}, count)
	for serial := range serials {
		if _, exists := seen[serial]; exists {
			t.Fatalf("duplicate serial %q", serial)
		}
		seen[serial] = struct{}{}
	}
	if len(seen) != count {
		t.Fatalf("serial count = %d, want %d", len(seen), count)
	}
}

// TestLoadRejectsMalformedOrMismatchedMaterial exercises persisted-pair failure modes.
func TestLoadRejectsMalformedOrMismatchedMaterial(t *testing.T) {
	t.Parallel()
	config := Config{Now: func() time.Time { return fixedTime }}
	first := mustAuthority(t, config).Material()
	second := mustAuthority(t, config).Material()
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("GenerateKey(RSA) error = %v", err)
	}
	rsaDER, err := x509.MarshalPKCS8PrivateKey(rsaKey)
	if err != nil {
		t.Fatalf("MarshalPKCS8PrivateKey() error = %v", err)
	}
	p384Key, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey(P384) error = %v", err)
	}
	p384DER, err := x509.MarshalPKCS8PrivateKey(p384Key)
	if err != nil {
		t.Fatalf("MarshalPKCS8PrivateKey(P384) error = %v", err)
	}
	certificateBlock, _ := pem.Decode(first.CertificatePEM)
	certificateBlock.Headers = map[string]string{"Harbor-Test": "unsupported"}
	certificateWithHeaders := pem.EncodeToMemory(certificateBlock)
	privateKeyBlock, _ := pem.Decode(first.PrivateKeyPEM)
	privateKeyBlock.Headers = map[string]string{"Harbor-Test": "unsupported"}
	privateKeyWithHeaders := pem.EncodeToMemory(privateKeyBlock)
	malformedCertificatePrefix := []byte("-----BEGIN CERTIFICATE-----\ninvalid\n-----END CERTIFICATE-----\n")
	malformedPrivateKeyPrefix := []byte("-----BEGIN PRIVATE KEY-----\ninvalid\n-----END PRIVATE KEY-----\n")
	tests := []struct {
		name        string
		certificate []byte
		key         []byte
		message     string
	}{
		{name: "missing certificate", key: first.PrivateKeyPEM, message: "CERTIFICATE"},
		{name: "wrong certificate type", certificate: pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: []byte("bad")}), key: first.PrivateKeyPEM, message: "CERTIFICATE"},
		{name: "corrupt certificate", certificate: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("bad")}), key: first.PrivateKeyPEM, message: "parse certificate"},
		{name: "certificate leading", certificate: append([]byte("leading data\n"), first.CertificatePEM...), key: first.PrivateKeyPEM, message: "leading data"},
		{name: "malformed certificate leading", certificate: append(malformedCertificatePrefix, first.CertificatePEM...), key: first.PrivateKeyPEM, message: "leading data"},
		{name: "certificate headers", certificate: certificateWithHeaders, key: first.PrivateKeyPEM, message: "headers are not supported"},
		{name: "certificate trailing", certificate: append(append([]byte(nil), first.CertificatePEM...), []byte("trailing")...), key: first.PrivateKeyPEM, message: "trailing data"},
		{name: "missing key", certificate: first.CertificatePEM, message: "PRIVATE KEY"},
		{name: "wrong key type", certificate: first.CertificatePEM, key: pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: []byte("bad")}), message: "PRIVATE KEY"},
		{name: "corrupt key", certificate: first.CertificatePEM, key: pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("bad")}), message: "PKCS#8"},
		{name: "key leading", certificate: first.CertificatePEM, key: append([]byte("leading data\n"), first.PrivateKeyPEM...), message: "leading data"},
		{name: "malformed key leading", certificate: first.CertificatePEM, key: append(malformedPrivateKeyPrefix, first.PrivateKeyPEM...), message: "leading data"},
		{name: "key headers", certificate: first.CertificatePEM, key: privateKeyWithHeaders, message: "headers are not supported"},
		{name: "key trailing", certificate: first.CertificatePEM, key: append(append([]byte(nil), first.PrivateKeyPEM...), []byte("trailing")...), message: "trailing data"},
		{name: "RSA key", certificate: first.CertificatePEM, key: pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: rsaDER}), message: "ECDSA P-256"},
		{name: "P384 key", certificate: first.CertificatePEM, key: pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: p384DER}), message: "ECDSA P-256"},
		{name: "mismatch", certificate: first.CertificatePEM, key: second.PrivateKeyPEM, message: "does not match"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if _, err := Load(config, test.certificate, test.key); err == nil || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("Load() error = %v, want containing %q", err, test.message)
			}
		})
	}
}

// TestRootValidationRejectsPolicyDrift directly covers persisted-certificate semantic checks.
func TestRootValidationRejectsPolicyDrift(t *testing.T) {
	t.Parallel()
	base := mustAuthority(t, Config{Now: func() time.Time { return fixedTime }})
	rsaCertificate := mustRSARoot(t, base.certificate)
	mismatchedKey := mustECDSAKey(t, elliptic.P256())
	testPolicy, err := x509.OIDFromInts([]uint64{1, 2, 3, 4})
	if err != nil {
		t.Fatalf("OIDFromInts() error = %v", err)
	}
	tests := []struct {
		name    string
		mutate  func(*Authority)
		message string
	}{
		{name: "serial", mutate: func(authority *Authority) { authority.certificate.SerialNumber = nil }, message: "serial number"},
		{name: "not CA", mutate: func(authority *Authority) { authority.certificate.IsCA = false }, message: "constrained CA"},
		{name: "path length", mutate: func(authority *Authority) { authority.certificate.MaxPathLenZero = false }, message: "subordinate"},
		{name: "usage", mutate: func(authority *Authority) { authority.certificate.KeyUsage = x509.KeyUsageCertSign }, message: "signing key usage"},
		{name: "leaf usage", mutate: func(authority *Authority) {
			authority.certificate.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
		}, message: "leaf usages"},
		{name: "unknown leaf usage", mutate: func(authority *Authority) {
			authority.certificate.UnknownExtKeyUsage = []asn1.ObjectIdentifier{{1, 2, 3, 4}}
		}, message: "leaf usages"},
		{name: "names", mutate: func(authority *Authority) {
			authority.certificate.IPAddresses = []net.IP{net.ParseIP("127.0.0.1")}
		}, message: "leaf usages or names"},
		{name: "critical name constraints", mutate: func(authority *Authority) {
			authority.certificate.PermittedDNSDomainsCritical = true
		}, message: "name constraints"},
		{name: "permitted DNS constraints", mutate: func(authority *Authority) {
			authority.certificate.PermittedDNSDomains = []string{".test"}
		}, message: "name constraints"},
		{name: "excluded DNS constraints", mutate: func(authority *Authority) {
			authority.certificate.ExcludedDNSDomains = []string{".test"}
		}, message: "name constraints"},
		{name: "permitted IP constraints", mutate: func(authority *Authority) {
			authority.certificate.PermittedIPRanges = []*net.IPNet{new(net.IPNet)}
		}, message: "name constraints"},
		{name: "excluded IP constraints", mutate: func(authority *Authority) {
			authority.certificate.ExcludedIPRanges = []*net.IPNet{new(net.IPNet)}
		}, message: "name constraints"},
		{name: "permitted email constraints", mutate: func(authority *Authority) {
			authority.certificate.PermittedEmailAddresses = []string{"example.test"}
		}, message: "name constraints"},
		{name: "excluded email constraints", mutate: func(authority *Authority) {
			authority.certificate.ExcludedEmailAddresses = []string{"example.test"}
		}, message: "name constraints"},
		{name: "permitted URI constraints", mutate: func(authority *Authority) {
			authority.certificate.PermittedURIDomains = []string{".test"}
		}, message: "name constraints"},
		{name: "excluded URI constraints", mutate: func(authority *Authority) {
			authority.certificate.ExcludedURIDomains = []string{".test"}
		}, message: "name constraints"},
		{name: "OCSP location", mutate: func(authority *Authority) {
			authority.certificate.OCSPServer = []string{"https://ocsp.example.test"}
		}, message: "authority information access"},
		{name: "issuer location", mutate: func(authority *Authority) {
			authority.certificate.IssuingCertificateURL = []string{"https://issuer.example.test"}
		}, message: "authority information access"},
		{name: "CRL distribution", mutate: func(authority *Authority) {
			authority.certificate.CRLDistributionPoints = []string{"https://crl.example.test"}
		}, message: "CRL distribution"},
		{name: "legacy policy identifier", mutate: func(authority *Authority) {
			authority.certificate.PolicyIdentifiers = []asn1.ObjectIdentifier{{1, 2, 3, 4}}
		}, message: "certificate policies"},
		{name: "policy identifier", mutate: func(authority *Authority) {
			authority.certificate.Policies = []x509.OID{testPolicy}
		}, message: "certificate policies"},
		{name: "inhibit any policy", mutate: func(authority *Authority) {
			authority.certificate.InhibitAnyPolicy = 1
		}, message: "certificate policies"},
		{name: "inhibit any policy zero", mutate: func(authority *Authority) {
			authority.certificate.InhibitAnyPolicyZero = true
		}, message: "certificate policies"},
		{name: "inhibit policy mapping", mutate: func(authority *Authority) {
			authority.certificate.InhibitPolicyMapping = 1
		}, message: "certificate policies"},
		{name: "inhibit policy mapping zero", mutate: func(authority *Authority) {
			authority.certificate.InhibitPolicyMappingZero = true
		}, message: "certificate policies"},
		{name: "require explicit policy", mutate: func(authority *Authority) {
			authority.certificate.RequireExplicitPolicy = 1
		}, message: "certificate policies"},
		{name: "require explicit policy zero", mutate: func(authority *Authority) {
			authority.certificate.RequireExplicitPolicyZero = true
		}, message: "certificate policies"},
		{name: "policy mapping", mutate: func(authority *Authority) {
			authority.certificate.PolicyMappings = []x509.PolicyMapping{{IssuerDomainPolicy: testPolicy, SubjectDomainPolicy: testPolicy}}
		}, message: "certificate policies"},
		{name: "critical extension", mutate: func(authority *Authority) {
			authority.certificate.UnhandledCriticalExtensions = append(authority.certificate.UnhandledCriticalExtensions, authority.certificate.Extensions[0].Id)
		}, message: "critical extension"},
		{name: "noncritical extension", mutate: func(authority *Authority) {
			authority.certificate.Extensions = append(authority.certificate.Extensions, pkix.Extension{Id: asn1.ObjectIdentifier{1, 2, 3, 4}})
		}, message: "unsupported extension"},
		{name: "common name", mutate: func(authority *Authority) { authority.certificate.Subject.CommonName = "" }, message: "common name"},
		{name: "signature algorithm", mutate: func(authority *Authority) { authority.certificate.SignatureAlgorithm = x509.ECDSAWithSHA384 }, message: "SHA-256"},
		{name: "lifetime", mutate: func(authority *Authority) { authority.certificate.NotAfter = authority.certificate.NotBefore }, message: "lifetime"},
		{name: "future", mutate: func(authority *Authority) { authority.certificate.NotBefore = fixedTime.Add(time.Hour) }, message: "currently valid"},
		{name: "issuer", mutate: func(authority *Authority) { authority.certificate.RawIssuer = []byte("other") }, message: "self-issued"},
		{name: "signature", mutate: func(authority *Authority) { authority.certificate.Signature = []byte("bad") }, message: "self-signed"},
		{name: "public algorithm", mutate: func(authority *Authority) { authority.certificate = rsaCertificate }, message: "ECDSA P-256"},
		{name: "key mismatch", mutate: func(authority *Authority) { authority.privateKey = mismatchedKey }, message: "does not match"},
		{name: "subject key ID", mutate: func(authority *Authority) { authority.certificate.SubjectKeyId = []byte("wrong") }, message: "subject key"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			copyCertificate := *base.certificate
			copyAuthority := &Authority{
				config:      base.config,
				certificate: &copyCertificate,
				privateKey:  base.privateKey,
				material:    base.material,
			}
			test.mutate(copyAuthority)
			if err := copyAuthority.validateRoot(fixedTime); err == nil || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("validateRoot() error = %v, want containing %q", err, test.message)
			}
		})
	}
}

// TestVerifyLeafRejectsPolicyDrift covers every semantic check before TLS publication.
func TestVerifyLeafRejectsPolicyDrift(t *testing.T) {
	t.Parallel()
	authority := mustAuthority(t, Config{Now: func() time.Time { return fixedTime }})
	leaf, err := authority.Issue(context.Background(), []string{"orders.test"})
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}
	base := leaf.TLSCertificate.Leaf
	tests := []struct {
		name    string
		mutate  func(*x509.Certificate)
		message string
	}{
		{name: "serial", mutate: func(certificate *x509.Certificate) { certificate.SerialNumber = nil }, message: "serial number"},
		{name: "CA", mutate: func(certificate *x509.Certificate) { certificate.IsCA = true }, message: "basic constraints"},
		{name: "key usage", mutate: func(certificate *x509.Certificate) { certificate.KeyUsage = x509.KeyUsageKeyEncipherment }, message: "key usage"},
		{name: "extended usage", mutate: func(certificate *x509.Certificate) {
			certificate.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
		}, message: "server-auth"},
		{name: "authority key", mutate: func(certificate *x509.Certificate) { certificate.AuthorityKeyId = []byte("wrong") }, message: "authority key"},
		{name: "public key", mutate: func(certificate *x509.Certificate) { certificate.PublicKey = &rsa.PublicKey{} }, message: "ECDSA P-256"},
		{name: "subject key", mutate: func(certificate *x509.Certificate) { certificate.SubjectKeyId = []byte("wrong") }, message: "subject key"},
		{name: "signature algorithm", mutate: func(certificate *x509.Certificate) { certificate.SignatureAlgorithm = x509.ECDSAWithSHA384 }, message: "SHA-256"},
		{name: "hostname", mutate: func(certificate *x509.Certificate) { certificate.DNSNames = []string{"other.test"} }, message: "requested exact hosts"},
		{name: "non-DNS name", mutate: func(certificate *x509.Certificate) {
			certificate.IPAddresses = []net.IP{net.ParseIP("127.0.0.1")}
		}, message: "DNS names only"},
		{name: "critical extension", mutate: func(certificate *x509.Certificate) {
			certificate.UnhandledCriticalExtensions = append(certificate.UnhandledCriticalExtensions, certificate.Extensions[0].Id)
		}, message: "critical extension"},
		{name: "lifetime", mutate: func(certificate *x509.Certificate) { certificate.NotAfter = certificate.NotBefore }, message: "lifetime"},
		{name: "signature", mutate: func(certificate *x509.Certificate) { certificate.Signature = []byte("bad") }, message: "signature is invalid"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			copyCertificate := *base
			test.mutate(&copyCertificate)
			if err := authority.verifyLeaf(&copyCertificate, []string{"orders.test"}, fixedTime); err == nil || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("verifyLeaf() error = %v, want containing %q", err, test.message)
			}
		})
	}
}

// TestCertificatePanicsOnRetainedCorruption verifies impossible in-memory corruption fails fast.
func TestCertificatePanicsOnRetainedCorruption(t *testing.T) {
	t.Parallel()
	authority := mustAuthority(t, Config{Now: func() time.Time { return fixedTime }})
	authority.certificate.Raw = []byte("corrupt")
	defer func() {
		if recover() == nil {
			t.Fatal("Certificate() did not panic on retained corruption")
		}
	}()
	_ = authority.Certificate()
}

// TestEntropyAndEncodingFailures verifies cryptographic boundary errors retain their causes.
func TestEntropyAndEncodingFailures(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("entropy unavailable")
	if _, err := New(Config{Random: errorReader{err: sentinel}}); !errors.Is(err, sentinel) {
		t.Fatalf("New(entropy failure) error = %v", err)
	}
	if serial, err := randomSerial(bytes.NewReader(make([]byte, 16))); err != nil || serial.Int64() != 1 {
		t.Fatalf("randomSerial(zero) = %v, %v", serial, err)
	}
	if _, err := randomSerial(bytes.NewReader([]byte{1})); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("randomSerial(short) error = %v", err)
	}
	p384 := mustECDSAKey(t, elliptic.P384())
	if _, err := encodeMaterial(baseCertificate(t), p384); err == nil || !strings.Contains(err.Error(), "ECDSA P-256") {
		t.Fatalf("encodeMaterial(P384) error = %v", err)
	}
	first := mustAuthority(t, Config{Now: func() time.Time { return fixedTime }})
	second := mustAuthority(t, Config{Now: func() time.Time { return fixedTime }})
	if _, err := encodeMaterial(first.certificate, second.privateKey); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("encodeMaterial(mismatch) error = %v", err)
	}
	corruptCertificate := *first.certificate
	corruptCertificate.Raw = []byte("corrupt")
	if _, err := encodeMaterial(&corruptCertificate, first.privateKey); err == nil || !strings.Contains(err.Error(), "validate encoded") {
		t.Fatalf("encodeMaterial(corrupt certificate) error = %v", err)
	}
}

// sequencedContext becomes cancelled on its second Err call to exercise queue revalidation.
type sequencedContext struct {
	calls atomic.Int64
}

// Deadline reports no deadline because cancellation is controlled by Err calls.
func (ctx *sequencedContext) Deadline() (time.Time, bool) {
	return time.Time{}, false
}

// Done has no channel because Issue checks Err at both authority boundaries.
func (ctx *sequencedContext) Done() <-chan struct{} {
	return nil
}

// Err reports cancellation after the first successful admission check.
func (ctx *sequencedContext) Err() error {
	if ctx.calls.Add(1) > 1 {
		return context.Canceled
	}
	return nil
}

// Value has no values because this test context carries only cancellation state.
func (ctx *sequencedContext) Value(any) any {
	return nil
}

// errorReader always fails so entropy error propagation is deterministic.
type errorReader struct {
	err error
}

// Read returns the configured entropy failure without producing bytes.
func (reader errorReader) Read([]byte) (int, error) {
	return 0, reader.err
}

// mustAuthority fails before a test can use incomplete trust state.
func mustAuthority(t *testing.T, config Config) *Authority {
	t.Helper()
	authority, err := New(config)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return authority
}

// mustECDSAKey creates one test key on the requested curve.
func mustECDSAKey(t *testing.T, curve elliptic.Curve) *ecdsa.PrivateKey {
	t.Helper()
	privateKey, err := ecdsa.GenerateKey(curve, rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}
	return privateKey
}

// mustRSARoot creates a valid self-signed CA whose unsupported key reaches algorithm policy validation.
func mustRSARoot(t *testing.T, source *x509.Certificate) *x509.Certificate {
	t.Helper()
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("GenerateKey(RSA root) error = %v", err)
	}
	template := *source
	template.SignatureAlgorithm = x509.UnknownSignatureAlgorithm
	template.PublicKey = &privateKey.PublicKey
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &privateKey.PublicKey, privateKey)
	if err != nil {
		t.Fatalf("CreateCertificate(RSA root) error = %v", err)
	}
	certificate, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("ParseCertificate(RSA root) error = %v", err)
	}
	return certificate
}

// baseCertificate returns a valid P-256 leaf-shaped certificate for encoding mismatch tests.
func baseCertificate(t *testing.T) *x509.Certificate {
	t.Helper()
	authority := mustAuthority(t, Config{Now: func() time.Time { return fixedTime }})
	return authority.certificate
}

// parseCertificateDER keeps test certificate construction failures explicit.
func parseCertificateDER(der []byte) (x509.Certificate, error) {
	certificate, err := x509.ParseCertificate(der)
	if err != nil {
		return x509.Certificate{}, err
	}
	return *certificate, nil
}

// assertTLSHandshake verifies real client trust and hostname behavior over an in-memory connection.
func assertTLSHandshake(t *testing.T, authority *Authority, leaf Leaf, host string, valid bool) {
	t.Helper()
	serverConnection, clientConnection := net.Pipe()
	defer serverConnection.Close()
	defer clientConnection.Close()
	deadline := time.Now().Add(5 * time.Second)
	_ = serverConnection.SetDeadline(deadline)
	_ = clientConnection.SetDeadline(deadline)
	server := tls.Server(serverConnection, &tls.Config{
		Certificates: []tls.Certificate{leaf.TLSCertificate},
		MinVersion:   tls.VersionTLS12,
		Time:         func() time.Time { return fixedTime },
	})
	roots := x509.NewCertPool()
	roots.AddCert(authority.Certificate())
	client := tls.Client(clientConnection, &tls.Config{
		RootCAs:    roots,
		ServerName: host,
		MinVersion: tls.VersionTLS12,
		Time:       func() time.Time { return fixedTime },
	})
	serverDone := make(chan error, 1)
	go func() {
		serverDone <- server.Handshake()
	}()
	clientErr := client.Handshake()
	serverErr := <-serverDone
	if valid {
		if clientErr != nil || serverErr != nil {
			t.Fatalf("TLS handshake errors = client %v, server %v", clientErr, serverErr)
		}
		return
	}
	if clientErr == nil {
		t.Fatal("TLS handshake unexpectedly trusted host")
	}
}

// fmtHost creates a unique valid exact host without importing formatting into production code.
func fmtHost(index int) string {
	const digits = "0123456789"
	if index < 10 {
		return "host" + string(digits[index]) + ".test"
	}
	return "host" + string(digits[index/10]) + string(digits[index%10]) + ".test"
}
