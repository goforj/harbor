package projectprocess

import (
	"os"
	"os/signal"
	"syscall"
)

// waitForTerminationSignal lets the helper prove Harbor's graceful operating-system signal reaches it.
func waitForTerminationSignal() {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	<-signals
}

// signalIgnoreTermination makes deadline tests require Harbor's forceful process-tree path.
func signalIgnoreTermination() {
	signal.Ignore(os.Interrupt, syscall.SIGTERM)
}
