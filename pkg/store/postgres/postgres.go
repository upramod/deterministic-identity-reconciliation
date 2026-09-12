// Package postgres provides the durable PostgreSQL boundary for the
// deterministic reconciliation engine. It uses database/sql and does not
// impose a driver or ORM on callers.
package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/upramod/deterministic-identity-reconciliation/pkg/reconcile"
)

// Store persists physical events, canonical facts, and canonical projections.
type Store struct {
	db *sql.DB
}

// New creates a store around an existing database/sql connection.
func New(db *sql.DB) (*Store, error) {
	if db == nil {
		return nil, errors.New("database connection is required")
	}
	return &Store{db: db}, nil
}

const insertPhysicalEventSQL = `
INSERT INTO physical_events (
	event_key, subject_id, event_type, source_scope, effective_time, end_time,
	modification_time, source_sequence, revision_number, payload, payload_hash,
	received_at
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
ON CONFLICT (event_key) DO NOTHING`

// InsertPhysicalEvent stores an immutable physical event. The returned bool
// is false when the event key was already present.
func (s *Store) InsertPhysicalEvent(ctx context.Context, fact reconcile.CanonicalFact) (bool, error) {
	payload, err := json.Marshal(fact.Payload)
	if err != nil {
		return false, fmt.Errorf("marshal physical payload: %w", err)
	}
	result, err := s.db.ExecContext(ctx, insertPhysicalEventSQL,
		fact.EventKey,
		fact.SubjectID,
		fact.EventType,
		fact.SourceScope,
		fact.EffectiveTime,
		fact.EndTime,
		fact.ModificationTime,
		fact.Freshness.SourceSequence,
		fact.Freshness.RevisionNumber,
		payload,
		fact.PayloadHash,
		fact.ReceivedAt,
	)
	if err != nil {
		return false, fmt.Errorf("insert physical event: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("read physical event result: %w", err)
	}
	return rows == 1, nil
}

const upsertCanonicalFactSQL = `
INSERT INTO canonical_facts (
	fact_key, event_key, subject_id, event_type, source_scope, effective_time,
	end_time, modification_time, source_sequence, revision_number, payload,
	attributes, payload_hash, received_at
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
ON CONFLICT (fact_key) DO UPDATE
SET event_key = EXCLUDED.event_key,
	end_time = EXCLUDED.end_time,
	modification_time = EXCLUDED.modification_time,
	source_sequence = EXCLUDED.source_sequence,
	revision_number = EXCLUDED.revision_number,
	payload = EXCLUDED.payload,
	attributes = EXCLUDED.attributes,
	payload_hash = EXCLUDED.payload_hash,
	received_at = EXCLUDED.received_at,
	updated_at = CURRENT_TIMESTAMP
WHERE canonical_facts.source_sequence < EXCLUDED.source_sequence
	OR (
		canonical_facts.source_sequence = EXCLUDED.source_sequence
		AND canonical_facts.modification_time < EXCLUDED.modification_time
	)
	OR (
		canonical_facts.source_sequence = EXCLUDED.source_sequence
		AND canonical_facts.modification_time = EXCLUDED.modification_time
		AND canonical_facts.revision_number < EXCLUDED.revision_number
	)
	OR (
		canonical_facts.source_sequence = EXCLUDED.source_sequence
		AND canonical_facts.modification_time = EXCLUDED.modification_time
		AND canonical_facts.revision_number = EXCLUDED.revision_number
		AND canonical_facts.event_key < EXCLUDED.event_key
	)
RETURNING fact_key`

// UpsertCanonicalFact stores only the freshest revision for a logical fact.
func (s *Store) UpsertCanonicalFact(ctx context.Context, fact reconcile.CanonicalFact) (bool, error) {
	payload, err := json.Marshal(fact.Payload)
	if err != nil {
		return false, fmt.Errorf("marshal canonical payload: %w", err)
	}
	attributes, err := json.Marshal(fact.Attributes)
	if err != nil {
		return false, fmt.Errorf("marshal canonical attributes: %w", err)
	}

	var storedKey string
	err = s.db.QueryRowContext(ctx, upsertCanonicalFactSQL,
		fact.FactKey,
		fact.EventKey,
		fact.SubjectID,
		fact.EventType,
		fact.SourceScope,
		fact.EffectiveTime,
		fact.EndTime,
		fact.ModificationTime,
		fact.Freshness.SourceSequence,
		fact.Freshness.RevisionNumber,
		payload,
		attributes,
		fact.PayloadHash,
		fact.ReceivedAt,
	).Scan(&storedKey)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("upsert canonical fact: %w", err)
	}
	return storedKey == fact.FactKey, nil
}

const compareAndSetProjectionSQL = `
INSERT INTO canonical_state (
	subject_id, row_version, exists_flag, enabled, lifecycle_state, attributes,
	source_fact_key, source_event_key, effective_time, source_sequence,
	source_modification_time, source_revision_number, source_physical_event_key
)
VALUES ($1, 0, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
ON CONFLICT (subject_id) DO UPDATE
SET row_version = canonical_state.row_version + 1,
	exists_flag = EXCLUDED.exists_flag,
	enabled = EXCLUDED.enabled,
	lifecycle_state = EXCLUDED.lifecycle_state,
	attributes = EXCLUDED.attributes,
	source_fact_key = EXCLUDED.source_fact_key,
	source_event_key = EXCLUDED.source_event_key,
	effective_time = EXCLUDED.effective_time,
	source_sequence = EXCLUDED.source_sequence,
	source_modification_time = EXCLUDED.source_modification_time,
	source_revision_number = EXCLUDED.source_revision_number,
	source_physical_event_key = EXCLUDED.source_physical_event_key,
	updated_at = CURRENT_TIMESTAMP
WHERE canonical_state.row_version = $13
	AND (
		canonical_state.source_sequence < EXCLUDED.source_sequence
		OR (
			canonical_state.source_sequence = EXCLUDED.source_sequence
			AND canonical_state.source_modification_time < EXCLUDED.source_modification_time
		)
		OR (
			canonical_state.source_sequence = EXCLUDED.source_sequence
			AND canonical_state.source_modification_time = EXCLUDED.source_modification_time
			AND canonical_state.source_revision_number < EXCLUDED.source_revision_number
		)
		OR (
			canonical_state.source_sequence = EXCLUDED.source_sequence
			AND canonical_state.source_modification_time = EXCLUDED.source_modification_time
			AND canonical_state.source_revision_number = EXCLUDED.source_revision_number
			AND canonical_state.source_physical_event_key < EXCLUDED.source_physical_event_key
		)
	)
RETURNING row_version`

// CompareAndSetProjection atomically advances a subject projection.
//
// It returns false when another writer advanced the row or when the incoming
// freshness tuple is not newer than the persisted tuple.
func (s *Store) CompareAndSetProjection(
	ctx context.Context,
	expectedRowVersion int64,
	projection reconcile.IdentityProjection,
) (bool, error) {
	if expectedRowVersion < 0 {
		return false, errors.New("expected row version cannot be negative")
	}
	attributes, err := json.Marshal(projection.Attributes)
	if err != nil {
		return false, fmt.Errorf("marshal projection attributes: %w", err)
	}

	var rowVersion int64
	err = s.db.QueryRowContext(ctx, compareAndSetProjectionSQL,
		projection.SubjectID,
		projection.Exists,
		projection.Enabled,
		projection.LifecycleState,
		attributes,
		projection.SourceFactKey,
		projection.SourceEventKey,
		projection.EffectiveTime,
		projection.Freshness.SourceSequence,
		projection.Freshness.ModificationTime,
		projection.Freshness.RevisionNumber,
		projection.Freshness.PhysicalEventKey,
		expectedRowVersion,
	).Scan(&rowVersion)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("compare and set projection: %w", err)
	}
	return rowVersion >= 0, nil
}

const loadProjectionSQL = `
SELECT subject_id, row_version, exists_flag, enabled, lifecycle_state,
	attributes, source_fact_key, source_event_key, effective_time,
	source_sequence, source_modification_time, source_revision_number,
	source_physical_event_key
FROM canonical_state
WHERE subject_id = $1`

// LoadProjection reads the current durable projection for a subject.
func (s *Store) LoadProjection(ctx context.Context, subjectID string) (reconcile.IdentityProjection, error) {
	var projection reconcile.IdentityProjection
	var attributes []byte
	var state string
	var sourceSequence int64
	var sourceModificationTime time.Time
	var sourceRevisionNumber int64
	var sourcePhysicalEventKey string

	err := s.db.QueryRowContext(ctx, loadProjectionSQL, subjectID).Scan(
		&projection.SubjectID,
		&projection.RowVersion,
		&projection.Exists,
		&projection.Enabled,
		&state,
		&attributes,
		&projection.SourceFactKey,
		&projection.SourceEventKey,
		&projection.EffectiveTime,
		&sourceSequence,
		&sourceModificationTime,
		&sourceRevisionNumber,
		&sourcePhysicalEventKey,
	)
	if err != nil {
		return reconcile.IdentityProjection{}, err
	}
	if err := json.Unmarshal(attributes, &projection.Attributes); err != nil {
		return reconcile.IdentityProjection{}, fmt.Errorf("decode projection attributes: %w", err)
	}
	projection.LifecycleState = reconcile.LifecycleState(state)
	projection.Freshness = reconcile.FreshnessTuple{
		SourceSequence:   sourceSequence,
		ModificationTime: sourceModificationTime,
		RevisionNumber:   sourceRevisionNumber,
		PhysicalEventKey: sourcePhysicalEventKey,
	}
	return projection, nil
}
