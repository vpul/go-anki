# go-anki Specification

A headless Anki client library in Go. The first standalone, programmatic Anki client that works without Anki Desktop or any GUI.

## Why This Exists

Every existing Anki integration requires either:
- **Anki Desktop** running with AnkiConnect (heavy, needs Qt/GUI)
- **Python `anki` library** (corrupts DB on writes — unicase collation bug, broken sync)
- **AnkiDroid** (Android only, no API)
- **AnkiWeb** (website only, no programmatic API)

`go-anki` fills the gap: a single static Go binary that reads/writes Anki collections, schedules reviews with FSRS, creates cards/decks, syncs with AnkiWeb, serves an HTTP API, and supports media sync + multi-collection — all without a desktop app.

## Architecture

```
github.com/vpul/go-anki/
├── pkg/
│   ├── collection/    # SQLite schema layer — reads/writes Anki .anki2 (v1 JSON & v18+ protobuf)
│   ├── scheduler/     # FSRS spaced repetition (wraps go-fsrs)
│   ├── sync/          # AnkiWeb sync client (full download/upload, incremental delta, media)
│   ├── apkg/          # .apkg export + .colpkg import
│   └── types/         # Shared data types (Card, Note, Deck, Model, ReviewLog, etc.)
├── cmd/
│   └── anki-go/       # CLI binary: due, answer, add-note, create-deck, stats, sync, serve
├── server/
│   ├── server.go      # HTTP API server (REST, Go 1.22+ method routing, multi-collection)
│   └── registry.go    # Collection registry — maps names to .anki2 paths, per-collection locks
├── SPEC.md            # This file
├── ADR.md             # Architecture Decision Records
└── go.mod
```

## Features

### Tier 1: Local Operations
- [x] Read Anki collection (due cards, decks, notes, models)
- [x] Answer cards with FSRS scheduling (update intervals, ease, reps, lapses)
- [x] Create new notes and cards
- [x] Create new decks
- [x] HTTP API server (for Hermes/AI agent integration)
- [x] CLI binary (`anki-go`) with due, answer, add-note, create-deck, stats, sync, serve

### Tier 2: Sync & Import/Export
- [x] .apkg export (push deck changes to phone)
- [x] .colpkg import (pull collection from phone)
- [x] AnkiWeb full download (pull entire collection)
- [x] AnkiWeb full upload (push entire collection)
- [x] Incremental delta sync (bidirectional, USN tracking, conflict resolution)
- [x] Media sync (images, audio via AnkiWeb `/msync/` API)
- [x] AnkiWeb streaming sync (chunked transfers via `io.Pipe`, avoids loading full DB into memory)

### Tier 3: Multi-Collection Support
- [x] Multiple collection support (server handles many .anki2 files simultaneously)
- [x] Collection registry with per-collection mutexes for sync isolation
- [x] Isolated write locks per collection (no cross-collection contention)
- [x] List available collections via API

## Key Technical Decisions

See [ADR.md](ADR.md) for detailed rationale.

### No CGO
Pure Go with `modernc.org/sqlite`. Single static binary, no C compiler needed, works on any platform. Trade-off: ~2x slower than CGO sqlite for bulk operations, but Anki collections are small (<100MB typically) so this doesn't matter.

### FSRS over SM-2
FSRS is what Anki uses since v23.12+. The `go-fsrs` library gives us the exact same scheduling algorithm. SM-2 is available as fallback but not the default.

### Full Download/Upload v1 → Incremental Delta v2
v1 implemented only full download/upload to AnkiWeb (covers 95% of use cases: pull before query, push after changes). v2 added incremental delta sync with USN tracking, conflict resolution, and bidirectional change propagation.

### Raw SQLite over Anki Python Library
Read/write the Anki .anki2 database directly via SQLite. We avoid Anki's `unicase` collation by:
1. Registering a custom `unicase` collation with modernc (case-insensitive comparison)
2. Computing `csum` (field checksum) ourselves with CRC32
3. Setting proper `mod` timestamps and `usn` values on writes
4. Querying v18+ tables by ID/pk instead of name to avoid unicase JOINs

This approach has zero risk of DB corruption since we never touch the buggy Python library.

## Anki Database Schema

The .anki2 file is a SQLite database. go-anki supports **both** the v1 JSON-in-col schema and the v18+ protobuf-table schema transparently.

### v11 (Legacy — JSON in `col` table)

Single `col` table stores all metadata as JSON blobs:

| Column | Type | Description |
|--------|------|-------------|
| `id` | integer | Collection ID |
| `crt` | integer | Created timestamp |
| `mod` | integer | Modified timestamp |
| `usn` | integer | Sync USN counter |
| `ver` | integer | Schema version (11) |
| `conf` | text | Scheduler config JSON |
| `models` | text | Note types JSON |
| `decks` | text | Decks JSON |
| `dconf` | text | Deck configs JSON |
| `tags` | text | Tags JSON |

### v18+ (Modern — Separate Tables)

Decks, notetypes, and configs live in their own tables with **protobuf BLOB columns** (no protobuf library needed — we decode manually with simple varint/tag parsing):

**`decks`** table: `id`, `name`, `mtime_secs`, `usn`, `common` BLOB, `kind` BLOB (oneof: NormalDeck/FilteredDeck)

**`notetypes`** table: `id`, `name`, `mtime_secs`, `usn`, `config` BLOB (CSS, sort field, cloze flag, latex pre/post)

**`fields`** table: `ntid`, `ord`, `name`, `config` BLOB

**`templates`** table: `ntid`, `ord`, `name`, `mtime_secs`, `usn`, `config` BLOB (qfmt + afmt)

**`deck_config`** table: `id`, `name`, `mtime_secs`, `usn`, `config` BLOB

**`config`** table: `key` TEXT PK, `usn`, `mtime_secs`, `val` BLOB (arbitrary key-value)

**`tags`** table: `tag` TEXT PK, `usn`, `collapsed`, `config` BLOB

**`graves`** table: `oid`, `type`, `usn` (deletion tracking for sync)

### Tables (both schemas)

**`notes`**: `id`, `guid`, `mid`, `mod`, `usn`, `tags` (COLLATE unicase), `flds` (\x1f-separated), `sfld`, `csum`, `flags`, `data`
- v18+: `sfld` and `csum` are INTEGER columns (values cast when writing)

**`cards`**: `id`, `nid`, `did`, `ord`, `mod`, `usn`, `type` (0=new, 1=learning, 2=review, 3=relearning), `queue`, `due`, `ivl`, `factor`, `reps`, `lapses`, `left`, `odue`, `odid`, `flags`, `data`

**`revlog`**: `id`, `cid`, `usn`, `ease` (1=Again..4=Easy), `ivl`, `lastIvl` (camelCase!), `factor`, `time`, `type`

## FSRS Integration

```go
import "github.com/open-spaced-repetition/go-fsrs"

scheduler := fsrs.NewFSRS(fsrs.DefaultParameters())
card := fsrs.Card{
    Due:       time.Now(),
    Stability: 0.5,
    Difficulty: 5.0,
    // map from Anki DB card fields
}

result := scheduler.Repeat(card, time.Now())
// result[fsrs.RatingGood].Card has new Due, Stability, Difficulty
// Write result back to Anki DB (use result[rating].Card for the new state)
```

Key difference from v1 spec: `Repeat()` returns a `map[Rating]SchedulingInfo`, not a single result. Always call with the current time, not `time.Now` cached at function start.

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
Body: msgpack({k: <key>, v: <protocol_version>})
Response: zstd-compressed .anki2 database
```

### Full Upload
```
POST /sync/upload
Body: msgpack({k: <key>, v: <protocol_version>, data: <zstd-compressed-anki2>, ...})
Response: {"data": null, "err": ""}
```

### Incremental Delta Sync (v2)
```
POST /sync/sync
Body: msgpack({k: <key>, v: <protocol_version>, ...})
Response: streaming msgpack chunks of changes
```
Bidirectional: client sends local changes, server responds with remote changes. USN-based tracking with conflict resolution. Streamed via msgpack chunks to avoid loading full DB.

### Media Sync (v2)
```
GET  /msync/<session_key>/<filename>   # Download media file
POST /msync/<session_key>/<filename>   # Upload media file
```
Media files stored in `collection.media/` directory alongside the .anki2 file. Synced separately from card data.

### Protocol Version
Currently **11** (Anki v24.06+). Client must report valid Anki version string.

## HTTP API (Server Mode)

### Core Endpoints

| Method | Path | Description |
|--------|------|-------------|
| GET | `/health` | Health check |
| GET | `/api/v1/version` | Library version |
| GET | `/api/v1/decks` | List all decks |
| GET | `/api/v1/due-cards` | Get all due cards |
| GET | `/api/v1/cards/{id}` | Get card details |
| POST | `/api/v1/answer` | Answer a card (rating: again/hard/good/easy) |
| POST | `/api/v1/notes` | Create a new note |
| POST | `/api/v1/decks` | Create a new deck |
| GET | `/api/v1/stats` | Collection statistics |

### Sync Endpoints

| Method | Path | Description |
|--------|------|-------------|
| POST | `/api/v1/sync/download` | Pull full collection from AnkiWeb |
| POST | `/api/v1/sync/upload` | Push full collection to AnkiWeb |
| POST | `/api/v1/sync/delta` | Incremental delta sync (v2) |

### Media Endpoints (v2)

| Method | Path | Description |
|--------|------|-------------|
| GET | `/api/v1/media` | List media files |
| GET | `/api/v1/media/{filename}` | Download a media file |
| POST | `/api/v1/media/{filename}` | Upload a media file |
| DELETE | `/api/v1/media/{filename}` | Delete a media file |

### Multi-Collection Endpoints (v2)

| Method | Path | Description |
|--------|------|-------------|
| GET | `/api/v1/collections` | List available collections |

All endpoints (except `/health`) support optional `?collection=<name>` parameter for multi-collection mode. Collection registry maps names to `.anki2` paths; per-collection mutexes isolate concurrent sync operations.

### Response Format
- Success: JSON object or array
- Error: `{"error": "message"}`, with `collection.ErrNotFound` sentinel for 404s
- Auth: Bearer token via `Authorization` header, 401 returns `WWW-Authenticate: Bearer`
- Rate-limited: 429 with `Retry-After` header

## CLI (`anki-go`)

```bash
anki-go due [--deck <name>]        # Show due cards
anki-go answer <card-id> --rating good  # Answer a card
anki-go add-note --deck Default --model Basic --fields "Front|Back"
anki-go create-deck "My Deck"
anki-go stats                      # Collection statistics
anki-go sync                       # Sync with AnkiWeb
anki-go serve --port 8765          # Start HTTP API server
anki-go version                    # Print version
```

Built with Go 1.22+ `flag` package. Credentials via `ANKIWEB_USERNAME` / `ANKIWEB_PASSWORD` env vars. Port validation: `1 <= port <= 65535`.

## Testing Strategy

- Unit tests for each package (collection, scheduler, sync, apkg, types)
- Integration tests using real .anki2 fixtures
- Server integration tests (server_test.go, multicollection_test.go)
- CI via GitHub Actions (Go 1.22+, linux/mac/windows)
- `go test -race ./...` with race detector
- golangci-lint v2 strict (all errors checked, `_ =` pattern for unchecked returns)

## Development

### Build
```bash
cd /home/vipul/projects/go-anki
go build ./...
```

### Test
```bash
go test -race -v ./...
```

### Lint
```bash
golangci-lint run ./...
```

### PR Workflow
1. Branch: `feat/<name>` or `fix/<name>`
2. PR via `gh pr create`
3. `@claude review` → fix findings → re-review → merge ONLY on clean pass
4. Squash merge + delete branch
5. NEVER push to main directly
