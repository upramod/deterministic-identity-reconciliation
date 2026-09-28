package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/upramod/deterministic-identity-reconciliation/pkg/reconcile"
)

func writeInput(t *testing.T, input string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "events.json")
	if err := os.WriteFile(path, []byte(input), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestReadEventsPreservesLargeNumericIdentifiers(t *testing.T) {
	previousHash := ""
	for _, identifier := range []string{"9007199254740992", "9007199254740993"} {
		input := fmt.Sprintf(`[{"event_key":"event-1","subject_id":"person-001","event_type":"hire","source_scope":"hire_snapshot","effective_time":"2026-09-01T00:00:00Z","modification_time":"2026-09-01T00:00:00Z","payload":{"attributes":{"employeeId":%s}}}]`, identifier)
		events, err := readEvents(writeInput(t, input))
		if err != nil {
			t.Fatal(err)
		}
		fact, err := reconcile.Canonicalize(events[0], time.Time{})
		if err != nil {
			t.Fatal(err)
		}
		if got := fact.Attributes["employeeId"]; got != identifier {
			t.Errorf("employeeId = %q, want %q", got, identifier)
		}
		if fact.PayloadHash == previousHash {
			t.Error("adjacent large identifiers have the same payload hash")
		}
		previousHash = fact.PayloadHash
		result, err := reconcile.ResolveBatch(events, reconcile.DefaultPolicy(time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)))
		if err != nil {
			t.Fatal(err)
		}
		if got := result.Projections["person-001"].Attributes["employeeId"]; got != identifier {
			t.Errorf("projected employeeId = %q, want %q", got, identifier)
		}
	}
}

func TestReadEventsRejectsTrailingJSON(t *testing.T) {
	for _, input := range []string{"[] []", "[] null", "[] garbage", "", "["} {
		t.Run(input, func(t *testing.T) {
			if _, err := readEvents(writeInput(t, input)); err == nil {
				t.Fatal("invalid or extra input was accepted")
			}
		})
	}
	if _, err := readEvents(writeInput(t, "[] \n\t")); err != nil {
		t.Fatalf("trailing whitespace rejected: %v", err)
	}
}
