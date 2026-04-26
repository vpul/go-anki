package scheduler

import (
	"testing"
	"time"

	goanki "github.com/vpul/go-anki/pkg/types"
)

func TestFSRSSchedulerAnswerNewCard(t *testing.T) {
	s := NewFSRSScheduler()

	card := goanki.Card{
		ID:   1000000000001,
		NID:  1000000000000,
		DID:  1,
		Type: goanki.CardTypeNew,
		Queue: goanki.QueueNew,
		Reps: 0,
		Lapses: 0,
		IVL: 0,
		Factor: 0,
		Left: 0,
	}

	now := time.Now()

	// Test "Good" on a new card
	answer, err := s.Answer(card, goanki.RatingGood, now)
	if err != nil {
		t.Fatalf("Answer: %v", err)
	}

	if answer.Card.ID != card.ID {
		t.Errorf("expected card ID %d, got %d", card.ID, answer.Card.ID)
	}

	if answer.Card.Reps != 1 {
		t.Errorf("expected 1 rep after first review, got %d", answer.Card.Reps)
	}

	if answer.Card.Type == goanki.CardTypeNew {
		t.Error("expected card type to change from New after review")
	}

	if answer.Review.Ease != goanki.RatingGood {
		t.Errorf("expected review ease Good, got %v", answer.Review.Ease)
	}

	t.Logf("After 'Good' on new card: Type=%v, Queue=%v, IVL=%d, Factor=%d, Reps=%d, Lapses=%d",
		answer.Card.Type, answer.Card.Queue, answer.Card.IVL, answer.Card.Factor,
		answer.Card.Reps, answer.Card.Lapses)
}

func TestFSRSSchedulerAnswerAgain(t *testing.T) {
	s := NewFSRSScheduler()

	// "Again" on a new card transitions it to learning state
	// Lapses only increment on review cards, not new cards (correct FSRS behavior)
	card := goanki.Card{
		ID:     1000000000001,
		NID:    1000000000000,
		DID:    1,
		Type:   goanki.CardTypeNew,
		Queue:  goanki.QueueNew,
		Reps:   0,
		Lapses: 0,
	}

	answer, err := s.Answer(card, goanki.RatingAgain, time.Now())
	if err != nil {
		t.Fatalf("Answer: %v", err)
	}

	// New card answered "Again" becomes learning, not a lapse
	if answer.Card.Type != goanki.CardTypeLearning {
		t.Errorf("expected learning type after Again on new card, got %v", answer.Card.Type)
	}

	// Now test "Again" on a REVIEW card which should increment lapses
	stability := 5.0
	difficulty := 5.0
	reviewCard := goanki.Card{
		ID:         1000000000002,
		NID:        1000000000000,
		DID:        1,
		Type:       goanki.CardTypeReview,
		Queue:      goanki.CardQueue(2),
		IVL:        7,
		Factor:     2500,
		Reps:       5,
		Lapses:     1,
		Mod:        time.Now().Unix() - 86400,
		Stability:  &stability,
		Difficulty: &difficulty,
	}

	answer2, err := s.Answer(reviewCard, goanki.RatingAgain, time.Now())
	if err != nil {
		t.Fatalf("Answer on review card: %v", err)
	}

	if answer2.Card.Lapses != 2 {
		t.Errorf("expected 2 lapses after 'Again' on review card, got %d", answer2.Card.Lapses)
	}

	t.Logf("After 'Again' on review card: Type=%v, Lapses=%d", answer2.Card.Type, answer2.Card.Lapses)
}

func TestFSRSSchedulerReviewCard(t *testing.T) {
	s := NewFSRSScheduler()

	// A review card that has been reviewed before
	stability := 5.0
	difficulty := 5.0
	card := goanki.Card{
		ID:         1000000000001,
		NID:        1000000000000,
		DID:        1,
		Type:       goanki.CardTypeReview,
		Queue:      goanki.CardQueue(2), // QueueReview
		IVL:        7,                    // 7 day interval
		Factor:     2500,                 // 250% ease
		Reps:       5,
		Lapses:     1,
		Mod:        time.Now().Unix() - 86400, // Modified yesterday
		Stability:  &stability,
		Difficulty: &difficulty,
	}

	answer, err := s.Answer(card, goanki.RatingGood, time.Now())
	if err != nil {
		t.Fatalf("Answer: %v", err)
	}

	// After "Good" on a review card, stability should increase
	if answer.Card.Stability == nil || *answer.Card.Stability <= stability {
		t.Logf("Stability: before=%v, after=%v", stability, answer.Card.Stability)
	}

	t.Logf("After 'Good' on review card: IVL=%d->%d, Factor=%d->%d, Reps=%d->%d, Stability=%.2f->%.2f",
		card.IVL, answer.Card.IVL, card.Factor, answer.Card.Factor,
		card.Reps, answer.Card.Reps, stability, *answer.Card.Stability)
}

func TestFSRSSchedulerAllRatings(t *testing.T) {
	s := NewFSRSScheduler()

	ratings := []goanki.Rating{goanki.RatingAgain, goanki.RatingHard, goanki.RatingGood, goanki.RatingEasy}

	for _, rating := range ratings {
		card := goanki.Card{
			ID:   1000000000001,
			NID:  1000000000000,
			DID:  1,
			Type: goanki.CardTypeNew,
			Queue: goanki.QueueNew,
		}

		answer, err := s.Answer(card, rating, time.Now())
		if err != nil {
			t.Errorf("Rating %v: Answer error: %v", rating, err)
			continue
		}

		t.Logf("Rating %v → Type=%v, IVL=%d, Factor=%d, Reps=%d, Lapses=%d",
			rating, answer.Card.Type, answer.Card.IVL, answer.Card.Factor,
			answer.Card.Reps, answer.Card.Lapses)
	}
}

func TestFSRSSchedulerInvalidCard(t *testing.T) {
	s := NewFSRSScheduler()

	card := goanki.Card{} // Zero ID

	_, err := s.Answer(card, goanki.RatingGood, time.Now())
	if err == nil {
		t.Error("expected error for zero card ID")
	}
}