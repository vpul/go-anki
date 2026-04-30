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
| Audit round 26 (5 bugs) | Claude Code review | `fix/audit-round26` | [#26](https://github.com/vpul/go-anki/pull/26) | ✅ Merged |
| Audit round 27 (labeled break) | Parallel QA review | `fix/audit-round27` | [#27](https://github.com/vpul/go-anki/pull/27) | ✅ Merged |

### What's Deferred (Future)
- Nothing — all features complete
