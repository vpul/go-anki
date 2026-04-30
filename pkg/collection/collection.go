package collection

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	// Pure Go SQLite driver — no CGO required
	_ "modernc.org/sqlite"

	goanki "github.com/vpul/go-anki/pkg/types"
)

const secondsPerDay int64 = 86400

// ErrNotFound is returned when a requested resource does not exist.
var ErrNotFound = errors.New("not found")

// Collection represents an open Anki .anki2 database.
type Collection struct {
	db     *sql.DB
	path   string
	schema int    // cached schema version (0 = unset, 11 = old JSON blob, 18+ = table-based)
	mu     sync.Mutex // protects write operations for concurrent access
}

// dbOrTx matches the methods of *sql.DB and *sql.Tx that we use.
type dbOrTx interface {
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
	Exec(query string, args ...any) (sql.Result, error)
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
		dsn = fmt.Sprintf("file:%s?mode=ro", url.PathEscape(path))
	case ReadWrite:
		// Use mode=rw (not rwc) so that a missing file produces a clear error
		// instead of silently creating an empty database with no Anki tables.
		// Add busy_timeout for concurrent access safety.
		dsn = fmt.Sprintf("file:%s?mode=rw&_busy_timeout=5000", url.PathEscape(path))
	default:
		dsn = fmt.Sprintf("file:%s?mode=ro", url.PathEscape(path))
	}

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open anki collection: %w", err)
	}

	// In ReadWrite mode, the sync.Mutex (c.mu) serializes all write operations.
	// We intentionally do NOT set SetMaxOpenConns(1) because:
	//   1. It would prevent transactions from using a separate connection for reads
	//      (e.g., dayOffsetSinceCreation uses c.db.QueryRow inside a tx), causing deadlocks.
	//   2. The mutex + SQLite WAL mode + _busy_timeout provides sufficient
	//      serialization for our use case.

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
	return c.getDecksWithDB(c.db)
}

func (c *Collection) getDecksWithDB(db dbOrTx) (map[int64]goanki.Deck, error) {
	if c.isV18Plus() {
		return getDecksV18WithDB(db)
	}
	var decksJSON string
	err := db.QueryRow("SELECT decks FROM col").Scan(&decksJSON)
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
		dayCutoff = (time.Now().Unix() - crt) / secondsPerDay
	} else {
		dayCutoff = time.Now().Unix() / secondsPerDay
	}

	// Queue 0 (new): due is a position/order number (always ≤ dayCutoff as it grows).
	// Queue 2/3 (review/relearning): due is a day-number, compared against dayCutoff.
	// Queue 1 (intraday learning): due is a Unix timestamp; use wall-clock seconds.
	query := `
		SELECT c.id, c.nid, c.did, c.ord, c.mod, c.usn,
		       c.type, c.queue, c.due, c.ivl, c.factor,
		       c.reps, c.lapses, c.left, c.odue, c.odid,
		       c.flags, c.data
		FROM cards c
		WHERE (
		    (c.queue IN (0, 2, 3) AND c.due <= ?)
		    OR (c.queue = 1 AND c.due <= ?)
		)`

	now := time.Now().Unix()
	args := []any{dayCutoff, now}

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
	decks, decksErr := c.GetDecks()
	models, modelsErr := c.GetModels()
	if decksErr != nil {
		log.Printf("warning: failed to load decks for enrichment: %v", decksErr)
	}
	if modelsErr != nil {
		log.Printf("warning: failed to load models for enrichment: %v", modelsErr)
	}

	var cards []goanki.Card
	var nids []int64
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

		cards = append(cards, card)
		nids = append(nids, card.NID)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate due cards: %w", err)
	}

	// Batch-load notes for all cards in one query (avoids N+1).
	// On failure we degrade gracefully: cards are returned without question/answer
	// enrichment rather than failing the whole call. Callers that need enrichment
	// can detect empty Question fields if necessary.
	notes, err := c.getNotesByIDs(nids)
	if err != nil {
		log.Printf("warning: failed to batch-load notes for enrichment: %v", err)
		notes = map[int64]*goanki.Note{}
	}

	for i := range cards {
		note, ok := notes[cards[i].NID]
		if !ok || note == nil {
			continue
		}
		if m, ok := models[note.MID]; ok && cards[i].ORD < len(m.Templates) {
			cards[i].Question, cards[i].Answer = goanki.RenderCard(
				note.FieldsAsMap(&m), &m.Templates[cards[i].ORD],
			)
		}
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
	decks, decksErr := c.GetDecks()
	if decksErr != nil {
		log.Printf("warning: failed to load decks for enrichment: %v", decksErr)
	}
	if d, ok := decks[card.DID]; ok {
		card.DeckName = d.Name
	}

	note, err := c.getNoteByID(card.NID)
	if err == nil && note != nil {
		models, modelsErr := c.GetModels()
		if modelsErr != nil {
			log.Printf("warning: failed to load models for enrichment: %v", modelsErr)
		}
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
	if err := c.db.QueryRow("SELECT crt FROM col").Scan(&crt); err != nil {
		return nil, fmt.Errorf("query collection creation time: %w", err)
	}
	var dayCutoff int64
	if crt > 0 {
		dayCutoff = (time.Now().Unix() - crt) / secondsPerDay
	} else {
		dayCutoff = time.Now().Unix() / secondsPerDay
	}

	if err := c.db.QueryRow("SELECT COUNT(*) FROM cards WHERE queue = 0").Scan(&stats.NewCards); err != nil {
		return nil, fmt.Errorf("count new cards: %w", err)
	}
	if err := c.db.QueryRow("SELECT COUNT(*) FROM cards WHERE queue IN (1, 3)").Scan(&stats.LearningCards); err != nil {
		return nil, fmt.Errorf("count learning cards: %w", err)
	}
	if err := c.db.QueryRow("SELECT COUNT(*) FROM cards WHERE queue = 2 AND due <= ?", dayCutoff).Scan(&stats.DueCards); err != nil {
		return nil, fmt.Errorf("count due cards: %w", err)
	}
	if err := c.db.QueryRow("SELECT COUNT(*) FROM cards WHERE queue = 2").Scan(&stats.ReviewCards); err != nil {
		return nil, fmt.Errorf("count review cards: %w", err)
	}

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

// ifaceToStr converts a scanned SQLite value to a string.
// In v18+ schema, sfld and csum columns may be INTEGER or TEXT depending on Anki's behavior.
func ifaceToStr(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case int64:
		return fmt.Sprintf("%d", t)
	case float64:
		return fmt.Sprintf("%v", t)
	case nil:
		return ""
	default:
		return fmt.Sprintf("%v", t)
	}
}

// getNoteByID returns a note by its ID.
// This avoids the COLLATE unicase issue by doing a simple ID lookup.
func (c *Collection) getNoteByID(id int64) (*goanki.Note, error) {
	var note goanki.Note

	if c.isV18Plus() {
		// In v18+, sfld and csum are INTEGER columns, but the actual values may be
		// stored as either integers or strings depending on Anki's behavior.
		var sfld, csum interface{}
		err := c.db.QueryRow(`
			SELECT id, guid, mid, mod, usn, tags, flds, sfld, csum, flags, data
			FROM notes WHERE id = ?`, id).Scan(
			&note.ID, &note.GUID, &note.MID, &note.Mod, &note.USN,
			&note.Tags, &note.Flds, &sfld, &csum, &note.Flags, &note.Data,
		)
		if err != nil {
			return nil, err
		}
		note.Sfld = ifaceToStr(sfld)
		note.Csum = ifaceToStr(csum)
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

// getNotesByIDs returns notes for a batch of IDs in a single query.
// Callers use the returned map to enrich cards without N+1 queries.
func (c *Collection) getNotesByIDs(ids []int64) (map[int64]*goanki.Note, error) {
	if len(ids) == 0 {
		return map[int64]*goanki.Note{}, nil
	}

	// Deduplicate IDs to keep the IN clause minimal
	seen := make(map[int64]struct{}, len(ids))
	unique := make([]any, 0, len(ids))
	for _, id := range ids {
		if _, ok := seen[id]; !ok {
			seen[id] = struct{}{}
			unique = append(unique, id)
		}
	}

	placeholders := strings.Repeat(",?", len(unique))[1:]
	query := `SELECT id, guid, mid, mod, usn, tags, flds, sfld, csum, flags, data
		FROM notes WHERE id IN (` + placeholders + `)`

	rows, err := c.db.Query(query, unique...)
	if err != nil {
		return nil, fmt.Errorf("query notes by IDs: %w", err)
	}
	defer func() { _ = rows.Close() }()

	notes := make(map[int64]*goanki.Note, len(unique))
	v18 := c.isV18Plus()
	for rows.Next() {
		note := &goanki.Note{}
		if v18 {
			var sfld, csum interface{}
			if err := rows.Scan(&note.ID, &note.GUID, &note.MID, &note.Mod, &note.USN,
				&note.Tags, &note.Flds, &sfld, &csum, &note.Flags, &note.Data); err != nil {
				return nil, fmt.Errorf("scan note: %w", err)
			}
			note.Sfld = ifaceToStr(sfld)
			note.Csum = ifaceToStr(csum)
		} else {
			if err := rows.Scan(&note.ID, &note.GUID, &note.MID, &note.Mod, &note.USN,
				&note.Tags, &note.Flds, &note.Sfld, &note.Csum, &note.Flags, &note.Data); err != nil {
				return nil, fmt.Errorf("scan note: %w", err)
			}
		}
		notes[note.ID] = note
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate notes: %w", err)
	}
	return notes, nil
}
