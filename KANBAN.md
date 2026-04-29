# go-anki v2 — Kanban Board

> Shared task board for autonomous multi-agent v2 implementation.
> Agents: Update this file after completing each task.

## Legend
- [ ] Pending  |  ◐ In Progress  |  ✅ Done  |  ❌ Blocked

---

## Feature 1: AnkiWeb Streaming Sync (Chunked Transfers)

**Status:** ◐ In Progress
**Branch:** `feat/streaming-sync`
**PR:** Not yet created

| Task | Agent | Status | Notes |
|------|-------|--------|-------|
| PLAN: Design chunked transfer approach | Architect | ✅ Done | See PLAN.md §1 |
| IMPL: Streaming download with io.Pipe | Implementer | ◐ In Progress | |
| IMPL: Streaming upload with chunked reader | Implementer | ◐ In Progress | |
| IMPL: Progress reporting for large transfers | Implementer | ⏳ Pending | |
| TEST: Unit tests for streaming | Implementer | ⏳ Pending | |
| TEST: Integration test (mocked server) | Implementer | ⏳ Pending | |
| CLI: --progress flag for sync commands | Implementer | ⏳ Pending | |
| REVIEW: Spec compliance + code quality | Reviewer | ⏳ Pending | |
| PR: Create and merge | Orchestrator | ⏳ Pending | |

---

## Feature 2: Incremental Delta Sync

**Status:** ❌ Blocked on streaming sync
**Branch:** `feat/delta-sync`
**PR:** Not yet created

| Task | Agent | Status | Notes |
|------|-------|--------|-------|
| PLAN: Design USN tracking + conflict resolution | Architect | ✅ Done | See PLAN.md §2 |
| IMPL: USN tracking in collection layer | Implementer | ⏳ Pending | |
| IMPL: Delta extraction (cards, notes, decks, graves) | Implementer | ⏳ Pending | |
| IMPL: AnkiWeb delta protocol (start/applyChanges/applyGraves/finish) | Implementer | ⏳ Pending | |
| IMPL: Conflict resolution (server-wins or client-wins) | Implementer | ⏳ Pending | |
| TEST: Unit tests for delta generation | Implementer | ⏳ Pending | |
| TEST: Integration test with mock AnkiWeb server | Implementer | ⏳ Pending | |
| CLI: sync command enhancement for delta | Implementer | ⏳ Pending | |
| Server: sync endpoints for delta | Implementer | ⏳ Pending | |
| REVIEW: Spec compliance + code quality | Reviewer | ⏳ Pending | |
| PR: Create and merge | Orchestrator | ⏳ Pending | |

---

## Progress Summary

| Feature | Architecture | Implementation | Tests | Review | PR |
|---------|-------------|----------------|-------|--------|-----|
| Multi-collection (#19) | ✅ | ✅ | ✅ | ✅ | ✅ Merged |
| Media sync (#20) | ✅ | ✅ | ✅ | ✅ (CI) | ✅ Merged |
| Streaming sync | ✅ | ◐ | ☐ | ☐ | ☐ |
| Delta sync | ✅ | ☐ | ☐ | ☐ | ☐ |
