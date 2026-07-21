package projectprocess

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/goforj/harbor/internal/domain"
)

// OutputBrokerLaunchSpec gives an optional broker launcher the exact child output pipes and lifecycle identity.
//
// A launcher may duplicate or inherit Stdout and Stderr before returning. Once Launch returns successfully,
// Supervisor closes its copies and the attachment becomes the only output reader. The launcher does not receive
// process-stop authority; the managed child remains owned by Supervisor's platform process boundary.
type OutputBrokerLaunchSpec struct {
	// ProjectID identifies the registered project whose output is being launched.
	ProjectID domain.ProjectID
	// SessionID identifies the exact lifecycle whose output is being launched.
	SessionID domain.SessionID
	// OutputDirectory is the owner-private root used by the broker's journal.
	OutputDirectory string
	// Stdout is the parent read end of the child's standard-output pipe.
	Stdout *os.File
	// Stderr is the parent read end of the child's standard-error pipe.
	Stderr *os.File
}

// OutputBrokerAdoptionSpec identifies one owner-private broker that survived a Harbor restart.
//
// Adoption has no child-pipe inputs: the broker already owns those descriptors and only needs a fresh,
// independently authenticated Harbor reader. The persisted peer evidence remains separate from the
// managed GoForj process evidence so adopting output never grants stop or reap authority.
type OutputBrokerAdoptionSpec struct {
	// ProjectID identifies the registered project whose output is retained.
	ProjectID domain.ProjectID
	// SessionID identifies the exact lifecycle whose output is retained.
	SessionID domain.SessionID
	// OutputDirectory is the per-user root that owns the broker journal and manifest.
	OutputDirectory string
	// Peer is the complete broker process, endpoint, and manifest evidence persisted with the session.
	Peer OutputBrokerPeer
}

// Validate reports whether an adoption request contains one complete durable broker boundary.
func (spec OutputBrokerAdoptionSpec) Validate() error {
	if err := spec.ProjectID.Validate(); err != nil {
		return fmt.Errorf("output broker adoption project ID: %w", err)
	}
	if err := spec.SessionID.Validate(); err != nil {
		return fmt.Errorf("output broker adoption session ID: %w", err)
	}
	if spec.OutputDirectory == "" || !filepath.IsAbs(spec.OutputDirectory) || filepath.Clean(spec.OutputDirectory) != spec.OutputDirectory {
		return errors.New("output broker adoption output directory must be a canonical absolute path")
	}
	if err := spec.Peer.Validate(); err != nil {
		return fmt.Errorf("output broker adoption peer: %w", err)
	}
	if spec.Peer.ProjectID != spec.ProjectID || spec.Peer.SessionID != spec.SessionID {
		return errors.New("output broker adoption peer does not match lifecycle")
	}
	if spec.Peer.ManifestPath == "" || spec.Peer.TicketDigest == "" {
		return errors.New("output broker adoption requires owner-private manifest metadata")
	}
	return nil
}

// Validate reports whether a broker launcher received one complete, canonical launch boundary.
func (spec OutputBrokerLaunchSpec) Validate() error {
	if err := spec.ProjectID.Validate(); err != nil {
		return fmt.Errorf("output broker launch project ID: %w", err)
	}
	if err := spec.SessionID.Validate(); err != nil {
		return fmt.Errorf("output broker launch session ID: %w", err)
	}
	if spec.OutputDirectory == "" || !filepath.IsAbs(spec.OutputDirectory) || filepath.Clean(spec.OutputDirectory) != spec.OutputDirectory {
		return errors.New("output broker launch output directory must be a canonical absolute path")
	}
	if spec.Stdout == nil || spec.Stderr == nil {
		return errors.New("output broker launch stdout and stderr pipes are required")
	}
	return nil
}

// OutputBrokerAttachment receives journal records after a broker has adopted the child output pipes.
//
// Close retires the attachment transport and any broker process owned by that attachment. It must not
// signal, kill, or reap the managed child.
type OutputBrokerAttachment interface {
	// Peer returns the exact broker process evidence authenticated by the launcher.
	Peer() OutputBrokerPeer
	// Receive waits for the next replay/live record or returns a terminal attachment error.
	Receive(context.Context) (OutputBrokerRecord, error)
	// Close retires the attachment transport and broker-owned process without acquiring child lifecycle authority.
	Close() error
}

// OutputBrokerLauncher starts or adopts one process-surviving broker for the supplied output pipes.
//
// The launcher is optional. When absent or when its optional handoff fails, Supervisor retains direct pipe
// readers. A successful launcher must return an attachment whose peer identifies the same project and session
// as the launch spec.
type OutputBrokerLauncher interface {
	// Launch transfers output-pipe ownership to one authenticated broker attachment.
	Launch(context.Context, OutputBrokerLaunchSpec) (OutputBrokerAttachment, error)
}

// OutputBrokerAdopter is the optional restart boundary for a process-surviving broker.
//
// Launchers that do not implement this interface retain the existing safe behavior: Harbor can still
// display the checksummed historical spool, but it will not guess how to reconnect to a live broker.
type OutputBrokerAdopter interface {
	// Adopt attaches a fresh transport to one broker whose persisted process and manifest evidence were revalidated.
	Adopt(context.Context, OutputBrokerAdoptionSpec) (OutputBrokerAttachment, error)
}

// readOutputBrokerAttachment feeds authenticated broker records into the existing bounded output relay.
func readOutputBrokerAttachment(
	ctx context.Context,
	attachment OutputBrokerAttachment,
	relay *outputRelay,
	readers *sync.WaitGroup,
	onFailure func(),
) {
	defer readers.Done()
	defer attachment.Close()
	for {
		record, err := attachment.Receive(ctx)
		if err != nil {
			if onFailure != nil && !errors.Is(err, io.EOF) {
				onFailure()
			}
			return
		}
		if err := record.Validate(); err != nil {
			relay.dropped.Add(1)
			if onFailure != nil {
				onFailure()
			}
			return
		}
		if record.Gap != nil {
			relay.dropped.Add(record.Gap.DroppedRecords)
			continue
		}
		stream := outputStreamStdout
		if record.Frame.Stream == OutputBrokerStreamStderr {
			stream = outputStreamStderr
		}
		relay.offer(stream, []byte(record.Frame.Text))
	}
}
