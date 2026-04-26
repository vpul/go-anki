# go-anki

A headless Anki client library in Go. Read/write Anki collections, schedule reviews with FSRS, create cards and decks, and sync with AnkiWeb — all without needing Anki Desktop.

## Why?

Every existing Anki integration requires either Anki Desktop (AnkiConnect), the buggy Python `anki` library, or AnkiDroid (no API). `go-anki` is the first standalone, headless Anki client that gives you programmatic access to:

- **Read** due cards, decks, notes, and models
- **Answer** cards with FSRS spaced repetition scheduling
- **Create** new notes, cards, and decks
- **Sync** with AnkiWeb (full download/upload)
- **Export/Import** .apkg and .colpkg files

Single static binary. No Python. No Qt. No GUI. No CGO.

## Installation

```bash
go get github.com/vpul/go-anki
```

## Quick Start

```go
package main

import (
    "fmt"
    "log"
    "time"

    goanki "github.com/vpul/go-anki/pkg/collection"
    "github.com/vpul/go-anki/pkg/scheduler"
)

func main() {
    // Open an Anki collection
    col, err := goanki.Open("/path/to/collection.anki2")
    if err != nil {
        log.Fatal(err)
    }
    defer col.Close()

    // Get due cards
    cards, err := col.GetDueCards(goanki.DueCardsFilter{DeckName: "Basic piano note names"})
    if err != nil {
        log.Fatal(err)
    }

    for _, card := range cards {
        fmt.Printf("Card %d: %s (due: %s)\n", card.ID, card.Question, card.Due.Format("2006-01-02"))
    }

    // Answer a card with FSRS scheduling
    sched := scheduler.NewFSRSScheduler()
    result, err := sched.Answer(card, scheduler.RatingGood, time.Now())
    if err != nil {
        log.Fatal(err)
    }

    // Write the updated card back
    err = col.UpdateCard(result.Card)
    if err != nil {
        log.Fatal(err)
    }

    // Create a new deck and note
    deckID, err := col.CreateDeck("Marine Biology")
    noteID, err := col.AddNote(goanki.NewNote{
        DeckName:  "Marine Biology",
        ModelName: "Basic",
        Fields:    map[string]string{"Front": "What is a cetacean?", "Back": "A marine mammal"},
        Tags:      []string{"marine", "mammals"},
    })
}
```

## HTTP API Server

```bash
# Start the server
go run ./server --port 8765 --db /path/to/collection.anki2

# Query due cards
curl http://localhost:8765/api/v1/decks/Basic%20piano%20note%20names/cards/due

# Answer a card
curl -X POST http://localhost:8765/api/v1/cards/1234/answer \
  -H "Content-Type: application/json" \
  -d '{"rating": "good"}'

# Sync with AnkiWeb
curl -X POST http://localhost:8765/api/v1/sync/download \
  -d '{"username": "user", "password": "pass"}'
```

## Architecture

| Package | Description |
|---------|-------------|
| `pkg/types` | Core data types (Card, Note, Deck, Model) |
| `pkg/collection` | SQLite schema layer — read/write Anki .anki2 databases |
| `pkg/scheduler` | FSRS spaced repetition scheduling |
| `pkg/sync` | AnkiWeb sync client (full download/upload) |
| `pkg/apkg` | .apkg export and .colpkg import |
| `server` | HTTP API server for AI/MCP integration |

See [SPEC.md](SPEC.md) for full specification and [ADR.md](ADR.md) for architecture decisions.

## Development

```bash
# Run all tests
go test ./...

# Run with race detector
go test -race ./...

# Run tests for a specific package
go test ./pkg/collection/...
```

## License

AGPL-3.0 — see [LICENSE](LICENSE) for details. This matches Anki's own license for compatibility.