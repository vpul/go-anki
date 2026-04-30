package types

import "time"

// Version is the go-anki version string.
const Version = "go-anki/2.0.0"

// ValidationError is a sentinel error type for client-side validation errors.
// Use errors.As(err, &ve) to distinguish 400 (validation) from 500 (internal).
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return e.Msg }

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
	LastIVL int       `json:"last_ivl" db:"lastIvl"` // Previous interval
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

// MaxCardsPerQuery is the upper bound on cards returned by a single due-cards query.
// The value 1000 determines how many ? placeholders appear in the IN clause.
// Safe with modernc.org/sqlite (bundles SQLite 3.46+, SQLITE_MAX_VARIABLE_NUMBER=32766).
// If the driver is ever swapped, verify the build's SQLITE_MAX_VARIABLE_NUMBER ≥ 1000.
const MaxCardsPerQuery = 1000

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

// SyncConfig holds AnkiWeb sync credentials and optional server URL.
type SyncConfig struct {
	Username string `json:"username"`
	Password string `json:"-"`
	SyncURL  string `json:"sync_url,omitempty"` // Optional: custom AnkiWeb sync URL (for testing/local sync servers)
}

// SyncMeta holds metadata returned by AnkiWeb sync handshake.
type SyncMeta struct {
	Modified    int64 `json:"mod"`     // Server modification timestamp
	SchemaMod   int64 `json:"scm"`     // Schema modification count
	USN         int   `json:"usn"`     // Server update sequence number
	MediaUSN    int   `json:"musn"`    // Media update sequence number
	Timestamp   int64 `json:"ts"`      // Server timestamp
}

// Grave represents a deletion record in the graves table.
type Grave struct {
	OID  int64 `json:"oid"`  // Object ID
	Type int8  `json:"type"` // Object type (0=card, 1=note, 2=deck)
	USN  int   `json:"usn"`  // Update sequence number
}

// SyncState represents the sync state for delta negotiation.
type SyncState struct {
	SCM     int64 `json:"scm"`     // Server collection modification time (crt from col)
	USN     int   `json:"usn"`     // Current update sequence number
	HostNum int   `json:"hostNum"` // Host number (from server)
}

// SyncDelta holds changes for a sync round-trip.
type SyncDelta struct {
	Cards  []Card  `json:"cards,omitempty"`
	Notes  []Note  `json:"notes,omitempty"`
	Decks  []Deck  `json:"decks,omitempty"`
	Graves []Grave `json:"graves,omitempty"`
	USN    int     `json:"usn"`   // After applying this delta
	More   bool    `json:"more"`  // True if there are more changes
}