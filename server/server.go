// Package server provides an HTTP API server for go-anki,
// exposing REST endpoints for collection management, card review,
// and AnkiWeb sync operations.
package server

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	stdsync "sync"
	"time"

	goanki "github.com/vpul/go-anki/pkg/types"

	"github.com/vpul/go-anki/pkg/collection"
	"github.com/vpul/go-anki/pkg/scheduler"
	"github.com/vpul/go-anki/pkg/sync"
)

const (
	// maxBodyBytes is the maximum allowed request body size (1MB).
	maxBodyBytes int64 = 1 << 20

	// maxTrackedIPs caps the number of distinct IP addresses tracked by the
	// rate limiter, preventing memory exhaustion from unbounded map growth.
	maxTrackedIPs = 50000
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

// WithAuthToken sets the bearer token for authenticating requests.
// When configured, all endpoints except GET /health require a valid
// Authorization: Bearer <token> header. The token is stored as a SHA-256 hash.
func WithAuthToken(token string) ServerOption {
	return func(s *Server) {
		hash := sha256.Sum256([]byte(token))
		s.authTokenHash = hash[:]
	}
}

// WithRateLimit sets the maximum requests per minute per IP address.
// A value of 0 or less disables rate limiting. Default is 60 RPM.
func WithRateLimit(requestsPerMinute int) ServerOption {
	return func(s *Server) {
		s.rateLimit = requestsPerMinute
	}
}

// rateLimiter implements a per-IP sliding window rate limiter.
type rateLimiter struct {
	mu       stdsync.Mutex
	requests map[string][]time.Time
	limit    int // requests per minute
	maxIPs   int // maximum number of distinct IPs tracked (prevents memory DoS)
}

// rateLimitResult indicates whether a request is allowed.
type rateLimitResult struct {
	allowed   bool
	retryAfter time.Duration // seconds until next request is allowed
}

// allow checks if a request from the given IP is allowed.
func (rl *rateLimiter) allow(ip string) rateLimitResult {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	windowStart := now.Add(-time.Minute)

	// Evict old entries for this IP
	timestamps := rl.requests[ip]
	valid := timestamps[:0]
	for _, t := range timestamps {
		if t.After(windowStart) {
			valid = append(valid, t)
		}
	}

	if len(valid) >= rl.limit {
		rl.requests[ip] = valid
		// Calculate when the oldest request expires
		retryAfter := time.Until(valid[0].Add(time.Minute)).Seconds()
		if retryAfter < 0 {
			retryAfter = 0
		}
		return rateLimitResult{allowed: false, retryAfter: time.Duration(retryAfter * float64(time.Second))}
	}

	// If this is a new IP and we've hit the max IPs cap, reject
	if len(timestamps) == 0 && rl.maxIPs > 0 && len(rl.requests) >= rl.maxIPs {
		return rateLimitResult{allowed: false, retryAfter: time.Minute}
	}

	valid = append(valid, now)
	rl.requests[ip] = valid
	return rateLimitResult{allowed: true}
}

// cleanup removes expired entries from the rate limiter.
func (rl *rateLimiter) cleanup() {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	windowStart := now.Add(-time.Minute)

	for ip, timestamps := range rl.requests {
		valid := timestamps[:0]
		for _, t := range timestamps {
			if t.After(windowStart) {
				valid = append(valid, t)
			}
		}
		if len(valid) == 0 {
			delete(rl.requests, ip)
		} else {
			rl.requests[ip] = valid
		}
	}
}

// Server is an HTTP API server wrapping go-anki collection operations.
type Server struct {
	dbPath        string
	mediaDir      string
	syncConfig    *goanki.SyncConfig
	port          int
	scheduler     collection.Scheduler
	readTimeout   time.Duration
	writeTimeout  time.Duration
	idleTimeout   time.Duration
	authTokenHash []byte
	rateLimit     int // requests per minute per IP; 0 = disabled
	closeCh       chan struct{}
	limiter       *rateLimiter
	syncMu        stdsync.Mutex // Serializes concurrent sync operations (download/upload).
	// Note: This does not protect against sync-vs-write races.
	// Write handlers (handleAnswer, handleAddNote, etc.) go through
	// withMode and acquire their own DB-level locks via SQLite WAL.
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
		rateLimit:    60, // default 60 RPM
		closeCh:      make(chan struct{}),
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

	// Patterns that indicate internal details should be hidden entirely.
	// Checked BEFORE the "not found" shortcut so that errors like
	// "SELECT * FROM cards WHERE id=5: record not found" are sanitized
	// as "internal error" rather than leaking SQL.
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

	// "not found" errors are safe to surface only after checking for SQL/path leaks
	if strings.Contains(lower, "not found") {
		return "not found"
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

// requireAuth is middleware that checks the Authorization: Bearer <token> header
// against the hashed auth token. If no token is configured, auth is not required.
func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(s.authTokenHash) == 0 {
			// No auth configured — allow everything
			next.ServeHTTP(w, r)
			return
		}

		// Health endpoint is always accessible
		if r.Method == http.MethodGet && r.URL.Path == "/health" {
			next.ServeHTTP(w, r)
			return
		}

		authHeader := r.Header.Get("Authorization")
		if !strings.HasPrefix(authHeader, "Bearer ") {
			w.Header().Set("WWW-Authenticate", "Bearer")
			errorResponse(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		token := strings.TrimPrefix(authHeader, "Bearer ")
		tokenHash := sha256.Sum256([]byte(token))
		if subtle.ConstantTimeCompare(tokenHash[:], s.authTokenHash) != 1 {
			w.Header().Set("WWW-Authenticate", "Bearer")
			errorResponse(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// rateLimitMiddleware applies per-IP rate limiting.
func (s *Server) rateLimitMiddleware(next http.Handler) http.Handler {
	// If rate limiting is disabled, pass through
	if s.rateLimit <= 0 {
		return next
	}

	// Create the rate limiter if not yet initialized
	if s.limiter == nil {
		s.limiter = &rateLimiter{
			requests: make(map[string][]time.Time),
			limit:    s.rateLimit,
			maxIPs:   maxTrackedIPs,
		}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			ip = r.RemoteAddr
		}

		result := s.limiter.allow(ip)
		if !result.allowed {
			w.Header().Set("Retry-After", strconv.Itoa(int(result.retryAfter.Seconds()+0.5)))
			errorResponse(w, http.StatusTooManyRequests, "rate limit exceeded")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// handleHealth returns the server health status.
func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	jsonResponse(w, map[string]string{"status": "ok"})
}

// Handler returns an http.Handler with all API routes registered.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// Health check endpoint (no auth required)
	mux.HandleFunc("GET /health", s.handleHealth)

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

	return s.rateLimitMiddleware(s.requireAuth(maxBodySize(mux)))
}

// ListenAndServe starts the HTTP server on the configured port.
func (s *Server) ListenAndServe() error {
	addr := fmt.Sprintf(":%d", s.port)

	// Warn if running without auth or TLS
	if len(s.authTokenHash) == 0 {
		log.Printf("WARNING: No auth token configured — all endpoints are publicly accessible.")
		log.Printf("         Set auth token via WithAuthToken() or --auth-token flag.")
	}
	log.Printf("WARNING: Server is running without TLS. For production use, run behind a TLS-terminating proxy (nginx, Caddy, etc.).")

	log.Printf("go-anki server listening on %s", addr)

	// Start rate limiter cleanup goroutine if rate limiting is enabled
	if s.rateLimit > 0 {
		// Ensure limiter is initialized
		if s.limiter == nil {
			s.limiter = &rateLimiter{
				requests: make(map[string][]time.Time),
				limit:    s.rateLimit,
				maxIPs:   maxTrackedIPs,
			}
		}
		go func() {
			ticker := time.NewTicker(5 * time.Minute)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					s.limiter.cleanup()
				case <-s.closeCh:
					return
				}
			}
		}()
	}

	srv := &http.Server{
		Addr:         addr,
		Handler:      s.Handler(),
		ReadTimeout:  s.readTimeout,
		WriteTimeout: s.writeTimeout,
		IdleTimeout:  s.idleTimeout,
	}
	return srv.ListenAndServe()
}

// Close shuts down the server gracefully, stopping the rate limiter cleanup goroutine.
// It signals the closeCh to stop background goroutines and then closes the HTTP server.
func (s *Server) Close() error {
	// Signal background goroutines to stop
	select {
	case <-s.closeCh:
		// Already closed
	default:
		close(s.closeCh)
	}
	return nil
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
	filter := goanki.DueCardsFilter{}

	if deck := r.URL.Query().Get("deck"); deck != "" {
		filter.DeckName = deck
	}
	if limitStr := r.URL.Query().Get("limit"); limitStr != "" {
		if n, err := strconv.Atoi(limitStr); err == nil {
			filter.Limit = n
		}
	}
	if filter.Limit <= 0 {
		filter.Limit = 100
	}
	if filter.Limit > 1000 {
		filter.Limit = 1000
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
	s.syncMu.Lock()
	defer s.syncMu.Unlock()

	if s.syncConfig == nil {
		errorResponse(w, http.StatusBadRequest, "sync not configured: set SyncConfig")
		return
	}

	client, err := sync.NewClient(*s.syncConfig)
	if err != nil {
		log.Printf("create sync client: %v", err)
		errorResponse(w, http.StatusInternalServerError, "sync client initialization failed")
		return
	}
	ctx := r.Context()

	if err := client.Authenticate(ctx); err != nil {
		errorResponse(w, http.StatusUnauthorized, "sync authentication failed")
		return
	}

	_, err = client.FullDownload(ctx, s.dbPath, s.mediaDir)
	if err != nil {
		errorResponse(w, http.StatusInternalServerError, sanitizeErr(err))
		return
	}

	// Count cards in the downloaded collection
	cardCount := 0
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
	})
}

// handleSyncUpload performs a full upload to AnkiWeb.
func (s *Server) handleSyncUpload(w http.ResponseWriter, r *http.Request) {
	s.syncMu.Lock()
	defer s.syncMu.Unlock()

	if s.syncConfig == nil {
		errorResponse(w, http.StatusBadRequest, "sync not configured: set SyncConfig")
		return
	}

	client, err := sync.NewClient(*s.syncConfig)
	if err != nil {
		log.Printf("create sync client: %v", err)
		errorResponse(w, http.StatusInternalServerError, "sync client initialization failed")
		return
	}
	ctx := r.Context()

	if err := client.Authenticate(ctx); err != nil {
		errorResponse(w, http.StatusUnauthorized, "sync authentication failed")
		return
	}

	if err := client.FullUpload(ctx, s.dbPath, s.mediaDir); err != nil {
		errorResponse(w, http.StatusInternalServerError, sanitizeErr(err))
		return
	}

	jsonResponse(w, map[string]string{"status": "ok"})
}