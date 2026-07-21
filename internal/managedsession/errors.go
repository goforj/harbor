package managedsession

import "errors"

// ErrManagedSessionAwaitingAttach identifies the short startup window before Harbor records process evidence.
var ErrManagedSessionAwaitingAttach = errors.New("managed session is awaiting process attachment")
