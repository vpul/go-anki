package collection

import (
	"fmt"
	"log"
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
	//
	// Note: This registration is global to the process. If the app ever opens
	// non-Anki SQLite databases, they'll also see the "unicase" collation, which
	// is harmless.
	if err := modernc.RegisterCollationUtf8("unicase", func(a, b string) int {
		al := strings.ToLower(a)
		bl := strings.ToLower(b)
		if al < bl {
			return -1
		}
		if al > bl {
			return 1
		}
		return 0
	}); err != nil {
		log.Printf("warning: failed to register unicase collation: %v", err)
	}
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

// getDecksV18 reads decks from the separate decks table (v18+ schema).
func (c *Collection) getDecksV18() (map[int64]goanki.Deck, error) {
	decks := make(map[int64]goanki.Deck)
	// Single query: fetch id, name, mtime, usn, and kind (for filtered deck detection)
	rows, err := c.db.Query("SELECT id, name, mtime_secs, usn, kind FROM decks")
	if err != nil {
		return nil, fmt.Errorf("query decks table: %w", err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var d goanki.Deck
		var mtime int64
		var kind []byte
		if err := rows.Scan(&d.ID, &d.Name, &mtime, &d.USN, &kind); err != nil {
			return nil, fmt.Errorf("scan deck: %w", err)
		}
		d.Mtime = mtime
		// Default values for regular deck
		d.Conf = 1
		d.Desc = ""
		d.Bury = true
		// Determine if this is a filtered deck from the kind protobuf blob
		d.Dyn = isFilteredDeckKind(kind)
		decks[d.ID] = d
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate decks: %w", err)
	}

	return decks, nil
}

// isFilteredDeckKind decodes the protobuf oneof tag from a deck's kind blob.
// In Anki's protobuf schema, DeckKind is a oneof:
//   - field 1 (tag 0x0a) = NormalDeck → returns 0
//   - field 2 (tag 0x12) = FilteredDeck → returns 1
func isFilteredDeckKind(kind []byte) int {
	if len(kind) == 0 {
		return 0
	}
	tag, n := decodeVarint(kind)
	if n <= 0 {
		return 0
	}
	fieldNum := tag >> 3
	if fieldNum == 2 {
		return 1 // Filtered deck
	}
	return 0 // Normal deck
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
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate notetypes: %w", err)
	}

	// For each model, query fields, templates, and notetype config
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
		if err := fieldRows.Err(); err != nil {
			_ = fieldRows.Close()
			continue
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
		if err := tmplRows.Err(); err != nil {
			_ = tmplRows.Close()
			continue
		}
		_ = tmplRows.Close()

		// Parse notetype config for CSS, sort field, and type info
		var ntConfig []byte
		err = c.db.QueryRow("SELECT config FROM notetypes WHERE id = ?", mid).Scan(&ntConfig)
		if err == nil && len(ntConfig) > 0 {
			models[mid] = parseNotetypeConfig(ntConfig, models[mid])
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
//   - field 1: qfmt (string)
//   - field 2: afmt (string)
func parseTemplateConfig(data []byte) (qfmt, afmt string) {
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
			_, n = decodeVarint(data)
			if n <= 0 {
				break
			}
			data = data[n:]
		case 1: // fixed64
			if len(data) < 8 {
				return qfmt, afmt
			}
			data = data[8:]
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
		case 5: // fixed32
			if len(data) < 4 {
				return qfmt, afmt
			}
			data = data[4:]
		default:
			// Unknown wire type — cannot advance safely, abort
			return qfmt, afmt
		}
	}
	return qfmt, afmt
}

// parseNotetypeConfig decodes a v18 notetype config protobuf blob and returns
// the model with CSS, sort field, and type info populated.
// The protobuf structure is:
//   - field 1: sort_field (varint)
//   - field 2: is_cloze (bool varint)
//   - field 3: css (string)
//   - field 5: latex_pre (string)
//   - field 6: latex_post (string)
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
		case 1: // fixed64
			if len(data) < 8 {
				return m
			}
			data = data[8:]
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
		case 5: // fixed32
			if len(data) < 4 {
				return m
			}
			data = data[4:]
		default:
			// Unknown wire type — cannot advance safely, abort
			return m
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