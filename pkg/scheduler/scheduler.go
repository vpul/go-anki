package scheduler

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"time"

	goanki "github.com/vpul/go-anki/pkg/types"
	fsrs "github.com/open-spaced-repetition/go-fsrs"
)

// FSRSScheduler wraps go-fsrs to provide Anki-compatible scheduling.
type FSRSScheduler struct {
	params fsrs.Parameters
}

// NewFSRSScheduler creates a new FSRS scheduler with default parameters.
func NewFSRSScheduler() *FSRSScheduler {
	return &FSRSScheduler{
		params: fsrs.DefaultParam(),
	}
}

// NewFSRSSchedulerWithParams creates a scheduler with custom FSRS parameters.
// This allows per-deck tuning using parameters exported from Anki's FSRS optimizer.
func NewFSRSSchedulerWithParams(params fsrs.Parameters) *FSRSScheduler {
	return &FSRSScheduler{
		params: params,
	}
}

// Answer computes the next card state after a review and returns
// both the updated card and the review log entry.
func (s *FSRSScheduler) Answer(card goanki.Card, rating goanki.Rating, now time.Time) (*goanki.Answer, error) {
	if card.ID == 0 {
		return nil, fmt.Errorf("card ID must not be zero")
	}

	// Convert Anki card to go-fsrs Card
	fsrsCard := ankiCardToFSRS(card, now)

	// Get FSRS rating
	fsrsRating := ankiRatingToFSRS(rating)

	// Compute scheduling for all ratings
	scheduling := s.params.Repeat(fsrsCard, now)

	// Get the result for the chosen rating
	result, ok := scheduling[fsrsRating]
	if !ok {
		return nil, fmt.Errorf("rating %v not found in scheduling result", fsrsRating)
	}

	// Convert back to Anki card
	updatedCard := fsrsCardToAnki(result.Card, card, now)

	// Calculate interval
	interval := int(result.Card.ScheduledDays)
	lastInterval := card.IVL
	if interval == 0 {
		// Learning/relearning step
		interval = -1 // Anki convention
	}

	// Create review log entry
	review := goanki.ReviewLog{
		ID:      now.UnixMilli()*1000 + int64(randIntn(1000)), // millisecond ID + random to avoid sub-ms collisions
		CID:     card.ID,
		USN:     -1, // Not yet synced
		Ease:    rating,
		IVL:     interval,
		LastIVL: lastInterval,
		Factor:  updatedCard.Factor,
		Time:    0, // TODO: Track actual review time in milliseconds
		Type:    card.Type,
	}

	return &goanki.Answer{
		Card:   updatedCard,
		Review: review,
	}, nil
}

// ankiCardToFSRS converts an Anki card to a go-fsrs Card.
func ankiCardToFSRS(card goanki.Card, now time.Time) fsrs.Card {
	fsrsCard := fsrs.NewCard()

	// Map state
	fsrsCard.State = ankiCardTypeToFSRS(card.Type)

	// Set stability and difficulty if available (modern Anki 23.12+)
	if card.Stability != nil {
		fsrsCard.Stability = *card.Stability
	}
	if card.Difficulty != nil {
		fsrsCard.Difficulty = *card.Difficulty
	}

	// Set review counts
	fsrsCard.Reps = uint64(card.Reps)
	fsrsCard.Lapses = uint64(card.Lapses)

	// Set last review time
	if card.Mod > 0 {
		fsrsCard.LastReview = time.Unix(card.Mod, 0)
	} else {
		fsrsCard.LastReview = now
	}

	// Set due date (for review cards, convert from day offset)
	if card.Type == goanki.CardTypeReview || card.Type == goanki.CardTypeRelearning {
		fsrsCard.ScheduledDays = uint64(card.IVL)
		if card.Mod > 0 {
			elapsedHours := now.Sub(time.Unix(card.Mod, 0)).Hours()
			fsrsCard.ElapsedDays = uint64(elapsedHours / 24)
		}
	}

	// Set due time for learning cards
	if card.Type == goanki.CardTypeLearning {
		fsrsCard.Due = now // Learning cards are due now
	}

	return fsrsCard
}

// fsrsCardToAnki converts a go-fsrs Card back to an Anki card.
func fsrsCardToAnki(fsrsCard fsrs.Card, original goanki.Card, now time.Time) goanki.Card {
	result := original

	// Copy FSRS fields
	stability := fsrsCard.Stability
	difficulty := fsrsCard.Difficulty
	result.Stability = &stability
	result.Difficulty = &difficulty
	result.Reps = int(fsrsCard.Reps)
	result.Lapses = int(fsrsCard.Lapses)
	result.Mod = now.Unix()
	result.USN = -1 // Not yet synced

	// Convert FSRS state to Anki card type
	result.Type = fsrsStateToAnkiCardType(fsrsCard.State)
	result.Queue = fsrsStateToAnkiQueue(fsrsCard.State)

	// Set interval
	result.IVL = int(fsrsCard.ScheduledDays)

	// Set ease factor (Anki uses factor * 10, e.g., 2500 = 250%)
	// FSRS difficulty inversely maps to ease
	result.Factor = difficultyToFactor(fsrsCard.Difficulty)

	// Set due date based on state
	switch fsrsCard.State {
	case fsrs.New:
		result.Due = 0 // Due immediately (relative ordering)
	case fsrs.Learning, fsrs.Relearning:
		result.Due = now.Unix() // Due now (seconds since epoch)
		result.Left = 1         // One learning step remaining
	case fsrs.Review:
		// Due is stored as days since collection creation (day offset).
		// FSRS gives us ScheduledDays from now, so we calculate the day
		// offset using the current time plus the interval.
		// The collection layer will correct this if needed (see AnswerCard).
		result.Due = -1 // Sentinel: collection layer must compute day offset
	}

	return result
}

// difficultyToFactor converts FSRS difficulty (0-10) to Anki ease factor (1300-5000).
// Higher difficulty = lower ease factor.
func difficultyToFactor(difficulty float64) int {
	// Anki default is 2500 (250%). Map FSRS difficulty inversely.
	// Difficulty 5 (average) → factor 2500
	// Difficulty 1 (easy) → factor 3500
	// Difficulty 10 (very hard) → factor 1500
	factor := 4000 - int(difficulty*200)
	if factor < 1300 {
		factor = 1300
	}
	if factor > 5000 {
		factor = 5000
	}
	return factor
}

func ankiCardTypeToFSRS(cardType goanki.CardType) fsrs.State {
	switch cardType {
	case goanki.CardTypeNew:
		return fsrs.New
	case goanki.CardTypeLearning:
		return fsrs.Learning
	case goanki.CardTypeReview:
		return fsrs.Review
	case goanki.CardTypeRelearning:
		return fsrs.Relearning
	default:
		return fsrs.New
	}
}

func fsrsStateToAnkiCardType(state fsrs.State) goanki.CardType {
	switch state {
	case fsrs.New:
		return goanki.CardTypeNew
	case fsrs.Learning:
		return goanki.CardTypeLearning
	case fsrs.Review:
		return goanki.CardTypeReview
	case fsrs.Relearning:
		return goanki.CardTypeRelearning
	default:
		return goanki.CardTypeNew
	}
}

func fsrsStateToAnkiQueue(state fsrs.State) goanki.CardQueue {
	switch state {
	case fsrs.New:
		return goanki.QueueNew
	case fsrs.Learning:
		return goanki.QueueLearning
	case fsrs.Review:
		return goanki.CardQueue(2) // QueueReview
	case fsrs.Relearning:
		return goanki.CardQueue(1) // QueueLearning (Anki uses learning queue for relearning too)
	default:
		return goanki.QueueNew
	}
}

func ankiRatingToFSRS(rating goanki.Rating) fsrs.Rating {
	switch rating {
	case goanki.RatingAgain:
		return fsrs.Again
	case goanki.RatingHard:
		return fsrs.Hard
	case goanki.RatingGood:
		return fsrs.Good
	case goanki.RatingEasy:
		return fsrs.Easy
	default:
		return fsrs.Good
	}
}

// randIntn generates a cryptographically random integer in [0, n).
// Uses rejection sampling to avoid modulo bias.
func randIntn(n int) int {
	if n <= 0 {
		return 0
	}
	threshold := uint32(0xFFFFFFFF-uint32(n)) + 1
	threshold -= threshold % uint32(n)
	b := make([]byte, 4)
	for {
		if _, err := rand.Read(b); err != nil {
			panic(fmt.Sprintf("crypto/rand.Read failed: %v", err))
		}
		v := binary.BigEndian.Uint32(b)
		if v < threshold {
			return int(v % uint32(n))
		}
	}
}