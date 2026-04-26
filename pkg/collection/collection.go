package collection

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	// Pure Go SQLite driver — no CGO required
	_ "modernc.org/sqlite"

	goanki "github.com/vpul/go-anki/pkg/types"
)

// ErrNotFound is returned when a requested resource does not exist.
var ErrNotFound = errors.New("not found")

// Collection represents an open Anki .anki2 database.
type Collection struct {
	db     *sql.DB
	path   string
	schema int    // cached schema version (0 = unset, 11 = old JSON blob, 18+ = table-based)
	mu     sync.Mutex // protects write operations for concurrent access
}

// OpenMode controls database access mode.
type OpenMode int

const (
	ReadOnly  OpenMode = iota // Read-only access (safe)
	ReadWrite                 // Read-write access (required for mutations)
)

// Open opens an Anki collection database.
// mode controls read/write access:
//   - ReadOnly: open in read-only mode (safe, no writes possible)
//   - ReadWrite: open in read-write mode (required for answering cards, creating notes)
func Open(path string, mode OpenMode) (*Collection, error) {
	// Validate that the database file exists and is non-empty before opening.
	// Without this check, mode=rwc silently creates a brand-new empty database,
	// which then fails with a confusing "no such table: col" error.
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("collection file does not exist: %s", path)
		}
		return nil, fmt.Errorf("stat collection file: %w", err)
	}
	if info.Size() == 0 {
		return nil, fmt.Errorf("collection file is empty (0 bytes): %s", path)
	}

	var dsn string
	switch mode {
	case ReadOnly:
		dsn = fmt.Sprintf("file:%s?mode=ro", path)
	case ReadWrite:
		// Use mode=rw (not rwc) so that a missing file produces a clear error
		// instead of silently creating an empty database with no Anki tables.
		// Add busy_timeout for concurrent access safety.
		dsn = fmt.Sprintf("file:%s?mode=rw&_busy_timeout=5000", path)
	default:
		dsn = fmt.Sprintf("file:%s?mode=ro", path)
	}

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open anki collection: %w", err)
	}

	// In ReadWrite mode, the sync.Mutex (c.mu) serializes all write operations.
	// We intentionally do NOT set SetMaxOpenConns(1) because:
	//   1. It would prevent transactions from using a separate connection for reads
	//      (e.g., dayOffsetSinceCreation uses c.db.QueryRow inside a tx), causing deadlocks.
	//   2. The WriteWrite mutex + SQLite WAL mode + _busy_timeout provides sufficient
	//      serialization for our use case.
	if mode == ReadWrite {
		// Mutex + busy_timeout is sufficient; no SetMaxOpenConns needed.
	}

	// Verify this is actually an Anki database and cache schema version
	var ver int
	err = db.QueryRow("SELECT ver FROM col").Scan(&ver)
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("not a valid Anki collection: %w", err)
	}

	// Enable WAL mode for writes (Anki's expected journal mode)
	if mode == ReadWrite {
		if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("set WAL mode: %w", err)
		}
	}

	return &Collection{db: db, path: path, schema: ver}, nil
}

// Close closes the database connection.
func (c *Collection) Close() error {
	return c.db.Close()
}

// Path returns the file path of the collection database.
func (c *Collection) Path() string {
	return c.path
}

// DB returns the underlying database connection for advanced queries.
func (c *Collection) DB() *sql.DB {
	return c.db
}

// GetDecks returns all decks in the collection.
func (c *Collection) GetDecks() (map[int64]goanki.Deck, error) {
	if c.isV18Plus() {
		return c.getDecksV18()
	}
	var decksJSON string
	err := c.db.QueryRow("SELECT decks FROM col").Scan(&decksJSON)
	if err != nil {
		return nil, fmt.Errorf("query decks: %w", err)
	}
	return goanki.ParseDecksJSON([]byte(decksJSON))
}

// GetDeckByName returns a deck by name.
func (c *Collection) GetDeckByName(name string) (*goanki.Deck, error) {
	decks, err := c.GetDecks()
	if err != nil {
		return nil, err
	}
	for _, d := range decks {
		if d.Name == name {
			return &d, nil
		}
	}
	return nil, fmt.Errorf("deck %q not found", name)
}

// GetModels returns all note types (models) in the collection.
func (c *Collection) GetModels() (map[int64]goanki.Model, error) {
	if c.isV18Plus() {
		return c.getModelsV18()
	}
	var modelsJSON string
	err := c.db.QueryRow("SELECT models FROM col").Scan(&modelsJSON)
	if err != nil {
		return nil, fmt.Errorf("query models: %w", err)
	}
	return goanki.ParseModelsJSON([]byte(modelsJSON))
}

// GetModelByName returns a model by name.
func (c *Collection) GetModelByName(name string) (*goanki.Model, error) {
	models, err := c.GetModels()
	if err != nil {
		return nil, err
	}
	for _, m := range models {
		if m.Name == name {
			return &m, nil
		}
	}
	return nil, fmt.Errorf("model %q not found", name)
}

// GetDueCards returns cards that are due for review.
func (c *Collection) GetDueCards(filter goanki.DueCardsFilter) ([]goanki.Card, error) {
	// Calculate due cutoff: today's day number since collection creation
	var crt int64
	err := c.db.QueryRow("SELECT crt FROM col").Scan(&crt)
	if err != nil {
		return nil, fmt.Errorf("query collection creation time: %w", err)
	}

	// Calculate days since collection creation
	var dayCutoff int64
	if crt > 0 {
		dayCutoff = (time.Now().Unix() - crt) / 86400
	} else {
		dayCutoff = time.Now().Unix() / 86400
	}

	query := `
		SELECT c.id, c.nid, c.did, c.ord, c.mod, c.usn,
		       c.type, c.queue, c.due, c.ivl, c.factor,
		       c.reps, c.lapses, c.left, c.odue, c.odid,
		       c.flags, c.data
		FROM cards c
		WHERE c.queue IN (0, 1, 2, 3)
		  AND c.due <= ?`

	args := []interface{}{dayCutoff}

	// Filter by deck if specified
	if filter.DeckName != "" {
		deck, err := c.GetDeckByName(filter.DeckName)
		if err != nil {
			return nil, err
		}
		query += " AND c.did = ?"
		args = append(args, deck.ID)
	}

	// Apply limit
	if filter.Limit > 0 {
		query += fmt.Sprintf(" LIMIT %d", filter.Limit)
	}

	rows, err := c.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("query due cards: %w", err)
	}
	defer func() { _ = rows.Close() }()

	// Preload decks and models for enrichment
	decks, _ := c.GetDecks()
	models, _ := c.GetModels()

	var cards []goanki.Card
	for rows.Next() {
		var card goanki.Card
		err := rows.Scan(
			&card.ID, &card.NID, &card.DID, &card.ORD, &card.Mod, &card.USN,
			&card.Type, &card.Queue, &card.Due, &card.IVL, &card.Factor,
			&card.Reps, &card.Lapses, &card.Left, &card.ODue, &card.ODID,
			&card.Flags, &card.Data,
		)
		if err != nil {
			return nil, fmt.Errorf("scan card: %w", err)
		}

		// Enrich with deck name
		if d, ok := decks[card.DID]; ok {
			card.DeckName = d.Name
		}

		// Enrich with question/answer content
		note, err := c.getNoteByID(card.NID)
		if err == nil && note != nil {
			if m, ok := models[note.MID]; ok && card.ORD < len(m.Templates) {
				card.Question, card.Answer = goanki.RenderCard(
					note.FieldsAsMap(&m), &m.Templates[card.ORD],
				)
			}
		}

		cards = append(cards, card)
	}

	return cards, nil
}

// GetCardByID returns a card by its ID.
func (c *Collection) GetCardByID(id int64) (*goanki.Card, error) {
	var card goanki.Card
	err := c.db.QueryRow(`
		SELECT c.id, c.nid, c.did, c.ord, c.mod, c.usn,
		       c.type, c.queue, c.due, c.ivl, c.factor,
		       c.reps, c.lapses, c.left, c.odue, c.odid,
		       c.flags, c.data
		FROM cards c
		WHERE c.id = ?`, id).Scan(
		&card.ID, &card.NID, &card.DID, &card.ORD, &card.Mod, &card.USN,
		&card.Type, &card.Queue, &card.Due, &card.IVL, &card.Factor,
		&card.Reps, &card.Lapses, &card.Left, &card.ODue, &card.ODID,
		&card.Flags, &card.Data,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("query card %d: %w", id, err)
	}

	// Enrich with deck name and content
	decks, _ := c.GetDecks()
	if d, ok := decks[card.DID]; ok {
		card.DeckName = d.Name
	}

	note, err := c.getNoteByID(card.NID)
	if err == nil && note != nil {
		models, _ := c.GetModels()
		if m, ok := models[note.MID]; ok && card.ORD < len(m.Templates) {
			card.Question, card.Answer = goanki.RenderCard(
				note.FieldsAsMap(&m), &m.Templates[card.ORD],
			)
		}
	}

	return &card, nil
}

// GetStats returns collection statistics.
func (c *Collection) GetStats() (*goanki.Stats, error) {
	stats := &goanki.Stats{}

	if err := c.db.QueryRow("SELECT COUNT(*) FROM cards").Scan(&stats.TotalCards); err != nil {
		return nil, fmt.Errorf("count cards: %w", err)
	}
	if err := c.db.QueryRow("SELECT COUNT(*) FROM notes").Scan(&stats.TotalNotes); err != nil {
		return nil, fmt.Errorf("count notes: %w", err)
	}
	if err := c.db.QueryRow("SELECT COUNT(*) FROM revlog").Scan(&stats.TotalReviews); err != nil {
		return nil, fmt.Errorf("count reviews: %w", err)
	}

	// Count due cards
	var crt int64
	_ = c.db.QueryRow("SELECT crt FROM col").Scan(&crt)
	var dayCutoff int64
	if crt > 0 {
		dayCutoff = (time.Now().Unix() - crt) / 86400
	} else {
		dayCutoff = time.Now().Unix() / 86400
	}

	_ = c.db.QueryRow("SELECT COUNT(*) FROM cards WHERE queue = 0").Scan(&stats.NewCards)
	_ = c.db.QueryRow("SELECT COUNT(*) FROM cards WHERE queue IN (1, 3)").Scan(&stats.LearningCards)
	_ = c.db.QueryRow("SELECT COUNT(*) FROM cards WHERE queue = 2 AND due <= ?", dayCutoff).Scan(&stats.DueCards)
	_ = c.db.QueryRow("SELECT COUNT(*) FROM cards WHERE queue = 2").Scan(&stats.ReviewCards)

	// Count decks and models
	decks, err := c.GetDecks()
	if err != nil {
		return nil, fmt.Errorf("get decks for stats: %w", err)
	}
	stats.TotalDecks = len(decks)

	models, err := c.GetModels()
	if err != nil {
		return nil, fmt.Errorf("get models for stats: %w", err)
	}
	stats.TotalModels = len(models)

	return stats, nil
}

// getNoteByID returns a note by its ID.
// This avoids the COLLATE unicase issue by doing a simple ID lookup.
func (c *Collection) getNoteByID(id int64) (*goanki.Note, error) {
	var note goanki.Note

	if c.isV18Plus() {
		// In v18+, sfld and csum are INTEGER columns, but the actual values may be
		// stored as either integers or strings depending on Anki's behavior.
		// We scan them into interface{} and format accordingly.
		var sfld interface{}
		var csum interface{}
		err := c.db.QueryRow(`
			SELECT id, guid, mid, mod, usn, tags, flds, sfld, csum, flags, data
			FROM notes WHERE id = ?`, id).Scan(
			&note.ID, &note.GUID, &note.MID, &note.Mod, &note.USN,
			&note.Tags, &note.Flds, &sfld, &csum, &note.Flags, &note.Data,
		)
		if err != nil {
			return nil, err
		}
		// Convert sfld and csum to strings regardless of stored type
		switch v := sfld.(type) {
		case string:
			note.Sfld = v
		case int64:
			note.Sfld = fmt.Sprintf("%d", v)
		case float64:
			note.Sfld = fmt.Sprintf("%v", v)
		case nil:
			note.Sfld = ""
		default:
			note.Sfld = fmt.Sprintf("%v", v)
		}
		switch v := csum.(type) {
		case string:
			note.Csum = v
		case int64:
			note.Csum = fmt.Sprintf("%d", v)
		case float64:
			note.Csum = fmt.Sprintf("%v", v)
		case nil:
			note.Csum = ""
		default:
			note.Csum = fmt.Sprintf("%v", v)
		}
	} else {
		err := c.db.QueryRow(`
			SELECT id, guid, mid, mod, usn, tags, flds, sfld, csum, flags, data
			FROM notes WHERE id = ?`, id).Scan(
			&note.ID, &note.GUID, &note.MID, &note.Mod, &note.USN,
			&note.Tags, &note.Flds, &note.Sfld, &note.Csum, &note.Flags, &note.Data,
		)
		if err != nil {
			return nil, err
		}
	}
	return &note, nil
}