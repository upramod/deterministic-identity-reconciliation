package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lib/pq"
	"github.com/upramod/deterministic-identity-reconciliation/pkg/adapter/scim"
	"github.com/upramod/deterministic-identity-reconciliation/pkg/reconcile"
	"github.com/upramod/deterministic-identity-reconciliation/pkg/store/postgres"
)

func TestPersistedConvergenceEndToEnd(t *testing.T) {
	dsn, target := os.Getenv("IDENTITY_TEST_DATABASE_URL"), os.Getenv("IDENTITY_SCIM_TEST_URL")
	if dsn == "" || target == "" {
		t.Skip("requires real PostgreSQL and the independent SCIM server")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := fmt.Sprintf("identity_e2e_%d", time.Now().UnixNano())
	if _, err := admin.ExecContext(ctx, "CREATE SCHEMA "+pq.QuoteIdentifier(schema)); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		if _, err := admin.ExecContext(cleanup, "DROP SCHEMA "+pq.QuoteIdentifier(schema)+" CASCADE"); err != nil {
			t.Error(err)
		}
	}()
	query := u.Query()
	query.Set("search_path", schema)
	u.RawQuery = query.Encode()
	db, err := sql.Open("postgres", u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ddl, err := os.ReadFile("../../schema/postgres.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, string(ddl)); err != nil {
		t.Fatal(err)
	}
	store, err := postgres.New(db)
	if err != nil {
		t.Fatal(err)
	}
	subject := schema
	adapter, err := scim.New(scim.Config{BaseURL: target, Token: os.Getenv("IDENTITY_SCIM_TEST_TOKEN"), ManagedAttributes: []string{"displayName"}, DeprovisionMode: scim.DeprovisionDelete})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		if err := adapter.Apply(cleanup, reconcile.DesiredState{SubjectID: subject}); err != nil {
			t.Error(err)
		}
	}()
	binary := filepath.Join(t.TempDir(), "converge")
	build := exec.CommandContext(ctx, "go", "build", "-o", binary, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v: %s", err, output)
	}
	command := func(extra ...string) (report, string, error) {
		args := append([]string{"-subject", subject, "-managed-attributes", "displayName"}, extra...)
		cmd := exec.CommandContext(ctx, binary, args...)
		cmd.Env = append(os.Environ(), "IDENTITY_DATABASE_URL="+u.String(), "IDENTITY_SCIM_URL="+target, "IDENTITY_SCIM_TOKEN="+os.Getenv("IDENTITY_SCIM_TEST_TOKEN"))
		output, err := cmd.CombinedOutput()
		var result report
		if err == nil {
			err = json.Unmarshal(output, &result)
		}
		return result, string(output), err
	}
	check := func(label, action, mode string, version int64, extra ...string) {
		t.Helper()
		result, output, err := command(extra...)
		if err != nil {
			t.Fatalf("%s: %v %s", label, err, output)
		}
		if result.Action != action || result.Mode != mode || result.RowVersion != version || result.SubjectID != subject {
			t.Fatalf("%s: unexpected report %#v", label, result)
		}
		t.Logf("%s: %s", label, strings.TrimSpace(output))
	}
	persist := func(expected int64, projection reconcile.IdentityProjection) {
		t.Helper()
		if changed, err := store.CompareAndSetProjection(ctx, expected, projection); err != nil || !changed {
			t.Fatalf("persist: changed=%v err=%v", changed, err)
		}
	}
	assertTarget := func(exists, enabled bool) {
		t.Helper()
		observed, err := adapter.Observe(ctx, subject)
		if err != nil {
			t.Fatal(err)
		}
		if observed.Exists != exists || observed.Enabled != enabled {
			t.Fatalf("target exists=%v enabled=%v, want %v/%v", observed.Exists, observed.Enabled, exists, enabled)
		}
	}
	now := time.Now().UTC().Truncate(time.Second)
	projection := reconcile.IdentityProjection{SubjectID: subject, Exists: true, Enabled: true, LifecycleState: reconcile.StateActive, Attributes: map[string]string{"displayName": "End to end identity"}, EffectiveTime: now, SourceFactKey: "synthetic-fact", SourceEventKey: "synthetic-1", Freshness: reconcile.FreshnessTuple{SourceSequence: 1, ModificationTime: now, PhysicalEventKey: "synthetic-1"}}
	persist(0, projection)
	check("preview creation", "would_update", "preview", 0)
	assertTarget(false, false)
	check("apply saved creation", "updated", "apply", 0, "-apply")
	assertTarget(true, true)
	check("fresh-process replay", "unchanged", "apply", 0, "-apply")
	projection.Exists, projection.Enabled, projection.LifecycleState = false, false, reconcile.StateTerminated
	projection.Freshness.SourceSequence = 2
	persist(0, projection) // Stop after commit, before any target write.
	assertTarget(true, true)
	check("preview pending termination", "would_update", "preview", 1)
	assertTarget(true, true)
	check("recover committed termination", "updated", "apply", 1, "-apply")
	assertTarget(true, false)
	check("replay recovered termination", "unchanged", "apply", 1, "-apply")
	projection.Exists, projection.Enabled, projection.LifecycleState = true, true, reconcile.StateActive
	projection.Freshness.SourceSequence = 3
	persist(1, projection)
	check("apply persisted rehire", "updated", "apply", 2, "-apply")
	assertTarget(true, true)
	if _, output, err := command("-apply", "-managed-attributes", ""); err == nil || !strings.Contains(output, "not in -managed-attributes") {
		t.Fatalf("unowned attributes accepted: %v %s", err, output)
	}
	assertTarget(true, true)
	if _, output, err := command("-apply", "-subject", subject+"-absent"); err == nil || !strings.Contains(output, "no persisted projection") {
		t.Fatalf("missing projection accepted: %v %s", err, output)
	}

	customSubject := subject + "-employee"
	customProjection := reconcile.IdentityProjection{
		SubjectID:      customSubject,
		Exists:         true,
		Enabled:        true,
		LifecycleState: reconcile.StateActive,
		Attributes:     map[string]string{"displayName": "Custom-bound identity"},
		EffectiveTime:  now,
		SourceFactKey:  "synthetic-custom-fact",
		SourceEventKey: "synthetic-custom-1",
		Freshness:      reconcile.FreshnessTuple{SourceSequence: 1, ModificationTime: now, PhysicalEventKey: "synthetic-custom-1"},
	}
	persist(0, customProjection)
	var customGets, customPosts atomic.Int32
	customTarget := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.Method {
		case http.MethodGet:
			customGets.Add(1)
			if got, want := request.URL.Query().Get("filter"), fmt.Sprintf(`employeeNumber eq "%s"`, customSubject); got != want {
				t.Errorf("custom subject filter = %q, want %q", got, want)
				_, _ = writer.Write([]byte(`{"totalResults":0,"Resources":[]}`))
				return
			}
			writer.Header().Set("Content-Type", "application/scim+json")
			_, _ = fmt.Fprintf(writer, `{"totalResults":1,"Resources":[{"id":"existing-custom","employeeNumber":%q,"active":true,"displayName":"Custom-bound identity"}]}`, customSubject)
		case http.MethodPost:
			customPosts.Add(1)
			writer.WriteHeader(http.StatusCreated)
		default:
			t.Errorf("unexpected custom target request: %s %s", request.Method, request.URL)
			writer.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	defer customTarget.Close()
	custom := exec.CommandContext(ctx, binary,
		"-subject", customSubject,
		"-subject-attribute", "employeeNumber",
		"-managed-attributes", "displayName",
		"-apply",
	)
	custom.Env = append(os.Environ(),
		"IDENTITY_DATABASE_URL="+u.String(),
		"IDENTITY_SCIM_URL="+customTarget.URL,
	)
	customOutput, err := custom.CombinedOutput()
	if err != nil {
		t.Fatalf("custom subject binding: %v %s", err, customOutput)
	}
	var customResult report
	if err := json.Unmarshal(customOutput, &customResult); err != nil {
		t.Fatalf("decode custom subject report: %v: %s", err, customOutput)
	}
	if customResult.Action != "unchanged" || customResult.Mode != "apply" || customResult.SubjectID != customSubject {
		t.Fatalf("custom subject binding: unexpected report %#v", customResult)
	}
	if got := customGets.Load(); got != 1 {
		t.Fatalf("custom subject lookup requests = %d, want 1", got)
	}
	if got := customPosts.Load(); got != 0 {
		t.Fatalf("custom subject create requests = %d, want 0", got)
	}
}
