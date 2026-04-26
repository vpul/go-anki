package collection

import (
	"testing"
	"time"

	goanki "github.com/vpul/go-anki/pkg/types"
)

func TestCreateDeck(t *testing.T) {
	col, _ := createReadWriteTestDB(t)
	defer col.Close()

	// Create a new deck
	deckID, err := col.CreateDeck("Test Deck")
	if err != nil {
		t.Fatalf("CreateDeck: %v", err)
	}

	if deckID == 0 {
		t.Error("expected non-zero deck ID")
	}

	// Verify the deck was created
	deck, err := col.GetDeckByName("Test Deck")
	if err != nil {
		t.Fatalf("GetDeckByName after create: %v", err)
	}

	if deck.Name != "Test Deck" {
		t.Errorf("expected deck name 'Test Deck', got %q", deck.Name)
	}

	// Creating the same deck again should return the existing ID
	existingID, err := col.CreateDeck("Test Deck")
	if err != nil {
		t.Fatalf("CreateDeck (existing): %v", err)
	}

	if existingID != deckID {
		t.Errorf("expected same deck ID %d, got %d", deckID, existingID)
	}
}

func TestAddNote(t *testing.T) {
	col, _ := createReadWriteTestDB(t)
	defer col.Close()

	// Add a note with the Basic model
	noteID, err := col.AddNote(goanki.NewNote{
		DeckName:  "Default",
		ModelName: "Basic",
		Fields: map[string]string{
			"Front": "What is the capital of France?",
			"Back":  "Paris",
		},
		Tags: []string{"geography", "europe"},
	})
	if err != nil {
		t.Fatalf("AddNote: %v", err)
	}

	if noteID == 0 {
		t.Error("expected non-zero note ID")
	}

	// Verify the note was created by fetching due cards
	cards, err := col.GetDueCards(goanki.DueCardsFilter{})
	if err != nil {
		t.Fatalf("GetDueCards: %v", err)
	}

	// Should have 2 cards now (original + new)
	if len(cards) < 2 {
		t.Errorf("expected at least 2 cards, got %d", len(cards))
	}

	// Verify the note content
	var flds string
	err = col.db.QueryRow("SELECT flds FROM notes WHERE id = ?", noteID).Scan(&flds)
	if err != nil {
		t.Fatalf("query note: %v", err)
	}

	if flds == "" {
		t.Error("expected non-empty fields")
	}

	t.Logf("Created note %d with fields: %q", noteID, flds)
}

func TestUpdateCard(t *testing.T) {
	col, _ := createReadWriteTestDB(t)
	defer col.Close()

	// Get a card
	cards, err := col.GetDueCards(goanki.DueCardsFilter{})
	if err != nil {
		t.Fatalf("GetDueCards: %v", err)
	}
	if len(cards) == 0 {
		t.Fatal("expected at least 1 card")
	}

	card := cards[0]
	card.IVL = 7
	card.Factor = 2500
	card.Reps = 1

	// Update the card
	err = col.UpdateCard(card)
	if err != nil {
		t.Fatalf("UpdateCard: %v", err)
	}

	// Verify the update
	updated, err := col.GetCardByID(card.ID)
	if err != nil {
		t.Fatalf("GetCardByID: %v", err)
	}

	if updated.IVL != 7 {
		t.Errorf("expected ivl=7, got %d", updated.IVL)
	}
	if updated.Reps != 1 {
		t.Errorf("expected reps=1, got %d", updated.Reps)
	}
}

func TestInsertReviewLog(t *testing.T) {
	col, _ := createReadWriteTestDB(t)
	defer col.Close()

	cards, err := col.GetDueCards(goanki.DueCardsFilter{})
	if err != nil {
		t.Fatalf("GetDueCards: %v", err)
	}
	if len(cards) == 0 {
		t.Fatal("expected at least 1 card")
	}

	card := cards[0]
	review := goanki.ReviewLog{
		ID:      time.Now().UnixMilli(),
		CID:     card.ID,
		USN:     -1,
		Ease:    goanki.RatingGood,
		IVL:     1,
		LastIVL: 0,
		Factor:  2500,
		Time:    5000,
		Type:    goanki.CardTypeNew,
	}

	err = col.InsertReviewLog(review)
	if err != nil {
		t.Fatalf("InsertReviewLog: %v", err)
	}

	// Verify the review was inserted
	var count int
	err = col.db.QueryRow("SELECT COUNT(*) FROM revlog WHERE cid = ?", card.ID).Scan(&count)
	if err != nil {
		t.Fatalf("count revlog: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 review log entry, got %d", count)
	}
}

func TestFieldChecksum(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"hello", "6a8d"},
		{"", "00000000"},
		{"test", "af3e"},
	}

	for _, tt := range tests {
		result := fieldChecksum(tt.input)
		if len(result) != 8 {
			t.Errorf("fieldChecksum(%q) returned %q, expected 8 chars", tt.input, result)
		}
		t.Logf("fieldChecksum(%q) = %s", tt.input, result)
	}
}

// createReadWriteTestDB creates a test database in read-write mode.
func createReadWriteTestDB(t *testing.T) (*Collection, string) {
	t.Helper()

	// Reuse createTestDB but open in ReadWrite mode
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "collection.anki2")

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("create test db: %v", err)
	}

	now := time.Now().Unix()

	// Create schema
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

	col, err := Open(dbPath, ReadWrite)
	if err != nil {
		t.Fatalf("open collection: %v", err)
	}

	return col, dbPath
}