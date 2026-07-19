// Package projectreadiness proves that a locally supervised GoForj App reached its generated readiness endpoint.
package projectreadiness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/goforj/harbor/internal/projectdiscovery"
)

const maximumReadinessBodyBytes int64 = 64 << 10

// HTTPClient is the bounded HTTP surface needed for one readiness observation.
type HTTPClient interface {
	Do(*http.Request) (*http.Response, error)
}

// State distinguishes an expected not-yet-ready observation from proven readiness.
type State string

const (
	// StatePending means the local listener is absent or returned an explicit not-ready response.
	StatePending State = "pending"
	// StateReady means the exact local target returned a valid ready response.
	StateReady State = "ready"
)

// Prober performs one bounded readiness observation without owning retry or process lifecycle policy.
type Prober struct {
	client HTTPClient
}

// NewProber creates a readiness observer around the caller's timeout-configured HTTP client.
func NewProber(client HTTPClient) *Prober {
	if client == nil {
		panic("projectreadiness.NewProber requires a non-nil HTTP client")
	}
	return &Prober{client: client}
}

// Probe returns ready only after the generated endpoint proves the expected default App identity.
func (prober *Prober) Probe(ctx context.Context, target projectdiscovery.RuntimeTarget) (State, error) {
	if prober == nil || prober.client == nil {
		panic("projectreadiness.Prober.Probe requires a constructed prober")
	}
	if err := target.Validate(); err != nil {
		return "", fmt.Errorf("readiness target: %w", err)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.ReadyURL, nil)
	if err != nil {
		return "", fmt.Errorf("create readiness request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	response, err := prober.client.Do(request)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", ctxErr
		}
		return StatePending, nil
	}
	if response == nil || response.Body == nil {
		return "", errors.New("readiness client returned an incomplete response")
	}
	defer response.Body.Close()

	switch response.StatusCode {
	case http.StatusServiceUnavailable:
		if err := drainBoundedReadinessBody(response.Body); err != nil {
			return "", err
		}
		return StatePending, nil
	case http.StatusOK:
		payload, err := decodeReadinessBody(response.Body)
		if err != nil {
			return "", err
		}
		if payload.Status != "ready" {
			return "", fmt.Errorf("readiness response status is %q, not %q", payload.Status, StateReady)
		}
		if payload.App != string(target.AppID) {
			return "", fmt.Errorf("readiness response App is %q, not %q", payload.App, target.AppID)
		}
		return StateReady, nil
	default:
		if err := drainBoundedReadinessBody(response.Body); err != nil {
			return "", err
		}
		return "", fmt.Errorf("readiness endpoint returned HTTP %d", response.StatusCode)
	}
}

// readinessPayload retains only the two generated public readiness fields.
type readinessPayload struct {
	Status string `json:"status"`
	App    string `json:"app"`
}

// decodeReadinessBody rejects oversized, malformed, concatenated, or incomplete successful responses.
func decodeReadinessBody(body io.Reader) (readinessPayload, error) {
	limited := &io.LimitedReader{R: body, N: maximumReadinessBodyBytes + 1}
	data, err := io.ReadAll(limited)
	if err != nil {
		return readinessPayload{}, fmt.Errorf("read readiness response: %w", err)
	}
	if int64(len(data)) > maximumReadinessBodyBytes {
		return readinessPayload{}, fmt.Errorf("readiness response exceeds %d bytes", maximumReadinessBodyBytes)
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	var payload readinessPayload
	if err := decoder.Decode(&payload); err != nil {
		return readinessPayload{}, fmt.Errorf("decode readiness response: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return readinessPayload{}, errors.New("decode readiness response: multiple JSON values")
		}
		return readinessPayload{}, fmt.Errorf("decode readiness response trailing data: %w", err)
	}
	if strings.TrimSpace(payload.Status) != payload.Status || strings.TrimSpace(payload.App) != payload.App || payload.Status == "" || payload.App == "" {
		return readinessPayload{}, errors.New("readiness response requires canonical status and App fields")
	}
	return payload, nil
}

// drainBoundedReadinessBody lets HTTP transports reuse a small response without accepting unbounded startup output.
func drainBoundedReadinessBody(body io.Reader) error {
	read, err := io.Copy(io.Discard, io.LimitReader(body, maximumReadinessBodyBytes+1))
	if err != nil {
		return fmt.Errorf("read readiness response: %w", err)
	}
	if read > maximumReadinessBodyBytes {
		return fmt.Errorf("readiness response exceeds %d bytes", maximumReadinessBodyBytes)
	}
	return nil
}
