package scim

import (
	"context"
	"encoding/json"
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
		_, _ = writer.Write([]byte(`{"Resources":[{"id":"scim-1","externalId":"person-001","active":true,"email":"person@example.test"}]}`))
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
			_, _ = writer.Write([]byte(`{"Resources":[{"id":"scim-1","externalId":"person-001","active":true,"email":"person@example.test"}]}`))
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
}

func TestPatchBodyUsesSCIMOperationShape(t *testing.T) {
	var received map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodGet {
			_, _ = writer.Write([]byte(`{"Resources":[{"id":"scim-1","externalId":"person-001","active":true}]}`))
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
