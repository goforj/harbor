package projectprocess

import (
	"errors"
	"fmt"
	"sync"
	"unicode/utf8"

	"github.com/goforj/harbor/internal/domain"
)

const (
	// MaximumOutputBrokerReplayBytes bounds one broker replay response to the same payload ceiling as live output.
	MaximumOutputBrokerReplayBytes        = MaximumOutputChunkBytes
	maximumOutputBrokerSubscriptionBuffer = 1024
)

var (
	// ErrOutputBrokerJournalClosed prevents appends and subscriptions after journal shutdown.
	ErrOutputBrokerJournalClosed = errors.New("output broker journal is closed")
	// ErrOutputBrokerCursorConflict rejects an append that cannot be proven to be a retry of one exact frame.
	ErrOutputBrokerCursorConflict = errors.New("output broker cursor conflicts with retained journal")
	// ErrOutputBrokerSubscriptionClosed reports use of a retired live subscription.
	ErrOutputBrokerSubscriptionClosed = errors.New("output broker subscription is closed")
)

// OutputBrokerStream identifies the honest source stream retained by a broker journal.
type OutputBrokerStream uint8

const (
	// OutputBrokerStreamStdout identifies child standard output.
	OutputBrokerStreamStdout OutputBrokerStream = iota + 1
	// OutputBrokerStreamStderr identifies child standard error.
	OutputBrokerStreamStderr
)

// Validate reports whether the stream is one of the two supported child-output sources.
func (stream OutputBrokerStream) Validate() error {
	switch stream {
	case OutputBrokerStreamStdout, OutputBrokerStreamStderr:
		return nil
	default:
		return fmt.Errorf("output broker stream %d is unsupported", stream)
	}
}

// OutputBrokerFrame is one cursor-addressed, normalized output record.
type OutputBrokerFrame struct {
	// Cursor is the first output byte represented by Text.
	Cursor uint64 `json:"cursor"`
	// NextCursor is the first output byte after Text.
	NextCursor uint64 `json:"next_cursor"`
	// Stream preserves stdout/stderr provenance across replay.
	Stream OutputBrokerStream `json:"stream"`
	// Text contains valid UTF-8 output bytes.
	Text string `json:"text"`
}

// Validate reports whether a broker frame has a bounded cursor and valid text payload.
func (frame OutputBrokerFrame) Validate() error {
	if err := frame.Stream.Validate(); err != nil {
		return err
	}
	if frame.NextCursor < frame.Cursor {
		return errors.New("output broker frame cursor range is reversed")
	}
	if frame.NextCursor-frame.Cursor != uint64(len([]byte(frame.Text))) {
		return errors.New("output broker frame cursor range does not match text bytes")
	}
	if !utf8.ValidString(frame.Text) {
		return errors.New("output broker frame text must be valid UTF-8")
	}
	if len([]byte(frame.Text)) > MaximumOutputBrokerReplayBytes {
		return fmt.Errorf("output broker frame text exceeds %d bytes", MaximumOutputBrokerReplayBytes)
	}
	if frame.Cursor > uint64(domain.MaximumSequence) || frame.NextCursor > uint64(domain.MaximumSequence) {
		return fmt.Errorf("output broker frame cursor exceeds %d", domain.MaximumSequence)
	}
	return nil
}

// OutputBrokerGap identifies output dropped for one slow live subscriber.
type OutputBrokerGap struct {
	// StartCursor is the first omitted output byte.
	StartCursor uint64 `json:"start_cursor"`
	// EndCursor is the first output byte after the omitted range.
	EndCursor uint64 `json:"end_cursor"`
	// DroppedRecords counts omitted broker frames, not bytes.
	DroppedRecords uint64 `json:"dropped_records"`
}

// Validate reports whether a gap describes a positive bounded dropped range.
func (gap OutputBrokerGap) Validate() error {
	if gap.EndCursor <= gap.StartCursor {
		return errors.New("output broker gap cursor range must advance")
	}
	if gap.DroppedRecords == 0 {
		return errors.New("output broker gap must report dropped records")
	}
	if gap.EndCursor > uint64(domain.MaximumSequence) {
		return fmt.Errorf("output broker gap cursor exceeds %d", domain.MaximumSequence)
	}
	return nil
}

// OutputBrokerRecord is either one output frame or an explicit backpressure gap.
type OutputBrokerRecord struct {
	// Frame is set for an output record.
	Frame *OutputBrokerFrame `json:"frame,omitempty"`
	// Gap is set when a subscriber could not keep up with the live stream.
	Gap *OutputBrokerGap `json:"gap,omitempty"`
}

// Validate reports whether exactly one broker record variant is present.
func (record OutputBrokerRecord) Validate() error {
	if (record.Frame == nil) == (record.Gap == nil) {
		return errors.New("output broker record must contain exactly one frame or gap")
	}
	if record.Frame != nil {
		return record.Frame.Validate()
	}
	return record.Gap.Validate()
}

// OutputBrokerReplay is one bounded exact-cursor replay result.
type OutputBrokerReplay struct {
	// Reset reports that the requested cursor was ahead of or split the retained transcript.
	Reset bool `json:"reset"`
	// Truncated reports that compaction discarded bytes before the requested cursor.
	Truncated bool `json:"truncated"`
	// HasMore reports that retained frames remain after the returned frames.
	HasMore bool `json:"has_more"`
	// NextCursor is the first output byte after the returned frames.
	NextCursor uint64 `json:"next_cursor"`
	// Frames contains ordered output records bounded by MaximumOutputBrokerReplayBytes.
	Frames []OutputBrokerFrame `json:"frames"`
}

// Validate reports whether a replay result is bounded and internally cursor-contiguous.
func (replay OutputBrokerReplay) Validate() error {
	if len(replay.Frames) == 0 {
		if replay.HasMore {
			return errors.New("empty output broker replay cannot report more frames")
		}
		return nil
	}
	var previous uint64
	var bytes int
	for index, frame := range replay.Frames {
		if err := frame.Validate(); err != nil {
			return fmt.Errorf("output broker replay frame %d: %w", index, err)
		}
		if index > 0 && frame.Cursor != previous {
			return errors.New("output broker replay frames are not cursor-contiguous")
		}
		previous = frame.NextCursor
		bytes += len([]byte(frame.Text))
	}
	if bytes > MaximumOutputBrokerReplayBytes {
		return fmt.Errorf("output broker replay exceeds %d bytes", MaximumOutputBrokerReplayBytes)
	}
	if replay.NextCursor != previous {
		return errors.New("output broker replay next cursor does not match its frames")
	}
	return nil
}

// OutputBrokerJournal is the append-before-notify boundary for one project/session output stream.
//
// The journal persists through the existing owner-private checksummed spool and adds a bounded live
// subscriber surface. It carries no process signaling authority; a future broker transport must still
// authenticate the exact process evidence before exposing these records to a restarted Harbor.
type OutputBrokerJournal struct {
	mu             sync.Mutex
	spool          *outputSpool
	frames         []OutputBrokerFrame
	baseCursor     uint64
	nextCursor     uint64
	nextSubscriber uint64
	subscribers    map[uint64]*OutputBrokerSubscription
	closed         bool
}

// OpenOutputBrokerJournal opens one owner-private checksummed journal for an exact project/session pair.
func OpenOutputBrokerJournal(directory string, projectID domain.ProjectID, sessionID domain.SessionID) (*OutputBrokerJournal, error) {
	if directory == "" {
		return nil, errors.New("output broker journal directory is required")
	}
	spool, err := openOutputSpool(directory, projectID, sessionID)
	if err != nil {
		return nil, fmt.Errorf("open output broker journal spool: %w", err)
	}
	if spool == nil {
		return nil, errors.New("open output broker journal spool returned no journal")
	}
	journal := &OutputBrokerJournal{
		spool:       spool,
		frames:      outputBrokerFramesFromSpool(spool.frames),
		baseCursor:  spool.header.baseCursor,
		nextCursor:  spool.nextCursor,
		subscribers: make(map[uint64]*OutputBrokerSubscription),
	}
	return journal, nil
}

// Append persists one normalized frame before notifying live subscribers.
func (journal *OutputBrokerJournal) Append(stream OutputBrokerStream, cursor uint64, payload []byte) (OutputBrokerFrame, error) {
	if err := stream.Validate(); err != nil {
		return OutputBrokerFrame{}, err
	}
	normalized := normalizeOutputBytes(payload)
	if len(normalized) == 0 {
		return OutputBrokerFrame{}, errors.New("output broker frame payload is empty")
	}
	if len(normalized) > outputSpoolMaximumPayloadBytes {
		return OutputBrokerFrame{}, fmt.Errorf("output broker frame payload exceeds %d bytes", outputSpoolMaximumPayloadBytes)
	}

	journal.mu.Lock()
	defer journal.mu.Unlock()
	if journal.closed {
		return OutputBrokerFrame{}, ErrOutputBrokerJournalClosed
	}
	if cursor != journal.nextCursor {
		for _, existing := range journal.frames {
			if existing.Cursor == cursor && existing.Stream == stream && existing.Text == string(normalized) {
				return existing, nil
			}
		}
		return OutputBrokerFrame{}, fmt.Errorf("%w: expected %d, got %d", ErrOutputBrokerCursorConflict, journal.nextCursor, cursor)
	}
	if err := journal.spool.appendNormalized(outputBrokerInternalStream(stream), normalized); err != nil {
		return OutputBrokerFrame{}, fmt.Errorf("append output broker journal frame: %w", err)
	}
	journal.frames = outputBrokerFramesFromSpool(journal.spool.frames)
	journal.baseCursor = journal.spool.header.baseCursor
	journal.nextCursor = journal.spool.nextCursor
	if len(journal.frames) == 0 {
		return OutputBrokerFrame{}, fmt.Errorf("%w: append retained no frame", ErrOutputBrokerCursorConflict)
	}
	frame := journal.frames[len(journal.frames)-1]
	for _, subscriber := range journal.subscribers {
		subscriber.publishLocked(frame)
	}
	return frame, nil
}

// Replay returns the retained frames at one exact cursor without exposing process authority.
func (journal *OutputBrokerJournal) Replay(cursor uint64) OutputBrokerReplay {
	journal.mu.Lock()
	defer journal.mu.Unlock()
	return journal.replayLocked(cursor)
}

// Subscribe returns retained replay plus a bounded future-record channel.
func (journal *OutputBrokerJournal) Subscribe(cursor uint64, buffer int) (OutputBrokerReplay, *OutputBrokerSubscription, error) {
	if buffer <= 0 || buffer > maximumOutputBrokerSubscriptionBuffer {
		return OutputBrokerReplay{}, nil, fmt.Errorf("output broker subscription buffer must be between 1 and %d", maximumOutputBrokerSubscriptionBuffer)
	}
	journal.mu.Lock()
	defer journal.mu.Unlock()
	if journal.closed {
		return OutputBrokerReplay{}, nil, ErrOutputBrokerJournalClosed
	}
	replay := journal.replayLocked(cursor)
	ackCursor := replay.NextCursor
	if len(replay.Frames) > 0 {
		// Replay frames are acknowledged in order after the handshake, so the live subscription
		// cannot begin at the end of replay without rejecting its first acknowledgement.
		ackCursor = replay.Frames[0].Cursor
	}
	journal.nextSubscriber++
	subscription := &OutputBrokerSubscription{
		journal: journal,
		id:      journal.nextSubscriber,
		records: make(chan OutputBrokerRecord, buffer),
		ack:     ackCursor,
	}
	journal.subscribers[subscription.id] = subscription
	return replay, subscription, nil
}

// NextCursor returns the first byte not yet appended to the journal.
func (journal *OutputBrokerJournal) NextCursor() uint64 {
	journal.mu.Lock()
	defer journal.mu.Unlock()
	return journal.nextCursor
}

// Close writes the normal completion footer and retires every live subscriber.
func (journal *OutputBrokerJournal) Close() error {
	journal.mu.Lock()
	if journal.closed {
		journal.mu.Unlock()
		return nil
	}
	journal.closed = true
	for id, subscription := range journal.subscribers {
		subscription.closeLocked()
		delete(journal.subscribers, id)
	}
	err := journal.spool.close()
	journal.mu.Unlock()
	return err
}

// OutputBrokerSubscription is one bounded live view of a broker journal.
type OutputBrokerSubscription struct {
	journal    *OutputBrokerJournal
	id         uint64
	records    chan OutputBrokerRecord
	ack        uint64
	pendingGap *OutputBrokerGap
	closed     bool
}

// Records returns the future output/gap channel for this subscription.
func (subscription *OutputBrokerSubscription) Records() <-chan OutputBrokerRecord {
	return subscription.records
}

// Ack advances the subscriber's idempotent cursor acknowledgement.
func (subscription *OutputBrokerSubscription) Ack(cursor uint64) error {
	subscription.journal.mu.Lock()
	defer subscription.journal.mu.Unlock()
	if subscription.closed {
		return ErrOutputBrokerSubscriptionClosed
	}
	if cursor < subscription.ack {
		return fmt.Errorf("output broker acknowledgement regressed from %d to %d", subscription.ack, cursor)
	}
	if cursor > subscription.journal.nextCursor {
		return fmt.Errorf("output broker acknowledgement %d exceeds journal cursor %d", cursor, subscription.journal.nextCursor)
	}
	subscription.ack = cursor
	return nil
}

// Close retires the subscription without affecting the journal or child process.
func (subscription *OutputBrokerSubscription) Close() error {
	subscription.journal.mu.Lock()
	defer subscription.journal.mu.Unlock()
	if subscription.closed {
		return nil
	}
	delete(subscription.journal.subscribers, subscription.id)
	subscription.closeLocked()
	return nil
}

// closeLocked closes one subscription while the parent journal mutex is held.
func (subscription *OutputBrokerSubscription) closeLocked() {
	if subscription.closed {
		return
	}
	subscription.closed = true
	close(subscription.records)
}

// publishLocked sends one frame after durable append, retaining an explicit gap when the reader is slow.
func (subscription *OutputBrokerSubscription) publishLocked(frame OutputBrokerFrame) {
	if subscription.closed {
		return
	}
	if subscription.pendingGap != nil {
		select {
		case subscription.records <- OutputBrokerRecord{Gap: cloneOutputBrokerGap(subscription.pendingGap)}:
			subscription.pendingGap = nil
		default:
			subscription.extendGapLocked(frame)
			return
		}
	}
	select {
	case subscription.records <- OutputBrokerRecord{Frame: cloneOutputBrokerFrame(&frame)}:
	default:
		subscription.extendGapLocked(frame)
	}
}

// extendGapLocked coalesces one dropped frame into the subscriber's explicit loss record.
func (subscription *OutputBrokerSubscription) extendGapLocked(frame OutputBrokerFrame) {
	if subscription.pendingGap == nil {
		subscription.pendingGap = &OutputBrokerGap{StartCursor: frame.Cursor, EndCursor: frame.NextCursor, DroppedRecords: 1}
		return
	}
	subscription.pendingGap.EndCursor = frame.NextCursor
	subscription.pendingGap.DroppedRecords++
}

// replayLocked performs cursor-boundary checks while the journal mutex is held.
func (journal *OutputBrokerJournal) replayLocked(cursor uint64) OutputBrokerReplay {
	replay := OutputBrokerReplay{NextCursor: cursor}
	effective := cursor
	switch {
	case cursor < journal.baseCursor:
		replay.Truncated = true
		effective = journal.baseCursor
	case cursor > journal.nextCursor:
		replay.Reset = true
		effective = journal.baseCursor
	case !outputBrokerCursorBoundary(journal.frames, cursor):
		replay.Reset = true
		effective = journal.baseCursor
	}
	replay.NextCursor = effective
	remaining := MaximumOutputBrokerReplayBytes
	for _, frame := range journal.frames {
		if frame.NextCursor <= effective {
			continue
		}
		start := effective
		if start < frame.Cursor {
			start = frame.Cursor
		}
		text := frame.Text
		if start > frame.Cursor {
			text = text[start-frame.Cursor:]
		}
		if len([]byte(text)) > remaining {
			text = outputBrokerUTF8Prefix(text, remaining)
		}
		if text == "" {
			break
		}
		part := frame
		part.Cursor = start
		part.Text = text
		part.NextCursor = start + uint64(len([]byte(text)))
		replay.Frames = append(replay.Frames, part)
		replay.NextCursor = part.NextCursor
		remaining -= len([]byte(text))
		effective = part.NextCursor
		if remaining == 0 {
			break
		}
	}
	replay.HasMore = replay.NextCursor < journal.nextCursor
	return replay
}

// outputBrokerFramesFromSpool converts the existing durable frame representation without exposing file details.
func outputBrokerFramesFromSpool(frames []outputSpoolFrame) []OutputBrokerFrame {
	converted := make([]OutputBrokerFrame, 0, len(frames))
	for _, frame := range frames {
		stream := outputBrokerStreamFromInternal(frame.stream)
		if stream == 0 {
			continue
		}
		converted = append(converted, OutputBrokerFrame{
			Cursor:     frame.start,
			NextCursor: frame.start + uint64(len(frame.bytes)),
			Stream:     stream,
			Text:       string(frame.bytes),
		})
	}
	return converted
}

// outputBrokerInternalStream maps the exported stream identity to the existing spool format.
func outputBrokerInternalStream(stream OutputBrokerStream) outputStream {
	if stream == OutputBrokerStreamStdout {
		return outputStreamStdout
	}
	return outputStreamStderr
}

// outputBrokerStreamFromInternal maps validated spool bytes back to the broker contract.
func outputBrokerStreamFromInternal(stream byte) OutputBrokerStream {
	switch stream {
	case outputSpoolStreamStdout:
		return OutputBrokerStreamStdout
	case outputSpoolStreamStderr:
		return OutputBrokerStreamStderr
	default:
		return 0
	}
}

// outputBrokerCursorBoundary rejects a cursor that splits a retained UTF-8 encoding.
func outputBrokerCursorBoundary(frames []OutputBrokerFrame, cursor uint64) bool {
	if len(frames) == 0 {
		return true
	}
	for _, frame := range frames {
		if cursor == frame.Cursor || cursor == frame.NextCursor {
			return true
		}
		if cursor > frame.Cursor && cursor < frame.NextCursor {
			offset := cursor - frame.Cursor
			return utf8.RuneStart([]byte(frame.Text)[offset])
		}
	}
	return cursor == frames[len(frames)-1].NextCursor
}

// outputBrokerUTF8Prefix returns the largest valid prefix no larger than the byte limit.
func outputBrokerUTF8Prefix(text string, maximum int) string {
	if maximum >= len([]byte(text)) {
		return text
	}
	cut := maximum
	for cut > 0 && !utf8.RuneStart([]byte(text)[cut]) {
		cut--
	}
	return text[:cut]
}

// cloneOutputBrokerFrame isolates subscriber records from future journal compaction.
func cloneOutputBrokerFrame(frame *OutputBrokerFrame) *OutputBrokerFrame {
	if frame == nil {
		return nil
	}
	copy := *frame
	return &copy
}

// cloneOutputBrokerGap isolates subscriber loss records from later coalescing.
func cloneOutputBrokerGap(gap *OutputBrokerGap) *OutputBrokerGap {
	if gap == nil {
		return nil
	}
	copy := *gap
	return &copy
}
