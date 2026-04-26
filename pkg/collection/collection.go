package collection

import (
	"database/sql"
	"fmt"
	"time"

	// Pure Go SQLite driver — no CGO required
	_ "modernc.org/sqlite"

	goanki "github.com/vpul/go-anki/pkg/types"
)

// Collection represents an open Anki .anki2 database.
type Collection struct {
	db   *sql.DB
	path string
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
	var dsn string
	switch mode {
	case ReadOnly:
		dsn = fmt.Sprintf("file:%s?mode=ro", path)
	case ReadWrite:
		dsn = fmt.Sprintf("file:%s?mode=rwc", path)
	default:
		dsn = fmt.Sprintf("file:%s?mode=ro", path)
	}

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open anki collection: %w", err)
	}

	// Verify this is actually an Anki database
	var ver int
	err = db.QueryRow("SELECT ver FROM col").Scan(&ver)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("not a valid Anki collection: %w", err)
	}

	// Enable WAL mode for writes (Anki's expected journal mode)
	if mode == ReadWrite {
		if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
			db.Close()
			return nil, fmt.Errorf("set WAL mode: %w", err)
		}
	}

	return &Collection{db: db, path: path}, nil
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
	defer rows.Close()

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
	err := c.db.QueryRow(`
		SELECT id, guid, mid, mod, usn, tags, flds, sfld, csum, flags, data
		FROM notes WHERE id = ?`, id).Scan(
		&note.ID, &note.GUID, &note.MID, &note.Mod, &note.USN,
		&note.Tags, &note.Flds, &note.Sfld, &note.Csum, &note.Flags, &note.Data,
	)
	if err != nil {
		return nil, err
	}
	return &note, nil
}