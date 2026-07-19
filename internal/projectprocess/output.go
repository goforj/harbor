package projectprocess

import (
	"bufio"
	"errors"
	"io"
	"sync"
	"sync/atomic"
)

const maximumOutputLineBytes = 64 * 1024

var outputTruncationMarker = []byte(" [output truncated]\n")

// outputStream distinguishes independent failure handling for stdout and stderr writers.
type outputStream uint8

const (
	outputStreamStdout outputStream = iota
	outputStreamStderr
)

// outputLine retains one complete child line and its caller-owned destination.
type outputLine struct {
	stream outputStream
	bytes  []byte
}

// outputRelay isolates child pipes and durable diagnostics from slow caller-owned writers.
type outputRelay struct {
	stdout      io.Writer
	stderr      io.Writer
	trace       io.WriteCloser
	transcript  *outputTranscript
	queue       chan outputLine
	callerQueue chan outputLine
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
		transcript:  newOutputTranscript(outputTranscriptCapacityBytes),
		queue:       make(chan outputLine, bufferLines),
		callerQueue: make(chan outputLine, bufferLines),
		traceDone:   make(chan struct{}),
	}
	go relay.deliverCallerOutput()
	go relay.deliver()
	return relay
}

// offer preserves child progress by dropping complete lines after the bounded diagnostic queue fills.
func (relay *outputRelay) offer(stream outputStream, bytes []byte) {
	line := outputLine{stream: stream, bytes: append([]byte(nil), bytes...)}
	select {
	case relay.queue <- line:
	default:
		relay.dropped.Add(1)
	}
}

// finish closes the relay after both pipe readers have stopped producing lines.
func (relay *outputRelay) finish() {
	relay.once.Do(func() {
		close(relay.queue)
		<-relay.traceDone
	})
}

// deliver owns the durable trace so caller backpressure cannot hide the diagnostics needed to explain startup.
func (relay *outputRelay) deliver() {
	traceFailed := false
	defer func() {
		if relay.trace != nil {
			_ = relay.trace.Close()
		}
		close(relay.callerQueue)
		close(relay.traceDone)
	}()
	for line := range relay.queue {
		relay.transcript.append(line.bytes)
		if relay.trace != nil && !traceFailed {
			if writeOutputLine(relay.trace, line.bytes) != nil {
				traceFailed = true
			}
		}
		select {
		case relay.callerQueue <- line:
		default:
			relay.dropped.Add(1)
		}
	}
}

// deliverCallerOutput preserves best-effort terminal output without joining a writer Harbor does not own.
func (relay *outputRelay) deliverCallerOutput() {
	stdoutFailed := false
	stderrFailed := false
	for line := range relay.callerQueue {
		switch line.stream {
		case outputStreamStdout:
			if stdoutFailed {
				relay.dropped.Add(1)
			} else if writeOutputLine(relay.stdout, line.bytes) != nil {
				relay.dropped.Add(1)
				stdoutFailed = true
			}
		case outputStreamStderr:
			if stderrFailed {
				relay.dropped.Add(1)
			} else if writeOutputLine(relay.stderr, line.bytes) != nil {
				relay.dropped.Add(1)
				stderrFailed = true
			}
		}
	}
}

// writeOutputLine converts a caller-writer panic into a failed stream so process supervision remains intact.
func writeOutputLine(writer io.Writer, bytes []byte) (err error) {
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

// readOutputLines drains a pipe without allowing caller backpressure to block the child process.
func readOutputLines(reader io.Reader, stream outputStream, relay *outputRelay, readers *sync.WaitGroup) {
	defer readers.Done()
	if closer, ok := reader.(io.Closer); ok {
		defer closer.Close()
	}
	buffered := bufio.NewReaderSize(reader, maximumOutputLineBytes)
	line := make([]byte, 0, 4096)
	truncated := false
	for {
		fragment, err := buffered.ReadSlice('\n')
		if !truncated && len(fragment) > 0 {
			remaining := maximumOutputLineBytes - len(line)
			if len(fragment) > remaining {
				line = append(line, fragment[:remaining]...)
				truncated = true
			} else {
				line = append(line, fragment...)
			}
		}
		if err == nil {
			relay.offerBoundedLine(stream, line, truncated)
			line = line[:0]
			truncated = false
			continue
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			truncated = true
			continue
		}
		if len(line) > 0 || truncated {
			relay.offerBoundedLine(stream, line, truncated)
		}
		if errors.Is(err, io.EOF) {
			return
		}
		return
	}
}

// offerBoundedLine records one truncated line as dropped while retaining a diagnostic prefix within the memory bound.
func (relay *outputRelay) offerBoundedLine(stream outputStream, line []byte, truncated bool) {
	if truncated {
		relay.dropped.Add(1)
		prefixBytes := maximumOutputLineBytes - len(outputTruncationMarker)
		if len(line) > prefixBytes {
			line = line[:prefixBytes]
		}
		line = append(line, outputTruncationMarker...)
	}
	if len(line) > 0 {
		relay.offer(stream, line)
	}
}
