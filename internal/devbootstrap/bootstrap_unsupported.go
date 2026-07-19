//go:build !darwin && !linux

package devbootstrap

import "github.com/goforj/harbor/internal/platform/machinepaths"

// platformEffectiveUID has no privileged identity on unsupported development hosts.
func platformEffectiveUID() int {
	return -1
}

// applyPlatformPlan rejects platforms without reviewed Unix filesystem primitives.
func applyPlatformPlan(plan) error {
	return machinepaths.ErrUnsupported
}
