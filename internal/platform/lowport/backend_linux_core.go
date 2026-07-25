package lowport

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"

	"github.com/goforj/harbor/internal/host/networkpolicy"
)

const (
	ubuntuNFTTableName         = "goforj_harbor"
	ubuntuNFTChainName         = "output"
	ubuntuNFTOwnerPrefix       = "goforj.harbor.nft.v1|"
	ubuntuNFTHTTPComment       = "goforj.harbor.http.v1"
	ubuntuNFTHTTPSComment      = "goforj.harbor.https.v1"
	ubuntuNFTTableAbsentDomain = "goforj.harbor.ubuntu-nftables.table.absent.v1\x00"
	ubuntuNFTRulesAbsentDomain = "goforj.harbor.ubuntu-nftables.rules.absent.v1\x00"
)

// ubuntuNFTSnapshot contains the bounded fixed-table facts needed for one low-port decision.
type ubuntuNFTSnapshot struct {
	TablePresent     bool
	TableOwned       bool
	TableExact       bool
	TableAmbiguous   bool
	TableFingerprint string
	RulesPresent     bool
	RulesOwned       bool
	RulesExact       bool
	RulesAmbiguous   bool
	RulesFingerprint string
}

// ubuntuNFTNative confines native effects to Harbor's fixed inet table.
type ubuntuNFTNative interface {
	snapshot(context.Context, Request) (ubuntuNFTSnapshot, error)
	ensure(context.Context, Request) error
	release(context.Context, Request) error
}

// ubuntuNFTBackend translates bounded nftables facts into the portable low-port model.
type ubuntuNFTBackend struct {
	native ubuntuNFTNative
}

// newUbuntuNFTBackend injects the native boundary for portable ownership and compare-and-swap tests.
func newUbuntuNFTBackend(native ubuntuNFTNative) backend {
	if native == nil {
		panic("lowport.newUbuntuNFTBackend requires a non-nil native boundary")
	}
	return &ubuntuNFTBackend{native: native}
}

// observe converts one complete fixed-table snapshot into canonical low-port facts.
func (backend *ubuntuNFTBackend) observe(ctx context.Context, request Request) (Observation, error) {
	if err := validateUbuntuNFTRequest(request); err != nil {
		return Observation{}, err
	}
	snapshot, err := backend.native.snapshot(ctx, request)
	if err != nil {
		return Observation{}, err
	}
	if !snapshot.TablePresent {
		snapshot.TableFingerprint = ubuntuNFTAbsentFingerprint(ubuntuNFTTableAbsentDomain)
	}
	if !snapshot.RulesPresent {
		snapshot.RulesFingerprint = ubuntuNFTAbsentFingerprint(ubuntuNFTRulesAbsentDomain)
	}
	observation := Observation{
		Request:  request,
		Complete: true,
		Artifacts: []Artifact{
			{
				Kind:        ArtifactKindNFTTable,
				Present:     snapshot.TablePresent,
				Owned:       snapshot.TableOwned,
				Exact:       snapshot.TableExact,
				Ambiguous:   snapshot.TableAmbiguous,
				Fingerprint: snapshot.TableFingerprint,
			},
			{
				Kind:        ArtifactKindNFTRules,
				Present:     snapshot.RulesPresent,
				Owned:       snapshot.RulesOwned,
				Exact:       snapshot.RulesExact,
				Ambiguous:   snapshot.RulesAmbiguous,
				Fingerprint: snapshot.RulesFingerprint,
			},
		},
	}
	if err := observation.Validate(); err != nil {
		return Observation{}, err
	}
	return observation, nil
}

// ensure creates only the fixed table after the portable adapter proves complete absence.
func (backend *ubuntuNFTBackend) ensure(ctx context.Context, request Request, before Observation) error {
	if err := validateUbuntuNFTRequest(request); err != nil {
		return err
	}
	if err := before.Validate(); err != nil {
		return err
	}
	if classifyValidated(before) != StateAbsent {
		return fmt.Errorf("Ubuntu nftables ensure requires an absent fixed table: %w", errNativeMutationConflict)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return backend.native.ensure(ctx, request)
}

// release deletes only the complete exact fixed table admitted by the portable adapter.
func (backend *ubuntuNFTBackend) release(ctx context.Context, request Request, before Observation) error {
	if err := validateUbuntuNFTRequest(request); err != nil {
		return err
	}
	if err := before.Validate(); err != nil {
		return err
	}
	if classifyValidated(before) != StateExact {
		return fmt.Errorf("Ubuntu nftables release requires one exact owned table: %w", errNativeMutationConflict)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return backend.native.release(ctx, request)
}

// validateUbuntuNFTRequest confines this backend to the declared redirected Ubuntu profile.
func validateUbuntuNFTRequest(request Request) error {
	if err := request.Validate(); err != nil {
		return err
	}
	if request.Mechanism() != networkpolicy.UbuntuNFTables {
		return fmt.Errorf("Ubuntu nftables backend rejected mechanism %q", request.Mechanism())
	}
	if request.HTTPUpstream().Addr() != canonicalLocalhost || request.HTTPSUpstream().Addr() != canonicalLocalhost {
		return fmt.Errorf("Ubuntu nftables upstreams must use canonical localhost")
	}
	return nil
}

// ubuntuNFTOwnerComment binds the fixed table to the exact user and complete host policy.
func ubuntuNFTOwnerComment(request Request) string {
	return ubuntuNFTOwnerPrefix + strconv.FormatUint(uint64(request.OwnerUID()), 10) + "|" + request.PolicyFingerprint()
}

// ubuntuNFTAbsentFingerprint returns stable evidence for one absent fixed artifact.
func ubuntuNFTAbsentFingerprint(domain string) string {
	digest := sha256.Sum256([]byte(domain))
	return hex.EncodeToString(digest[:])
}
