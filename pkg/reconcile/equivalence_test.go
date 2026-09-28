package reconcile

import (
	"context"
	"testing"
)

func TestConvergeDistinguishesMissingAndEmptyAttributes(t *testing.T) {
	for _, test := range []struct {
		name       string
		observed   map[string]string
		desired    map[string]string
		wantAction ConvergenceAction
		wantWrites int
	}{
		{
			name:       "different empty attribute keys require a write",
			observed:   map[string]string{"old_attribute": ""},
			desired:    map[string]string{"new_attribute": ""},
			wantAction: ConvergenceUpdated,
			wantWrites: 1,
		},
		{
			name:       "matching empty attributes need no write",
			observed:   map[string]string{"attribute": ""},
			desired:    map[string]string{"attribute": ""},
			wantAction: ConvergenceUnchanged,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			adapter := &fakeAdapter{observed: ObservedState{
				Exists: true, Enabled: true, Attributes: test.observed,
			}}
			desired := DesiredState{
				SubjectID: "person-001", Exists: true, Enabled: true,
				LifecycleState: StateActive, Attributes: test.desired,
			}
			result, err := Converge(context.Background(), adapter, desired)
			if err != nil {
				t.Fatal(err)
			}
			if result.Action != test.wantAction || adapter.applyCount != test.wantWrites {
				t.Fatalf("got action=%s writes=%d; want action=%s writes=%d",
					result.Action, adapter.applyCount, test.wantAction, test.wantWrites)
			}
		})
	}
}
