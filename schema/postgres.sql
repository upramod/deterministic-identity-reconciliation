-- PostgreSQL schema for the durable ingestion and canonical projection layers.
-- Delivery time is retained for audit. It is not used in freshness ordering.

CREATE TABLE IF NOT EXISTS physical_events (
    event_key TEXT PRIMARY KEY,
    subject_id TEXT NOT NULL,
    event_type TEXT NOT NULL,
    source_scope TEXT NOT NULL,
    effective_time TIMESTAMPTZ NOT NULL,
    end_time TIMESTAMPTZ,
    modification_time TIMESTAMPTZ NOT NULL,
    source_sequence BIGINT NOT NULL CHECK (source_sequence >= 0),
    revision_number BIGINT NOT NULL DEFAULT 0 CHECK (revision_number >= 0),
    payload JSONB NOT NULL,
    payload_hash CHAR(64) NOT NULL,
    received_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS ix_physical_events_subject_effective
    ON physical_events (subject_id, effective_time);

CREATE INDEX IF NOT EXISTS ix_physical_events_subject_freshness
    ON physical_events (
        subject_id,
        source_sequence,
        modification_time,
        revision_number
    );

CREATE TABLE IF NOT EXISTS canonical_facts (
    fact_key TEXT PRIMARY KEY,
    event_key TEXT NOT NULL REFERENCES physical_events(event_key),
    subject_id TEXT NOT NULL,
    event_type TEXT NOT NULL,
    source_scope TEXT NOT NULL,
    effective_time TIMESTAMPTZ NOT NULL,
    end_time TIMESTAMPTZ,
    modification_time TIMESTAMPTZ NOT NULL,
    source_sequence BIGINT NOT NULL CHECK (source_sequence >= 0),
    revision_number BIGINT NOT NULL DEFAULT 0 CHECK (revision_number >= 0),
    payload JSONB NOT NULL,
    attributes JSONB NOT NULL DEFAULT '{}'::jsonb,
    payload_hash CHAR(64) NOT NULL,
    received_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (subject_id, source_scope, event_type, effective_time)
);

CREATE INDEX IF NOT EXISTS ix_canonical_facts_subject_effective
    ON canonical_facts (subject_id, effective_time);

CREATE TABLE IF NOT EXISTS canonical_state (
    subject_id TEXT PRIMARY KEY,
    row_version BIGINT NOT NULL DEFAULT 0 CHECK (row_version >= 0),
    exists_flag BOOLEAN NOT NULL,
    enabled BOOLEAN NOT NULL,
    lifecycle_state TEXT NOT NULL,
    attributes JSONB NOT NULL DEFAULT '{}'::jsonb,
    source_fact_key TEXT NOT NULL,
    source_event_key TEXT NOT NULL,
    effective_time TIMESTAMPTZ NOT NULL,
    source_sequence BIGINT NOT NULL CHECK (source_sequence >= 0),
    source_modification_time TIMESTAMPTZ NOT NULL,
    source_revision_number BIGINT NOT NULL DEFAULT 0 CHECK (source_revision_number >= 0),
    source_physical_event_key TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS ix_canonical_state_source_order
    ON canonical_state (
        source_sequence,
        source_modification_time,
        source_revision_number,
        source_physical_event_key
    );
