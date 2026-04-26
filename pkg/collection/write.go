package collection

import (
	"fmt"
	"time"

	goanki "github.com/vpul/go-anki/pkg/types"
)

// UpdateCard updates a card in the database after answering it.
// Sets mod timestamp and marks usn=-1 (not yet synced).
func (c *Collection) UpdateCard(card goanki.Card) error {
	now := time.Now().Unix()
	_, err := c.db.Exec(`
		UPDATE cards SET
			type = ?, queue = ?, due = ?, ivl = ?, factor = ?,
			reps = ?, lapses = ?, left = ?, mod = ?, usn = -1,
			odue = ?, odid = ?, flags = ?, data = ?
		WHERE id = ?`,
		card.Type, card.Queue, card.Due, card.IVL, card.Factor,
		card.Reps, card.Lapses, card.Left, now, card.ODue, card.ODID,
		card.Flags, card.Data, card.ID,
	)
	if err != nil {
		return fmt.Errorf("update card %d: %w", card.ID, err)
	}
	return nil
}

// InsertReviewLog adds a review entry to the revlog table.
func (c *Collection) InsertReviewLog(review goanki.ReviewLog) error {
	_, err := c.db.Exec(`
		INSERT INTO revlog (id, cid, usn, ease, ivl, last_ivl, factor, time, type)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		review.ID, review.CID, review.USN, review.Ease,
		review.IVL, review.LastIVL, review.Factor, review.Time, review.Type,
	)
	if err != nil {
		return fmt.Errorf("insert review log: %w", err)
	}
	return nil
}

// CreateDeck creates a new deck and returns its ID.
// It updates the decks JSON blob in the col table.
func (c *Collection) CreateDeck(name string) (int64, error) {
	decks, err := c.GetDecks()
	if err != nil {
		return 0, err
	}

	// Check if deck already exists
	for _, d := range decks {
		if d.Name == name {
			return d.ID, nil // Return existing deck ID
		}
	}

	now := time.Now().Unix()
	id := now * 1000 // Anki-style ID: timestamp in milliseconds

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

	return id, nil
}

// AddNote creates a new note and its associated cards.
func (c *Collection) AddNote(input goanki.NewNote) (int64, error) {
	models, err := c.GetModels()
	if err != nil {
		return 0, err
	}

	// Find the model
	var model *goanki.Model
	for _, m := range models {
		if m.Name == input.ModelName {
			model = &m
			break
		}
	}
	if model == nil {
		return 0, fmt.Errorf("model %q not found", input.ModelName)
	}

	// Find the deck
	decks, err := c.GetDecks()
	if err != nil {
		return 0, err
	}
	var deckID int64
	for _, d := range decks {
		if d.Name == input.DeckName {
			deckID = d.ID
			break
		}
	}
	if deckID == 0 {
		return 0, fmt.Errorf("deck %q not found", input.DeckName)
	}

	now := time.Now().Unix()
	noteID := now * 1000

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
	sortIdx := model.SortField
	if sortIdx < len(fieldValues) {
		sfld = fieldValues[sortIdx]
	}
	csum := fieldChecksum(sfld)

	// Build tags string
	tags := ""
	for i, t := range input.Tags {
		if i > 0 {
			tags += " "
		}
		tags += t
	}
	if tags != "" {
		tags = " " + tags + " "
	}

	// Insert note
	_, err = c.db.Exec(`
		INSERT INTO notes (id, guid, mid, mod, usn, tags, flds, sfld, csum, flags, data)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		noteID, generateGUID(), model.ID, now, -1, tags,
		joinFields(fieldValues), sfld, csum, 0, "",
	)
	if err != nil {
		return 0, fmt.Errorf("insert note: %w", err)
	}

	// Create cards for each template
	for _, tmpl := range model.Templates {
		cardID := noteID + int64(tmpl.ORD) // Unique card ID
		_, err = c.db.Exec(`
			INSERT INTO cards (id, nid, did, ord, mod, usn, type, queue, due, ivl,
			                    factor, reps, lapses, left, odue, odid, flags, data)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			cardID, noteID, deckID, tmpl.ORD, now, -1,
			int(goanki.CardTypeNew), int(goanki.QueueNew),
			c.nowAsDays(), 0, 0, 0, 0, 0, 0, 0, 0, "",
		)
		if err != nil {
			return 0, fmt.Errorf("insert card: %w", err)
		}
	}

	return noteID, nil
}

// nowAsDays returns the current time as days since collection creation.
func (c *Collection) nowAsDays() int64 {
	var crt int64
	c.db.QueryRow("SELECT crt FROM col").Scan(&crt)
	if crt == 0 {
		return int64(time.Now().Unix() / 86400)
	}
	return (time.Now().Unix() - crt) / 86400
}

// generateGUID creates a GUID for a new note.
func generateGUID() string {
	// Anki uses a random 10-character string
	const chars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	b := make([]byte, 10)
	for i := range b {
		b[i] = chars[time.Now().UnixNano()%int64(len(chars))]
	}
	return string(b)
}

// fieldChecksum computes the Anki field checksum (first 8 hex chars of CRC32).
func fieldChecksum(field string) string {
	// Anki uses the first 8 characters of the CRC32 hash
	// of the stripped field value, converted to a string
	// Simplified: use a basic hash
	// TODO: implement proper Anki-compatible CRC32 checksum
	return "00000000"
}

// joinFields joins field values with the Anki field separator.
func joinFields(fields []string) string {
	result := ""
	for i, f := range fields {
		if i > 0 {
			result += "\x1f"
		}
		result += f
	}
	return result
}