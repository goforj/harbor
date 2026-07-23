package launchdsocket

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"

	"github.com/goforj/harbor/internal/network/ingressrelay"
)

const (
	httpSocketName  = "HTTP"
	httpsSocketName = "HTTPS"
)

var (
	// ErrUnavailable reports that the current build cannot acquire launchd sockets.
	ErrUnavailable = errors.New("launchd socket activation is unavailable")

	launchdLocalhost = netip.MustParseAddr("127.0.0.1")
	httpEndpoint     = netip.AddrPortFrom(launchdLocalhost, 80)
	httpsEndpoint    = netip.AddrPortFrom(launchdLocalhost, 443)
)

// socketObservation contains only the native facts required before an activated descriptor becomes a Go listener.
type socketObservation struct {
	IPv4  bool
	TCP   bool
	Local netip.AddrPort
}

// nativeActivator acquires every descriptor registered under one fixed launchd socket name.
type nativeActivator func(string) ([]*os.File, error)

// socketInspector derives bounded protocol and address facts without changing descriptor ownership.
type socketInspector func(*os.File) (socketObservation, error)

// listenerConverter duplicates one descriptor into an independently owned Go listener.
type listenerConverter func(*os.File) (net.Listener, error)

// fileCloser releases one original descriptor after conversion or rejection.
type fileCloser func(*os.File) error

// activationDependencies keep native activation and descriptor conversion deterministic in portable tests.
type activationDependencies struct {
	activate nativeActivator
	inspect  socketInspector
	convert  listenerConverter
	close    fileCloser
}

// ActivateIngress acquires exactly Harbor's HTTP and HTTPS launchd sockets as one listener pair.
func ActivateIngress() (ingressrelay.Listeners, error) {
	return activateIngress(activationDependencies{
		activate: platformActivateSocket,
		inspect:  inspectPlatformSocket,
		convert:  net.FileListener,
		close:    func(file *os.File) error { return file.Close() },
	})
}

// activateIngress closes every native descriptor unless both exact listeners are safely converted.
func activateIngress(dependencies activationDependencies) (ingressrelay.Listeners, error) {
	httpFiles, err := dependencies.activate(httpSocketName)
	if err != nil {
		return ingressrelay.Listeners{}, errors.Join(
			fmt.Errorf("activate launchd HTTP socket: %w", err),
			closeActivatedFiles(dependencies.close, httpFiles),
		)
	}
	if len(httpFiles) != 1 {
		return ingressrelay.Listeners{}, errors.Join(
			fmt.Errorf("activate launchd HTTP socket: received %d descriptors, want exactly 1", len(httpFiles)),
			closeActivatedFiles(dependencies.close, httpFiles),
		)
	}

	httpsFiles, err := dependencies.activate(httpsSocketName)
	if err != nil {
		return ingressrelay.Listeners{}, errors.Join(
			fmt.Errorf("activate launchd HTTPS socket: %w", err),
			closeActivatedFiles(dependencies.close, httpFiles),
			closeActivatedFiles(dependencies.close, httpsFiles),
		)
	}
	if len(httpsFiles) != 1 {
		return ingressrelay.Listeners{}, errors.Join(
			fmt.Errorf("activate launchd HTTPS socket: received %d descriptors, want exactly 1", len(httpsFiles)),
			closeActivatedFiles(dependencies.close, httpFiles),
			closeActivatedFiles(dependencies.close, httpsFiles),
		)
	}

	files := []*os.File{httpFiles[0], httpsFiles[0]}
	descriptors, err := validateActivatedDescriptors(files)
	if err != nil {
		return ingressrelay.Listeners{}, errors.Join(err, closeActivatedFiles(dependencies.close, files))
	}
	if descriptors[0] == descriptors[1] {
		return ingressrelay.Listeners{}, errors.Join(
			fmt.Errorf("activate launchd ingress sockets: HTTP and HTTPS returned duplicate descriptor %d", descriptors[0]),
			closeActivatedFiles(dependencies.close, files),
		)
	}

	checks := []struct {
		name string
		file *os.File
		want netip.AddrPort
	}{
		{
			name: "HTTP",
			file: files[0],
			want: httpEndpoint,
		},
		{
			name: "HTTPS",
			file: files[1],
			want: httpsEndpoint,
		},
	}
	for _, check := range checks {
		observation, inspectErr := dependencies.inspect(check.file)
		if inspectErr != nil {
			return ingressrelay.Listeners{}, errors.Join(
				fmt.Errorf("inspect launchd %s descriptor: %w", check.name, inspectErr),
				closeActivatedFiles(dependencies.close, files),
			)
		}
		if validateErr := validateSocketObservation(check.name, observation, check.want); validateErr != nil {
			return ingressrelay.Listeners{}, errors.Join(
				validateErr,
				closeActivatedFiles(dependencies.close, files),
			)
		}
	}

	listeners := ingressrelay.Listeners{}
	listeners.HTTP, err = convertActivatedFile(dependencies, files[0], "HTTP")
	files[0] = nil
	if err != nil {
		return ingressrelay.Listeners{}, errors.Join(err, closeActivatedFiles(dependencies.close, files))
	}
	listeners.HTTPS, err = convertActivatedFile(dependencies, files[1], "HTTPS")
	files[1] = nil
	if err != nil {
		return ingressrelay.Listeners{}, errors.Join(
			err,
			closeIngressListeners(listeners),
			closeActivatedFiles(dependencies.close, files),
		)
	}

	return listeners, nil
}

// validateActivatedDescriptors rejects nil, closed, overflowing, or negative native descriptor identities.
func validateActivatedDescriptors(files []*os.File) ([]int, error) {
	descriptors := make([]int, len(files))
	for index, file := range files {
		descriptor, err := activatedDescriptorNumber(file)
		if err != nil {
			return nil, fmt.Errorf("activate launchd ingress descriptor %d: %w", index, err)
		}
		descriptors[index] = descriptor
	}
	return descriptors, nil
}

// activatedDescriptorNumber converts os.File's unsigned handle without accepting its closed-file sentinel.
func activatedDescriptorNumber(file *os.File) (int, error) {
	if file == nil {
		return 0, errors.New("descriptor file is nil")
	}
	native := file.Fd()
	if native == ^uintptr(0) {
		return 0, errors.New("descriptor is closed")
	}
	maximumInt := uintptr(^uint(0) >> 1)
	if native > maximumInt {
		return 0, fmt.Errorf("descriptor %d exceeds the platform integer range", native)
	}
	return int(native), nil
}

// validateSocketObservation requires the complete launchd socket contract before conversion.
func validateSocketObservation(name string, observation socketObservation, want netip.AddrPort) error {
	if !observation.IPv4 {
		return fmt.Errorf("launchd %s descriptor is not an IPv4 socket", name)
	}
	if !observation.TCP {
		return fmt.Errorf("launchd %s descriptor is not a TCP stream socket", name)
	}
	if observation.Local != want {
		return fmt.Errorf("launchd %s descriptor is bound to %s, want exactly %s", name, observation.Local, want)
	}
	return nil
}

// convertActivatedFile duplicates one validated socket and always closes its original descriptor.
func convertActivatedFile(dependencies activationDependencies, file *os.File, name string) (net.Listener, error) {
	listener, convertErr := dependencies.convert(file)
	closeErr := dependencies.close(file)
	if convertErr == nil && listener == nil {
		convertErr = errors.New("listener conversion returned nil")
	}
	if convertErr != nil || closeErr != nil {
		var listenerCloseErr error
		if listener != nil {
			listenerCloseErr = listener.Close()
		}
		var conversionError error
		if convertErr != nil {
			conversionError = fmt.Errorf("convert launchd %s descriptor: %w", name, convertErr)
		}
		return nil, errors.Join(
			conversionError,
			wrappedCloseError("close original launchd "+name+" descriptor", closeErr),
			wrappedCloseError("close converted launchd "+name+" listener", listenerCloseErr),
		)
	}
	return listener, nil
}

// closeActivatedFiles releases every non-nil original descriptor while retaining all cleanup failures.
func closeActivatedFiles(closeFile fileCloser, files []*os.File) error {
	var result error
	for _, file := range files {
		if file == nil {
			continue
		}
		result = errors.Join(result, wrappedCloseError("close activated launchd descriptor", closeFile(file)))
	}
	return result
}

// closeIngressListeners releases every converted listener after a later conversion failure.
func closeIngressListeners(listeners ingressrelay.Listeners) error {
	var result error
	if listeners.HTTP != nil {
		result = errors.Join(result, wrappedCloseError("close converted launchd HTTP listener", listeners.HTTP.Close()))
	}
	if listeners.HTTPS != nil {
		result = errors.Join(result, wrappedCloseError("close converted launchd HTTPS listener", listeners.HTTPS.Close()))
	}
	return result
}

// wrappedCloseError adds cleanup context only when a close operation failed.
func wrappedCloseError(operation string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", operation, err)
}
