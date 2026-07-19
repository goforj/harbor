package main

import (
	"errors"
	"fmt"
	"io"
)

const authorizationExternalFormLength = 32

// authorizationImporter internalizes and rechecks one complete Authorization Services external form.
type authorizationImporter func([authorizationExternalFormLength]byte) error

// authorizeExternalInvocation rejects ambient setuid execution unless one exact external form retains the approved right.
func authorizeExternalInvocation(reader io.Reader, effectiveUserID int, importer authorizationImporter) error {
	if reader == nil {
		return errors.New("authorization external form reader is required")
	}
	if effectiveUserID != 0 {
		return fmt.Errorf("helper effective user ID is %d, want root", effectiveUserID)
	}

	var externalForm [authorizationExternalFormLength]byte
	if _, err := io.ReadFull(reader, externalForm[:]); err != nil {
		return fmt.Errorf("read authorization external form: %w", err)
	}
	var trailing [1]byte
	written, err := reader.Read(trailing[:])
	if written != 0 || !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("authorization external form contains trailing data")
		}
		return fmt.Errorf("finish authorization external form: %w", err)
	}
	if err := importer(externalForm); err != nil {
		return fmt.Errorf("recheck authorization external form: %w", err)
	}
	return nil
}
