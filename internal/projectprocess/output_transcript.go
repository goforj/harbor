package projectprocess

import (
	"bytes"
	"context"
	"sync"
	"unicode/utf8"

	"github.com/goforj/harbor/internal/domain"
)

const (
	// MaximumOutputChunkBytes bounds one supervisor response below the IPC frame ceiling after JSON escaping.
	MaximumOutputChunkBytes       = 64 * 1024
	outputTranscriptCapacityBytes = 256 * 1024
	outputTranscriptReplacement   = "\uFFFD"
)

// OutputChunk is one bounded, cursor-addressed view of a supervised process transcript.
type OutputChunk struct {
	// Available reports whether the exact project and session still name a supervised process.
	Available bool `json:"available"`
	// Reset reports that the supplied cursor was ahead of or not aligned with the current transcript.
	Reset bool `json:"reset"`
	// Truncated reports that retained output begins after the supplied cursor.
	Truncated bool `json:"truncated"`
	// HasMore reports that another chunk is already retained after NextCursor.
	HasMore bool `json:"has_more"`
	// NextCursor is the first transcript byte not included in Text.
	NextCursor uint64 `json:"next_cursor"`
	// Text contains at most MaximumOutputChunkBytes of valid UTF-8 process output.
	Text string `json:"text"`
}

// outputTranscript retains the latest process output without coupling child progress to a reader.
type outputTranscript struct {
	mu      sync.Mutex
	buffer  []byte
	start   int
	length  int
	base    uint64
	next    uint64
	changed chan struct{}
	closed  bool
}

// newOutputTranscript creates one fixed-capacity transcript ring.
func newOutputTranscript(capacity int) *outputTranscript {
	if capacity <= 0 {
		panic("projectprocess output transcript capacity must be positive")
	}
	return &outputTranscript{
		buffer:  make([]byte, capacity),
		changed: make(chan struct{}),
	}
}

// ReadOutput returns output only while both identities still select the same supervised process.
func (supervisor *Supervisor) ReadOutput(
	projectID domain.ProjectID,
	sessionID domain.SessionID,
	cursor uint64,
) OutputChunk {
	supervisor.mu.Lock()
	process := supervisor.projects[projectID]
	if process == nil || supervisor.sessions[sessionID] != process {
		supervisor.mu.Unlock()
		return OutputChunk{}
	}
	transcript := process.relay.transcript
	supervisor.mu.Unlock()

	return transcript.read(cursor)
}

// WaitOutput waits until output after cursor is available or the exact supervised process is no longer available.
func (supervisor *Supervisor) WaitOutput(
	ctx context.Context,
	projectID domain.ProjectID,
	sessionID domain.SessionID,
	cursor uint64,
) (OutputChunk, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return OutputChunk{}, err
	}

	supervisor.mu.Lock()
	process := supervisor.projects[projectID]
	if process == nil || supervisor.sessions[sessionID] != process {
		supervisor.mu.Unlock()
		return OutputChunk{}, nil
	}
	transcript := process.relay.transcript
	supervisor.mu.Unlock()

	return transcript.wait(ctx, cursor)
}

// append normalizes one relay record before adding it to the bounded transcript.
func (transcript *outputTranscript) append(output []byte) {
	normalized := bytes.ToValidUTF8(output, []byte(outputTranscriptReplacement))
	if len(normalized) == 0 {
		return
	}

	transcript.mu.Lock()
	defer transcript.mu.Unlock()
	if transcript.closed {
		return
	}

	capacity := len(transcript.buffer)
	if len(normalized) > capacity {
		transcript.discardLocked(transcript.length)
		skipped := len(normalized) - capacity
		for skipped < len(normalized) && !utf8.RuneStart(normalized[skipped]) {
			skipped++
		}
		transcript.base += uint64(skipped)
		transcript.writeLocked(normalized[skipped:])
		transcript.next += uint64(len(normalized))
		transcript.notifyLocked()
		return
	}

	excess := transcript.length + len(normalized) - capacity
	if excess > 0 {
		transcript.discardLocked(excess)
		for transcript.length > 0 && !utf8.RuneStart(transcript.byteAtLocked(0)) {
			transcript.discardLocked(1)
		}
	}
	transcript.writeLocked(normalized)
	transcript.next += uint64(len(normalized))
	transcript.notifyLocked()
}

// read copies one bounded chunk while keeping cursors on valid UTF-8 boundaries.
func (transcript *outputTranscript) read(cursor uint64) OutputChunk {
	transcript.mu.Lock()
	defer transcript.mu.Unlock()
	return transcript.readLocked(cursor)
}

// wait registers its change signal under the transcript lock so appends cannot race past a waiter.
func (transcript *outputTranscript) wait(ctx context.Context, cursor uint64) (OutputChunk, error) {
	for {
		transcript.mu.Lock()
		chunk := transcript.readLocked(cursor)
		if chunk.Text != "" || chunk.Reset || chunk.Truncated || chunk.HasMore {
			transcript.mu.Unlock()
			return chunk, nil
		}
		if transcript.closed {
			transcript.mu.Unlock()
			return OutputChunk{}, nil
		}
		changed := transcript.changed
		transcript.mu.Unlock()

		select {
		case <-changed:
		case <-ctx.Done():
			return OutputChunk{}, ctx.Err()
		}
	}
}

// close makes caught-up waiters observe that no more process output can arrive.
func (transcript *outputTranscript) close() {
	transcript.mu.Lock()
	defer transcript.mu.Unlock()
	if transcript.closed {
		return
	}
	transcript.closed = true
	close(transcript.changed)
}

// notifyLocked advances the one-shot change generation after an append.
func (transcript *outputTranscript) notifyLocked() {
	close(transcript.changed)
	transcript.changed = make(chan struct{})
}

// readLocked copies one bounded chunk while keeping cursors on valid UTF-8 boundaries.
func (transcript *outputTranscript) readLocked(cursor uint64) OutputChunk {

	chunk := OutputChunk{Available: true}
	effective := cursor
	switch {
	case cursor < transcript.base:
		chunk.Truncated = true
		effective = transcript.base
	case cursor > transcript.next:
		chunk.Reset = true
		effective = transcript.base
	case !transcript.cursorBoundaryLocked(cursor):
		chunk.Reset = true
		effective = transcript.base
	}

	offset := int(effective - transcript.base)
	count := transcript.length - offset
	if count > MaximumOutputChunkBytes {
		count = MaximumOutputChunkBytes
	}
	end := offset + count
	for end < transcript.length && end > offset && !utf8.RuneStart(transcript.byteAtLocked(end)) {
		end--
	}
	content := transcript.copyLocked(offset, end-offset)
	chunk.Text = string(content)
	chunk.NextCursor = effective + uint64(len(content))
	chunk.HasMore = chunk.NextCursor < transcript.next
	return chunk
}

// cursorBoundaryLocked rejects caller-created cursors that split one retained UTF-8 encoding.
func (transcript *outputTranscript) cursorBoundaryLocked(cursor uint64) bool {
	if cursor < transcript.base || cursor > transcript.next {
		return false
	}
	offset := int(cursor - transcript.base)
	return offset == transcript.length || utf8.RuneStart(transcript.byteAtLocked(offset))
}

// discardLocked advances the retained base without changing the absolute end cursor.
func (transcript *outputTranscript) discardLocked(count int) {
	if count <= 0 {
		return
	}
	if count > transcript.length {
		count = transcript.length
	}
	transcript.start = (transcript.start + count) % len(transcript.buffer)
	transcript.length -= count
	transcript.base += uint64(count)
	if transcript.length == 0 {
		transcript.start = 0
	}
}

// writeLocked appends owned bytes after enough capacity has already been reserved.
func (transcript *outputTranscript) writeLocked(output []byte) {
	if len(output) == 0 {
		return
	}
	writeAt := (transcript.start + transcript.length) % len(transcript.buffer)
	first := copy(transcript.buffer[writeAt:], output)
	copy(transcript.buffer, output[first:])
	transcript.length += len(output)
}

// copyLocked returns one linear copy from the possibly wrapped retained range.
func (transcript *outputTranscript) copyLocked(offset int, count int) []byte {
	if count <= 0 {
		return nil
	}
	result := make([]byte, count)
	readAt := (transcript.start + offset) % len(transcript.buffer)
	first := copy(result, transcript.buffer[readAt:])
	copy(result[first:], transcript.buffer[:count-first])
	return result
}

// byteAtLocked returns one byte relative to the retained transcript base.
func (transcript *outputTranscript) byteAtLocked(offset int) byte {
	return transcript.buffer[(transcript.start+offset)%len(transcript.buffer)]
}
