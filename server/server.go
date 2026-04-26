// Package server provides an HTTP API server for go-anki,
// exposing REST endpoints for collection management, card review,
// and AnkiWeb sync operations.
package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	goanki "github.com/vpul/go-anki/pkg/types"

	"github.com/vpul/go-anki/pkg/collection"
	"github.com/vpul/go-anki/pkg/scheduler"
	"github.com/vpul/go-anki/pkg/sync"
)

const (
	// maxBodyBytes is the maximum allowed request body size (1MB).
	maxBodyBytes int64 = 1 << 20
)

// ServerOption is a functional option for configuring a Server.
type ServerOption func(*Server)

// WithPort sets the HTTP server port.
func WithPort(port int) ServerOption {
	return func(s *Server) {
		s.port = port
	}
}

// WithSyncConfig sets the AnkiWeb sync credentials.
func WithSyncConfig(cfg goanki.SyncConfig) ServerOption {
	return func(s *Server) {
		s.syncConfig = &cfg
	}
}

// WithMediaDir sets the media directory path for sync operations.
func WithMediaDir(dir string) ServerOption {
	return func(s *Server) {
		s.mediaDir = dir
	}
}

// WithScheduler sets the card scheduling implementation.
func WithScheduler(sched collection.Scheduler) ServerOption {
	return func(s *Server) {
		s.scheduler = sched
	}
}

// WithServerTimeouts sets the HTTP server timeouts.
func WithServerTimeouts(read, write, idle time.Duration) ServerOption {
	return func(s *Server) {
		s.readTimeout = read
		s.writeTimeout = write
		s.idleTimeout = idle
	}
}

// Server is an HTTP API server wrapping go-anki collection operations.
type Server struct {
	dbPath      string
	mediaDir    string
	syncConfig  *goanki.SyncConfig
	port        int
	scheduler   collection.Scheduler
	readTimeout time.Duration
	writeTimeout time.Duration
	idleTimeout time.Duration
}

// NewServer creates a new Server with the given database path and options.
func NewServer(dbPath string, opts ...ServerOption) *Server {
	s := &Server{
		dbPath:       dbPath,
		port:         8765,
		scheduler:    scheduler.NewFSRSScheduler(),
		readTimeout:  5 * time.Second,
		writeTimeout: 10 * time.Second,
		idleTimeout:  60 * time.Second,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

//sanitizeErr sanitizes error messages for 500 status responses.
// It removes internal details like file paths, SQL keywords, and
// SQLite error patterns that should not be exposed to clients.
func sanitizeErr(err error) string {
	msg := err.Error()
	lower := strings.ToLower(msg)

	// "not found" errors are safe to surface
	if strings.Contains(lower, "not found") {
		return "not found"
	}

	// Patterns that indicate internal details should be hidden entirely
	pathPattern := regexp.MustCompile(`[/\\][\w._-]+`)
	sqlKeywords := []string{"SELECT ", "INSERT ", "UPDATE ", "DELETE ", "WHERE ", "select ", "insert ", "update ", "delete ", "where "}
	sqlitePatterns := []string{"SQL logic error", "database is locked", "disk I/O error"}

	if pathPattern.MatchString(msg) {
		return "internal error"
	}
	for _, kw := range sqlKeywords {
		if strings.Contains(msg, kw) {
			return "internal error"
		}
	}
	for _, pat := range sqlitePatterns {
		if strings.Contains(lower, strings.ToLower(pat)) {
			return "internal error"
		}
	}

	// For other errors, strip any path-like content but return the rest
	sanitized := pathPattern.ReplaceAllString(msg, "[redacted]")
	return sanitized
}

// errorResponse writes a JSON error response with the given status code.
func errorResponse(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// jsonResponse writes a JSON response with status 200.
func jsonResponse(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// withMode opens a collection with the given mode, calls fn, and closes it.
// It handles DB locking errors and returns appropriate HTTP error responses.
func (s *Server) withMode(mode collection.OpenMode, fn func(col *collection.Collection, w http.ResponseWriter, r *http.Request)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		col, err := collection.Open(s.dbPath, mode)
		if err != nil {
			errorResponse(w, http.StatusInternalServerError, sanitizeErr(err))
			return
		}
		defer func() { _ = col.Close() }()
		fn(col, w, r)
	}
}

// maxBodySize is middleware that limits the request body size for all requests.
func maxBodySize(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
		}
		next.ServeHTTP(w, r)
	})
}

// Handler returns an http.Handler with all API routes registered.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// Read endpoints
	mux.HandleFunc("GET /api/v1/version", s.handleVersion)
	mux.HandleFunc("GET /api/v1/decks", s.withMode(collection.ReadOnly, s.handleGetDecks))
	mux.HandleFunc("GET /api/v1/due-cards", s.withMode(collection.ReadOnly, s.handleGetDueCards))
	mux.HandleFunc("GET /api/v1/stats", s.withMode(collection.ReadOnly, s.handleGetStats))
	mux.HandleFunc("GET /api/v1/cards/{id}", s.withMode(collection.ReadOnly, s.handleGetCardByID))

	// Write endpoints
	mux.HandleFunc("POST /api/v1/answer", s.withMode(collection.ReadWrite, s.handleAnswer))
	mux.HandleFunc("POST /api/v1/decks", s.withMode(collection.ReadWrite, s.handleCreateDeck))
	mux.HandleFunc("POST /api/v1/notes", s.withMode(collection.ReadWrite, s.handleAddNote))

	// Sync endpoints
	mux.HandleFunc("POST /api/v1/sync/download", s.handleSyncDownload)
	mux.HandleFunc("POST /api/v1/sync/upload", s.handleSyncUpload)

	return maxBodySize(mux)
}

// ListenAndServe starts the HTTP server on the configured port.
func (s *Server) ListenAndServe() error {
	addr := fmt.Sprintf(":%d", s.port)
	log.Printf("go-anki server listening on %s", addr)
	srv := &http.Server{
		Addr:         addr,
		Handler:      s.Handler(),
		ReadTimeout:  s.readTimeout,
		WriteTimeout: s.writeTimeout,
		IdleTimeout:  s.idleTimeout,
	}
	return srv.ListenAndServe()
}

// handleVersion returns the server version.
func (s *Server) handleVersion(w http.ResponseWriter, _ *http.Request) {
	jsonResponse(w, map[string]string{"version": "go-anki/1.0.0"})
}

// handleGetDecks returns all decks in the collection.
func (s *Server) handleGetDecks(col *collection.Collection, w http.ResponseWriter, _ *http.Request) {
	decks, err := col.GetDecks()
	if err != nil {
		errorResponse(w, http.StatusInternalServerError, sanitizeErr(err))
		return
	}
	// Convert map to slice for JSON array response
	deckList := make([]goanki.Deck, 0, len(decks))
	for _, d := range decks {
		deckList = append(deckList, d)
	}
	jsonResponse(w, map[string]interface{}{"decks": deckList})
}

// handleGetDueCards returns cards that are due for review.
func (s *Server) handleGetDueCards(col *collection.Collection, w http.ResponseWriter, r *http.Request) {
	filter := goanki.DueCardsFilter{
		Limit: 20,
	}

	if deck := r.URL.Query().Get("deck"); deck != "" {
		filter.DeckName = deck
	}
	if limitStr := r.URL.Query().Get("limit"); limitStr != "" {
		if n, err := strconv.Atoi(limitStr); err == nil && n > 0 {
			filter.Limit = n
		}
	}

	cards, err := col.GetDueCards(filter)
	if err != nil {
		errorResponse(w, http.StatusInternalServerError, sanitizeErr(err))
		return
	}
	jsonResponse(w, map[string]interface{}{"cards": cards})
}

// handleGetStats returns collection statistics.
func (s *Server) handleGetStats(col *collection.Collection, w http.ResponseWriter, _ *http.Request) {
	stats, err := col.GetStats()
	if err != nil {
		errorResponse(w, http.StatusInternalServerError, sanitizeErr(err))
		return
	}
	jsonResponse(w, map[string]interface{}{"stats": stats})
}

// handleGetCardByID returns a single card by its ID.
func (s *Server) handleGetCardByID(col *collection.Collection, w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		errorResponse(w, http.StatusBadRequest, "invalid card ID")
		return
	}

	card, err := col.GetCardByID(id)
	if err != nil {
		if errors.Is(err, collection.ErrNotFound) {
			errorResponse(w, http.StatusNotFound, fmt.Sprintf("card %d not found", id))
		} else {
			errorResponse(w, http.StatusInternalServerError, sanitizeErr(err))
		}
		return
	}
	jsonResponse(w, map[string]interface{}{"card": card})
}

// answerRequest is the request body for the answer endpoint.
type answerRequest struct {
	CardID int64  `json:"card_id"`
	Rating string `json:"rating"`
}

// handleAnswer processes a card answer and updates its scheduling.
func (s *Server) handleAnswer(col *collection.Collection, w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var req answerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		errorResponse(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.CardID == 0 {
		errorResponse(w, http.StatusBadRequest, "card_id is required")
		return
	}

	switch req.Rating {
	case "again", "hard", "good", "easy":
		// valid
	default:
		errorResponse(w, http.StatusBadRequest, "invalid rating: must be one of again, hard, good, easy")
		return
	}
	rating := goanki.ParseRating(req.Rating)

	answer, err := col.AnswerCard(req.CardID, rating, s.scheduler)
	if err != nil {
		if errors.Is(err, collection.ErrNotFound) {
			errorResponse(w, http.StatusNotFound, fmt.Sprintf("card %d not found", req.CardID))
		} else {
			errorResponse(w, http.StatusInternalServerError, sanitizeErr(err))
		}
		return
	}
	jsonResponse(w, map[string]interface{}{
		"card":   answer.Card,
		"review": answer.Review,
	})
}

// createDeckRequest is the request body for the create deck endpoint.
type createDeckRequest struct {
	Name string `json:"name"`
}

// handleCreateDeck creates a new deck.
func (s *Server) handleCreateDeck(col *collection.Collection, w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var req createDeckRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		errorResponse(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name == "" {
		errorResponse(w, http.StatusBadRequest, "name is required")
		return
	}

	deckID, err := col.CreateDeck(req.Name)
	if err != nil {
		errorResponse(w, http.StatusInternalServerError, sanitizeErr(err))
		return
	}
	jsonResponse(w, map[string]interface{}{"deck_id": deckID})
}

// addNoteRequest is the request body for the add note endpoint.
type addNoteRequest struct {
	DeckName  string            `json:"deck_name"`
	ModelName string            `json:"model_name"`
	Fields    map[string]string `json:"fields"`
	Tags      []string          `json:"tags"`
}

// handleAddNote creates a new note and its associated cards.
func (s *Server) handleAddNote(col *collection.Collection, w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var req addNoteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		errorResponse(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.DeckName == "" {
		errorResponse(w, http.StatusBadRequest, "deck_name is required")
		return
	}
	if req.ModelName == "" {
		errorResponse(w, http.StatusBadRequest, "model_name is required")
		return
	}
	if len(req.Fields) == 0 {
		errorResponse(w, http.StatusBadRequest, "fields are required")
		return
	}

	input := goanki.NewNote{
		DeckName:  req.DeckName,
		ModelName: req.ModelName,
		Fields:    req.Fields,
		Tags:      req.Tags,
	}

	noteID, err := col.AddNote(input)
	if err != nil {
		errorResponse(w, http.StatusInternalServerError, sanitizeErr(err))
		return
	}
	jsonResponse(w, map[string]interface{}{"note_id": noteID})
}

// handleSyncDownload performs a full download from AnkiWeb.
func (s *Server) handleSyncDownload(w http.ResponseWriter, r *http.Request) {
	if s.syncConfig == nil {
		errorResponse(w, http.StatusBadRequest, "sync not configured: set SyncConfig")
		return
	}

	client := sync.NewClient(*s.syncConfig)
	ctx := r.Context()

	if err := client.Authenticate(ctx); err != nil {
		errorResponse(w, http.StatusUnauthorized, fmt.Sprintf("authenticate: %v", err))
		return
	}

	result, err := client.FullDownload(ctx, s.dbPath, s.mediaDir)
	if err != nil {
		errorResponse(w, http.StatusInternalServerError, sanitizeErr(err))
		return
	}

	// Count cards in the downloaded collection
	var cardCount int
	col, err := collection.Open(s.dbPath, collection.ReadOnly)
	if err != nil {
		log.Printf("warning: open collection after download: %v", err)
	} else {
		defer func() { _ = col.Close() }()
		stats, statsErr := col.GetStats()
		if statsErr != nil {
			log.Printf("warning: get stats after download: %v", statsErr)
		} else {
			cardCount = stats.TotalCards
		}
	}

	jsonResponse(w, map[string]interface{}{
		"status": "ok",
		"cards":  cardCount,
		"path":   result.DBPath,
	})
}

// handleSyncUpload performs a full upload to AnkiWeb.
func (s *Server) handleSyncUpload(w http.ResponseWriter, r *http.Request) {
	if s.syncConfig == nil {
		errorResponse(w, http.StatusBadRequest, "sync not configured: set SyncConfig")
		return
	}

	client := sync.NewClient(*s.syncConfig)
	ctx := r.Context()

	if err := client.Authenticate(ctx); err != nil {
		errorResponse(w, http.StatusUnauthorized, fmt.Sprintf("authenticate: %v", err))
		return
	}

	if err := client.FullUpload(ctx, s.dbPath, s.mediaDir); err != nil {
		errorResponse(w, http.StatusInternalServerError, sanitizeErr(err))
		return
	}

	jsonResponse(w, map[string]string{"status": "ok"})
}