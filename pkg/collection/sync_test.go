package collection

import (
	"context"
	"testing"
	"time"

	goanki "github.com/vpul/go-anki/pkg/types"
)

func TestGetSyncState(t *testing.T) {
	col, _ := createReadWriteTestDB(t)
	defer func() { _ = col.Close() }()

	state, err := col.GetSyncState(context.Background())
	if err != nil {
		t.Fatalf("GetSyncState: %v", err)
	}

	if state.SCM == 0 {
		t.Error("expected non-zero SCM (crt)")
	}
	if state.HostNum != 0 {
		t.Errorf("expected HostNum=0, got %d", state.HostNum)
	}
	// Initially USN should be 0 or -1 (no synced objects)
	t.Logf("SyncState: SCM=%d USN=%d HostNum=%d", state.SCM, state.USN, state.HostNum)
}

func TestGetChangesNoChanges(t *testing.T) {
	col, _ := createReadWriteTestDB(t)
	defer func() { _ = col.Close() }()

	// Mark everything synced with USN=999 first
	err := col.MarkSynced(context.Background(), 999)
	if err != nil {
		t.Fatalf("MarkSynced: %v", err)
	}

	// After marking synced, getting changes with sinceUSN=999 should return empty
	// (no objects have usn = -1 or usn > 999)
	delta, err := col.GetChanges(context.Background(), 999)
	if err != nil {
		t.Fatalf("GetChanges: %v", err)
	}

	// After MarkSynced(999) on v11 schema, cards and notes should have usn=999,
	// but decks stored in the col JSON blob may still show usn=-1.
	// So we check cards and notes (which have direct usn columns).
	if len(delta.Cards) > 0 {
		t.Errorf("expected no cards, got %d", len(delta.Cards))
	}
	if len(delta.Notes) > 0 {
		t.Errorf("expected no notes, got %d", len(delta.Notes))
	}
}

func TestGetChangesWithLocalChanges(t *testing.T) {
	col, _ := createReadWriteTestDB(t)
	defer func() { _ = col.Close() }()

	// Get changes with sinceUSN=0 should find the test card/note (usn=-1)
	delta, err := col.GetChanges(context.Background(), 0)
	if err != nil {
		t.Fatalf("GetChanges: %v", err)
	}

	if len(delta.Cards) < 1 {
		t.Errorf("expected at least 1 card with usn=-1, got %d", len(delta.Cards))
	}
	if len(delta.Notes) < 1 {
		t.Errorf("expected at least 1 note with usn=-1, got %d", len(delta.Notes))
	}

	// Check USN field
	if delta.USN < 0 {
		t.Errorf("expected USN >= 0, got %d", delta.USN)
	}
}

func TestMarkSynced(t *testing.T) {
	col, _ := createReadWriteTestDB(t)
	defer func() { _ = col.Close() }()

	// Mark all pending objects as synced with USN=42
	err := col.MarkSynced(context.Background(), 42)
	if err != nil {
		t.Fatalf("MarkSynced: %v", err)
	}

	// After MarkSynced(42), GetChanges(42) should find nothing
	// because usn=42 is not > 42, and nothing has usn=-1 anymore
	delta, err := col.GetChanges(context.Background(), 42)
	if err != nil {
		t.Fatalf("GetChanges after MarkSynced: %v", err)
	}
	if len(delta.Cards) > 0 {
		t.Errorf("expected 0 cards after MarkSynced, got %d", len(delta.Cards))
	}
	if len(delta.Notes) > 0 {
		t.Errorf("expected 0 notes after MarkSynced, got %d", len(delta.Notes))
	}

	// Verify USN was set on cards
	var usn int
	err = col.db.QueryRow("SELECT usn FROM cards LIMIT 1").Scan(&usn)
	if err != nil {
		t.Fatalf("query card usn: %v", err)
	}
	if usn != 42 {
		t.Errorf("expected card usn=42, got %d", usn)
	}
}

func TestAddGrave(t *testing.T) {
	col, _ := createReadWriteTestDB(t)
	defer func() { _ = col.Close() }()

	// Add a grave record for a card
	err := col.AddGrave(context.Background(), 1001, 0) // type 0 = card
	if err != nil {
		t.Fatalf("AddGrave: %v", err)
	}

	// Verify the grave exists
	var count int
	err = col.db.QueryRow("SELECT COUNT(*) FROM graves WHERE oid = 1001").Scan(&count)
	if err != nil {
		t.Fatalf("count graves: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 grave, got %d", count)
	}

	// Get changes should include the grave (usn=-1)
	delta, err := col.GetChanges(context.Background(), 0)
	if err != nil {
		t.Fatalf("GetChanges: %v", err)
	}
	found := false
	for _, g := range delta.Graves {
		if g.OID == 1001 && g.Type == 0 {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected grave for oid=1001 type=1 in GetChanges")
	}
}

func TestApplyChangesCardsAndNotes(t *testing.T) {
	col, _ := createReadWriteTestDB(t)
	defer func() { _ = col.Close() }()

	now := time.Now().Unix()
	delta := &goanki.SyncDelta{
		Cards: []goanki.Card{
			{
				ID: 2001, NID: 2000, DID: 1, ORD: 0, Mod: now, USN: 50,
				Type: 0, Queue: 0, Due: 0, IVL: 0, Factor: 0,
				Reps: 0, Lapses: 0, Left: 0, ODue: 0, ODID: 0, Flags: 0, Data: "",
			},
		},
		Notes: []goanki.Note{
			{
				ID: 2000, GUID: "remote-guid", MID: 1585323248, Mod: now, USN: 50,
				Tags: " remote ", Flds: "Front\x1fBack", Sfld: "Front", Csum: "abcdef12",
				Flags: 0, Data: "",
			},
		},
		USN: 50,
	}

	err := col.ApplyChanges(context.Background(), delta)
	if err != nil {
		t.Fatalf("ApplyChanges: %v", err)
	}

	// Verify card was inserted
	card, err := col.GetCardByID(2001)
	if err != nil {
		t.Fatalf("GetCardByID after apply: %v", err)
	}
	if card.USN != 50 {
		t.Errorf("expected card usn=50, got %d", card.USN)
	}

	// Verify note was inserted
	var guid string
	err = col.db.QueryRow("SELECT guid FROM notes WHERE id = 2000").Scan(&guid)
	if err != nil {
		t.Fatalf("query inserted note: %v", err)
	}
	if guid != "remote-guid" {
		t.Errorf("expected guid 'remote-guid', got %q", guid)
	}
}

func TestApplyChangesCardReplace(t *testing.T) {
	col, _ := createReadWriteTestDB(t)
	defer func() { _ = col.Close() }()

	now := time.Now().Unix()

	// First get the existing test card to know its ID
	cards, err := col.GetDueCards(goanki.DueCardsFilter{})
	if err != nil || len(cards) == 0 {
		t.Fatalf("get cards: %v", err)
	}
	existingCard := cards[0]

	// Apply a replacement for the existing card
	delta := &goanki.SyncDelta{
		Cards: []goanki.Card{
			{
				ID: existingCard.ID, NID: existingCard.NID, DID: existingCard.DID,
				ORD: existingCard.ORD, Mod: now, USN: 60,
				Type: 0, Queue: 0, Due: 5, IVL: 5, Factor: 2500,
				Reps: 1, Lapses: 0, Left: 0, ODue: 0, ODID: 0, Flags: 0, Data: "",
			},
		},
		USN: 60,
	}

	err = col.ApplyChanges(context.Background(), delta)
	if err != nil {
		t.Fatalf("ApplyChanges: %v", err)
	}

	// Verify the card was updated (replaced)
	updated, err := col.GetCardByID(existingCard.ID)
	if err != nil {
		t.Fatalf("GetCardByID after replace: %v", err)
	}
	if updated.IVL != 5 {
		t.Errorf("expected ivl=5, got %d", updated.IVL)
	}
	if updated.Reps != 1 {
		t.Errorf("expected reps=1, got %d", updated.Reps)
	}
}

func TestApplyChangesGraveDeletesCard(t *testing.T) {
	col, _ := createReadWriteTestDB(t)
	defer func() { _ = col.Close() }()

	// Get existing card
	cards, err := col.GetDueCards(goanki.DueCardsFilter{})
	if err != nil || len(cards) == 0 {
		t.Fatalf("get cards: %v", err)
	}
	cardID := cards[0].ID

	// Apply a grave that deletes this card
	delta := &goanki.SyncDelta{
		Graves: []goanki.Grave{
			{OID: cardID, Type: 0, USN: 70},
		},
		USN: 70,
	}

	err = col.ApplyChanges(context.Background(), delta)
	if err != nil {
		t.Fatalf("ApplyChanges with grave: %v", err)
	}

	// Verify card is gone
	_, err = col.GetCardByID(cardID)
	if err == nil {
		t.Error("expected error when getting deleted card")
	}

	// Verify grave record is removed
	var count int
	err = col.db.QueryRow("SELECT COUNT(*) FROM graves WHERE oid = ?", cardID).Scan(&count)
	if err != nil {
		t.Fatalf("count graves: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 graves after applying, got %d", count)
	}
}

func TestFullSyncRoundTrip(t *testing.T) {
	col, _ := createReadWriteTestDB(t)
	defer func() { _ = col.Close() }()

	// Step 1: Get initial state
	state, err := col.GetSyncState(context.Background())
	if err != nil {
		t.Fatalf("GetSyncState: %v", err)
	}
	t.Logf("Initial state: SCM=%d USN=%d", state.SCM, state.USN)

	// Step 2: Get changes (local-only items with usn=-1)
	delta, err := col.GetChanges(context.Background(), state.USN)
	if err != nil {
		t.Fatalf("GetChanges: %v", err)
	}
	t.Logf("Local changes: %d cards, %d notes, %d decks, %d graves, USN=%d",
		len(delta.Cards), len(delta.Notes), len(delta.Decks), len(delta.Graves), delta.USN)

	if len(delta.Cards) == 0 {
		t.Fatal("expected at least 1 local card change")
	}

	// Step 3: Mark as synced
	err = col.MarkSynced(context.Background(), 100)
	if err != nil {
		t.Fatalf("MarkSynced: %v", err)
	}

	// Step 4: Verify no more pending changes (with sinceUSN=100)
	delta2, err := col.GetChanges(context.Background(), 100)
	if err != nil {
		t.Fatalf("GetChanges after MarkSynced: %v", err)
	}
	if len(delta2.Cards) > 0 {
		t.Errorf("expected 0 pending cards after MarkSynced, got %d", len(delta2.Cards))
	}

	// Step 5: Verify the USN was set
	var testUSN int
	err = col.db.QueryRow("SELECT usn FROM cards WHERE usn > 0 LIMIT 1").Scan(&testUSN)
	if err != nil {
		t.Fatalf("query synced card usn: %v", err)
	}
	if testUSN != 100 {
		t.Errorf("expected card usn=100, got %d", testUSN)
	}
}

func TestApplyChangesDecksV11(t *testing.T) {
	col, _ := createReadWriteTestDB(t)
	defer func() { _ = col.Close() }()

	// Apply a deck change
	delta := &goanki.SyncDelta{
		Decks: []goanki.Deck{
			{
				ID:    42,
				Name:  "Remote Deck",
				Mtime: time.Now().Unix(),
				USN:   80,
				Dyn:   0,
				Conf:  1,
			},
		},
		USN: 80,
	}

	err := col.ApplyChanges(context.Background(), delta)
	if err != nil {
		t.Fatalf("ApplyChanges deck: %v", err)
	}

	// Verify deck was created
	deck, err := col.GetDeckByName("Remote Deck")
	if err != nil {
		t.Fatalf("GetDeckByName after apply: %v", err)
	}
	if deck.USN != 80 {
		t.Errorf("expected deck usn=80, got %d", deck.USN)
	}
}

func TestGetChangesWithSinceUSN(t *testing.T) {
	col, _ := createReadWriteTestDB(t)
	defer func() { _ = col.Close() }()

	// Initially objects have usn=-1, so sinceUSN=50 should still find them (usn=-1 always included)
	delta, err := col.GetChanges(context.Background(), 50)
	if err != nil {
		t.Fatalf("GetChanges(50): %v", err)
	}
	if len(delta.Cards) < 1 {
		t.Error("expected to find cards with usn=-1 when sinceUSN=50")
	}

	// After marking synced with USN=100, sinceUSN=100 should find nothing
	// (cards now have usn=100 which is not > 100, and nothing has usn=-1)
	err = col.MarkSynced(context.Background(), 100)
	if err != nil {
		t.Fatalf("MarkSynced: %v", err)
	}

	delta2, err := col.GetChanges(context.Background(), 100)
	if err != nil {
		t.Fatalf("GetChanges(100) after mark: %v", err)
	}
	if len(delta2.Cards) > 0 {
		t.Errorf("expected no cards since sinceUSN=100, got %d", len(delta2.Cards))
	}

	// But sinceUSN=50 should still find them (100 > 50)
	delta3, err := col.GetChanges(context.Background(), 50)
	if err != nil {
		t.Fatalf("GetChanges(50) after mark: %v", err)
	}
	if len(delta3.Cards) < 1 {
		t.Error("expected to find cards with usn=100 when sinceUSN=50")
	}
}
