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

### Current Sprint

| Task | Agent | Branch | Status |
|------|-------|--------|--------|
| Streaming upload protocol | Claude Code | `feat/streaming-upload` | 🔄 In Progress |
| Per-collection locks | OpenCode | `fix/per-collection-locks` | 🔄 In Progress |
| Server delta sync test | Claude Code | `test/delta-server-test` | 🔄 In Progress |
| Code review all | Review Agent | — | ⏳ Pending |

### What's Deferred (Future)
- Full media upload/download implementation (API scaffolding in place)
