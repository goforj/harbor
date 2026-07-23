package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"
)

const parentLivenessCancellationGrace = time.Second

var errParentLivenessLost = errors.New("helper parent liveness was lost")

// parentLivenessRunner performs one helper invocation while its launching parent remains alive.
type parentLivenessRunner func(context.Context) error

// runWithParentLiveness cancels work when the inherited parent-lifetime pipe reaches EOF.
func runWithParentLiveness(ctx context.Context, liveness io.ReadCloser, run parentLivenessRunner) error {
	return runWithParentLivenessGrace(ctx, liveness, run, parentLivenessCancellationGrace)
}

// runWithParentLivenessGrace gives context-aware child processes a bounded opportunity to terminate before the helper hard-exits.
func runWithParentLivenessGrace(ctx context.Context, liveness io.ReadCloser, run parentLivenessRunner, cancellationGrace time.Duration) error {
	if liveness == nil {
		return errors.New("helper parent liveness reader is required")
	}
	if run == nil {
		return errors.New("helper parent liveness runner is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	runContext, cancel := context.WithCancel(ctx)
	defer cancel()

	runResult := make(chan error, 1)
	go func() {
		runResult <- run(runContext)
	}()
	livenessResult := make(chan error, 1)
	go func() {
		livenessResult <- awaitParentLiveness(liveness)
	}()

	select {
	case err := <-runResult:
		_ = liveness.Close()
		return err
	case err := <-livenessResult:
		cancel()
		timer := time.NewTimer(cancellationGrace)
		defer timer.Stop()
		select {
		case <-runResult:
		case <-timer.C:
		}
		return errors.Join(errParentLivenessLost, err)
	}
}

// awaitParentLiveness accepts only EOF because the launcher never writes to the liveness pipe.
func awaitParentLiveness(reader io.Reader) error {
	var probe [1]byte
	count, err := reader.Read(probe[:])
	if count != 0 {
		return errors.New("helper parent liveness pipe contains data")
	}
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return io.ErrNoProgress
	}
	return fmt.Errorf("read helper parent liveness pipe: %w", err)
}
