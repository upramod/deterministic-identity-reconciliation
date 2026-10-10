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

const physicalEventMatchesSQL = `
SELECT subject_id = $2
	AND event_type = $3
	AND source_scope = $4
	AND effective_time = $5
	AND end_time IS NOT DISTINCT FROM $6
	AND modification_time = $7
	AND source_sequence = $8
	AND revision_number = $9
	AND payload = $10::jsonb
	AND payload_hash = $11
FROM physical_events
WHERE event_key = $1`

// InsertPhysicalEvent stores an immutable physical event. The returned bool
// is false for an exact replay. Reusing an event key for different immutable
// content returns an error. ReceivedAt is deliberately excluded from the
// comparison because delivery time can differ across retries.
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
	if rows == 1 {
		return true, nil
	}

	var exactReplay bool
	err = s.db.QueryRowContext(ctx, physicalEventMatchesSQL,
		fact.EventKey,
		fact.SubjectID,
		fact.EventType,
		fact.SourceScope,
		fact.EffectiveTime,
		fact.EndTime,
		fact.ModificationTime,
		fact.Freshness.SourceSequence,
		fact.Freshness.RevisionNumber,
		string(payload),
		fact.PayloadHash,
	).Scan(&exactReplay)
	if err != nil {
		return false, fmt.Errorf("verify physical event replay: %w", err)
	}
	if !exactReplay {
		return false, fmt.Errorf("event key %q conflicts with previously stored immutable event", fact.EventKey)
	}
	return false, nil
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

// A positive expected version requires an existing row. Use UPDATE rather than
// an upsert so a missing row cannot silently reset the caller's version history.
const updateProjectionSQL = `
UPDATE canonical_state
SET row_version = row_version + 1,
	exists_flag = $2,
	enabled = $3,
	lifecycle_state = $4,
	attributes = $5,
	source_fact_key = $6,
	source_event_key = $7,
	effective_time = $8,
	source_sequence = $9,
	source_modification_time = $10,
	source_revision_number = $11,
	source_physical_event_key = $12,
	updated_at = CURRENT_TIMESTAMP
WHERE subject_id = $1 AND row_version = $13
	AND (source_sequence, source_modification_time,
		source_revision_number, source_physical_event_key) < ($9, $10, $11, $12)
RETURNING row_version`

// CompareAndSetProjection atomically advances a subject projection.
//
// It returns false when another writer advanced the row or when the incoming
// freshness tuple is not newer than the persisted tuple. A positive expected
// version requires an existing row; a missing row returns false. Version zero
// retains the bootstrap convention: create a missing row at version zero or
// update a matching version-zero row when the incoming tuple is newer.
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

	query := compareAndSetProjectionSQL
	if expectedRowVersion > 0 {
		query = updateProjectionSQL
	}
	var rowVersion int64
	err = s.db.QueryRowContext(ctx, query,
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
	return loadProjection(ctx, s.db, loadProjectionSQL, subjectID)
}

// WithLockedProjection reads the current projection under a row lock and keeps
// that lock for the callback. Writers to that row and other locked readers wait
// until the callback and transaction finish. The caller must bound ctx and any
// external I/O. Remote side effects cannot be rolled back by this transaction.
func (s *Store) WithLockedProjection(ctx context.Context, subjectID string, apply func(reconcile.IdentityProjection) error) error {
	if apply == nil {
		return errors.New("projection callback is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin projection transaction: %w", err)
	}
	defer tx.Rollback()
	projection, err := loadProjection(ctx, tx, loadProjectionSQL+" FOR UPDATE", subjectID)
	if err != nil {
		return err
	}
	if err := apply(projection); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit projection transaction: %w", err)
	}
	return nil
}

type projectionQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func loadProjection(ctx context.Context, q projectionQuerier, query, subjectID string) (reconcile.IdentityProjection, error) {
	var projection reconcile.IdentityProjection
	var attributes []byte
	var state string
	var sourceSequence int64
	var sourceModificationTime time.Time
	var sourceRevisionNumber int64
	var sourcePhysicalEventKey string

	err := q.QueryRowContext(ctx, query, subjectID).Scan(
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
