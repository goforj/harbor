//go:build windows

package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/goforj/harbor/internal/control"
	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/helper/launcher"
	"github.com/goforj/harbor/internal/networkdataplaneapproval"
	"github.com/goforj/harbor/internal/networkreleaseapproval"
	"github.com/goforj/harbor/internal/networkresolverapproval"
	"github.com/goforj/harbor/internal/networksetupapproval"
	"github.com/goforj/harbor/internal/platform/windowsinstallation"
	"github.com/goforj/harbor/internal/rpc"
	"golang.org/x/sys/windows"
)

// receipt describes fixture authorization separately from unsupported real GUI consent and containment claims.
type receipt struct {
	Observation      *projectObservation `json:"observation,omitempty"`
	Profile          string              `json:"profile"`
	GUIConsentProved bool                `json:"gui_consent_proved"`
	Selection        selection           `json:"selection"`
	UserSID          string              `json:"user_sid,omitempty"`
	Session          uint32              `json:"session,omitempty"`
	Operations       []operationFence    `json:"operations"`
	Complete         bool                `json:"complete"`
	Error            string              `json:"error,omitempty"`
}

// main writes only bounded public evidence and never accepts a helper path, ticket or native capability from arguments.
func main() {
	result := receipt{Profile: "preauthorized-hosted-functional", Operations: []operationFence{}}
	err := run(os.Args[1:], &result)
	if err != nil {
		result.Error = err.Error()
	} else {
		result.Complete = true
	}
	if outputErr := json.NewEncoder(os.Stdout).Encode(result); outputErr != nil {
		fmt.Fprintln(os.Stderr, outputErr)
		os.Exit(1)
	}
	if err != nil {
		os.Exit(1)
	}
}

// run keeps the installation and exact daemon native handles retained throughout all existing protocol calls.
func run(arguments []string, evidence *receipt) (result error) {
	flags := flag.NewFlagSet("windowsapproval", flag.ContinueOnError)
	flags.StringVar(&evidence.Selection.Action, "action", "", "setup, release or read-only observe")
	flags.StringVar(&evidence.Selection.InstallationID, "installation-id", "", "exact admitted installation ID")
	flags.UintVar(&evidence.Selection.DaemonPID, "daemon-pid", 0, "exact observed installed daemon PID")
	flags.Uint64Var(&evidence.Selection.DaemonBirth, "daemon-birth", 0, "exact observed native daemon creation time")
	projectID := flags.String("project-id", "", "exact disposable project for read-only observation")
	directory := flags.String("project-directory", "", "exact checkout for read-only observation")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected fixture arguments")
	}
	if err := evidence.Selection.validate(); err != nil {
		return err
	}
	if evidence.Selection.Action != "observe" && (*projectID != "" || *directory != "") {
		return errors.New("project selection is allowed only for read-only observation")
	}
	if !windows.GetCurrentProcessToken().IsElevated() {
		return errors.New("fixture requires the preauthorized high controller")
	}
	groups, err := windows.GetCurrentProcessToken().GetTokenGroups()
	if err != nil {
		return err
	}
	administrator := false
	for _, group := range groups.AllGroups() {
		if group.Sid.String() == "S-1-5-32-544" && group.Attributes&windows.SE_GROUP_ENABLED != 0 && group.Attributes&windows.SE_GROUP_USE_FOR_DENY_ONLY == 0 {
			administrator = true
		}
	}
	if !administrator {
		return errors.New("fixture controller lacks effective administrator authority")
	}
	if err := windowsinstallation.VerifyInteractiveSession(); err != nil {
		return err
	}
	installation, err := windowsinstallation.Admit()
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, installation.Close()) }()
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	selected := installation.Selection()
	if selected.InstallationID != evidence.Selection.InstallationID || selected.UserSID != user.User.Sid.String() {
		return errors.New("fixture installation differs from selected interactive user")
	}
	evidence.UserSID = selected.UserSID
	if err := windows.ProcessIdToSessionId(windows.GetCurrentProcessId(), &evidence.Session); err != nil {
		return err
	}
	if evidence.Session == 0 {
		return errors.New("fixture requires an interactive session")
	}
	process, err := retainDaemon(installation, evidence)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, windows.CloseHandle(process)) }()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	client, err := control.NewClient(ctx, control.ClientConfig{Role: rpc.RoleDesktop, RequestTimeout: 2 * time.Minute})
	if err != nil {
		return err
	}
	defer client.Close()
	peer := client.Peer().Transport
	if peer.ProcessID != uint32(evidence.Selection.DaemonPID) || peer.UserID != selected.UserSID {
		return errors.New("fixture connection is not the exact retained daemon")
	}
	check := func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		state, err := windows.WaitForSingleObject(process, 0)
		if err != nil {
			return err
		}
		if state != uint32(windows.WAIT_TIMEOUT) {
			return errors.New("retained installed daemon exited")
		}
		return nil
	}
	snapshot, err := client.Snapshot(ctx)
	if err != nil {
		return err
	}
	if evidence.Selection.Action == "observe" {
		if err := check(); err != nil {
			return err
		}
		activity, err := client.ProjectActivity(ctx, control.ProjectActivityRequest{ProjectID: domain.ProjectID(*projectID)})
		if err != nil {
			return err
		}
		observation, err := observeProject(snapshot, activity, domain.ProjectID(*projectID), *directory)
		if err != nil {
			return err
		}
		evidence.Observation = &observation
		return check()
	}
	if len(snapshot.Projects) != 0 {
		return errors.New("fixture networking authority requires no registered projects")
	}
	native := launcher.New(launcher.NewNativeTransport(), helper.SystemClock{})
	if evidence.Selection.Action == "setup" {
		if len(snapshot.Operations) != 0 || snapshot.NetworkSetup == nil || snapshot.NetworkSetup.Configured {
			return errors.New("setup fixture requires a fresh empty installed profile before any low setup click")
		}
		return setup(ctx, client, native, evidence, check)
	}
	return release(ctx, client, native, evidence, check)
}

// retainDaemon correlates the kernel-authenticated pipe peer with the exact low process already measured by the GUI harness.
func retainDaemon(installation *windowsinstallation.Installation, evidence *receipt) (windows.Handle, error) {
	process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, uint32(evidence.Selection.DaemonPID))
	if err != nil {
		return 0, err
	}
	refuse := func(err error) (windows.Handle, error) { return 0, errors.Join(err, windows.CloseHandle(process)) }
	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(process, &created, &exited, &kernel, &user); err != nil {
		return refuse(err)
	}
	birth := uint64(created.HighDateTime)<<32 | uint64(created.LowDateTime)
	if birth != evidence.Selection.DaemonBirth {
		return refuse(errors.New("installed daemon native creation identity changed"))
	}
	image := make([]uint16, 32768)
	size := uint32(len(image))
	if err := windows.QueryFullProcessImageName(process, 0, &image[0], &size); err != nil {
		return refuse(err)
	}
	expected, err := installation.Path("daemon")
	if err != nil {
		return refuse(err)
	}
	if !strings.EqualFold(windows.UTF16ToString(image[:size]), expected) {
		return refuse(errors.New("daemon image differs from admitted selected payload"))
	}
	var token windows.Token
	if err := windows.OpenProcessToken(process, windows.TOKEN_QUERY, &token); err != nil {
		return refuse(err)
	}
	owner, err := token.GetTokenUser()
	elevated := token.IsElevated()
	closeErr := token.Close()
	if err = errors.Join(err, closeErr); err != nil {
		return refuse(err)
	}
	var session uint32
	if err := windows.ProcessIdToSessionId(uint32(evidence.Selection.DaemonPID), &session); err != nil {
		return refuse(err)
	}
	if owner.User.Sid.String() != evidence.UserSID || elevated || session != evidence.Session {
		return refuse(errors.New("daemon is not the same-user non-elevated interactive process"))
	}
	return process, nil
}

// setup selects every operation and revision before using unchanged production approval executors.
func setup(ctx context.Context, client *control.Client, native *launcher.Launcher, evidence *receipt, check func() error) error {
	if err := check(); err != nil {
		return err
	}
	pool, err := client.StartNetworkSetup(ctx, control.StartNetworkSetupRequest{IntentID: evidence.Selection.intent("pool")})
	if err != nil {
		return err
	}
	if err := pool.Validate(); err != nil {
		return err
	}
	selected, err := fence(pool.Operation, pool.Revision, evidence.Selection.intent("pool"), domain.OperationKindNetworkSetup)
	if err != nil {
		return err
	}
	evidence.Operations = append(evidence.Operations, selected)
	if pool.Operation.State != domain.OperationRequiresApproval {
		return errors.New("fresh pool setup did not require exact approval")
	}
	if err := check(); err != nil {
		return err
	}
	outcome, err := networksetupapproval.New(client, native).Execute(ctx, networksetupapproval.Request{OperationID: selected.ID, ExpectedOperationRevision: selected.Revision})
	if err != nil {
		return err
	}
	if outcome.State != networksetupapproval.Succeeded || outcome.Confirmation == nil || outcome.HelperFailure != nil {
		return fmt.Errorf("pool approval did not succeed: %s: %v", outcome.State, outcome.HelperFailure)
	}
	if err := outcome.Confirmation.ValidateApproval(selected.ID, selected.Revision); err != nil {
		return err
	}
	if err := selected.advance(outcome.Confirmation.Operation, outcome.Confirmation.Revision); err != nil {
		return err
	}
	if err := check(); err != nil {
		return err
	}
	resolver, err := client.StartNetworkResolverSetup(ctx, control.StartNetworkResolverSetupRequest{IntentID: evidence.Selection.intent("resolver")})
	if err != nil {
		return err
	}
	if err := resolver.Validate(); err != nil {
		return err
	}
	selected, err = fence(resolver.Operation, resolver.Revision, evidence.Selection.intent("resolver"), domain.OperationKindNetworkResolverSetup)
	if err != nil {
		return err
	}
	evidence.Operations = append(evidence.Operations, selected)
	if resolver.Operation.State != domain.OperationRequiresApproval {
		return errors.New("fresh resolver setup did not require exact approval")
	}
	if err := check(); err != nil {
		return err
	}
	resolved, err := networkresolverapproval.New(client, native).Execute(ctx, networkresolverapproval.Request{OperationID: selected.ID, ExpectedOperationRevision: selected.Revision})
	if err != nil {
		return err
	}
	if resolved.State != networkresolverapproval.Succeeded || resolved.Confirmation == nil || resolved.HelperFailure != nil {
		return fmt.Errorf("resolver approval did not succeed: %s: %v", resolved.State, resolved.HelperFailure)
	}
	if err := resolved.Confirmation.Validate(); err != nil {
		return err
	}
	if err := selected.advance(resolved.Confirmation.Operation, resolved.Confirmation.Revision); err != nil {
		return err
	}
	if err := check(); err != nil {
		return err
	}
	plane, err := client.StartNetworkDataPlaneSetup(ctx, control.StartNetworkDataPlaneSetupRequest{IntentID: evidence.Selection.intent("plane")})
	if err != nil {
		return err
	}
	if err := plane.Validate(); err != nil {
		return err
	}
	selected, err = fence(plane.Operation, plane.Revision, evidence.Selection.intent("plane"), domain.OperationKindNetworkDataPlaneSetup)
	if err != nil {
		return err
	}
	evidence.Operations = append(evidence.Operations, selected)
	executor := networkdataplaneapproval.New(client, native)
	if plane.Operation.State != domain.OperationRequiresApproval || plane.Operation.Phase != "awaiting trust approval" {
		return errors.New("fresh data plane did not require exact trust approval")
	}
	if err := check(); err != nil {
		return err
	}
	trusted, err := executor.ExecuteTrust(ctx, networkdataplaneapproval.Request{OperationID: selected.ID, ExpectedOperationRevision: selected.Revision})
	if err != nil {
		return err
	}
	if trusted.State != networkdataplaneapproval.Succeeded || trusted.Setup == nil || trusted.HelperFailure != nil {
		return fmt.Errorf("trust approval did not succeed: %s: %v", trusted.State, trusted.HelperFailure)
	}
	if err := trusted.Setup.Validate(); err != nil {
		return err
	}
	if err := selected.advance(trusted.Setup.Operation, trusted.Setup.Revision); err != nil {
		return err
	}
	plane = *trusted.Setup
	completed, err := planeCheckpoint(plane)
	if err != nil {
		return err
	}
	if completed {
		return requireConfigured(ctx, client, check)
	}
	selected, err = fence(plane.Operation, plane.Revision, evidence.Selection.intent("plane"), domain.OperationKindNetworkDataPlaneSetup)
	if err != nil {
		return err
	}
	evidence.Operations = append(evidence.Operations, selected)
	if err := check(); err != nil {
		return err
	}
	ports, err := executor.ExecuteLowPorts(ctx, networkdataplaneapproval.Request{OperationID: selected.ID, ExpectedOperationRevision: selected.Revision})
	if err != nil {
		return err
	}
	if ports.State != networkdataplaneapproval.Succeeded || ports.Confirmation == nil || ports.HelperFailure != nil {
		return fmt.Errorf("low-port approval did not succeed: %s: %v", ports.State, ports.HelperFailure)
	}
	if err := ports.Confirmation.Validate(); err != nil {
		return err
	}
	if err := selected.advance(ports.Confirmation.Operation, ports.Confirmation.Revision); err != nil {
		return err
	}
	return requireConfigured(ctx, client, check)
}

// requireConfigured accepts direct Windows listeners only after the daemon confirms the complete network foundation.
func requireConfigured(ctx context.Context, client *control.Client, check func() error) error {
	snapshot, err := client.Snapshot(ctx)
	if err != nil {
		return err
	}
	if snapshot.NetworkSetup == nil || !snapshot.NetworkSetup.Configured {
		return errors.New("daemon did not confirm configured networking")
	}
	return check()
}

// release advances only this installation's exact durable helper checkpoints after fixture unregistration.
func release(ctx context.Context, client *control.Client, native *launcher.Launcher, evidence *receipt, check func() error) error {
	intent := evidence.Selection.intent("release")
	if err := check(); err != nil {
		return err
	}
	operation, err := client.StartNetworkRelease(ctx, control.StartNetworkReleaseRequest{IntentID: intent})
	if err != nil {
		return err
	}
	executor := networkreleaseapproval.New(client, native)
	for attempt := 0; attempt < 12; attempt++ {
		if err := operation.Validate(); err != nil {
			return err
		}
		selected, err := fence(operation.Operation, operation.Revision, intent, domain.OperationKindNetworkRelease)
		if err != nil {
			return err
		}
		selected.CheckpointRevision = operation.CheckpointRevision
		selected.Phase = string(operation.Phase)
		evidence.Operations = append(evidence.Operations, selected)
		if err := check(); err != nil {
			return err
		}
		if operation.Operation.State == domain.OperationSucceeded {
			if operation.Phase != control.NetworkReleasePhaseProjection {
				return errors.New("release succeeded outside final projection")
			}
			return nil
		}
		previous := operation
		switch operation.Phase {
		case control.NetworkReleasePhaseLowPorts, control.NetworkReleasePhaseResolver, control.NetworkReleasePhaseTrust, control.NetworkReleasePhaseLoopbacks, control.NetworkReleasePhaseOwnership:
			outcome, err := executor.Execute(ctx, networkreleaseapproval.Request{OperationID: selected.ID, ExpectedCheckpointRevision: operation.CheckpointRevision, Phase: operation.Phase})
			if err != nil {
				return err
			}
			if outcome.State != networkreleaseapproval.Succeeded || outcome.Operation == nil || outcome.HelperFailure != nil {
				return fmt.Errorf("release approval did not succeed at %s: %s: %v", operation.Phase, outcome.State, outcome.HelperFailure)
			}
			operation = *outcome.Operation
		case control.NetworkReleasePhaseRuntimeRelease, control.NetworkReleasePhaseVerifyEffects, control.NetworkReleasePhaseProjection:
			operation, err = client.StartNetworkRelease(ctx, control.StartNetworkReleaseRequest{IntentID: intent})
			if err != nil {
				return err
			}
		default:
			return errors.New("unsupported release checkpoint")
		}
		if err := releaseAdvanced(previous, operation); err != nil {
			return err
		}
	}
	return errors.New("release exceeded bounded checkpoint count " + strconv.Itoa(12))
}
