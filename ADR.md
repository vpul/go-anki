# Architecture Decision Records

## ADR-001: Pure Go (No CGO)

**Status**: Accepted

**Context**: Need to read/write Anki's .anki2 SQLite databases on a headless VPS (677MB RAM). Options:
1. `github.com/mattn/go-sqlite3` — CGO, needs C compiler, faster
2. `modernc.org/sqlite` — Pure Go, no C compiler, slightly slower
3. Use the Python `anki` library via subprocess — buggy, heavy, slow

**Decision**: Use `modernc.org/sqlite` (pure Go).

**Consequences**:
- Single static binary, cross-compile without toolchain
- No CGO = no build complexity, works on any platform
- ~2x slower than mattn for bulk ops, but Anki collections are small (<100MB)
- Compatible with the user's VPS that may not have a C compiler

---

## ADR-002: FSRS via go-fsrs (No custom SR algorithm)

**Status**: Accepted

**Context**: Need a spaced repetition algorithm. Options:
1. Implement SM-2 from scratch (trivial math, but outdated)
2. Use `github.com/open-spaced-repetition/go-fsrs` (official Go FSRS)
3. Use Anki's Python scheduler (requires Python runtime)

**Decision**: Use `go-fsrs`, the official Go implementation of FSRS.

**Consequences**:
- Same algorithm as modern Anki (v23.12+), results will match
- Well-maintained by the open-spaced-repetition org
- Produces intervals, stability, difficulty values compatible with Anki's schema
- Can extract FSRS parameters from Anki's deck config JSON for per-deck tuning

---

## ADR-003: Full Download/Upload Sync (No Incremental Delta)

**Status**: Accepted

**Context**: Need AnkiWeb sync. Options:
1. Full incremental delta sync (8-10 hrs, complex protocol, USN tracking)
2. Full download/upload only (3-4 hrs, simpler)
3. No sync, .apkg import/export only

**Decision**: v1 implements full download/upload only. Incremental sync deferred to v2.

**Consequences**:
- Covers 95% of use cases: pull before query, push after changes
- Before querying: download from AnkiWeb → read collection
- After changes: upload entire collection to AnkiWeb → AnkiDroid syncs
- Risk: if user reviews on both phone and server between syncs, last-write-wins
- Mitigation: always download before making changes, upload immediately after
- Incremental sync can be added in v2 when there's user demand

---

## ADR-004: Avoid Anki's Unicase Collation

**Status**: Accepted

**Context**: Anki's `notes` table uses `COLLATE unicase` on `sfld` and `csum` fields. The Python `anki` library's SQLite implementation doesn't properly handle this collation, causing index corruption on writes.

**Decision**: Never use `COLLATE unicase` in our queries. Instead:
- For lookups that would use unicase: use separate SELECT queries with dict mapping
- For `csum` field: compute CRC32 checksums ourselves
- For `sfld` (sort field): extract from `flds` by taking the first field

**Consequences**:
- Zero risk of DB corruption
- Slightly less efficient queries (can't JOIN on unicase columns)
- But perfectly safe writes since we never trigger the buggy collation
- Our writes set proper `mod` timestamps and `usn = -1` (not yet synced)

---

## ADR-005: AGPL-3.0 License

**Status**: Accepted

**Context**: Choosing an open-source license for go-anki.

**Decision**: AGPL-3.0 (Affero General Public License v3).

**Rationale**:
- Anki itself is AGPL-3.0 — matching license ensures compatibility
- Since we read/write Anki's data format and reference their protocol, AGPL is safest legally
- Any fork or modification must remain open-source (copyleft)
- Network use clause (AGPL vs GPL) means even SaaS usage requires sharing source

**Consequences**:
- Users must provide source code if they distribute or offer network use
- Cannot be used in proprietary closed-source software
- Compatible with other AGPL projects

---

## ADR-006: HTTP API for AI/MCP Integration

**Status**: Accepted

**Context**: Primary use case is AI agent integration (Hermes). The agent needs to query Anki via an HTTP endpoint.

**Decision**: Implement an HTTP API server (`server/`) alongside the library packages.

**Consequences**:
- The library packages (`pkg/`) are usable standalone without the server
- The server is a thin wrapper: `go-anki serve --port 8765 --db /path/to/collection.anki2`
- API follows REST conventions with JSON request/response
- This is how Hermes currently integrates via MCP (through a bridge server)

---

## ADR-007: Zstd + Msgpack for Sync Wire Format

**Status**: Accepted

**Context**: AnkiWeb sync protocol uses msgpack for serialization and zstd for compression. We need to implement both.

**Decision**: Use `github.com/vmihailenco/msgpack/v5` for msgpack and `github.com/klauspost/compress/zstd` for compression.

**Consequences**:
- Both are mature, well-maintained Go libraries
- msgpack gives us binary efficiency over JSON
- zstd matches what AnkiWeb expects for upload/download payloads
- The .colpkg format (ZIP of zstd-compressed .anki21b) also uses zstd