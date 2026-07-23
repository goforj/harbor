package cmd

// DaemonCmd groups commands that inspect and control the current user's Harbor daemon.
type DaemonCmd struct {
	Status   DaemonStatusCmd   `cmd:""`
	Stop     DaemonStopCmd     `cmd:""`
	Snapshot DaemonSnapshotCmd `cmd:""`
	Release  ReleaseCmd        `cmd:""`
}

// NewDaemonCmd assembles the daemon command group.
func NewDaemonCmd(status *DaemonStatusCmd, stop *DaemonStopCmd, snapshot *DaemonSnapshotCmd, release *ReleaseCmd) *DaemonCmd {
	return &DaemonCmd{
		Status:   *status,
		Stop:     *stop,
		Snapshot: *snapshot,
		Release:  *release,
	}
}

// Signature defines CLI metadata for the daemon command group.
func (*DaemonCmd) Signature() string {
	return `name:"daemon" help:"Inspect and control the local Harbor daemon"`
}
