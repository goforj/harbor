package ticketspool

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"time"

	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/helper/ticketauth"
)

const (
	// MaximumCleanupEntries caps the amount of pending-spool work accepted by one cleanup call.
	MaximumCleanupEntries = 128
)

var (
	// ErrCleanupDurabilityUncertain means expired tickets were removed but the directory persistence barrier failed.
	ErrCleanupDurabilityUncertain = errors.New("helper ticket cleanup durability is uncertain")
)

// CleanupResult reports the bounded pending-spool work completed by CleanupExpired.
type CleanupResult struct {
	// Scanned is the number of directory entries inspected during this call.
	Scanned int
	// Removed is the number of authenticated expired ticket references successfully deleted.
	Removed int
	// Preserved is the number of inspected entries not successfully deleted by this call.
	Preserved int
	// LimitReached reports that at least one directory entry remains beyond the requested scan limit.
	// Cleanup does not retain a scan cursor, so preserved entries may occupy the same bounded window on a later call.
	LimitReached bool
}

// CleanupExpired removes only pending references authenticated by the expected verifier key and proven expired.
// Current tickets, claimed tickets, staging files, malformed entries, and tickets signed by other keys are preserved.
// Each call is a bounded best-effort scan from the directory's beginning; LimitReached requires caller observability
// because repeated calls are not guaranteed to progress past a window filled entirely by preserved entries.
func (publisher *Publisher) CleanupExpired(ctx context.Context, expectedVerifierKey ed25519.PublicKey, limit int) (result CleanupResult, operationErr error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return CleanupResult{}, err
	}
	if len(expectedVerifierKey) != ed25519.PublicKeySize {
		return CleanupResult{}, errors.New("cleanup helper tickets: expected Ed25519 verifier key is invalid")
	}
	if limit <= 0 || limit > MaximumCleanupEntries {
		return CleanupResult{}, fmt.Errorf("cleanup helper tickets: limit must be between 1 and %d", MaximumCleanupEntries)
	}
	verifierKey := append(ed25519.PublicKey(nil), expectedVerifierKey...)

	publisher.stateMu.Lock()
	defer publisher.stateMu.Unlock()
	if publisher.closed {
		return CleanupResult{}, errors.New("cleanup helper tickets: publisher is closed")
	}
	if err := publisher.validateRoot(); err != nil {
		return CleanupResult{}, err
	}

	entries, limitReached, err := publisher.cleanupEntries(limit)
	if err != nil {
		return CleanupResult{}, err
	}
	result.LimitReached = limitReached
	now := publisher.dependencies.clock.Now().UTC()
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			operationErr = err
			break
		}
		result.Scanned++
		reference := helper.TicketReference(entry.Name())
		if err := reference.Validate(); err != nil {
			result.Preserved++
			continue
		}

		removed, err := publisher.cleanupReference(ctx, reference, verifierKey, now)
		if removed {
			result.Removed++
		} else {
			result.Preserved++
		}
		if err != nil {
			operationErr = fmt.Errorf("cleanup helper ticket reference %q: %w", reference, err)
			break
		}
	}

	if result.Removed > 0 {
		if err := publisher.dependencies.files.syncDirectory(publisher.directory); err != nil {
			operationErr = errors.Join(
				operationErr,
				ErrCleanupDurabilityUncertain,
				fmt.Errorf("sync helper ticket spool after cleanup: %w", err),
			)
		}
	}
	return result, operationErr
}

// cleanupEntries opens an independent retained-directory cursor and reads at most one entry beyond the caller's limit.
func (publisher *Publisher) cleanupEntries(limit int) (entries []os.DirEntry, limitReached bool, operationErr error) {
	directory, err := publisher.root.Open(".")
	if err != nil {
		return nil, false, fmt.Errorf("open helper ticket spool for cleanup: %w", err)
	}
	defer func() {
		operationErr = errors.Join(operationErr, publisher.dependencies.files.closeFile(directory))
	}()

	retainedInfo, retainedErr := publisher.directory.Stat()
	openedInfo, openedErr := directory.Stat()
	var boundaryErr error
	if openedErr == nil {
		boundaryErr = validatePlatformObject(directory, openedInfo, true)
	}
	if retainedErr != nil || openedErr != nil || boundaryErr != nil || !os.SameFile(retainedInfo, openedInfo) {
		return nil, false, errors.Join(
			fmt.Errorf("%w: helper ticket spool changed while opening cleanup cursor", ErrUnsafePath),
			retainedErr,
			openedErr,
			boundaryErr,
		)
	}

	entries, err = directory.ReadDir(limit + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, false, fmt.Errorf("read helper ticket spool for cleanup: %w", err)
	}
	if len(entries) > limit {
		return entries[:limit], true, nil
	}
	return entries, false, nil
}

// cleanupReference authenticates one immutable pending-file snapshot before deleting its unchanged direct name.
func (publisher *Publisher) cleanupReference(ctx context.Context, reference helper.TicketReference, verifierKey ed25519.PublicKey, now time.Time) (removed bool, operationErr error) {
	name := string(reference)
	file, err := publisher.dependencies.files.reopen(publisher.root, publisher.directory, publisher.path, name)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("open pending ticket: %w", err)
	}
	defer func() {
		operationErr = errors.Join(operationErr, publisher.dependencies.files.closeFile(file))
	}()

	initialInfo, err := file.Stat()
	if err != nil {
		return false, fmt.Errorf("stat pending ticket: %w", err)
	}
	if err := validatePlatformObject(file, initialInfo, false); err != nil {
		return false, fmt.Errorf("%w: validate pending ticket: %v", ErrUnsafePath, err)
	}
	content, eligible, err := readCleanupCandidate(file, initialInfo)
	if err != nil {
		return false, err
	}
	if !eligible {
		return false, nil
	}
	envelope, err := ticketauth.Decode(content)
	if err != nil {
		return false, nil
	}
	expiry := envelope.Ticket.ExpiresAt
	if expiry.IsZero() || expiry.After(now) {
		return false, nil
	}
	if _, err := envelope.Verify(verifierKey, expiry.Add(-time.Nanosecond)); err != nil {
		return false, nil
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if err := publisher.validateRoot(); err != nil {
		return false, err
	}

	namedInfo, err := publisher.root.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("revalidate pending ticket name: %w", err)
	}
	if !os.SameFile(initialInfo, namedInfo) {
		return false, nil
	}
	if err := publisher.dependencies.files.remove(publisher.root, name); errors.Is(err, fs.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, fmt.Errorf("remove authenticated expired pending ticket: %w", err)
	}
	return true, nil
}

// readCleanupCandidate returns one stable bounded snapshot without interpreting untrusted ticket bytes.
func readCleanupCandidate(file *os.File, initialInfo os.FileInfo) ([]byte, bool, error) {
	if initialInfo.Size() <= 0 || initialInfo.Size() > ticketauth.MaxEnvelopeBytes {
		return nil, false, nil
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, false, fmt.Errorf("seek pending ticket: %w", err)
	}
	content, err := io.ReadAll(io.LimitReader(file, ticketauth.MaxEnvelopeBytes+1))
	if err != nil {
		return nil, false, fmt.Errorf("read pending ticket: %w", err)
	}
	if len(content) > ticketauth.MaxEnvelopeBytes {
		return nil, false, nil
	}
	finalInfo, err := file.Stat()
	if err != nil {
		return nil, false, fmt.Errorf("restat pending ticket: %w", err)
	}
	if err := validatePlatformObject(file, finalInfo, false); err != nil {
		return nil, false, fmt.Errorf("%w: revalidate pending ticket: %v", ErrUnsafePath, err)
	}
	if !os.SameFile(initialInfo, finalInfo) || int64(len(content)) != initialInfo.Size() || finalInfo.Size() != initialInfo.Size() {
		return nil, false, nil
	}
	return content, true, nil
}
