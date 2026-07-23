package projectprocess

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/goforj/harbor/internal/domain"
)

// TestOutputBrokerJournalReplaysExactFrames proves durable append precedes cursor-addressed replay.
func TestOutputBrokerJournalReplaysExactFrames(t *testing.T) {
	directory := t.TempDir()
	projectID := domain.ProjectID("project-broker")
	sessionID := domain.SessionID("session-broker")
	journal, err := OpenOutputBrokerJournal(directory, projectID, sessionID)
	if err != nil {
		t.Fatalf("OpenOutputBrokerJournal() error = %v", err)
	}
	first, err := journal.Append(OutputBrokerStreamStdout, 0, []byte("one\n"))
	if err != nil {
		t.Fatalf("append stdout: %v", err)
	}
	second, err := journal.Append(OutputBrokerStreamStderr, first.NextCursor, []byte("two\n"))
	if err != nil {
		t.Fatalf("append stderr: %v", err)
	}
	if first.Stream != OutputBrokerStreamStdout || second.Stream != OutputBrokerStreamStderr || journal.NextCursor() != 8 {
		t.Fatalf("frames/cursor = %#v / %#v / %d", first, second, journal.NextCursor())
	}
	replay := journal.Replay(0)
	if err := replay.Validate(); err != nil {
		t.Fatalf("replay validation: %v", err)
	}
	if replay.Reset || replay.Truncated || replay.HasMore || replay.NextCursor != 8 || len(replay.Frames) != 2 || replay.Frames[0].Text != "one\n" || replay.Frames[1].Text != "two\n" {
		t.Fatalf("replay = %#v, want exact ordered frames", replay)
	}
	if err := journal.Close(); err != nil {
		t.Fatalf("close journal: %v", err)
	}
}

// TestOutputBrokerJournalAppendIsIdempotent proves a retry cannot duplicate an uncertain final write.
func TestOutputBrokerJournalAppendIsIdempotent(t *testing.T) {
	journal, err := OpenOutputBrokerJournal(t.TempDir(), "project-broker-idempotent", "session-broker-idempotent")
	if err != nil {
		t.Fatalf("OpenOutputBrokerJournal() error = %v", err)
	}
	defer func() { _ = journal.Close() }()
	first, err := journal.Append(OutputBrokerStreamStdout, 0, []byte("same"))
	if err != nil {
		t.Fatalf("first append: %v", err)
	}
	retried, err := journal.Append(OutputBrokerStreamStdout, 0, []byte("same"))
	if err != nil || retried != first {
		t.Fatalf("idempotent append = %#v, %v, want %#v", retried, err, first)
	}
	if journal.NextCursor() != 4 {
		t.Fatalf("idempotent append advanced cursor to %d", journal.NextCursor())
	}
	if _, err := journal.Append(OutputBrokerStreamStdout, 0, []byte("different")); !errors.Is(err, ErrOutputBrokerCursorConflict) {
		t.Fatalf("conflicting retry error = %v, want cursor conflict", err)
	}
	if _, err := journal.Append(OutputBrokerStreamStdout, 5, []byte("future")); !errors.Is(err, ErrOutputBrokerCursorConflict) {
		t.Fatalf("future cursor error = %v, want cursor conflict", err)
	}
}

// TestOutputBrokerJournalResumesCrashTail proves the broker can reopen complete frames without accepting a torn tail.
func TestOutputBrokerJournalResumesCrashTail(t *testing.T) {
	directory := t.TempDir()
	projectID := domain.ProjectID("project-broker-resume")
	sessionID := domain.SessionID("session-broker-resume")
	journal, err := OpenOutputBrokerJournal(directory, projectID, sessionID)
	if err != nil {
		t.Fatalf("OpenOutputBrokerJournal() error = %v", err)
	}
	if _, err := journal.Append(OutputBrokerStreamStdout, 0, []byte("complete\n")); err != nil {
		t.Fatalf("append complete frame: %v", err)
	}
	if _, err := journal.spool.file.Write([]byte("HOF1\x01")); err != nil {
		t.Fatalf("write crash tail: %v", err)
	}
	if err := journal.spool.file.Close(); err != nil {
		t.Fatalf("close crashed journal: %v", err)
	}

	resumed, err := OpenOutputBrokerJournal(directory, projectID, sessionID)
	if err != nil {
		t.Fatalf("reopen journal: %v", err)
	}
	defer func() { _ = resumed.Close() }()
	replay := resumed.Replay(0)
	if len(replay.Frames) != 1 || replay.Frames[0].Text != "complete\n" || replay.NextCursor != 9 {
		t.Fatalf("resumed replay = %#v, want complete frame only", replay)
	}
}

// TestOutputBrokerJournalReportsTruncationPreservesAbsoluteCursor proves compaction is a visible cursor boundary.
func TestOutputBrokerJournalReportsTruncationPreservesAbsoluteCursor(t *testing.T) {
	journal, err := OpenOutputBrokerJournal(t.TempDir(), "project-broker-truncate", "session-broker-truncate")
	if err != nil {
		t.Fatalf("OpenOutputBrokerJournal() error = %v", err)
	}
	defer func() { _ = journal.Close() }()
	journal.spool.maximum = 140
	cursor := uint64(0)
	for _, text := range []string{strings.Repeat("a", 20), strings.Repeat("b", 20), strings.Repeat("c", 20)} {
		frame, appendErr := journal.Append(OutputBrokerStreamStdout, cursor, []byte(text))
		if appendErr != nil {
			t.Fatalf("append %q: %v", text[:1], appendErr)
		}
		cursor = frame.NextCursor
	}
	replay := journal.Replay(0)
	if !replay.Truncated || replay.Reset || replay.NextCursor != 60 || len(replay.Frames) != 1 || replay.Frames[0].Text != strings.Repeat("c", 20) {
		t.Fatalf("truncated replay = %#v, want absolute retained suffix", replay)
	}
}

// TestOutputBrokerSubscriptionEmitsGapsAndIdempotentAcks proves slow readers receive explicit loss rather than false continuity.
func TestOutputBrokerSubscriptionEmitsGapsAndIdempotentAcks(t *testing.T) {
	journal, err := OpenOutputBrokerJournal(t.TempDir(), "project-broker-subscribe", "session-broker-subscribe")
	if err != nil {
		t.Fatalf("OpenOutputBrokerJournal() error = %v", err)
	}
	defer func() { _ = journal.Close() }()
	_, subscription, err := journal.Subscribe(0, 1)
	if err != nil {
		t.Fatalf("Subscribe() error = %v", err)
	}
	defer func() { _ = subscription.Close() }()
	first, err := journal.Append(OutputBrokerStreamStdout, 0, []byte("first"))
	if err != nil {
		t.Fatalf("append first: %v", err)
	}
	firstRecord := <-subscription.Records()
	if firstRecord.Frame == nil || firstRecord.Frame.Text != "first" {
		t.Fatalf("first subscription record = %#v", firstRecord)
	}
	if err := subscription.Ack(first.NextCursor); err != nil {
		t.Fatalf("first ack: %v", err)
	}
	if err := subscription.Ack(first.NextCursor); err != nil {
		t.Fatalf("idempotent first ack: %v", err)
	}
	second, err := journal.Append(OutputBrokerStreamStdout, first.NextCursor, []byte("second"))
	if err != nil {
		t.Fatalf("append second: %v", err)
	}
	third, err := journal.Append(OutputBrokerStreamStdout, second.NextCursor, []byte("third"))
	if err != nil {
		t.Fatalf("append third: %v", err)
	}
	if err := subscription.Ack(third.NextCursor + 1); err == nil {
		t.Fatal("future acknowledgement passed validation")
	}
	fourth, err := journal.Append(OutputBrokerStreamStdout, third.NextCursor, []byte("fourth"))
	if err != nil {
		t.Fatalf("append fourth: %v", err)
	}
	secondRecord := <-subscription.Records()
	if secondRecord.Frame == nil || secondRecord.Frame.Text != "second" {
		t.Fatalf("second subscription record = %#v", secondRecord)
	}
	_, err = journal.Append(OutputBrokerStreamStdout, fourth.NextCursor, []byte("fifth"))
	if err != nil {
		t.Fatalf("append fifth: %v", err)
	}
	gapRecord := <-subscription.Records()
	if gapRecord.Gap == nil || gapRecord.Gap.StartCursor != third.Cursor || gapRecord.Gap.EndCursor != fourth.NextCursor || gapRecord.Gap.DroppedRecords != 2 {
		if gapRecord.Gap == nil {
			t.Fatalf("gap subscription record = %#v, want dropped third/fourth frames", gapRecord)
		}
		t.Fatalf("gap subscription record = %+v, want start=%d end=%d dropped=2", *gapRecord.Gap, third.Cursor, fourth.NextCursor)
	}
	if err := gapRecord.Validate(); err != nil {
		t.Fatalf("gap record validation: %v", err)
	}
}

// TestOutputBrokerSubscriptionAcknowledgesBufferedReplayInOrder proves startup output cannot disconnect the live stream.
func TestOutputBrokerSubscriptionAcknowledgesBufferedReplayInOrder(t *testing.T) {
	journal, err := OpenOutputBrokerJournal(t.TempDir(), "project-broker-replay-ack", "session-broker-replay-ack")
	if err != nil {
		t.Fatalf("OpenOutputBrokerJournal() error = %v", err)
	}
	defer func() { _ = journal.Close() }()

	first, err := journal.Append(OutputBrokerStreamStdout, 0, []byte("pre-dev\n"))
	if err != nil {
		t.Fatalf("append first replay frame: %v", err)
	}
	second, err := journal.Append(OutputBrokerStreamStdout, first.NextCursor, []byte("compose\n"))
	if err != nil {
		t.Fatalf("append second replay frame: %v", err)
	}
	replay, subscription, err := journal.Subscribe(0, 1)
	if err != nil {
		t.Fatalf("Subscribe() error = %v", err)
	}
	defer func() { _ = subscription.Close() }()
	if len(replay.Frames) != 2 {
		t.Fatalf("replay frames = %d, want 2", len(replay.Frames))
	}
	if err := subscription.Ack(first.NextCursor); err != nil {
		t.Fatalf("ack first replay frame: %v", err)
	}
	if err := subscription.Ack(second.NextCursor); err != nil {
		t.Fatalf("ack second replay frame: %v", err)
	}

	live, err := journal.Append(OutputBrokerStreamStdout, second.NextCursor, []byte("ready\n"))
	if err != nil {
		t.Fatalf("append live frame: %v", err)
	}
	record := <-subscription.Records()
	if record.Frame == nil || *record.Frame != live {
		t.Fatalf("live record = %#v, want %#v", record, live)
	}
	if err := subscription.Ack(live.NextCursor); err != nil {
		t.Fatalf("ack live frame: %v", err)
	}
}

// TestOutputBrokerValidationRejectsUnboundedOrAmbiguousRecords keeps future transport decoders fail closed.
func TestOutputBrokerValidationRejectsUnboundedOrAmbiguousRecords(t *testing.T) {
	if err := (OutputBrokerFrame{Stream: OutputBrokerStreamStdout, Cursor: 4, NextCursor: 3, Text: "x"}).Validate(); err == nil {
		t.Fatal("reversed broker frame passed validation")
	}
	if err := (OutputBrokerFrame{Stream: OutputBrokerStreamStdout, Cursor: 0, NextCursor: uint64(MaximumOutputBrokerReplayBytes + 1), Text: strings.Repeat("x", MaximumOutputBrokerReplayBytes+1)}).Validate(); err == nil {
		t.Fatal("oversized broker frame passed validation")
	}
	if err := (OutputBrokerGap{StartCursor: 0, EndCursor: 1}).Validate(); err == nil {
		t.Fatal("zero-count broker gap passed validation")
	}
	if err := (OutputBrokerRecord{}).Validate(); err == nil {
		t.Fatal("empty broker record passed validation")
	}
	if err := (OutputBrokerRecord{Frame: &OutputBrokerFrame{}, Gap: &OutputBrokerGap{}}).Validate(); err == nil {
		t.Fatal("ambiguous broker record passed validation")
	}
}

// TestOutputBrokerJournalRejectsInvalidConstruction verifies durable broker identity is required before use.
func TestOutputBrokerJournalRejectsInvalidConstruction(t *testing.T) {
	if _, err := OpenOutputBrokerJournal("", "project-broker", "session-broker"); err == nil {
		t.Fatal("empty broker journal directory passed construction")
	}
	if err := (OutputBrokerStream(99)).Validate(); err == nil {
		t.Fatal("unsupported broker stream passed validation")
	}
	journal, err := OpenOutputBrokerJournal(t.TempDir(), "project-broker-close", "session-broker-close")
	if err != nil {
		t.Fatalf("OpenOutputBrokerJournal() error = %v", err)
	}
	if err := journal.Close(); err != nil {
		t.Fatalf("close journal: %v", err)
	}
	if _, err := journal.Append(OutputBrokerStreamStdout, 0, []byte("late")); !errors.Is(err, ErrOutputBrokerJournalClosed) {
		t.Fatalf("late append error = %v, want closed journal", err)
	}
}

// TestOutputBrokerJournalDoesNotFollowOutputWithInvalidUTF8 proves normalization happens before persistence and notification.
func TestOutputBrokerJournalDoesNotFollowOutputWithInvalidUTF8(t *testing.T) {
	journal, err := OpenOutputBrokerJournal(t.TempDir(), "project-broker-utf8", "session-broker-utf8")
	if err != nil {
		t.Fatalf("OpenOutputBrokerJournal() error = %v", err)
	}
	defer func() { _ = journal.Close() }()
	frame, err := journal.Append(OutputBrokerStreamStderr, 0, []byte{'x', 0xff, 'y'})
	if err != nil {
		t.Fatalf("append invalid UTF-8: %v", err)
	}
	if frame.Text != "x\uFFFDy" || !utf8.ValidString(frame.Text) {
		t.Fatalf("normalized frame = %#v", frame)
	}
}
