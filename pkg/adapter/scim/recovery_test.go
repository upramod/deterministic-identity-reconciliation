package scim

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/upramod/deterministic-identity-reconciliation/pkg/reconcile"
)

// The server commits the mutation but drops its response. Recovery constructs a
// new adapter so no in-memory retry state can hide a repeated write.
func TestConvergeRecoversAfterLostDeprovisionResponse(t *testing.T) {
	for _, mode := range []DeprovisionMode{DeprovisionDisable, DeprovisionDelete} {
		t.Run(string(mode), func(t *testing.T) {
			var mu sync.Mutex
			exists, active, mutations := true, true, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				switch r.Method {
				case http.MethodGet:
					resources := []map[string]any{}
					if exists {
						resources = append(resources, map[string]any{"id": "user-1", "externalId": "person-1", "active": active, "displayName": "Retained name"})
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"totalResults": len(resources), "Resources": resources})
				case http.MethodPatch:
					if mode != DeprovisionDisable {
						t.Error("unexpected PATCH")
					}
					var body struct {
						Operations []struct {
							Path  string
							Value any
						}
					}
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					if len(body.Operations) != 1 || body.Operations[0].Path != "active" || body.Operations[0].Value != false {
						t.Errorf("unexpected disable payload: %#v", body)
					}
					active = false
					mutations++
					if mutations == 1 {
						dropResponse(t, w)
						return
					}
					w.WriteHeader(http.StatusNoContent)
				case http.MethodDelete:
					if mode != DeprovisionDelete {
						t.Error("unexpected DELETE")
					}
					exists = false
					mutations++
					dropResponse(t, w)
				default:
					t.Errorf("unexpected method: %s", r.Method)
					w.WriteHeader(http.StatusMethodNotAllowed)
				}
			}))
			defer server.Close()
			newAdapter := func() *Adapter {
				a, err := New(Config{BaseURL: server.URL, DeprovisionMode: mode, ManagedAttributes: []string{"displayName"}})
				if err != nil {
					t.Fatal(err)
				}
				return a
			}
			desired := reconcile.DesiredState{SubjectID: "person-1"}
			if _, err := reconcile.Converge(context.Background(), newAdapter(), desired); err == nil {
				t.Fatal("lost response must surface as an error")
			}
			for attempt := 0; attempt < 2; attempt++ {
				result, err := reconcile.Converge(context.Background(), newAdapter(), desired)
				if err != nil {
					t.Fatal(err)
				}
				if result.Action != reconcile.ConvergenceUnchanged {
					t.Errorf("recovery action = %s, want unchanged", result.Action)
				}
				if result.Observed.Exists != (mode == DeprovisionDisable) {
					t.Error("observed physical existence was lost")
				}
			}
			mu.Lock()
			defer mu.Unlock()
			if mutations != 1 {
				t.Errorf("mutations = %d, want exactly 1", mutations)
			}
		})
	}
}

func dropResponse(t *testing.T, w http.ResponseWriter) {
	t.Helper()
	conn, _, err := w.(http.Hijacker).Hijack()
	if err != nil {
		t.Error(err)
		return
	}
	_ = conn.Close()
}

func TestConvergeDeprovisionModes(t *testing.T) {
	for _, tc := range []struct {
		name                                          string
		mode                                          DeprovisionMode
		exists, active, desiredExists, desiredEnabled bool
		wantWrites                                    int
	}{
		{"default retains disabled", "", true, false, false, false, 0},
		{"disable active", DeprovisionDisable, true, true, false, false, 1},
		{"disable absent", DeprovisionDisable, false, false, false, false, 0},
		{"delete disabled", DeprovisionDelete, true, false, false, false, 1},
		{"delete absent", DeprovisionDelete, false, false, false, false, 0},
		{"rehire disabled", DeprovisionDisable, true, false, true, true, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			writes := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				if r.Method == http.MethodGet {
					resources := []map[string]any{}
					if tc.exists {
						resources = append(resources, map[string]any{"id": "user-1", "active": tc.active})
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"totalResults": len(resources), "Resources": resources})
					return
				}
				writes++
				wantMethod := http.MethodPatch
				if tc.mode == DeprovisionDelete {
					wantMethod = http.MethodDelete
				}
				if r.Method != wantMethod {
					t.Errorf("method = %s, want %s", r.Method, wantMethod)
				}
				w.WriteHeader(http.StatusNoContent)
			}))
			defer server.Close()
			a, err := New(Config{BaseURL: server.URL, DeprovisionMode: tc.mode})
			if err != nil {
				t.Fatal(err)
			}
			result, err := reconcile.Converge(context.Background(), a, reconcile.DesiredState{SubjectID: "person-1", Exists: tc.desiredExists, Enabled: tc.desiredEnabled})
			if err != nil {
				t.Fatal(err)
			}
			wantAction := reconcile.ConvergenceUnchanged
			if tc.wantWrites > 0 {
				wantAction = reconcile.ConvergenceUpdated
			}
			if result.Action != wantAction {
				t.Errorf("action = %s, want %s", result.Action, wantAction)
			}
			mu.Lock()
			defer mu.Unlock()
			if writes != tc.wantWrites {
				t.Errorf("writes = %d, want %d", writes, tc.wantWrites)
			}
		})
	}
}
