package reconcile

import (
	"context"
	"fmt"
	"sort"
)

// ResolveBatch canonicalizes, deduplicates, orders, and projects a batch.
//
// The output does not depend on input delivery order. A batch may contain
// events for more than one subject; projections are returned by subject ID.
func ResolveBatch(events []PhysicalEvent, policy Policy) (BatchResult, error) {
	if err := policy.Validate(); err != nil {
		return BatchResult{}, err
	}

	facts := make([]CanonicalFact, 0, len(events))
	decisions := make([]EventResolution, 0, len(events))
	seenEventKeys := make(map[string]CanonicalFact)

	for _, event := range events {
		fact, err := Canonicalize(event, policy.Now)
		if err != nil {
			return BatchResult{}, fmt.Errorf("canonicalize %q: %w", event.EventKey, err)
		}
		if previous, exists := seenEventKeys[fact.EventKey]; exists {
			if previous.PayloadHash != fact.PayloadHash || previous.FactKey != fact.FactKey {
				return BatchResult{}, fmt.Errorf("event_key %q was reused for a different event", fact.EventKey)
			}
			decisions = append(decisions, EventResolution{
				EventKey: fact.EventKey,
				FactKey:  fact.FactKey,
				Action:   ActionSkip,
				Reason:   ReasonDuplicateDelivery,
				Fact:     fact,
			})
			continue
		}
		seenEventKeys[fact.EventKey] = fact
		facts = append(facts, fact)
	}

	grouped := make(map[string][]CanonicalFact)
	for _, fact := range facts {
		grouped[fact.FactKey] = append(grouped[fact.FactKey], fact)
	}

	winners := make(map[string]CanonicalFact, len(grouped))
	for factKey, candidates := range grouped {
		winner := candidates[0]
		for _, candidate := range candidates[1:] {
			if candidate.Freshness.Compare(winner.Freshness) > 0 {
				winner = candidate
			}
		}
		winners[factKey] = winner
	}

	for _, fact := range facts {
		winner := winners[fact.FactKey]
		if fact.EventKey != winner.EventKey {
			decisions = append(decisions, EventResolution{
				EventKey: fact.EventKey,
				FactKey:  fact.FactKey,
				Action:   ActionSkip,
				Reason:   ReasonDuplicateOrOlder,
				Fact:     fact,
			})
			continue
		}
		action, reason, err := policy.Evaluate(fact)
		if err != nil {
			return BatchResult{}, fmt.Errorf("evaluate %q: %w", fact.EventKey, err)
		}
		decisions = append(decisions, EventResolution{
			EventKey: fact.EventKey,
			FactKey:  fact.FactKey,
			Action:   action,
			Reason:   reason,
			Fact:     fact,
		})
	}

	sort.Slice(decisions, func(i, j int) bool {
		if decisions[i].EventKey != decisions[j].EventKey {
			return decisions[i].EventKey < decisions[j].EventKey
		}
		return decisions[i].FactKey < decisions[j].FactKey
	})

	acceptedFacts := make([]CanonicalFact, 0, len(decisions))
	for _, decision := range decisions {
		if decision.Action != ActionProcessNow {
			continue
		}
		acceptedFacts = append(acceptedFacts, decision.Fact)
	}
	sort.Slice(acceptedFacts, func(i, j int) bool {
		left := acceptedFacts[i]
		right := acceptedFacts[j]
		if left.EffectiveTime.Before(right.EffectiveTime) {
			return true
		}
		if left.EffectiveTime.After(right.EffectiveTime) {
			return false
		}
		if freshness := left.Freshness.Compare(right.Freshness); freshness != 0 {
			return freshness < 0
		}
		if left.EventType != right.EventType {
			return left.EventType < right.EventType
		}
		return left.EventKey < right.EventKey
	})

	projections := make(map[string]IdentityProjection)
	for _, fact := range acceptedFacts {
		decision := EventResolution{Fact: fact}
		projection, exists := projections[decision.Fact.SubjectID]
		if !exists {
			projection = IdentityProjection{
				SubjectID:  decision.Fact.SubjectID,
				Attributes: make(map[string]string),
			}
		}
		applyFact(&projection, decision.Fact)
		projections[decision.Fact.SubjectID] = projection
	}

	return BatchResult{
		Decisions:   decisions,
		Projections: projections,
	}, nil
}

func applyFact(projection *IdentityProjection, fact CanonicalFact) {
	if projection.Attributes == nil {
		projection.Attributes = make(map[string]string)
	}
	for key, value := range fact.Attributes {
		projection.Attributes[key] = value
	}

	switch fact.EventType {
	case EventHire, EventRehire:
		projection.Exists = true
		projection.Enabled = true
		projection.LifecycleState = StateActive
	case EventLeave:
		projection.Exists = true
		projection.Enabled = false
		projection.LifecycleState = StateOnLeave
	case EventReturnFromLeave:
		projection.Exists = true
		projection.Enabled = true
		projection.LifecycleState = StateActive
	case EventTermination:
		projection.Exists = true
		projection.Enabled = false
		projection.LifecycleState = StateTerminated
	case EventDataUpdate, EventConversion, EventTransfer:
		// These events change attributes but do not infer account existence.
	}

	projection.SourceFactKey = fact.FactKey
	projection.SourceEventKey = fact.EventKey
	projection.EffectiveTime = fact.EffectiveTime
	projection.Freshness = fact.Freshness
}

// StateEquivalent compares only target-observable state.
func StateEquivalent(observed ObservedState, desired DesiredState) bool {
	if observed.Exists != desired.Exists {
		return false
	}
	if !desired.Exists {
		return true
	}
	if observed.Enabled != desired.Enabled {
		return false
	}
	return stringMapsEqual(observed.Attributes, desired.Attributes)
}

// Converge performs a read-before-write target operation.
func Converge(ctx context.Context, adapter TargetAdapter, desired DesiredState) (ConvergenceResult, error) {
	if adapter == nil {
		return ConvergenceResult{}, fmt.Errorf("target adapter is required")
	}
	observed, err := adapter.Observe(ctx, desired.SubjectID)
	if err != nil {
		return ConvergenceResult{}, fmt.Errorf("observe %q: %w", desired.SubjectID, err)
	}
	if StateEquivalent(observed, desired) {
		return ConvergenceResult{
			Action:   ConvergenceUnchanged,
			Observed: observed,
		}, nil
	}
	if err := adapter.Apply(ctx, desired); err != nil {
		return ConvergenceResult{}, fmt.Errorf("apply %q: %w", desired.SubjectID, err)
	}
	return ConvergenceResult{
		Action:   ConvergenceUpdated,
		Observed: observed,
	}, nil
}

func stringMapsEqual(left, right map[string]string) bool {
	if len(left) != len(right) {
		return false
	}
	for key, value := range left {
		if right[key] != value {
			return false
		}
	}
	return true
}
