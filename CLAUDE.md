# go-anki — Headless Anki Client in Go

Single static Go binary that reads/writes Anki `.anki2` collections, schedules FSRS reviews, creates cards/decks, syncs with AnkiWeb — no Python, no Qt, no CGO.

## Architecture

```
pkg/types/       — Core types: Card, Note, Deck, Model, Rating, ReviewLog
pkg/collection/  — SQLite schema layer (modernc.org/sqlite, pure Go)
pkg/scheduler/   — FSRS via go-fsrs (github.com/open-spaced-repetition/go-fsrs)
pkg/sync/        — AnkiWeb sync (Login, SyncMeta, FullDownload, FullUpload)
pkg/apkg/        — .apkg export + .colpkg import (zstd via klauspost/compress)
server/          — HTTP API server (REST, Go 1.22+ method routing)
cmd/anki-go/     — CLI (due, answer, add-note, create-deck, stats, sync, serve, version)
```

## Key Constraints

- **Pure Go, no CGO** (`modernc.org/sqlite`)
- **FSRS via go-fsrs** — `params.Repeat(card, now)` returns `map[Rating]SchedulingInfo`, no `NewFSRS()` factory
- **AGPL-3.0** license
- **Go 1.23 directive** in go.mod with `toolchain go1.25.0` (golangci-lint compat)
- **Go 1.22+ method routing**: `mux.HandleFunc("GET /api/v1/decks", ...)`, `r.PathValue("id")`
- **Error responses**: `{"error": "message"}`, use `collection.ErrNotFound` sentinel for 404
- **MaxBytesReader** on all POST endpoints for DoS protection
- Use `r.Context()` not `context.Background()` in handlers
- **Endpoint paths as exported constants**: `SyncHostKeyURL`, `SyncMetaURL`, etc.

## Anki v18+ Schema (what's actually in the wild)

v18 stores decks/models/config as **separate tables with protobuf BLOB columns**, NOT JSON in `col` table:
- `decks` table: `id, name, mtime_secs, usn, common BLOB, kind BLOB`
- `notetypes` table: `id, name, mtime_secs, usn, config BLOB`
- `fields` table: `ntid, ord, name, config BLOB`
- `templates` table: `ntid, ord, name, mtime_secs, usn, config BLOB`
- `deck_config` table: `id, name, mtime_secs, usn, config BLOB`
- `tags` table: `tag TEXT PK, usn, collapsed, config BLOB`
- `config` table: `KEY TEXT PK, usn, mtime_secs, val BLOB`
- `graves` table: `oid, type, usn`

**Revlog column is `lastIvl` (camelCase), NOT `last_ivl`.**

## Critical Patterns (from audit history)

### DB
- `collection.Open()` validates file existence/size before `sql.Open()`
- DSN: `mode=rw` (not `rwc`) — no silent empty-DB creation
- `sync.Mutex` + `_busy_timeout=5000` for concurrent access (NOT `SetMaxOpenConns(1)` — causes deadlocks)
- Always check `rows.Err()` after `rows.Next()` loops
- Use `withMode(OpenMode, fn)` helper for per-request collection access

### Security
- `sanitizeErr()` for all HTTP error responses — check SQL/path patterns FIRST, then "not found"
- `WithAuthToken(token)` stores SHA-256 hash, `subtle.ConstantTimeCompare`
- 401 returns `WWW-Authenticate: Bearer`
- `io.LimitReader` on all sync downloads/uploads
- ZIP path traversal prevention: `validateMediaFilename()` + `validatePathWithinDir()`
- SSRF: `ssrfSafeDialContext` in `http.Transport.DialContext` re-validates IP at connect time
- Rate limiter: sliding window per IP, `maxIPs` cap (default 50k, zero = no limit)
- Server timeouts: 5s read, 10s write, 60s idle

### Error Handling (golangci-lint v2 strict)
- `defer col.Close()` → `defer func() { _ = col.Close() }()`
- `defer rows.Close()` → `defer func() { _ = rows.Close() }()`
- `db.Close()` → `_ = db.Close()`
- `rand.Read(b)` → `_, _ = rand.Read(b)`
- All unchecked returns must use `_ =`
- `.golangci.yml`: `version: "2"`, no standalone `typecheck` or `gosimple` (merged into `staticcheck`)

### CLI
- `reorderFlags()` before `flag.Parse()` — Go's `flag` stops at first positional arg
- Use `boolFlagsFor(fs)` (auto-detect bool flags), never hardcode
- `runX() error` pattern: logic returns error, `main()` calls `os.Exit(runCmd(runX))`
- Env var fallback for credentials: `envOr("ANKIWEB_PASSWORD", "")`
- Port validation: `1 <= port <= 65535`

### Sync
- `synckey` stored as `sessionKey` (not `hostKey`) for clarity
- Full download: extract to temp dir first, then `renameWithCopy()` to final location
- Zstd: `io.LimitReader` on BOTH compressed AND decompressed streams
- `syncMu` serializes concurrent download/upload operations
- Goroutine leak prevention: `pw.CloseWithError(err)` on `io.Pipe` error paths

### AnkiWeb Protocol
- Auth: `POST /sync/hostKey` → `{"key": "session_key"}`
- Download: msgpack `{k, v}` → zstd-compressed .anki2
- Upload: msgpack `{k, v, data}` → `{"data": null, "err": ""}`
- Protocol version: currently 11 (Anki v24.06+)

## Commands
```bash
# Build
cd /home/vipul/projects/go-anki && PATH=$PATH:/home/vipul/go/bin GOPATH=$HOME/gopath go build ./...

# Test with race detector
cd /home/vipul/projects/go-anki && PATH=$PATH:/home/vipul/go/bin GOPATH=$HOME/gopath go test -race -v ./...

# Go mod tidy
cd /home/vipul/projects/go-anki && PATH=$PATH:/home/vipul/go/bin GOPATH=$HOME/gopath go mod tidy

# Lint
golangci-lint run ./...
```

## PR Workflow
1. Branch: `feat/<name>` or `fix/<name>`
2. PR via `gh pr create`
3. `@claude review` → fix findings → re-review → merge ONLY on clean pass
4. Squash merge + delete branch
5. NEVER push to main directly

## v2 Features (from SPEC.md)
- [ ] Incremental delta sync (bidirectional changes — USN tracking, conflict resolution)
- [ ] Media sync (AnkiWeb `/msync/` API — images, audio files)
- [ ] AnkiWeb streaming sync (chunked transfers, avoid loading full DB into memory)
- [x] Multiple collection support (server handles multiple .anki2 files simultaneously)
