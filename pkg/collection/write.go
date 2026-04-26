package collection

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"log"
	"strings"
	"time"

	goanki "github.com/vpul/go-anki/pkg/types"
)

// AnswerCard answers a card with the given rating using the FSRS scheduler.
// It updates the card in the database and inserts a review log entry.
// Returns the updated card and review log, or an error.
func (c *Collection) AnswerCard(cardID int64, rating goanki.Rating, scheduler Scheduler) (*goanki.Answer, error) {
	// Fetch the card
	card, err := c.GetCardByID(cardID)
	if err != nil {
		return nil, fmt.Errorf("get card %d: %w", cardID, err)
	}

	// Compute the next state using the scheduler
	answer, err := scheduler.Answer(*card, rating, time.Now())
	if err != nil {
		return nil, fmt.Errorf("schedule answer: %w", err)
	}

	// Start a transaction for atomicity
	tx, err := c.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Update the card
	_, err = tx.Exec(`
		UPDATE cards SET
			type = ?, queue = ?, due = ?, ivl = ?, factor = ?,
			reps = ?, lapses = ?, left = ?, mod = ?, usn = -1,
			odue = ?, odid = ?, flags = ?, data = ?
		WHERE id = ?`,
		answer.Card.Type, answer.Card.Queue, answer.Card.Due, answer.Card.IVL,
		answer.Card.Factor, answer.Card.Reps, answer.Card.Lapses, answer.Card.Left,
		answer.Card.Mod, answer.Card.ODue, answer.Card.ODID, answer.Card.Flags,
		answer.Card.Data, answer.Card.ID,
	)
	if err != nil {
		return nil, fmt.Errorf("update card %d: %w", cardID, err)
	}

	// Update FSRS fields if present (Anki 23.12+ schema)
	if answer.Card.Stability != nil && answer.Card.Difficulty != nil {
		// Try to update FSRS columns — ignore error if they don't exist (older schema)
		_, _ = tx.Exec(`UPDATE cards SET stability = ?, difficulty = ? WHERE id = ?`,
			*answer.Card.Stability, *answer.Card.Difficulty, answer.Card.ID)
	}

	// Insert review log
	_, err = tx.Exec(`
		INSERT INTO revlog (id, cid, usn, ease, ivl, lastIvl, factor, time, type)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		answer.Review.ID, answer.Review.CID, answer.Review.USN, int(answer.Review.Ease),
		answer.Review.IVL, answer.Review.LastIVL, answer.Review.Factor,
		answer.Review.Time, int(answer.Review.Type),
	)
	if err != nil {
		return nil, fmt.Errorf("insert review log: %w", err)
	}

	// Commit transaction
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit transaction: %w", err)
	}

	return answer, nil
}

// UpdateCard updates a card in the database.
// Sets mod timestamp and marks usn=-1 (not yet synced).
func (c *Collection) UpdateCard(card goanki.Card) error {
	_, err := c.db.Exec(`
		UPDATE cards SET
			type = ?, queue = ?, due = ?, ivl = ?, factor = ?,
			reps = ?, lapses = ?, left = ?, mod = ?, usn = -1,
			odue = ?, odid = ?, flags = ?, data = ?
		WHERE id = ?`,
		card.Type, card.Queue, card.Due, card.IVL, card.Factor,
		card.Reps, card.Lapses, card.Left, time.Now().Unix(),
		card.ODue, card.ODID, card.Flags, card.Data, card.ID,
	)
	if err != nil {
		return fmt.Errorf("update card %d: %w", card.ID, err)
	}
	return nil
}

// InsertReviewLog adds a review entry to the revlog table.
func (c *Collection) InsertReviewLog(review goanki.ReviewLog) error {
	_, err := c.db.Exec(`
INSERT INTO revlog (id, cid, usn, ease, ivl, lastIvl, factor, time, type)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
	review.ID, review.CID, review.USN, int(review.Ease),
	review.IVL, review.LastIVL, review.Factor, review.Time, int(review.Type),
	)
	if err != nil {
		return fmt.Errorf("insert review log: %w", err)
	}
	return nil
}

// CreateDeck creates a new deck and returns its ID.
// If a deck with the same name already exists, returns its ID without error.
func (c *Collection) CreateDeck(name string) (int64, error) {
	decks, err := c.GetDecks()
	if err != nil {
		return 0, fmt.Errorf("get decks: %w", err)
	}

	// Check if deck already exists
	for _, d := range decks {
		if d.Name == name {
			return d.ID, nil
		}
	}

	now := time.Now().Unix()
	id := now*1000 + int64(randInt(1000)) // Anki-style: timestamp_ms + random offset

	if c.isV18Plus() {
		// v18+: Insert into separate decks table with protobuf blobs
		// Regular deck common blob: 08011001 (field 1=1, field 2=1)
		// Regular deck kind blob: 0a020801 (field 1=bytes{0801})
		commonBlob := []byte{0x08, 0x01, 0x10, 0x01} // study_mode=1, new_cards_order=1
		kindBlob := []byte{0x0a, 0x02, 0x08, 0x01}   // regular deck

		_, err = c.db.Exec(
			"INSERT INTO decks (id, name, mtime_secs, usn, common, kind) VALUES (?, ?, ?, ?, ?, ?)",
			id, name, now, -1, commonBlob, kindBlob,
		)
		if err != nil {
			return 0, fmt.Errorf("insert deck into decks table: %w", err)
		}

		// Also create deck_config for the new deck
		// Default deck config: minimal protobuf
		configID := id
		configBlob := []byte{} // Minimal config, Anki will fill defaults
		_, err = c.db.Exec(
			"INSERT OR IGNORE INTO deck_config (id, name, mtime_secs, usn, config) VALUES (?, ?, ?, ?, ?)",
			configID, name, now, -1, configBlob,
		)
		if err != nil {
			// Non-fatal: INSERT OR IGNORE already handles duplicates, so any error
			// here indicates a schema mismatch or write failure worth reporting.
			// We don't fail the whole operation since the deck itself was created.
			log.Printf("warning: failed to create deck_config for deck %d: %v", id, err)
		}
	} else {
		// v11: Update JSON blob in col table
		newDeck := goanki.Deck{
			ID:    id,
			Name:  name,
			Mtime: now,
			USN:   -1,
			Dyn:   0, // Regular deck (not filtered)
			Conf:  1, // Default deck config
		}

		decks[id] = newDeck

		// Serialize back to JSON and update col table
		decksJSON, err := goanki.MarshalDecksJSON(decks)
		if err != nil {
			return 0, fmt.Errorf("serialize decks: %w", err)
		}

		_, err = c.db.Exec("UPDATE col SET decks = ?, mod = ?, usn = -1", string(decksJSON), now)
		if err != nil {
			return 0, fmt.Errorf("update decks in col: %w", err)
		}
	}

	return id, nil
}

// AddNote creates a new note and its associated cards.
// Returns the note ID on success.
func (c *Collection) AddNote(input goanki.NewNote) (int64, error) {
	models, err := c.GetModels()
	if err != nil {
		return 0, fmt.Errorf("get models: %w", err)
	}

	// Find the model
	var model *goanki.Model
	for _, m := range models {
		if m.Name == input.ModelName {
			m := m // capture loop variable
			model = &m
			break
		}
	}
	if model == nil {
		return 0, fmt.Errorf("model %q not found", input.ModelName)
	}

	// Find or create the deck
	deckID, err := c.CreateDeck(input.DeckName)
	if err != nil {
		return 0, fmt.Errorf("create/get deck: %w", err)
	}

	// Start a transaction for atomicity
	tx, err := c.db.Begin()
	if err != nil {
		return 0, fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	now := time.Now().Unix()
	noteID := now*1000 + int64(randInt(1000))

	// Build fields string (separated by \x1f)
	fieldValues := make([]string, len(model.Fields))
	for i, f := range model.Fields {
		if v, ok := input.Fields[f.Name]; ok {
			fieldValues[i] = v
		} else {
			fieldValues[i] = ""
		}
	}

	// Calculate sort field and checksum
	sfld := ""
	if model.SortField < len(fieldValues) {
		sfld = fieldValues[model.SortField]
	}
	csum := fieldChecksum(sfld)

	// Build tags string (space-separated, wrapped in spaces)
	tags := ""
	if len(input.Tags) > 0 {
		tags = " " + strings.Join(input.Tags, " ") + " "
	}

	// Insert note
	guid := generateGUID()
	if c.isV18Plus() {
		// In v18+, sfld and csum are INTEGER columns. We store the CRC32 checksum
		// as an integer (which is what Anki does in v18+).
		csumInt := int64(crc32.ChecksumIEEE([]byte(strings.TrimSpace(sfld))))
		_, err = tx.Exec(`
			INSERT INTO notes (id, guid, mid, mod, usn, tags, flds, sfld, csum, flags, data)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			noteID, guid, model.ID, now, -1, tags,
			strings.Join(fieldValues, "\x1f"), sfld, csumInt, 0, "",
		)
	} else {
		_, err = tx.Exec(`
			INSERT INTO notes (id, guid, mid, mod, usn, tags, flds, sfld, csum, flags, data)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			noteID, guid, model.ID, now, -1, tags,
			strings.Join(fieldValues, "\x1f"), sfld, csum, 0, "",
		)
	}
	if err != nil {
		return 0, fmt.Errorf("insert note: %w", err)
	}

	// Create cards for each template
	for _, tmpl := range model.Templates {
		cardID := noteID + int64(tmpl.ORD) + int64(randInt(100))
		_, err = tx.Exec(`
			INSERT INTO cards (id, nid, did, ord, mod, usn, type, queue, due, ivl,
			                    factor, reps, lapses, left, odue, odid, flags, data)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			cardID, noteID, deckID, tmpl.ORD, now, -1,
			int(goanki.CardTypeNew), int(goanki.QueueNew),
			dayOffsetSinceCreation(c), 0, 0, 0, 0, 0, 0, 0, 0, "",
		)
		if err != nil {
			return 0, fmt.Errorf("insert card: %w", err)
		}
	}

	// Commit transaction
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit transaction: %w", err)
	}

	return noteID, nil
}

// dayOffsetSinceCreation returns the number of days since the collection was created.
func dayOffsetSinceCreation(c *Collection) int64 {
	var crt int64
	_ = c.db.QueryRow("SELECT crt FROM col").Scan(&crt)
	if crt == 0 {
		return time.Now().Unix() / 86400
	}
	return (time.Now().Unix() - crt) / 86400
}

// generateGUID creates a cryptographically random 10-character GUID for a note.
// Uses rejection sampling to avoid modulo bias (since 256 is not evenly
// divisible by 62, we reject bytes >= 248 and only accept bytes in [0, 248)
// which gives uniform distribution over the 62-character alphabet).
func generateGUID() string {
	const chars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	const maxByte = byte(248) // 62 * 4 = 248, giving uniform distribution
	result := make([]byte, 10)
	for i := 0; i < len(result); {
		b := make([]byte, 1)
		_, _ = rand.Read(b)
		if b[0] < maxByte {
			result[i] = chars[b[0]%byte(len(chars))]
			i++
		}
	}
	return string(result)
}

// fieldChecksum computes the Anki field checksum.
// Anki uses the first 8 hex characters of CRC32 of the stripped field value.
func fieldChecksum(field string) string {
	// Strip HTML tags and whitespace for checksum
	stripped := strings.TrimSpace(field)
	checksum := crc32.ChecksumIEEE([]byte(stripped))
	return fmt.Sprintf("%08x", checksum)
}

// randInt generates a cryptographically random non-negative integer.
func randInt(max int) int {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return int(binary.BigEndian.Uint32(b) % uint32(max))
}

// Scheduler interface for the AnswerCard method.
// This allows different scheduling implementations.
type Scheduler interface {
	Answer(card goanki.Card, rating goanki.Rating, now time.Time) (*goanki.Answer, error)
}