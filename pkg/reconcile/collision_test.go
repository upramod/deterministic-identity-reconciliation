package reconcile

import "testing"

func TestCanonicalizeRejectsAmbiguousAttributeNames(t *testing.T) {
	for _, test := range []struct {
		name       string
		attributes any
		wantError  string
	}{
		{"conflicting JSON values", map[string]any{"department": "engineering", " department ": "security"}, `attribute names " department " and "department" normalize to "department"`},
		{"matching JSON values", map[string]any{"department": "engineering", " department ": "engineering"}, `attribute names " department " and "department" normalize to "department"`},
		{"conflicting string values", map[string]string{"department": "engineering", " department ": "security"}, `attribute names " department " and "department" normalize to "department"`},
		{"matching string values", map[string]string{"department": "engineering", " department ": "engineering"}, `attribute names " department " and "department" normalize to "department"`},
		{"whitespace JSON name", map[string]any{"  ": "engineering"}, `attribute name "  " is empty after trimming`},
		{"empty string name", map[string]string{"": "engineering"}, `attribute name "" is empty after trimming`},
		{"non-scalar collision", map[string]any{"department": nil, " department ": "engineering"}, `attribute names " department " and "department" normalize to "department"`},
	} {
		t.Run(test.name, func(t *testing.T) {
			physical := event("hire", EventHire, SourceHireSnapshot, "2026-09-10T00:00:00Z", "2026-09-01T00:00:00Z", 1, nil)
			physical.Payload["attributes"] = test.attributes
			_, err := Canonicalize(physical, testTime("2026-09-12T00:00:00Z"))
			if err == nil || err.Error() != test.wantError {
				t.Fatalf("got error %v; want %q", err, test.wantError)
			}
		})
	}
}

func TestCanonicalizeTrimsUnambiguousAttributeName(t *testing.T) {
	physical := event("hire", EventHire, SourceHireSnapshot, "2026-09-10T00:00:00Z", "2026-09-01T00:00:00Z", 1, map[string]any{" department ": "engineering"})
	fact, err := Canonicalize(physical, testTime("2026-09-12T00:00:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	if len(fact.Attributes) != 1 || fact.Attributes["department"] != "engineering" {
		t.Fatalf("unexpected attributes: %#v", fact.Attributes)
	}
}
