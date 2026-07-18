package ticketredeemer

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/helper/ticketauth"
	"github.com/goforj/harbor/internal/host/ownership"
	"github.com/goforj/harbor/internal/platform/machinepaths"
)

var (
	// ErrUnsafePath identifies a ticket-spool object that crossed its installer-provisioned security boundary.
	ErrUnsafePath = errors.New("unsafe helper ticket redemption path")
	// ErrReferenceConsumed means the reference crossed the single-use boundary and cannot be retried safely.
	ErrReferenceConsumed = errors.New("helper ticket reference was consumed")
	// ErrClaimDurabilityUncertain identifies a consumed reference whose directory barriers did not confirm persistence.
	ErrClaimDurabilityUncertain = errors.New("helper ticket claim durability is uncertain")
)

// ownershipObserver is the narrow protected-state view needed after a reference has been consumed.
type ownershipObserver interface {
	Observe(context.Context) (ownership.Observation, error)
	Close() error
}

// ownershipOpen keeps fixed production path resolution separate from fault-focused tests.
type ownershipOpen func(string) (ownershipObserver, error)

// fileOperations groups post-open mutation seams without allowing production callers to select filesystem paths.
type fileOperations struct {
	openPending func(*os.File, string, string) (*os.File, error)
	openClaim   func(*os.File, string, string) (*os.File, error)
	entryExists func(*os.File, string, string) (bool, error)
	rename      func(*os.File, *os.File, *os.File, string, string) (bool, error)
	secureClaim func(*os.File) error
	syncFile    func(*os.File) error
	syncDir     func(*os.File) error
	closeFile   func(*os.File) error
	read        func(*os.File, int64) ([]byte, error)
}

// dependencies fixes trusted time and protected storage operations for one Redeemer.
type dependencies struct {
	clock         helper.Clock
	admitProcess  func() error
	openOwnership ownershipOpen
	files         fileOperations
}

// topology retains every directory handle that participates in a pending-to-claimed transition.
type topology struct {
	paths             machinepaths.Paths
	root              *os.File
	tickets           *os.File
	pending           *os.File
	claims            *os.File
	state             *os.File
	requesterIdentity string
}

// Redeemer consumes references through retained handles rooted at Harbor's compiled machine layout.
type Redeemer struct {
	topology     *topology
	ownership    ownershipObserver
	dependencies dependencies
	stateMu      sync.RWMutex
	closed       bool
}

// OpenDefault opens Harbor's installer-provisioned ticket and ownership layout.
func OpenDefault() (*Redeemer, error) {
	paths, err := machinepaths.Resolve()
	if err != nil {
		return nil, fmt.Errorf("resolve helper ticket redemption paths: %w", err)
	}
	return open(paths, defaultDependencies())
}

// open retains the complete fixed-shape topology before opening protected ownership state.
func open(paths machinepaths.Paths, dependencies dependencies) (*Redeemer, error) {
	if err := validateDependencies(dependencies); err != nil {
		return nil, err
	}
	if err := dependencies.admitProcess(); err != nil {
		return nil, fmt.Errorf("admit privileged helper process: %w", err)
	}
	if err := validateLayout(paths); err != nil {
		return nil, err
	}
	topology, err := openTopology(paths)
	if err != nil {
		return nil, err
	}
	ownershipStore, err := dependencies.openOwnership(paths.OwnershipPath)
	if err != nil {
		return nil, errors.Join(
			fmt.Errorf("open helper ticket ownership record: %w", err),
			topology.close(),
		)
	}
	if err := topology.validate(); err != nil {
		return nil, errors.Join(
			fmt.Errorf("revalidate helper ticket topology after opening ownership: %w", err),
			ownershipStore.Close(),
			topology.close(),
		)
	}
	return &Redeemer{
		topology:     topology,
		ownership:    ownershipStore,
		dependencies: dependencies,
	}, nil
}

// Close releases retained handles without removing pending or consumed references.
func (redeemer *Redeemer) Close() error {
	redeemer.stateMu.Lock()
	defer redeemer.stateMu.Unlock()
	if redeemer.closed {
		return nil
	}
	redeemer.closed = true
	return errors.Join(redeemer.ownership.Close(), redeemer.topology.close())
}

// Redeem atomically consumes one opaque reference before authenticating its immutable in-memory ticket snapshot.
func (redeemer *Redeemer) Redeem(ctx context.Context, reference helper.TicketReference) (redemption helper.TicketRedemption, redemptionErr error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := reference.Validate(); err != nil {
		return helper.TicketRedemption{}, redemptionFailed("validate ticket reference", err)
	}
	if err := ctx.Err(); err != nil {
		return helper.TicketRedemption{}, redemptionFailed("begin ticket redemption", err)
	}

	redeemer.stateMu.RLock()
	defer redeemer.stateMu.RUnlock()
	if redeemer.closed {
		return helper.TicketRedemption{}, redemptionFailed("redeem ticket", errors.New("redeemer is closed"))
	}
	if err := redeemer.topology.validate(); err != nil {
		return helper.TicketRedemption{}, redemptionFailed("validate ticket topology", err)
	}

	claimed, err := redeemer.claim(ctx, reference)
	if err != nil {
		return helper.TicketRedemption{}, err
	}
	defer func() {
		if closeErr := redeemer.dependencies.files.closeFile(claimed); closeErr != nil {
			redemption = helper.TicketRedemption{}
			redemptionErr = errors.Join(redemptionErr, consumedFailure("close claimed ticket", closeErr))
		}
	}()

	encoded, err := redeemer.dependencies.files.read(claimed, ticketauth.MaxEnvelopeBytes)
	if err != nil {
		return helper.TicketRedemption{}, consumedFailure("read claimed ticket", err)
	}
	envelope, err := ticketauth.Decode(encoded)
	if err != nil {
		return helper.TicketRedemption{}, consumedFailure("decode claimed ticket", err)
	}
	if err := ctx.Err(); err != nil {
		return helper.TicketRedemption{}, consumedFailure("authenticate claimed ticket", err)
	}

	observation, err := redeemer.ownership.Observe(ctx)
	if err != nil {
		return helper.TicketRedemption{}, consumedFailure("observe machine ownership", err)
	}
	if !observation.Exists {
		return helper.TicketRedemption{}, consumedFailure("observe machine ownership", errors.New("machine ownership is not claimed"))
	}
	record := observation.Record
	if record.OwnerIdentity != redeemer.topology.requesterIdentity {
		return helper.TicketRedemption{}, consumedFailure(
			"bind ticket requester",
			fmt.Errorf("pending owner %q does not match machine owner %q", redeemer.topology.requesterIdentity, record.OwnerIdentity),
		)
	}
	verifierKey, err := decodeVerifierKey(record.TicketVerifierKey)
	if err != nil {
		return helper.TicketRedemption{}, consumedFailure("decode machine ticket verifier", err)
	}

	now := redeemer.dependencies.clock.Now().UTC()
	ticket, err := verifyEnvelope(envelope, verifierKey, now)
	if err != nil {
		return helper.TicketRedemption{}, errors.Join(ErrReferenceConsumed, err)
	}
	if ticket.OwnershipGeneration != record.Generation {
		return helper.TicketRedemption{}, errors.Join(
			helper.ErrTicketReferenceStale,
			ErrReferenceConsumed,
			fmt.Errorf("ticket ownership generation %d does not match current generation %d", ticket.OwnershipGeneration, record.Generation),
		)
	}
	if ticket.RequesterIdentity != redeemer.topology.requesterIdentity ||
		ticket.InstallationID != record.InstallationID ||
		ticket.ApprovedPool != record.LoopbackPoolPrefix {
		return helper.TicketRedemption{}, consumedFailure("bind ticket to machine ownership", errors.New("signed ticket does not match protected ownership dimensions"))
	}

	admission := helper.TicketAdmission{
		TicketReference:     reference,
		RequesterIdentity:   redeemer.topology.requesterIdentity,
		InstallationID:      record.InstallationID,
		OwnershipGeneration: record.Generation,
		ApprovedPool:        record.LoopbackPoolPrefix,
	}
	return helper.TicketRedemption{Ticket: ticket, Admission: admission}, nil
}

// claim moves one pending direct file into the protected permanent namespace before any ticket bytes are trusted.
func (redeemer *Redeemer) claim(ctx context.Context, reference helper.TicketReference) (claimed *os.File, claimErr error) {
	name := string(reference)
	pending, err := redeemer.dependencies.files.openPending(redeemer.topology.pending, redeemer.topology.paths.PendingDirectory, name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, redeemer.classifyAbsentReference(name)
	}
	if err != nil {
		return nil, redemptionFailed("open pending ticket", err)
	}
	pendingOpen := true
	referenceConsumed := false
	closePending := func() error {
		if !pendingOpen {
			return nil
		}
		pendingOpen = false
		return redeemer.dependencies.files.closeFile(pending)
	}
	defer func() {
		if closeErr := closePending(); closeErr != nil {
			if referenceConsumed {
				claimErr = errors.Join(claimErr, consumedFailure("close pending ticket handle", closeErr))
			} else {
				claimErr = errors.Join(claimErr, redemptionFailed("close pending ticket handle", closeErr))
			}
		}
	}()
	if err := validatePlatformPendingFile(pending, redeemer.topology.requesterIdentity); err != nil {
		if raceErr, classified := redeemer.classifyConsumedReference(name); classified {
			return nil, raceErr
		}
		return nil, redemptionFailed("validate pending ticket", errors.Join(ErrUnsafePath, err))
	}
	if err := validatePendingEnvelopeFile(pending); err != nil {
		return nil, redemptionFailed("validate pending ticket size", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, redemptionFailed("claim pending ticket", err)
	}

	applied, renameErr := redeemer.dependencies.files.rename(
		redeemer.topology.pending,
		redeemer.topology.claims,
		pending,
		name,
		name,
	)
	if !applied {
		if errors.Is(renameErr, fs.ErrExist) {
			return nil, helper.ErrTicketReferenceRedeemed
		}
		if errors.Is(renameErr, fs.ErrNotExist) {
			return nil, redeemer.classifyAbsentReference(name)
		}
		if renameErr == nil {
			renameErr = errors.New("claim transition reported no applied result")
		}
		return nil, redemptionFailed("claim pending ticket", renameErr)
	}
	referenceConsumed = true

	claimed, err = redeemer.dependencies.files.openClaim(redeemer.topology.claims, redeemer.topology.paths.ClaimsDirectory, name)
	if err != nil {
		return nil, consumedFailure("open claimed ticket", errors.Join(renameErr, err))
	}
	claimedInfo, claimedStatErr := claimed.Stat()
	pendingInfo, pendingStatErr := pending.Stat()
	if claimedStatErr != nil || pendingStatErr != nil || !os.SameFile(claimedInfo, pendingInfo) {
		return nil, errors.Join(
			consumedFailure("validate claimed ticket identity", errors.New("claimed name does not identify the opened pending object")),
			claimedStatErr,
			pendingStatErr,
			redeemer.dependencies.files.closeFile(claimed),
		)
	}
	if err := validatePlatformPendingFile(claimed, redeemer.topology.requesterIdentity); err != nil {
		return nil, errors.Join(
			consumedFailure("validate claimed ticket source policy", errors.Join(ErrUnsafePath, err)),
			redeemer.dependencies.files.closeFile(claimed),
		)
	}
	if err := validatePendingEnvelopeFile(claimed); err != nil {
		return nil, errors.Join(
			consumedFailure("validate claimed ticket size", err),
			redeemer.dependencies.files.closeFile(claimed),
		)
	}
	if err := redeemer.dependencies.files.secureClaim(claimed); err != nil {
		return nil, errors.Join(consumedFailure("protect claimed ticket", err), redeemer.dependencies.files.closeFile(claimed))
	}
	if err := validatePlatformMachineFile(claimed); err != nil {
		return nil, errors.Join(
			consumedFailure("validate protected claimed ticket", errors.Join(ErrUnsafePath, err)),
			redeemer.dependencies.files.closeFile(claimed),
		)
	}
	if err := redeemer.dependencies.files.syncFile(claimed); err != nil {
		return nil, errors.Join(consumedFailure("sync protected claimed ticket", err), redeemer.dependencies.files.closeFile(claimed))
	}
	if err := errors.Join(
		redeemer.dependencies.files.syncDir(redeemer.topology.pending),
		redeemer.dependencies.files.syncDir(redeemer.topology.claims),
	); err != nil {
		return nil, errors.Join(claimDurabilityFailure("sync ticket claim directories", err), redeemer.dependencies.files.closeFile(claimed))
	}
	if renameErr != nil {
		return nil, errors.Join(claimDurabilityFailure("confirm ticket claim", renameErr), redeemer.dependencies.files.closeFile(claimed))
	}
	if err := closePending(); err != nil {
		return nil, errors.Join(consumedFailure("close pending ticket handle", err), redeemer.dependencies.files.closeFile(claimed))
	}
	if err := ctx.Err(); err != nil {
		return nil, errors.Join(consumedFailure("finish ticket claim", err), redeemer.dependencies.files.closeFile(claimed))
	}
	return claimed, nil
}

// validatePendingEnvelopeFile bounds disk content before and after the atomic name transition.
func validatePendingEnvelopeFile(file *os.File) error {
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if info.Size() <= 0 || info.Size() > ticketauth.MaxEnvelopeBytes {
		return fmt.Errorf("pending ticket size is %d, want between 1 and %d bytes", info.Size(), ticketauth.MaxEnvelopeBytes)
	}
	return nil
}

// classifyConsumedReference gives the protected consumed namespace precedence over any recreated pending name.
func (redeemer *Redeemer) classifyConsumedReference(name string) (error, bool) {
	claimedExists, err := redeemer.dependencies.files.entryExists(
		redeemer.topology.claims,
		redeemer.topology.paths.ClaimsDirectory,
		name,
	)
	if err != nil {
		return redemptionFailed("classify concurrent ticket claim", err), true
	}
	if claimedExists {
		return helper.ErrTicketReferenceRedeemed, true
	}
	return nil, false
}

// classifyAbsentReference distinguishes a never-published capability from a permanently retained claim.
func (redeemer *Redeemer) classifyAbsentReference(name string) error {
	exists, err := redeemer.dependencies.files.entryExists(redeemer.topology.claims, redeemer.topology.paths.ClaimsDirectory, name)
	if err != nil {
		return redemptionFailed("inspect claimed ticket reference", err)
	}
	if exists {
		return helper.ErrTicketReferenceRedeemed
	}
	return helper.ErrTicketReferenceUnknown
}

// verifyEnvelope authenticates expired tickets at their last valid instant before classifying them as stale.
func verifyEnvelope(envelope ticketauth.Envelope, verifierKey ed25519.PublicKey, now time.Time) (helper.Ticket, error) {
	ticket, err := envelope.Verify(verifierKey, now)
	if err == nil {
		return ticket, nil
	}
	expiry := envelope.Ticket.ExpiresAt
	if !expiry.IsZero() && !expiry.After(now) {
		if _, historicalErr := envelope.Verify(verifierKey, expiry.Add(-time.Nanosecond)); historicalErr == nil {
			return helper.Ticket{}, errors.Join(helper.ErrTicketReferenceStale, err)
		}
	}
	return helper.Ticket{}, redemptionFailed("verify claimed ticket", err)
}

// decodeVerifierKey independently reconstructs the exact Ed25519 key pinned in protected ownership state.
func decodeVerifierKey(encoded string) (ed25519.PublicKey, error) {
	decoded, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil || len(decoded) != ed25519.PublicKeySize || base64.StdEncoding.EncodeToString(decoded) != encoded {
		return nil, errors.New("machine ticket verifier key is invalid")
	}
	return ed25519.PublicKey(decoded), nil
}

// readBounded rejects metadata and stream sizes outside the canonical envelope limit.
func readBounded(file *os.File, maximum int64) ([]byte, error) {
	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat claimed ticket: %w", err)
	}
	if info.Size() <= 0 || info.Size() > maximum {
		return nil, fmt.Errorf("claimed ticket size is %d, want between 1 and %d bytes", info.Size(), maximum)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("seek claimed ticket: %w", err)
	}
	content, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil {
		return nil, fmt.Errorf("read claimed ticket: %w", err)
	}
	if int64(len(content)) > maximum {
		return nil, fmt.Errorf("claimed ticket exceeds %d bytes", maximum)
	}
	return content, nil
}

// redemptionFailed preserves internal evidence while exposing the stable authentication-failure class.
func redemptionFailed(operation string, err error) error {
	return errors.Join(helper.ErrTicketRedemptionFailed, fmt.Errorf("%s: %w", operation, err))
}

// consumedFailure records that retry is unsafe because the reference already crossed into claims.
func consumedFailure(operation string, err error) error {
	return errors.Join(
		helper.ErrTicketRedemptionFailed,
		ErrReferenceConsumed,
		fmt.Errorf("%s: %w", operation, err),
	)
}

// claimDurabilityFailure marks only rename or directory-barrier outcomes that cannot prove crash persistence.
func claimDurabilityFailure(operation string, err error) error {
	return errors.Join(consumedFailure(operation, err), ErrClaimDurabilityUncertain)
}

// validateDependencies rejects partial test seams before any protected path is opened.
func validateDependencies(dependencies dependencies) error {
	if dependencies.clock == nil || dependencies.admitProcess == nil || dependencies.openOwnership == nil ||
		dependencies.files.openPending == nil || dependencies.files.openClaim == nil ||
		dependencies.files.entryExists == nil || dependencies.files.rename == nil ||
		dependencies.files.secureClaim == nil || dependencies.files.syncFile == nil ||
		dependencies.files.syncDir == nil || dependencies.files.closeFile == nil ||
		dependencies.files.read == nil {
		return errors.New("open helper ticket redeemer: dependencies are incomplete")
	}
	return nil
}

// defaultDependencies binds production redemption to reviewed native filesystem primitives.
func defaultDependencies() dependencies {
	return dependencies{
		clock:         helper.SystemClock{},
		admitProcess:  validatePlatformProcessAdmission,
		openOwnership: func(path string) (ownershipObserver, error) { return ownership.NewStore(path) },
		files: fileOperations{
			openPending: openPlatformFile,
			openClaim:   openPlatformFile,
			entryExists: platformEntryExists,
			rename:      renamePlatformNoReplace,
			secureClaim: securePlatformClaim,
			syncFile:    func(file *os.File) error { return file.Sync() },
			syncDir:     syncPlatformDirectory,
			closeFile:   func(file *os.File) error { return file.Close() },
			read:        readBounded,
		},
	}
}

// validateLayout admits only the fixed machinepaths shape even through private test construction.
func validateLayout(paths machinepaths.Paths) error {
	values := []struct {
		name string
		got  string
		want string
	}{
		{name: "state directory", got: paths.StateDirectory, want: filepath.Join(paths.Root, "state")},
		{name: "replay directory", got: paths.ReplayDirectory, want: filepath.Join(paths.Root, "state", "replay")},
		{name: "ownership path", got: paths.OwnershipPath, want: filepath.Join(paths.Root, "state", "ownership.json")},
		{name: "tickets directory", got: paths.TicketsDirectory, want: filepath.Join(paths.Root, "tickets")},
		{name: "pending directory", got: paths.PendingDirectory, want: filepath.Join(paths.Root, "tickets", "pending")},
		{name: "claims directory", got: paths.ClaimsDirectory, want: filepath.Join(paths.Root, "tickets", "claims")},
	}
	if paths.Root == "" || !filepath.IsAbs(paths.Root) || filepath.Clean(paths.Root) != paths.Root {
		return fmt.Errorf("open helper ticket redeemer: root %q is not absolute and canonical", paths.Root)
	}
	for _, value := range values {
		if value.got != value.want || !filepath.IsAbs(value.got) || filepath.Clean(value.got) != value.got {
			return fmt.Errorf("open helper ticket redeemer: %s %q does not match fixed path %q", value.name, value.got, value.want)
		}
	}
	return nil
}

// openTopology resolves each fixed child relative to its retained protected parent.
func openTopology(paths machinepaths.Paths) (*topology, error) {
	root, err := openPlatformRootDirectory(paths.Root)
	if err != nil {
		return nil, fmt.Errorf("open helper ticket root: %w", err)
	}
	topology := &topology{paths: paths, root: root}
	failed := true
	defer func() {
		if failed {
			_ = topology.close()
		}
	}()

	topology.tickets, err = openPlatformDirectory(root, paths.Root, "tickets")
	if err != nil {
		return nil, fmt.Errorf("open helper tickets directory: %w", err)
	}
	topology.pending, err = openPlatformDirectory(topology.tickets, paths.TicketsDirectory, "pending")
	if err != nil {
		return nil, fmt.Errorf("open pending tickets directory: %w", err)
	}
	topology.claims, err = openPlatformDirectory(topology.tickets, paths.TicketsDirectory, "claims")
	if err != nil {
		return nil, fmt.Errorf("open claimed tickets directory: %w", err)
	}
	topology.state, err = openPlatformDirectory(root, paths.Root, "state")
	if err != nil {
		return nil, fmt.Errorf("open helper state directory: %w", err)
	}
	topology.requesterIdentity, err = platformPendingIdentity(topology.pending)
	if err != nil {
		return nil, fmt.Errorf("derive admitted ticket requester: %w", err)
	}
	if err := topology.validateRetained(); err != nil {
		return nil, err
	}
	failed = false
	return topology, nil
}

// validate reopens every fixed edge so an absolute or child name swap cannot redirect retained authority.
func (topology *topology) validate() error {
	root, err := openPlatformRootDirectory(topology.paths.Root)
	if err != nil {
		return errors.Join(ErrUnsafePath, fmt.Errorf("reopen helper ticket root: %w", err))
	}
	defer root.Close()
	if err := sameOpenedObject(root, topology.root, "helper ticket root"); err != nil {
		return err
	}
	checks := []struct {
		parent     *os.File
		parentPath string
		name       string
		retained   *os.File
	}{
		{parent: topology.root, parentPath: topology.paths.Root, name: "tickets", retained: topology.tickets},
		{parent: topology.root, parentPath: topology.paths.Root, name: "state", retained: topology.state},
		{parent: topology.tickets, parentPath: topology.paths.TicketsDirectory, name: "pending", retained: topology.pending},
		{parent: topology.tickets, parentPath: topology.paths.TicketsDirectory, name: "claims", retained: topology.claims},
	}
	for _, check := range checks {
		opened, err := openPlatformDirectory(check.parent, check.parentPath, check.name)
		if err != nil {
			return errors.Join(ErrUnsafePath, fmt.Errorf("reopen %s: %w", check.name, err))
		}
		sameErr := sameOpenedObject(opened, check.retained, check.name)
		closeErr := opened.Close()
		if sameErr != nil || closeErr != nil {
			return errors.Join(sameErr, closeErr)
		}
	}
	return topology.validateRetained()
}

// validateRetained applies exact gateway, interactive, and machine policies to the already opened topology.
func (topology *topology) validateRetained() error {
	if err := validatePlatformGatewayDirectory(topology.root, topology.requesterIdentity); err != nil {
		return errors.Join(ErrUnsafePath, fmt.Errorf("validate helper ticket root: %w", err))
	}
	if err := validatePlatformGatewayDirectory(topology.tickets, topology.requesterIdentity); err != nil {
		return errors.Join(ErrUnsafePath, fmt.Errorf("validate helper tickets directory: %w", err))
	}
	identity, err := platformPendingIdentity(topology.pending)
	if err != nil {
		return errors.Join(ErrUnsafePath, fmt.Errorf("validate pending tickets directory: %w", err))
	}
	if identity != topology.requesterIdentity {
		return errors.Join(ErrUnsafePath, errors.New("pending ticket owner changed after admission"))
	}
	if err := validatePlatformMachineDirectory(topology.claims); err != nil {
		return errors.Join(ErrUnsafePath, fmt.Errorf("validate claimed tickets directory: %w", err))
	}
	if err := validatePlatformMachineDirectory(topology.state); err != nil {
		return errors.Join(ErrUnsafePath, fmt.Errorf("validate helper state directory: %w", err))
	}
	if err := validatePlatformTopology(topology.tickets, topology.pending, topology.claims); err != nil {
		return errors.Join(ErrUnsafePath, fmt.Errorf("validate ticket filesystem topology: %w", err))
	}
	return nil
}

// close releases topology handles in leaf-to-root order.
func (topology *topology) close() error {
	if topology == nil {
		return nil
	}
	var closeErr error
	for _, file := range []*os.File{topology.pending, topology.claims, topology.state, topology.tickets, topology.root} {
		if file != nil {
			closeErr = errors.Join(closeErr, file.Close())
		}
	}
	return closeErr
}

// sameOpenedObject compares stable operating-system file identity rather than mutable path text.
func sameOpenedObject(opened *os.File, retained *os.File, label string) error {
	openedInfo, err := opened.Stat()
	if err != nil {
		return errors.Join(ErrUnsafePath, fmt.Errorf("stat reopened %s: %w", label, err))
	}
	retainedInfo, err := retained.Stat()
	if err != nil {
		return errors.Join(ErrUnsafePath, fmt.Errorf("stat retained %s: %w", label, err))
	}
	if !os.SameFile(openedInfo, retainedInfo) {
		return errors.Join(ErrUnsafePath, fmt.Errorf("%s changed after it was opened", label))
	}
	return nil
}
