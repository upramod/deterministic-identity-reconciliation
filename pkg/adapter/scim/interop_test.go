package scim

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/upramod/deterministic-identity-reconciliation/pkg/reconcile"
)

type interopTransport func(*http.Request) (*http.Response, error)

func (f interopTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func interopAdapter(t *testing.T, client *http.Client, mode DeprovisionMode) *Adapter {
	t.Helper()
	base := os.Getenv("IDENTITY_SCIM_TEST_URL")
	if base == "" {
		t.Skip("run scripts/test-scim-interop.py with the pinned independent server")
	}
	a, err := New(Config{BaseURL: base, Token: os.Getenv("IDENTITY_SCIM_TEST_TOKEN"), ManagedAttributes: []string{"displayName"}, HTTPClient: client, DeprovisionMode: mode})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func interopSubject(t *testing.T) string {
	return fmt.Sprintf("identity-interop-%s-%d", t.Name(), time.Now().UnixNano())
}

func interopCleanup(t *testing.T, subject string) {
	t.Helper()
	a := interopAdapter(t, nil, DeprovisionDelete)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := a.Apply(ctx, reconcile.DesiredState{SubjectID: subject}); err != nil {
			t.Errorf("cleanup: %v", err)
		}
	})
}

func TestSCIMInteropLifecycle(t *testing.T) {
	var writes atomic.Int64
	client := &http.Client{Timeout: 10 * time.Second, Transport: interopTransport(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodGet {
			writes.Add(1)
		}
		return http.DefaultTransport.RoundTrip(r)
	})}
	a := interopAdapter(t, client, DeprovisionDisable)
	subject := interopSubject(t)
	interopCleanup(t, subject)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	desired := reconcile.DesiredState{SubjectID: subject, Exists: true, Enabled: true, Attributes: map[string]string{"displayName": "Initial name"}}
	check := func(label string, adapter *Adapter, wantAction reconcile.ConvergenceAction, wantWrites int64, exists, enabled bool, name string) {
		t.Helper()
		result, err := reconcile.Converge(ctx, adapter, desired)
		if err != nil {
			t.Fatalf("%s: %v", label, err)
		}
		if result.Action != wantAction || writes.Load() != wantWrites {
			t.Fatalf("%s: action=%s writes=%d, want %s/%d", label, result.Action, writes.Load(), wantAction, wantWrites)
		}
		observed, err := adapter.Observe(ctx, subject)
		if err != nil {
			t.Fatal(err)
		}
		if observed.Exists != exists || observed.Enabled != enabled || observed.Attributes["displayName"] != name {
			t.Fatalf("%s: unexpected state %#v", label, observed)
		}
		t.Logf("%s: action=%s cumulative_writes=%d", label, result.Action, writes.Load())
	}
	check("create", a, reconcile.ConvergenceUpdated, 1, true, true, "Initial name")
	check("replay create", a, reconcile.ConvergenceUnchanged, 1, true, true, "Initial name")
	desired.Attributes["displayName"] = "Updated name"
	check("update", a, reconcile.ConvergenceUpdated, 2, true, true, "Updated name")
	desired.Exists, desired.Enabled = false, false
	check("disable", a, reconcile.ConvergenceUpdated, 3, true, false, "Updated name")
	check("replay disable with fresh adapter", interopAdapter(t, client, DeprovisionDisable), reconcile.ConvergenceUnchanged, 3, true, false, "Updated name")
	desired.Exists, desired.Enabled = true, true
	check("rehire", a, reconcile.ConvergenceUpdated, 4, true, true, "Updated name")
	desired.Exists, desired.Enabled = false, false
	deleting := interopAdapter(t, client, DeprovisionDelete)
	check("delete", deleting, reconcile.ConvergenceUpdated, 5, false, false, "")
	check("replay delete", deleting, reconcile.ConvergenceUnchanged, 5, false, false, "")
}

func TestSCIMInteropLostDisableResponse(t *testing.T) {
	a := interopAdapter(t, nil, DeprovisionDisable)
	subject := interopSubject(t)
	interopCleanup(t, subject)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := a.Apply(ctx, reconcile.DesiredState{SubjectID: subject, Exists: true, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	var writes atomic.Int64
	client := &http.Client{Timeout: 10 * time.Second, Transport: interopTransport(func(r *http.Request) (*http.Response, error) {
		response, err := http.DefaultTransport.RoundTrip(r)
		if r.Method == http.MethodPatch {
			writes.Add(1)
			if err == nil && response.StatusCode >= 200 && response.StatusCode < 300 {
				_ = response.Body.Close()
				return nil, io.ErrUnexpectedEOF
			}
		}
		return response, err
	})}
	desired := reconcile.DesiredState{SubjectID: subject}
	if _, err := reconcile.Converge(ctx, interopAdapter(t, client, DeprovisionDisable), desired); err == nil {
		t.Fatal("lost response did not surface as an error")
	}
	for attempt := 0; attempt < 2; attempt++ {
		result, err := reconcile.Converge(ctx, interopAdapter(t, client, DeprovisionDisable), desired)
		if err != nil {
			t.Fatal(err)
		}
		if result.Action != reconcile.ConvergenceUnchanged || !result.Observed.Exists || result.Observed.Enabled {
			t.Fatalf("incorrect recovered state: %#v", result)
		}
	}
	if writes.Load() != 1 {
		t.Fatalf("disable writes=%d, want 1", writes.Load())
	}
}

func TestSCIMInteropConcurrentTargetChange(t *testing.T) {
	a := interopAdapter(t, nil, DeprovisionDisable)
	subject := interopSubject(t)
	interopCleanup(t, subject)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	desired := reconcile.DesiredState{SubjectID: subject, Exists: true, Enabled: true, Attributes: map[string]string{"displayName": "Initial name"}}
	if err := a.Apply(ctx, desired); err != nil {
		t.Fatal(err)
	}
	var writes, status atomic.Int64
	client := &http.Client{Timeout: 10 * time.Second, Transport: interopTransport(func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodPatch && writes.Add(1) == 1 {
			if r.Header.Get("If-Match") == "" {
				return nil, fmt.Errorf("adapter omitted If-Match")
			}
			body := `{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],"Operations":[{"op":"replace","path":"displayName","value":"Other writer"}]}`
			other, err := http.NewRequestWithContext(ctx, http.MethodPatch, r.URL.String(), strings.NewReader(body))
			if err != nil {
				return nil, err
			}
			other.Header = r.Header.Clone()
			resp, err := http.DefaultTransport.RoundTrip(other)
			if err != nil {
				return nil, err
			}
			_ = resp.Body.Close()
			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				return nil, fmt.Errorf("competing write returned %d", resp.StatusCode)
			}
		}
		resp, err := http.DefaultTransport.RoundTrip(r)
		if r.Method == http.MethodPatch && err == nil {
			status.Store(int64(resp.StatusCode))
		}
		return resp, err
	})}
	desired.Attributes["displayName"] = "Stale writer"
	_, err := reconcile.Converge(ctx, interopAdapter(t, client, DeprovisionDisable), desired)
	if err == nil || status.Load() != http.StatusPreconditionFailed || writes.Load() != 1 {
		t.Fatalf("expected one rejected conditional write: status=%d writes=%d err=%v", status.Load(), writes.Load(), err)
	}
	observed, err := a.Observe(ctx, subject)
	if err != nil {
		t.Fatal(err)
	}
	if observed.Attributes["displayName"] != "Other writer" {
		t.Fatalf("concurrent state was overwritten: %#v", observed)
	}
}
