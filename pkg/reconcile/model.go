// Package reconcile contains the pure deterministic identity reconciliation
// engine. It has no network or database dependencies.
package reconcile

import (
	"context"
	"strings"
	"time"
)

// EventType identifies a workforce lifecycle fact.
type EventType string

const (
	EventHire            EventType = "hire"
	EventRehire          EventType = "rehire"
	EventDataUpdate      EventType = "data_update"
	EventLeave           EventType = "leave"
	EventReturnFromLeave EventType = "return_from_leave"
	EventConversion      EventType = "conversion"
	EventTransfer        EventType = "transfer"
	EventTermination     EventType = "termination"
)

// IsHire reports whether the event creates or restores an identity.
func (e EventType) IsHire() bool {
	return e == EventHire || e == EventRehire
}

// SourceScope identifies the semantic view that produced the event.
type SourceScope string

const (
	SourceHireSnapshot SourceScope = "hire_snapshot"
	SourceLifecycle    SourceScope = "lifecycle"
)

// DecisionAction is the action selected for a canonical fact.
type DecisionAction string

const (
	ActionProcessNow DecisionAction = "process_now"
	ActionSchedule   DecisionAction = "schedule"
	ActionSkip       DecisionAction = "skip"
	ActionNoOp       DecisionAction = "no_op"
)

// ReasonCode explains a decision without relying on free-form prose.
type ReasonCode string

const (
	ReasonAccepted            ReasonCode = "accepted"
	ReasonAcceptedRetroactive ReasonCode = "accepted_retroactive"
	ReasonScheduledFutureHire ReasonCode = "scheduled_future_hire"
	ReasonFutureLifecycle     ReasonCode = "future_lifecycle_not_scheduled"
	ReasonDuplicateDelivery   ReasonCode = "duplicate_delivery"
	ReasonDuplicateOrOlder    ReasonCode = "duplicate_or_older"
	ReasonPreCutoff           ReasonCode = "pre_cutoff"
	ReasonPastEndTime         ReasonCode = "past_end_time"
	ReasonScopeMismatch       ReasonCode = "scope_mismatch"
)

// LifecycleState is the desired account lifecycle state.
type LifecycleState string

const (
	StateUnknown    LifecycleState = "unknown"
	StateActive     LifecycleState = "active"
	StateOnLeave    LifecycleState = "on_leave"
	StateTerminated LifecycleState = "terminated"
)

// PhysicalEvent is an immutable record received from a source system.
//
// ReceivedAt is intentionally absent. Delivery time belongs to the ingestion
// boundary and must not affect freshness or canonical identity.
type PhysicalEvent struct {
	EventKey         string         `json:"event_key"`
	SubjectID        string         `json:"subject_id"`
	EventType        EventType      `json:"event_type"`
	SourceScope      SourceScope    `json:"source_scope"`
	EffectiveTime    time.Time      `json:"effective_time"`
	EndTime          *time.Time     `json:"end_time,omitempty"`
	ModificationTime time.Time      `json:"modification_time"`
	SourceSequence   int64          `json:"source_sequence"`
	RevisionNumber   int64          `json:"revision_number"`
	Payload          map[string]any `json:"payload"`
}

// FreshnessTuple orders revisions of one logical fact.
//
// SourceSequence is compared first because it represents the source's
// monotonic ordering. ModificationTime resolves equal-sequence revisions.
// RevisionNumber and PhysicalEventKey make ties deterministic.
type FreshnessTuple struct {
	SourceSequence   int64     `json:"source_sequence"`
	ModificationTime time.Time `json:"modification_time"`
	RevisionNumber   int64     `json:"revision_number"`
	PhysicalEventKey string    `json:"physical_event_key"`
}

// Compare returns -1, 0, or 1 according to the freshness ordering.
func (f FreshnessTuple) Compare(other FreshnessTuple) int {
	if f.SourceSequence < other.SourceSequence {
		return -1
	}
	if f.SourceSequence > other.SourceSequence {
		return 1
	}
	if f.ModificationTime.Before(other.ModificationTime) {
		return -1
	}
	if f.ModificationTime.After(other.ModificationTime) {
		return 1
	}
	if f.RevisionNumber < other.RevisionNumber {
		return -1
	}
	if f.RevisionNumber > other.RevisionNumber {
		return 1
	}
	return strings.Compare(f.PhysicalEventKey, other.PhysicalEventKey)
}

// CanonicalFact is the normalized representation of a physical event.
type CanonicalFact struct {
	FactKey          string            `json:"fact_key"`
	EventKey         string            `json:"event_key"`
	SubjectID        string            `json:"subject_id"`
	EventType        EventType         `json:"event_type"`
	SourceScope      SourceScope       `json:"source_scope"`
	EffectiveTime    time.Time         `json:"effective_time"`
	EndTime          *time.Time        `json:"end_time,omitempty"`
	ModificationTime time.Time         `json:"modification_time"`
	ReceivedAt       time.Time         `json:"received_at"`
	Freshness        FreshnessTuple    `json:"freshness"`
	PayloadHash      string            `json:"payload_hash"`
	Payload          map[string]any    `json:"payload"`
	Attributes       map[string]string `json:"attributes"`
}

// IdentityProjection is the desired state for one subject.
type IdentityProjection struct {
	SubjectID      string            `json:"subject_id"`
	Exists         bool              `json:"exists"`
	Enabled        bool              `json:"enabled"`
	LifecycleState LifecycleState    `json:"lifecycle_state"`
	Attributes     map[string]string `json:"attributes"`
	SourceFactKey  string            `json:"source_fact_key"`
	SourceEventKey string            `json:"source_event_key"`
	EffectiveTime  time.Time         `json:"effective_time"`
	Freshness      FreshnessTuple    `json:"freshness"`
	RowVersion     int64             `json:"row_version,omitempty"`
}

// DesiredState is an alias that makes adapter code read naturally.
type DesiredState = IdentityProjection

// ObservedState is the state read from a target identity system.
type ObservedState struct {
	Exists     bool              `json:"exists"`
	Enabled    bool              `json:"enabled"`
	Attributes map[string]string `json:"attributes"`
}

// TargetAdapter must read the target before applying a mutation.
type TargetAdapter interface {
	Observe(ctx context.Context, subjectID string) (ObservedState, error)
	Apply(ctx context.Context, desired DesiredState) error
}

// ConvergenceAction describes whether a target mutation occurred.
type ConvergenceAction string

const (
	ConvergenceUnchanged ConvergenceAction = "unchanged"
	ConvergenceUpdated   ConvergenceAction = "updated"
)

// ConvergenceResult reports the result of a read-before-write operation.
type ConvergenceResult struct {
	Action   ConvergenceAction `json:"action"`
	Observed ObservedState     `json:"observed"`
}

// EventResolution contains the decision for one physical event.
type EventResolution struct {
	EventKey string         `json:"event_key"`
	FactKey  string         `json:"fact_key"`
	Action   DecisionAction `json:"action"`
	Reason   ReasonCode     `json:"reason"`
	Fact     CanonicalFact  `json:"fact"`
}

// BatchResult contains deterministic decisions and current projections.
type BatchResult struct {
	Decisions   []EventResolution             `json:"decisions"`
	Projections map[string]IdentityProjection `json:"projections"`
}
