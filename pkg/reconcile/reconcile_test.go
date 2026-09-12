package reconcile

import (
	"context"
	"reflect"
	"testing"
	"time"
)

func testTime(value string) time.Time {
	return timeMustParse(value)
}

func timeMustParse(value string) time.Time {
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		panic(err)
	}
	return parsed
}

func event(key string, eventType EventType, scope SourceScope, effective, modified string, sequence int64, attributes map[string]any) PhysicalEvent {
	return PhysicalEvent{
		EventKey:         key,
		SubjectID:        "person-001",
		EventType:        eventType,
		SourceScope:      scope,
		EffectiveTime:    testTime(effective),
		ModificationTime: testTime(modified),
		SourceSequence:   sequence,
		Payload: map[string]any{
			"attributes": attributes,
		},
	}
}

func TestFreshnessUsesSequenceBeforeModificationTime(t *testing.T) {
	older := FreshnessTuple{
		SourceSequence:   1,
		ModificationTime: testTime("2026-01-02T00:00:00Z"),
		PhysicalEventKey: "a",
	}
	newer := FreshnessTuple{
		SourceSequence:   2,
		ModificationTime: testTime("2026-01-01T00:00:00Z"),
		PhysicalEventKey: "b",
	}
	if newer.Compare(older) <= 0 {
		t.Fatalf("expected higher source sequence to win")
	}
}

func TestCanonicalHashIgnoresDeliveryTime(t *testing.T) {
	physical := event(
		"evt-1",
		EventHire,
		SourceHireSnapshot,
		"2026-01-10T00:00:00Z",
		"2026-01-01T00:00:00Z",
		1,
		map[string]any{"email": "person@example.test"},
	)
	first, err := Canonicalize(physical, testTime("2026-01-01T00:01:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := Canonicalize(physical, testTime("2026-01-02T00:01:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	if first.PayloadHash != second.PayloadHash {
		t.Fatalf("delivery time changed canonical payload hash")
	}
	if first.ReceivedAt.Equal(second.ReceivedAt) {
		t.Fatalf("delivery time was not retained for audit")
	}
}

func TestResolveBatchIsPermutationInvariant(t *testing.T) {
	now := testTime("2026-09-12T00:00:00Z")
	policy := DefaultPolicy(now)
	events := []PhysicalEvent{
		event("hire-1", EventHire, SourceHireSnapshot, "2026-09-10T00:00:00Z", "2026-09-01T00:00:00Z", 10, map[string]any{
			"email":      "person@example.test",
			"department": "engineering",
		}),
		event("update-1", EventDataUpdate, SourceLifecycle, "2026-09-11T00:00:00Z", "2026-09-11T00:00:00Z", 11, map[string]any{
			"title": "engineer",
		}),
		event("termination-1", EventTermination, SourceLifecycle, "2026-09-13T00:00:00Z", "2026-09-11T00:00:00Z", 12, nil),
	}

	first, err := ResolveBatch(events, policy)
	if err != nil {
		t.Fatal(err)
	}
	permuted := []PhysicalEvent{events[2], events[0], events[1]}
	second, err := ResolveBatch(permuted, policy)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first.Projections, second.Projections) {
		t.Fatalf("projection changed with delivery order: %#v != %#v", first.Projections, second.Projections)
	}
}

func TestProjectionUsesEffectiveTimeNotEventKey(t *testing.T) {
	now := testTime("2026-09-12T00:00:00Z")
	policy := DefaultPolicy(now)
	events := []PhysicalEvent{
		event("z-hire", EventHire, SourceHireSnapshot, "2026-09-10T00:00:00Z", "2026-09-01T00:00:00Z", 10, nil),
		event("a-termination", EventTermination, SourceLifecycle, "2026-09-11T00:00:00Z", "2026-09-02T00:00:00Z", 11, nil),
	}
	result, err := ResolveBatch(events, policy)
	if err != nil {
		t.Fatal(err)
	}
	projection := result.Projections["person-001"]
	if projection.LifecycleState != StateTerminated || !projection.Exists || projection.Enabled {
		t.Fatalf("projection ignored effective-time order: %#v", projection)
	}
}

func TestOlderRevisionIsSkipped(t *testing.T) {
	now := testTime("2026-09-12T00:00:00Z")
	policy := DefaultPolicy(now)
	events := []PhysicalEvent{
		event("hire-old", EventHire, SourceHireSnapshot, "2026-09-10T00:00:00Z", "2026-09-01T00:00:00Z", 10, map[string]any{
			"department": "old",
		}),
		event("hire-new", EventHire, SourceHireSnapshot, "2026-09-10T00:00:00Z", "2026-09-02T00:00:00Z", 11, map[string]any{
			"department": "new",
		}),
	}
	result, err := ResolveBatch(events, policy)
	if err != nil {
		t.Fatal(err)
	}
	decisions := decisionByEventKey(result.Decisions)
	if decisions["hire-old"].Reason != ReasonDuplicateOrOlder {
		t.Fatalf("old event reason = %s", decisions["hire-old"].Reason)
	}
	if result.Projections["person-001"].Attributes["department"] != "new" {
		t.Fatalf("newer attribute was not projected")
	}
}

func TestFutureRules(t *testing.T) {
	now := testTime("2026-09-12T00:00:00Z")
	policy := DefaultPolicy(now)
	withinWindow := event("hire-soon", EventHire, SourceHireSnapshot, "2026-09-14T00:00:00Z", "2026-09-11T00:00:00Z", 1, nil)
	outsideWindow := event("hire-later", EventHire, SourceHireSnapshot, "2026-09-20T00:00:00Z", "2026-09-11T00:00:00Z", 2, nil)
	futureUpdate := event("update-future", EventDataUpdate, SourceLifecycle, "2026-09-14T00:00:00Z", "2026-09-11T00:00:00Z", 3, nil)
	futureTermination := event("termination-future", EventTermination, SourceLifecycle, "2026-09-14T00:00:00Z", "2026-09-11T00:00:00Z", 4, nil)

	for _, test := range []struct {
		name   string
		input  PhysicalEvent
		action DecisionAction
		reason ReasonCode
	}{
		{"hire within window", withinWindow, ActionProcessNow, ReasonAccepted},
		{"hire outside window", outsideWindow, ActionSchedule, ReasonScheduledFutureHire},
		{"future update", futureUpdate, ActionSkip, ReasonFutureLifecycle},
		{"future termination", futureTermination, ActionSkip, ReasonFutureLifecycle},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, err := ResolveBatch([]PhysicalEvent{test.input}, policy)
			if err != nil {
				t.Fatal(err)
			}
			decision := result.Decisions[0]
			if decision.Action != test.action || decision.Reason != test.reason {
				t.Fatalf("got %s/%s, want %s/%s", decision.Action, decision.Reason, test.action, test.reason)
			}
		})
	}
}

func TestReadBeforeWriteConvergence(t *testing.T) {
	desired := IdentityProjection{
		SubjectID:      "person-001",
		Exists:         true,
		Enabled:        true,
		LifecycleState: StateActive,
		Attributes:     map[string]string{"email": "person@example.test"},
	}
	adapter := &fakeAdapter{
		observed: ObservedState{
			Exists:     true,
			Enabled:    true,
			Attributes: map[string]string{"email": "person@example.test"},
		},
	}
	result, err := Converge(context.Background(), adapter, desired)
	if err != nil {
		t.Fatal(err)
	}
	if result.Action != ConvergenceUnchanged || adapter.applyCount != 0 {
		t.Fatalf("equivalent target was mutated: %#v", result)
	}

	adapter.observed.Enabled = false
	result, err = Converge(context.Background(), adapter, desired)
	if err != nil {
		t.Fatal(err)
	}
	if result.Action != ConvergenceUpdated || adapter.applyCount != 1 {
		t.Fatalf("changed target was not updated: %#v", result)
	}
}

type fakeAdapter struct {
	observed   ObservedState
	applyCount int
}

func (f *fakeAdapter) Observe(context.Context, string) (ObservedState, error) {
	return f.observed, nil
}

func (f *fakeAdapter) Apply(context.Context, DesiredState) error {
	f.applyCount++
	return nil
}

func decisionByEventKey(decisions []EventResolution) map[string]EventResolution {
	result := make(map[string]EventResolution, len(decisions))
	for _, decision := range decisions {
		result[decision.EventKey] = decision
	}
	return result
}
