package types

import (
	"encoding/json"
	"fmt"
)

// Deck represents an Anki deck. Decks are stored as JSON in the col table.
type Deck struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Mtime   int64  `json:"mod"`
	USN     int    `json:"usn"`
	Dyn     int    `json:"dyn"`
	Conf    int64  `json:"conf"`
	Desc    string `json:"desc"`
	Bury    bool   `json:"bury"`
}

// ParseDecksJSON parses the decks JSON blob from the col table.
func ParseDecksJSON(data []byte) (map[int64]Deck, error) {
	var raw map[string]Deck
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	decks := make(map[int64]Deck, len(raw))
	for _, deck := range raw {
		decks[deck.ID] = deck
	}
	return decks, nil
}

// MarshalDecksJSON serializes decks back to the JSON format Anki expects.
func MarshalDecksJSON(decks map[int64]Deck) ([]byte, error) {
	raw := make(map[string]Deck, len(decks))
	for _, deck := range decks {
		raw[formatDeckID(deck.ID)] = deck
	}
	return json.Marshal(raw)
}

func formatDeckID(id int64) string {
	return fmt.Sprintf("%d", id)
}