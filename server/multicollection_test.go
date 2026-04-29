package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vpul/go-anki/pkg/scheduler"
)

// TestParseCollections verifies the ParseCollections helper.
func TestParseCollections(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantLen int
		wantErr bool
	}{
		{"two entries", "alpha:/path/a.anki2,beta:/path/b.anki2", 2, false},
		{"one entry", "main:/home/user/col.anki2", 1, false},
		{"empty string", "", 0, true},
		{"no colon", "badentry", 0, true},
		{"empty name", ":/path/x.anki2", 0, true},
		{"empty path", "name:", 0, true},
		{"only commas", ",,,", 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := ParseCollections(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Errorf("expected error for input %q, got nil", tt.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error for input %q: %v", tt.input, err)
			}
			if len(result) != tt.wantLen {
				t.Errorf("expected %d entries, got %d", tt.wantLen, len(result))
			}
		})
	}
}

// TestNewCollectionRegistry verifies path validation at registry construction.
func TestNewCollectionRegistry(t *testing.T) {
	db1 := createTestDB(t)
	db2 := createTestDB(t)

	t.Run("valid", func(t *testing.T) {
		reg, err := NewCollectionRegistry(map[string]string{
			"coll1": db1,
			"coll2": db2,
		})
		if err != nil {
			t.Fatalf("NewCollectionRegistry: %v", err)
		}
		if reg == nil {
			t.Fatal("expected non-nil registry")
		}
	})

	t.Run("empty map", func(t *testing.T) {
		_, err := NewCollectionRegistry(map[string]string{})
		if err == nil {
			t.Error("expected error for empty map")
		}
	})

	t.Run("nonexistent path", func(t *testing.T) {
		_, err := NewCollectionRegistry(map[string]string{
			"bad": "/nonexistent/path/col.anki2",
		})
		if err == nil {
			t.Error("expected error for nonexistent path")
		}
	})

	t.Run("empty name", func(t *testing.T) {
		_, err := NewCollectionRegistry(map[string]string{
			"": db1,
		})
		if err == nil {
			t.Error("expected error for empty name")
		}
	})
}

// TestRegistryResolveAndNames verifies Resolve and Names methods.
func TestRegistryResolveAndNames(t *testing.T) {
	db1 := createTestDB(t)
	db2 := createTestDB(t)

	reg, err := NewCollectionRegistry(map[string]string{
		"zebra": db1,
		"alpha": db2,
	})
	if err != nil {
		t.Fatalf("NewCollectionRegistry: %v", err)
	}

	// Resolve known name
	path, err := reg.Resolve("alpha")
	if err != nil {
		t.Fatalf("Resolve alpha: %v", err)
	}
	if path != db2 {
		t.Errorf("expected path %q, got %q", db2, path)
	}

	// Resolve unknown name
	_, err = reg.Resolve("nonexistent")
	if err == nil {
		t.Error("expected error for unknown collection name")
	}

	// Names returns sorted list
	names := reg.Names()
	if len(names) != 2 {
		t.Fatalf("expected 2 names, got %d", len(names))
	}
	if names[0] != "alpha" || names[1] != "zebra" {
		t.Errorf("expected sorted names [alpha zebra], got %v", names)
	}
}

// TestMultiCollectionListCollections verifies GET /api/v1/collections.
func TestMultiCollectionListCollections(t *testing.T) {
	db1 := createTestDB(t)
	db2 := createTestDB(t)

	reg, err := NewCollectionRegistry(map[string]string{
		"beta":  db1,
		"alpha": db2,
	})
	if err != nil {
		t.Fatalf("NewCollectionRegistry: %v", err)
	}

	srv := NewServer("", WithScheduler(scheduler.NewFSRSScheduler()), WithCollectionRegistry(reg))
	handler := srv.Handler()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/collections", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d; body: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Collections []string `json:"collections"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Collections) != 2 {
		t.Fatalf("expected 2 collections, got %d: %v", len(resp.Collections), resp.Collections)
	}
	// Names should be sorted
	if resp.Collections[0] != "alpha" || resp.Collections[1] != "beta" {
		t.Errorf("expected [alpha beta], got %v", resp.Collections)
	}
}

// TestMultiCollectionDecksEndpoint verifies GET /api/v1/collections/{name}/decks.
func TestMultiCollectionDecksEndpoint(t *testing.T) {
	db1 := createTestDB(t)
	db2 := createTestDB(t)

	reg, err := NewCollectionRegistry(map[string]string{
		"coll1": db1,
		"coll2": db2,
	})
	if err != nil {
		t.Fatalf("NewCollectionRegistry: %v", err)
	}

	srv := NewServer("", WithScheduler(scheduler.NewFSRSScheduler()), WithCollectionRegistry(reg))
	handler := srv.Handler()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/collections/coll1/decks", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d; body: %s", w.Code, w.Body.String())
	}

	var resp map[string]json.RawMessage
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if _, ok := resp["decks"]; !ok {
		t.Error("expected decks key in response")
	}
	if _, ok := resp["collection"]; !ok {
		t.Error("expected collection key in response")
	}

	// Verify collection name matches the requested one
	var colName string
	if err := json.Unmarshal(resp["collection"], &colName); err != nil {
		t.Fatalf("decode collection field: %v", err)
	}
	if colName != "coll1" {
		t.Errorf("expected collection=coll1, got %q", colName)
	}
}

// TestMultiCollectionDueCardsEndpoint verifies GET /api/v1/collections/{name}/due-cards.
func TestMultiCollectionDueCardsEndpoint(t *testing.T) {
	db1 := createTestDB(t)

	reg, err := NewCollectionRegistry(map[string]string{"main": db1})
	if err != nil {
		t.Fatalf("NewCollectionRegistry: %v", err)
	}

	srv := NewServer("", WithScheduler(scheduler.NewFSRSScheduler()), WithCollectionRegistry(reg))
	handler := srv.Handler()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/collections/main/due-cards", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d; body: %s", w.Code, w.Body.String())
	}

	var resp map[string]json.RawMessage
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if _, ok := resp["cards"]; !ok {
		t.Error("expected cards key in response")
	}
	if _, ok := resp["collection"]; !ok {
		t.Error("expected collection key in response")
	}
}

// TestMultiCollectionStatsEndpoint verifies GET /api/v1/collections/{name}/stats.
func TestMultiCollectionStatsEndpoint(t *testing.T) {
	db1 := createTestDB(t)

	reg, err := NewCollectionRegistry(map[string]string{"main": db1})
	if err != nil {
		t.Fatalf("NewCollectionRegistry: %v", err)
	}

	srv := NewServer("", WithScheduler(scheduler.NewFSRSScheduler()), WithCollectionRegistry(reg))
	handler := srv.Handler()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/collections/main/stats", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d; body: %s", w.Code, w.Body.String())
	}

	var resp map[string]json.RawMessage
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if _, ok := resp["stats"]; !ok {
		t.Error("expected stats key in response")
	}
	if _, ok := resp["collection"]; !ok {
		t.Error("expected collection key in response")
	}
}

// TestMultiCollectionVersionEndpoint verifies GET /api/v1/collections/{name}/version.
func TestMultiCollectionVersionEndpoint(t *testing.T) {
	db1 := createTestDB(t)

	reg, err := NewCollectionRegistry(map[string]string{"main": db1})
	if err != nil {
		t.Fatalf("NewCollectionRegistry: %v", err)
	}

	srv := NewServer("", WithScheduler(scheduler.NewFSRSScheduler()), WithCollectionRegistry(reg))
	handler := srv.Handler()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/collections/main/version", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d; body: %s", w.Code, w.Body.String())
	}

	var resp map[string]string
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["version"] != "go-anki/1.0.0" {
		t.Errorf("expected version go-anki/1.0.0, got %q", resp["version"])
	}
	if resp["collection"] != "main" {
		t.Errorf("expected collection=main, got %q", resp["collection"])
	}
}

// TestMultiCollectionInvalidName verifies 404 for unregistered collection name.
func TestMultiCollectionInvalidName(t *testing.T) {
	db1 := createTestDB(t)

	reg, err := NewCollectionRegistry(map[string]string{"coll1": db1})
	if err != nil {
		t.Fatalf("NewCollectionRegistry: %v", err)
	}

	srv := NewServer("", WithScheduler(scheduler.NewFSRSScheduler()), WithCollectionRegistry(reg))
	handler := srv.Handler()

	endpoints := []string{
		"/api/v1/collections/nonexistent/decks",
		"/api/v1/collections/nonexistent/due-cards",
		"/api/v1/collections/nonexistent/stats",
		"/api/v1/collections/nonexistent/version",
	}

	for _, path := range endpoints {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)

			if w.Code != http.StatusNotFound {
				t.Errorf("expected status 404, got %d; body: %s", w.Code, w.Body.String())
			}

			var resp map[string]string
			if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
				t.Fatalf("decode error response: %v", err)
			}
			if resp["error"] != "collection not found" {
				t.Errorf("expected error 'collection not found', got %q", resp["error"])
			}
		})
	}
}

// TestMultiCollectionCreateDeck verifies POST /api/v1/collections/{name}/decks.
func TestMultiCollectionCreateDeck(t *testing.T) {
	db1 := createTestDB(t)

	reg, err := NewCollectionRegistry(map[string]string{"main": db1})
	if err != nil {
		t.Fatalf("NewCollectionRegistry: %v", err)
	}

	srv := NewServer("", WithScheduler(scheduler.NewFSRSScheduler()), WithCollectionRegistry(reg))
	handler := srv.Handler()

	body := `{"name": "Multi Test Deck"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/collections/main/decks", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d; body: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if _, ok := resp["deck_id"]; !ok {
		t.Error("expected deck_id in response")
	}
	if resp["collection"] != "main" {
		t.Errorf("expected collection=main, got %v", resp["collection"])
	}
}

// TestMultiCollectionSyncWithoutConfig verifies sync returns 503 without config.
func TestMultiCollectionSyncWithoutConfig(t *testing.T) {
	db1 := createTestDB(t)

	reg, err := NewCollectionRegistry(map[string]string{"main": db1})
	if err != nil {
		t.Fatalf("NewCollectionRegistry: %v", err)
	}

	srv := NewServer("", WithScheduler(scheduler.NewFSRSScheduler()), WithCollectionRegistry(reg))
	handler := srv.Handler()

	for _, path := range []string{
		"/api/v1/collections/main/sync/download",
		"/api/v1/collections/main/sync/upload",
	} {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, path, nil)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)

			if w.Code != http.StatusServiceUnavailable {
				t.Errorf("expected status 503, got %d; body: %s", w.Code, w.Body.String())
			}
		})
	}
}

// TestSingleCollectionBackwardCompat verifies existing single-collection routes
// are unaffected when the registry is not configured.
func TestSingleCollectionBackwardCompat(t *testing.T) {
	srv, _ := setupServer(t)
	handler := srv.Handler()

	cases := []struct {
		method string
		path   string
		body   string
		want   int
	}{
		{http.MethodGet, "/api/v1/decks", "", http.StatusOK},
		{http.MethodGet, "/api/v1/due-cards", "", http.StatusOK},
		{http.MethodGet, "/api/v1/stats", "", http.StatusOK},
		{http.MethodGet, "/api/v1/version", "", http.StatusOK},
		{http.MethodGet, "/health", "", http.StatusOK},
	}

	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			var bodyReader *strings.Reader
			if tc.body != "" {
				bodyReader = strings.NewReader(tc.body)
			} else {
				bodyReader = strings.NewReader("")
			}
			req := httptest.NewRequest(tc.method, tc.path, bodyReader)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)

			if w.Code != tc.want {
				t.Errorf("expected status %d, got %d; body: %s", tc.want, w.Code, w.Body.String())
			}
		})
	}
}

// TestMultiCollectionCollectionsNotAvailableInSingleMode verifies that
// GET /api/v1/collections is not registered in single-collection mode.
func TestMultiCollectionCollectionsNotAvailableInSingleMode(t *testing.T) {
	srv, _ := setupServer(t)
	handler := srv.Handler()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/collections", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	// Go's default mux returns 405 (Method Not Allowed) or 404 (Not Found)
	// when a route is not registered; we just check it's not 200.
	if w.Code == http.StatusOK {
		t.Error("expected non-200 for /api/v1/collections in single-collection mode")
	}
}

// TestMultiCollectionHealthCheck verifies GET /health works in multi-collection mode.
func TestMultiCollectionHealthCheck(t *testing.T) {
	db1 := createTestDB(t)

	reg, err := NewCollectionRegistry(map[string]string{"main": db1})
	if err != nil {
		t.Fatalf("NewCollectionRegistry: %v", err)
	}

	srv := NewServer("", WithScheduler(scheduler.NewFSRSScheduler()), WithCollectionRegistry(reg))
	handler := srv.Handler()

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d; body: %s", w.Code, w.Body.String())
	}

	var resp map[string]string
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["status"] != "ok" {
		t.Errorf("expected status ok, got %q", resp["status"])
	}
}

// TestMultiCollectionAddNote verifies POST /api/v1/collections/{name}/notes.
func TestMultiCollectionAddNote(t *testing.T) {
	db1 := createTestDB(t)

	reg, err := NewCollectionRegistry(map[string]string{"main": db1})
	if err != nil {
		t.Fatalf("NewCollectionRegistry: %v", err)
	}

	srv := NewServer("", WithScheduler(scheduler.NewFSRSScheduler()), WithCollectionRegistry(reg))
	handler := srv.Handler()

	body := `{"deck_name": "Default", "model_name": "Basic", "fields": {"Front": "Q", "Back": "A"}}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/collections/main/notes", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d; body: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if _, ok := resp["note_id"]; !ok {
		t.Error("expected note_id in response")
	}
	if resp["collection"] != "main" {
		t.Errorf("expected collection=main, got %v", resp["collection"])
	}
}
