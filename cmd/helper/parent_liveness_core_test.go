package main

import (
	"context"
	"errors"
	"io"
	"os"
	"testing"
	"time"
)

// TestRunWithParentLivenessReturnsNormalCompletion proves helper completion does not wait for the parent's EOF.
func TestRunWithParentLivenessReturnsNormalCompletion(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("open liveness pipe: %v", err)
	}
	defer writer.Close()

	want := errors.New("operation complete")
	got := runWithParentLiveness(t.Context(), reader, func(context.Context) error {
		return want
	})
	if !errors.Is(got, want) {
		t.Fatalf("run result = %v, want %v", got, want)
	}
}

// TestRunWithParentLivenessStopsWorkAtParentEOF proves losing the launching process terminates active helper work.
func TestRunWithParentLivenessStopsWorkAtParentEOF(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("open liveness pipe: %v", err)
	}
	defer reader.Close()
	started := make(chan struct{})
	canceled := make(chan struct{})
	cleanupAllowed := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		result <- runWithParentLiveness(context.Background(), reader, func(ctx context.Context) error {
			close(started)
			<-ctx.Done()
			close(canceled)
			<-cleanupAllowed
			return ctx.Err()
		})
	}()
	<-started
	if err := writer.Close(); err != nil {
		t.Fatalf("close liveness writer: %v", err)
	}

	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("active work was not canceled after parent liveness EOF")
	}
	select {
	case err := <-result:
		t.Fatalf("helper returned before canceled work was reaped: %v", err)
	default:
	}
	close(cleanupAllowed)
	if err := <-result; !errors.Is(err, errParentLivenessLost) {
		t.Fatalf("run result = %v, want parent liveness loss", err)
	}
}

// TestRunWithParentLivenessBoundsUncooperativeWork verifies a blocked native call cannot keep an orphaned helper alive indefinitely.
func TestRunWithParentLivenessBoundsUncooperativeWork(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("open liveness pipe: %v", err)
	}
	defer reader.Close()
	runRelease := make(chan struct{})
	runReturned := make(chan struct{})
	defer func() {
		close(runRelease)
		<-runReturned
	}()

	result := make(chan error, 1)
	go func() {
		result <- runWithParentLivenessGrace(context.Background(), reader, func(context.Context) error {
			<-runRelease
			close(runReturned)
			return nil
		}, 10*time.Millisecond)
	}()
	if err := writer.Close(); err != nil {
		t.Fatalf("close liveness writer: %v", err)
	}

	select {
	case err := <-result:
		if !errors.Is(err, errParentLivenessLost) {
			t.Fatalf("run result = %v, want parent liveness loss", err)
		}
	case <-time.After(time.Second):
		t.Fatal("uncooperative work kept the orphaned helper alive")
	}
}

// TestRunWithParentLivenessFailsClosedWithoutAReader covers absent liveness inheritance.
func TestRunWithParentLivenessFailsClosedWithoutAReader(t *testing.T) {
	err := runWithParentLiveness(t.Context(), nil, func(context.Context) error { return nil })
	if err == nil {
		t.Fatal("run succeeded without a parent liveness reader")
	}
}

// TestAwaitParentLivenessRejectsInvalidInput covers invalid inherited liveness descriptors and protocol data.
func TestAwaitParentLivenessRejectsInvalidInput(t *testing.T) {
	tests := []struct {
		name   string
		reader io.Reader
	}{
		{
			name:   "descriptor error",
			reader: errorParentLivenessReader{err: errors.New("bad descriptor")},
		},
		{
			name:   "unexpected data",
			reader: errorParentLivenessReader{body: []byte{1}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := awaitParentLiveness(test.reader); err == nil {
				t.Fatal("invalid parent liveness input was accepted")
			}
		})
	}
}

// errorParentLivenessReader returns deterministic liveness data or errors without an operating-system descriptor.
type errorParentLivenessReader struct {
	body []byte
	err  error
}

// Read returns the configured protocol input for parent-liveness validation tests.
func (reader errorParentLivenessReader) Read(body []byte) (int, error) {
	if len(reader.body) > 0 {
		body[0] = reader.body[0]
		return 1, reader.err
	}
	return 0, reader.err
}
