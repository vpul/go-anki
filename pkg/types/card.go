package types

// Card represents an Anki card (row in the cards table).
type Card struct {
	ID       int64  `json:"id" db:"id"`
	NID      int64  `json:"nid" db:"nid"` // Note ID
	DID      int64  `json:"did" db:"did"` // Deck ID
	ORD      int    `json:"ord" db:"ord"` // Ordinal within note
	Mod      int64  `json:"mod" db:"mod"` // Modification timestamp
	USN      int    `json:"usn" db:"usn"` // Update sequence number (-1 = not synced)
	Type     CardType `json:"type" db:"type"`
	Queue    CardQueue `json:"queue" db:"queue"`
	Due      int64  `json:"due" db:"due"` // Due date (for review cards) or position (for new cards)
	IVL      int    `json:"ivl" db:"ivl"` // Interval (negative = seconds, positive = days)
	Factor   int    `json:"factor" db:"factor"` // Ease factor (2500 = 250%)
	Reps     int    `json:"reps" db:"reps"` // Number of reviews
	Lapses   int    `json:"lapses" db:"lapses"` // Number of lapses
	Left     int    `json:"left" db:"left"` // Remaining steps
	ODue     int    `json:"odue" db:"odue"` // Original due (for filtered decks)
	ODID     int64  `json:"odid" db:"odid"` // Original deck ID
	Flags    int    `json:"flags" db:"flags"`
	Data     string `json:"data" db:"data"` // JSON blob (unused in modern Anki)

	// FSRS fields (Anki 23.12+ schema)
	FSRSParams  *string `json:"fsrs,omitempty" db:"fsrs"`     // Per-card FSRS parameters (nullable)
	Difficulty  *float64 `json:"difficulty,omitempty" db:"difficulty"` // FSRS difficulty (nullable)
	Stability   *float64 `json:"stability,omitempty" db:"stability"`   // FSRS stability (nullable)

	// Joined fields (populated by queries, not in DB)
	DeckName string `json:"deck_name,omitempty"`
	Question string `json:"question,omitempty"`
	Answer   string `json:"answer,omitempty"`
}

type CardType int

const (
	CardTypeNew         CardType = 0
	CardTypeLearning    CardType = 1
	CardTypeReview      CardType = 2
	CardTypeRelearning   CardType = 3
)

type CardQueue int

const (
	QueueSuspended  CardQueue = -1 // Card is suspended
	QueueSchedBuried CardQueue = -2 // Buried by scheduler
	QueueUserBuried CardQueue = -3 // Buried by user (AKA manually buried)
	// QueueManual is a deprecated alias for QueueUserBuried (-3).
	// Filtered deck status is tracked by ODID, not by queue value.
	QueueManual  CardQueue = -3
	QueueNew      CardQueue = 0
	QueueLearning CardQueue = 1
	QueueReview   CardQueue = 2
	QueueDayLearn   CardQueue = 3 // Learning but shown on review day
)

func (t CardType) String() string {
	switch t {
	case CardTypeNew:
		return "new"
	case CardTypeLearning:
		return "learning"
	case CardTypeReview:
		return "review"
	case CardTypeRelearning:
		return "relearning"
	default:
		return "unknown"
	}
}

func (q CardQueue) String() string {
	switch q {
	case QueueManual:
		return "manual"
	case QueueSuspended:
		return "suspended"
	case QueueSchedBuried:
		return "buried"
	case QueueNew:
		return "new"
	case QueueLearning:
		return "learning"
	case QueueReview:
		return "review"
	case QueueDayLearn:
		return "day_learn"
	default:
		return "unknown"
	}
}