package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lib/pq"
	"github.com/upramod/deterministic-identity-reconciliation/pkg/reconcile"
)

var schemaNumber atomic.Uint64

// Every test gets its own schema. The database must be a disposable test DB.
func integrationStore(t *testing.T) (*Store, *sql.DB, string) {
	t.Helper()
	dsn := os.Getenv("IDENTITY_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set IDENTITY_TEST_DATABASE_URL to run real PostgreSQL tests")
	}
	u, err := url.Parse(dsn)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") {
		t.Fatal("test database must use a postgres URL")
	}
	admin, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	schema := fmt.Sprintf("identity_test_%d_%d", time.Now().UnixNano(), schemaNumber.Add(1))
	if _, err := admin.ExecContext(ctx, "CREATE SCHEMA "+pq.QuoteIdentifier(schema)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := admin.ExecContext(ctx, "DROP SCHEMA "+pq.QuoteIdentifier(schema)+" CASCADE"); err != nil {
			t.Error(err)
		}
	})
	query := u.Query()
	query.Set("search_path", schema)
	u.RawQuery = query.Encode()
	s, db := reopenStore(t, u.String())
	ddl, err := os.ReadFile(filepath.Join("..", "..", "..", "schema", "postgres.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, string(ddl)); err != nil {
		t.Fatal(err)
	}
	return s, db, u.String()
}

func reopenStore(t *testing.T, dsn string) (*Store, *sql.DB) {
	t.Helper()
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	s, err := New(db)
	if err != nil {
		t.Fatal(err)
	}
	return s, db
}

func testProjection(sequence int64) reconcile.IdentityProjection {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	return reconcile.IdentityProjection{
		SubjectID: "person-1", Exists: true, Enabled: true, LifecycleState: reconcile.StateActive,
		Attributes: map[string]string{"displayName": "Example"}, SourceFactKey: "fact-1", SourceEventKey: fmt.Sprintf("event-%d", sequence),
		EffectiveTime: now, Freshness: reconcile.FreshnessTuple{SourceSequence: sequence, ModificationTime: now, RevisionNumber: 1, PhysicalEventKey: fmt.Sprintf("event-%d", sequence)},
	}
}

func requireCAS(t *testing.T, s *Store, expected int64, p reconcile.IdentityProjection, want bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	got, err := s.CompareAndSetProjection(ctx, expected, p)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("CAS(expected=%d, sequence=%d) = %v, want %v", expected, p.Freshness.SourceSequence, got, want)
	}
}

func TestPostgresCASMissingExpectedVersion(t *testing.T) {
	s, _, _ := integrationStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	changed, err := s.CompareAndSetProjection(ctx, 7, testProjection(10))
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Error("missing-row CAS unexpectedly created a projection")
	}
	if _, err := s.LoadProjection(ctx, "person-1"); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("missing row was not preserved: %v", err)
	}
}

func TestPostgresProjectionRecoveryAndStaleWrites(t *testing.T) {
	s, db, dsn := integrationStore(t)
	requireCAS(t, s, 0, testProjection(1), true)
	terminated := testProjection(2)
	terminated.Exists, terminated.Enabled, terminated.LifecycleState = false, false, reconcile.StateTerminated
	requireCAS(t, s, 0, terminated, true)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	recovered, _ := reopenStore(t, dsn)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	got, err := recovered.LoadProjection(ctx, "person-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.RowVersion != 1 || got.Exists || got.Enabled || got.LifecycleState != reconcile.StateTerminated || got.Freshness.Compare(terminated.Freshness) != 0 || got.Attributes["displayName"] != "Example" {
		t.Fatalf("unexpected recovered projection: %#v", got)
	}
	requireCAS(t, recovered, 0, testProjection(3), false) // stale row version
	requireCAS(t, recovered, 1, testProjection(1), false) // stale source revision
	requireCAS(t, recovered, 1, terminated, false)        // replay after lost acknowledgement
	after, err := recovered.LoadProjection(ctx, "person-1")
	if err != nil {
		t.Fatal(err)
	}
	if after.RowVersion != 1 || after.Enabled || after.Exists {
		t.Fatalf("retry changed terminated state: %#v", after)
	}
	requireCAS(t, recovered, 1, testProjection(3), true) // valid newer update
	advanced, err := recovered.LoadProjection(ctx, "person-1")
	if err != nil {
		t.Fatal(err)
	}
	if advanced.RowVersion != 2 || !advanced.Enabled {
		t.Fatalf("new projection did not advance: %#v", advanced)
	}
}

func TestPostgresConcurrentProjectionWriters(t *testing.T) {
	s, _, dsn := integrationStore(t)
	requireCAS(t, s, 0, testProjection(1), true)
	requireCAS(t, s, 0, testProjection(2), true)
	other, _ := reopenStore(t, dsn)
	start := make(chan struct{})
	type result struct {
		changed  bool
		err      error
		sequence int64
	}
	results := make(chan result, 2)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for i, store := range []*Store{s, other} {
		go func(store *Store, seq int64) {
			<-start
			changed, err := store.CompareAndSetProjection(ctx, 1, testProjection(seq))
			results <- result{changed, err, seq}
		}(store, int64(i+3))
	}
	close(start)
	winners, winner := 0, int64(0)
	for i := 0; i < 2; i++ {
		r := <-results
		if r.err != nil {
			t.Fatal(r.err)
		}
		if r.changed {
			winners++
			winner = r.sequence
		}
	}
	if winners != 1 {
		t.Fatalf("successful writers = %d, want 1", winners)
	}
	got, err := s.LoadProjection(ctx, "person-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.RowVersion != 2 || got.Freshness.SourceSequence != winner {
		t.Fatalf("persisted state differs from winning writer: %#v", got)
	}
}

func TestPostgresPhysicalEventDeduplication(t *testing.T) {
	s, db, _ := integrationStore(t)
	p := testProjection(1)
	fact := reconcile.CanonicalFact{EventKey: "event-1", SubjectID: p.SubjectID, EventType: reconcile.EventHire, SourceScope: reconcile.SourceHireSnapshot, EffectiveTime: p.EffectiveTime, ModificationTime: p.EffectiveTime, ReceivedAt: p.EffectiveTime, Freshness: p.Freshness, Payload: map[string]any{"name": "original"}, PayloadHash: strings.Repeat("a", 64)}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if inserted, err := s.InsertPhysicalEvent(ctx, fact); err != nil || !inserted {
		t.Fatalf("initial insert = %v, %v", inserted, err)
	}
	replay := fact
	replay.ReceivedAt = fact.ReceivedAt.Add(time.Minute)
	if inserted, err := s.InsertPhysicalEvent(ctx, replay); err != nil || inserted {
		t.Fatalf("exact replay = %v, %v", inserted, err)
	}
	conflict := fact
	conflict.Payload = map[string]any{"name": "replacement"}
	conflict.PayloadHash = strings.Repeat("b", 64)
	if inserted, err := s.InsertPhysicalEvent(ctx, conflict); err == nil || inserted || !strings.Contains(err.Error(), "event key") {
		t.Fatalf("conflicting replay = %v, %v; want collision error", inserted, err)
	}
	var count int
	var name string
	if err := db.QueryRowContext(ctx, "SELECT count(*), min(payload->>'name') FROM physical_events").Scan(&count, &name); err != nil {
		t.Fatal(err)
	}
	if count != 1 || name != "original" {
		t.Fatalf("physical event changed: count=%d name=%q", count, name)
	}
}

func TestPostgresPositiveVersionFreshnessOrdering(t *testing.T) {
	for _, field := range []string{"sequence", "modification", "revision", "event_key"} {
		for _, newer := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/newer=%v", field, newer), func(t *testing.T) {
				s, _, _ := integrationStore(t)
				requireCAS(t, s, 0, testProjection(1), true)
				current := testProjection(2)
				requireCAS(t, s, 0, current, true)
				candidate := current
				delta := int64(-1)
				if newer {
					delta = 1
				}
				switch field {
				case "sequence":
					candidate.Freshness.SourceSequence += delta
				case "modification":
					candidate.Freshness.ModificationTime = candidate.Freshness.ModificationTime.Add(time.Duration(delta) * time.Second)
				case "revision":
					candidate.Freshness.RevisionNumber += delta
				case "event_key":
					candidate.Freshness.PhysicalEventKey = fmt.Sprintf("event-%d", 2+delta)
				}
				want := candidate.Freshness.Compare(current.Freshness) > 0
				requireCAS(t, s, 1, candidate, want)
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				got, err := s.LoadProjection(ctx, current.SubjectID)
				if err != nil {
					t.Fatal(err)
				}
				expected, version := current, int64(1)
				if want {
					expected, version = candidate, 2
				}
				if got.RowVersion != version || got.Freshness.Compare(expected.Freshness) != 0 {
					t.Fatalf("unexpected persisted freshness: %#v", got)
				}
			})
		}
	}
}

func TestPostgresLockedProjectionBlocksWritersAndReleasesOnFailure(t *testing.T) {
	s, _, dsn := integrationStore(t)
	requireCAS(t, s, 0, testProjection(1), true)
	requireCAS(t, s, 0, testProjection(2), true)
	other, _ := reopenStore(t, dsn)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	callbackFailure := errors.New("simulated target failure")
	err := s.WithLockedProjection(ctx, "person-1", func(p reconcile.IdentityProjection) error {
		if p.RowVersion != 1 {
			t.Errorf("locked version=%d, want 1", p.RowVersion)
		}
		blocked, stop := context.WithTimeout(ctx, 150*time.Millisecond)
		defer stop()
		changed, err := other.CompareAndSetProjection(blocked, 1, testProjection(3))
		if changed || err == nil || !errors.Is(blocked.Err(), context.DeadlineExceeded) {
			t.Errorf("writer was not blocked: changed=%v err=%v context=%v", changed, err, blocked.Err())
		}
		return callbackFailure
	})
	if !errors.Is(err, callbackFailure) {
		t.Fatalf("callback failure was lost: %v", err)
	}
	requireCAS(t, other, 1, testProjection(3), true)
	called := false
	err = s.WithLockedProjection(ctx, "missing-person", func(reconcile.IdentityProjection) error { called = true; return nil })
	if !errors.Is(err, sql.ErrNoRows) || called {
		t.Fatalf("missing row callback: called=%v err=%v", called, err)
	}
}
