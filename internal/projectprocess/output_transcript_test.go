package projectprocess

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/goforj/harbor/internal/domain"
)

// TestOutputRelayTranscriptPreservesOrderedValidText verifies the serialized relay is the transcript's only producer.
func TestOutputRelayTranscriptPreservesOrderedValidText(t *testing.T) {
	relay := newOutputRelay(io.Discard, io.Discard, 8)
	relay.offer(outputStreamStdout, []byte("first\n"))
	relay.offer(outputStreamStderr, []byte{'b', 'a', 'd', '-', 0xff, '\n'})
	relay.offer(outputStreamStdout, []byte("last\n"))
	relay.finish()

	chunk := relay.transcript.read(0)
	want := "first\nbad-\uFFFD\nlast\n"
	if !chunk.Available || chunk.Reset || chunk.Truncated || chunk.HasMore || chunk.Text != want {
		t.Fatalf("transcript chunk = %#v, want complete text %q", chunk, want)
	}
	if chunk.NextCursor != uint64(len(want)) || !utf8.ValidString(chunk.Text) {
		t.Fatalf("transcript cursor/UTF-8 = %d/%t, want %d/true", chunk.NextCursor, utf8.ValidString(chunk.Text), len(want))
	}
}

// TestOutputTranscriptWrapRetainsByteOrder verifies physical wrap does not reorder the retained suffix.
func TestOutputTranscriptWrapRetainsByteOrder(t *testing.T) {
	transcript := newOutputTranscript(10)
	transcript.append([]byte("abcdefghij"))
	transcript.append([]byte("kl"))

	chunk := transcript.read(2)
	if chunk.Text != "cdefghijkl" || chunk.NextCursor != 12 || chunk.HasMore || chunk.Reset || chunk.Truncated {
		t.Fatalf("wrapped chunk = %#v", chunk)
	}
}

// TestOutputTranscriptReportsEvictionAndAheadReset keeps lost history distinct from a caller-created future cursor.
func TestOutputTranscriptReportsEvictionAndAheadReset(t *testing.T) {
	transcript := newOutputTranscript(10)
	transcript.append([]byte("abcdefghijkl"))

	evicted := transcript.read(0)
	if !evicted.Truncated || evicted.Reset || evicted.Text != "cdefghijkl" || evicted.NextCursor != 12 {
		t.Fatalf("evicted chunk = %#v", evicted)
	}
	ahead := transcript.read(99)
	if !ahead.Reset || ahead.Truncated || ahead.Text != "cdefghijkl" || ahead.NextCursor != 12 {
		t.Fatalf("ahead chunk = %#v", ahead)
	}
}

// TestOutputTranscriptKeepsEvictionAndResetOnUTF8Boundaries verifies neither ring pressure nor an arbitrary cursor exposes partial text.
func TestOutputTranscriptKeepsEvictionAndResetOnUTF8Boundaries(t *testing.T) {
	transcript := newOutputTranscript(5)
	transcript.append([]byte("界ab"))
	transcript.append([]byte("c"))

	evicted := transcript.read(0)
	if !evicted.Truncated || evicted.Text != "abc" || evicted.NextCursor != 6 || !utf8.ValidString(evicted.Text) {
		t.Fatalf("UTF-8 eviction chunk = %#v", evicted)
	}

	transcript = newOutputTranscript(10)
	transcript.append([]byte("a界b"))
	misaligned := transcript.read(2)
	if !misaligned.Reset || misaligned.Truncated || misaligned.Text != "a界b" || misaligned.NextCursor != 5 {
		t.Fatalf("misaligned cursor chunk = %#v", misaligned)
	}
}

// TestOutputTranscriptChunksOnUTF8Boundaries verifies every response stays within the byte ceiling without splitting a rune.
func TestOutputTranscriptChunksOnUTF8Boundaries(t *testing.T) {
	transcript := newOutputTranscript(outputTranscriptCapacityBytes)
	want := strings.Repeat("界", MaximumOutputChunkBytes/3+200)
	transcript.append([]byte(want))

	first := transcript.read(0)
	if len(first.Text) > MaximumOutputChunkBytes || !utf8.ValidString(first.Text) || !first.HasMore {
		t.Fatalf("first chunk bytes/UTF-8/more = %d/%t/%t", len(first.Text), utf8.ValidString(first.Text), first.HasMore)
	}
	second := transcript.read(first.NextCursor)
	if len(second.Text) > MaximumOutputChunkBytes || !utf8.ValidString(second.Text) || second.HasMore {
		t.Fatalf("second chunk bytes/UTF-8/more = %d/%t/%t", len(second.Text), utf8.ValidString(second.Text), second.HasMore)
	}
	if got := first.Text + second.Text; got != want {
		t.Fatalf("joined chunks contain %d bytes, want %d", len(got), len(want))
	}
}

// TestOutputTranscriptBoundsOneOversizedAppend verifies retention never exceeds the fixed ring even for an internal oversized write.
func TestOutputTranscriptBoundsOneOversizedAppend(t *testing.T) {
	transcript := newOutputTranscript(outputTranscriptCapacityBytes)
	transcript.append([]byte(strings.Repeat("é", outputTranscriptCapacityBytes)))

	transcript.mu.Lock()
	retained := transcript.length
	transcript.mu.Unlock()
	if retained > outputTranscriptCapacityBytes {
		t.Fatalf("retained bytes = %d, want at most %d", retained, outputTranscriptCapacityBytes)
	}

	cursor := uint64(0)
	var output strings.Builder
	firstRead := true
	for {
		chunk := transcript.read(cursor)
		if !utf8.ValidString(chunk.Text) || len(chunk.Text) > MaximumOutputChunkBytes {
			t.Fatalf("chunk bytes/UTF-8 = %d/%t", len(chunk.Text), utf8.ValidString(chunk.Text))
		}
		if firstRead && !chunk.Truncated {
			t.Fatal("oversized transcript did not report its evicted prefix")
		}
		firstRead = false
		output.WriteString(chunk.Text)
		cursor = chunk.NextCursor
		if !chunk.HasMore {
			break
		}
	}
	if output.Len() != retained || output.Len() > outputTranscriptCapacityBytes || !utf8.ValidString(output.String()) {
		t.Fatalf("retained output bytes/UTF-8 = %d/%t, want %d/true", output.Len(), utf8.ValidString(output.String()), retained)
	}
}

// TestSupervisorReadOutputRequiresExactLiveIdentities prevents one project or session from reading another process transcript.
func TestSupervisorReadOutputRequiresExactLiveIdentities(t *testing.T) {
	relay := newOutputRelay(io.Discard, io.Discard, 2)
	relay.offer(outputStreamStdout, []byte("owned\n"))
	relay.finish()
	process := &managedProcess{relay: relay}
	supervisor := &Supervisor{
		projects: map[domain.ProjectID]*managedProcess{"project-owned": process},
		sessions: map[domain.SessionID]*managedProcess{"session-owned": process},
	}

	owned := supervisor.ReadOutput("project-owned", "session-owned", 0)
	if !owned.Available || owned.Text != "owned\n" {
		t.Fatalf("owned output = %#v", owned)
	}
	for _, selection := range []struct {
		project domain.ProjectID
		session domain.SessionID
	}{
		{project: "project-other", session: "session-owned"},
		{project: "project-owned", session: "session-other"},
		{project: "project-other", session: "session-other"},
	} {
		if chunk := supervisor.ReadOutput(selection.project, selection.session, 0); chunk != (OutputChunk{}) {
			t.Fatalf("unowned output for %q/%q = %#v", selection.project, selection.session, chunk)
		}
	}
}

// TestSupervisorWaitOutputWakesForAppend proves output cannot arrive between the caught-up read and waiter registration unnoticed.
func TestSupervisorWaitOutputWakesForAppend(t *testing.T) {
	relay := newOutputRelay(io.Discard, io.Discard, 2)
	defer relay.finish()
	supervisor := outputTestSupervisor(relay)
	result := make(chan OutputChunk, 1)
	errors := make(chan error, 1)
	go func() {
		chunk, err := supervisor.WaitOutput(t.Context(), "project-owned", "session-owned", 0)
		result <- chunk
		errors <- err
	}()

	relay.transcript.append([]byte("ready\n"))
	select {
	case chunk := <-result:
		if err := <-errors; err != nil {
			t.Fatalf("WaitOutput() error = %v", err)
		}
		if !chunk.Available || chunk.Text != "ready\n" || chunk.NextCursor != 6 {
			t.Fatalf("WaitOutput() = %#v", chunk)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("WaitOutput() did not wake for appended output")
	}
}

// TestSupervisorWaitOutputHonorsCancellationAndDeadline keeps abandoned long polls from retaining control handlers.
func TestSupervisorWaitOutputHonorsCancellationAndDeadline(t *testing.T) {
	relay := newOutputRelay(io.Discard, io.Discard, 2)
	defer relay.finish()
	supervisor := outputTestSupervisor(relay)

	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if chunk, err := supervisor.WaitOutput(cancelled, "project-owned", "session-owned", 0); !errors.Is(err, context.Canceled) || chunk != (OutputChunk{}) {
		t.Fatalf("cancelled WaitOutput() = %#v, %v", chunk, err)
	}

	expired, cancelDeadline := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer cancelDeadline()
	if chunk, err := supervisor.WaitOutput(expired, "project-owned", "session-owned", 0); !errors.Is(err, context.DeadlineExceeded) || chunk != (OutputChunk{}) {
		t.Fatalf("expired WaitOutput() = %#v, %v", chunk, err)
	}
}

// TestSupervisorWaitOutputRequiresExactLiveIdentities prevents one long poll from following another process or session.
func TestSupervisorWaitOutputRequiresExactLiveIdentities(t *testing.T) {
	relay := newOutputRelay(io.Discard, io.Discard, 2)
	defer relay.finish()
	supervisor := outputTestSupervisor(relay)
	for _, selection := range []struct {
		project domain.ProjectID
		session domain.SessionID
	}{
		{project: "project-other", session: "session-owned"},
		{project: "project-owned", session: "session-other"},
		{project: "project-other", session: "session-other"},
	} {
		chunk, err := supervisor.WaitOutput(t.Context(), selection.project, selection.session, 0)
		if err != nil || chunk != (OutputChunk{}) {
			t.Fatalf("WaitOutput(%q, %q) = %#v, %v", selection.project, selection.session, chunk, err)
		}
	}
}

// TestSupervisorWaitOutputWakesWhenProcessOutputCloses makes process exit observable without another output append.
func TestSupervisorWaitOutputWakesWhenProcessOutputCloses(t *testing.T) {
	relay := newOutputRelay(io.Discard, io.Discard, 2)
	supervisor := outputTestSupervisor(relay)
	result := make(chan OutputChunk, 1)
	errors := make(chan error, 1)
	go func() {
		chunk, err := supervisor.WaitOutput(t.Context(), "project-owned", "session-owned", 0)
		result <- chunk
		errors <- err
	}()

	relay.finish()
	select {
	case chunk := <-result:
		if err := <-errors; err != nil || chunk != (OutputChunk{}) {
			t.Fatalf("closed WaitOutput() = %#v, %v", chunk, err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("WaitOutput() did not wake when process output closed")
	}
}

// TestOutputRelayTranscriptIgnoresBlockedCaller proves UI or terminal backpressure cannot stop transcript delivery.
func TestOutputRelayTranscriptIgnoresBlockedCaller(t *testing.T) {
	writer := newBlockingWriter()
	defer close(writer.release)
	relay := newOutputRelay(writer, writer, 4)
	relay.offer(outputStreamStdout, []byte("first\n"))
	select {
	case <-writer.started:
	case <-time.After(5 * time.Second):
		t.Fatal("caller writer was not reached")
	}
	relay.offer(outputStreamStderr, []byte("second\n"))
	relay.finish()

	chunk := relay.transcript.read(0)
	if chunk.Text != "first\nsecond\n" || chunk.HasMore || chunk.Truncated || chunk.Reset {
		t.Fatalf("blocked-caller transcript = %#v", chunk)
	}
}

// TestOutputTranscriptSupportsConcurrentReaders verifies cursor reads remain race-free while the relay appends output.
func TestOutputTranscriptSupportsConcurrentReaders(t *testing.T) {
	transcript := newOutputTranscript(outputTranscriptCapacityBytes)
	const writes = 2000
	const readers = 4
	errors := make(chan error, readers)
	start := make(chan struct{})
	var wait sync.WaitGroup
	wait.Add(1 + readers)
	go func() {
		defer wait.Done()
		<-start
		for index := 0; index < writes; index++ {
			transcript.append([]byte(fmt.Sprintf("line-%04d-世界\n", index)))
		}
	}()
	for range readers {
		go func() {
			defer wait.Done()
			<-start
			cursor := uint64(0)
			for range writes {
				chunk := transcript.read(cursor)
				if !chunk.Available || len(chunk.Text) > MaximumOutputChunkBytes || !utf8.ValidString(chunk.Text) {
					errors <- fmt.Errorf("invalid concurrent chunk: %#v", chunk)
					return
				}
				cursor = chunk.NextCursor
			}
		}()
	}
	close(start)
	wait.Wait()
	close(errors)
	for err := range errors {
		t.Fatal(err)
	}
}

// outputTestSupervisor binds one relay to the exact identities used by output wait tests.
func outputTestSupervisor(relay *outputRelay) *Supervisor {
	process := &managedProcess{relay: relay}
	return &Supervisor{
		projects: map[domain.ProjectID]*managedProcess{"project-owned": process},
		sessions: map[domain.SessionID]*managedProcess{"session-owned": process},
	}
}
