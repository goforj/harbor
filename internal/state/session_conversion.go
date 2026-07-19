package state

import (
	"fmt"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/models"
	"github.com/goforj/null/v6"
)

// projectSessionFromModel converts one generated row into a process-correlated domain session.
func projectSessionFromModel(row models.ProjectSession) (domain.ProjectSession, error) {
	key := durableKey(row.SessionId, row.Id)
	if row.Id <= 0 {
		return domain.ProjectSession{}, corruptStateError("project session", key, fmt.Errorf("database ID must be positive"))
	}
	if row.Generation <= 0 {
		return domain.ProjectSession{}, corruptStateError("project session", key, fmt.Errorf("generation must be positive"))
	}

	process, err := processEvidenceFromModel(row, key)
	if err != nil {
		return domain.ProjectSession{}, err
	}
	session := domain.ProjectSession{
		ID:               domain.SessionID(row.SessionId),
		ProjectID:        domain.ProjectID(row.ProjectId),
		Owner:            domain.SessionOwner(row.Owner),
		State:            domain.SessionState(row.State),
		DescriptorDigest: row.DescriptorDigest,
		CredentialDigest: row.CredentialDigest,
		Generation:       uint64(row.Generation),
		Process:          process,
		CreatedAt:        row.CreatedAt,
		UpdatedAt:        row.UpdatedAt,
	}
	if err := session.Validate(); err != nil {
		return domain.ProjectSession{}, corruptStateError("project session", key, err)
	}
	return session, nil
}

// projectSessionModelFromDomain prepares one validated session for generated repository persistence.
func projectSessionModelFromDomain(session domain.ProjectSession) (models.ProjectSession, error) {
	if err := session.Validate(); err != nil {
		return models.ProjectSession{}, err
	}
	generation, err := unsignedToModelInt("session generation", session.Generation, false)
	if err != nil {
		return models.ProjectSession{}, err
	}
	row := models.ProjectSession{
		SessionId:        string(session.ID),
		ProjectId:        string(session.ProjectID),
		Owner:            string(session.Owner),
		State:            string(session.State),
		DescriptorDigest: session.DescriptorDigest,
		CredentialDigest: session.CredentialDigest,
		Generation:       generation,
		CreatedAt:        session.CreatedAt,
		UpdatedAt:        session.UpdatedAt,
	}
	if session.Process != nil {
		row.Pid = null.IntFrom(session.Process.PID)
		row.BirthToken = null.StringFrom(session.Process.BirthToken)
		row.ExecutableIdentity = null.StringFrom(session.Process.ExecutableIdentity)
		row.ArgumentDigest = null.StringFrom(session.Process.ArgumentDigest)
	}
	return row, nil
}

// processEvidenceFromModel rejects partial security evidence before any caller can use a persisted PID.
func processEvidenceFromModel(row models.ProjectSession, key string) (*domain.ProcessEvidence, error) {
	validFields := 0
	for _, valid := range []bool{row.Pid.Valid, row.BirthToken.Valid, row.ExecutableIdentity.Valid, row.ArgumentDigest.Valid} {
		if valid {
			validFields++
		}
	}
	if validFields == 0 {
		return nil, nil
	}
	if validFields != 4 {
		return nil, corruptStateError("project session", key, fmt.Errorf("process evidence must be all present or all absent"))
	}
	return &domain.ProcessEvidence{
		PID:                row.Pid.Int64,
		BirthToken:         row.BirthToken.String,
		ExecutableIdentity: row.ExecutableIdentity.String,
		ArgumentDigest:     row.ArgumentDigest.String,
	}, nil
}
