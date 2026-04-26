package types

import "time"

// Rating represents a review answer choice (maps to Anki's ease values).
type Rating int

const (
	RatingAgain Rating = 1 // Forgot / incorrect
	RatingHard  Rating = 2 // Difficult
	RatingGood  Rating = 3 // Correct
	RatingEasy  Rating = 4 // Very easy
)

func (r Rating) String() string {
	switch r {
	case RatingAgain:
		return "again"
	case RatingHard:
		return "hard"
	case RatingGood:
		return "good"
	case RatingEasy:
		return "easy"
	default:
		return "unknown"
	}
}

// ParseRating parses a rating from a string.
func ParseRating(s string) Rating {
	switch s {
	case "again":
		return RatingAgain
	case "hard":
		return RatingHard
	case "good":
		return RatingGood
	case "easy":
		return RatingEasy
	default:
		return RatingGood
	}
}

// ReviewLog represents a review entry (row in the revlog table).
type ReviewLog struct {
	ID      int64     `json:"id" db:"id"`       // Timestamp in ms (also the primary key)
	CID     int64     `json:"cid" db:"cid"`     // Card ID
	USN     int       `json:"usn" db:"usn"`     // Update sequence number
	Ease    Rating    `json:"ease" db:"ease"`   // 1=Again, 2=Hard, 3=Good, 4=Easy
	IVL     int       `json:"ivl" db:"ivl"`     // Interval
	LastIVL int       `json:"last_ivl" db:"last_ivl"` // Previous interval
	Factor  int       `json:"factor" db:"factor"`    // Ease factor
	Time    int       `json:"time" db:"time"`   // Time taken in ms
	Type    CardType  `json:"type" db:"type"`   // Card type at review time
}

// ReviewLogType is the type of review (for new/learning/review/relearning cards).
const (
	ReviewLogTypeNew        = 0
	ReviewLogTypeLearning   = 1
	ReviewLogTypeReview     = 2
	ReviewLogTypeRelearning = 3
)

// Answer represents the result of answering a card.
type Answer struct {
	Card   Card      `json:"card"`    // Updated card state after answering
	Review ReviewLog `json:"review"` // Review log entry to insert
}

// DueCardsFilter is used to filter due cards queries.
type DueCardsFilter struct {
	DeckName string `json:"deck_name,omitempty"` // Filter by deck name
	Limit    int    `json:"limit,omitempty"`     // Max cards to return (0 = no limit)
}

// CollectionInfo holds metadata about an Anki collection.
type CollectionInfo struct {
	ID      int64     `json:"id"`
	Created time.Time `json:"created"`
	Modified time.Time `json:"modified"`
	SchemaModified time.Time `json:"schema_modified"`
	Version int       `json:"version"` // Schema version
	Dirty   bool      `json:"dirty"`
	USN     int       `json:"usn"`
}

// Stats holds collection statistics.
type Stats struct {
	TotalCards  int `json:"total_cards"`
	TotalNotes  int `json:"total_notes"`
	DueCards    int `json:"due_cards"`
	NewCards    int `json:"new_cards"`
	LearningCards int `json:"learning_cards"`
	ReviewCards int `json:"review_cards"`
	TotalDecks  int `json:"total_decks"`
	TotalModels int `json:"total_models"`
	TotalReviews int `json:"total_reviews"` // From revlog
}

// SyncConfig holds AnkiWeb sync credentials.
type SyncConfig struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// SyncMeta holds metadata returned by AnkiWeb sync handshake.
type SyncMeta struct {
	Modified    int64 `json:"mod"`     // Server modification timestamp
	SchemaMod   int64 `json:"scm"`     // Schema modification count
	USN         int   `json:"usn"`     // Server update sequence number
	MediaUSN    int   `json:"musn"`    // Media update sequence number
	Timestamp   int64 `json:"ts"`      // Server timestamp
}