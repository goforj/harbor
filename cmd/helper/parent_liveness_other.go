//go:build !darwin

package main

import "context"

// runWithPlatformParentLiveness preserves existing non-Darwin helper process contracts.
func runWithPlatformParentLiveness(run parentLivenessRunner) error {
	return run(context.Background())
}

// parentLivenessFailed is false where the native launcher has no inherited liveness contract.
func parentLivenessFailed(error) bool {
	return false
}
