package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/vpul/go-anki/pkg/collection"
	"github.com/vpul/go-anki/pkg/scheduler"
	"github.com/vpul/go-anki/pkg/sync"
	goanki "github.com/vpul/go-anki/pkg/types"
)

// createTestDB creates a minimal Anki collection database for testing.
func createTestDB(t *testing.T) string {
	t.Helper()
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "collection.anki2")

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("create test db: %v", err)
	}

	now := time.Now().Unix()

	schema := `
	CREATE TABLE col (
		id    INTEGER PRIMARY KEY,
		crt   INTEGER NOT NULL,
		mod   INTEGER NOT NULL,
		usn   INTEGER NOT NULL,
		ver   INTEGER NOT NULL,
		conf  TEXT NOT NULL,
		models TEXT NOT NULL,
		decks TEXT NOT NULL,
		dconf  TEXT NOT NULL,
		tags   TEXT NOT NULL
	);
	CREATE TABLE notes (
		id    INTEGER PRIMARY KEY,
		guid  TEXT NOT NULL,
		mid   INTEGER NOT NULL,
		mod   INTEGER NOT NULL,
		usn   INTEGER NOT NULL,
		tags  TEXT NOT NULL,
		flds  TEXT NOT NULL,
		sfld  TEXT NOT NULL,
		csum  TEXT NOT NULL,
		flags INTEGER NOT NULL,
		data  TEXT NOT NULL
	);
	CREATE TABLE cards (
		id    INTEGER PRIMARY KEY,
		nid   INTEGER NOT NULL,
		did   INTEGER NOT NULL,
		ord   INTEGER NOT NULL,
		mod   INTEGER NOT NULL,
		usn   INTEGER NOT NULL,
		type  INTEGER NOT NULL,
		queue INTEGER NOT NULL,
		due   INTEGER NOT NULL,
		ivl   INTEGER NOT NULL,
		factor INTEGER NOT NULL,
		reps  INTEGER NOT NULL,
		lapses INTEGER NOT NULL,
		left  INTEGER NOT NULL,
		odue  INTEGER NOT NULL,
		odid  INTEGER NOT NULL,
		flags INTEGER NOT NULL,
		data  TEXT NOT NULL
	);
	CREATE TABLE revlog (
		id    INTEGER PRIMARY KEY,
		cid   INTEGER NOT NULL,
		usn   INTEGER NOT NULL,
		ease  INTEGER NOT NULL,
		ivl   INTEGER NOT NULL,
		lastIvl INTEGER NOT NULL,
		factor INTEGER NOT NULL,
		time  INTEGER NOT NULL,
		type  INTEGER NOT NULL
	);
	CREATE TABLE graves (
		usn  INTEGER NOT NULL,
		oid  INTEGER NOT NULL,
		type INTEGER NOT NULL
	);
	CREATE INDEX ix_notes_csum ON notes (csum);
	CREATE INDEX ix_cards_nid ON cards (nid);
	CREATE INDEX ix_cards_sched ON cards (did, queue, due);
	CREATE INDEX ix_cards_usn ON cards (usn);
	CREATE INDEX ix_notes_usn ON notes (usn);
	CREATE INDEX ix_revlog_cid ON revlog (cid);
	CREATE INDEX ix_revlog_usn ON revlog (usn);`

	_, err = db.Exec(schema)
	if err != nil {
		t.Fatalf("create schema: %v", err)
	}

	decks := fmt.Sprintf(`{"1": {"id": 1, "name": "Default", "mod": %d, "usn": -1, "dyn": 0, "conf": 1, "desc": "", "bury": true}}`, now)
	models := fmt.Sprintf(`{"1585323248": {"id": 1585323248, "name": "Basic", "mod": %d, "usn": -1, "sortf": 0, "type": 0, "did": 1, "flds": [{"name": "Front", "ord": 0, "sticky": false, "rtags": ""}, {"name": "Back", "ord": 1, "sticky": false, "rtags": ""}], "tmpls": [{"name": "Card 1", "ord": 0, "qfmt": "{{Front}}", "afmt": "{{FrontSide}}<hr>{{Back}}", "did": 0, "bqfmt": "", "bafmt": ""}], "css": ".card { font-family: arial; }", "latexPre": "", "latexPost": "", "latexsvg": 0, "req": [[0, "any", [0]]]}}`, now)
	conf := `{"curModel": 1585323248, "newBury": true, "timebox": 0}`
	dconf := fmt.Sprintf(`{"1": {"id": 1, "name": "Default", "mod": %d, "usn": -1}}`, now)
	tags := `{}`

	_, err = db.Exec(`INSERT INTO col (id, crt, mod, usn, ver, conf, models, decks, dconf, tags)
		VALUES (1, ?, ?, -1, 11, ?, ?, ?, ?, ?)`,
		now, now, conf, models, decks, dconf, tags)
	if err != nil {
		t.Fatalf("insert col: %v", err)
	}

	noteID := int64(1000000000000)
	cardID := noteID + 1
	_, err = db.Exec(`INSERT INTO notes (id, guid, mid, mod, usn, tags, flds, sfld, csum, flags, data)
		VALUES (?, 'test123', 1585323248, ?, -1, ' test ', 'What is 2+2?\x1fFour', 'What is 2+2?', '12345678', 0, '')`,
		noteID, now)
	if err != nil {
		t.Fatalf("insert note: %v", err)
	}

	_, err = db.Exec(`INSERT INTO cards (id, nid, did, ord, mod, usn, type, queue, due, ivl, factor, reps, lapses, left, odue, odid, flags, data)
		VALUES (?, ?, 1, 0, ?, -1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, '')`,
		cardID, noteID, now)
	if err != nil {
		t.Fatalf("insert card: %v", err)
	}

	_ = db.Close()
	return dbPath
}

// setupServer creates a test database and a Server configured for testing.
func setupServer(t *testing.T) (*Server, string) {
	t.Helper()
	dbPath := createTestDB(t)
	srv := NewServer(dbPath, WithScheduler(scheduler.NewFSRSScheduler()))
	return srv, dbPath
}

// TestVersion verifies the version endpoint returns expected data.
func TestVersion(t *testing.T) {
	srv, _ := setupServer(t)
	handler := srv.Handler()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/version", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", w.Code)
	}

	var resp map[string]string
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["version"] != "go-anki/1.0.0" {
		t.Errorf("expected version go-anki/1.0.0, got %q", resp["version"])
	}
}

// TestGetDecks verifies the decks endpoint returns decks.
func TestGetDecks(t *testing.T) {
	srv, _ := setupServer(t)
	handler := srv.Handler()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/decks", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", w.Code)
	}

	var resp map[string]json.RawMessage
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if _, ok := resp["decks"]; !ok {
		t.Error("expected decks key in response")
	}
}

// TestGetDueCards verifies the due-cards endpoint returns cards.
func TestGetDueCards(t *testing.T) {
	srv, _ := setupServer(t)
	handler := srv.Handler()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/due-cards", nil)
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
}

// TestGetDueCardsWithParams verifies due-cards respects deck and limit params.
func TestGetDueCardsWithParams(t *testing.T) {
	srv, _ := setupServer(t)
	handler := srv.Handler()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/due-cards?deck=Default&limit=5", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", w.Code)
	}
}

// TestGetStats verifies the stats endpoint.
func TestGetStats(t *testing.T) {
	srv, _ := setupServer(t)
	handler := srv.Handler()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/stats", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", w.Code)
	}

	var resp map[string]json.RawMessage
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if _, ok := resp["stats"]; !ok {
		t.Error("expected stats key in response")
	}
}

// TestGetCardByID verifies fetching a single card.
func TestGetCardByID(t *testing.T) {
	srv, dbPath := setupServer(t)
	handler := srv.Handler()

	// Open the DB to find the card ID
	col, err := collection.Open(dbPath, collection.ReadOnly)
	if err != nil {
		t.Fatalf("open collection: %v", err)
	}
	cards, err := col.GetDueCards(goanki.DueCardsFilter{})
	_ = col.Close()
	if err != nil || len(cards) == 0 {
		t.Fatalf("no cards found: %v", err)
	}

	cardID := cards[0].ID
	url := "/api/v1/cards/" + strconv.FormatInt(cardID, 10)
	req := httptest.NewRequest(http.MethodGet, url, nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d; body: %s", w.Code, w.Body.String())
	}

	var resp map[string]json.RawMessage
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if _, ok := resp["card"]; !ok {
		t.Error("expected card key in response")
	}
}

// TestGetCardByIDInvalid verifies invalid card ID returns error.
func TestGetCardByIDInvalid(t *testing.T) {
	srv, _ := setupServer(t)
	handler := srv.Handler()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/cards/invalid", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", w.Code)
	}
}

// TestGetCardByIDNotFound verifies nonexistent card returns 404.
func TestGetCardByIDNotFound(t *testing.T) {
	srv, _ := setupServer(t)
	handler := srv.Handler()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/cards/999999999", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected status 404, got %d", w.Code)
	}
}

// TestAnswer verifies answering a card.
func TestAnswer(t *testing.T) {
	srv, dbPath := setupServer(t)
	handler := srv.Handler()

	// Get a card ID first
	col, err := collection.Open(dbPath, collection.ReadOnly)
	if err != nil {
		t.Fatalf("open collection: %v", err)
	}
	cards, err := col.GetDueCards(goanki.DueCardsFilter{})
	_ = col.Close()
	if err != nil || len(cards) == 0 {
		t.Fatalf("no cards: %v", err)
	}

	cardID := cards[0].ID
	body := fmt.Sprintf(`{"card_id": %d, "rating": "good"}`, cardID)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/answer", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d; body: %s", w.Code, w.Body.String())
	}

	var resp map[string]json.RawMessage
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if _, ok := resp["card"]; !ok {
		t.Error("expected card key in response")
	}
	if _, ok := resp["review"]; !ok {
		t.Error("expected review key in response")
	}
}

// TestAnswerInvalidRating verifies invalid rating returns error.
func TestAnswerInvalidRating(t *testing.T) {
	srv, _ := setupServer(t)
	handler := srv.Handler()

	body := `{"card_id": 1, "rating": "invalid"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/answer", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", w.Code)
	}
}

// TestAnswerMissingCardID verifies missing card_id returns error.
func TestAnswerMissingCardID(t *testing.T) {
	srv, _ := setupServer(t)
	handler := srv.Handler()

	body := `{"rating": "good"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/answer", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", w.Code)
	}
}

// TestAnswerInvalidBody verifies invalid JSON body returns error.
func TestAnswerInvalidBody(t *testing.T) {
	srv, _ := setupServer(t)
	handler := srv.Handler()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/answer", strings.NewReader("not json"))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", w.Code)
	}
}

// TestCreateDeck verifies creating a new deck.
func TestCreateDeck(t *testing.T) {
	srv, _ := setupServer(t)
	handler := srv.Handler()

	body := `{"name": "Test Deck"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/decks", strings.NewReader(body))
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
	deckID, ok := resp["deck_id"].(float64)
	if !ok || deckID == 0 {
		t.Error("expected non-zero deck_id")
	}
}

// TestCreateDeckEmptyName verifies empty deck name returns error.
func TestCreateDeckEmptyName(t *testing.T) {
	srv, _ := setupServer(t)
	handler := srv.Handler()

	body := `{"name": ""}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/decks", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", w.Code)
	}
}

// TestAddNote verifies adding a new note.
func TestAddNote(t *testing.T) {
	srv, _ := setupServer(t)
	handler := srv.Handler()

	body := `{"deck_name": "Default", "model_name": "Basic", "fields": {"Front": "Hello", "Back": "World"}, "tags": ["test"]}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/notes", strings.NewReader(body))
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
	noteID, ok := resp["note_id"].(float64)
	if !ok || noteID == 0 {
		t.Error("expected non-zero note_id")
	}
}

// TestAddNoteMissingFields verifies missing required fields return error.
func TestAddNoteMissingFields(t *testing.T) {
	srv, _ := setupServer(t)
	handler := srv.Handler()

	tests := []struct {
		name string
		body string
	}{
		{"missing deck_name", `{"model_name": "Basic", "fields": {"Front": "Hi"}}`},
		{"missing model_name", `{"deck_name": "Default", "fields": {"Front": "Hi"}}`},
		{"missing fields", `{"deck_name": "Default", "model_name": "Basic"}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/v1/notes", strings.NewReader(tt.body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)

			if w.Code != http.StatusBadRequest {
				t.Errorf("expected status 400, got %d", w.Code)
			}
		})
	}
}

// TestSyncDownloadWithoutConfig verifies sync download returns 503 without config.
func TestSyncDownloadWithoutConfig(t *testing.T) {
	srv, _ := setupServer(t)
	handler := srv.Handler()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/sync/download", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected status 503, got %d", w.Code)
	}
}

// TestSyncDeltaWithoutConfig verifies sync delta returns 503 without config.
func TestSyncDeltaWithoutConfig(t *testing.T) {
	srv, _ := setupServer(t)
	handler := srv.Handler()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/sync/delta", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected status 503, got %d", w.Code)
	}
}

// TestSyncDeltaHandler verifies the POST /api/v1/sync/delta endpoint with a mock AnkiWeb server.
func TestSyncDeltaHandler(t *testing.T) {
	// Create a test collection DB
	dbPath := createTestDB(t)

	var sessionKey atomic.Value
	sessionKey.Store("")

	// Mock AnkiWeb delta sync server
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/sync/hostKey":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"key":"handler-mock-key"}`))
			sessionKey.Store("handler-mock-key")
		case "/sync/start":
			if r.URL.Query().Get("k") != sessionKey.Load().(string) {
				t.Errorf("expected session key %q, got %q", sessionKey.Load().(string), r.URL.Query().Get("k"))
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":{"scm":1700000000,"usn":0,"hostNum":0}}`))
		case "/sync/applyChanges":
			if r.URL.Query().Get("k") != sessionKey.Load().(string) {
				t.Errorf("expected session key %q, got %q", sessionKey.Load().(string), r.URL.Query().Get("k"))
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":{"cards":null,"notes":null,"decks":null,"graves":null,"usn":0,"more":false}}`))
		case "/sync/applyGraves":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":{"usn":0}}`))
		case "/sync/finish":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":{"scm":1700000000,"usn":0,"hostNum":0}}`))
		default:
			t.Errorf("unexpected mock server path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer mockServer.Close()

	// Create server with sync config pointing at the mock
	srv := NewServer(dbPath,
		WithScheduler(scheduler.NewFSRSScheduler()),
		WithSyncConfig(goanki.SyncConfig{
			Username: "test@example.com",
			Password: "secret",
			SyncURL:  mockServer.URL + "/sync/",
		}),
	)
	handler := srv.Handler()

	// Call the delta sync endpoint
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sync/delta", nil)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	// Verify response
	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d; body: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if resp["status"] != "ok" {
		t.Errorf("expected status 'ok', got %q", resp["status"])
	}

	// Verify cards_before and cards_after are present
	if _, ok := resp["cards_before"]; !ok {
		t.Error("expected cards_before in response")
	}
	if _, ok := resp["cards_after"]; !ok {
		t.Error("expected cards_after in response")
	}

	// Verify the mock server was actually hit (sessionKey should be set)
	if sessionKey.Load().(string) != "handler-mock-key" {
		t.Error("mock server was not called")
	}
}

// TestSyncDeltaClientFullSync verifies a full delta sync cycle against a mock AnkiWeb server.
func TestSyncDeltaClientFullSync(t *testing.T) {
	// Create a test collection DB
	dbPath := createTestDB(t)

	// Track session key for validation
	var sessionKey string

	// Mock AnkiWeb delta sync server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/sync/hostKey":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"key":"delta-mock-key"}`))
			sessionKey = "delta-mock-key"
		case "/sync/start":
			if r.URL.Query().Get("k") != sessionKey {
				t.Errorf("expected session key %q", sessionKey)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":{"scm":1700000000,"usn":0,"hostNum":0}}`))
		case "/sync/applyChanges":
			if r.URL.Query().Get("k") != sessionKey {
				t.Errorf("expected session key %q", sessionKey)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			// Return no changes and more=false to end pagination
			_, _ = w.Write([]byte(`{"data":{"cards":null,"notes":null,"decks":null,"graves":null,"usn":0,"more":false}}`))
		case "/sync/applyGraves":
			if r.URL.Query().Get("k") != sessionKey {
				t.Errorf("expected session key %q", sessionKey)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":{"usn":0}}`))
		case "/sync/finish":
			if r.URL.Query().Get("k") != sessionKey {
				t.Errorf("expected session key %q", sessionKey)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":{"scm":1700000000,"usn":0,"hostNum":0}}`))
		default:
			t.Errorf("unexpected mock server path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	client, err := sync.NewDeltaClientWithURL(goanki.SyncConfig{
		Username: "test@example.com",
		Password: "secret",
	}, server.URL+"/sync/")
	if err != nil {
		t.Fatalf("NewDeltaClientWithURL: %v", err)
	}

	// Run FullSync — this exercises Authenticate, SyncStart, ApplyChanges, ApplyGraves, SyncFinish
	// against the mock server, plus collection operations on the test DB.
	if err := client.FullSync(context.Background(), dbPath); err != nil {
		t.Fatalf("FullSync: %v", err)
	}
}

// TestSyncDeltaClientFullSyncPagination verifies pagination loop (More field).
func TestSyncDeltaClientFullSyncPagination(t *testing.T) {
	dbPath := createTestDB(t)

	applyCount := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/sync/hostKey":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"key":"mock-pagination-key"}`))
		case "/sync/start":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":{"scm":1700000001,"usn":0,"hostNum":0}}`))
		case "/sync/applyChanges":
			applyCount++
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			if applyCount <= 2 {
				// Return more=true for first two calls
				_, _ = w.Write([]byte(`{"data":{"cards":null,"notes":null,"decks":null,"graves":null,"usn":0,"more":true}}`))
			} else {
				// Return more=false on third call to stop pagination
				_, _ = w.Write([]byte(`{"data":{"cards":null,"notes":null,"decks":null,"graves":null,"usn":0,"more":false}}`))
			}
		case "/sync/applyGraves":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":{"usn":0}}`))
		case "/sync/finish":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":{"scm":1700000001,"usn":0,"hostNum":0}}`))
		default:
			t.Errorf("unexpected mock path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	client, err := sync.NewDeltaClientWithURL(goanki.SyncConfig{
		Username: "test@example.com",
		Password: "secret",
	}, server.URL+"/sync/")
	if err != nil {
		t.Fatalf("NewDeltaClientWithURL: %v", err)
	}

	if err := client.FullSync(context.Background(), dbPath); err != nil {
		t.Fatalf("FullSync with pagination: %v", err)
	}

	if applyCount < 2 {
		t.Errorf("expected at least 2 applyChanges calls for pagination, got %d", applyCount)
	}
}

func TestSyncUploadWithoutConfig(t *testing.T) {
	srv, _ := setupServer(t)
	handler := srv.Handler()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/sync/upload", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected status 503, got %d", w.Code)
	}
}

// TestAllRatings verifies all valid rating strings work.
func TestAllRatings(t *testing.T) {
	ratings := []string{"again", "hard", "good", "easy"}
	for _, rating := range ratings {
		t.Run(rating, func(t *testing.T) {
			// Create a fresh DB for each rating to avoid state conflicts
			srv, _ := setupServer(t)
			handler := srv.Handler()

			// Open the fresh DB to get a card
			col, err := collection.Open(srv.dbPath, collection.ReadOnly)
			if err != nil {
				t.Fatalf("open: %v", err)
			}
			cards, err := col.GetDueCards(goanki.DueCardsFilter{})
			_ = col.Close()
			if err != nil || len(cards) == 0 {
				t.Fatalf("no cards: %v", err)
			}

			cardID := cards[0].ID
			body := fmt.Sprintf(`{"card_id": %d, "rating": "%s"}`, cardID, rating)
			req := httptest.NewRequest(http.MethodPost, "/api/v1/answer", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)

			if w.Code != http.StatusOK {
				t.Fatalf("rating %q: expected status 200, got %d; body: %s", rating, w.Code, w.Body.String())
			}
		})
	}
}

// TestWithPort verifies the port option works.
func TestWithPort(t *testing.T) {
	srv := NewServer("/tmp/test.anki2", WithPort(9999))
	if srv.port != 9999 {
		t.Errorf("expected port 9999, got %d", srv.port)
	}
}

// TestDefaultPort verifies the default port is 8765.
func TestDefaultPort(t *testing.T) {
	srv := NewServer("/tmp/test.anki2")
	if srv.port != 8765 {
		t.Errorf("expected default port 8765, got %d", srv.port)
	}
}

// TestWithSyncConfig verifies the sync config option works.
func TestWithSyncConfig(t *testing.T) {
	cfg := goanki.SyncConfig{Username: "test", Password: "pass"}
	srv := NewServer("/tmp/test.anki2", WithSyncConfig(cfg))
	if srv.syncConfig == nil || srv.syncConfig.Username != "test" {
		t.Error("expected sync config to be set")
	}
}

func TestAuthTokenHash(t *testing.T) {
	srv := NewServer("/tmp/test.anki2", WithAuthToken("mysecret"))
	if len(srv.authTokenHash) != 32 {
		t.Errorf("expected 32-byte hash, got %d bytes", len(srv.authTokenHash))
	}
	// Verify the hash is SHA-256 of "mysecret"
	expected := sha256.Sum256([]byte("mysecret"))
	if !bytes.Equal(srv.authTokenHash, expected[:]) {
		t.Error("auth token hash does not match expected SHA-256")
	}
}

func TestAuthMiddlewareWithoutToken(t *testing.T) {
	srv, _ := setupServer(t)
	handler := srv.Handler()

	// Without token configured, all endpoints should be accessible
	req := httptest.NewRequest(http.MethodGet, "/api/v1/version", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200 without auth, got %d", w.Code)
	}
}

func TestAuthMiddlewareWithToken(t *testing.T) {
	dbPath := createTestDB(t)
	srv := NewServer(dbPath, WithScheduler(scheduler.NewFSRSScheduler()), WithAuthToken("testtoken"))
	handler := srv.Handler()

	tests := []struct {
		name       string
		path       string
		method     string
		token      string
		wantStatus int
	}{
		{
			name:       "health endpoint no auth needed",
			path:       "/health",
			method:     http.MethodGet,
			token:      "",
			wantStatus: http.StatusOK,
		},
		{
			name:       "API endpoint no auth header",
			path:       "/api/v1/version",
			method:     http.MethodGet,
			token:      "",
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "API endpoint wrong token",
			path:       "/api/v1/version",
			method:     http.MethodGet,
			token:      "wrongtoken",
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "API endpoint correct token",
			path:       "/api/v1/version",
			method:     http.MethodGet,
			token:      "testtoken",
			wantStatus: http.StatusOK,
		},
		{
			name:       "decks endpoint correct token",
			path:       "/api/v1/decks",
			method:     http.MethodGet,
			token:      "testtoken",
			wantStatus: http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.path, nil)
			if tt.token != "" {
				req.Header.Set("Authorization", "Bearer "+tt.token)
			}
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)

			if w.Code != tt.wantStatus {
				t.Errorf("expected status %d, got %d; body: %s", tt.wantStatus, w.Code, w.Body.String())
			}
		})
	}
}

func TestAuthMiddlewareUnauthorizedResponse(t *testing.T) {
	dbPath := createTestDB(t)
	srv := NewServer(dbPath, WithScheduler(scheduler.NewFSRSScheduler()), WithAuthToken("secret"))
	handler := srv.Handler()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/version", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected status 401, got %d", w.Code)
	}

	var resp map[string]string
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["error"] != "unauthorized" {
		t.Errorf("expected error 'unauthorized', got %q", resp["error"])
	}
}

func TestHealthEndpointNoAuth(t *testing.T) {
	dbPath := createTestDB(t)
	srv := NewServer(dbPath, WithScheduler(scheduler.NewFSRSScheduler()), WithAuthToken("secret"))
	handler := srv.Handler()

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", w.Code)
	}

	var resp map[string]string
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["status"] != "ok" {
		t.Errorf("expected status 'ok', got %q", resp["status"])
	}
}

func TestRateLimitMiddleware(t *testing.T) {
	srv, _ := setupServer(t)
	srv.rateLimit = 3 // Low limit for testing
	srv.limiter = &rateLimiter{
		requests: make(map[string][]time.Time),
		limit:    3,
	}
	handler := srv.Handler()

	// First 3 requests should succeed
	for i := 0; i < 3; i++ {
		req := httptest.NewRequest(http.MethodGet, "/health", nil)
		req.RemoteAddr = "1.2.3.4:12345"
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Errorf("request %d: expected status 200, got %d", i+1, w.Code)
		}
	}

	// 4th request should be rate limited
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	req.RemoteAddr = "1.2.3.4:12345"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusTooManyRequests {
		t.Errorf("expected status 429, got %d", w.Code)
	}

	// Different IP should not be rate limited
	req = httptest.NewRequest(http.MethodGet, "/health", nil)
	req.RemoteAddr = "5.6.7.8:12345"
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("different IP: expected status 200, got %d", w.Code)
	}
}

func TestRateLimitDisabled(t *testing.T) {
	srv, _ := setupServer(t)
	srv.rateLimit = 0 // disabled
	handler := srv.Handler()

	// Should allow many requests without rate limiting
	for i := 0; i < 100; i++ {
		req := httptest.NewRequest(http.MethodGet, "/health", nil)
		req.RemoteAddr = "1.2.3.4:12345"
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Errorf("request %d: expected status 200, got %d", i, w.Code)
			break
		}
	}
}

func TestRateLimitResponseFormat(t *testing.T) {
	srv, _ := setupServer(t)
	srv.rateLimit = 1
	srv.limiter = &rateLimiter{
		requests: make(map[string][]time.Time),
		limit:    1,
	}
	handler := srv.Handler()

	// Use up the quota
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	req.RemoteAddr = "1.2.3.4:12345"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	// Next request should be 429 with JSON error
	req = httptest.NewRequest(http.MethodGet, "/health", nil)
	req.RemoteAddr = "1.2.3.4:12345"
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("expected status 429, got %d", w.Code)
	}

	var resp map[string]string
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["error"] != "rate limit exceeded" {
		t.Errorf("expected error 'rate limit exceeded', got %q", resp["error"])
	}
}

func TestRateLimiterAllow(t *testing.T) {
	rl := &rateLimiter{
		requests: make(map[string][]time.Time),
		limit:    2,
	}

	// First request should be allowed
	result := rl.allow("1.2.3.4")
	if !result.allowed {
		t.Error("first request should be allowed")
	}

	// Second request should be allowed
	result = rl.allow("1.2.3.4")
	if !result.allowed {
		t.Error("second request should be allowed")
	}

	// Third request should be rate limited
	result = rl.allow("1.2.3.4")
	if result.allowed {
		t.Error("third request should be rate limited")
	}

	// Different IP should be allowed
	result = rl.allow("5.6.7.8")
	if !result.allowed {
		t.Error("different IP should be allowed")
	}
}

// TestGetDueCardsLimitValidation verifies that negative, zero, and oversized limits
// are handled gracefully (negative/zero default to 100, oversized cap at 1000).
func TestGetDueCardsLimitValidation(t *testing.T) {
	tests := []struct {
		param     string
		wantCards bool // true = response should include cards key
	}{
		{"limit=-1", true},
		{"limit=0", true},
		{"limit=abc", true},
		{"limit=5", true},
		{"limit=9999", true},
	}
	for _, tt := range tests {
		t.Run(tt.param, func(t *testing.T) {
			srv, _ := setupServer(t)
			handler := srv.Handler()

			req := httptest.NewRequest(http.MethodGet, "/api/v1/due-cards?"+tt.param, nil)
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
		})
	}
}

// TestGetDueCardsQuestionAnswer verifies that due cards are enriched with question/answer.
func TestGetDueCardsQuestionAnswer(t *testing.T) {
	srv, _ := setupServer(t)
	handler := srv.Handler()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/due-cards", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d; body: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Cards []struct {
			Question string `json:"question"`
			Answer   string `json:"answer"`
		} `json:"cards"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Cards) == 0 {
		t.Fatal("expected at least one due card")
	}
	if resp.Cards[0].Question == "" {
		t.Error("expected non-empty question on due card")
	}
	if resp.Cards[0].Answer == "" {
		t.Error("expected non-empty answer on due card")
	}
}

// TestAnswerCardNotFoundHTTP verifies that answering a non-existent card returns 404.
func TestAnswerCardNotFoundHTTP(t *testing.T) {
	srv, _ := setupServer(t)
	handler := srv.Handler()

	body := `{"card_id": 999999999999, "rating": "good"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/answer", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected status 404, got %d; body: %s", w.Code, w.Body.String())
	}
}

// TestSecurityHeaders verifies that X-Content-Type-Options and X-Frame-Options are set.
func TestSecurityHeaders(t *testing.T) {
	srv, _ := setupServer(t)
	handler := srv.Handler()

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if got := w.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("expected X-Content-Type-Options: nosniff, got %q", got)
	}
	if got := w.Header().Get("X-Frame-Options"); got != "DENY" {
		t.Errorf("expected X-Frame-Options: DENY, got %q", got)
	}
}

func TestRateLimiterCleanup(t *testing.T) {
	rl := &rateLimiter{
		requests: make(map[string][]time.Time),
		limit:    100,
	}

	// Add an old entry
	rl.requests["1.2.3.4"] = []time.Time{time.Now().Add(-2 * time.Minute)}
	rl.requests["5.6.7.8"] = []time.Time{time.Now()}

	rl.cleanup()

	// Old entry should be removed
	if _, ok := rl.requests["1.2.3.4"]; ok {
		t.Error("expired IP should be cleaned up")
	}

	// Recent entry should remain
	if _, ok := rl.requests["5.6.7.8"]; !ok {
		t.Error("recent IP should not be cleaned up")
	}
}