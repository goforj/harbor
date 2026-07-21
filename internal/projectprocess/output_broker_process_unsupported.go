//go:build !darwin && !linux && !windows

package projectprocess

import (
	"errors"
	"os"
)

// prepareOutputBrokerProcess keeps unsupported platforms fail-closed until their inherited-handle contract is proven.
func prepareOutputBrokerProcess(string, string, *os.File, *os.File) (*outputBrokerProcess, []string, error) {
	return nil, nil, errors.New("output broker process launch is unsupported on this platform")
}
