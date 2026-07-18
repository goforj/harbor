// Package localca creates and reloads Harbor-owned local development certificates.
package localca

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	defaultCommonName   = "GoForj Harbor Local Development CA"
	defaultCAValidity   = 10 * 365 * 24 * time.Hour
	maximumCAValidity   = 20 * 365 * 24 * time.Hour
	defaultLeafValidity = 30 * 24 * time.Hour
	maximumLeafValidity = 90 * 24 * time.Hour
	defaultBackdate     = 5 * time.Minute
	maximumBackdate     = time.Hour
	maximumCommonName   = 128
	maximumLeafHosts    = 100
	maximumDomainLength = 253
	rootSubjectKeyID    = "2.5.29.14"
	rootKeyUsage        = "2.5.29.15"
	rootBasicConstraint = "2.5.29.19"
	leafSubjectAltName  = "2.5.29.17"
	leafAuthorityKeyID  = "2.5.29.35"
	leafExtendedUsage   = "2.5.29.37"
)

// Config controls certificate lifetimes and supplies deterministic boundaries for tests.
type Config struct {
	// CommonName identifies the user-profile CA without embedding a username or machine secret.
	// Empty selects Harbor's stable default.
	CommonName string
	// CAValidity controls a newly generated root's lifetime.
	// Zero selects Harbor's conservative default.
	CAValidity time.Duration
	// LeafValidity controls each newly issued server certificate's lifetime.
	// Zero selects Harbor's conservative default.
	LeafValidity time.Duration
	// Backdate tolerates small clock adjustments without extending the configured expiration.
	// Zero selects Harbor's conservative default.
	Backdate time.Duration
	// Now supplies the current time.
	// Nil uses time.Now.
	Now func() time.Time
	// Random supplies cryptographic key and serial entropy.
	// Nil uses crypto/rand.Reader.
	Random io.Reader
}

// Material is one validated certificate and private key pair ready for atomic persistence.
type Material struct {
	// CertificatePEM contains one X.509 certificate and no private material.
	CertificatePEM []byte
	// PrivateKeyPEM contains one unencrypted PKCS#8 ECDSA private key.
	PrivateKeyPEM []byte
	// Fingerprint is the lowercase SHA-256 digest of the certificate DER.
	Fingerprint string
	// NotBefore is the certificate's UTC activation time.
	NotBefore time.Time
	// NotAfter is the certificate's UTC expiration time.
	NotAfter time.Time
}

// Leaf is one exact-name server certificate plus its validated TLS representation.
type Leaf struct {
	// Hosts contains the canonical exact DNS SANs in deterministic order.
	Hosts []string
	// Material contains the persistable certificate and private key pair.
	Material Material
	// TLSCertificate is ready for tls.Config certificate selection.
	TLSCertificate tls.Certificate
}

// Authority owns one self-signed CA and serializes access to its entropy source.
type Authority struct {
	config      Config
	certificate *x509.Certificate
	privateKey  *ecdsa.PrivateKey
	material    Material
	mutex       sync.Mutex
}

// New generates one modern self-signed local CA for the current user profile.
func New(config Config) (*Authority, error) {
	config = normalizeConfig(config)
	if err := validateConfig(config); err != nil {
		return nil, err
	}
	now, err := currentTime(config.Now)
	if err != nil {
		return nil, err
	}
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), config.Random)
	if err != nil {
		return nil, fmt.Errorf("generate local CA private key: %w", err)
	}
	serial, err := randomSerial(config.Random)
	if err != nil {
		return nil, fmt.Errorf("generate local CA serial: %w", err)
	}
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   config.CommonName,
			Organization: []string{"GoForj Harbor"},
		},
		NotBefore:             now.Add(-config.Backdate),
		NotAfter:              now.Add(config.CAValidity),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            0,
		MaxPathLenZero:        true,
		SubjectKeyId:          subjectKeyID(&privateKey.PublicKey),
	}
	der, err := x509.CreateCertificate(config.Random, template, template, &privateKey.PublicKey, privateKey)
	if err != nil {
		return nil, fmt.Errorf("create local CA certificate: %w", err)
	}
	certificate, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, fmt.Errorf("parse generated local CA certificate: %w", err)
	}
	material, err := encodeMaterial(certificate, privateKey)
	if err != nil {
		return nil, err
	}
	authority := &Authority{config: config, certificate: certificate, privateKey: privateKey, material: material}
	if err := authority.validateRoot(now); err != nil {
		return nil, fmt.Errorf("validate generated local CA: %w", err)
	}
	return authority, nil
}

// Load parses and validates a previously persisted Harbor CA certificate and key pair.
func Load(config Config, certificatePEM, privateKeyPEM []byte) (*Authority, error) {
	config = normalizeConfig(config)
	if err := validateConfig(config); err != nil {
		return nil, err
	}
	now, err := currentTime(config.Now)
	if err != nil {
		return nil, err
	}
	certificate, err := parseCertificatePEM(certificatePEM)
	if err != nil {
		return nil, fmt.Errorf("load local CA certificate: %w", err)
	}
	privateKey, err := parsePrivateKeyPEM(privateKeyPEM)
	if err != nil {
		return nil, fmt.Errorf("load local CA private key: %w", err)
	}
	material, err := encodeMaterial(certificate, privateKey)
	if err != nil {
		return nil, err
	}
	authority := &Authority{config: config, certificate: certificate, privateKey: privateKey, material: material}
	if err := authority.validateRoot(now); err != nil {
		return nil, fmt.Errorf("validate loaded local CA: %w", err)
	}
	return authority, nil
}

// Material returns defensive copies of the root certificate and private key encoding.
func (authority *Authority) Material() Material {
	return cloneMaterial(authority.material)
}

// Certificate returns a fresh parsed copy so callers cannot mutate signing policy.
func (authority *Authority) Certificate() *x509.Certificate {
	certificate, err := x509.ParseCertificate(append([]byte(nil), authority.certificate.Raw...))
	if err != nil {
		panic(fmt.Sprintf("localca: retained certificate became unparsable: %v", err))
	}
	return certificate
}

// ValidateCurrent proves the retained root remains usable at the authority's configured clock.
func (authority *Authority) ValidateCurrent() error {
	authority.mutex.Lock()
	defer authority.mutex.Unlock()
	now, err := currentTime(authority.config.Now)
	if err != nil {
		return err
	}
	if err := authority.validateRoot(now); err != nil {
		return fmt.Errorf("validate local CA: %w", err)
	}
	return nil
}

// CanonicalHosts validates and returns Harbor's deterministic exact-name certificate identity.
func CanonicalHosts(hosts []string) ([]string, error) {
	return canonicalizeHosts(hosts)
}

// LoadLeaf parses a persisted server certificate and proves it still belongs to this authority and exact host set.
func (authority *Authority) LoadLeaf(certificatePEM, privateKeyPEM []byte, expectedHosts []string) (Leaf, error) {
	canonicalHosts, err := canonicalizeHosts(expectedHosts)
	if err != nil {
		return Leaf{}, err
	}
	certificate, err := parseCertificatePEM(certificatePEM)
	if err != nil {
		return Leaf{}, fmt.Errorf("load local certificate: %w", err)
	}
	privateKey, err := parsePrivateKeyPEM(privateKeyPEM)
	if err != nil {
		return Leaf{}, fmt.Errorf("load local certificate private key: %w", err)
	}
	material, err := encodeMaterial(certificate, privateKey)
	if err != nil {
		return Leaf{}, fmt.Errorf("load local certificate material: %w", err)
	}
	tlsCertificate, err := tls.X509KeyPair(material.CertificatePEM, material.PrivateKeyPEM)
	if err != nil {
		return Leaf{}, fmt.Errorf("load local certificate pair: %w", err)
	}

	authority.mutex.Lock()
	defer authority.mutex.Unlock()
	now, err := currentTime(authority.config.Now)
	if err != nil {
		return Leaf{}, err
	}
	if err := authority.validateRoot(now); err != nil {
		return Leaf{}, fmt.Errorf("load local certificate: %w", err)
	}
	if err := authority.verifyLeaf(certificate, canonicalHosts, now); err != nil {
		return Leaf{}, fmt.Errorf("validate loaded local certificate: %w", err)
	}
	tlsCertificate.Leaf = certificate
	return Leaf{
		Hosts:          append([]string(nil), canonicalHosts...),
		Material:       material,
		TLSCertificate: tlsCertificate,
	}, nil
}

// Issue generates one short-lived server certificate for registered exact .test domains.
func (authority *Authority) Issue(ctx context.Context, hosts []string) (Leaf, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	canonicalHosts, err := canonicalizeHosts(hosts)
	if err != nil {
		return Leaf{}, err
	}
	if err := ctx.Err(); err != nil {
		return Leaf{}, err
	}

	authority.mutex.Lock()
	defer authority.mutex.Unlock()
	if err := ctx.Err(); err != nil {
		return Leaf{}, err
	}
	now, err := currentTime(authority.config.Now)
	if err != nil {
		return Leaf{}, err
	}
	if err := authority.validateRoot(now); err != nil {
		return Leaf{}, fmt.Errorf("issue local certificate: %w", err)
	}
	notBefore := now.Add(-authority.config.Backdate)
	if notBefore.Before(authority.certificate.NotBefore) {
		notBefore = authority.certificate.NotBefore
	}
	notAfter := now.Add(authority.config.LeafValidity)
	if notAfter.After(authority.certificate.NotAfter) {
		notAfter = authority.certificate.NotAfter
	}
	if !notAfter.After(now) || !notAfter.After(notBefore) {
		return Leaf{}, fmt.Errorf("issue local certificate: CA expires before a usable leaf can be issued")
	}
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), authority.config.Random)
	if err != nil {
		return Leaf{}, fmt.Errorf("generate local certificate private key: %w", err)
	}
	serial, err := randomSerial(authority.config.Random)
	if err != nil {
		return Leaf{}, fmt.Errorf("generate local certificate serial: %w", err)
	}
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName: canonicalHosts[0],
		},
		DNSNames:              canonicalHosts,
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  false,
		SubjectKeyId:          subjectKeyID(&privateKey.PublicKey),
		AuthorityKeyId:        append([]byte(nil), authority.certificate.SubjectKeyId...),
	}
	der, err := x509.CreateCertificate(authority.config.Random, template, authority.certificate, &privateKey.PublicKey, authority.privateKey)
	if err != nil {
		return Leaf{}, fmt.Errorf("create local certificate: %w", err)
	}
	certificate, err := x509.ParseCertificate(der)
	if err != nil {
		return Leaf{}, fmt.Errorf("parse generated local certificate: %w", err)
	}
	material, err := encodeMaterial(certificate, privateKey)
	if err != nil {
		return Leaf{}, err
	}
	tlsCertificate, err := tls.X509KeyPair(material.CertificatePEM, material.PrivateKeyPEM)
	if err != nil {
		return Leaf{}, fmt.Errorf("load generated local certificate pair: %w", err)
	}
	tlsCertificate.Leaf = certificate
	if err := authority.verifyLeaf(certificate, canonicalHosts, now); err != nil {
		return Leaf{}, fmt.Errorf("verify generated local certificate: %w", err)
	}
	return Leaf{
		Hosts:          append(make([]string, 0, len(canonicalHosts)), canonicalHosts...),
		Material:       material,
		TLSCertificate: tlsCertificate,
	}, nil
}

// validateRoot proves constraints, current validity, self-signature, and private-key ownership together.
func (authority *Authority) validateRoot(now time.Time) error {
	certificate := authority.certificate
	if certificate.SerialNumber == nil || certificate.SerialNumber.Sign() <= 0 {
		return fmt.Errorf("CA certificate must have a positive serial number")
	}
	if !certificate.IsCA || !certificate.BasicConstraintsValid {
		return fmt.Errorf("certificate is not a constrained CA")
	}
	if !certificate.MaxPathLenZero || certificate.MaxPathLen != 0 {
		return fmt.Errorf("certificate must prohibit subordinate CAs")
	}
	if certificate.KeyUsage != x509.KeyUsageCertSign|x509.KeyUsageCRLSign {
		return fmt.Errorf("certificate lacks CA signing key usage")
	}
	if len(certificate.ExtKeyUsage) != 0 || len(certificate.UnknownExtKeyUsage) != 0 || len(certificate.DNSNames) != 0 || len(certificate.IPAddresses) != 0 || len(certificate.EmailAddresses) != 0 || len(certificate.URIs) != 0 {
		return fmt.Errorf("CA certificate must not contain leaf usages or names")
	}
	if certificate.PermittedDNSDomainsCritical || len(certificate.PermittedDNSDomains) != 0 || len(certificate.ExcludedDNSDomains) != 0 || len(certificate.PermittedIPRanges) != 0 || len(certificate.ExcludedIPRanges) != 0 || len(certificate.PermittedEmailAddresses) != 0 || len(certificate.ExcludedEmailAddresses) != 0 || len(certificate.PermittedURIDomains) != 0 || len(certificate.ExcludedURIDomains) != 0 {
		return fmt.Errorf("CA certificate must not contain name constraints")
	}
	if len(certificate.OCSPServer) != 0 || len(certificate.IssuingCertificateURL) != 0 {
		return fmt.Errorf("CA certificate must not contain authority information access locations")
	}
	if len(certificate.CRLDistributionPoints) != 0 {
		return fmt.Errorf("CA certificate must not contain CRL distribution points")
	}
	if len(certificate.PolicyIdentifiers) != 0 || len(certificate.Policies) != 0 || certificate.InhibitAnyPolicy > 0 || certificate.InhibitAnyPolicyZero || certificate.InhibitPolicyMapping > 0 || certificate.InhibitPolicyMappingZero || certificate.RequireExplicitPolicy > 0 || certificate.RequireExplicitPolicyZero || len(certificate.PolicyMappings) != 0 {
		return fmt.Errorf("CA certificate must not contain certificate policies")
	}
	if len(certificate.UnhandledCriticalExtensions) != 0 {
		return fmt.Errorf("CA certificate contains an unsupported critical extension")
	}
	for _, extension := range certificate.Extensions {
		switch extension.Id.String() {
		case rootSubjectKeyID, rootKeyUsage, rootBasicConstraint:
		default:
			return fmt.Errorf("CA certificate contains unsupported extension %s", extension.Id.String())
		}
	}
	if certificate.Subject.CommonName == "" || len(certificate.Subject.CommonName) > maximumCommonName {
		return fmt.Errorf("CA certificate has an invalid common name")
	}
	publicKey, ok := certificate.PublicKey.(*ecdsa.PublicKey)
	if !ok || publicKey.Curve != elliptic.P256() {
		return fmt.Errorf("CA certificate must use ECDSA P-256")
	}
	if certificate.SignatureAlgorithm != x509.ECDSAWithSHA256 {
		return fmt.Errorf("CA certificate must use ECDSA with SHA-256")
	}
	if !certificate.NotAfter.After(certificate.NotBefore) || certificate.NotAfter.Sub(certificate.NotBefore) > maximumCAValidity+maximumBackdate {
		return fmt.Errorf("CA certificate has an invalid lifetime")
	}
	if now.Before(certificate.NotBefore) || !now.Before(certificate.NotAfter) {
		return fmt.Errorf("CA certificate is not currently valid")
	}
	if !bytes.Equal(certificate.RawSubject, certificate.RawIssuer) {
		return fmt.Errorf("CA certificate is not self-issued")
	}
	if err := certificate.CheckSignatureFrom(certificate); err != nil {
		return fmt.Errorf("CA certificate is not self-signed: %w", err)
	}
	if authority.privateKey.Curve != elliptic.P256() || !authority.privateKey.PublicKey.Equal(publicKey) {
		return fmt.Errorf("CA private key does not match certificate")
	}
	if len(certificate.SubjectKeyId) == 0 || !bytes.Equal(certificate.SubjectKeyId, subjectKeyID(publicKey)) {
		return fmt.Errorf("CA certificate has an invalid subject key identifier")
	}
	return nil
}

// verifyLeaf checks every SAN against the retained root with server-auth semantics.
func (authority *Authority) verifyLeaf(certificate *x509.Certificate, hosts []string, now time.Time) error {
	if certificate.SerialNumber == nil || certificate.SerialNumber.Sign() <= 0 {
		return fmt.Errorf("leaf certificate must have a positive serial number")
	}
	if certificate.IsCA || !certificate.BasicConstraintsValid || certificate.MaxPathLen != -1 || certificate.MaxPathLenZero {
		return fmt.Errorf("leaf certificate has invalid basic constraints")
	}
	if certificate.KeyUsage != x509.KeyUsageDigitalSignature {
		return fmt.Errorf("leaf certificate has invalid key usage")
	}
	if len(certificate.ExtKeyUsage) != 1 || certificate.ExtKeyUsage[0] != x509.ExtKeyUsageServerAuth {
		return fmt.Errorf("leaf certificate is not server-auth only")
	}
	if len(certificate.UnknownExtKeyUsage) != 0 {
		return fmt.Errorf("leaf certificate contains an unknown extended key usage")
	}
	if !bytes.Equal(certificate.AuthorityKeyId, authority.certificate.SubjectKeyId) {
		return fmt.Errorf("leaf certificate has an invalid authority key identifier")
	}
	publicKey, ok := certificate.PublicKey.(*ecdsa.PublicKey)
	if !ok || publicKey.Curve != elliptic.P256() {
		return fmt.Errorf("leaf certificate must use ECDSA P-256")
	}
	if len(certificate.SubjectKeyId) == 0 || !bytes.Equal(certificate.SubjectKeyId, subjectKeyID(publicKey)) {
		return fmt.Errorf("leaf certificate has an invalid subject key identifier")
	}
	if certificate.SignatureAlgorithm != x509.ECDSAWithSHA256 {
		return fmt.Errorf("leaf certificate must use ECDSA with SHA-256")
	}
	if certificate.Subject.CommonName != hosts[0] || !slices.Equal(certificate.DNSNames, hosts) {
		return fmt.Errorf("leaf certificate names do not match the requested exact hosts")
	}
	if len(certificate.IPAddresses) != 0 || len(certificate.EmailAddresses) != 0 || len(certificate.URIs) != 0 {
		return fmt.Errorf("leaf certificate must contain DNS names only")
	}
	if certificate.PermittedDNSDomainsCritical || len(certificate.PermittedDNSDomains) != 0 || len(certificate.ExcludedDNSDomains) != 0 || len(certificate.PermittedIPRanges) != 0 || len(certificate.ExcludedIPRanges) != 0 || len(certificate.PermittedEmailAddresses) != 0 || len(certificate.ExcludedEmailAddresses) != 0 || len(certificate.PermittedURIDomains) != 0 || len(certificate.ExcludedURIDomains) != 0 {
		return fmt.Errorf("leaf certificate must not contain name constraints")
	}
	if len(certificate.OCSPServer) != 0 || len(certificate.IssuingCertificateURL) != 0 {
		return fmt.Errorf("leaf certificate must not contain authority information access locations")
	}
	if len(certificate.CRLDistributionPoints) != 0 {
		return fmt.Errorf("leaf certificate must not contain CRL distribution points")
	}
	if len(certificate.PolicyIdentifiers) != 0 || len(certificate.Policies) != 0 || certificate.InhibitAnyPolicy > 0 || certificate.InhibitAnyPolicyZero || certificate.InhibitPolicyMapping > 0 || certificate.InhibitPolicyMappingZero || certificate.RequireExplicitPolicy > 0 || certificate.RequireExplicitPolicyZero || len(certificate.PolicyMappings) != 0 {
		return fmt.Errorf("leaf certificate must not contain certificate policies")
	}
	if len(certificate.UnhandledCriticalExtensions) != 0 {
		return fmt.Errorf("leaf certificate contains an unsupported critical extension")
	}
	for _, extension := range certificate.Extensions {
		switch extension.Id.String() {
		case rootSubjectKeyID, rootKeyUsage, rootBasicConstraint, leafSubjectAltName, leafAuthorityKeyID, leafExtendedUsage:
		default:
			return fmt.Errorf("leaf certificate contains unsupported extension %s", extension.Id.String())
		}
	}
	if certificate.NotBefore.Before(authority.certificate.NotBefore) || certificate.NotAfter.After(authority.certificate.NotAfter) || !certificate.NotAfter.After(certificate.NotBefore) {
		return fmt.Errorf("leaf certificate lifetime escapes its CA")
	}
	if certificate.NotAfter.Sub(certificate.NotBefore) > maximumLeafValidity+maximumBackdate {
		return fmt.Errorf("leaf certificate exceeds Harbor's maximum lifetime")
	}
	if err := certificate.CheckSignatureFrom(authority.certificate); err != nil {
		return fmt.Errorf("leaf certificate signature is invalid: %w", err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(authority.certificate)
	for _, host := range hosts {
		if _, err := certificate.Verify(x509.VerifyOptions{
			DNSName:     host,
			Roots:       roots,
			KeyUsages:   []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
			CurrentTime: now,
		}); err != nil {
			return fmt.Errorf("verify host %q: %w", host, err)
		}
	}
	return nil
}

// normalizeConfig supplies secure defaults without changing explicit invalid values.
func normalizeConfig(config Config) Config {
	if config.CommonName == "" {
		config.CommonName = defaultCommonName
	}
	if config.CAValidity == 0 {
		config.CAValidity = defaultCAValidity
	}
	if config.LeafValidity == 0 {
		config.LeafValidity = defaultLeafValidity
	}
	if config.Backdate == 0 {
		config.Backdate = defaultBackdate
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	if config.Random == nil {
		config.Random = rand.Reader
	}
	return config
}

// validateConfig rejects weak or operationally unreasonable certificate policy.
func validateConfig(config Config) error {
	if strings.TrimSpace(config.CommonName) != config.CommonName || config.CommonName == "" {
		return fmt.Errorf("local CA common name must be nonempty without surrounding whitespace")
	}
	if len(config.CommonName) > maximumCommonName {
		return fmt.Errorf("local CA common name must not exceed %d bytes", maximumCommonName)
	}
	if config.CAValidity < time.Hour || config.CAValidity > maximumCAValidity {
		return fmt.Errorf("local CA validity must be between 1h and %s", maximumCAValidity)
	}
	if config.LeafValidity < time.Minute || config.LeafValidity > maximumLeafValidity {
		return fmt.Errorf("local certificate validity must be between 1m and %s", maximumLeafValidity)
	}
	if config.Backdate < 0 || config.Backdate > maximumBackdate {
		return fmt.Errorf("local certificate backdate must be between zero and %s", maximumBackdate)
	}
	if config.Backdate >= config.CAValidity || config.Backdate >= config.LeafValidity {
		return fmt.Errorf("local certificate backdate must be shorter than both certificate lifetimes")
	}
	return nil
}

// currentTime normalizes certificate instants to UTC while rejecting unusable clocks.
func currentTime(now func() time.Time) (time.Time, error) {
	current := now().UTC().Round(0)
	if current.IsZero() {
		return time.Time{}, fmt.Errorf("local certificate clock returned zero time")
	}
	return current, nil
}

// randomSerial creates a positive 128-bit serial within RFC 5280's size bound.
func randomSerial(random io.Reader) (*big.Int, error) {
	serialBytes := make([]byte, 16)
	if _, err := io.ReadFull(random, serialBytes); err != nil {
		return nil, err
	}
	serialBytes[0] &= 0x7f
	serial := new(big.Int).SetBytes(serialBytes)
	if serial.Sign() == 0 {
		serial.SetInt64(1)
	}
	return serial, nil
}

// subjectKeyID derives a stable non-secret identifier from the public signing key.
func subjectKeyID(publicKey crypto.PublicKey) []byte {
	der, err := x509.MarshalPKIXPublicKey(publicKey)
	if err != nil {
		panic(fmt.Sprintf("localca: marshal validated public key: %v", err))
	}
	digest := sha256.Sum256(der)
	return append([]byte(nil), digest[:20]...)
}

// encodeMaterial serializes only Harbor's supported ECDSA P-256 key format and validates the pair.
func encodeMaterial(certificate *x509.Certificate, privateKey *ecdsa.PrivateKey) (Material, error) {
	if privateKey.Curve != elliptic.P256() {
		return Material{}, fmt.Errorf("encode certificate material: private key must use ECDSA P-256")
	}
	publicKey, ok := certificate.PublicKey.(*ecdsa.PublicKey)
	if !ok || !privateKey.PublicKey.Equal(publicKey) {
		return Material{}, fmt.Errorf("encode certificate material: private key does not match certificate")
	}
	privateDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		return Material{}, fmt.Errorf("encode certificate private key: %w", err)
	}
	certificatePEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate.Raw})
	privateKeyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER})
	if _, err := tls.X509KeyPair(certificatePEM, privateKeyPEM); err != nil {
		return Material{}, fmt.Errorf("validate encoded certificate pair: %w", err)
	}
	return Material{
		CertificatePEM: certificatePEM,
		PrivateKeyPEM:  privateKeyPEM,
		Fingerprint:    certificateFingerprint(certificate),
		NotBefore:      certificate.NotBefore.UTC(),
		NotAfter:       certificate.NotAfter.UTC(),
	}, nil
}

// parseCertificatePEM accepts only one complete block so corrupt persistence cannot be partially accepted.
func parseCertificatePEM(data []byte) (*x509.Certificate, error) {
	block, rest := pem.Decode(data)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, fmt.Errorf("expected one CERTIFICATE PEM block")
	}
	consumed := data[:len(data)-len(rest)]
	if bytes.LastIndex(consumed, []byte("-----BEGIN CERTIFICATE-----")) != 0 {
		return nil, fmt.Errorf("certificate PEM contains leading data")
	}
	if len(block.Headers) != 0 {
		return nil, fmt.Errorf("certificate PEM headers are not supported")
	}
	if len(bytes.TrimSpace(rest)) != 0 {
		return nil, fmt.Errorf("certificate PEM contains trailing data")
	}
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse certificate: %w", err)
	}
	return certificate, nil
}

// parsePrivateKeyPEM accepts only one complete block so corrupt persistence cannot be partially accepted.
func parsePrivateKeyPEM(data []byte) (*ecdsa.PrivateKey, error) {
	block, rest := pem.Decode(data)
	if block == nil || block.Type != "PRIVATE KEY" {
		return nil, fmt.Errorf("expected one unencrypted PRIVATE KEY PEM block")
	}
	consumed := data[:len(data)-len(rest)]
	if bytes.LastIndex(consumed, []byte("-----BEGIN PRIVATE KEY-----")) != 0 {
		return nil, fmt.Errorf("private key PEM contains leading data")
	}
	if len(block.Headers) != 0 {
		return nil, fmt.Errorf("private key PEM headers are not supported")
	}
	if len(bytes.TrimSpace(rest)) != 0 {
		return nil, fmt.Errorf("private key PEM contains trailing data")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse PKCS#8 private key: %w", err)
	}
	privateKey, ok := parsed.(*ecdsa.PrivateKey)
	if !ok || privateKey.Curve != elliptic.P256() {
		return nil, fmt.Errorf("private key must use ECDSA P-256")
	}
	return privateKey, nil
}

// canonicalizeHosts validates, deduplicates, and sorts an exact SAN set.
func canonicalizeHosts(hosts []string) ([]string, error) {
	if len(hosts) == 0 {
		return nil, fmt.Errorf("local certificate must contain at least one DNS host")
	}
	if len(hosts) > maximumLeafHosts {
		return nil, fmt.Errorf("local certificate must not contain more than %d DNS hosts", maximumLeafHosts)
	}
	result := make([]string, 0, len(hosts))
	seen := make(map[string]struct{}, len(hosts))
	for index, host := range hosts {
		canonical, err := canonicalDomain(host)
		if err != nil {
			return nil, fmt.Errorf("local certificate host %d: %w", index, err)
		}
		if _, exists := seen[canonical]; exists {
			return nil, fmt.Errorf("local certificate host %q is duplicated", canonical)
		}
		seen[canonical] = struct{}{}
		result = append(result, canonical)
	}
	sort.Strings(result)
	return result, nil
}

// canonicalDomain applies Harbor's portable ASCII exact-name policy without accepting wildcards.
func canonicalDomain(raw string) (string, error) {
	if raw == "" || strings.TrimSpace(raw) != raw {
		return "", fmt.Errorf("DNS host must be nonempty without surrounding whitespace")
	}
	if strings.HasSuffix(raw, ".") {
		raw = strings.TrimSuffix(raw, ".")
	}
	host := strings.ToLower(raw)
	if len(host) > maximumDomainLength {
		return "", fmt.Errorf("DNS host must not exceed %d bytes", maximumDomainLength)
	}
	if !strings.HasSuffix(host, ".test") || host == ".test" {
		return "", fmt.Errorf("DNS host %q must be beneath .test", host)
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", fmt.Errorf("DNS host %q contains an invalid label", host)
		}
		for _, character := range label {
			if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || character == '-' {
				continue
			}
			return "", fmt.Errorf("DNS host %q must contain only ASCII letters, digits, dots, and hyphens", host)
		}
	}
	return host, nil
}

// certificateFingerprint gives persistence and trust adapters one exact public identity.
func certificateFingerprint(certificate *x509.Certificate) string {
	digest := sha256.Sum256(certificate.Raw)
	return hex.EncodeToString(digest[:])
}

// cloneMaterial prevents callers from mutating authority-owned encoded key material.
func cloneMaterial(material Material) Material {
	material.CertificatePEM = append([]byte(nil), material.CertificatePEM...)
	material.PrivateKeyPEM = append([]byte(nil), material.PrivateKeyPEM...)
	return material
}
