package control

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

const (
	// A maximally escaped project identifier remains below this fixed inspection envelope.
	maximumProjectRuntimeRepairInspectionRequestBytes = 2048
	// The two 64-character selectors and one maximally escaped project identifier remain below this envelope.
	maximumProjectRuntimeRepairConfirmationRequestBytes = 4096
)

// decodeInspectProjectRuntimeRepairRequest rejects hidden authority beyond one project selection.
func decodeInspectProjectRuntimeRepairRequest(payload []byte) (InspectProjectRuntimeRepairRequest, error) {
	if len(payload) == 0 || len(payload) > maximumProjectRuntimeRepairInspectionRequestBytes {
		return InspectProjectRuntimeRepairRequest{}, errors.New("project runtime repair inspection request exceeds its bounded object shape")
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	opening, err := decoder.Token()
	if err != nil {
		return InspectProjectRuntimeRepairRequest{}, fmt.Errorf("decode project runtime repair inspection request: %w", err)
	}
	if delimiter, ok := opening.(json.Delim); !ok || delimiter != '{' {
		return InspectProjectRuntimeRepairRequest{}, errors.New("project runtime repair inspection request must be an object")
	}

	var request InspectProjectRuntimeRepairRequest
	projectSeen := false
	for decoder.More() {
		fieldToken, err := decoder.Token()
		if err != nil {
			return InspectProjectRuntimeRepairRequest{}, fmt.Errorf("decode project runtime repair inspection field: %w", err)
		}
		field, ok := fieldToken.(string)
		if !ok {
			return InspectProjectRuntimeRepairRequest{}, errors.New("project runtime repair inspection field name must be a string")
		}
		if field != "project_id" {
			return InspectProjectRuntimeRepairRequest{}, fmt.Errorf("project runtime repair inspection request contains unknown field %q", field)
		}
		if projectSeen {
			return InspectProjectRuntimeRepairRequest{}, errors.New("project runtime repair inspection request contains duplicate field \"project_id\"")
		}
		if err := decoder.Decode(&request.ProjectID); err != nil {
			return InspectProjectRuntimeRepairRequest{}, fmt.Errorf("decode project runtime repair inspection project ID: %w", err)
		}
		projectSeen = true
	}
	closing, err := decoder.Token()
	if err != nil {
		return InspectProjectRuntimeRepairRequest{}, fmt.Errorf("decode project runtime repair inspection request end: %w", err)
	}
	if delimiter, ok := closing.(json.Delim); !ok || delimiter != '}' {
		return InspectProjectRuntimeRepairRequest{}, errors.New("project runtime repair inspection request object is not terminated")
	}
	if !projectSeen {
		return InspectProjectRuntimeRepairRequest{}, errors.New("project runtime repair inspection request requires project_id")
	}
	if err := requireJSONEnd(decoder); err != nil {
		return InspectProjectRuntimeRepairRequest{}, err
	}
	if err := request.Validate(); err != nil {
		return InspectProjectRuntimeRepairRequest{}, err
	}
	return request, nil
}

// decodeConfirmProjectRuntimeRepairRequest rejects process, network, and durable fences supplied by a client.
func decodeConfirmProjectRuntimeRepairRequest(payload []byte) (ConfirmProjectRuntimeRepairRequest, error) {
	if len(payload) == 0 || len(payload) > maximumProjectRuntimeRepairConfirmationRequestBytes {
		return ConfirmProjectRuntimeRepairRequest{}, errors.New("project runtime repair confirmation request exceeds its bounded object shape")
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	opening, err := decoder.Token()
	if err != nil {
		return ConfirmProjectRuntimeRepairRequest{}, fmt.Errorf("decode project runtime repair confirmation request: %w", err)
	}
	if delimiter, ok := opening.(json.Delim); !ok || delimiter != '{' {
		return ConfirmProjectRuntimeRepairRequest{}, errors.New("project runtime repair confirmation request must be an object")
	}

	var request ConfirmProjectRuntimeRepairRequest
	projectSeen := false
	inspectionSeen := false
	fingerprintSeen := false
	for decoder.More() {
		fieldToken, err := decoder.Token()
		if err != nil {
			return ConfirmProjectRuntimeRepairRequest{}, fmt.Errorf("decode project runtime repair confirmation field: %w", err)
		}
		field, ok := fieldToken.(string)
		if !ok {
			return ConfirmProjectRuntimeRepairRequest{}, errors.New("project runtime repair confirmation field name must be a string")
		}
		switch field {
		case "project_id":
			if projectSeen {
				return ConfirmProjectRuntimeRepairRequest{}, errors.New("project runtime repair confirmation request contains duplicate field \"project_id\"")
			}
			if err := decoder.Decode(&request.ProjectID); err != nil {
				return ConfirmProjectRuntimeRepairRequest{}, fmt.Errorf("decode project runtime repair confirmation project ID: %w", err)
			}
			projectSeen = true
		case "inspection_id":
			if inspectionSeen {
				return ConfirmProjectRuntimeRepairRequest{}, errors.New("project runtime repair confirmation request contains duplicate field \"inspection_id\"")
			}
			if err := decoder.Decode(&request.InspectionID); err != nil {
				return ConfirmProjectRuntimeRepairRequest{}, fmt.Errorf("decode project runtime repair confirmation inspection ID: %w", err)
			}
			inspectionSeen = true
		case "candidate_fingerprint":
			if fingerprintSeen {
				return ConfirmProjectRuntimeRepairRequest{}, errors.New("project runtime repair confirmation request contains duplicate field \"candidate_fingerprint\"")
			}
			if err := decoder.Decode(&request.Fingerprint); err != nil {
				return ConfirmProjectRuntimeRepairRequest{}, fmt.Errorf("decode project runtime repair confirmation fingerprint: %w", err)
			}
			fingerprintSeen = true
		default:
			return ConfirmProjectRuntimeRepairRequest{}, fmt.Errorf("project runtime repair confirmation request contains unknown field %q", field)
		}
	}
	closing, err := decoder.Token()
	if err != nil {
		return ConfirmProjectRuntimeRepairRequest{}, fmt.Errorf("decode project runtime repair confirmation request end: %w", err)
	}
	if delimiter, ok := closing.(json.Delim); !ok || delimiter != '}' {
		return ConfirmProjectRuntimeRepairRequest{}, errors.New("project runtime repair confirmation request object is not terminated")
	}
	if !projectSeen || !inspectionSeen || !fingerprintSeen {
		return ConfirmProjectRuntimeRepairRequest{}, errors.New("project runtime repair confirmation request requires project_id, inspection_id, and candidate_fingerprint")
	}
	if err := requireJSONEnd(decoder); err != nil {
		return ConfirmProjectRuntimeRepairRequest{}, err
	}
	if err := request.Validate(); err != nil {
		return ConfirmProjectRuntimeRepairRequest{}, err
	}
	return request, nil
}
