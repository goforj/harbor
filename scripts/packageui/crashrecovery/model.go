// Package main exercises recovery through an installed Harbor daemon on a disposable package worker.
package main

// daemonIdentity binds a crash to the authenticated peer and its installed native process facts.
type daemonIdentity struct {
	PID        uint32 `json:"pid"`
	UserID     string `json:"user_id"`
	Executable string `json:"executable"`
	Birth      string `json:"birth"`
}
