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
		settled: func(_ context.Context, _ runtimeRepairReceipt) (bool, error) {
			return false, fmt.Errorf("runtime repair settlement observation is unsupported on this platform")
		},
	}
}
