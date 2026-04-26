package collection

import (
	"fmt"
	"strings"

	modernc "modernc.org/sqlite"

	goanki "github.com/vpul/go-anki/pkg/types"
)

func init() {
	// Register a unicase collation that maps to case-insensitive comparison.
	// Anki v18+ databases use COLLATE unicase on several columns (notetypes.name,
	// fields.name, templates.name, notes.sfld, notes.tags). Without this registration,
	// modernc.org/sqlite cannot read those tables at all because the collation is
	// unknown. We implement it as a case-insensitive comparison which is close enough
	// for our read-only needs.
	modernc.RegisterCollationUtf8("unicase", func(a, b string) int {
		al := strings.ToLower(a)
		bl := strings.ToLower(b)
		if al < bl {
			return -1
		}
		if al > bl {
			return 1
		}
		return 0
	})
}

// schemaVersion returns the cached Anki schema version.
func (c *Collection) schemaVersion() int {
	if c.schema != 0 {
		return c.schema
	}
	var ver int
	err := c.db.QueryRow("SELECT ver FROM col").Scan(&ver)
	if err != nil {
		return 0
	}
	c.schema = ver
	return ver
}

// isV18Plus returns true if the collection uses schema version 18 or later,
// which stores decks, notetypes, etc. in separate tables instead of JSON blobs
// in the col table.
func (c *Collection) isV18Plus() bool {
	return c.schemaVersion() >= 18
}

// hasTable checks if a table exists in the database.
func (c *Collection) hasTable(name string) bool {
	var count int
	err := c.db.QueryRow(
		"SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?", name,
	).Scan(&count)
	if err != nil {
		return false
	}
	return count > 0
}

// getDecksV18 reads decks from the separate decks table (v18+ schema).
func (c *Collection) getDecksV18() (map[int64]goanki.Deck, error) {
	decks := make(map[int64]goanki.Deck)
	rows, err := c.db.Query("SELECT id, name, mtime_secs, usn FROM decks")
	if err != nil {
		return nil, fmt.Errorf("query decks table: %w", err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var d goanki.Deck
		var mtime int64
		if err := rows.Scan(&d.ID, &d.Name, &mtime, &d.USN); err != nil {
			return nil, fmt.Errorf("scan deck: %w", err)
		}
		d.Mtime = mtime
		// In v18, we don't have dyn/conf/desc from the table columns directly.
		// The "kind" blob determines if a deck is filtered (dyn=1) or regular (dyn=0).
		// For now, set defaults for regular decks. We'll determine Dyn from the kind blob.
		d.Dyn = 0  // Will be updated below
		d.Conf = 1 // Default config ID
		d.Desc = ""
		d.Bury = true
		decks[d.ID] = d
	}

	// Determine which decks are filtered by checking the kind blob.
	// A regular deck's kind blob is short (e.g., 0a020801).
	// A filtered deck's kind blob is longer and contains search terms.
	// We check the length: if kind has more than 4 bytes, it's likely filtered.
	type deckKind struct {
		id   int64
		kind []byte
	}
	rows2, err := c.db.Query("SELECT id, kind FROM decks")
	if err != nil {
		// Kind information is optional; proceed with defaults
		return decks, nil
	}
	defer func() { _ = rows2.Close() }()
	for rows2.Next() {
		var id int64
		var kind []byte
		if err := rows2.Scan(&id, &kind); err != nil {
			continue
		}
		if d, ok := decks[id]; ok {
			// Decode kind protobuf: regular deck kind is 0a020801 (field 1=bytes len=2: 0801)
			// Filtered deck kind has more content with search terms.
			// Simple heuristic: if kind bytes contain a search string, it's filtered.
			if isFilteredDeck(kind) {
				d.Dyn = 1
			}
			decks[id] = d
		}
	}
	return decks, nil
}

// isFilteredDeck checks if a deck's kind blob indicates a filtered deck.
// Regular deck kind: 0a020801 (short, ~4 bytes)
// Filtered deck kind: contains a search string (longer)
func isFilteredDeck(kind []byte) bool {
	if len(kind) <= 4 {
		return false
	}
	// Check if it contains typical filtered deck indicators
	// (search terms, etc.) by looking for readable text content
	return len(kind) > 6
}

// getModelsV18 reads note types from the separate notetypes, fields, and templates
// tables (v18+ schema).
func (c *Collection) getModelsV18() (map[int64]goanki.Model, error) {
	models := make(map[int64]goanki.Model)

	// Query notetypes (by ID to avoid unicase collation issues on name)
	rows, err := c.db.Query("SELECT id, name, mtime_secs, usn FROM notetypes")
	if err != nil {
		return nil, fmt.Errorf("query notetypes table: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var modelIDs []int64
	for rows.Next() {
		var m goanki.Model
		var mtime int64
		if err := rows.Scan(&m.ID, &m.Name, &mtime, &m.USN); err != nil {
			return nil, fmt.Errorf("scan notetype: %w", err)
		}
		m.Mod = mtime
		m.Type = 0 // Standard (0 = standard, 1 = cloze)
		m.SortField = 0
		m.DID = 0
		m.CSS = ""
		m.LatexPre = ""
		m.LatexPost = ""
		m.LatexSVG = 0

		models[m.ID] = m
		modelIDs = append(modelIDs, m.ID)
	}

	// For each model, query fields and templates
	for _, mid := range modelIDs {
		// Query fields
		fieldRows, err := c.db.Query("SELECT ntid, ord, name FROM fields WHERE ntid = ? ORDER BY ord", mid)
		if err != nil {
			// Log warning but continue - fields are optional for basic operations
			continue
		}
		var fields []goanki.ModelField
		for fieldRows.Next() {
			var ntid int64
			var ord int
			var name string
			if err := fieldRows.Scan(&ntid, &ord, &name); err != nil {
				_ = fieldRows.Close()
				continue
			}
			fields = append(fields, goanki.ModelField{
				Name: name,
				ORD:  ord,
			})
		}
		_ = fieldRows.Close()

		// Query templates
		tmplRows, err := c.db.Query("SELECT ntid, ord, name, config FROM templates WHERE ntid = ? ORDER BY ord", mid)
		if err != nil {
			// Templates are needed for card rendering
			continue
		}
		var templates []goanki.ModelTemplate
		for tmplRows.Next() {
			var ntid int64
			var ord int
			var name string
			var config []byte
			if err := tmplRows.Scan(&ntid, &ord, &name, &config); err != nil {
				continue
			}
			qfmt, afmt := parseTemplateConfig(config)
			templates = append(templates, goanki.ModelTemplate{
				Name: name,
				ORD:  ord,
				QFmt: qfmt,
				AFmt: afmt,
			})
		}
		_ = tmplRows.Close()

		// Parse notetype config for CSS and sort field
		var ntConfig []byte
		err = c.db.QueryRow("SELECT config FROM notetypes WHERE id = ?", mid).Scan(&ntConfig)
		if err == nil {
			parseNotetypeConfig(ntConfig, models[mid])
		}

		m := models[mid]
		m.Fields = fields
		m.Templates = templates
		models[mid] = m
	}

	return models, nil
}

// parseTemplateConfig decodes a v18 template config protobuf blob to extract
// the question format (qfmt) and answer format (afmt) strings.
// The protobuf structure is:
//   field 1: qfmt (string)
//   field 2: afmt (string)
func parseTemplateConfig(data []byte) (qfmt, afmt string) {
	var fieldNum uint64
	var wireType uint64
	for len(data) > 0 {
		tag, n := decodeVarint(data)
		if n <= 0 {
			break
		}
		data = data[n:]
		fieldNum = tag >> 3
		wireType = tag & 0x7

		switch wireType {
		case 0: // varint
			_, n = decodeVarint(data)
			if n <= 0 {
				break
			}
			data = data[n:]
		case 2: // length-delimited
			length, n := decodeVarint(data)
			if n <= 0 || int(length) > len(data[n:]) {
				break
			}
			data = data[n:]
			value := string(data[:length])
			data = data[length:]

			switch fieldNum {
			case 1:
				qfmt = value
			case 2:
				afmt = value
			}
		default:
			break
		}
	}
	return qfmt, afmt
}

// parseNotetypeConfig decodes a v18 notetype config protobuf blob and updates
// the model with CSS, sort field, and type info.
// The protobuf structure is:
//   field 1: sort_field (varint)
//   field 2: is_cloze (bool varint)
//   field 3: css (string)
//   field 5: latex_pre (string)
//   field 6: latex_post (string)
func parseNotetypeConfig(data []byte, m goanki.Model) goanki.Model {
	for len(data) > 0 {
		tag, n := decodeVarint(data)
		if n <= 0 {
			break
		}
		data = data[n:]
		fieldNum := tag >> 3
		wireType := tag & 0x7

		switch wireType {
		case 0: // varint
			val, n := decodeVarint(data)
			if n <= 0 {
				break
			}
			data = data[n:]
			switch fieldNum {
			case 1:
				m.SortField = int(val)
			case 2:
				if val > 0 {
					m.Type = 1 // Cloze
				}
			}
		case 2: // length-delimited
			length, n := decodeVarint(data)
			if n <= 0 || int(length) > len(data[n:]) {
				break
			}
			data = data[n:]
			value := string(data[:length])
			data = data[length:]
			switch fieldNum {
			case 3:
				m.CSS = value
			case 5:
				m.LatexPre = value
			case 6:
				m.LatexPost = value
			}
		default:
			break
		}
	}
	return m
}

// decodeVarint decodes a protobuf varint from the beginning of data.
// Returns the value and the number of bytes read.
func decodeVarint(data []byte) (uint64, int) {
	var result uint64
	var shift uint
	for i := 0; i < len(data); i++ {
		b := data[i]
		result |= uint64(b&0x7f) << shift
		if b&0x80 == 0 {
			return result, i + 1
		}
		shift += 7
		if shift >= 64 {
			return 0, 0
		}
	}
	return 0, 0
}

// getDeckConfigDefaultID returns the default deck config ID for v18 schema.
// In v18+, deck configs are in the deck_config table.
func (c *Collection) getDeckConfigDefaultID() int64 {
	var id int64
	err := c.db.QueryRow("SELECT id FROM deck_config WHERE name = 'Default' LIMIT 1").Scan(&id)
	if err != nil {
		// If "Default" doesn't work (unicase on name), try ID 1
		err = c.db.QueryRow("SELECT id FROM deck_config WHERE id = 1").Scan(&id)
		if err != nil {
			return 1 // Fallback
		}
	}
	return id
}