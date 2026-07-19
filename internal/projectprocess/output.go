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

// outputRelay isolates child pipes from slow UI or log writers through one bounded line queue.
type outputRelay struct {
	stdout  io.Writer
	stderr  io.Writer
	queue   chan outputLine
	dropped atomic.Uint64
	once    sync.Once
}

// newOutputRelay starts one serializer so stdout and stderr cannot interleave bytes when they share a writer.
func newOutputRelay(stdout, stderr io.Writer, bufferLines int) *outputRelay {
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	relay := &outputRelay{
		stdout: stdout,
		stderr: stderr,
		queue:  make(chan outputLine, bufferLines),
	}
	go relay.deliver()
	return relay
}

// offer preserves child progress by dropping complete lines after the caller-owned output queue fills.
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
	})
}

// deliver writes one complete line at a time and abandons only the stream whose destination fails.
func (relay *outputRelay) deliver() {
	stdoutFailed := false
	stderrFailed := false
	for line := range relay.queue {
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
