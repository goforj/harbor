package helper

import (
	"strings"
	"testing"
	"time"
)

// TestReplayClaimValidate covers every durable replay identity and lifetime boundary.
func TestReplayClaimValidate(t *testing.T) {
	now := time.Date(2026, time.July, 18, 12, 0, 0, 0, time.UTC)
	valid := ReplayClaim{
		Key: ReplayKey{
			InstallationID:      "harbor-test-installation",
			OwnershipGeneration: 1,
			Nonce:               strings.Repeat("n", minimumNonceLength),
		},
		ExpiresAt: now.Add(time.Minute),
	}
	if err := valid.Validate(now); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	tests := []struct {
		name   string
		mutate func(*ReplayClaim)
	}{
		{name: "installation", mutate: func(claim *ReplayClaim) { claim.Key.InstallationID = "-invalid" }},
		{name: "generation", mutate: func(claim *ReplayClaim) { claim.Key.OwnershipGeneration = 0 }},
		{name: "nonce", mutate: func(claim *ReplayClaim) { claim.Key.Nonce = "short" }},
		{name: "zero expiry", mutate: func(claim *ReplayClaim) { claim.ExpiresAt = time.Time{} }},
		{name: "expired", mutate: func(claim *ReplayClaim) { claim.ExpiresAt = now }},
		{name: "non UTC", mutate: func(claim *ReplayClaim) { claim.ExpiresAt = claim.ExpiresAt.In(time.FixedZone("offset", 3600)) }},
		{name: "excessive lifetime", mutate: func(claim *ReplayClaim) { claim.ExpiresAt = now.Add(MaxTicketLifetime + time.Nanosecond) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := valid
			test.mutate(&candidate)
			if err := candidate.Validate(now); err == nil {
				t.Fatal("Validate() accepted an invalid replay claim")
			}
		})
	}
}
