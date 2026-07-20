package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/goforj/harbor/desktop/internal/desktopwire"
	"github.com/goforj/harbor/desktop/internal/networkprerequisite"
	"github.com/goforj/harbor/internal/control"
	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/helper/launcher"
	"github.com/goforj/harbor/internal/networkresolverapproval"
	"github.com/goforj/harbor/internal/networksetupapproval"
	"github.com/goforj/harbor/internal/projectapproval"
	"github.com/goforj/harbor/internal/rpc"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

const (
	desktopReconnectDelay        = time.Second
	desktopPollInterval          = 2 * time.Second
	networkSetupIntentBytes      = 16
	connectionEventName          = desktopwire.ConnectionEventName
	snapshotEventName            = desktopwire.SnapshotEventName
	networkSetupIntentID         = domain.IntentID("intent-network-setup")
	networkResolverSetupIntentID = domain.IntentID("intent-network-resolver-setup")
)

var errDaemonDisconnected = errors.New("Harbor daemon is not connected")

// controlClient is the narrow daemon surface the desktop keeps alive between Wails calls.
type controlClient interface {
	Status(context.Context) (control.DaemonStatus, error)
	Snapshot(context.Context) (domain.Snapshot, error)
	RegisterProject(context.Context, control.RegisterProjectRequest) (control.ProjectRegistration, error)
	StartNetworkSetup(context.Context, control.StartNetworkSetupRequest) (control.NetworkSetupOperation, error)
	PrepareNetworkSetupApproval(context.Context, control.PrepareNetworkSetupApprovalRequest) (control.NetworkSetupApprovalPreparation, error)
	ConfirmNetworkSetupApproval(context.Context, control.ConfirmNetworkSetupApprovalRequest) (control.NetworkSetupApprovalConfirmation, error)
	StartNetworkResolverSetup(context.Context, control.StartNetworkResolverSetupRequest) (control.NetworkResolverSetupOperation, error)
	PrepareNetworkResolverSetupApproval(context.Context, control.PrepareNetworkResolverSetupApprovalRequest) (control.NetworkResolverSetupApprovalPreparation, error)
	ConfirmNetworkResolverSetupApproval(context.Context, control.ConfirmNetworkResolverSetupApprovalRequest) (control.NetworkResolverSetupApprovalConfirmation, error)
	InspectProjectRuntimeRepair(context.Context, control.InspectProjectRuntimeRepairRequest) (control.ProjectRuntimeRepairInspection, error)
	ConfirmProjectRuntimeRepair(context.Context, control.ConfirmProjectRuntimeRepairRequest) (control.ProjectRuntimeRepairConfirmation, error)
	ProjectActivity(context.Context, control.ProjectActivityRequest) (control.ProjectActivity, error)
	ServiceLogs(context.Context, control.ServiceLogsRequest) (control.ServiceLogs, error)
	StartProject(context.Context, control.StartProjectRequest) (control.ProjectLifecycleOperation, error)
	StopProject(context.Context, control.StopProjectRequest) (control.ProjectLifecycleOperation, error)
	UnregisterProject(context.Context, control.UnregisterProjectRequest) (control.ProjectUnregistration, error)
	PrepareProjectUnregisterApproval(context.Context, control.PrepareProjectUnregisterApprovalRequest) (control.ProjectUnregisterApprovalPreparation, error)
	ConfirmProjectUnregisterApproval(context.Context, control.ConfirmProjectUnregisterApprovalRequest) (control.ProjectUnregisterApprovalConfirmation, error)
	Done() <-chan struct{}
	Close() error
}

// clientFactory opens one authenticated desktop-role control session.
type clientFactory func(context.Context, control.ClientConfig) (controlClient, error)

// eventEmitter keeps the replaceable raw Wails callback aligned with the typed desktop event boundary.
type eventEmitter = desktopwire.RawEmitter

// resourceOpener delegates a reviewed resource URL to the operating system browser.
type resourceOpener func(context.Context, string)

// directoryChooser keeps the native folder picker deterministic in adapter tests.
type directoryChooser func(context.Context, runtime.OpenDialogOptions) (string, error)

// waitFunc makes reconnect and polling boundaries cancellation-aware and deterministic in tests.
type waitFunc func(context.Context, time.Duration) bool

// networkSetupApprovalRunner performs one exact interactive helper attempt for a daemon-selected setup revision.
type networkSetupApprovalRunner interface {
	Execute(context.Context, networksetupapproval.Request) (networksetupapproval.Outcome, error)
}

// networkSetupApprovalFactory binds the current authenticated desktop session only when the user requests setup.
type networkSetupApprovalFactory func(networksetupapproval.Client) networkSetupApprovalRunner

// networkSetupIntentFactory creates a fresh retry identity without coupling desktop idempotency to process lifetime.
type networkSetupIntentFactory func() (domain.IntentID, error)

// networkResolverSetupApprovalRunner performs one exact interactive resolver helper attempt for a daemon-selected revision.
type networkResolverSetupApprovalRunner interface {
	Execute(context.Context, networkresolverapproval.Request) (networkresolverapproval.Outcome, error)
}

// networkResolverSetupApprovalFactory binds resolver approval to the same authenticated session that selected the operation.
type networkResolverSetupApprovalFactory func(networkresolverapproval.Client) networkResolverSetupApprovalRunner

// projectRemovalApprovalRunner performs one bounded interactive release workflow for an exact unregister revision.
type projectRemovalApprovalRunner interface {
	Execute(context.Context, projectapproval.Request) (projectapproval.Outcome, error)
}

// projectRemovalApprovalFactory binds release approval to the authenticated session that replayed the removal intent.
type projectRemovalApprovalFactory func(projectapproval.Client) projectRemovalApprovalRunner

// networkResolverSetupIntentFactory creates a fresh retry identity after a recoverable resolver setup terminal state.
type networkResolverSetupIntentFactory func() (domain.IntentID, error)

// ConnectionState identifies the desktop backend's current relationship to harbord.
type ConnectionState = desktopwire.ConnectionState

const (
	// ConnectionConnecting means the desktop is opening or negotiating a daemon session.
	ConnectionConnecting ConnectionState = desktopwire.ConnectionConnecting
	// ConnectionConnected means the desktop owns a negotiated daemon session.
	ConnectionConnected ConnectionState = desktopwire.ConnectionConnected
	// ConnectionDisconnected means the last connection attempt or live session ended.
	ConnectionDisconnected ConnectionState = desktopwire.ConnectionDisconnected
)

// ConnectionEvent reports connection lifecycle independently from durable snapshot revisions.
type ConnectionEvent = desktopwire.ConnectionEvent

// snapshotCursor suppresses duplicate revisions only within one negotiated daemon connection.
type snapshotCursor struct {
	sequence    domain.Sequence
	initialized bool
}

// App owns the Wails lifecycle and the desktop's single persistent daemon connection.
type App struct {
	mu                    sync.RWMutex
	activityWaitMu        sync.Mutex
	activityWaitID        uint64
	activityWaitCancel    context.CancelFunc
	serviceLogsWaitMu     sync.Mutex
	serviceLogsWaitID     uint64
	serviceLogsWaitCancel context.CancelFunc
	ctx                   context.Context
	cancel                context.CancelFunc
	done                  chan struct{}
	client                controlClient
	clientLeases          int
	clientDrain           chan struct{}
	clientFactory         clientFactory
	events                desktopwire.Emitter
	open                  resourceOpener
	choose                directoryChooser
	setupApproval         networkSetupApprovalFactory
	resolverApproval      networkResolverSetupApprovalFactory
	projectApproval       projectRemovalApprovalFactory
	setupPrerequisite     networkprerequisite.Ensurer
	setupIntent           networkSetupIntentFactory
	resolverIntent        networkResolverSetupIntentFactory
	presentation          *presentationController
	wait                  waitFunc
	reconnectDelay        time.Duration
	pollInterval          time.Duration
	resourceIconClient    *http.Client
}

// App must remain exactly compatible with the Go-owned Wails method contract.
var _ desktopwire.AppContract = (*App)(nil)

// NewApp creates a desktop client wired to Harbor's local control transport and the Wails runtime.
func NewApp() *App {
	return newApp(newDesktopClient, runtime.EventsEmit, runtime.BrowserOpenURL, waitForContext)
}

// newApp keeps operating-system and timing effects replaceable without making them optional in production.
func newApp(factory clientFactory, emit eventEmitter, open resourceOpener, wait waitFunc) *App {
	return &App{
		clientFactory: factory,
		events:        desktopwire.NewEmitter(emit),
		open:          open,
		choose:        runtime.OpenDirectoryDialog,
		setupApproval: func(client networksetupapproval.Client) networkSetupApprovalRunner {
			return networksetupapproval.New(
				client,
				launcher.New(launcher.NewNativeTransport(), helper.SystemClock{}),
			)
		},
		resolverApproval: func(client networkresolverapproval.Client) networkResolverSetupApprovalRunner {
			return networkresolverapproval.New(
				client,
				launcher.New(launcher.NewNativeTransport(), helper.SystemClock{}),
			)
		},
		projectApproval: func(client projectapproval.Client) projectRemovalApprovalRunner {
			return projectapproval.New(
				client,
				launcher.New(launcher.NewNativeTransport(), helper.SystemClock{}),
			)
		},
		setupPrerequisite: networkprerequisite.New(),
		setupIntent:       newNetworkSetupIntent,
		resolverIntent:    newNetworkResolverSetupIntent,
		presentation:      newPresentationController(runtime.WindowUnminimise, runtime.Show, runtime.Quit),
		wait:              wait,
		reconnectDelay:    desktopReconnectDelay,
		pollInterval:      desktopPollInterval,
		resourceIconClient: &http.Client{},
	}
}

// SetupNetwork completes the address and resolver foundations before reporting Harbor networking ready.
func (a *App) SetupNetwork() (control.NetworkSetupOperation, error) {
	ctx, client, release, err := a.leaseCurrentConnection()
	if err != nil {
		return control.NetworkSetupOperation{}, err
	}
	defer release()

	setup, err := a.completeNetworkSetup(ctx, client)
	if err != nil {
		return control.NetworkSetupOperation{}, err
	}
	if err := a.completeNetworkResolverSetup(ctx, client); err != nil {
		return control.NetworkSetupOperation{}, err
	}
	return setup, nil
}

// completeNetworkSetup starts, replays, or approves the durable loopback pool setup phase.
func (a *App) completeNetworkSetup(ctx context.Context, client controlClient) (control.NetworkSetupOperation, error) {
	intentID := networkSetupIntentID
	setup, err := client.StartNetworkSetup(ctx, control.StartNetworkSetupRequest{IntentID: intentID})
	if networkSetupNeedsFreshIntent(setup, err) {
		intentID, err = a.setupIntent()
		if err != nil {
			return control.NetworkSetupOperation{}, fmt.Errorf("create Harbor network setup retry: %w", err)
		}
		setup, err = client.StartNetworkSetup(ctx, control.StartNetworkSetupRequest{IntentID: intentID})
	}
	if err != nil {
		return control.NetworkSetupOperation{}, fmt.Errorf("start Harbor network setup: %w", err)
	}
	if err := setup.Validate(); err != nil {
		return control.NetworkSetupOperation{}, fmt.Errorf("validate Harbor network setup: %w", err)
	}
	if setup.Operation.IntentID != intentID {
		return control.NetworkSetupOperation{}, fmt.Errorf("validate Harbor network setup: daemon result belongs to another intent")
	}

	switch setup.Operation.State {
	case domain.OperationSucceeded:
		return setup, nil
	case domain.OperationRequiresApproval:
		return a.approveNetworkSetup(ctx, client, setup)
	default:
		return control.NetworkSetupOperation{}, fmt.Errorf("Harbor network setup is %s", setup.Operation.State)
	}
}

// completeNetworkResolverSetup starts, replays, or approves the resolver policy only after the loopback pool exists.
func (a *App) completeNetworkResolverSetup(ctx context.Context, client controlClient) error {
	intentID := networkResolverSetupIntentID
	setup, err := client.StartNetworkResolverSetup(ctx, control.StartNetworkResolverSetupRequest{IntentID: intentID})
	if networkResolverSetupNeedsFreshIntent(setup, err) {
		intentID, err = a.resolverIntent()
		if err != nil {
			return fmt.Errorf("create Harbor network resolver setup retry: %w", err)
		}
		setup, err = client.StartNetworkResolverSetup(ctx, control.StartNetworkResolverSetupRequest{IntentID: intentID})
	}
	if err != nil {
		return fmt.Errorf("start Harbor network resolver setup: %w", err)
	}
	if err := setup.Validate(); err != nil {
		return fmt.Errorf("validate Harbor network resolver setup: %w", err)
	}
	if setup.Operation.IntentID != intentID {
		return errors.New("validate Harbor network resolver setup: daemon result belongs to another intent")
	}

	switch setup.Operation.State {
	case domain.OperationSucceeded:
		return nil
	case domain.OperationRequiresApproval:
		return a.approveNetworkResolverSetup(ctx, client, setup)
	default:
		return fmt.Errorf("Harbor network resolver setup is %s", setup.Operation.State)
	}
}

// networkSetupNeedsFreshIntent recovers only a poisoned singleton retry or an opaque fixed-intent replay failure.
func networkSetupNeedsFreshIntent(setup control.NetworkSetupOperation, err error) bool {
	if err != nil {
		var wireError rpc.WireError
		return errors.As(err, &wireError) && wireError.Code == rpc.ErrorCodeInternal
	}
	if setup.Operation.State == domain.OperationCancelled {
		return true
	}
	return setup.Operation.State == domain.OperationFailed &&
		setup.Operation.Problem != nil &&
		setup.Operation.Problem.Retryable
}

// networkResolverSetupNeedsFreshIntent recovers only a poisoned stable replay or a retryable terminal resolver operation.
func networkResolverSetupNeedsFreshIntent(setup control.NetworkResolverSetupOperation, err error) bool {
	if err != nil {
		var wireError rpc.WireError
		return errors.As(err, &wireError) && wireError.Code == rpc.ErrorCodeInternal
	}
	if setup.Operation.State == domain.OperationCancelled {
		return true
	}
	return setup.Operation.State == domain.OperationFailed &&
		setup.Operation.Problem != nil &&
		setup.Operation.Problem.Retryable
}

// newNetworkSetupIntent creates an independently retryable setup identity from operating-system entropy.
func newNetworkSetupIntent() (domain.IntentID, error) {
	return newNetworkSetupIntentFrom(rand.Reader)
}

// newNetworkSetupIntentFrom keeps entropy failure testable without weakening production identity generation.
func newNetworkSetupIntentFrom(reader io.Reader) (domain.IntentID, error) {
	random := make([]byte, networkSetupIntentBytes)
	if _, err := io.ReadFull(reader, random); err != nil {
		return "", fmt.Errorf("read network setup intent entropy: %w", err)
	}
	intentID := domain.IntentID("intent-network-setup-" + hex.EncodeToString(random))
	if err := intentID.Validate(); err != nil {
		return "", fmt.Errorf("validate network setup intent: %w", err)
	}
	return intentID, nil
}

// newNetworkResolverSetupIntent creates an independently retryable resolver setup identity from operating-system entropy.
func newNetworkResolverSetupIntent() (domain.IntentID, error) {
	return newNetworkResolverSetupIntentFrom(rand.Reader)
}

// newNetworkResolverSetupIntentFrom keeps resolver retry entropy failure visible to the desktop action.
func newNetworkResolverSetupIntentFrom(reader io.Reader) (domain.IntentID, error) {
	random := make([]byte, networkSetupIntentBytes)
	if _, err := io.ReadFull(reader, random); err != nil {
		return "", fmt.Errorf("read network resolver setup intent entropy: %w", err)
	}
	intentID := domain.IntentID("intent-network-resolver-setup-" + hex.EncodeToString(random))
	if err := intentID.Validate(); err != nil {
		return "", fmt.Errorf("validate network resolver setup intent: %w", err)
	}
	return intentID, nil
}

// approveNetworkSetup delegates only the exact daemon-selected revision to the native approval boundary.
func (a *App) approveNetworkSetup(
	ctx context.Context,
	client controlClient,
	setup control.NetworkSetupOperation,
) (control.NetworkSetupOperation, error) {
	request := networksetupapproval.Request{
		OperationID:               setup.Operation.ID,
		ExpectedOperationRevision: setup.Revision,
	}
	runner := a.setupApproval(client)
	outcome, err := runner.Execute(ctx, request)
	if networkSetupNeedsPrerequisiteRepair(outcome, err) {
		repairErr := a.setupPrerequisite.Ensure(ctx)
		if repairErr != nil {
			return control.NetworkSetupOperation{}, fmt.Errorf("install Harbor privileged networking support: %w", repairErr)
		}
		outcome, err = runner.Execute(ctx, request)
		if networkSetupNeedsPrerequisiteRepair(outcome, err) {
			return control.NetworkSetupOperation{}, networkSetupPrerequisiteVerificationError(outcome, err)
		}
	}
	if err != nil {
		return control.NetworkSetupOperation{}, fmt.Errorf("approve Harbor network setup: %w", err)
	}
	if outcome.State != networksetupapproval.Succeeded {
		return control.NetworkSetupOperation{}, networkSetupApprovalError(outcome)
	}
	if outcome.Confirmation == nil || outcome.HelperFailure != nil {
		return control.NetworkSetupOperation{}, fmt.Errorf("approve Harbor network setup: successful approval returned inconsistent evidence")
	}

	confirmation := *outcome.Confirmation
	if err := confirmation.Validate(); err != nil {
		return control.NetworkSetupOperation{}, fmt.Errorf("validate Harbor network setup confirmation: %w", err)
	}
	if confirmation.Operation.ID != setup.Operation.ID ||
		confirmation.Operation.IntentID != setup.Operation.IntentID ||
		confirmation.NetworkRevision != setup.Revision+2 ||
		confirmation.Revision != setup.Revision+3 {
		return control.NetworkSetupOperation{}, fmt.Errorf("validate Harbor network setup confirmation: result crossed the selected operation revision")
	}

	result := control.NetworkSetupOperation{
		Operation: confirmation.Operation,
		Revision:  confirmation.Revision,
	}
	return result, nil
}

// networkSetupNeedsPrerequisiteRepair recognizes only reviewed daemon and launcher evidence that the fixed helper boundary needs installation or repair.
func networkSetupNeedsPrerequisiteRepair(outcome networksetupapproval.Outcome, err error) bool {
	if err != nil {
		var wireError rpc.WireError
		return errors.As(err, &wireError) &&
			(wireError.Code == rpc.ErrorCodePrivilegedHelperRequired || wireError.Code == rpc.ErrorCodePrivilegedHelperUnsafe)
	}
	return outcome.State == networksetupapproval.Unavailable
}

// networkSetupPrerequisiteVerificationError reports only fixed peer-safe categories after native installation claimed success.
func networkSetupPrerequisiteVerificationError(outcome networksetupapproval.Outcome, err error) error {
	if err != nil {
		var wireError rpc.WireError
		if errors.As(err, &wireError) {
			switch wireError.Code {
			case rpc.ErrorCodePrivilegedHelperRequired:
				return errors.New("verify Harbor privileged networking support after installation: harbord still cannot find the ticket directory")
			case rpc.ErrorCodePrivilegedHelperUnsafe:
				return errors.New("verify Harbor privileged networking support after installation: harbord rejected the ticket directory's ownership, permissions, type, or ACLs")
			}
		}
	}
	if outcome.State == networksetupapproval.Unavailable {
		return errors.New("verify Harbor privileged networking support after installation: the native helper is unavailable")
	}
	return errors.New("verify Harbor privileged networking support after installation: the result was inconsistent")
}

// networkSetupApprovalError preserves retry guidance without exposing helper tickets or privileged command details.
func networkSetupApprovalError(outcome networksetupapproval.Outcome) error {
	switch outcome.State {
	case networksetupapproval.Declined:
		return errors.New("Harbor network setup approval was declined; setup is safe to retry")
	case networksetupapproval.Unavailable:
		return errors.New("Harbor network setup approval is unavailable on this installation")
	case networksetupapproval.HelperFailed:
		if outcome.HelperFailure == nil {
			return errors.New("Harbor network setup helper failed without a problem description")
		}
		return fmt.Errorf("Harbor network setup helper failed (%s): %s", outcome.HelperFailure.Code, outcome.HelperFailure.Message)
	case networksetupapproval.Indeterminate:
		return errors.New("Harbor network setup may have changed the host; refresh before retrying")
	default:
		return fmt.Errorf("Harbor network setup approval returned unsupported state %q", outcome.State)
	}
}

// approveNetworkResolverSetup delegates only the exact daemon-selected resolver revision to native consent.
func (a *App) approveNetworkResolverSetup(
	ctx context.Context,
	client controlClient,
	setup control.NetworkResolverSetupOperation,
) error {
	request := networkresolverapproval.Request{
		OperationID:               setup.Operation.ID,
		ExpectedOperationRevision: setup.Revision,
	}
	runner := a.resolverApproval(client)
	outcome, err := runner.Execute(ctx, request)
	if networkResolverSetupNeedsPrerequisiteRepair(outcome, err) {
		if repairErr := a.setupPrerequisite.Ensure(ctx); repairErr != nil {
			return fmt.Errorf("install Harbor privileged networking support: %w", repairErr)
		}
		outcome, err = runner.Execute(ctx, request)
		if networkResolverSetupNeedsPrerequisiteRepair(outcome, err) {
			return networkResolverSetupPrerequisiteVerificationError(outcome, err)
		}
	}
	if err != nil {
		return fmt.Errorf("approve Harbor network resolver setup: %w", err)
	}
	if outcome.State != networkresolverapproval.Succeeded {
		return networkResolverSetupApprovalError(outcome)
	}
	if outcome.Confirmation == nil || outcome.HelperFailure != nil {
		return errors.New("approve Harbor network resolver setup: successful approval returned inconsistent evidence")
	}

	confirmation := *outcome.Confirmation
	if err := confirmation.Validate(); err != nil {
		return fmt.Errorf("validate Harbor network resolver setup confirmation: %w", err)
	}
	if confirmation.Operation.ID != setup.Operation.ID ||
		confirmation.Operation.IntentID != setup.Operation.IntentID ||
		confirmation.NetworkRevision <= setup.Revision+1 ||
		confirmation.Revision != confirmation.NetworkRevision+1 {
		return errors.New("validate Harbor network resolver setup confirmation: result crossed the selected operation revision")
	}
	return nil
}

// networkResolverSetupNeedsPrerequisiteRepair recognizes only reviewed evidence that the shared helper boundary needs repair.
func networkResolverSetupNeedsPrerequisiteRepair(outcome networkresolverapproval.Outcome, err error) bool {
	if err != nil {
		var wireError rpc.WireError
		return errors.As(err, &wireError) &&
			(wireError.Code == rpc.ErrorCodePrivilegedHelperRequired || wireError.Code == rpc.ErrorCodePrivilegedHelperUnsafe)
	}
	if outcome.State == networkresolverapproval.HelperFailed && outcome.HelperFailure != nil {
		return outcome.HelperFailure.Code == helper.ErrorCodeAuthenticationFailed
	}
	return outcome.State == networkresolverapproval.Unavailable
}

// networkResolverSetupPrerequisiteVerificationError reports fixed peer-safe diagnostics after one bounded repair.
func networkResolverSetupPrerequisiteVerificationError(outcome networkresolverapproval.Outcome, err error) error {
	if err != nil {
		var wireError rpc.WireError
		if errors.As(err, &wireError) {
			switch wireError.Code {
			case rpc.ErrorCodePrivilegedHelperRequired:
				return errors.New("verify Harbor privileged networking support after installation: harbord still cannot find the ticket directory")
			case rpc.ErrorCodePrivilegedHelperUnsafe:
				return errors.New("verify Harbor privileged networking support after installation: harbord rejected the ticket directory's ownership, permissions, type, or ACLs")
			}
		}
	}
	if outcome.State == networkresolverapproval.Unavailable {
		return errors.New("verify Harbor privileged networking support after installation: the native helper is unavailable")
	}
	if outcome.State == networkresolverapproval.HelperFailed && outcome.HelperFailure != nil &&
		outcome.HelperFailure.Code == helper.ErrorCodeAuthenticationFailed {
		return errors.New("verify Harbor privileged networking support after installation: the installed helper could not authenticate a newly issued ticket")
	}
	return errors.New("verify Harbor privileged networking support after installation: the result was inconsistent")
}

// networkResolverSetupApprovalError preserves retry guidance without exposing helper capabilities or privileged details.
func networkResolverSetupApprovalError(outcome networkresolverapproval.Outcome) error {
	switch outcome.State {
	case networkresolverapproval.Declined:
		return errors.New("Harbor network resolver setup approval was declined; setup is safe to retry")
	case networkresolverapproval.Unavailable:
		return errors.New("Harbor network resolver setup approval is unavailable on this installation")
	case networkresolverapproval.HelperFailed:
		if outcome.HelperFailure == nil {
			return errors.New("Harbor network resolver setup helper failed without a problem description")
		}
		return fmt.Errorf(
			"Harbor network resolver setup helper failed (%s): %s",
			outcome.HelperFailure.Code,
			outcome.HelperFailure.Message,
		)
	case networkresolverapproval.Indeterminate:
		return errors.New("Harbor network resolver setup may have changed the host; refresh before retrying")
	default:
		return fmt.Errorf("Harbor network resolver setup approval returned unsupported state %q", outcome.State)
	}
}

// AddProject lets the operating system choose a directory before asking the connected daemon to register it.
func (a *App) AddProject() (desktopwire.AddProjectResult, error) {
	ctx, client, err := a.currentConnection()
	if err != nil {
		return desktopwire.AddProjectResult{}, err
	}

	path, err := a.choose(ctx, runtime.OpenDialogOptions{
		Title:                "Add a GoForj project",
		ResolvesAliases:      true,
		CanCreateDirectories: false,
	})
	if err != nil {
		return desktopwire.AddProjectResult{}, fmt.Errorf("choose GoForj project directory: %w", err)
	}
	if path == "" {
		return desktopwire.AddProjectResult{Canceled: true}, nil
	}

	registration, err := client.RegisterProject(ctx, control.RegisterProjectRequest{Path: path})
	if err != nil {
		return desktopwire.AddProjectResult{}, fmt.Errorf("register GoForj project: %w", err)
	}
	result := desktopwire.AddProjectResult{Registration: &registration}
	if err := result.Validate(); err != nil {
		return desktopwire.AddProjectResult{}, fmt.Errorf("validate project registration: %w", err)
	}
	return result, nil
}

// StartProject starts or resumes exactly one client-owned project start intent through the connected daemon.
func (a *App) StartProject(projectID string, intentID string) (control.ProjectLifecycleOperation, error) {
	request := control.StartProjectRequest{
		ProjectID: domain.ProjectID(projectID),
		IntentID:  domain.IntentID(intentID),
	}
	if err := request.Validate(); err != nil {
		return control.ProjectLifecycleOperation{}, fmt.Errorf("project start request: %w", err)
	}

	ctx, client, err := a.currentConnection()
	if err != nil {
		return control.ProjectLifecycleOperation{}, err
	}
	result, err := client.StartProject(ctx, request)
	if err != nil {
		return control.ProjectLifecycleOperation{}, fmt.Errorf("start GoForj project: %w", err)
	}
	if err := result.Validate(); err != nil {
		return control.ProjectLifecycleOperation{}, fmt.Errorf("validate project start: %w", err)
	}
	if result.Operation.Kind != domain.OperationKindProjectStart ||
		result.Operation.ProjectID != request.ProjectID ||
		result.Operation.IntentID != request.IntentID {
		return control.ProjectLifecycleOperation{}, fmt.Errorf("validate project start: daemon result does not match the requested action, project, and intent")
	}

	return result, nil
}

// InspectProjectRuntimeRepair asks the daemon to derive one bounded stale-runtime candidate for explicit review.
func (a *App) InspectProjectRuntimeRepair(projectID string) (control.ProjectRuntimeRepairInspection, error) {
	request := control.InspectProjectRuntimeRepairRequest{ProjectID: domain.ProjectID(projectID)}
	if err := request.Validate(); err != nil {
		return control.ProjectRuntimeRepairInspection{}, fmt.Errorf("project runtime repair inspection request: %w", err)
	}

	ctx, client, err := a.currentConnection()
	if err != nil {
		return control.ProjectRuntimeRepairInspection{}, err
	}
	result, err := client.InspectProjectRuntimeRepair(ctx, request)
	if err != nil {
		return control.ProjectRuntimeRepairInspection{}, fmt.Errorf("inspect stale GoForj runtime: %w", err)
	}
	if err := result.Validate(); err != nil {
		return control.ProjectRuntimeRepairInspection{}, fmt.Errorf("validate project runtime repair inspection: %w", err)
	}
	if result.ProjectID != request.ProjectID {
		return control.ProjectRuntimeRepairInspection{}, errors.New("validate project runtime repair inspection: daemon result belongs to another project")
	}

	return result, nil
}

// ConfirmProjectRuntimeRepair submits only the opaque selection from one prior inspection for immediate daemon revalidation.
func (a *App) ConfirmProjectRuntimeRepair(
	projectID string,
	inspectionID string,
	candidateFingerprint string,
) (control.ProjectRuntimeRepairConfirmation, error) {
	request := control.ConfirmProjectRuntimeRepairRequest{
		ProjectID:    domain.ProjectID(projectID),
		InspectionID: control.ProjectRuntimeRepairInspectionID(inspectionID),
		Fingerprint:  control.ProjectRuntimeRepairCandidateFingerprint(candidateFingerprint),
	}
	if err := request.Validate(); err != nil {
		return control.ProjectRuntimeRepairConfirmation{}, fmt.Errorf("project runtime repair confirmation request: %w", err)
	}

	ctx, client, err := a.currentConnection()
	if err != nil {
		return control.ProjectRuntimeRepairConfirmation{}, err
	}
	result, err := client.ConfirmProjectRuntimeRepair(ctx, request)
	if err != nil {
		return control.ProjectRuntimeRepairConfirmation{}, fmt.Errorf("confirm stale GoForj runtime repair: %w", err)
	}
	if err := result.Validate(); err != nil {
		return control.ProjectRuntimeRepairConfirmation{}, fmt.Errorf("validate project runtime repair confirmation: %w", err)
	}
	if result.Project.ID != request.ProjectID {
		return control.ProjectRuntimeRepairConfirmation{}, errors.New("validate project runtime repair confirmation: daemon result belongs to another project")
	}

	return result, nil
}

// ProjectActivity reads one bounded output chunk from the project's current durable session.
func (a *App) ProjectActivity(projectID string, sessionID string, cursor uint64) (control.ProjectActivity, error) {
	return a.projectActivity(projectID, sessionID, cursor, 0)
}

// WaitProjectActivity waits briefly for the current session cursor to advance before returning one bounded output chunk.
func (a *App) WaitProjectActivity(
	projectID string,
	sessionID string,
	cursor uint64,
	waitMilliseconds uint64,
) (control.ProjectActivity, error) {
	if waitMilliseconds > uint64(control.MaximumProjectActivityWaitMilliseconds) {
		return control.ProjectActivity{}, fmt.Errorf(
			"project activity request: wait exceeds %d milliseconds",
			control.MaximumProjectActivityWaitMilliseconds,
		)
	}
	return a.projectActivity(projectID, sessionID, cursor, uint32(waitMilliseconds))
}

// projectActivity validates and delegates one immediate or held current-session output read.
func (a *App) projectActivity(
	projectID string,
	sessionID string,
	cursor uint64,
	waitMilliseconds uint32,
) (control.ProjectActivity, error) {
	request := control.ProjectActivityRequest{
		ProjectID:        domain.ProjectID(projectID),
		SessionID:        domain.SessionID(sessionID),
		Cursor:           cursor,
		WaitMilliseconds: waitMilliseconds,
	}
	if err := request.Validate(); err != nil {
		return control.ProjectActivity{}, fmt.Errorf("project activity request: %w", err)
	}

	ctx, client, err := a.currentConnection()
	if err != nil {
		return control.ProjectActivity{}, err
	}
	requestContext, releaseRequest := a.activityRequestContext(ctx, waitMilliseconds > 0)
	defer releaseRequest()
	activity, err := client.ProjectActivity(requestContext, request)
	if err != nil {
		return control.ProjectActivity{}, fmt.Errorf("read GoForj project activity: %w", err)
	}
	if err := activity.Validate(); err != nil {
		return control.ProjectActivity{}, fmt.Errorf("validate project activity: %w", err)
	}
	if activity.ProjectID != request.ProjectID {
		return control.ProjectActivity{}, errors.New("validate project activity: daemon result belongs to another project")
	}
	if request.SessionID != "" && activity.Session != nil && activity.Session.ID != request.SessionID && !activity.Session.Output.Reset {
		return control.ProjectActivity{}, errors.New("validate project activity: daemon changed sessions without resetting output")
	}

	return activity, nil
}

// activityRequestContext cancels the prior held desktop read before selecting new activity.
func (a *App) activityRequestContext(parent context.Context, held bool) (context.Context, func()) {
	requestContext, cancel := context.WithCancel(parent)

	a.activityWaitMu.Lock()
	prior := a.activityWaitCancel
	a.activityWaitID++
	waitID := a.activityWaitID
	if held {
		a.activityWaitCancel = cancel
	} else {
		a.activityWaitCancel = nil
	}
	a.activityWaitMu.Unlock()

	if prior != nil {
		prior()
	}
	return requestContext, func() {
		cancel()
		a.activityWaitMu.Lock()
		if a.activityWaitID == waitID {
			a.activityWaitCancel = nil
		}
		a.activityWaitMu.Unlock()
	}
}

// ServiceLogs reads one bounded output chunk from a Compose service in the project's current session.
func (a *App) ServiceLogs(
	projectID string,
	sessionID string,
	serviceID string,
	cursor uint64,
) (control.ServiceLogs, error) {
	return a.serviceLogs(projectID, sessionID, serviceID, cursor, 0)
}

// WaitServiceLogs waits briefly for the selected service cursor to advance before returning one bounded output chunk.
func (a *App) WaitServiceLogs(
	projectID string,
	sessionID string,
	serviceID string,
	cursor uint64,
	waitMilliseconds uint64,
) (control.ServiceLogs, error) {
	if waitMilliseconds > uint64(control.MaximumServiceLogsWaitMilliseconds) {
		return control.ServiceLogs{}, fmt.Errorf(
			"service logs request: wait exceeds %d milliseconds",
			control.MaximumServiceLogsWaitMilliseconds,
		)
	}
	return a.serviceLogs(projectID, sessionID, serviceID, cursor, uint32(waitMilliseconds))
}

// serviceLogs validates and delegates one immediate or held current-session service output read.
func (a *App) serviceLogs(
	projectID string,
	sessionID string,
	serviceID string,
	cursor uint64,
	waitMilliseconds uint32,
) (control.ServiceLogs, error) {
	request := control.ServiceLogsRequest{
		ProjectID:        domain.ProjectID(projectID),
		SessionID:        domain.SessionID(sessionID),
		ServiceID:        domain.ServiceID(serviceID),
		Cursor:           cursor,
		WaitMilliseconds: waitMilliseconds,
	}
	if err := request.Validate(); err != nil {
		return control.ServiceLogs{}, fmt.Errorf("service logs request: %w", err)
	}

	ctx, client, err := a.currentConnection()
	if err != nil {
		return control.ServiceLogs{}, err
	}
	requestContext, releaseRequest := a.serviceLogsRequestContext(ctx, waitMilliseconds > 0)
	defer releaseRequest()
	logs, err := client.ServiceLogs(requestContext, request)
	if err != nil {
		return control.ServiceLogs{}, fmt.Errorf("read Compose service logs: %w", err)
	}
	if err := logs.Validate(); err != nil {
		return control.ServiceLogs{}, fmt.Errorf("validate service logs: %w", err)
	}
	if logs.ProjectID != request.ProjectID {
		return control.ServiceLogs{}, errors.New("validate service logs: daemon result belongs to another project")
	}
	if logs.ServiceID != request.ServiceID {
		return control.ServiceLogs{}, errors.New("validate service logs: daemon result belongs to another service")
	}
	if request.SessionID != "" && logs.SessionID != "" && logs.SessionID != request.SessionID && !logs.Output.Reset {
		return control.ServiceLogs{}, errors.New("validate service logs: daemon changed sessions without resetting output")
	}

	return logs, nil
}

// serviceLogsRequestContext cancels only the prior held service-log read before selecting a new service stream.
func (a *App) serviceLogsRequestContext(parent context.Context, held bool) (context.Context, func()) {
	requestContext, cancel := context.WithCancel(parent)

	a.serviceLogsWaitMu.Lock()
	prior := a.serviceLogsWaitCancel
	a.serviceLogsWaitID++
	waitID := a.serviceLogsWaitID
	if held {
		a.serviceLogsWaitCancel = cancel
	} else {
		a.serviceLogsWaitCancel = nil
	}
	a.serviceLogsWaitMu.Unlock()

	if prior != nil {
		prior()
	}
	return requestContext, func() {
		cancel()
		a.serviceLogsWaitMu.Lock()
		if a.serviceLogsWaitID == waitID {
			a.serviceLogsWaitCancel = nil
		}
		a.serviceLogsWaitMu.Unlock()
	}
}

// StopProject starts or resumes exactly one client-owned project stop intent through the connected daemon.
func (a *App) StopProject(projectID string, intentID string) (control.ProjectLifecycleOperation, error) {
	request := control.StopProjectRequest{
		ProjectID: domain.ProjectID(projectID),
		IntentID:  domain.IntentID(intentID),
	}
	if err := request.Validate(); err != nil {
		return control.ProjectLifecycleOperation{}, fmt.Errorf("project stop request: %w", err)
	}

	ctx, client, err := a.currentConnection()
	if err != nil {
		return control.ProjectLifecycleOperation{}, err
	}
	result, err := client.StopProject(ctx, request)
	if err != nil {
		return control.ProjectLifecycleOperation{}, fmt.Errorf("stop GoForj project: %w", err)
	}
	if err := result.Validate(); err != nil {
		return control.ProjectLifecycleOperation{}, fmt.Errorf("validate project stop: %w", err)
	}
	if result.Operation.Kind != domain.OperationKindProjectStop ||
		result.Operation.ProjectID != request.ProjectID ||
		result.Operation.IntentID != request.IntentID {
		return control.ProjectLifecycleOperation{}, fmt.Errorf("validate project stop: daemon result does not match the requested action, project, and intent")
	}

	return result, nil
}

// RemoveProject starts or resumes exactly one client-owned project removal intent through the connected daemon.
func (a *App) RemoveProject(projectID string, intentID string) (control.ProjectUnregistration, error) {
	request := control.UnregisterProjectRequest{
		ProjectID: domain.ProjectID(projectID),
		IntentID:  domain.IntentID(intentID),
	}
	if err := request.Validate(); err != nil {
		return control.ProjectUnregistration{}, fmt.Errorf("project removal request: %w", err)
	}

	ctx, client, err := a.currentConnection()
	if err != nil {
		return control.ProjectUnregistration{}, err
	}
	result, err := client.UnregisterProject(ctx, request)
	if err != nil {
		return control.ProjectUnregistration{}, fmt.Errorf("remove GoForj project: %w", err)
	}
	if err := validateProjectRemovalResult(request, result); err != nil {
		return control.ProjectUnregistration{}, err
	}

	return result, nil
}

// ApproveProjectRemoval replays one retained removal identity before opening native consent for its exact current revision.
func (a *App) ApproveProjectRemoval(projectID string, intentID string) (control.ProjectUnregistration, error) {
	request := control.UnregisterProjectRequest{
		ProjectID: domain.ProjectID(projectID),
		IntentID:  domain.IntentID(intentID),
	}
	if err := request.Validate(); err != nil {
		return control.ProjectUnregistration{}, fmt.Errorf("project removal approval request: %w", err)
	}

	ctx, client, release, err := a.leaseCurrentConnection()
	if err != nil {
		return control.ProjectUnregistration{}, err
	}
	defer release()

	replayed, err := client.UnregisterProject(ctx, request)
	if err != nil {
		return control.ProjectUnregistration{}, fmt.Errorf("replay GoForj project removal before approval: %w", err)
	}
	if err := validateProjectRemovalResult(request, replayed); err != nil {
		return control.ProjectUnregistration{}, err
	}
	if replayed.Operation.State != domain.OperationRequiresApproval {
		return replayed, nil
	}

	return a.approveProjectRemoval(ctx, client, request, replayed)
}

// approveProjectRemoval runs native approval only for the exact replayed operation revision.
func (a *App) approveProjectRemoval(
	ctx context.Context,
	client controlClient,
	request control.UnregisterProjectRequest,
	replayed control.ProjectUnregistration,
) (control.ProjectUnregistration, error) {
	selection := projectapproval.Request{
		OperationID:               replayed.Operation.ID,
		ExpectedOperationRevision: replayed.Revision,
	}
	runner := a.projectApproval(client)
	outcome, err := runner.Execute(ctx, selection)
	if projectRemovalApprovalNeedsPrerequisiteRepair(outcome, err) {
		if repairErr := a.setupPrerequisite.Ensure(ctx); repairErr != nil {
			return control.ProjectUnregistration{}, fmt.Errorf("install Harbor privileged project-removal support: %w", repairErr)
		}
		outcome, err = runner.Execute(ctx, selection)
		if projectRemovalApprovalNeedsPrerequisiteRepair(outcome, err) {
			return control.ProjectUnregistration{}, projectRemovalApprovalPrerequisiteVerificationError(outcome, err)
		}
	}
	if err != nil {
		return control.ProjectUnregistration{}, fmt.Errorf("approve GoForj project removal: %w", err)
	}
	if outcome.State != projectapproval.Succeeded {
		return control.ProjectUnregistration{}, projectRemovalApprovalError(outcome)
	}
	if outcome.Confirmation == nil || outcome.HelperFailure != nil {
		return control.ProjectUnregistration{}, errors.New("approve GoForj project removal: successful approval returned inconsistent evidence")
	}

	confirmation := *outcome.Confirmation
	if err := confirmation.Validate(); err != nil {
		return control.ProjectUnregistration{}, fmt.Errorf("validate project removal approval confirmation: %w", err)
	}
	if confirmation.Operation.ID != replayed.Operation.ID ||
		confirmation.Operation.ProjectID != request.ProjectID ||
		confirmation.Operation.IntentID != request.IntentID ||
		confirmation.Revision <= replayed.Revision {
		return control.ProjectUnregistration{}, errors.New("validate project removal approval confirmation: result crossed the replayed operation, project, intent, or revision")
	}

	result := control.ProjectUnregistration{
		Operation: confirmation.Operation,
		Revision:  confirmation.Revision,
	}
	if err := validateProjectRemovalResult(request, result); err != nil {
		return control.ProjectUnregistration{}, err
	}
	return result, nil
}

// projectRemovalApprovalNeedsPrerequisiteRepair recognizes only reviewed evidence that the fixed helper boundary needs one repair.
func projectRemovalApprovalNeedsPrerequisiteRepair(outcome projectapproval.Outcome, err error) bool {
	if err != nil {
		var wireError rpc.WireError
		return errors.As(err, &wireError) &&
			(wireError.Code == rpc.ErrorCodePrivilegedHelperRequired || wireError.Code == rpc.ErrorCodePrivilegedHelperUnsafe)
	}
	return outcome.State == projectapproval.Unavailable
}

// projectRemovalApprovalPrerequisiteVerificationError reports fixed diagnostics after one claimed helper repair.
func projectRemovalApprovalPrerequisiteVerificationError(outcome projectapproval.Outcome, err error) error {
	if err != nil {
		var wireError rpc.WireError
		if errors.As(err, &wireError) {
			switch wireError.Code {
			case rpc.ErrorCodePrivilegedHelperRequired:
				return errors.New("verify Harbor privileged project-removal support after installation: harbord still cannot find the ticket directory")
			case rpc.ErrorCodePrivilegedHelperUnsafe:
				return errors.New("verify Harbor privileged project-removal support after installation: harbord rejected the ticket directory's ownership, permissions, type, or ACLs")
			}
		}
	}
	if outcome.State == projectapproval.Unavailable {
		return errors.New("verify Harbor privileged project-removal support after installation: the native helper is unavailable")
	}
	return errors.New("verify Harbor privileged project-removal support after installation: the result was inconsistent")
}

// projectRemovalApprovalError preserves safe retry guidance without exposing helper capabilities or host selections.
func projectRemovalApprovalError(outcome projectapproval.Outcome) error {
	switch outcome.State {
	case projectapproval.Declined:
		return errors.New("Harbor project removal approval was declined; removal is safe to retry")
	case projectapproval.Unavailable:
		return errors.New("Harbor project removal approval is unavailable on this installation")
	case projectapproval.HelperFailed:
		if outcome.HelperFailure == nil {
			return errors.New("Harbor project removal helper failed without a problem description")
		}
		return fmt.Errorf(
			"Harbor project removal helper failed (%s): %s",
			outcome.HelperFailure.Code,
			outcome.HelperFailure.Message,
		)
	case projectapproval.Indeterminate:
		return errors.New("Harbor project removal may have changed the host; refresh before retrying")
	default:
		return fmt.Errorf("Harbor project removal approval returned unsupported state %q", outcome.State)
	}
}

// validateProjectRemovalResult binds one daemon result to the exact project and client-owned intent.
func validateProjectRemovalResult(
	request control.UnregisterProjectRequest,
	result control.ProjectUnregistration,
) error {
	if err := result.Validate(); err != nil {
		return fmt.Errorf("validate project removal: %w", err)
	}
	if result.Operation.ProjectID != request.ProjectID || result.Operation.IntentID != request.IntentID {
		return errors.New("validate project removal: daemon result does not match the requested project and intent")
	}
	return nil
}

// newDesktopClient adapts the concrete control client to the desktop's narrow lifecycle interface.
func newDesktopClient(ctx context.Context, config control.ClientConfig) (controlClient, error) {
	return control.NewClient(ctx, config)
}

// startup starts background connection ownership only after Wails supplies its runtime context.
func (a *App) startup(ctx context.Context) {
	a.mu.Lock()
	if a.cancel != nil {
		a.mu.Unlock()
		panic("desktop app started more than once")
	}
	runContext, cancel := context.WithCancel(ctx)
	a.ctx = ctx
	a.cancel = cancel
	a.done = make(chan struct{})
	done := a.done
	a.presentation.startup(ctx)
	a.mu.Unlock()

	go a.run(runContext, done)
}

// shutdown cancels background calls, closes the live transport, and waits until the owner goroutine exits.
func (a *App) shutdown(_ context.Context) {
	a.presentation.shutdown()

	a.mu.Lock()
	cancel := a.cancel
	client := a.client
	done := a.done
	a.ctx = nil
	a.cancel = nil
	a.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if client != nil {
		_ = client.Close()
	}
	if done != nil {
		<-done
	}
}

// onSecondInstanceLaunch restores the existing client because a second desktop must not become another runtime authority.
func (a *App) onSecondInstanceLaunch(_ options.SecondInstanceData) {
	a.presentation.activate()
}

// Status returns the diagnostic reported by the currently authenticated daemon connection.
func (a *App) Status() (control.DaemonStatus, error) {
	ctx, client, err := a.currentConnection()
	if err != nil {
		return control.DaemonStatus{}, err
	}

	status, err := client.Status(ctx)
	if err != nil {
		return control.DaemonStatus{}, fmt.Errorf("read Harbor daemon status: %w", err)
	}

	return status, nil
}

// Snapshot returns a fresh complete replacement of desktop-visible Harbor state.
func (a *App) Snapshot() (domain.Snapshot, error) {
	ctx, client, err := a.currentConnection()
	if err != nil {
		return domain.Snapshot{}, err
	}

	snapshot, err := client.Snapshot(ctx)
	if err != nil {
		return domain.Snapshot{}, fmt.Errorf("read Harbor snapshot: %w", err)
	}
	if err := snapshot.Validate(); err != nil {
		return domain.Snapshot{}, fmt.Errorf("validate Harbor snapshot: %w", err)
	}

	return snapshot, nil
}

// OpenResource resolves a project-scoped resource from fresh daemon state before opening its reviewed URL.
func (a *App) OpenResource(projectID string, resourceID string) error {
	typedProjectID := domain.ProjectID(projectID)
	if err := typedProjectID.Validate(); err != nil {
		return fmt.Errorf("project: %w", err)
	}
	typedResourceID := domain.ResourceID(resourceID)
	if err := typedResourceID.Validate(); err != nil {
		return fmt.Errorf("resource: %w", err)
	}

	ctx, client, err := a.currentConnection()
	if err != nil {
		return err
	}
	snapshot, err := client.Snapshot(ctx)
	if err != nil {
		return fmt.Errorf("read Harbor snapshot: %w", err)
	}
	if err := snapshot.Validate(); err != nil {
		return fmt.Errorf("validate Harbor snapshot: %w", err)
	}

	resource, err := findResource(snapshot, typedProjectID, typedResourceID)
	if err != nil {
		return err
	}

	a.open(ctx, resource.URL)
	return nil
}

// OpenTerminalURL opens one safe absolute web URL selected from terminal output.
func (a *App) OpenTerminalURL(rawURL string) error {
	parsed, err := url.ParseRequestURI(rawURL)
	if err != nil || parsed == nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil {
		return errors.New("terminal URL must be an absolute credential-free HTTP or HTTPS URL")
	}
	a.mu.RLock()
	ctx := a.ctx
	a.mu.RUnlock()
	if ctx == nil {
		return errors.New("Harbor desktop lifecycle is not active")
	}
	a.open(ctx, parsed.String())
	return nil
}

// run owns reconnection so neither Wails calls nor the presentation layer can accidentally create competing sessions.
func (a *App) run(ctx context.Context, done chan struct{}) {
	defer close(done)

	for {
		if ctx.Err() != nil {
			return
		}
		a.emitConnection(ctx, ConnectionConnecting)

		client, err := a.clientFactory(ctx, control.ClientConfig{Role: rpc.RoleDesktop})
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			a.emitConnection(ctx, ConnectionDisconnected)
			if !a.wait(ctx, a.reconnectDelay) {
				return
			}
			continue
		}
		if client == nil {
			panic("desktop client factory returned a nil client")
		}

		if !a.installClient(ctx, client) {
			_ = client.Close()
			return
		}
		a.emitConnection(ctx, ConnectionConnected)
		a.poll(ctx, client)
		a.retireClient(ctx, client)

		if ctx.Err() != nil {
			return
		}
		a.emitConnection(ctx, ConnectionDisconnected)
		if !a.wait(ctx, a.reconnectDelay) {
			return
		}
	}
}

// poll emits only validated, monotonically ordered replacement snapshots while one connection remains usable.
func (a *App) poll(ctx context.Context, client controlClient) {
	cursor := snapshotCursor{}
	for {
		select {
		case <-ctx.Done():
			return
		case <-client.Done():
			return
		default:
		}

		snapshot, err := client.Snapshot(ctx)
		if err != nil {
			select {
			case <-client.Done():
				return
			default:
				// A snapshot is the desktop's complete authority; reconnecting is safer than presenting an unrefreshable session as healthy.
				return
			}
		}
		a.emitSnapshot(ctx, snapshot, &cursor)

		if !a.wait(ctx, a.pollInterval) {
			return
		}
	}
}

// emitSnapshot rejects unchanged, stale, or invalid state within the current connection epoch.
func (a *App) emitSnapshot(ctx context.Context, snapshot domain.Snapshot, cursor *snapshotCursor) {
	if err := snapshot.Validate(); err != nil {
		return
	}
	if cursor.initialized && snapshot.Sequence <= cursor.sequence {
		return
	}
	cursor.sequence = snapshot.Sequence
	cursor.initialized = true

	a.events.Snapshot(ctx, snapshot)
}

// emitConnection keeps ephemeral transport state independent from durable snapshot ordering.
func (a *App) emitConnection(ctx context.Context, state ConnectionState) {
	a.events.Connection(ctx, ConnectionEvent{State: state})
}

// currentConnection snapshots lifecycle state so a Wails call cannot observe a partially installed client.
func (a *App) currentConnection() (context.Context, controlClient, error) {
	a.mu.RLock()
	ctx := a.ctx
	client := a.client
	a.mu.RUnlock()

	if ctx == nil || client == nil {
		return nil, nil, errDaemonDisconnected
	}

	return ctx, client, nil
}

// leaseCurrentConnection keeps an interactive approval on its selected session until its exact confirmation returns.
func (a *App) leaseCurrentConnection() (context.Context, controlClient, func(), error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.ctx == nil || a.client == nil {
		return nil, nil, nil, errDaemonDisconnected
	}
	if a.clientDrain != nil {
		panic("desktop connection is published while retirement is pending")
	}

	ctx := a.ctx
	client := a.client
	a.clientLeases++
	var once sync.Once
	release := func() {
		once.Do(func() {
			a.releaseClientLease(client)
		})
	}
	return ctx, client, release, nil
}

// releaseClientLease lets a retired connection close only after every selected approval has left its confirmation boundary.
func (a *App) releaseClientLease(client controlClient) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.clientLeases == 0 {
		panic("desktop connection lease released without ownership")
	}
	if a.client != nil && a.client != client {
		panic("desktop connection lease crossed a replacement session")
	}

	a.clientLeases--
	if a.clientLeases == 0 && a.clientDrain != nil {
		close(a.clientDrain)
		a.clientDrain = nil
	}
}

// installClient publishes a negotiated session only while the desktop lifecycle remains active.
func (a *App) installClient(ctx context.Context, client controlClient) bool {
	a.mu.Lock()
	defer a.mu.Unlock()

	if ctx.Err() != nil || a.ctx == nil {
		return false
	}
	if a.clientLeases != 0 || a.clientDrain != nil {
		panic("desktop connection installed before the retired session drained")
	}
	a.client = client
	return true
}

// removeClient clears only the connection owned by the exiting poll loop and reports when its approvals have drained.
func (a *App) removeClient(client controlClient) <-chan struct{} {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.client != client {
		return nil
	}
	a.client = nil
	if a.clientLeases == 0 {
		return nil
	}
	if a.clientDrain != nil {
		panic("desktop connection retirement started more than once")
	}
	a.clientDrain = make(chan struct{})
	return a.clientDrain
}

// retireClient prevents polling from closing a session between privileged helper success and durable confirmation.
func (a *App) retireClient(ctx context.Context, client controlClient) {
	drained := a.removeClient(client)
	if drained != nil {
		select {
		case <-drained:
		case <-ctx.Done():
		}
	}
	_ = client.Close()
}

// findResource requires both identities because resource IDs are unique only inside a project snapshot.
func findResource(snapshot domain.Snapshot, projectID domain.ProjectID, resourceID domain.ResourceID) (domain.ResourceSnapshot, error) {
	for _, project := range snapshot.Projects {
		if project.ID != projectID {
			continue
		}
		for _, resource := range project.Resources {
			if resource.ID == resourceID {
				return resource, nil
			}
		}
		return domain.ResourceSnapshot{}, fmt.Errorf("resource %q was not found in project %q", resourceID, projectID)
	}

	return domain.ResourceSnapshot{}, fmt.Errorf("project %q was not found", projectID)
}

// waitForContext bounds background polling without delaying shutdown after cancellation.
func waitForContext(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
