package reconcile

import (
	"errors"
	"fmt"
	"time"
)

// Policy controls temporal and source-scope decisions.
type Policy struct {
	Now               time.Time
	CutoffTime        *time.Time
	RetroactiveWindow time.Duration
	HireWindow        time.Duration
	PastEndGrace      time.Duration
}

// DefaultPolicy returns the reference policy described by the framework.
func DefaultPolicy(now time.Time) Policy {
	return Policy{
		Now:               normalizeTime(now),
		RetroactiveWindow: 48 * time.Hour,
		HireWindow:        72 * time.Hour,
		PastEndGrace:      72 * time.Hour,
	}
}

// Validate checks that the policy can make deterministic decisions.
func (p Policy) Validate() error {
	if p.Now.IsZero() {
		return errors.New("policy Now is required")
	}
	if p.RetroactiveWindow < 0 {
		return errors.New("retroactive window cannot be negative")
	}
	if p.HireWindow < 0 {
		return errors.New("hire window cannot be negative")
	}
	if p.PastEndGrace < 0 {
		return errors.New("past-end grace cannot be negative")
	}
	return nil
}

// Evaluate selects an action for a canonical fact.
func (p Policy) Evaluate(fact CanonicalFact) (DecisionAction, ReasonCode, error) {
	if err := p.Validate(); err != nil {
		return "", "", err
	}

	if fact.SourceScope == SourceHireSnapshot && !fact.EventType.IsHire() {
		return ActionNoOp, ReasonScopeMismatch, nil
	}
	if fact.SourceScope == SourceLifecycle && fact.EventType.IsHire() {
		return ActionNoOp, ReasonScopeMismatch, nil
	}

	if fact.EndTime != nil && fact.EndTime.Before(p.Now.Add(-p.PastEndGrace)) {
		return ActionSkip, ReasonPastEndTime, nil
	}

	if p.CutoffTime != nil && fact.EffectiveTime.Before(normalizeTime(*p.CutoffTime)) {
		retroactiveLimit := p.Now.Add(-p.RetroactiveWindow)
		if fact.ModificationTime.Before(retroactiveLimit) {
			return ActionSkip, ReasonPreCutoff, nil
		}
		return ActionProcessNow, ReasonAcceptedRetroactive, nil
	}

	if fact.EffectiveTime.After(p.Now) {
		if !fact.EventType.IsHire() {
			return ActionSkip, ReasonFutureLifecycle, nil
		}
		if fact.EffectiveTime.Sub(p.Now) <= p.HireWindow {
			return ActionProcessNow, ReasonAccepted, nil
		}
		return ActionSchedule, ReasonScheduledFutureHire, nil
	}

	return ActionProcessNow, ReasonAccepted, nil
}

func (p Policy) String() string {
	return fmt.Sprintf("now=%s hire_window=%s retroactive_window=%s past_end_grace=%s",
		p.Now.Format(time.RFC3339), p.HireWindow, p.RetroactiveWindow, p.PastEndGrace)
}
