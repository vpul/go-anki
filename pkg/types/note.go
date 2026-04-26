package types

import "strings"

// Note represents an Anki note (row in the notes table).
type Note struct {
	ID    int64  `json:"id" db:"id"`
	GUID  string `json:"guid" db:"guid"`
	MID   int64  `json:"mid" db:"mid"` // Model (note type) ID
	Mod   int64  `json:"mod" db:"mod"` // Modification timestamp
	USN   int    `json:"usn" db:"usn"` // Update sequence number
	Tags  string `json:"tags" db:"tags"` // Space-separated tags
	Flds  string `json:"flds" db:"flds"` // Fields separated by \x1f
	Sfld  string `json:"sfld" db:"sfld"` // Sort field (first field by default)
	Csum  string `json:"csum" db:"csum"` // Checksum of sort field
	Flags int    `json:"flags" db:"flags"`
	Data  string `json:"data" db:"data"` // Unused in modern Anki

	// Joined fields
	ModelName string `json:"model_name,omitempty"`
	DeckName  string `json:"deck_name,omitempty"`
}

// FieldSeparator is the character Anki uses to separate note fields.
const FieldSeparator = "\x1f"

// ParseFields splits theFields string into individual field values.
func (n *Note) ParseFields() []string {
	return strings.Split(n.Flds, FieldSeparator)
}

// FieldsMap returns note fields as a map (field names require model info).
func (n *Note) FieldsAsMap(model *Model) map[string]string {
	fields := n.ParseFields()
	result := make(map[string]string, len(model.Fields))
	for i, val := range fields {
		if i < len(model.Fields) {
			result[model.Fields[i].Name] = val
		}
	}
	return result
}

// NewNote is the input type for creating a new note.
type NewNote struct {
	DeckName  string            `json:"deck_name"`
	ModelName string            `json:"model_name"`
	Fields    map[string]string `json:"fields"`
	Tags      []string          `json:"tags"`
	AllowDup  bool              `json:"allow_dup,omitempty"`
}

// NewNoteMinimal is a simpler input type using field values by position.
type NewNoteMinimal struct {
	DeckName string   `json:"deck_name"`
	ModelName string  `json:"model_name"`
	Values    []string `json:"values"`
	Tags      []string `json:"tags"`
}