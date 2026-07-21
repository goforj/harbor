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
