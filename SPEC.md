# go-anki Specification

A headless Anki client library in Go. The first standalone, programmatic Anki client that works without Anki Desktop or any GUI.

## Why This Exists

Every existing Anki integration requires either:
- **Anki Desktop** running with AnkiConnect (heavy, needs Qt/GUI)
- **Python `anki` library** (corrupts DB on writes — unicase collation bug, broken sync)
- **AnkiDroid** (Android only, no API)
- **AnkiWeb** (website only, no programmatic API)

`go-anki` fills the gap: a single static Go binary that can read/write Anki collections, schedule reviews with FSRS, create cards/decks, and sync with AnkiWeb — all without a desktop app.

## Architecture

```
github.com/vpul/go-anki/
├── pkg/
│   ├── collection/    # SQLite schema, read/write Anki .anki2 databases
│   ├── scheduler/     # FSRS spaced repetition (wraps go-fsrs)
│   ├── sync/          # AnkiWeb sync client (full download/upload)
│   ├── apkg/          # .apkg export + .colpkg import
│   └── types/         # Shared data types (Card, Note, Deck, Model, etc.)
├── cmd/
│   └── anki-go/       # CLI binary (future)
├── server/
│   └── server.go      # HTTP API server (for AI/MCP integration)
├── SPEC.md            # This file
├── ADR.md             # Architecture Decision Records
└── go.mod
```

## Features (v1)

### Tier 1: Local Operations
- [x] Read Anki collection (due cards, decks, notes, models)
- [x] Answer cards with FSRS scheduling (update intervals, ease, reps, lapses)
- [x] Create new notes and cards
- [x] Create new decks
- [x] HTTP API server (for Hermes/AI agent integration)

### Tier 2: Sync & Import/Export
- [x] .apkg export (push deck changes to phone)
- [x] .colpkg import (pull collection from phone)
- [x] AnkiWeb full download (pull entire collection)
- [x] AnkiWeb full upload (push entire collection)

### Deferred (v2+)
- [ ] Incremental delta sync (bidirectional changes)
- [ ] Media sync (images, audio)
- [ ] CLI tool (`anki-go due`, `anki-go answer`, etc.)
- [ ] AnkiWeb streaming sync (chunked transfers)
- [ ] Multiple collection support

## Key Technical Decisions

See [ADR.md](ADR.md) for detailed rationale.

### No CGO
Pure Go with `modernc.org/sqlite`. Single static binary, no C compiler needed, works on any platform. Trade-off: ~2x slower than CGO sqlite for bulk operations, but Anki collections are small (<100MB typically) so this doesn't matter.

### FSRS over SM-2
FSRS is what Anki uses since v23.12+. The `go-fsrs` library gives us the exact same scheduling algorithm. SM-2 is available as fallback but not the default.

### Full Download/Upload over Incremental Sync
v1 implements only full download/upload to AnkiWeb. This covers 95% of use cases (pull before query, push after changes) and avoids the 8-10 hours of implementing delta sync with USN tracking and conflict resolution. Incremental sync is planned for v2.

### Raw SQLite over Anki Python Library
Read/write the Anki .anki2 database directly via SQLite. We avoid Anki's `unicase` collation by:
1. Skipping `COLLATE unicase` in queries (use separate lookups instead of JOINs)
2. Computing `csum` (field checksum) ourselves with CRC32
3. Setting proper `mod` timestamps and `usn` values on writes

This approach has zero risk of DB corruption since we never touch the buggy Python library.

## Anki Database Schema

The .anki2 file is a SQLite database with these key tables:

### `col` (single row — collection metadata)
- `id`, `crt` (created), `mod` (modified), `usn` (sync), `ver` (schema version)
- `conf` JSON (scheduler config), `models` JSON (note types), `decks` JSON, `dconf` JSON (deck configs), `tags` JSON

### `notes`
- `id`, `guid`, `mid` (model ID), `mod`, `usn`, `tags`, `flds` (fields, `\x1f`-separated), `sfld` (sort field), `csum` (checksum), `flags`, `data`
- **Uses `COLLATE unicase`** — we avoid this and compute `csum` ourselves

### `cards`
- `id`, `nid` (note ID), `did` (deck ID), `ord`, `mod`, `usn`, `type` (0=new, 1=learning, 2=review, 3=relearning), `queue`, `due`, `ivl`, `factor`, `reps`, `lapses`, `left`, `odue`, `odid`, `flags`, `data`
- **No unicase** — safe for direct writes

### `revlog`
- `id`, `cid` (card ID), `usn`, `ease` (1=Again, 2=Hard, 3=Good, 4=Easy), `ivl`, `last_ivl`, `factor`, `time`, `type`
- **No unicase** — safe for direct writes

### `graves` (deletion tracking for sync)
- `usn`, `oid` (object ID), `type` (0=card, 1=note, 2=deck)

## FSRS Integration

```go
import "github.com/open-spaced-repetition/go-fsrs"

scheduler := fsrs.NewFSRS(fsrs.DefaultParameters)
card := fsrs.Card{
    Due:       time.Now(),
    Stability: 0.5,
    Difficulty: 5.0,
    // ... map from Anki DB fields
}

result := scheduler.Review(card, fsrs.RatingGood)
// result.Card has new Due, Stability, Difficulty, etc.
// Write result back to Anki DB
```

## AnkiWeb Sync Protocol

### Authentication
```
POST https://sync.ankiweb.net/sync/hostKey
Body: u=<username>&p=<password>
Response: {"key": "<session_key>"}
```

### Full Download
```
POST /sync/download
Body: msgpack({ k: <key>, v: <protocol_version> })
Response: zstd-compressed .anki2 database
```

### Full Upload
```
POST /sync/upload
Body: msgpack({ k: <key>, v: <protocol_version>, data: <zstd-compressed-anki2>, ...
Response: {"data": null, "err": ""}
```

### Protocol Version
Currently **11**. Client must report valid Anki version string.

## HTTP API (Server Mode)

```
GET    /api/v1/decks                    # List all decks
GET    /api/v1/decks/:id/cards/due      # Get due cards for a deck
GET    /api/v1/cards/:id                # Get card details
POST   /api/v1/cards/:id/answer         # Answer a card (rating: again/hard/good/easy)
POST   /api/v1/notes                     # Create a new note
POST   /api/v1/decks                    # Create a new deck
POST   /api/v1/sync/download            # Pull from AnkiWeb
POST   /api/v1/sync/upload             # Push to AnkiWeb
GET    /api/v1/stats                    # Collection statistics
```

## Development PR Plan

1. **PR 1**: Project scaffold + specs + ADR + Go module init + CI
2. **PR 2**: `pkg/types` — Core data types (Card, Note, Deck, Model)
3. **PR 3**: `pkg/collection` — DB schema layer (read operations)
4. **PR 4**: `pkg/scheduler` — FSRS integration + answer card logic
5. **PR 5**: `pkg/collection` — Write operations (create notes, decks)
6. **PR 6**: `pkg/apkg` — Export/import (.apkg + .colpkg)
7. **PR 7**: `pkg/sync` — AnkiWeb full download/upload
8. **PR 8**: HTTP API server

## Testing Strategy

- Unit tests for each package
- Integration tests using a real .anki2 fixture (the user's collection)
- CI via GitHub Actions (Go 1.22+, linux/mac/windows)
- `go test ./...` with race detector