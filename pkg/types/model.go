package types

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Model (Note Type) represents an Anki note type. Stored as JSON in col table.
type Model struct {
	ID      int64            `json:"id"`
	Name    string           `json:"name"`
	Fields  []ModelField     `json:"flds"`
	Templates []ModelTemplate `json:"tmpls"`
	USN     int              `json:"usn"`
	Mod     int64             `json:"mod"`
	SortField int             `json:"sortf"`
	Type    int              `json:"type"` // 0 = standard, 1 = cloze
	DID     int64            `json:"did"` // Deck ID (for add dialog)
	CSS     string           `json:"css"`
	LatexPre  string         `json:"latexPre"`
	LatexPost string         `json:"latexPost"`
	LatexSVG  int            `json:"latexsvg"`
	Req     [][]interface{}  `json:"req"` // Required field specs
}

// ModelField describes a field in a note type.
type ModelField struct {
	Name   string `json:"name"`
	ORD    int    `json:"ord"`
	Sticky bool   `json:"sticky"`
	// Media field rarely used, omit for now
	RTags string `json:"rtags"` // Rarely used
}

// ModelTemplate (Card Template) describes a card template within a model.
type ModelTemplate struct {
	Name      string `json:"name"`
	ORD       int    `json:"ord"`
	QFmt      string `json:"qfmt"`  // Question format template
	AFmt      string `json:"afmt"`  // Answer format template
	DID       int64  `json:"did"`   // Deck override (0 = model's deck)
	BQFmt     string `json:"bqfmt"` // Browser question format
	BAFmt     string `json:"bafmt"` // Browser answer format
}

// ParseModelsJSON parses the models JSON blob from the col table.
func ParseModelsJSON(data []byte) (map[int64]Model, error) {
	var raw map[string]Model
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	models := make(map[int64]Model, len(raw))
	for _, m := range raw {
		models[m.ID] = m
	}
	return models, nil
}

// MarshalModelsJSON serializes models back to the JSON format Anki expects.
func MarshalModelsJSON(models map[int64]Model) ([]byte, error) {
	raw := make(map[string]Model, len(models))
	for _, m := range models {
		raw[fmt.Sprintf("%d", m.ID)] = m
	}
	return json.Marshal(raw)
}

// RenderCard takes a note's fields and a model template, and returns
// the rendered question and answer HTML.
func RenderCard(fields map[string]string, tmpl *ModelTemplate) (question, answer string) {
	// Simple template rendering: replace {{Field}} placeholders
	question = renderTemplate(tmpl.QFmt, fields)
	answer = renderTemplate(tmpl.AFmt, fields)
	return
}

func renderTemplate(tmpl string, fields map[string]string) string {
	result := tmpl
	for name, value := range fields {
		result = strings.ReplaceAll(result, "{{"+name+"}}", value)
	}
	// Unmatched {{...}} placeholders are left verbatim.
	// A full renderer would handle conditionals and cloze deletions,
	// but basic field substitution is sufficient for card display.
	return result
}