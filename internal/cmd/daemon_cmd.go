package cmd

// DaemonCmd groups commands that inspect the current user's Harbor daemon.
type DaemonCmd struct {
	Status   DaemonStatusCmd   `cmd:""`
	Snapshot DaemonSnapshotCmd `cmd:""`
}

// NewDaemonCmd assembles the daemon command group.
func NewDaemonCmd(status *DaemonStatusCmd, snapshot *DaemonSnapshotCmd) *DaemonCmd {
	return &DaemonCmd{
		Status:   *status,
		Snapshot: *snapshot,
	}
}

// Signature defines CLI metadata for the daemon command group.
func (*DaemonCmd) Signature() string {
	return `name:"daemon" help:"Inspect the local Harbor daemon"`
}
