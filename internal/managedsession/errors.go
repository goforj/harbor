package managedsession

import "errors"

// ErrManagedSessionAwaitingAttach identifies the short startup window before Harbor records process evidence.
var ErrManagedSessionAwaitingAttach = errors.New("managed session is awaiting process attachment")

// ErrManagedSessionNotReady identifies a valid attached session whose dependent runtime evidence is still settling.
var ErrManagedSessionNotReady = errors.New("managed session runtime is not ready")

// ErrManagedSessionNetworkSetupRequired identifies a managed start that cannot proceed until Harbor reaches the required network stage.
var ErrManagedSessionNetworkSetupRequired = errors.New("managed session network setup is required")
