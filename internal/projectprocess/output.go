package projectprocess

import (
	"io"
	"sync"
	"sync/atomic"
	"unicode/utf8"
)

const outputReadBufferBytes = 4 * 1024

// outputStream distinguishes independent failure handling for stdout and stderr writers.
type outputStream uint8

const (
	outputStreamStdout outputStream = iota
	outputStreamStderr
)

// outputRecord retains one serialized child output fragment and its caller-owned destination.
type outputRecord struct {
	stream outputStream
	bytes  []byte
}

// outputRelay isolates child pipes and durable diagnostics from slow caller-owned writers.
type outputRelay struct {
	stdout      io.Writer
	stderr      io.Writer
	trace       io.WriteCloser
	spool       *outputSpool
	transcript  *outputTranscript
	queue       chan outputRecord
	callerQueue chan outputRecord
	traceDone   chan struct{}
	dropped     atomic.Uint64
	once        sync.Once
}

// newOutputRelay starts one serializer so stdout and stderr cannot interleave bytes when they share a writer.
func newOutputRelay(stdout, stderr io.Writer, bufferLines int) *outputRelay {
	return newOutputRelayWithTrace(stdout, stderr, nil, bufferLines)
}

// newOutputRelayWithTrace retains an owned launch trace without making project progress depend on a caller-owned writer.
func newOutputRelayWithTrace(stdout, stderr io.Writer, trace io.WriteCloser, bufferLines int) *outputRelay {
	return newOutputRelayWithTraceAndSpool(stdout, stderr, trace, nil, bufferLines)
}

// newOutputRelayWithTraceAndSpool adds a durable history sink without coupling child progress to that sink.
func newOutputRelayWithTraceAndSpool(stdout, stderr io.Writer, trace io.WriteCloser, spool *outputSpool, bufferLines int) *outputRelay {
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	relay := &outputRelay{
		stdout:      stdout,
		stderr:      stderr,
		trace:       trace,
		spool:       spool,
		transcript:  newOutputTranscript(outputTranscriptCapacityBytes),
		queue:       make(chan outputRecord, bufferLines),
		callerQueue: make(chan outputRecord, bufferLines),
		traceDone:   make(chan struct{}),
	}
	go relay.deliverCallerOutput()
	go relay.deliver()
	return relay
}

// offer preserves child progress by dropping output records after the bounded diagnostic queue fills.
func (relay *outputRelay) offer(stream outputStream, bytes []byte) {
	record := outputRecord{stream: stream, bytes: append([]byte(nil), bytes...)}
	select {
	case relay.queue <- record:
	default:
		relay.dropped.Add(1)
	}
}

// finish closes the relay after both pipe readers have stopped producing records.
func (relay *outputRelay) finish() {
	relay.once.Do(func() {
		close(relay.queue)
		<-relay.traceDone
		relay.transcript.close()
	})
}

// deliver owns the durable trace so caller backpressure cannot hide the diagnostics needed to explain startup.
func (relay *outputRelay) deliver() {
	traceFailed := false
	defer func() {
		if relay.trace != nil {
			_ = relay.trace.Close()
		}
		if relay.spool != nil {
			_ = relay.spool.close()
		}
		close(relay.callerQueue)
		close(relay.traceDone)
	}()
	for record := range relay.queue {
		normalized := normalizeOutputBytes(record.bytes)
		relay.transcript.appendNormalized(normalized)
		if relay.spool != nil {
			if err := relay.spool.appendNormalized(record.stream, normalized); err != nil {
				// Durable history is diagnostic state. A broken spool must not block or terminate the child.
				_ = relay.spool.close()
				relay.spool = nil
			}
		}
		if relay.trace != nil && !traceFailed {
			if writeOutputRecord(relay.trace, record.bytes) != nil {
				traceFailed = true
			}
		}
		select {
		case relay.callerQueue <- record:
		default:
			relay.dropped.Add(1)
		}
	}
}

// deliverCallerOutput preserves best-effort terminal output without joining a writer Harbor does not own.
func (relay *outputRelay) deliverCallerOutput() {
	stdoutFailed := false
	stderrFailed := false
	for record := range relay.callerQueue {
		switch record.stream {
		case outputStreamStdout:
			if stdoutFailed {
				relay.dropped.Add(1)
			} else if writeOutputRecord(relay.stdout, record.bytes) != nil {
				relay.dropped.Add(1)
				stdoutFailed = true
			}
		case outputStreamStderr:
			if stderrFailed {
				relay.dropped.Add(1)
			} else if writeOutputRecord(relay.stderr, record.bytes) != nil {
				relay.dropped.Add(1)
				stderrFailed = true
			}
		}
	}
}

// writeOutputRecord converts a caller-writer panic into a failed stream so process supervision remains intact.
func writeOutputRecord(writer io.Writer, bytes []byte) (err error) {
	defer func() {
		if recover() != nil {
			err = io.ErrClosedPipe
		}
	}()
	written, err := writer.Write(bytes)
	if err == nil && written != len(bytes) {
		return io.ErrShortWrite
	}
	return err
}

// readOutputStream drains each readable pipe chunk without allowing caller backpressure to block the child process.
func readOutputStream(reader io.Reader, stream outputStream, relay *outputRelay, readers *sync.WaitGroup) {
	defer readers.Done()
	if closer, ok := reader.(io.Closer); ok {
		defer closer.Close()
	}
	readOutputChunks(reader, stream, relay)
}

// readOutputChunks relays each successful read while retaining only an incomplete UTF-8 suffix.
func readOutputChunks(reader io.Reader, stream outputStream, relay *outputRelay) {
	buffer := make([]byte, outputReadBufferBytes)
	pending := make([]byte, 0, utf8.UTFMax)
	for {
		count, err := reader.Read(buffer)
		if count > 0 {
			pending = append(pending, buffer[:count]...)
			complete := completeUTF8PrefixBytes(pending)
			if complete > 0 {
				relay.offer(stream, pending[:complete])
				copy(pending, pending[complete:])
				pending = pending[:len(pending)-complete]
			}
		}
		if err != nil {
			if len(pending) > 0 {
				relay.offer(stream, pending)
			}
			return
		}
	}
}

// completeUTF8PrefixBytes keeps a trailing partial encoding until another read or EOF can complete it.
func completeUTF8PrefixBytes(output []byte) int {
	start := len(output) - 1
	minimum := len(output) - utf8.UTFMax
	if minimum < 0 {
		minimum = 0
	}
	for start >= minimum {
		if utf8.RuneStart(output[start]) {
			if !utf8.FullRune(output[start:]) {
				return start
			}
			return len(output)
		}
		start--
	}
	return len(output)
}
