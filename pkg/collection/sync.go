package collection

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"time"

	goanki "github.com/vpul/go-anki/pkg/types"
)

// maxUSN returns the maximum USN across cards, notes, and graves tables,
// and the graves table. For v18+ collections, also queries tables with
// their own USN columns: decks, notetypes, templates, deck_config, fields, tags.
// This represents the highest known update sequence number.
func (c *Collection) maxUSN(ctx context.Context) (int, error) {
	var usn int
	query := `
		SELECT MAX(max_usn) FROM (
			SELECT COALESCE(MAX(usn), 0) AS max_usn FROM cards
			UNION ALL
			SELECT COALESCE(MAX(usn), 0) FROM notes
			UNION ALL
			SELECT COALESCE(MAX(usn), 0) FROM graves`
	if c.isV18Plus() {
		query += `
			UNION ALL
			SELECT COALESCE(MAX(usn), 0) FROM decks
			UNION ALL
			SELECT COALESCE(MAX(usn), 0) FROM notetypes
			UNION ALL
			SELECT COALESCE(MAX(usn), 0) FROM templates
			UNION ALL
			SELECT COALESCE(MAX(usn), 0) FROM deck_config
			UNION ALL
			SELECT COALESCE(MAX(usn), 0) FROM fields
			UNION ALL
			SELECT COALESCE(MAX(usn), 0) FROM tags`
	}
	query += `
		)`
	err := c.db.QueryRowContext(ctx, query).Scan(&usn)
	if err != nil {
		return 0, fmt.Errorf("query max usn: %w", err)
	}
	return usn, nil
}

// GetChanges returns all objects modified after the given USN.
// Cards and notes with usn=-1 (not yet synced) are always included.
// Uses the provided context for cancellation.
func (c *Collection) GetChanges(ctx context.Context, sinceUSN int) (*goanki.SyncDelta, error) {
	delta := &goanki.SyncDelta{}

	// Query cards changed after sinceUSN (usn=-1 means local-only, usn>sinceUSN means server-side change)
	cards, err := c.getCardsChangedSince(ctx, sinceUSN)
	if err != nil {
		return nil, fmt.Errorf("get changed cards: %w", err)
	}
	delta.Cards = cards

	// Query notes changed after sinceUSN
	notes, err := c.getNotesChangedSince(ctx, sinceUSN)
	if err != nil {
		return nil, fmt.Errorf("get changed notes: %w", err)
	}
	delta.Notes = notes

	// Query decks changed after sinceUSN (v18+ decks table or v11 JSON blob)
	decks, err := c.getDecksChangedSince(ctx, sinceUSN)
	if err != nil {
		return nil, fmt.Errorf("get changed decks: %w", err)
	}
	delta.Decks = decks

	// Query graves (deletions) after sinceUSN
	graves, err := c.getGravesSince(ctx, sinceUSN)
	if err != nil {
		return nil, fmt.Errorf("get graves: %w", err)
	}
	delta.Graves = graves

	// Set the current max USN for this delta
	maxUSN, err := c.maxUSN(ctx)
	if err != nil {
		return nil, fmt.Errorf("get max usn: %w", err)
	}
	delta.USN = maxUSN

	return delta, nil
}

// getCardsChangedSince returns cards with usn=-1 or usn > sinceUSN.
func (c *Collection) getCardsChangedSince(ctx context.Context, sinceUSN int) ([]goanki.Card, error) {
	rows, err := c.db.QueryContext(ctx, `
		SELECT id, nid, did, ord, mod, usn,
		       type, queue, due, ivl, factor,
		       reps, lapses, left, odue, odid,
		       flags, data
		FROM cards
		WHERE usn = -1 OR usn > ?
		ORDER BY id`, sinceUSN)
	if err != nil {
		return nil, fmt.Errorf("query changed cards: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var cards []goanki.Card
	for rows.Next() {
		var card goanki.Card
		if err := rows.Scan(
			&card.ID, &card.NID, &card.DID, &card.ORD, &card.Mod, &card.USN,
			&card.Type, &card.Queue, &card.Due, &card.IVL, &card.Factor,
			&card.Reps, &card.Lapses, &card.Left, &card.ODue, &card.ODID,
			&card.Flags, &card.Data,
		); err != nil {
			return nil, fmt.Errorf("scan changed card: %w", err)
		}
		cards = append(cards, card)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate changed cards: %w", err)
	}
	return cards, nil
}

// getNotesChangedSince returns notes with usn=-1 or usn > sinceUSN.
func (c *Collection) getNotesChangedSince(ctx context.Context, sinceUSN int) ([]goanki.Note, error) {
	rows, err := c.db.QueryContext(ctx, `
		SELECT id, guid, mid, mod, usn, tags, flds, sfld, csum, flags, data
		FROM notes
		WHERE usn = -1 OR usn > ?
		ORDER BY id`, sinceUSN)
	if err != nil {
		return nil, fmt.Errorf("query changed notes: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var notes []goanki.Note
	v18 := c.isV18Plus()
	for rows.Next() {
		var note goanki.Note
		if v18 {
			var sfld, csum interface{}
			if err := rows.Scan(&note.ID, &note.GUID, &note.MID, &note.Mod, &note.USN,
				&note.Tags, &note.Flds, &sfld, &csum, &note.Flags, &note.Data); err != nil {
				return nil, fmt.Errorf("scan changed note: %w", err)
			}
			note.Sfld = ifaceToStr(sfld)
			note.Csum = ifaceToStr(csum)
		} else {
			if err := rows.Scan(&note.ID, &note.GUID, &note.MID, &note.Mod, &note.USN,
				&note.Tags, &note.Flds, &note.Sfld, &note.Csum, &note.Flags, &note.Data); err != nil {
				return nil, fmt.Errorf("scan changed note: %w", err)
			}
		}
		notes = append(notes, note)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate changed notes: %w", err)
	}
	return notes, nil
}

// getDecksChangedSince returns decks with usn=-1 or usn > sinceUSN.
func (c *Collection) getDecksChangedSince(ctx context.Context, sinceUSN int) ([]goanki.Deck, error) {
	if c.isV18Plus() {
		return c.getDecksChangedSinceV18(ctx, sinceUSN)
	}
	return c.getDecksChangedSinceV11(ctx, sinceUSN)
}

func (c *Collection) getDecksChangedSinceV18(ctx context.Context, sinceUSN int) ([]goanki.Deck, error) {
	rows, err := c.db.QueryContext(ctx, `
		SELECT id, name, mtime_secs, usn, kind
		FROM decks
		WHERE usn = -1 OR usn > ?
		ORDER BY id`, sinceUSN)
	if err != nil {
		return nil, fmt.Errorf("query v18 changed decks: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var decks []goanki.Deck
	for rows.Next() {
		var d goanki.Deck
		var mtime int64
		var kind []byte
		if err := rows.Scan(&d.ID, &d.Name, &mtime, &d.USN, &kind); err != nil {
			return nil, fmt.Errorf("scan changed deck: %w", err)
		}
		d.Mtime = mtime
		d.Conf = 1
		d.Desc = ""
		d.Bury = true
		d.Dyn = isFilteredDeckKind(kind)
		decks = append(decks, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate changed decks: %w", err)
	}
	return decks, nil
}

func (c *Collection) getDecksChangedSinceV11(ctx context.Context, sinceUSN int) ([]goanki.Deck, error) {
	// v11 stores all decks as a JSON blob in the col table.
	// We load all decks and filter by USN.
	allDecks, err := c.GetDecks()
	if err != nil {
		return nil, fmt.Errorf("get all decks for change tracking: %w", err)
	}
	var changed []goanki.Deck
	for _, d := range allDecks {
		if d.USN == -1 || d.USN > sinceUSN {
			changed = append(changed, d)
		}
	}
	return changed, nil
}

// getGravesSince returns grave records with usn = -1 or usn > sinceUSN.
func (c *Collection) getGravesSince(ctx context.Context, sinceUSN int) ([]goanki.Grave, error) {
	rows, err := c.db.QueryContext(ctx, `
		SELECT oid, type, usn
		FROM graves
		WHERE usn = -1 OR usn > ?
		ORDER BY oid`, sinceUSN)
	if err != nil {
		return nil, fmt.Errorf("query graves: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var graves []goanki.Grave
	for rows.Next() {
		var g goanki.Grave
		if err := rows.Scan(&g.OID, &g.Type, &g.USN); err != nil {
			return nil, fmt.Errorf("scan grave: %w", err)
		}
		graves = append(graves, g)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate graves: %w", err)
	}
	return graves, nil
}

// ApplyChanges applies a delta of remote changes to the collection.
// For each card/note/deck: INSERT OR REPLACE
// For each grave: DELETE the object + remove from graves
// Uses the provided context for cancellation.
func (c *Collection) ApplyChanges(ctx context.Context, delta *goanki.SyncDelta) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Apply card changes: INSERT OR REPLACE
	for _, card := range delta.Cards {
		_, err := tx.ExecContext(ctx, `
			INSERT OR REPLACE INTO cards
				(id, nid, did, ord, mod, usn, type, queue, due, ivl, factor,
				 reps, lapses, left, odue, odid, flags, data)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			card.ID, card.NID, card.DID, card.ORD, card.Mod, card.USN,
			card.Type, card.Queue, card.Due, card.IVL, card.Factor,
			card.Reps, card.Lapses, card.Left, card.ODue, card.ODID,
			card.Flags, card.Data)
		if err != nil {
			return fmt.Errorf("insert/replace card %d: %w", card.ID, err)
		}
	}

	// Apply note changes: INSERT OR REPLACE
	for _, note := range delta.Notes {
		if c.isV18Plus() {
			_, err := tx.ExecContext(ctx, `
				INSERT OR REPLACE INTO notes
					(id, guid, mid, mod, usn, tags, flds, sfld, csum, flags, data)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				note.ID, note.GUID, note.MID, note.Mod, note.USN,
				note.Tags, note.Flds, note.Sfld, note.Csum, note.Flags, note.Data)
			if err != nil {
				return fmt.Errorf("insert/replace note %d: %w", note.ID, err)
			}
		} else {
			// In v11, sfld and csum are TEXT
			_, err := tx.ExecContext(ctx, `
				INSERT OR REPLACE INTO notes
					(id, guid, mid, mod, usn, tags, flds, sfld, csum, flags, data)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				note.ID, note.GUID, note.MID, note.Mod, note.USN,
				note.Tags, note.Flds, note.Sfld, note.Csum, note.Flags, note.Data)
			if err != nil {
				return fmt.Errorf("insert/replace note %d: %w", note.ID, err)
			}
		}
	}

	// Apply deck changes: INSERT OR REPLACE
	for _, deck := range delta.Decks {
		if c.isV18Plus() {
			// v18: insert into decks table
			_, err := tx.ExecContext(ctx, `
				INSERT OR REPLACE INTO decks
					(id, name, mtime_secs, usn, common, kind)
				VALUES (?, ?, ?, ?, ?, ?)`,
				deck.ID, deck.Name, deck.Mtime, deck.USN,
				[]byte{0x08, 0x01, 0x10, 0x01}, // default common blob
				[]byte{0x0a, 0x02, 0x08, 0x01})  // default regular deck kind blob
			if err != nil {
				return fmt.Errorf("insert/replace deck %d: %w", deck.ID, err)
			}
		} else {
			// v11: update JSON blob in col table
			if err := c.applyDeckChangeV11(ctx, tx, deck); err != nil {
				return err
			}
		}
	}

	// Apply grave deletions
	for _, grave := range delta.Graves {
		if err := c.applyGrave(ctx, tx, grave); err != nil {
			return err
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit changes: %w", err)
	}
	return nil
}

// applyDeckChangeV11 updates a single deck in the v11 JSON blob.
func (c *Collection) applyDeckChangeV11(ctx context.Context, tx *sql.Tx, deck goanki.Deck) error {
	var decksJSON string
	err := tx.QueryRowContext(ctx, "SELECT decks FROM col").Scan(&decksJSON)
	if err != nil {
		return fmt.Errorf("query decks json: %w", err)
	}

	decks, err := goanki.ParseDecksJSON([]byte(decksJSON))
	if err != nil {
		return fmt.Errorf("parse decks json: %w", err)
	}

	decks[deck.ID] = deck

	newJSON, err := goanki.MarshalDecksJSON(decks)
	if err != nil {
		return fmt.Errorf("marshal decks json: %w", err)
	}

	_, err = tx.ExecContext(ctx, "UPDATE col SET decks = ?, mod = ?", newJSON, time.Now().Unix())
	if err != nil {
		return fmt.Errorf("update col decks: %w", err)
	}
	return nil
}

// applyGrave deletes an object and removes its grave record.
func (c *Collection) applyGrave(ctx context.Context, tx *sql.Tx, grave goanki.Grave) error {
	switch grave.Type {
	case 1: // Card
		_, err := tx.ExecContext(ctx, "DELETE FROM cards WHERE id = ?", grave.OID)
		if err != nil {
			return fmt.Errorf("delete card %d from grave: %w", grave.OID, err)
		}
	case 2: // Note
		_, err := tx.ExecContext(ctx, "DELETE FROM notes WHERE id = ?", grave.OID)
		if err != nil {
			return fmt.Errorf("delete note %d from grave: %w", grave.OID, err)
		}
		// Also delete cards belonging to this note
		_, err = tx.ExecContext(ctx, "DELETE FROM cards WHERE nid = ?", grave.OID)
		if err != nil {
			return fmt.Errorf("delete cards for note %d from grave: %w", grave.OID, err)
		}
	case 3: // Deck
		if c.isV18Plus() {
			_, err := tx.ExecContext(ctx, "DELETE FROM decks WHERE id = ?", grave.OID)
			if err != nil {
				return fmt.Errorf("delete deck %d from grave: %w", grave.OID, err)
			}
			// Also remove deck_config
			if _, err := tx.ExecContext(ctx, "DELETE FROM deck_config WHERE id = ?", grave.OID); err != nil {
				return fmt.Errorf("delete deck_config %d from grave: %w", grave.OID, err)
			}
		} else {
			// v11: remove from JSON blob
			var decksJSON string
			err := tx.QueryRowContext(ctx, "SELECT decks FROM col").Scan(&decksJSON)
			if err != nil {
				return fmt.Errorf("query decks json for grave: %w", err)
			}
			decks, err := goanki.ParseDecksJSON([]byte(decksJSON))
			if err != nil {
				return fmt.Errorf("parse decks json for grave: %w", err)
			}
			delete(decks, grave.OID)
			newJSON, err := goanki.MarshalDecksJSON(decks)
			if err != nil {
				return fmt.Errorf("marshal decks json for grave: %w", err)
			}
			_, err = tx.ExecContext(ctx, "UPDATE col SET decks = ?, mod = ?", newJSON, time.Now().Unix())
			if err != nil {
				return fmt.Errorf("update col decks for grave: %w", err)
			}
		}
		// Move cards from deleted deck to default deck (id=1)
		_, err := tx.ExecContext(ctx, "UPDATE cards SET did = 1 WHERE did = ?", grave.OID)
		if err != nil {
			return fmt.Errorf("reparent cards from deleted deck %d: %w", grave.OID, err)
		}
	default:
		log.Printf("warning: unknown grave type %d for oid %d", grave.Type, grave.OID)
	}

	// Remove the grave record itself
	_, err := tx.ExecContext(ctx, "DELETE FROM graves WHERE oid = ? AND type = ?", grave.OID, grave.Type)
	if err != nil {
		return fmt.Errorf("delete grave record for oid %d: %w", grave.OID, err)
	}
	return nil
}

// GetSyncState returns the current sync state for delta negotiation.
// SCM is the collection's creation time (crt), USN is the max USN across all tables,
// and HostNum is 0 (not used locally).
func (c *Collection) GetSyncState(ctx context.Context) (*goanki.SyncState, error) {
	var scm int64
	err := c.db.QueryRowContext(ctx, "SELECT COALESCE(crt, 0) FROM col LIMIT 1").Scan(&scm)
	if err != nil {
		return nil, fmt.Errorf("query scm: %w", err)
	}

	usn, err := c.maxUSN(ctx)
	if err != nil {
		return nil, fmt.Errorf("query max usn: %w", err)
	}

	return &goanki.SyncState{
		SCM:     scm,
		USN:     usn,
		HostNum: 0,
	}, nil
}

// MarkSynced updates all objects with usn=-1 to the given USN.
func (c *Collection) MarkSynced(ctx context.Context, usn int) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Mark cards
	_, err = tx.ExecContext(ctx, "UPDATE cards SET usn = ? WHERE usn = -1", usn)
	if err != nil {
		return fmt.Errorf("mark cards synced: %w", err)
	}

	// Mark notes
	_, err = tx.ExecContext(ctx, "UPDATE notes SET usn = ? WHERE usn = -1", usn)
	if err != nil {
		return fmt.Errorf("mark notes synced: %w", err)
	}

	// Mark decks (v18+ has separate table)
	if c.isV18Plus() {
		_, err = tx.ExecContext(ctx, "UPDATE decks SET usn = ? WHERE usn = -1", usn)
		if err != nil {
			return fmt.Errorf("mark decks synced: %w", err)
		}
		// Also update deck_config, notetypes, templates, fields
		if _, err := tx.ExecContext(ctx, "UPDATE deck_config SET usn = ? WHERE usn = -1", usn); err != nil {
			return fmt.Errorf("mark deck_config synced: %w", err)
		}
		if _, err := tx.ExecContext(ctx, "UPDATE notetypes SET usn = ? WHERE usn = -1", usn); err != nil {
			return fmt.Errorf("mark notetypes synced: %w", err)
		}
		if _, err := tx.ExecContext(ctx, "UPDATE templates SET usn = ? WHERE usn = -1", usn); err != nil {
			return fmt.Errorf("mark templates synced: %w", err)
		}
		if _, err := tx.ExecContext(ctx, "UPDATE fields SET usn = ? WHERE usn = -1", usn); err != nil {
			return fmt.Errorf("mark fields synced: %w", err)
		}
	} else {
		// v11: decks are stored as JSON in col.decks — reload, update usn, write back.
		var decksJSON string
		if err := tx.QueryRowContext(ctx, "SELECT decks FROM col").Scan(&decksJSON); err != nil {
			return fmt.Errorf("query decks json for mark synced: %w", err)
		}
		deckMap, err := goanki.ParseDecksJSON([]byte(decksJSON))
		if err != nil {
			return fmt.Errorf("parse decks json for mark synced: %w", err)
		}
		for _, d := range deckMap {
			if d.USN == -1 {
				d.USN = usn
				deckMap[d.ID] = d
			}
		}
		newJSON, err := goanki.MarshalDecksJSON(deckMap)
		if err != nil {
			return fmt.Errorf("marshal decks json for mark synced: %w", err)
		}
		if _, err := tx.ExecContext(ctx, "UPDATE col SET decks = ?", string(newJSON)); err != nil {
			return fmt.Errorf("update col decks for mark synced: %w", err)
		}
	}

	// Mark graves with usn=-1 to the new usn (these are deletions we just uploaded)
	_, err = tx.ExecContext(ctx, "UPDATE graves SET usn = ? WHERE usn = -1", usn)
	if err != nil {
		return fmt.Errorf("mark graves synced: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit mark synced: %w", err)
	}
	return nil
}

// AddGrave adds a deletion record to the graves table.
func (c *Collection) AddGrave(ctx context.Context, oid int64, otype int8) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	_, err := c.db.ExecContext(ctx,
		"INSERT OR REPLACE INTO graves (usn, oid, type) VALUES (-1, ?, ?)",
		oid, otype)
	if err != nil {
		return fmt.Errorf("add grave for oid=%d type=%d: %w", oid, otype, err)
	}
	return nil
}
