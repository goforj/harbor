package main

import "io"

// invocationStreams contains the one admitted request and response channel for a helper process.
type invocationStreams struct {
	reader io.Reader
	writer io.Writer
	close  func() error
}
