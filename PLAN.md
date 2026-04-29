# go-anki v2 — Implementation Plan

> For streaming sync (chunked transfers) and incremental delta sync.

---

## §1 — AnkiWeb Streaming Sync

### Current State
- `FullDownload()` reads full response → `io.ReadAll` → decompress zstd in memory → write .anki2 file
- `FullUpload()` reads full DB → compress zstd in memory → `io.ReadAll` → send as msgpack
- Both load the ENTIRE database into RAM before processing. For large collections (50MB+), this wastes memory.

### Design

**Download streaming:**
Replace `io.ReadAll(resp.Body)` → `zstd.Decompress()` with:
```go
// Use zstd.NewReader for streaming decompression
// Write directly to temp file as chunks arrive
zstdReader, err := zstd.NewReader(io.LimitReader(resp.Body, maxDownloadSize+1))
// ... stream directly to temp file via io.Copy
```

**Upload streaming:**
Replace `io.ReadAll(dbFile)` → `zstd.Compress()` → `msgpack.Marshal()` with:
```go
// Use io.Pipe to stream DB read → zstd compress → HTTP request body
pr, pw := io.Pipe()
go func() {
    zstdWriter, _ := zstd.NewWriter(pw)
    _, _ = io.Copy(zstdWriter, dbFileReader)
    _ = zstdWriter.Close()
    _ = pw.Close()
}()
req.Body = pr
```

**Files to modify:**
- `pkg/sync/sync.go` — `FullDownload()` and `FullUpload()`
- `pkg/apkg/zstd.go` — Add `NewStreamingZstdReader()` helper if needed
- `server/server.go` — Add `POST /api/v1/sync/stream/download` and `POST /api/v1/sync/stream/upload` optional endpoints

**Security:**
- `io.LimitReader` on BOTH compressed AND decompressed streams (zstd bomb protection)
- Verify checksum after streaming download
- Keep existing `maxDownloadSize` (500MB) and `maxUploadSize` limits

---

## §2 — Incremental Delta Sync

### Current State
- Full download/upload only: entire DB is transferred on every sync
- No USN tracking — objects use `usn = -1` for "not synced"
- `graves` table exists but is never populated
- ADR-003 explicitly deferred this to v2

### Design

#### USN Tracking
Each object (card, note, deck, deck_config, notetypes) has a `usn` field. When synced to AnkiWeb:
- `usn` = server-assigned update sequence number (> 0)
- `usn` = -1 for local-only changes (need upload)

**New file: `pkg/sync/delta.go`**

```go
// DeltaSyncClient extends Client with incremental sync operations.
type DeltaSyncClient struct {
    *Client
}

// SyncManifest describes the state of a collection for sync.
type SyncManifest struct {
    Scm     int64 // Server collection modification time
    Usn     int   // Last update sequence number
    Cards   int   // Card count
    Notes   int   // Note count
    Decks   int   // Deck count
    UsnMap  map[string]int  // Per-table USN tracking
}

// Delta holds changes for a sync round-trip.
type Delta struct {
    Cards    []types.Card
    Notes    []types.Note
    Decks    []types.Deck
    Graves   []types.Grave
    Usn      int   // After applying this delta
    Finished bool  // True if no more changes
}
```

#### AnkiWeb Protocol Enhancement

Current: `POST /sync/download` → full .anki2 file
New delta endpoints:
1. `POST /sync/start` — Begin session, returns `{data: {scm: int, usn: int, hostNum: int}}`
2. `POST /sync/applyChanges` — Upload local changes, returns `{data: {changes: [...], usn: int, ...}}`  
3. `POST /sync/applyGraves` — Upload deletions
4. `POST /sync/finish` — End session, returns `{data: {scm: int, usn: int}}`

Payload format (msgpack):
```go
// Request (upload changes)
{
    "k": sessionKey,
    "v": protocolVersion,
    "data": msgpack([]interface{}{
        {"kind": "card", "id": 123, "mod": 1.0, "usn": -1, "data": {...}},
        {"kind": "note", "id": 456, "mod": 1.0, "usn": -1, "data": {...}},
    }),
}

// Response (download changes)
{
    "data": {
        "changes": msgpack([...objects]),
        "usn": 50,
        "more": false,
    }
}
```

#### Conflict Resolution

Strategy: **Last-write-wins based on `mod` timestamp**
- If server's `mod` > local `mod` → server wins (discard local change)
- If local's `mod` > server's `mod` → local wins (keep local change)  
- If equal → server wins (avoid conflicts, consistent with AnkiWeb behavior)

#### Collection Layer Changes (`pkg/collection/`)

New functions (or in new file `pkg/collection/sync.go`):
```go
// GetChanges returns all objects modified since the given USN.
func (c *Collection) GetChanges(sinceUSN int) (*Delta, error)

// ApplyChanges applies a delta of remote changes to the collection.
func (c *Collection) ApplyChanges(delta *Delta) error

// GetSyncState returns the sync state for delta negotiation.
func (c *Collection) GetSyncState() (*SyncState, error)
```

#### Files to create/modify:

| File | Action | Purpose |
|------|--------|---------|
| `pkg/sync/delta.go` | Create | Delta sync client, USN tracking, protocol |
| `pkg/sync/delta_test.go` | Create | Tests for delta sync |
| `pkg/collection/sync.go` | Create | USN-based change tracking, conflict resolution |
| `pkg/collection/sync_test.go` | Create | Tests for change tracking |
| `pkg/types/common.go` | Modify | Add `Grave` type, `SyncState` type |
| `cmd/anki-go/main.go` | Modify | `sync delta` subcommand |
| `server/server.go` | Modify | `POST /api/v1/sync/delta` endpoints |
| `KANBAN.md` | Modify | Update progress |

#### Testing Strategy

1. Unit tests for USN tracking (create objects → check USN)
2. Unit tests for GetChanges (modify objects → verify delta)
3. Unit tests for ApplyChanges (apply delta → verify objects)
4. Unit tests for conflict resolution (server wins vs local wins)
5. Integration test with mock AnkiWeb server (full delta sync round-trip)
6. Regression: all existing tests still pass

#### CLI Usage

```bash
# One-shot delta sync (like git pull + push)
anki-go sync delta --username= --db=path

# With auto-commit
anki-go sync delta --commit
```

#### Security Considerations

- All HTTP responses use `io.LimitReader` (1MB for metadata, 50MB for bulk data)
- `sanitizeErr()` on all error responses
- `r.Context()` for request cancellation
- `syncMu` for serializing delta operations
- SSRF-safe `DialContext` for all connections
