package helper

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// MaxRequestBytes is the hard upper bound for one helper request body.
const MaxRequestBytes = 16 * 1024

// MaxResponseBytes is the hard upper bound for one helper response body, including its newline.
const MaxResponseBytes = 16 * 1024

const maximumJSONDepth = 32

// DecodeRequest reads exactly one bounded strict JSON request.
func DecodeRequest(reader io.Reader) (Request, error) {
	body, err := readBounded(reader, MaxRequestBytes)
	if err != nil {
		return Request{}, err
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return Request{}, newRequestError(ErrorCodeInvalidJSON, "helper request is empty")
	}
	if err := rejectDuplicateJSONKeys(body); err != nil {
		return Request{}, newRequestError(ErrorCodeInvalidJSON, "helper request contains invalid or duplicate JSON fields")
	}
	if err := validateCanonicalRequestObject(body); err != nil {
		return Request{}, newRequestError(ErrorCodeInvalidJSON, "helper request JSON does not match the protocol")
	}

	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var request Request
	if err := decoder.Decode(&request); err != nil {
		return Request{}, newRequestError(ErrorCodeInvalidJSON, "helper request JSON does not match the protocol")
	}
	if err := requireJSONEnd(decoder); err != nil {
		return Request{}, err
	}
	return request, nil
}

// WriteResponse writes one bounded JSON response followed by a newline.
func WriteResponse(writer io.Writer, response Response) error {
	body, err := json.Marshal(response)
	if err != nil {
		return fmt.Errorf("marshal helper response: %w", err)
	}
	if len(body)+1 > MaxResponseBytes {
		return errors.New("helper response exceeds protocol bound")
	}
	body = append(body, '\n')
	written, err := writer.Write(body)
	if err != nil {
		return fmt.Errorf("write helper response: %w", err)
	}
	if written != len(body) {
		return io.ErrShortWrite
	}
	return nil
}

// ServeOnce reads, dispatches, and responds to one helper request without a long-lived listener.
func ServeOnce(ctx context.Context, reader io.Reader, writer io.Writer, dispatcher *Dispatcher) error {
	if dispatcher == nil {
		panic("helper.ServeOnce requires a non-nil dispatcher")
	}

	request, err := DecodeRequest(reader)
	if err != nil {
		if writeErr := WriteResponse(writer, responseForError(err)); writeErr != nil {
			return errors.Join(err, writeErr)
		}
		return err
	}
	response, dispatchErr := dispatcher.Dispatch(ctx, request)
	if writeErr := WriteResponse(writer, response); writeErr != nil {
		return errors.Join(dispatchErr, writeErr)
	}
	return dispatchErr
}

// validateCanonicalRequestObject rejects JSON aliases and keeps mutation data outside the wire protocol.
func validateCanonicalRequestObject(body []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil || fields == nil {
		return errors.New("helper request is not an object")
	}
	if len(fields) != 2 {
		return errors.New("helper request has the wrong field count")
	}
	for _, field := range []string{"version", "ticket_reference"} {
		value, found := fields[field]
		if !found || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return errors.New("helper request is missing a required field")
		}
	}
	return nil
}

// readBounded distinguishes an oversized body before JSON decoding allocates beyond the protocol limit.
func readBounded(reader io.Reader, maximum int64) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(reader, maximum+1))
	if err != nil {
		return nil, newRequestError(ErrorCodeInvalidJSON, "helper request could not be read")
	}
	if int64(len(body)) > maximum {
		return nil, newRequestError(ErrorCodeRequestTooLarge, "helper request exceeds the protocol bound")
	}
	return body, nil
}

// requireJSONEnd rejects concatenated values that could be interpreted differently by another decoder.
func requireJSONEnd(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return newRequestError(ErrorCodeInvalidJSON, "helper request must contain exactly one JSON value")
	}
	return nil
}

// rejectDuplicateJSONKeys rejects ambiguous objects before the typed decoder chooses a value.
func rejectDuplicateJSONKeys(body []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := walkJSONValue(decoder, 0); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errors.New("JSON contains trailing data")
	}
	return nil
}

// walkJSONValue recursively tracks object keys while consuming one JSON value.
func walkJSONValue(decoder *json.Decoder, depth int) error {
	if depth > maximumJSONDepth {
		return errors.New("JSON nesting exceeds the protocol bound")
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}

	switch delimiter {
	case '{':
		keys := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("JSON object key is not a string")
			}
			if _, exists := keys[key]; exists {
				return errors.New("JSON object contains a duplicate key")
			}
			for existing := range keys {
				if strings.EqualFold(existing, key) {
					return errors.New("JSON object contains a case-folding key collision")
				}
			}
			keys[key] = struct{}{}
			if err := walkJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim('}') {
			return errors.New("JSON object is not closed")
		}
	case '[':
		for decoder.More() {
			if err := walkJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim(']') {
			return errors.New("JSON array is not closed")
		}
	default:
		return errors.New("JSON value has an unexpected delimiter")
	}
	return nil
}
