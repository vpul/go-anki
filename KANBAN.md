# go-anki v2 — Kanban Board (COMPLETE)

## ✅ All v2 Features Complete

| Feature | PR | Status | Key Files |
|---------|----|--------|-----------|
| Multi-collection support | #19 | ✅ Merged | `server/registry.go`, `server/server.go` |
| Media sync | #20 | ✅ Merged | `pkg/sync/media.go`, `pkg/sync/media_test.go` |
| Incremental delta sync | #21 | ✅ Merged | `pkg/sync/delta.go`, `pkg/collection/sync.go` |

### What Was Built

**PR #19 — Multi-collection:**
- CollectionRegistry with name validation + path resolution
- Server multi-collection routes at `/api/v1/collections/{name}/...`
- CLI `--collections` flag (mutually exclusive with `--db`)
- Backward compat preserved

**PR #20 — Media Sync:**
- Media sync client (MediaBegin, MediaDownload, MediaUpload, MediaSanity)
- AnkiWeb `/msync/` protocol with separate session key
- CLI: `anki-go sync media <download|upload|sanity>`
- 50MB size limits with `io.LimitReader`

**PR #21 — Incremental Delta Sync:**
- Collection USN-based change tracking (GetChanges, ApplyChanges, MarkSynced)
- Delta sync client (SyncStart, ApplyChanges, ApplyGraves, SyncFinish, FullSync)
- Pagination support for multi-page deltas
- Both v11 and v18+ schema support
- Conflict resolution: last-write-wins based on mod timestamp
- Server: `POST /api/v1/sync/delta` endpoint
- CLI: `anki-go sync delta` subcommand
- 17+ tests covering unit, integration, and pagination

### Current Sprint — ✅ Complete

| Task | Agent | Branch | PR | Status |
|------|-------|--------|----|--------|
| Streaming upload protocol | Claude Code | `feat/streaming-upload` | [#22](https://github.com/vpul/go-anki/pull/22) | ✅ Merged |
| Per-collection locks | OpenCode | `fix/per-collection-locks` | [#23](https://github.com/vpul/go-anki/pull/23) | ✅ Merged |
| Server delta sync test | Claude Code | `test/delta-server-test` | [#24](https://github.com/vpul/go-anki/pull/24) | ✅ Merged |

## 🔍 Code Review Sprint — April 29, 2026

### Results

| Agent | Method | Status | Findings |
|-------|--------|--------|----------|
| Claude Code | `claude -p` (max effort) | ✅ Complete | **29 findings** (4 critical, 7 high, 8 med, 10 low) |
| OpenCode | `opencode run` | ⏱ Timed out at 600s | — |
| Static Analysis | golangci-lint, go vet, go test -race | ✅ Complete | **0 issues** — all clean |

### Verified Real Bugs (after manual triage)

| # | Severity | Issue | File:Line |
|---|----------|-------|-----------|
| 1 | 🔴 Critical | Nil dereference — `col.Close()` called when `col` is nil | `server/server.go:519` |
| 2 | 🔴 Critical | `break` inside `switch` doesn't break outer `for` — protobuf parsing continues past corrupted data | `pkg/collection/schema.go:248,259` |
| 3 | 🟠 High | DSN not URL-encoded — paths with `?`, `#`, `&` corrupt SQLite DSN | `pkg/collection/collection.go:62-67` |
| 4 | 🟠 High | Media upload truncates before write + removes on partial failure — no atomic write | `server/server.go:1076-1091` |
| 5 | 🟡 Medium | Grave type values (1/2/3) shifted from Anki spec (0/1/2) — breaks cross-compat | `pkg/types/common.go:128` |

### Needs Discussion

| Finding | Assessment |
|---------|-----------|
| Card-ID collision on same-millisecond notes | Comment-acknowledged, "extremely unlikely" — theoretical edge case |
| randInt(1) overflow | ❌ False positive — `randInt` is never called with 1, handles it correctly anyway |
| Graves re-sent in pagination | Depends on USN behavior — needs integration test verification |

### What's Deferred (Future)
- Nothing — all features complete
