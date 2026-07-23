//go:build darwin

package launchdsocket

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

// TestInspectSocketProtocolDoesNotProbeAcceptConnection verifies launchd sockets remain valid when macOS rejects SO_ACCEPTCONN.
func TestInspectSocketProtocolDoesNotProbeAcceptConnection(t *testing.T) {
	socketTypeError := errors.New("socket type unavailable")
	tcpOptionError := errors.New("TCP option unavailable")
	tests := []struct {
		name            string
		socketType      int
		socketTypeError error
		tcpOptionError  error
		wantTCP         bool
		wantError       string
		wantOptions     []int
	}{
		{
			name:        "TCP listener",
			socketType:  unix.SOCK_STREAM,
			wantTCP:     true,
			wantOptions: []int{unix.SO_TYPE, unix.TCP_NODELAY},
		},
		{
			name:            "socket type failure",
			socketTypeError: socketTypeError,
			wantError:       "read socket type",
			wantOptions:     []int{unix.SO_TYPE},
		},
		{
			name:        "non-stream socket",
			socketType:  unix.SOCK_DGRAM,
			wantOptions: []int{unix.SO_TYPE},
		},
		{
			name:           "non-TCP stream",
			socketType:     unix.SOCK_STREAM,
			tcpOptionError: unix.ENOPROTOOPT,
			wantOptions:    []int{unix.SO_TYPE, unix.TCP_NODELAY},
		},
		{
			name:           "TCP inspection failure",
			socketType:     unix.SOCK_STREAM,
			tcpOptionError: tcpOptionError,
			wantError:      "verify TCP socket protocol",
			wantOptions:    []int{unix.SO_TYPE, unix.TCP_NODELAY},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var options []int
			tcp, err := inspectSocketProtocol(42, func(_ int, _ int, option int) (int, error) {
				options = append(options, option)
				switch option {
				case unix.SO_TYPE:
					return test.socketType, test.socketTypeError
				case unix.TCP_NODELAY:
					return 0, test.tcpOptionError
				case unix.SO_ACCEPTCONN:
					return 0, unix.ENOPROTOOPT
				default:
					return 0, errors.New("unexpected socket option")
				}
			})
			if test.wantError == "" {
				if err != nil {
					t.Fatalf("inspectSocketProtocol() error = %v", err)
				}
			} else if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("inspectSocketProtocol() error = %v, want substring %q", err, test.wantError)
			}
			if tcp != test.wantTCP {
				t.Fatalf("inspectSocketProtocol() TCP = %t, want %t", tcp, test.wantTCP)
			}
			if !reflect.DeepEqual(options, test.wantOptions) {
				t.Fatalf("inspectSocketProtocol() options = %v, want %v", options, test.wantOptions)
			}
			for _, option := range options {
				if option == unix.SO_ACCEPTCONN {
					t.Fatal("inspectSocketProtocol() queried SO_ACCEPTCONN")
				}
			}
		})
	}
}

// TestInspectSocketProtocolUsesExpectedLevels verifies type and protocol probes use their matching socket levels.
func TestInspectSocketProtocolUsesExpectedLevels(t *testing.T) {
	type request struct {
		level  int
		option int
	}
	var requests []request
	tcp, err := inspectSocketProtocol(42, func(_ int, level int, option int) (int, error) {
		requests = append(requests, request{
			level:  level,
			option: option,
		})
		switch option {
		case unix.SO_TYPE:
			return unix.SOCK_STREAM, nil
		case unix.TCP_NODELAY:
			return 0, nil
		default:
			return 0, errors.New("unexpected socket option")
		}
	})
	if err != nil {
		t.Fatalf("inspectSocketProtocol() error = %v", err)
	}
	if !tcp {
		t.Fatal("inspectSocketProtocol() TCP = false, want true")
	}
	want := []request{
		{
			level:  unix.SOL_SOCKET,
			option: unix.SO_TYPE,
		},
		{
			level:  unix.IPPROTO_TCP,
			option: unix.TCP_NODELAY,
		},
	}
	if !reflect.DeepEqual(requests, want) {
		t.Fatalf("inspectSocketProtocol() requests = %v, want %v", requests, want)
	}
}
