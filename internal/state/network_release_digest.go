package state

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"hash"
	"slices"
	"strconv"
	"time"
)

// projectNetworkReleaseSetDigestDomain prevents this payload format from aliasing another SHA-256 use or a future release-set version.
const projectNetworkReleaseSetDigestDomain = "goforj.harbor/network-project-release-set:v1"

// projectNetworkReleaseSetDigest binds replay evidence to the exact canonical host release facts committed by the store.
func projectNetworkReleaseSetDigest(releases []NetworkLeaseRelease) string {
	ordered := slices.Clone(releases)
	slices.SortFunc(ordered, func(left NetworkLeaseRelease, right NetworkLeaseRelease) int {
		if networkLeaseLess(left.Lease, right.Lease) {
			return -1
		}
		if networkLeaseLess(right.Lease, left.Lease) {
			return 1
		}
		return 0
	})

	hasher := sha256.New()
	writeProjectNetworkReleaseDigestField(hasher, projectNetworkReleaseSetDigestDomain)
	writeProjectNetworkReleaseDigestField(hasher, strconv.Itoa(len(ordered)))
	for _, release := range ordered {
		writeProjectNetworkReleaseDigestField(hasher, string(release.Lease.Key.ProjectID))
		writeProjectNetworkReleaseDigestField(hasher, string(release.Lease.Key.Kind()))
		writeProjectNetworkReleaseDigestField(hasher, release.Lease.Key.SecondaryID)
		writeProjectNetworkReleaseDigestField(hasher, release.Lease.Address.Unmap().String())
		writeProjectNetworkReleaseDigestField(hasher, string(release.Lease.Ownership.InstallationID))
		writeProjectNetworkReleaseDigestField(hasher, strconv.FormatUint(release.Lease.Ownership.Generation, 10))
		writeProjectNetworkReleaseDigestField(hasher, strconv.FormatUint(release.ReleaseGeneration, 10))
		writeProjectNetworkReleaseDigestField(hasher, release.ReleaseEvidence)
		writeProjectNetworkReleaseDigestField(hasher, release.ReleasedAt.UTC().Format(time.RFC3339Nano))
		writeProjectNetworkReleaseDigestField(hasher, release.QuarantinedAt.UTC().Format(time.RFC3339Nano))
		writeProjectNetworkReleaseDigestField(hasher, release.ReuseAfter.UTC().Format(time.RFC3339Nano))
		writeProjectNetworkReleaseDigestField(hasher, release.QuarantineReason)
	}
	return hex.EncodeToString(hasher.Sum(nil))
}

// validateProjectNetworkReleaseSetDigest rejects values outside the canonical SHA-256 representation persisted by Harbor.
func validateProjectNetworkReleaseSetDigest(value string) error {
	if len(value) != sha256.Size*2 {
		return fmt.Errorf("project network release set digest must be %d lowercase hexadecimal characters", sha256.Size*2)
	}
	for _, character := range []byte(value) {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return fmt.Errorf("project network release set digest must be %d lowercase hexadecimal characters", sha256.Size*2)
		}
	}
	return nil
}

// writeProjectNetworkReleaseDigestField preserves field boundaries so concatenation aliases cannot share one replay identity.
func writeProjectNetworkReleaseDigestField(hasher hash.Hash, value string) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(value)))
	_, _ = hasher.Write(length[:])
	_, _ = hasher.Write([]byte(value))
}
