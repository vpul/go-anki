package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

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

	var exitCode int
	switch os.Args[1] {
	case "due":
		exitCode = runCmd(runDue)
	case "answer":
		exitCode = runCmd(runAnswer)
	case "add-note":
		exitCode = runCmd(runAddNote)
	case "create-deck":
		exitCode = runCmd(runCreateDeck)
	case "stats":
		exitCode = runCmd(runStats)
	case "sync":
		cmdSync()
	case "serve":
		exitCode = runCmd(runServe)
	case "version":
		fmt.Println("go-anki/1.0.0")
	case "help", "--help", "-h":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", os.Args[1])
		printUsage()
		exitCode = 1
	}
	os.Exit(exitCode)
}

// runCmd executes a function that returns an error, printing to stderr and
// returning 1 on error. This ensures deferred Close() calls run before os.Exit.
func runCmd(fn func() error) int {
	if err := fn(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	return 0
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

// runDue lists cards that are due for review.
func runDue() error {
	fs := flag.NewFlagSet("due", flag.ExitOnError)
	db := fs.String("db", "collection.anki2", "path to collection database")
	deck := fs.String("deck", "", "filter by deck name")
	limit := fs.Int("limit", 20, "maximum number of cards to show")
	jsonOut := fs.Bool("json", false, "output as JSON")
	if err := fs.Parse(reorderFlags(os.Args[2:], boolFlagsFor(fs))); err != nil {
		return fmt.Errorf("parse flags: %w", err)
	}

	col, err := collection.Open(*db, collection.ReadOnly)
	if err != nil {
		return fmt.Errorf("open collection: %w", err)
	}
	defer func() { _ = col.Close() }()

	filter := goanki.DueCardsFilter{
		DeckName: *deck,
		Limit:    *limit,
	}

	cards, err := col.GetDueCards(filter)
	if err != nil {
		return fmt.Errorf("get due cards: %w", err)
	}

	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(cards)
	}

	if len(cards) == 0 {
		fmt.Println("No cards due.")
		return nil
	}

	for _, c := range cards {
		deckName := c.DeckName
		if deckName == "" {
			deckName = fmt.Sprintf("deck:%d", c.DID)
		}
		fmt.Printf("Card %d [%s] Due: %d Reps: %d IVL: %d\n",
			c.ID, deckName, c.Due, c.Reps, c.IVL)
	}
	return nil
}

// runAnswer processes a card answer with a rating.
func runAnswer() error {
	fs := flag.NewFlagSet("answer", flag.ExitOnError)
	db := fs.String("db", "collection.anki2", "path to collection database")
	cardIDStr := fs.String("card", "", "card ID to answer")
	ratingStr := fs.String("rating", "", "rating: again, hard, good, or easy")
	if err := fs.Parse(reorderFlags(os.Args[2:], boolFlagsFor(fs))); err != nil {
		return fmt.Errorf("parse flags: %w", err)
	}

	if *cardIDStr == "" {
		return fmt.Errorf("--card is required; usage: anki-go answer --card <card-id> --rating <again|hard|good|easy> [--db=path]")
	}

	cardID, err := strconv.ParseInt(*cardIDStr, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid card ID %q: %w", *cardIDStr, err)
	}

	switch *ratingStr {
	case "again", "hard", "good", "easy":
		// valid
	default:
		return fmt.Errorf("rating must be one of: again, hard, good, easy (got %q)", *ratingStr)
	}

	rating := goanki.ParseRating(*ratingStr)

	col, err := collection.Open(*db, collection.ReadWrite)
	if err != nil {
		return fmt.Errorf("open collection: %w", err)
	}
	defer func() { _ = col.Close() }()

	answer, err := col.AnswerCard(cardID, rating, scheduler.NewFSRSScheduler())
	if err != nil {
		return fmt.Errorf("answer card: %w", err)
	}

	fmt.Printf("Card %d answered with %s\n", answer.Card.ID, rating)
	fmt.Printf("  Queue: %s  Due: %d  IVL: %d  Factor: %d  Reps: %d\n",
		answer.Card.Queue, answer.Card.Due, answer.Card.IVL, answer.Card.Factor, answer.Card.Reps)
	return nil
}

// runAddNote creates a new note in the collection.
func runAddNote() error {
	fs := flag.NewFlagSet("add-note", flag.ExitOnError)
	db := fs.String("db", "collection.anki2", "path to collection database")
	deckName := fs.String("deck", "", "deck name (required)")
	modelName := fs.String("model", "", "note type/model name (required)")
	fieldsRaw := fs.String("fields", "", "fields as comma-separated key=value pairs (e.g., Front=Hello,Back=World)")
	tagsRaw := fs.String("tags", "", "comma-separated tags")
	if err := fs.Parse(reorderFlags(os.Args[2:], boolFlagsFor(fs))); err != nil {
		return fmt.Errorf("parse flags: %w", err)
	}

	if *deckName == "" {
		return fmt.Errorf("--deck is required")
	}
	if *modelName == "" {
		return fmt.Errorf("--model is required")
	}
	if *fieldsRaw == "" {
		return fmt.Errorf("--fields is required")
	}

	// Parse fields: comma-separated key=value pairs
	fields := make(map[string]string)
	pairs := strings.Split(*fieldsRaw, ",")
	for _, pair := range pairs {
		kv := strings.SplitN(pair, "=", 2)
		if len(kv) != 2 {
			return fmt.Errorf("invalid field %q (expected key=value)", pair)
		}
		fields[strings.TrimSpace(kv[0])] = strings.TrimSpace(kv[1])
	}
	if len(fields) == 0 {
		return fmt.Errorf("--fields must contain at least one key=value pair")
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
		return fmt.Errorf("open collection: %w", err)
	}
	defer func() { _ = col.Close() }()

	noteID, err := col.AddNote(input)
	if err != nil {
		return fmt.Errorf("add note: %w", err)
	}

	fmt.Println(noteID)
	return nil
}

// runCreateDeck creates a new deck in the collection.
func runCreateDeck() error {
	fs := flag.NewFlagSet("create-deck", flag.ExitOnError)
	db := fs.String("db", "collection.anki2", "path to collection database")
	deckName := fs.String("name", "", "name of the deck to create (required)")
	if err := fs.Parse(reorderFlags(os.Args[2:], boolFlagsFor(fs))); err != nil {
		return fmt.Errorf("parse flags: %w", err)
	}

	// Also accept a positional argument for the deck name (backward compat).
	if *deckName == "" && fs.NArg() >= 1 {
		*deckName = fs.Arg(0)
	}

	if *deckName == "" {
		return fmt.Errorf("deck name is required; usage: anki-go create-deck <name> [--db=path]")
	}

	col, err := collection.Open(*db, collection.ReadWrite)
	if err != nil {
		return fmt.Errorf("open collection: %w", err)
	}
	defer func() { _ = col.Close() }()

	deckID, err := col.CreateDeck(*deckName)
	if err != nil {
		return fmt.Errorf("create deck: %w", err)
	}

	fmt.Println(deckID)
	return nil
}

// runStats shows collection statistics.
func runStats() error {
	fs := flag.NewFlagSet("stats", flag.ExitOnError)
	db := fs.String("db", "collection.anki2", "path to collection database")
	jsonOut := fs.Bool("json", false, "output as JSON")
	if err := fs.Parse(reorderFlags(os.Args[2:], boolFlagsFor(fs))); err != nil {
		return fmt.Errorf("parse flags: %w", err)
	}

	col, err := collection.Open(*db, collection.ReadOnly)
	if err != nil {
		return fmt.Errorf("open collection: %w", err)
	}
	defer func() { _ = col.Close() }()

	stats, err := col.GetStats()
	if err != nil {
		return fmt.Errorf("get stats: %w", err)
	}

	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(stats)
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
	return nil
}

// cmdSync dispatches sync subcommands (download, upload, media).
func cmdSync() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "Usage: anki-go sync <download|upload|media> [options]")
		os.Exit(1)
	}

	switch os.Args[2] {
	case "download":
		os.Exit(runCmd(runSyncDownload))
	case "upload":
		os.Exit(runCmd(runSyncUpload))
	case "media":
		cmdSyncMedia()
	default:
		fmt.Fprintf(os.Stderr, "unknown sync subcommand: %s\n", os.Args[2])
		fmt.Fprintln(os.Stderr, "Usage: anki-go sync <download|upload|media> [options]")
		os.Exit(1)
	}
}

// cmdSyncMedia dispatches media sync subcommands.
func cmdSyncMedia() {
	if len(os.Args) < 4 {
		fmt.Fprintln(os.Stderr, "Usage: anki-go sync media <download|upload|sanity> [options]")
		os.Exit(1)
	}

	switch os.Args[3] {
	case "download":
		os.Exit(runCmd(runSyncMediaDownload))
	case "upload":
		os.Exit(runCmd(runSyncMediaUpload))
	case "sanity":
		os.Exit(runCmd(runSyncMediaSanity))
	default:
		fmt.Fprintf(os.Stderr, "unknown media subcommand: %s\n", os.Args[3])
		fmt.Fprintln(os.Stderr, "Usage: anki-go sync media <download|upload|sanity> [options]")
		os.Exit(1)
	}
}

// runSyncDownload downloads the full collection from AnkiWeb.
func runSyncDownload() error {
	fs := flag.NewFlagSet("sync download", flag.ExitOnError)
	db := fs.String("db", "collection.anki2", "path to collection database")
	media := fs.String("media", "collection.media", "media directory path")
	username := fs.String("username", envOr("ANKIWEB_USERNAME", ""), "AnkiWeb username (or set $ANKIWEB_USERNAME)")
	password := envOr("ANKIWEB_PASSWORD", "")
	timeout := fs.Duration("timeout", 5*time.Minute, "sync timeout")
	if err := fs.Parse(reorderFlags(os.Args[3:], boolFlagsFor(fs))); err != nil {
		return fmt.Errorf("parse flags: %w", err)
	}

	if *username == "" || password == "" {
		return fmt.Errorf("ANKIWEB_USERNAME and ANKIWEB_PASSWORD environment variables are required for sync")
	}

	client, err := sync.NewClient(goanki.SyncConfig{
		Username: *username,
		Password: password,
	})
	if err != nil {
		return fmt.Errorf("create sync client: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	if err := client.Authenticate(ctx); err != nil {
		return fmt.Errorf("authenticate: %w", err)
	}

	result, err := client.FullDownload(ctx, *db, *media)
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}

	// Open the downloaded collection to count cards
	col, openErr := collection.Open(*db, collection.ReadOnly)
	if openErr != nil {
		fmt.Fprintf(os.Stderr, "warning: could not open downloaded collection: %v\n", openErr)
		fmt.Printf("Downloaded to %s\n", result.DBPath)
		return nil
	}
	defer func() { _ = col.Close() }()

	stats, statsErr := col.GetStats()
	if statsErr != nil {
		fmt.Fprintf(os.Stderr, "warning: could not read stats: %v\n", statsErr)
		fmt.Printf("Downloaded to %s\n", result.DBPath)
		return nil
	}

	fmt.Printf("Downloaded %d cards to %s\n", stats.TotalCards, result.DBPath)
	return nil
}

// runSyncUpload uploads the full collection to AnkiWeb.
func runSyncUpload() error {
	fs := flag.NewFlagSet("sync upload", flag.ExitOnError)
	db := fs.String("db", "collection.anki2", "path to collection database")
	media := fs.String("media", "collection.media", "media directory path")
	username := fs.String("username", envOr("ANKIWEB_USERNAME", ""), "AnkiWeb username (or set $ANKIWEB_USERNAME)")
	password := envOr("ANKIWEB_PASSWORD", "")
	timeout := fs.Duration("timeout", 5*time.Minute, "sync timeout")
	if err := fs.Parse(reorderFlags(os.Args[3:], boolFlagsFor(fs))); err != nil {
		return fmt.Errorf("parse flags: %w", err)
	}

	if *username == "" || password == "" {
		return fmt.Errorf("ANKIWEB_USERNAME and ANKIWEB_PASSWORD environment variables are required for sync")
	}

	client, err := sync.NewClient(goanki.SyncConfig{
		Username: *username,
		Password: password,
	})
	if err != nil {
		return fmt.Errorf("create sync client: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	if err := client.Authenticate(ctx); err != nil {
		return fmt.Errorf("authenticate: %w", err)
	}

	if err := client.FullUpload(ctx, *db, *media); err != nil {
		return fmt.Errorf("upload: %w", err)
	}

	fmt.Println("Upload complete.")
	return nil
}

// runSyncMediaDownload downloads media files from AnkiWeb.
func runSyncMediaDownload() error {
	fs := flag.NewFlagSet("sync media download", flag.ExitOnError)
	media := fs.String("media", "collection.media", "media directory path")
	username := fs.String("username", envOr("ANKIWEB_USERNAME", ""), "AnkiWeb username (or set $ANKIWEB_USERNAME)")
	password := envOr("ANKIWEB_PASSWORD", "")
	timeout := fs.Duration("timeout", 5*time.Minute, "sync timeout")
	if err := fs.Parse(reorderFlags(os.Args[4:], boolFlagsFor(fs))); err != nil {
		return fmt.Errorf("parse flags: %w", err)
	}

	if *username == "" || password == "" {
		return fmt.Errorf("ANKIWEB_USERNAME and ANKIWEB_PASSWORD environment variables are required for sync")
	}

	client, err := sync.NewClient(goanki.SyncConfig{
		Username: *username,
		Password: password,
	})
	if err != nil {
		return fmt.Errorf("create sync client: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	if err := client.Authenticate(ctx); err != nil {
		return fmt.Errorf("authenticate: %w", err)
	}

	info, err := client.MediaBegin(ctx)
	if err != nil {
		return fmt.Errorf("media begin: %w", err)
	}

	fmt.Printf("Media USN: %d\n", info.USN)
	_ = *media // reserved for full implementation
	fmt.Println("Media download not yet implemented for media sync v2 — API scaffolding ready.")
	return nil
}

// runSyncMediaUpload uploads media files to AnkiWeb.
func runSyncMediaUpload() error {
	fs := flag.NewFlagSet("sync media upload", flag.ExitOnError)
	media := fs.String("media", "collection.media", "media directory path")
	username := fs.String("username", envOr("ANKIWEB_USERNAME", ""), "AnkiWeb username (or set $ANKIWEB_USERNAME)")
	password := envOr("ANKIWEB_PASSWORD", "")
	timeout := fs.Duration("timeout", 5*time.Minute, "sync timeout")
	if err := fs.Parse(reorderFlags(os.Args[4:], boolFlagsFor(fs))); err != nil {
		return fmt.Errorf("parse flags: %w", err)
	}

	if *username == "" || password == "" {
		return fmt.Errorf("ANKIWEB_USERNAME and ANKIWEB_PASSWORD environment variables are required for sync")
	}

	client, err := sync.NewClient(goanki.SyncConfig{
		Username: *username,
		Password: password,
	})
	if err != nil {
		return fmt.Errorf("create sync client: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	if err := client.Authenticate(ctx); err != nil {
		return fmt.Errorf("authenticate: %w", err)
	}

	info, err := client.MediaBegin(ctx)
	if err != nil {
		return fmt.Errorf("media begin: %w", err)
	}

	fmt.Printf("Media USN: %d\n", info.USN)
	_ = *media // reserved for full implementation
	fmt.Println("Media upload not yet implemented for media sync v2 — API scaffolding ready.")
	return nil
}

// runSyncMediaSanity performs a media sanity check against AnkiWeb.
func runSyncMediaSanity() error {
	fs := flag.NewFlagSet("sync media sanity", flag.ExitOnError)
	media := fs.String("media", "collection.media", "media directory path")
	username := fs.String("username", envOr("ANKIWEB_USERNAME", ""), "AnkiWeb username (or set $ANKIWEB_USERNAME)")
	password := envOr("ANKIWEB_PASSWORD", "")
	timeout := fs.Duration("timeout", 30*time.Second, "sync timeout")
	if err := fs.Parse(reorderFlags(os.Args[4:], boolFlagsFor(fs))); err != nil {
		return fmt.Errorf("parse flags: %w", err)
	}

	if *username == "" || password == "" {
		return fmt.Errorf("ANKIWEB_USERNAME and ANKIWEB_PASSWORD environment variables are required for sync")
	}

	client, err := sync.NewClient(goanki.SyncConfig{
		Username: *username,
		Password: password,
	})
	if err != nil {
		return fmt.Errorf("create sync client: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	if err := client.Authenticate(ctx); err != nil {
		return fmt.Errorf("authenticate: %w", err)
	}

	info, err := client.MediaBegin(ctx)
	if err != nil {
		return fmt.Errorf("media begin: %w", err)
	}

	// Count files in media directory
	files, err := os.ReadDir(*media)
	count := 0
	if err == nil {
		count = len(files)
	} else {
		fmt.Fprintf(os.Stderr, "warning: could not read media directory: %v\n", err)
	}

	if err := client.MediaSanity(ctx, info, count); err != nil {
		return fmt.Errorf("media sanity check: %w", err)
	}

	fmt.Printf("Media sanity check passed (local files: %d)\n", count)
	return nil
}

// runServe starts the HTTP API server.
func runServe() error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	db := fs.String("db", "collection.anki2", "path to collection database")
	media := fs.String("media", "collection.media", "media directory path")
	port := fs.Int("port", 8765, "HTTP server port")
	authToken := fs.String("auth-token", envOr("ANKIGO_AUTH_TOKEN", ""), "bearer token for HTTP authentication (or set $ANKIGO_AUTH_TOKEN)")
	username := fs.String("username", envOr("ANKIWEB_USERNAME", ""), "AnkiWeb username (optional, enables sync endpoints)")
	password := envOr("ANKIWEB_PASSWORD", "")
	if err := fs.Parse(reorderFlags(os.Args[2:], boolFlagsFor(fs))); err != nil {
		return fmt.Errorf("parse flags: %w", err)
	}

	if *port < 1 || *port > 65535 {
		return fmt.Errorf("invalid port %d: must be between 1 and 65535", *port)
	}

	opts := []server.ServerOption{
		server.WithPort(*port),
		server.WithMediaDir(*media),
		server.WithScheduler(scheduler.NewFSRSScheduler()),
	}

	if *authToken != "" {
		if len(*authToken) < 16 {
			return fmt.Errorf("auth-token must be at least 16 characters")
		}
		opts = append(opts, server.WithAuthToken(*authToken))
	}

	if *username != "" && password != "" {
		opts = append(opts, server.WithSyncConfig(goanki.SyncConfig{
			Username: *username,
			Password: password,
		}))
	}

	srv := server.NewServer(*db, opts...)

	fmt.Printf("go-anki server starting on :%d (db: %s)\n", *port, *db)
	return srv.ListenAndServe()
}

// reorderFlags moves all flag arguments (starting with -) and their values
// before positional arguments. This works around Go's standard flag package
// behavior, which stops parsing at the first non-flag argument. Without
// reordering, "create-deck MyDeck --db /tmp/test.anki2" would fail to
// parse --db because "MyDeck" appears first and is treated as positional.
//
// Boolean flags (which don't consume the next argument) are detected
// by pre-scanning each subcommand's FlagSet so the map stays accurate
// as new flags are added.
func reorderFlags(args []string, boolFlags map[string]bool) []string {
	var flags, positional []string

	for i := 0; i < len(args); i++ {
		if strings.HasPrefix(args[i], "-") {
			flags = append(flags, args[i])
			// Handle --flag=value form: value is embedded, no separate arg.
			if idx := strings.IndexByte(args[i], '='); idx >= 0 {
				continue
			}
			// Determine the flag name (strip leading dashes).
			name := strings.TrimLeft(args[i], "-")
			if boolFlags[name] {
				// Bool flag, no value arg to consume.
				continue
			}
			// Non-bool flag: consume the next argument as its value
			// (if present and not starting with -).
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				i++
				flags = append(flags, args[i])
			}
		} else {
			positional = append(positional, args[i])
		}
	}
	return append(flags, positional...)
}

// boolFlagsFor collects all boolean flags from a FlagSet into a lookup map.
func boolFlagsFor(fs *flag.FlagSet) map[string]bool {
	m := make(map[string]bool)
	fs.VisitAll(func(f *flag.Flag) {
		if fb, ok := f.Value.(interface{ IsBoolFlag() bool }); ok && fb.IsBoolFlag() {
			m[f.Name] = true
		}
	})
	return m
}

// envOr returns the environment variable value if set, otherwise returns fallback.
func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}