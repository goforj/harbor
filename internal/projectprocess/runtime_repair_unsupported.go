//go:build !darwin

package projectprocess

import (
	"context"
	"fmt"
)

// newRuntimeRepairControl returns an explicit unsupported result on platforms without reviewed native correlation.
func newRuntimeRepairControl() runtimeRepairControl {
	return runtimeRepairControl{
		inspect: func(_ context.Context, _ RuntimeRepairTarget) (runtimeRepairNativeInspection, error) {
			return runtimeRepairNativeInspection{State: RuntimeRepairInspectionUnsupported}, nil
		},
		graceful: func(_ context.Context, _ runtimeRepairReceipt) (bool, error) {
			return false, fmt.Errorf("runtime repair graceful termination is unsupported on this platform")
		},
		force: func(_ context.Context, _ runtimeRepairReceipt) (bool, error) {
			return false, fmt.Errorf("runtime repair forceful termination is unsupported on this platform")
		},
		settled: func(_ context.Context, _ runtimeRepairReceipt) (bool, error) {
			return false, fmt.Errorf("runtime repair settlement observation is unsupported on this platform")
		},
	}
}

// newUnattributedRuntimeControl returns an explicit unsupported result without inferring host-process ownership.
func newUnattributedRuntimeControl() unattributedRuntimeControl {
	return unattributedRuntimeControl{
		inspect: func(_ context.Context, _ RuntimeRepairTarget) (unattributedRuntimeNativeInspection, error) {
			return unattributedRuntimeNativeInspection{State: RuntimeRepairInspectionUnsupported}, nil
		},
		graceful: func(_ context.Context, _ unattributedRuntimeReceipt) (bool, error) {
			return false, fmt.Errorf("unattributed runtime graceful termination is unsupported on this platform")
		},
		force: func(_ context.Context, _ unattributedRuntimeReceipt) (bool, error) {
			return false, fmt.Errorf("unattributed runtime forceful termination is unsupported on this platform")
		},
		settled: func(_ context.Context, _ unattributedRuntimeReceipt) (bool, error) {
			return false, fmt.Errorf("unattributed runtime settlement observation is unsupported on this platform")
		},
	}
}
