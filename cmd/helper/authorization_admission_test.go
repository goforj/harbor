package main

import (
	"bytes"
	"errors"
	"io"
	"reflect"
	"strconv"
	"testing"
)

// TestAuthorizeExternalInvocationAcceptsExactlyOneRootAuthorization verifies the complete 32-byte form reaches one recheck.
func TestAuthorizeExternalInvocationAcceptsExactlyOneRootAuthorization(t *testing.T) {
	want := [authorizationExternalFormLength]byte{}
	for index := range want {
		want[index] = byte(index)
	}
	var imported [authorizationExternalFormLength]byte
	calls := 0
	err := authorizeExternalInvocation(bytes.NewReader(want[:]), 0, func(got [authorizationExternalFormLength]byte) error {
		calls++
		imported = got
		return nil
	})
	if err != nil {
		t.Fatalf("authorizeExternalInvocation() error = %v", err)
	}
	if calls != 1 || !reflect.DeepEqual(imported, want) {
		t.Fatalf("import calls = %d, external form = %#v, want one %#v", calls, imported, want)
	}
}

// TestAuthorizeExternalInvocationRejectsMalformedForms verifies missing, truncated, and extended capabilities never reach import.
func TestAuthorizeExternalInvocationRejectsMalformedForms(t *testing.T) {
	for _, size := range []int{0, authorizationExternalFormLength - 1, authorizationExternalFormLength + 1} {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			calls := 0
			err := authorizeExternalInvocation(bytes.NewReader(make([]byte, size)), 0, func([authorizationExternalFormLength]byte) error {
				calls++
				return nil
			})
			if err == nil {
				t.Fatalf("authorizeExternalInvocation(%d bytes) error = nil", size)
			}
			if calls != 0 {
				t.Fatalf("authorizeExternalInvocation(%d bytes) import calls = %d, want 0", size, calls)
			}
		})
	}
}

// TestAuthorizeExternalInvocationRequiresRootAndValidImport verifies neither identity nor Authorization Services rejection can be bypassed.
func TestAuthorizeExternalInvocationRequiresRootAndValidImport(t *testing.T) {
	form := make([]byte, authorizationExternalFormLength)
	importErr := errors.New("invalid authorization")
	called := false
	err := authorizeExternalInvocation(bytes.NewReader(form), 501, func([authorizationExternalFormLength]byte) error {
		called = true
		return nil
	})
	if err == nil || called {
		t.Fatalf("non-root authorization = (%v, called %t), want rejection before import", err, called)
	}

	err = authorizeExternalInvocation(bytes.NewReader(form), 0, func([authorizationExternalFormLength]byte) error {
		return importErr
	})
	if !errors.Is(err, importErr) {
		t.Fatalf("import error = %v, want %v", err, importErr)
	}
}

// TestAuthorizeExternalInvocationRequiresReader verifies a missing capability stream fails before import.
func TestAuthorizeExternalInvocationRequiresReader(t *testing.T) {
	if err := authorizeExternalInvocation(nil, 0, func([authorizationExternalFormLength]byte) error { return nil }); err == nil {
		t.Fatal("nil reader error = nil")
	}
}

// zeroProgressAuthorizationReader proves an unterminated capability stream cannot be mistaken for exact EOF.
type zeroProgressAuthorizationReader struct {
	reader *bytes.Reader
}

// Read returns the complete form and then violates the Reader progress contract instead of proving EOF.
func (reader *zeroProgressAuthorizationReader) Read(buffer []byte) (int, error) {
	if reader.reader.Len() == 0 {
		return 0, nil
	}
	return reader.reader.Read(buffer)
}

// TestAuthorizeExternalInvocationRequiresProvenEOF verifies a stalled FD cannot extend authorization after admission.
func TestAuthorizeExternalInvocationRequiresProvenEOF(t *testing.T) {
	reader := &zeroProgressAuthorizationReader{reader: bytes.NewReader(make([]byte, authorizationExternalFormLength))}
	calls := 0
	err := authorizeExternalInvocation(reader, 0, func([authorizationExternalFormLength]byte) error {
		calls++
		return nil
	})
	if err == nil || calls != 0 {
		t.Fatalf("authorizeExternalInvocation() = (%v, calls %d), want EOF rejection", err, calls)
	}
}

var _ io.Reader = (*zeroProgressAuthorizationReader)(nil)
