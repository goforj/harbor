// Package main approves one exact disposable fixture removal through the production client protocol.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/goforj/harbor/internal/control"
	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/helper/launcher"
	"github.com/goforj/harbor/internal/projectapproval"
	"github.com/goforj/harbor/internal/rpc"
)

// selection binds this harness invocation to the pending CLI removal and its disposable checkout.
type selection struct {
	projectID   domain.ProjectID
	operationID domain.OperationID
	intentID    domain.IntentID
	revision    domain.Sequence
	directory   string
}

// main leaves native helper and daemon admission intact and never accepts ticket material from arguments.
func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// run connects only to the current user's installed daemon and replays the CLI's exact pending intent.
func run(arguments []string) error {
	flags := flag.NewFlagSet("fixtureapproval", flag.ContinueOnError)
	projectID := flags.String("project-id", "", "Disposable registered project ID")
	operationID := flags.String("operation-id", "", "Exact pending removal operation ID")
	intentID := flags.String("intent-id", "", "Exact CLI removal intent ID")
	revision := flags.Uint64("revision", 0, "Exact requires-approval operation revision")
	directory := flags.String("project-directory", "", "Exact disposable checkout directory")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	selected := selection{domain.ProjectID(*projectID), domain.OperationID(*operationID), domain.IntentID(*intentID), domain.Sequence(*revision), *directory}
	if err := selected.validate(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	client, err := control.NewClient(ctx, control.ClientConfig{Role: rpc.RoleDesktop})
	if err != nil {
		return err
	}
	defer client.Close()
	snapshot, err := client.Snapshot(ctx)
	if err != nil {
		return err
	}
	if err := selected.validateProject(snapshot.Projects); err != nil {
		return err
	}
	pending, err := client.UnregisterProject(ctx, control.UnregisterProjectRequest{ProjectID: selected.projectID, IntentID: selected.intentID})
	if err != nil {
		return err
	}
	if err := selected.validatePending(pending); err != nil {
		return err
	}
	runner := projectapproval.New(client, launcher.New(launcher.NewNativeTransport(), helper.SystemClock{}))
	outcome, err := runner.Execute(ctx, projectapproval.Request{OperationID: selected.operationID, ExpectedOperationRevision: selected.revision})
	if err != nil {
		return err
	}
	if err := selected.validateOutcome(outcome); err != nil {
		return err
	}
	fmt.Println("Fixture project removal approval completed.")
	return nil
}

// validateOutcome accepts only independently confirmed completion of the selected pending revision.
func (selected selection) validateOutcome(outcome projectapproval.Outcome) error {
	if outcome.State != projectapproval.Succeeded || outcome.Confirmation == nil || outcome.HelperFailure != nil {
		return fmt.Errorf("native fixture removal approval ended %s", outcome.State)
	}
	confirmation := *outcome.Confirmation
	if err := confirmation.Validate(); err != nil {
		return err
	}
	if confirmation.Operation.ID != selected.operationID || confirmation.Operation.ProjectID != selected.projectID || confirmation.Operation.IntentID != selected.intentID || confirmation.Operation.State != domain.OperationSucceeded || confirmation.Revision <= selected.revision {
		return errors.New("fixture removal confirmation differs from the selected operation")
	}
	return nil
}

// validate rejects authority without a complete, bounded operation selection and an existing disposable root.
func (selected selection) validate() error {
	if err := selected.projectID.Validate(); err != nil {
		return err
	}
	if err := selected.operationID.Validate(); err != nil {
		return err
	}
	if err := selected.intentID.Validate(); err != nil {
		return err
	}
	if selected.revision == 0 || selected.revision > domain.MaximumSequence {
		return errors.New("pending removal revision is outside the supported sequence range")
	}
	_, err := fixtureRoot(selected.directory)
	return err
}

// fixtureRoot confines approval to canonical package-UI checkouts without granting arbitrary project removal.
func fixtureRoot(directory string) (string, error) {
	if directory == "" || !filepath.IsAbs(directory) {
		return "", errors.New("absolute disposable project directory is required")
	}
	root, err := filepath.EvalSymlinks(directory)
	if err != nil {
		return "", err
	}
	temporary, err := filepath.EvalSymlinks("/tmp")
	if err != nil {
		return "", err
	}
	parent := filepath.Dir(root)
	if filepath.Base(root) != "project" || filepath.Dir(parent) != temporary || !strings.HasPrefix(filepath.Base(parent), "harbor-package-ui-") || filepath.Base(parent) == "harbor-package-ui-" {
		return "", errors.New("refusing approval outside disposable package-UI project")
	}
	info, err := os.Stat(root)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", errors.New("fixture checkout is not a directory")
	}
	return root, nil
}

// validateProject proves the authenticated snapshot still maps the selected ID to this exact fixture root.
func (selected selection) validateProject(projects []domain.ProjectSnapshot) error {
	expected, err := fixtureRoot(selected.directory)
	if err != nil {
		return err
	}
	for _, project := range projects {
		if project.ID != selected.projectID {
			continue
		}
		actual, err := fixtureRoot(project.Path)
		if err != nil {
			return err
		}
		if actual != expected {
			return errors.New("selected project belongs to another disposable checkout")
		}
		return nil
	}
	return errors.New("selected fixture is absent from the authenticated snapshot")
}

// validatePending refuses stale revisions and every unrelated or terminal daemon result before helper execution.
func (selected selection) validatePending(pending control.ProjectUnregistration) error {
	if err := pending.Validate(); err != nil {
		return err
	}
	operation := pending.Operation
	if operation.ID != selected.operationID || operation.ProjectID != selected.projectID || operation.IntentID != selected.intentID || operation.Kind != domain.OperationKindProjectUnregister || operation.State != domain.OperationRequiresApproval || pending.Revision != selected.revision {
		return errors.New("daemon removal is not the exact selected pending operation and revision")
	}
	return nil
}
