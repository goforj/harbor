package managedsession

import "errors"

// ErrManagedSessionAwaitingAttach identifies the short startup window before Harbor records process evidence.
var ErrManagedSessionAwaitingAttach = errors.New("managed session is awaiting process attachment")

// ErrManagedSessionNotReady identifies a valid attached session whose dependent runtime evidence is still settling.
var ErrManagedSessionNotReady = errors.New("managed session runtime is not ready")
