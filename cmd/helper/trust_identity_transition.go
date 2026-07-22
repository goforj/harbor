package main

import (
	"fmt"
	"strconv"
)

// trustIdentityState records the process identities on one side of the irreversible boundary.
type trustIdentityState struct {
	realUID      uint32
	effectiveUID uint32
}

// trustIdentityTransition performs one platform-atomic identity drop and returns its before-and-after proof.
type trustIdentityTransition func(uint32) (trustIdentityState, trustIdentityState, error)

// transitionTrustIdentity binds one platform-atomic identity drop to the authenticated non-root requester.
func transitionTrustIdentity(requester string, transition trustIdentityTransition) error {
	if transition == nil {
		return fmt.Errorf("trust identity transition is required")
	}
	parsed, err := strconv.ParseUint(requester, 10, 32)
	if err != nil || parsed == 0 || strconv.FormatUint(parsed, 10) != requester {
		return fmt.Errorf("trust requester does not identify a canonical non-root Unix UID")
	}
	target := uint32(parsed)
	before, after, err := transition(target)
	if err != nil {
		return fmt.Errorf("irreversibly drop trust identity: %w", err)
	}
	if before.realUID != target {
		return fmt.Errorf("trust requester does not match the invoking real identity")
	}
	if before.effectiveUID != 0 {
		return fmt.Errorf("trust identity transition requires root effective identity")
	}
	if after.realUID != target || after.effectiveUID != target {
		return fmt.Errorf("trust identity drop did not bind real and effective identities to requester")
	}
	return nil
}
