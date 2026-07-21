package projectprocess

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"sync"
	"unicode/utf8"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/platform/runtimepath"
)

const (
	outputSpoolDirectoryName              = "output-spool"
	outputSpoolFilenameSuffix             = ".v1"
	outputSpoolMagic                      = "HARBOROS"
	outputSpoolVersion             uint16 = 1
	outputSpoolHeaderPrefixBytes          = 24
	outputSpoolFrameHeaderBytes           = 24
	outputSpoolMaximumBytes        int64  = 4 << 20
	outputSpoolMaximumPayloadBytes        = 64 << 10
	outputSpoolFrameOutput         byte   = 1
	outputSpoolFrameClosed         byte   = 2
	outputSpoolStreamStdout        byte   = 1
	outputSpoolStreamStderr        byte   = 2
)

var (
	// ErrOutputSpoolClosed prevents appending after a spool has published its completion footer.
	ErrOutputSpoolClosed = errors.New("project output spool is closed")
	// ErrOutputSpoolCorrupt identifies malformed or identity-mismatched persisted output.
	ErrOutputSpoolCorrupt = errors.New("project output spool is corrupt")
)

// outputSpoolFrame is one normalized, cursor-addressed child-output record retained for replay.
type outputSpoolFrame struct {
	stream byte
	start  uint64
	bytes  []byte
}

// outputSpoolHeader binds a spool file to one exact Harbor project/session pair.
type outputSpoolHeader struct {
	projectID  domain.ProjectID
	sessionID  domain.SessionID
	baseCursor uint64
}

// outputSpoolSnapshot is a read-only reconstruction of one persisted transcript.
type outputSpoolSnapshot struct {
	transcript *outputTranscript
	closed     bool
}

// outputSpool owns one append-only, checksummed transcript outside the project checkout.
type outputSpool struct {
	mu         sync.Mutex
	file       *os.File
	path       string
	header     outputSpoolHeader
	frames     []outputSpoolFrame
	nextCursor uint64
	closed     bool
	maximum    int64
}

// resolveOutputSpoolDirectory returns the caller override or Harbor's per-user runtime root.
func resolveOutputSpoolDirectory(configured string) string {
	if configured != "" {
		return configured
	}
	directory, err := runtimepath.Directory()
	if err != nil {
		return ""
	}
	return directory
}

// outputSpoolPath derives a path that cannot be redirected by separators in a domain identifier.
func outputSpoolPath(directory string, projectID domain.ProjectID, sessionID domain.SessionID) (string, error) {
	if err := projectID.Validate(); err != nil {
		return "", fmt.Errorf("validate output spool project ID: %w", err)
	}
	if err := sessionID.Validate(); err != nil {
		return "", fmt.Errorf("validate output spool session ID: %w", err)
	}
	if directory == "" || !filepath.IsAbs(directory) || filepath.Clean(directory) != directory {
		return "", fmt.Errorf("output spool directory must be a clean absolute path")
	}
	digest := sha256.Sum256(append(append([]byte(string(projectID)), 0), []byte(sessionID)...))
	return filepath.Join(directory, outputSpoolDirectoryName, hex.EncodeToString(digest[:])+outputSpoolFilenameSuffix), nil
}

// prepareOutputSpoolDirectory creates one owner-private directory without following a symbolic link.
func prepareOutputSpoolDirectory(directory string) (string, error) {
	if directory == "" || !filepath.IsAbs(directory) || filepath.Clean(directory) != directory {
		return "", fmt.Errorf("output spool directory must be a clean absolute path")
	}
	if information, err := os.Lstat(directory); err == nil {
		if information.Mode()&os.ModeSymlink != 0 || !information.IsDir() {
			return "", fmt.Errorf("output spool root is not a direct directory")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("inspect output spool root: %w", err)
	}
	spoolDirectory := filepath.Join(directory, outputSpoolDirectoryName)
	if err := os.MkdirAll(spoolDirectory, 0o700); err != nil {
		return "", fmt.Errorf("create output spool directory: %w", err)
	}
	information, err := os.Lstat(spoolDirectory)
	if err != nil {
		return "", fmt.Errorf("inspect output spool directory: %w", err)
	}
	if information.Mode()&os.ModeSymlink != 0 || !information.IsDir() {
		return "", fmt.Errorf("output spool directory is not an owner-private directory")
	}
	if err := os.Chmod(spoolDirectory, 0o700); err != nil {
		return "", fmt.Errorf("secure output spool directory: %w", err)
	}
	return spoolDirectory, nil
}

// openOutputSpool opens, resumes, or creates one exact session spool before a child process starts.
func openOutputSpool(directory string, projectID domain.ProjectID, sessionID domain.SessionID) (*outputSpool, error) {
	if directory == "" {
		return nil, nil
	}
	spoolDirectory, err := prepareOutputSpoolDirectory(directory)
	if err != nil {
		return nil, err
	}
	path, err := outputSpoolPath(directory, projectID, sessionID)
	if err != nil {
		return nil, err
	}
	if information, statErr := os.Lstat(path); statErr == nil {
		if information.Mode()&os.ModeSymlink != 0 || !information.Mode().IsRegular() {
			return nil, fmt.Errorf("output spool path %q is not a direct regular file", path)
		}
		file, openErr := os.OpenFile(path, os.O_RDWR, 0o600)
		if openErr != nil {
			return nil, fmt.Errorf("open existing output spool: %w", openErr)
		}
		header, frames, closed, validEnd, readErr := readOutputSpoolFile(file, projectID, sessionID)
		if readErr != nil {
			_ = file.Close()
			return nil, readErr
		}
		if closed {
			if closeErr := file.Close(); closeErr != nil {
				return nil, fmt.Errorf("close completed output spool: %w", closeErr)
			}
			if removeErr := os.Remove(path); removeErr != nil {
				return nil, fmt.Errorf("replace completed output spool: %w", removeErr)
			}
			return createOutputSpool(spoolDirectory, path, projectID, sessionID)
		}
		if err := file.Truncate(validEnd); err != nil {
			_ = file.Close()
			return nil, fmt.Errorf("truncate interrupted output spool: %w", err)
		}
		if _, err := file.Seek(validEnd, io.SeekStart); err != nil {
			_ = file.Close()
			return nil, fmt.Errorf("resume interrupted output spool: %w", err)
		}
		if err := file.Chmod(0o600); err != nil {
			_ = file.Close()
			return nil, fmt.Errorf("secure resumed output spool: %w", err)
		}
		return &outputSpool{
			file:       file,
			path:       path,
			header:     header,
			frames:     frames,
			nextCursor: outputSpoolNextCursor(header, frames),
			maximum:    outputSpoolMaximumBytes,
		}, nil
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect output spool path: %w", statErr)
	}
	return createOutputSpool(spoolDirectory, path, projectID, sessionID)
}

// createOutputSpool creates one new owner-private spool without replacing a concurrent creator.
func createOutputSpool(spoolDirectory, path string, projectID domain.ProjectID, sessionID domain.SessionID) (*outputSpool, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create output spool in %q: %w", spoolDirectory, err)
	}
	spool := &outputSpool{
		file:    file,
		path:    path,
		header:  outputSpoolHeader{projectID: projectID, sessionID: sessionID},
		maximum: outputSpoolMaximumBytes,
	}
	if err := spool.rewriteLocked(nil, 0); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return nil, fmt.Errorf("write output spool header: %w", err)
	}
	return spool, nil
}

// readOutputSpool opens a validated spool snapshot without granting any process-control authority.
func readOutputSpool(directory string, projectID domain.ProjectID, sessionID domain.SessionID) (outputSpoolSnapshot, bool, error) {
	if directory == "" {
		return outputSpoolSnapshot{}, false, nil
	}
	path, err := outputSpoolPath(directory, projectID, sessionID)
	if err != nil {
		return outputSpoolSnapshot{}, false, err
	}
	spoolDirectory := filepath.Dir(path)
	directoryInfo, directoryErr := os.Lstat(spoolDirectory)
	if errors.Is(directoryErr, os.ErrNotExist) {
		return outputSpoolSnapshot{}, false, nil
	}
	if directoryErr != nil {
		return outputSpoolSnapshot{}, false, directoryErr
	}
	if directoryInfo.Mode()&os.ModeSymlink != 0 || !directoryInfo.IsDir() {
		return outputSpoolSnapshot{}, false, fmt.Errorf("%w: output spool directory is not a direct directory", ErrOutputSpoolCorrupt)
	}
	information, statErr := os.Lstat(path)
	if errors.Is(statErr, os.ErrNotExist) {
		return outputSpoolSnapshot{}, false, nil
	}
	if statErr != nil {
		return outputSpoolSnapshot{}, false, statErr
	}
	if information.Mode()&os.ModeSymlink != 0 || !information.Mode().IsRegular() {
		return outputSpoolSnapshot{}, false, fmt.Errorf("%w: output spool path is not a direct regular file", ErrOutputSpoolCorrupt)
	}
	file, err := os.Open(path)
	if err != nil {
		return outputSpoolSnapshot{}, false, err
	}
	defer file.Close()
	fileInformation, err := file.Stat()
	if err != nil || !fileInformation.Mode().IsRegular() {
		return outputSpoolSnapshot{}, false, fmt.Errorf("%w: output spool is not a regular file", ErrOutputSpoolCorrupt)
	}
	header, frames, closed, _, err := readOutputSpoolFile(file, projectID, sessionID)
	if err != nil {
		return outputSpoolSnapshot{}, false, err
	}
	transcript := newOutputTranscriptAt(outputTranscriptCapacityBytes, header.baseCursor)
	for _, frame := range frames {
		transcript.appendNormalized(frame.bytes)
	}
	return outputSpoolSnapshot{transcript: transcript, closed: closed}, true, nil
}

// readOutputSpoolFile validates the header and complete frames while ignoring one incomplete crash tail.
func readOutputSpoolFile(file *os.File, projectID domain.ProjectID, sessionID domain.SessionID) (outputSpoolHeader, []outputSpoolFrame, bool, int64, error) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return outputSpoolHeader{}, nil, false, -1, fmt.Errorf("seek output spool start: %w", err)
	}
	header, err := readOutputSpoolHeader(file, projectID, sessionID)
	if err != nil {
		return outputSpoolHeader{}, nil, false, -1, err
	}
	validEnd, err := file.Seek(0, io.SeekCurrent)
	if err != nil {
		return outputSpoolHeader{}, nil, false, -1, fmt.Errorf("read output spool header offset: %w", err)
	}
	frames := make([]outputSpoolFrame, 0)
	nextCursor := header.baseCursor
	closed := false
	for {
		frame, complete, frameEnd, readErr := readOutputSpoolFrame(file, nextCursor)
		if readErr != nil {
			return outputSpoolHeader{}, nil, false, -1, readErr
		}
		if !complete {
			return header, frames, closed, validEnd, nil
		}
		validEnd = frameEnd
		if frame.kind == outputSpoolFrameClosed {
			closed = true
			validEnd = frameEnd
			trailing, err := file.Read(make([]byte, 1))
			if trailing != 0 || err != io.EOF {
				return outputSpoolHeader{}, nil, false, -1, fmt.Errorf("%w: bytes follow closed output spool", ErrOutputSpoolCorrupt)
			}
			return header, frames, closed, validEnd, nil
		}
		frames = append(frames, outputSpoolFrame{stream: frame.stream, start: frame.start, bytes: frame.bytes})
		nextCursor += uint64(len(frame.bytes))
	}
}

// outputSpoolParsedFrame carries framing fields before they become the public replay representation.
type outputSpoolParsedFrame struct {
	kind   byte
	stream byte
	start  uint64
	bytes  []byte
}

// readOutputSpoolFrame reads one frame and distinguishes a torn final write from structural corruption.
func readOutputSpoolFrame(file *os.File, expectedCursor uint64) (outputSpoolParsedFrame, bool, int64, error) {
	headerBytes := make([]byte, outputSpoolFrameHeaderBytes)
	count, err := io.ReadFull(file, headerBytes)
	if err == io.EOF && count == 0 {
		return outputSpoolParsedFrame{}, false, 0, nil
	}
	if errors.Is(err, io.ErrUnexpectedEOF) {
		return outputSpoolParsedFrame{}, false, 0, nil
	}
	if err != nil {
		return outputSpoolParsedFrame{}, false, 0, fmt.Errorf("read output spool frame header: %w", err)
	}
	if string(headerBytes[:4]) != "HOF1" {
		return outputSpoolParsedFrame{}, false, 0, fmt.Errorf("%w: output spool frame magic is invalid", ErrOutputSpoolCorrupt)
	}
	kind := headerBytes[4]
	stream := headerBytes[5]
	if headerBytes[6] != 0 || headerBytes[7] != 0 {
		return outputSpoolParsedFrame{}, false, 0, fmt.Errorf("%w: output spool frame reserved bytes are non-zero", ErrOutputSpoolCorrupt)
	}
	start := binary.BigEndian.Uint64(headerBytes[8:16])
	payloadLength := binary.BigEndian.Uint32(headerBytes[16:20])
	wantCRC := binary.BigEndian.Uint32(headerBytes[20:24])
	if start != expectedCursor {
		return outputSpoolParsedFrame{}, false, 0, fmt.Errorf("%w: output spool cursor gap", ErrOutputSpoolCorrupt)
	}
	if kind == outputSpoolFrameClosed {
		if stream != 0 || payloadLength != 0 {
			return outputSpoolParsedFrame{}, false, 0, fmt.Errorf("%w: output spool close frame is malformed", ErrOutputSpoolCorrupt)
		}
		if crc32.ChecksumIEEE(headerBytes[:20]) != wantCRC {
			return outputSpoolParsedFrame{}, false, 0, fmt.Errorf("%w: output spool close checksum mismatch", ErrOutputSpoolCorrupt)
		}
		end, err := file.Seek(0, io.SeekCurrent)
		if err != nil {
			return outputSpoolParsedFrame{}, false, 0, fmt.Errorf("read output spool close offset: %w", err)
		}
		return outputSpoolParsedFrame{kind: kind, start: start}, true, end, nil
	}
	if kind != outputSpoolFrameOutput || (stream != outputSpoolStreamStdout && stream != outputSpoolStreamStderr) || payloadLength == 0 || payloadLength > outputSpoolMaximumPayloadBytes {
		return outputSpoolParsedFrame{}, false, 0, fmt.Errorf("%w: output spool frame shape is invalid", ErrOutputSpoolCorrupt)
	}
	payload := make([]byte, int(payloadLength))
	if _, err := io.ReadFull(file, payload); errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
		return outputSpoolParsedFrame{}, false, 0, nil
	} else if err != nil {
		return outputSpoolParsedFrame{}, false, 0, fmt.Errorf("read output spool frame payload: %w", err)
	}
	if !utf8OutputPayloadValid(payload) {
		return outputSpoolParsedFrame{}, false, 0, fmt.Errorf("%w: output spool payload is not normalized UTF-8", ErrOutputSpoolCorrupt)
	}
	checksumInput := append(append([]byte(nil), headerBytes[:20]...), payload...)
	if crc32.ChecksumIEEE(checksumInput) != wantCRC {
		return outputSpoolParsedFrame{}, false, 0, fmt.Errorf("%w: output spool checksum mismatch", ErrOutputSpoolCorrupt)
	}
	end, err := file.Seek(0, io.SeekCurrent)
	if err != nil {
		return outputSpoolParsedFrame{}, false, 0, fmt.Errorf("read output spool frame offset: %w", err)
	}
	return outputSpoolParsedFrame{kind: kind, stream: stream, start: start, bytes: payload}, true, end, nil
}

// readOutputSpoolHeader validates the fixed identity envelope before any output reaches a caller.
func readOutputSpoolHeader(file *os.File, projectID domain.ProjectID, sessionID domain.SessionID) (outputSpoolHeader, error) {
	prefix := make([]byte, outputSpoolHeaderPrefixBytes)
	if _, err := io.ReadFull(file, prefix); err != nil {
		return outputSpoolHeader{}, fmt.Errorf("%w: read output spool header: %v", ErrOutputSpoolCorrupt, err)
	}
	if string(prefix[:8]) != outputSpoolMagic || binary.BigEndian.Uint16(prefix[8:10]) != outputSpoolVersion {
		return outputSpoolHeader{}, fmt.Errorf("%w: output spool header version is unsupported", ErrOutputSpoolCorrupt)
	}
	projectLength := int(binary.BigEndian.Uint16(prefix[10:12]))
	sessionLength := int(binary.BigEndian.Uint16(prefix[12:14]))
	if prefix[14] != 0 || prefix[15] != 0 || projectLength > 256 || sessionLength > 256 {
		return outputSpoolHeader{}, fmt.Errorf("%w: output spool header lengths are invalid", ErrOutputSpoolCorrupt)
	}
	baseCursor := binary.BigEndian.Uint64(prefix[16:24])
	identities := make([]byte, projectLength+sessionLength)
	if _, err := io.ReadFull(file, identities); err != nil {
		return outputSpoolHeader{}, fmt.Errorf("%w: read output spool identities: %v", ErrOutputSpoolCorrupt, err)
	}
	checksum := make([]byte, 4)
	if _, err := io.ReadFull(file, checksum); err != nil {
		return outputSpoolHeader{}, fmt.Errorf("%w: read output spool header checksum: %v", ErrOutputSpoolCorrupt, err)
	}
	checksumInput := append(append([]byte(nil), prefix...), identities...)
	if crc32.ChecksumIEEE(checksumInput) != binary.BigEndian.Uint32(checksum) {
		return outputSpoolHeader{}, fmt.Errorf("%w: output spool header checksum mismatch", ErrOutputSpoolCorrupt)
	}
	header := outputSpoolHeader{
		projectID:  domain.ProjectID(string(identities[:projectLength])),
		sessionID:  domain.SessionID(string(identities[projectLength:])),
		baseCursor: baseCursor,
	}
	if header.projectID != projectID || header.sessionID != sessionID {
		return outputSpoolHeader{}, fmt.Errorf("%w: output spool identity does not match the requested session", ErrOutputSpoolCorrupt)
	}
	if err := header.projectID.Validate(); err != nil {
		return outputSpoolHeader{}, fmt.Errorf("%w: output spool project identity: %v", ErrOutputSpoolCorrupt, err)
	}
	if err := header.sessionID.Validate(); err != nil {
		return outputSpoolHeader{}, fmt.Errorf("%w: output spool session identity: %v", ErrOutputSpoolCorrupt, err)
	}
	return header, nil
}

// outputSpoolNextCursor returns the first cursor after the complete retained frame set.
func outputSpoolNextCursor(header outputSpoolHeader, frames []outputSpoolFrame) uint64 {
	next := header.baseCursor
	for _, frame := range frames {
		next = frame.start + uint64(len(frame.bytes))
	}
	return next
}

// appendNormalized records one already-normalized relay fragment and atomically compacts old frames when needed.
func (spool *outputSpool) appendNormalized(stream outputStream, output []byte) error {
	if spool == nil || len(output) == 0 {
		return nil
	}
	streamByte, err := outputSpoolStream(stream)
	if err != nil {
		return err
	}
	if len(output) > outputSpoolMaximumPayloadBytes {
		return fmt.Errorf("output spool payload exceeds %d bytes", outputSpoolMaximumPayloadBytes)
	}
	spool.mu.Lock()
	defer spool.mu.Unlock()
	if spool.closed {
		return ErrOutputSpoolClosed
	}
	frame := outputSpoolFrame{stream: streamByte, start: spool.nextCursor, bytes: append([]byte(nil), output...)}
	candidate := append(append([]outputSpoolFrame(nil), spool.frames...), frame)
	if outputSpoolEncodedBytes(spool.header, candidate) > spool.maximum {
		candidate = compactOutputSpoolFrames(spool.header, candidate, spool.maximum)
		if len(candidate) == 0 {
			return fmt.Errorf("%w: output spool cannot retain a frame", ErrOutputSpoolCorrupt)
		}
		base := candidate[0].start
		if err := spool.rewriteLocked(candidate, base); err != nil {
			return err
		}
		spool.frames = candidate
		spool.header.baseCursor = base
		spool.nextCursor = frame.start + uint64(len(frame.bytes))
		return nil
	}
	encoded, err := encodeOutputSpoolFrame(outputSpoolFrameOutput, streamByte, frame.start, frame.bytes)
	if err != nil {
		return err
	}
	if _, err := spool.file.Write(encoded); err != nil {
		return fmt.Errorf("append output spool frame: %w", err)
	}
	if err := spool.file.Sync(); err != nil {
		return fmt.Errorf("sync output spool frame: %w", err)
	}
	spool.frames = candidate
	spool.nextCursor += uint64(len(output))
	return nil
}

// close flushes a complete footer so readers can distinguish normal completion from a daemon crash.
func (spool *outputSpool) close() error {
	if spool == nil {
		return nil
	}
	spool.mu.Lock()
	defer spool.mu.Unlock()
	if spool.closed {
		return nil
	}
	closeFrame, err := encodeOutputSpoolFrame(outputSpoolFrameClosed, 0, spool.nextCursor, nil)
	if err != nil {
		return err
	}
	if outputSpoolEncodedBytes(spool.header, spool.frames)+int64(len(closeFrame)) > spool.maximum {
		frames := compactOutputSpoolFrames(spool.header, spool.frames, spool.maximum-int64(len(closeFrame)))
		if len(frames) == 0 {
			_ = spool.file.Close()
			return fmt.Errorf("%w: output spool cannot retain a close frame", ErrOutputSpoolCorrupt)
		}
		spool.frames = frames
		spool.header.baseCursor = frames[0].start
		if err := spool.rewriteLocked(frames, spool.header.baseCursor); err != nil {
			_ = spool.file.Close()
			return err
		}
	}
	if _, err := spool.file.Write(closeFrame); err != nil {
		_ = spool.file.Close()
		return fmt.Errorf("write output spool close frame: %w", err)
	}
	if err := spool.file.Sync(); err != nil {
		_ = spool.file.Close()
		return fmt.Errorf("sync output spool close frame: %w", err)
	}
	spool.closed = true
	return spool.file.Close()
}

// rewriteLocked replaces one spool atomically so readers never observe a partially compacted transcript.
func (spool *outputSpool) rewriteLocked(frames []outputSpoolFrame, baseCursor uint64) error {
	if spool.file != nil {
		if err := spool.file.Close(); err != nil {
			return fmt.Errorf("close output spool before rewrite: %w", err)
		}
	}
	temporary, err := os.CreateTemp(filepath.Dir(spool.path), ".output-spool-*")
	if err != nil {
		return fmt.Errorf("create output spool rewrite: %w", err)
	}
	temporaryPath := temporary.Name()
	cleanup := func() {
		_ = temporary.Close()
		_ = os.Remove(temporaryPath)
	}
	if err := temporary.Chmod(0o600); err != nil {
		cleanup()
		return fmt.Errorf("secure output spool rewrite: %w", err)
	}
	header := spool.header
	header.baseCursor = baseCursor
	headerBytes, err := encodeOutputSpoolHeader(header)
	if err == nil {
		_, err = temporary.Write(headerBytes)
	}
	for _, frame := range frames {
		if err != nil {
			break
		}
		encoded, encodeErr := encodeOutputSpoolFrame(outputSpoolFrameOutput, frame.stream, frame.start, frame.bytes)
		if encodeErr != nil {
			err = encodeErr
			break
		}
		_, err = temporary.Write(encoded)
	}
	if err == nil {
		err = temporary.Sync()
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(temporaryPath)
		return fmt.Errorf("write output spool rewrite: %w", err)
	}
	if err := replaceOutputSpool(temporaryPath, spool.path); err != nil {
		_ = os.Remove(temporaryPath)
		return fmt.Errorf("publish output spool rewrite: %w", err)
	}
	file, err := os.OpenFile(spool.path, os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("reopen output spool after rewrite: %w", err)
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return fmt.Errorf("secure rewritten output spool: %w", err)
	}
	if _, err := file.Seek(0, io.SeekEnd); err != nil {
		_ = file.Close()
		return fmt.Errorf("seek rewritten output spool: %w", err)
	}
	spool.file = file
	return nil
}

// encodeOutputSpoolHeader serializes the identity envelope and checksum.
func encodeOutputSpoolHeader(header outputSpoolHeader) ([]byte, error) {
	if err := header.projectID.Validate(); err != nil {
		return nil, err
	}
	if err := header.sessionID.Validate(); err != nil {
		return nil, err
	}
	projectBytes := []byte(header.projectID)
	sessionBytes := []byte(header.sessionID)
	if len(projectBytes) > 256 || len(sessionBytes) > 256 {
		return nil, fmt.Errorf("output spool identity exceeds header capacity")
	}
	prefix := make([]byte, outputSpoolHeaderPrefixBytes)
	copy(prefix[:8], outputSpoolMagic)
	binary.BigEndian.PutUint16(prefix[8:10], outputSpoolVersion)
	binary.BigEndian.PutUint16(prefix[10:12], uint16(len(projectBytes)))
	binary.BigEndian.PutUint16(prefix[12:14], uint16(len(sessionBytes)))
	binary.BigEndian.PutUint64(prefix[16:24], header.baseCursor)
	identities := append(append([]byte(nil), projectBytes...), sessionBytes...)
	checksumInput := append(append([]byte(nil), prefix...), identities...)
	checksum := make([]byte, 4)
	binary.BigEndian.PutUint32(checksum, crc32.ChecksumIEEE(checksumInput))
	return append(append(checksumInput, checksum...), nil...), nil
}

// encodeOutputSpoolFrame serializes one output or close record with a CRC over its complete contents.
func encodeOutputSpoolFrame(kind, stream byte, start uint64, payload []byte) ([]byte, error) {
	if kind == outputSpoolFrameOutput {
		if stream != outputSpoolStreamStdout && stream != outputSpoolStreamStderr {
			return nil, fmt.Errorf("invalid output spool stream %d", stream)
		}
		if len(payload) == 0 || len(payload) > outputSpoolMaximumPayloadBytes || !utf8OutputPayloadValid(payload) {
			return nil, fmt.Errorf("invalid output spool payload")
		}
	} else if kind == outputSpoolFrameClosed {
		if stream != 0 || len(payload) != 0 {
			return nil, fmt.Errorf("invalid output spool close frame")
		}
	} else {
		return nil, fmt.Errorf("invalid output spool frame kind %d", kind)
	}
	header := make([]byte, outputSpoolFrameHeaderBytes)
	copy(header[:4], "HOF1")
	header[4] = kind
	header[5] = stream
	binary.BigEndian.PutUint64(header[8:16], start)
	binary.BigEndian.PutUint32(header[16:20], uint32(len(payload)))
	checksumInput := append(append([]byte(nil), header[:20]...), payload...)
	binary.BigEndian.PutUint32(header[20:24], crc32.ChecksumIEEE(checksumInput))
	return append(header, payload...), nil
}

// outputSpoolStream converts the internal relay stream to its stable persisted vocabulary.
func outputSpoolStream(stream outputStream) (byte, error) {
	switch stream {
	case outputStreamStdout:
		return outputSpoolStreamStdout, nil
	case outputStreamStderr:
		return outputSpoolStreamStderr, nil
	default:
		return 0, fmt.Errorf("unknown output stream %d", stream)
	}
}

// outputSpoolEncodedBytes bounds the complete on-disk size of a candidate frame set.
func outputSpoolEncodedBytes(header outputSpoolHeader, frames []outputSpoolFrame) int64 {
	encoded := int64(outputSpoolHeaderPrefixBytes + len(header.projectID) + len(header.sessionID) + 4)
	for _, frame := range frames {
		encoded += int64(outputSpoolFrameHeaderBytes + len(frame.bytes))
	}
	return encoded
}

// compactOutputSpoolFrames keeps the newest complete frames while preserving their absolute cursors.
func compactOutputSpoolFrames(header outputSpoolHeader, frames []outputSpoolFrame, maximum int64) []outputSpoolFrame {
	for len(frames) > 0 && outputSpoolEncodedBytes(header, frames) > maximum {
		frames = frames[1:]
	}
	return append([]outputSpoolFrame(nil), frames...)
}

// utf8OutputPayloadValid recognizes normalized output without accepting a cursor-changing replacement during replay.
func utf8OutputPayloadValid(payload []byte) bool {
	return utf8.Valid(payload)
}
