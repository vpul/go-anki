package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/vpul/go-anki/pkg/collection"
	"github.com/vpul/go-anki/pkg/scheduler"
	goanki "github.com/vpul/go-anki/pkg/types"
	"github.com/vpul/go-anki/pkg/sync"
	"github.com/vpul/go-anki/server"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "due":
		cmdDue()
	case "answer":
		cmdAnswer()
	case "add-note":
		cmdAddNote()
	case "create-deck":
		cmdCreateDeck()
	case "stats":
		cmdStats()
	case "sync":
		cmdSync()
	case "serve":
		cmdServe()
	case "version":
		fmt.Println("go-anki/1.0.0")
	case "help", "--help", "-h":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", os.Args[1])
		printUsage()
		os.Exit(1)
	}
}

// printUsage displays help text for all available commands.
func printUsage() {
	fmt.Fprint(os.Stderr, `Usage: anki-go <command> [options]

Commands:
  due          Show cards due for review
  answer       Answer a card with a rating
  add-note     Add a new note
  create-deck  Create a new deck
  stats        Show collection statistics
  sync         Sync with AnkiWeb (download/upload)
  serve        Start the HTTP API server
  version      Print version

Use "anki-go <command> --help" for more information about a command.
`)
}

// cmdDue lists cards that are due for review.
func cmdDue() {
	fs := flag.NewFlagSet("due", flag.ExitOnError)
	db := fs.String("db", "collection.anki2", "path to collection database")
	deck := fs.String("deck", "", "filter by deck name")
	limit := fs.Int("limit", 20, "maximum number of cards to show")
	jsonOut := fs.Bool("json", false, "output as JSON")
	if err := fs.Parse(os.Args[2:]); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	col, err := collection.Open(*db, collection.ReadOnly)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: open collection: %v\n", err)
		os.Exit(1)
	}
	defer func() { _ = col.Close() }()

	filter := goanki.DueCardsFilter{
		DeckName: *deck,
		Limit:    *limit,
	}

	cards, err := col.GetDueCards(filter)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: get due cards: %v\n", err)
		os.Exit(1)
	}

	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(cards); err != nil {
			fmt.Fprintf(os.Stderr, "error: encode JSON: %v\n", err)
			os.Exit(1)
		}
		return
	}

	if len(cards) == 0 {
		fmt.Println("No cards due.")
		return
	}

	for _, c := range cards {
		deckName := c.DeckName
		if deckName == "" {
			deckName = fmt.Sprintf("deck:%d", c.DID)
		}
		fmt.Printf("Card %d [%s] Due: %d Reps: %d IVL: %d\n",
			c.ID, deckName, c.Due, c.Reps, c.IVL)
	}
}

// cmdAnswer processes a card answer with a rating.
func cmdAnswer() {
	fs := flag.NewFlagSet("answer", flag.ExitOnError)
	db := fs.String("db", "collection.anki2", "path to collection database")
	ratingStr := fs.String("rating", "", "rating: again, hard, good, or easy")
	if err := fs.Parse(os.Args[2:]); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "error: card ID is required")
		fmt.Fprintln(os.Stderr, "Usage: anki-go answer <card-id> --rating=again|hard|good|easy [--db=path]")
		os.Exit(1)
	}

	cardID, err := strconv.ParseInt(fs.Arg(0), 10, 64)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: invalid card ID %q: %v\n", fs.Arg(0), err)
		os.Exit(1)
	}

	switch *ratingStr {
	case "again", "hard", "good", "easy":
		// valid
	default:
		fmt.Fprintf(os.Stderr, "error: --rating must be one of: again, hard, good, easy (got %q)\n", *ratingStr)
		os.Exit(1)
	}

	rating := goanki.ParseRating(*ratingStr)

	col, err := collection.Open(*db, collection.ReadWrite)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: open collection: %v\n", err)
		os.Exit(1)
	}
	defer func() { _ = col.Close() }()

	answer, err := col.AnswerCard(cardID, rating, scheduler.NewFSRSScheduler())
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: answer card: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Card %d answered with %s\n", answer.Card.ID, rating)
	fmt.Printf("  Queue: %s  Due: %d  IVL: %d  Factor: %d  Reps: %d\n",
		answer.Card.Queue, answer.Card.Due, answer.Card.IVL, answer.Card.Factor, answer.Card.Reps)
}

// cmdAddNote creates a new note in the collection.
func cmdAddNote() {
	fs := flag.NewFlagSet("add-note", flag.ExitOnError)
	db := fs.String("db", "collection.anki2", "path to collection database")
	deckName := fs.String("deck", "", "deck name (required)")
	modelName := fs.String("model", "", "note type/model name (required)")
	fieldsRaw := fs.String("fields", "", "fields as Front:Back or Front\\x1fBack (required)")
	tagsRaw := fs.String("tags", "", "comma-separated tags")
	if err := fs.Parse(os.Args[2:]); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	if *deckName == "" {
		fmt.Fprintln(os.Stderr, "error: --deck is required")
		os.Exit(1)
	}
	if *modelName == "" {
		fmt.Fprintln(os.Stderr, "error: --model is required")
		os.Exit(1)
	}
	if *fieldsRaw == "" {
		fmt.Fprintln(os.Stderr, "error: --fields is required")
		os.Exit(1)
	}

	// Parse fields: "Front:Back" or "Front\x1fBack"
	var fields map[string]string
	if strings.Contains(*fieldsRaw, ":") {
		// Key-value format: "Front:text:Back:text2"
		fields = make(map[string]string)
		pairs := strings.Split(*fieldsRaw, "\x1f")
		for _, pair := range pairs {
			kv := strings.SplitN(pair, ":", 2)
			if len(kv) == 2 {
				fields[kv[0]] = kv[1]
			} else {
				fmt.Fprintf(os.Stderr, "error: invalid field pair %q (expected key:value)\n", pair)
				os.Exit(1)
			}
		}
	} else {
		fmt.Fprintln(os.Stderr, "error: --fields must be in key:value format (e.g., Front:text)")
		os.Exit(1)
	}

	// Parse tags
	var tags []string
	if *tagsRaw != "" {
		tags = strings.Split(*tagsRaw, ",")
		for i, t := range tags {
			tags[i] = strings.TrimSpace(t)
		}
	}

	input := goanki.NewNote{
		DeckName:  *deckName,
		ModelName: *modelName,
		Fields:    fields,
		Tags:      tags,
	}

	col, err := collection.Open(*db, collection.ReadWrite)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: open collection: %v\n", err)
		os.Exit(1)
	}
	defer func() { _ = col.Close() }()

	noteID, err := col.AddNote(input)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: add note: %v\n", err)
		os.Exit(1)
	}

	fmt.Println(noteID)
}

// cmdCreateDeck creates a new deck in the collection.
func cmdCreateDeck() {
	fs := flag.NewFlagSet("create-deck", flag.ExitOnError)
	db := fs.String("db", "collection.anki2", "path to collection database")
	if err := fs.Parse(os.Args[2:]); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "error: deck name is required")
		fmt.Fprintln(os.Stderr, "Usage: anki-go create-deck <name> [--db=path]")
		os.Exit(1)
	}
	deckName := fs.Arg(0)

	col, err := collection.Open(*db, collection.ReadWrite)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: open collection: %v\n", err)
		os.Exit(1)
	}
	defer func() { _ = col.Close() }()

	deckID, err := col.CreateDeck(deckName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: create deck: %v\n", err)
		os.Exit(1)
	}

	fmt.Println(deckID)
}

// cmdStats shows collection statistics.
func cmdStats() {
	fs := flag.NewFlagSet("stats", flag.ExitOnError)
	db := fs.String("db", "collection.anki2", "path to collection database")
	jsonOut := fs.Bool("json", false, "output as JSON")
	if err := fs.Parse(os.Args[2:]); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	col, err := collection.Open(*db, collection.ReadOnly)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: open collection: %v\n", err)
		os.Exit(1)
	}
	defer func() { _ = col.Close() }()

	stats, err := col.GetStats()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: get stats: %v\n", err)
		os.Exit(1)
	}

	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(stats); err != nil {
			fmt.Fprintf(os.Stderr, "error: encode JSON: %v\n", err)
			os.Exit(1)
		}
		return
	}

	fmt.Printf("total_cards=%d\n", stats.TotalCards)
	fmt.Printf("total_notes=%d\n", stats.TotalNotes)
	fmt.Printf("due_cards=%d\n", stats.DueCards)
	fmt.Printf("new_cards=%d\n", stats.NewCards)
	fmt.Printf("learning_cards=%d\n", stats.LearningCards)
	fmt.Printf("review_cards=%d\n", stats.ReviewCards)
	fmt.Printf("total_decks=%d\n", stats.TotalDecks)
	fmt.Printf("total_models=%d\n", stats.TotalModels)
	fmt.Printf("total_reviews=%d\n", stats.TotalReviews)
}

// cmdSync dispatches sync subcommands (download, upload).
func cmdSync() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "Usage: anki-go sync <download|upload> [options]")
		os.Exit(1)
	}

	switch os.Args[2] {
	case "download":
		cmdSyncDownload()
	case "upload":
		cmdSyncUpload()
	default:
		fmt.Fprintf(os.Stderr, "unknown sync subcommand: %s\n", os.Args[2])
		fmt.Fprintln(os.Stderr, "Usage: anki-go sync <download|upload> [options]")
		os.Exit(1)
	}
}

// cmdSyncDownload downloads the full collection from AnkiWeb.
func cmdSyncDownload() {
	// Parse flags starting after "anki-go sync download"
	fs := flag.NewFlagSet("sync download", flag.ExitOnError)
	db := fs.String("db", "collection.anki2", "path to collection database")
	media := fs.String("media", "collection.media", "media directory path")
	username := fs.String("username", "", "AnkiWeb username")
	password := fs.String("password", "", "AnkiWeb password")
	if err := fs.Parse(os.Args[3:]); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	if *username == "" || *password == "" {
		fmt.Fprintln(os.Stderr, "error: --username and --password are required for sync")
		os.Exit(1)
	}

	client := sync.NewClient(goanki.SyncConfig{
		Username: *username,
		Password: *password,
	})

	ctx := context.Background()

	if err := client.Authenticate(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "error: authenticate: %v\n", err)
		os.Exit(1)
	}

	result, err := client.FullDownload(ctx, *db, *media)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: download: %v\n", err)
		os.Exit(1)
	}

	// Open the downloaded collection to count cards
	col, openErr := collection.Open(*db, collection.ReadOnly)
	if openErr != nil {
		fmt.Fprintf(os.Stderr, "warning: could not open downloaded collection: %v\n", openErr)
		fmt.Printf("Downloaded to %s\n", result.DBPath)
		return
	}
	defer func() { _ = col.Close() }()

	stats, statsErr := col.GetStats()
	if statsErr != nil {
		fmt.Fprintf(os.Stderr, "warning: could not read stats: %v\n", statsErr)
		fmt.Printf("Downloaded to %s\n", result.DBPath)
		return
	}

	fmt.Printf("%d\n", stats.TotalCards)
}

// cmdSyncUpload uploads the full collection to AnkiWeb.
func cmdSyncUpload() {
	// Parse flags starting after "anki-go sync upload"
	fs := flag.NewFlagSet("sync upload", flag.ExitOnError)
	db := fs.String("db", "collection.anki2", "path to collection database")
	media := fs.String("media", "collection.media", "media directory path")
	username := fs.String("username", "", "AnkiWeb username")
	password := fs.String("password", "", "AnkiWeb password")
	if err := fs.Parse(os.Args[3:]); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	if *username == "" || *password == "" {
		fmt.Fprintln(os.Stderr, "error: --username and --password are required for sync")
		os.Exit(1)
	}

	client := sync.NewClient(goanki.SyncConfig{
		Username: *username,
		Password: *password,
	})

	ctx := context.Background()

	if err := client.Authenticate(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "error: authenticate: %v\n", err)
		os.Exit(1)
	}

	if err := client.FullUpload(ctx, *db, *media); err != nil {
		fmt.Fprintf(os.Stderr, "error: upload: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("ok")
}

// cmdServe starts the HTTP API server.
func cmdServe() {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	db := fs.String("db", "collection.anki2", "path to collection database")
	media := fs.String("media", "collection.media", "media directory path")
	port := fs.Int("port", 8765, "HTTP server port")
	username := fs.String("username", "", "AnkiWeb username (optional, enables sync endpoints)")
	password := fs.String("password", "", "AnkiWeb password (optional, enables sync endpoints)")
	if err := fs.Parse(os.Args[2:]); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	opts := []server.ServerOption{
		server.WithPort(*port),
		server.WithMediaDir(*media),
		server.WithScheduler(scheduler.NewFSRSScheduler()),
	}

	if *username != "" && *password != "" {
		opts = append(opts, server.WithSyncConfig(goanki.SyncConfig{
			Username: *username,
			Password: *password,
		}))
	}

	srv := server.NewServer(*db, opts...)

	if err := srv.ListenAndServe(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}