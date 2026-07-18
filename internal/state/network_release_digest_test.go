package state

import (
	"net/netip"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/network/identity"
)

// TestProjectNetworkReleaseSetDigestIsCanonical proves ordering and process-local time zones cannot alter one release-set identity.
func TestProjectNetworkReleaseSetDigestIsCanonical(t *testing.T) {
	releases := projectNetworkReleaseDigestTestReleases()
	original := slices.Clone(releases)
	digest := projectNetworkReleaseSetDigest(releases)
	if err := validateProjectNetworkReleaseSetDigest(digest); err != nil {
		t.Fatalf("generated digest is invalid: %v", err)
	}
	if len(digest) != 64 || strings.Contains(digest, "verified") {
		t.Fatalf("digest %q is not fixed canonical hex", digest)
	}
	if !reflect.DeepEqual(releases, original) {
		t.Fatal("digest computation reordered caller-owned releases")
	}

	reversed := slices.Clone(releases)
	slices.Reverse(reversed)
	if got := projectNetworkReleaseSetDigest(reversed); got != digest {
		t.Fatalf("reversed digest = %q, want %q", got, digest)
	}

	localized := slices.Clone(releases)
	zone := time.FixedZone("test-offset", -7*60*60)
	for index := range localized {
		localized[index].ReleasedAt = localized[index].ReleasedAt.In(zone)
		localized[index].QuarantinedAt = localized[index].QuarantinedAt.In(zone)
		localized[index].ReuseAfter = localized[index].ReuseAfter.In(zone)
	}
	if got := projectNetworkReleaseSetDigest(localized); got != digest {
		t.Fatalf("localized digest = %q, want %q", got, digest)
	}

	if removed := projectNetworkReleaseSetDigest(releases[:1]); removed == digest {
		t.Fatal("removing a release did not affect digest")
	}
	appended := append(slices.Clone(releases), releases[0])
	if got := projectNetworkReleaseSetDigest(appended); got == digest {
		t.Fatal("appending a release did not affect digest")
	}
}

// TestProjectNetworkReleaseSetDigestCommitsEveryReleaseField proves no persisted teardown fact can change behind an existing replay identity.
func TestProjectNetworkReleaseSetDigestCommitsEveryReleaseField(t *testing.T) {
	baseline := projectNetworkReleaseDigestTestReleases()[:1]
	wantDifferent := []struct {
		name   string
		mutate func(*NetworkLeaseRelease)
	}{
		{name: "project", mutate: func(release *NetworkLeaseRelease) { release.Lease.Key.ProjectID = "project-beta" }},
		{name: "key kind", mutate: func(release *NetworkLeaseRelease) { release.Lease.Key.SecondaryID = "" }},
		{name: "secondary key", mutate: func(release *NetworkLeaseRelease) { release.Lease.Key.SecondaryID = "cache" }},
		{name: "address", mutate: func(release *NetworkLeaseRelease) { release.Lease.Address = netip.MustParseAddr("127.77.0.12") }},
		{name: "ownership installation", mutate: func(release *NetworkLeaseRelease) { release.Lease.Ownership.InstallationID = "installation-b" }},
		{name: "ownership generation", mutate: func(release *NetworkLeaseRelease) { release.Lease.Ownership.Generation++ }},
		{name: "release generation", mutate: func(release *NetworkLeaseRelease) { release.ReleaseGeneration++ }},
		{name: "release evidence", mutate: func(release *NetworkLeaseRelease) { release.ReleaseEvidence += " updated" }},
		{name: "released time", mutate: func(release *NetworkLeaseRelease) { release.ReleasedAt = release.ReleasedAt.Add(time.Nanosecond) }},
		{name: "quarantined time", mutate: func(release *NetworkLeaseRelease) { release.QuarantinedAt = release.QuarantinedAt.Add(time.Nanosecond) }},
		{name: "reuse time", mutate: func(release *NetworkLeaseRelease) { release.ReuseAfter = release.ReuseAfter.Add(time.Nanosecond) }},
		{name: "quarantine reason", mutate: func(release *NetworkLeaseRelease) { release.QuarantineReason += " updated" }},
	}
	want := projectNetworkReleaseSetDigest(baseline)
	for _, test := range wantDifferent {
		t.Run(test.name, func(t *testing.T) {
			changed := slices.Clone(baseline)
			test.mutate(&changed[0])
			if got := projectNetworkReleaseSetDigest(changed); got == want {
				t.Fatalf("changed %s retained digest %q", test.name, got)
			}
		})
	}
}

// TestProjectNetworkReleaseSetDigestPreservesFieldBoundaries proves adjacent text values cannot alias by moving bytes between fields.
func TestProjectNetworkReleaseSetDigestPreservesFieldBoundaries(t *testing.T) {
	left := projectNetworkReleaseDigestTestReleases()[:1]
	left[0].ReleaseEvidence = "ab"
	left[0].QuarantineReason = "c"
	right := slices.Clone(left)
	right[0].ReleaseEvidence = "a"
	right[0].QuarantineReason = "bc"

	if projectNetworkReleaseSetDigest(left) == projectNetworkReleaseSetDigest(right) {
		t.Fatal("length-ambiguous text fields produced the same digest")
	}
}

// TestValidateProjectNetworkReleaseSetDigestRequiresCanonicalHex covers every persisted representation boundary.
func TestValidateProjectNetworkReleaseSetDigestRequiresCanonicalHex(t *testing.T) {
	valid := projectNetworkReleaseSetDigest(projectNetworkReleaseDigestTestReleases())
	if err := validateProjectNetworkReleaseSetDigest(valid); err != nil {
		t.Fatalf("valid digest rejected: %v", err)
	}

	invalid := []string{
		"",
		strings.Repeat("a", 63),
		strings.Repeat("a", 65),
		strings.Repeat("A", 64),
		strings.Repeat("g", 64),
		strings.Repeat("a", 63) + "\n",
		strings.Repeat("a", 62) + "é",
	}
	for _, value := range invalid {
		if err := validateProjectNetworkReleaseSetDigest(value); err == nil {
			t.Fatalf("invalid digest %q was accepted", value)
		}
	}
}

// projectNetworkReleaseDigestTestReleases returns deliberately unsorted valid release facts with subsecond timestamps.
func projectNetworkReleaseDigestTestReleases() []NetworkLeaseRelease {
	base := time.Date(2026, time.July, 18, 12, 0, 0, 123456789, time.UTC)
	return []NetworkLeaseRelease{
		{
			Lease: identity.Lease{
				Key: identity.LeaseKey{
					ProjectID:   domain.ProjectID("project-alpha"),
					SecondaryID: "metrics",
				},
				Address: netip.MustParseAddr("127.77.0.11"),
				Ownership: identity.Ownership{
					InstallationID: identity.InstallationID("installation-a"),
					Generation:     3,
				},
			},
			ReleaseGeneration: 7,
			ReleaseEvidence:   "verified secondary release",
			ReleasedAt:        base,
			QuarantinedAt:     base.Add(time.Second),
			ReuseAfter:        base.Add(5 * time.Minute),
			QuarantineReason:  "project unregister safety window",
		},
		{
			Lease: identity.Lease{
				Key:     identity.LeaseKey{ProjectID: domain.ProjectID("project-alpha")},
				Address: netip.MustParseAddr("127.77.0.10"),
				Ownership: identity.Ownership{
					InstallationID: identity.InstallationID("installation-a"),
					Generation:     3,
				},
			},
			ReleaseGeneration: 6,
			ReleaseEvidence:   "verified primary release",
			ReleasedAt:        base.Add(-time.Second),
			QuarantinedAt:     base,
			ReuseAfter:        base.Add(5 * time.Minute),
			QuarantineReason:  "project unregister safety window",
		},
	}
}
