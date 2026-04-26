package collection

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	goanki "github.com/vpul/go-anki/pkg/types"
)

// createTestDB creates a minimal Anki collection database for testing.
func createTestDB(t *testing.T) (*Collection, string) {
	t.Helper()

	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "collection.anki2")

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("create test db: %v", err)
	}

	// Create minimal Anki schema
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
		last_ivl INTEGER NOT NULL,
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

	now := time.Now().Unix()

	// Insert collection metadata
	decks := fmt.Sprintf(`{"1": {"id": 1, "name": "Default", "mod": %d, "usn": -1, "dyn": 0, "conf": 1, "desc": "", "bury": true}}`, now)
	models := fmt.Sprintf(`{"1585323248": {"id": 1585323248, "name": "Basic", "mod": %d, "usn": -1, "sortf": 0, "type": 0, "did": 1, "flds": [{"name": "Front", "ord": 0, "sticky": false, "rtags": ""}, {"name": "Back", "ord": 1, "sticky": false, "rtags": ""}], "tmpls": [{"name": "Card 1", "ord": 0, "qfmt": "{{Front}}", "afmt": "{{FrontSide}}<hr>{{Back}}", "did": 0, "bqfmt": "", "bafmt": ""}], "css": ".card { font-family: arial; }", "latexPre": "", "latexPost": "", "latexsvg": 0, "req": [[0, "any", [0]]]}}`, now)
	conf := `{"curModel": 1585323248, "newBury": true, "timebox": 0}`
	dconf := fmt.Sprintf(`{"1": {"id": 1, "name": "Default", "mod": %d, "usn": -1, "new": {"delays": [1, 10], "ints": [1, 4, 7], "initialFactor": 2500}, "rev": {"perDay": 100}, "lrn": {"perDay": 100}}}`, now)
	tags := `{}`

	_, err = db.Exec(`INSERT INTO col (id, crt, mod, usn, ver, conf, models, decks, dconf, tags)
		VALUES (1, ?, ?, -1, 11, ?, ?, ?, ?, ?)`,
		now, now, conf, models, decks, dconf, tags)
	if err != nil {
		t.Fatalf("insert col: %v", err)
	}

	// Insert a test note and card
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

	db.Close()

	col, err := Open(dbPath, ReadOnly)
	if err != nil {
		t.Fatalf("open collection: %v", err)
	}

	return col, dbPath
}

func TestOpenCollection(t *testing.T) {
	col, _ := createTestDB(t)
	defer col.Close()

	if col.Path() == "" {
		t.Error("expected non-empty path")
	}
}

func TestGetDecks(t *testing.T) {
	col, _ := createTestDB(t)
	defer col.Close()

	decks, err := col.GetDecks()
	if err != nil {
		t.Fatalf("GetDecks: %v", err)
	}

	if len(decks) != 1 {
		t.Errorf("expected 1 deck, got %d", len(decks))
	}

	found := false
	for _, d := range decks {
		if d.Name == "Default" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected 'Default' deck not found")
	}
}

func TestGetModels(t *testing.T) {
	col, _ := createTestDB(t)
	defer col.Close()

	models, err := col.GetModels()
	if err != nil {
		t.Fatalf("GetModels: %v", err)
	}

	if len(models) != 1 {
		t.Errorf("expected 1 model, got %d", len(models))
	}
}

func TestGetDueCards(t *testing.T) {
	col, _ := createTestDB(t)
	defer col.Close()

	cards, err := col.GetDueCards(goanki.DueCardsFilter{})
	if err != nil {
		t.Fatalf("GetDueCards: %v", err)
	}

	if len(cards) == 0 {
		t.Error("expected at least 1 due card")
	}

	t.Logf("Found %d due cards", len(cards))
}

func TestGetDeckByName(t *testing.T) {
	col, _ := createTestDB(t)
	defer col.Close()

	_, err := col.GetDeckByName("Default")
	if err != nil {
		t.Fatalf("GetDeckByName: %v", err)
	}

	_, err = col.GetDeckByName("NonExistent")
	if err == nil {
		t.Error("expected error for non-existent deck")
	}
}

func TestGetStats(t *testing.T) {
	col, _ := createTestDB(t)
	defer col.Close()

	stats, err := col.GetStats()
	if err != nil {
		t.Fatalf("GetStats: %v", err)
	}

	if stats.TotalCards < 1 {
		t.Errorf("expected at least 1 card, got %d", stats.TotalCards)
	}
	if stats.TotalNotes < 1 {
		t.Errorf("expected at least 1 note, got %d", stats.TotalNotes)
	}
}