package scim

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/upramod/deterministic-identity-reconciliation/pkg/reconcile"
)

func TestObserveAndReadBeforeWrite(t *testing.T) {
	var mu sync.Mutex
	patches := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			t.Fatalf("unexpected method: %s", request.Method)
		}
		if !strings.Contains(request.URL.Query().Get("filter"), "externalId") {
			t.Fatalf("missing SCIM filter: %s", request.URL.Query().Get("filter"))
		}
		_, _ = writer.Write([]byte(`{"totalResults":1,"Resources":[{"id":"scim-1","externalId":"person-001","active":true,"email":"person@example.test"}]}`))
	}))
	defer server.Close()

	adapter, err := New(Config{
		BaseURL:           server.URL,
		ManagedAttributes: []string{"email"},
	})
	if err != nil {
		t.Fatal(err)
	}
	observed, err := adapter.Observe(context.Background(), "person-001")
	if err != nil {
		t.Fatal(err)
	}
	if !observed.Exists || !observed.Enabled || observed.Attributes["email"] != "person@example.test" {
		t.Fatalf("unexpected observed state: %#v", observed)
	}

	mu.Lock()
	gotPatches := patches
	mu.Unlock()
	if gotPatches != 0 {
		t.Fatalf("observe issued a mutation")
	}
}

func TestConvergePatchesOnlyWhenStateDiffers(t *testing.T) {
	var mu sync.Mutex
	patches := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.Method {
		case http.MethodGet:
			_, _ = writer.Write([]byte(`{"totalResults":1,"Resources":[{"id":"scim-1","externalId":"person-001","active":true,"email":"person@example.test"}]}`))
		case http.MethodPatch:
			mu.Lock()
			patches++
			mu.Unlock()
			writer.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected method: %s", request.Method)
		}
	}))
	defer server.Close()

	adapter, err := New(Config{
		BaseURL:           server.URL,
		ManagedAttributes: []string{"email"},
	})
	if err != nil {
		t.Fatal(err)
	}
	desired := reconcile.DesiredState{
		SubjectID:  "person-001",
		Exists:     true,
		Enabled:    true,
		Attributes: map[string]string{"email": "person@example.test"},
	}
	result, err := reconcile.Converge(context.Background(), adapter, desired)
	if err != nil {
		t.Fatal(err)
	}
	if result.Action != reconcile.ConvergenceUnchanged {
		t.Fatalf("equivalent target was changed: %#v", result)
	}

	desired.Enabled = false
	result, err = reconcile.Converge(context.Background(), adapter, desired)
	if err != nil {
		t.Fatal(err)
	}
	if result.Action != reconcile.ConvergenceUpdated {
		t.Fatalf("different target was not changed: %#v", result)
	}
	mu.Lock()
	gotPatches := patches
	mu.Unlock()
	if gotPatches != 1 {
		t.Fatalf("patch count = %d, want 1", gotPatches)
	}
}

func TestNewRejectsUnsafeConfiguration(t *testing.T) {
	if _, err := New(Config{BaseURL: "file:///tmp"}); err == nil {
		t.Fatalf("expected non-HTTP URL to be rejected")
	}
	if _, err := New(Config{BaseURL: "https://example.test", SubjectAttribute: "external id"}); err == nil {
		t.Fatalf("expected unsafe attribute to be rejected")
	}
	if _, err := New(Config{
		BaseURL:           "https://example.test",
		SubjectAttribute:  "employeeNumber",
		ManagedAttributes: []string{"displayName", "EMPLOYEENUMBER"},
	}); err == nil {
		t.Fatalf("expected mutable subject binding to be rejected")
	}
	for _, attribute := range []string{"schemas", "Active", "id", "meta"} {
		if _, err := New(Config{
			BaseURL:          "https://example.test",
			SubjectAttribute: attribute,
		}); err == nil {
			t.Errorf("expected unsafe subject attribute %q to be rejected", attribute)
		}
	}
	for _, attribute := range []string{"externalId", "userName", "employeeNumber"} {
		if _, err := New(Config{
			BaseURL:          "https://example.test",
			SubjectAttribute: attribute,
		}); err != nil {
			t.Errorf("expected supported subject attribute %q: %v", attribute, err)
		}
	}
	for _, attribute := range []string{"schemas", "USERNAME", "externalId", "Active", "id", "meta"} {
		if _, err := New(Config{
			BaseURL:           "https://example.test",
			ManagedAttributes: []string{attribute},
		}); err == nil {
			t.Errorf("expected adapter-owned attribute %q to be rejected", attribute)
		}
	}
}

func TestResponseAttributeNamesAreCaseInsensitive(t *testing.T) {
	gets, patches := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			gets++
			_, _ = w.Write([]byte(`{"totalResults":1,"Resources":[{"ID":"scim-1","EXTERNALID":"person-001","ACTIVE":true,"Email":"person@example.test","META":{"Version":"W/\"v1\""}}]}`))
		case http.MethodPatch:
			patches++
			if version := r.Header.Get("If-Match"); version != `W/"v1"` {
				t.Errorf("If-Match = %q, want resource version", version)
				w.WriteHeader(http.StatusPreconditionRequired)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected method %s", r.Method)
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	defer server.Close()

	adapter, err := New(Config{BaseURL: server.URL, ManagedAttributes: []string{"email"}})
	if err != nil {
		t.Fatal(err)
	}
	desired := reconcile.DesiredState{
		SubjectID:  "person-001",
		Exists:     true,
		Enabled:    false,
		Attributes: map[string]string{"email": "person@example.test"},
	}
	result, err := reconcile.Converge(context.Background(), adapter, desired)
	if err != nil {
		t.Fatal(err)
	}
	if result.Action != reconcile.ConvergenceUpdated || gets != 2 || patches != 1 {
		t.Fatalf("result=%#v gets=%d patches=%d", result, gets, patches)
	}
	if !result.Observed.Exists || !result.Observed.Enabled || result.Observed.Attributes["email"] != "person@example.test" {
		t.Fatalf("unexpected observation: %#v", result.Observed)
	}
}

func TestApplyRejectsCaseAmbiguousResponseAttributes(t *testing.T) {
	mutations := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{"totalResults":1,"Resources":[{"id":"scim-1","ID":"other","active":true}]}`))
			return
		}
		mutations++
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	adapter, err := New(Config{BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	err = adapter.Apply(context.Background(), reconcile.DesiredState{SubjectID: "person-001", Exists: false})
	if err == nil || mutations != 0 {
		t.Fatalf("ambiguous response: error=%v mutations=%d; want rejection without mutation", err, mutations)
	}
}

func TestApplyRejectsMalformedVersionMetadata(t *testing.T) {
	for _, resource := range []string{
		`{"id":"scim-1","active":true,"meta":"W/\"v1\""}`,
		`{"id":"scim-1","active":true,"meta":{"version":7}}`,
	} {
		t.Run(resource, func(t *testing.T) {
			mutations := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					_, _ = fmt.Fprintf(w, `{"totalResults":1,"Resources":[%s]}`, resource)
					return
				}
				mutations++
				w.WriteHeader(http.StatusNoContent)
			}))
			defer server.Close()

			adapter, err := New(Config{BaseURL: server.URL, DeprovisionMode: DeprovisionDelete})
			if err != nil {
				t.Fatal(err)
			}
			err = adapter.Apply(context.Background(), reconcile.DesiredState{SubjectID: "person-001", Exists: false})
			if err == nil || mutations != 0 {
				t.Fatalf("malformed version metadata: error=%v mutations=%d; want rejection without mutation", err, mutations)
			}
		})
	}
}

func TestApplyEncodesProviderResourceIDAsOnePathSegment(t *testing.T) {
	const resourceID = "tenant/user?legacy#record"
	writes := 0
	requestURI := ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			_, _ = fmt.Fprintf(w, `{"totalResults":1,"Resources":[{"id":%q,"active":true}]}`, resourceID)
		case http.MethodDelete:
			writes++
			requestURI = r.RequestURI
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected method %s", r.Method)
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	defer server.Close()

	adapter, err := New(Config{BaseURL: server.URL, DeprovisionMode: DeprovisionDelete})
	if err != nil {
		t.Fatal(err)
	}
	err = adapter.Apply(context.Background(), reconcile.DesiredState{SubjectID: "person-001", Exists: false})
	if err != nil {
		t.Fatal(err)
	}
	if writes != 1 {
		t.Fatalf("writes = %d, want 1", writes)
	}
	if requestURI != "/Users/tenant%2Fuser%3Flegacy%23record" {
		t.Fatalf("request URI = %q, want one encoded resource-id segment", requestURI)
	}
}

// The provider changes the account after lookup, before the write. No sleeps
// are needed: advancing its version before replying makes the race deterministic.
func TestApplyRejectsConcurrentTargetChange(t *testing.T) {
	for _, mode := range []DeprovisionMode{DeprovisionDisable, DeprovisionDelete} {
		t.Run(string(mode), func(t *testing.T) {
			mutations, attempts := 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					_, _ = w.Write([]byte(`{"totalResults":1,"Resources":[{"id":"scim-1","active":true,"meta":{"version":"W/\"v1\""}}]}`))
					return
				}
				attempts++
				// The target now has v2. A guarded v1 write must fail.
				if r.Header.Get("If-Match") == `W/"v1"` {
					w.WriteHeader(http.StatusPreconditionFailed)
					return
				}
				mutations++
				w.WriteHeader(http.StatusNoContent)
			}))
			defer server.Close()
			adapter, err := New(Config{BaseURL: server.URL, DeprovisionMode: mode})
			if err != nil {
				t.Fatal(err)
			}
			err = adapter.Apply(context.Background(), reconcile.DesiredState{SubjectID: "person-001", Exists: false})
			if err == nil || mutations != 0 || attempts != 1 {
				t.Fatalf("concurrent change: error=%v, mutations=%d, attempts=%d; want rejection without overwrite or retry", err, mutations, attempts)
			}
		})
	}
}

func TestApplyUsesVersionForSuccessfulUpdate(t *testing.T) {
	mutations := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{"totalResults":1,"Resources":[{"id":"scim-1","active":false,"meta":{"version":"W/\"v1\""}}]}`))
			return
		}
		if r.Method != http.MethodPatch || r.Header.Get("If-Match") != `W/"v1"` {
			t.Errorf("unexpected write: method=%s, If-Match=%q", r.Method, r.Header.Get("If-Match"))
			w.WriteHeader(http.StatusPreconditionFailed)
			return
		}
		mutations++
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	adapter, err := New(Config{BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	err = adapter.Apply(context.Background(), reconcile.DesiredState{SubjectID: "person-001", Exists: true, Enabled: true})
	if err != nil || mutations != 1 {
		t.Fatalf("unchanged version: error=%v, mutations=%d", err, mutations)
	}
}

func TestPatchBodyUsesSCIMOperationShape(t *testing.T) {
	var received map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodGet {
			_, _ = writer.Write([]byte(`{"totalResults":1,"Resources":[{"id":"scim-1","externalId":"person-001","active":true}]}`))
			return
		}
		if request.Method != http.MethodPatch {
			t.Fatalf("unexpected method: %s", request.Method)
		}
		if err := json.NewDecoder(request.Body).Decode(&received); err != nil {
			t.Fatal(err)
		}
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	adapter, err := New(Config{BaseURL: server.URL, ManagedAttributes: []string{"email"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.Apply(context.Background(), reconcile.DesiredState{
		SubjectID:  "person-001",
		Exists:     true,
		Enabled:    false,
		Attributes: map[string]string{"email": "new@example.test"},
	}); err != nil {
		t.Fatal(err)
	}
	if received["schemas"].([]any)[0] != patchOperationSchema {
		t.Fatalf("wrong patch schema: %#v", received["schemas"])
	}
	operations := received["Operations"].([]any)
	if len(operations) != 2 {
		t.Fatalf("operation count = %d, want 2", len(operations))
	}
}

func TestCreatePersistsCustomSubjectAttributeForReplay(t *testing.T) {
	var mu sync.Mutex
	created := map[string]any(nil)
	posts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.Method {
		case http.MethodGet:
			if filter := r.URL.Query().Get("filter"); filter != `employeeNumber eq "person-001"` {
				t.Errorf("unexpected filter %q", filter)
			}
			if created == nil || created["employeeNumber"] != "person-001" {
				_, _ = w.Write([]byte("{\"totalResults\":0,\"Resources\":[]}"))
				return
			}
			_, _ = w.Write([]byte("{\"totalResults\":1,\"Resources\":[{\"id\":\"scim-1\",\"employeeNumber\":\"person-001\",\"active\":true}]}"))
		case http.MethodPost:
			posts++
			if err := json.NewDecoder(r.Body).Decode(&created); err != nil {
				t.Error(err)
			}
			w.WriteHeader(http.StatusCreated)
		default:
			t.Errorf("unexpected method %s", r.Method)
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	defer server.Close()

	adapter, err := New(Config{BaseURL: server.URL, SubjectAttribute: "employeeNumber"})
	if err != nil {
		t.Fatal(err)
	}
	desired := reconcile.DesiredState{SubjectID: "person-001", Exists: true, Enabled: true}
	for attempt, want := range []reconcile.ConvergenceAction{reconcile.ConvergenceUpdated, reconcile.ConvergenceUnchanged} {
		result, err := reconcile.Converge(context.Background(), adapter, desired)
		if err != nil {
			t.Fatalf("attempt %d: %v", attempt+1, err)
		}
		if result.Action != want {
			t.Fatalf("attempt %d action = %s, want %s", attempt+1, result.Action, want)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if posts != 1 {
		t.Fatalf("create requests = %d, want 1", posts)
	}
	if created["employeeNumber"] != "person-001" {
		t.Fatalf("custom subject attribute was not persisted: %#v", created)
	}
}

func TestApplyRejectsIncompleteOrAmbiguousLookup(t *testing.T) {
	for _, response := range []struct {
		name string
		body string
	}{
		{"ambiguous truncated page", `{"totalResults":2,"startIndex":1,"itemsPerPage":1,"Resources":[{"id":"first-match","active":true}]}`},
		{"missing resource", `{"totalResults":1,"Resources":[]}`},
		{"inconsistent empty total", `{"totalResults":0,"Resources":[{"id":"first-match"}]}`},
		{"empty object", `{}`},
		{"missing total", `{"Resources":[]}`},
		{"null total", `{"totalResults":null,"Resources":[]}`},
		{"negative total", `{"totalResults":-1,"Resources":[]}`},
	} {
		for _, mode := range []DeprovisionMode{DeprovisionDisable, DeprovisionDelete} {
			for _, exists := range []bool{true, false} {
				t.Run(fmt.Sprintf("%s/%s/exists=%t", response.name, mode, exists), func(t *testing.T) {
					mutations := 0
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						if r.Method == http.MethodGet {
							_, _ = w.Write([]byte(response.body))
							return
						}
						mutations++
						w.WriteHeader(http.StatusNoContent)
					}))
					defer server.Close()
					adapter, err := New(Config{BaseURL: server.URL, DeprovisionMode: mode})
					if err != nil {
						t.Fatal(err)
					}
					err = adapter.Apply(context.Background(), reconcile.DesiredState{SubjectID: "person-001", Exists: exists})
					if err == nil {
						t.Error("unsafe lookup was accepted")
					}
					if mutations != 0 {
						t.Errorf("unsafe lookup issued %d mutation(s)", mutations)
					}
				})
			}
		}
	}
}

func TestApplyCreatesOnlyAfterConfirmedAbsence(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusNoContent} {
		t.Run(fmt.Sprintf("GET status %d", status), func(t *testing.T) {
			creates := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					w.WriteHeader(status)
					if status == http.StatusOK {
						_, _ = w.Write([]byte(`{"totalResults":0,"Resources":[]}`))
					}
					return
				}
				if r.Method == http.MethodPost {
					creates++
				}
				w.WriteHeader(http.StatusCreated)
			}))
			defer server.Close()
			adapter, err := New(Config{BaseURL: server.URL})
			if err != nil {
				t.Fatal(err)
			}
			err = adapter.Apply(context.Background(), reconcile.DesiredState{SubjectID: "person-001", Exists: true})
			if status == http.StatusOK {
				if err != nil || creates != 1 {
					t.Fatalf("confirmed absence: error=%v, creates=%d", err, creates)
				}
			} else if err == nil || creates != 0 {
				t.Fatalf("unconfirmed absence: error=%v, creates=%d", err, creates)
			}
		})
	}
}
