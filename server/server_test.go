package server

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/vpul/go-anki/pkg/collection"
	"github.com/vpul/go-anki/pkg/scheduler"
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

// TestSyncDownloadWithoutConfig verifies sync download returns error without config.
func TestSyncDownloadWithoutConfig(t *testing.T) {
	srv, _ := setupServer(t)
	handler := srv.Handler()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/sync/download", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", w.Code)
	}
}

// TestSyncUploadWithoutConfig verifies sync upload returns error without config.
func TestSyncUploadWithoutConfig(t *testing.T) {
	srv, _ := setupServer(t)
	handler := srv.Handler()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/sync/upload", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", w.Code)
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