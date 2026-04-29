// Package server provides an HTTP API server for go-anki,
// exposing REST endpoints for collection management, card review,
// and AnkiWeb sync operations.
package server

import (
	"context"
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

// contextKey is the type for context keys used by this package.
type contextKey string

const (
	dbPathContextKey         contextKey = "dbPath"
	collectionNameContextKey contextKey = "collectionName"
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

// WithCollectionRegistry enables multi-collection mode. When set, all
// collection-specific API routes are served under /api/v1/collections/{name}/.
func WithCollectionRegistry(reg *CollectionRegistry) ServerOption {
	return func(s *Server) {
		s.registry = reg
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
	allowed    bool
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
	// Snapshot keys under the lock, then iterate outside it to minimize lock contention.
	rl.mu.Lock()
	keys := make([]string, 0, len(rl.requests))
	for ip := range rl.requests {
		keys = append(keys, ip)
	}
	rl.mu.Unlock()

	if len(keys) == 0 {
		return
	}

	now := time.Now()
	windowStart := now.Add(-time.Minute)

	for _, ip := range keys {
		rl.mu.Lock()
		timestamps, exists := rl.requests[ip]
		if !exists {
			rl.mu.Unlock()
			continue
		}
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
		rl.mu.Unlock()
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
	// NOTE: syncMu and writeMu are server-wide locks. In multi-collection mode, a sync/write
	// to collection-A blocks sync/writes to collection-B. This is a known simplification —
	// per-collection locks could be added later for better throughput under concurrent access.
	syncMu        stdsync.Mutex   // Serializes concurrent sync operations (download/upload).
	writeMu       stdsync.RWMutex // Protects dbPath from concurrent write handlers and sync. Both acquire exclusive Lock.
	serverMu      stdsync.Mutex   // Protects httpServer field from concurrent access
	httpServer    *http.Server    // Stored for graceful shutdown in Close()
	registry      *CollectionRegistry
}

// NewServer creates a new Server with the given database path and options.
// In multi-collection mode, pass an empty dbPath and use WithCollectionRegistry.
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
	if s.rateLimit > 0 {
		s.limiter = &rateLimiter{
			requests: make(map[string][]time.Time),
			limit:    s.rateLimit,
			maxIPs:   maxTrackedIPs,
		}
	}
	return s
}

var (
	pathPattern = regexp.MustCompile(`[/\\][\w._\-]+`)
)

// sanitizeErr sanitizes error messages for 500 status responses.
// It removes internal details like file paths, SQL keywords, and
// SQLite error patterns that should not be exposed to clients.
func sanitizeErr(err error) string {
	msg := err.Error()
	lower := strings.ToLower(msg)

	// Known sentinel errors — safe to surface
	if errors.Is(err, collection.ErrNotFound) {
		return "not found"
	}

	// Patterns that indicate internal details should be hidden entirely.
	sqlKeywords := []string{"SELECT ", "INSERT ", "UPDATE ", "DELETE ", "WHERE "}
	sqlitePatterns := []string{"SQL logic error", "database is locked", "disk I/O error"}

	for _, kw := range sqlKeywords {
		if strings.Contains(msg, kw) || strings.Contains(msg, strings.ToLower(kw)) {
			return "internal error"
		}
	}
	for _, pat := range sqlitePatterns {
		if strings.Contains(lower, strings.ToLower(pat)) {
			return "internal error"
		}
	}

	// Strip path-like content instead of blanket-redacting the whole message.
	// This preserves meaningful error text while removing internal paths like
	// "/home/user/collection.anki2".
	sanitized := pathPattern.ReplaceAllString(msg, "")
	sanitized = strings.TrimSpace(sanitized)

	if sanitized == "" {
		return "internal error"
	}
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

// dbPathFromContext returns the resolved DB path from request context,
// falling back to s.dbPath in single-collection mode.
func (s *Server) dbPathFromContext(r *http.Request) string {
	if v, ok := r.Context().Value(dbPathContextKey).(string); ok && v != "" {
		return v
	}
	return s.dbPath
}

// collectionNameFromContext returns the collection name stored in context
// by the resolveCollection middleware (non-empty only in multi-collection mode).
func collectionNameFromContext(r *http.Request) string {
	v, _ := r.Context().Value(collectionNameContextKey).(string)
	return v
}

// lockForCollection acquires either a per-collection lock (multi-collection mode)
// or the global syncMu lock (single-collection mode) for the sync handler.
// This allows concurrent sync operations on different collections while serializing
// sync operations on the same collection or in single-collection mode.
func (s *Server) lockForCollection(r *http.Request) {
	if name := collectionNameFromContext(r); name != "" && s.registry != nil {
		s.registry.LockCollection(name)
	} else {
		s.syncMu.Lock()
	}
}

// unlockForCollection releases the lock acquired by lockForCollection.
func (s *Server) unlockForCollection(r *http.Request) {
	if name := collectionNameFromContext(r); name != "" && s.registry != nil {
		s.registry.UnlockCollection(name)
	} else {
		s.syncMu.Unlock()
	}
}

// addCollection adds a "collection" key to a response map when in multi-collection mode.
func addCollection(r *http.Request, m map[string]interface{}) map[string]interface{} {
	if name := collectionNameFromContext(r); name != "" {
		m["collection"] = name
	}
	return m
}

// withMode opens a collection with the given mode, calls fn, and closes it.
// In multi-collection mode the DB path is resolved from request context;
// in single-collection mode it falls back to s.dbPath.
// For ReadWrite mode, it acquires writeMu.Lock() to prevent concurrent write
// handlers or sync operations from replacing the DB file while writes are in progress.
func (s *Server) withMode(mode collection.OpenMode, fn func(col *collection.Collection, w http.ResponseWriter, r *http.Request)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		dbPath := s.dbPathFromContext(r)
		if mode == collection.ReadWrite {
			s.writeMu.Lock()
			defer s.writeMu.Unlock()
		}
		col, err := collection.Open(dbPath, mode)
		if err != nil {
			errorResponse(w, http.StatusInternalServerError, sanitizeErr(err))
			return
		}
		defer func() { _ = col.Close() }()
		fn(col, w, r)
	}
}

// resolveCollection is middleware that resolves the {name} path value to a DB path
// via the registry, storing both in the request context for downstream handlers.
func (s *Server) resolveCollection(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		dbPath, err := s.registry.Resolve(name)
		if err != nil {
			errorResponse(w, http.StatusNotFound, "collection not found")
			return
		}
		ctx := context.WithValue(r.Context(), dbPathContextKey, dbPath)
		ctx = context.WithValue(ctx, collectionNameContextKey, name)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// registerCollectionRoutes registers all collection-specific API routes under prefix.
// In multi-collection mode (registry != nil), each handler is wrapped with
// resolveCollection middleware that validates the {name} path value and injects
// the DB path into context.
func (s *Server) registerCollectionRoutes(mux *http.ServeMux, prefix string) {
	wrap := func(h http.Handler) http.Handler {
		if s.registry != nil {
			return s.resolveCollection(h)
		}
		return h
	}

	mux.Handle("GET "+prefix+"/version", wrap(http.HandlerFunc(s.handleVersion)))
	mux.Handle("GET "+prefix+"/decks", wrap(s.withMode(collection.ReadOnly, s.handleGetDecks)))
	mux.Handle("GET "+prefix+"/due-cards", wrap(s.withMode(collection.ReadOnly, s.handleGetDueCards)))
	mux.Handle("GET "+prefix+"/stats", wrap(s.withMode(collection.ReadOnly, s.handleGetStats)))
	mux.Handle("GET "+prefix+"/cards/{id}", wrap(s.withMode(collection.ReadOnly, s.handleGetCardByID)))
	mux.Handle("POST "+prefix+"/answer", wrap(s.withMode(collection.ReadWrite, s.handleAnswer)))
	mux.Handle("POST "+prefix+"/decks", wrap(s.withMode(collection.ReadWrite, s.handleCreateDeck)))
	mux.Handle("POST "+prefix+"/notes", wrap(s.withMode(collection.ReadWrite, s.handleAddNote)))
	mux.Handle("POST "+prefix+"/sync/download", wrap(http.HandlerFunc(s.handleSyncDownload)))
	mux.Handle("POST "+prefix+"/sync/upload", wrap(http.HandlerFunc(s.handleSyncUpload)))
	mux.Handle("POST "+prefix+"/sync/delta", wrap(http.HandlerFunc(s.handleSyncDelta)))
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

// securityHeaders adds defensive HTTP headers to all responses.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}

// recoverPanic is middleware that recovers from panics in HTTP handlers,
// returning a 500 Internal Server Error instead of dropping the connection.
func recoverPanic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if err := recover(); err != nil {
				log.Printf("panic recovered in HTTP handler: %v", err)
				errorResponse(w, http.StatusInternalServerError, "internal server error")
			}
		}()
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
	if s.rateLimit <= 0 {
		return next
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
	if s.registry != nil {
		// Multi-collection mode: check all collections, return 503 if any fail.
		names := s.registry.Names()
		for _, name := range names {
			path, err := s.registry.Resolve(name)
			if err != nil {
				errorResponse(w, http.StatusServiceUnavailable, "database unavailable")
				return
			}
			col, err := collection.Open(path, collection.ReadOnly)
			if err != nil {
				_ = col.Close()
				errorResponse(w, http.StatusServiceUnavailable, "database unavailable")
				return
			}
			_ = col.Close()
		}
	} else if s.dbPath != "" {
		col, err := collection.Open(s.dbPath, collection.ReadOnly)
		if err != nil {
			errorResponse(w, http.StatusServiceUnavailable, "database unavailable")
			return
		}
		_ = col.Close()
	}
	jsonResponse(w, map[string]string{"status": "ok"})
}

// Handler returns an http.Handler with all API routes registered.
// In single-collection mode routes are at /api/v1/...
// In multi-collection mode routes are at /api/v1/collections/{name}/...
// plus GET /api/v1/collections to list available collections.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// Health check endpoint (no auth required)
	mux.HandleFunc("GET /health", s.handleHealth)

	if s.registry != nil {
		s.registerCollectionRoutes(mux, "/api/v1/collections/{name}")
		mux.HandleFunc("GET /api/v1/collections", s.handleListCollections)
	} else {
		s.registerCollectionRoutes(mux, "/api/v1")
	}

	return recoverPanic(securityHeaders(s.rateLimitMiddleware(s.requireAuth(maxBodySize(mux)))))
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

	// Start rate limiter cleanup goroutine if rate limiting is enabled.
	// The goroutine stops when closeCh is signaled, which happens on
	// both graceful shutdown (Close) and startup failure (port in use).
	if s.limiter != nil {
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

	s.serverMu.Lock()
	s.httpServer = &http.Server{
		Addr:         addr,
		Handler:      s.Handler(),
		ReadTimeout:  s.readTimeout,
		WriteTimeout: s.writeTimeout,
		IdleTimeout:  s.idleTimeout,
	}
	s.serverMu.Unlock()
	err := s.httpServer.ListenAndServe()
	// Signal cleanup goroutine to stop — runs on both success and failure paths
	select {
	case <-s.closeCh:
	default:
		close(s.closeCh)
	}
	if err == http.ErrServerClosed {
		return nil // Shutdown initiated by Close(), not an error
	}
	return err
}

// Close shuts down the server gracefully, stopping the rate limiter cleanup goroutine
// and shutting down the HTTP server with a 5-second timeout.
func (s *Server) Close() error {
	// Signal background goroutines to stop
	select {
	case <-s.closeCh:
	default:
		close(s.closeCh)
	}
	// Shut down the HTTP server
	s.serverMu.Lock()
	srv := s.httpServer
	s.serverMu.Unlock()
	if srv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(ctx)
	}
	return nil
}

// handleVersion returns the server version.
func (s *Server) handleVersion(w http.ResponseWriter, r *http.Request) {
	resp := map[string]interface{}{"version": "go-anki/1.0.0"}
	jsonResponse(w, addCollection(r, resp))
}

// handleListCollections returns the names of all registered collections.
// Only available in multi-collection mode.
func (s *Server) handleListCollections(w http.ResponseWriter, _ *http.Request) {
	jsonResponse(w, map[string]interface{}{"collections": s.registry.Names()})
}

// handleGetDecks returns all decks in the collection.
func (s *Server) handleGetDecks(col *collection.Collection, w http.ResponseWriter, r *http.Request) {
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
	jsonResponse(w, addCollection(r, map[string]interface{}{"decks": deckList}))
}

// handleGetDueCards returns cards that are due for review.
func (s *Server) handleGetDueCards(col *collection.Collection, w http.ResponseWriter, r *http.Request) {
	filter := goanki.DueCardsFilter{}

	if deck := r.URL.Query().Get("deck"); deck != "" {
		filter.DeckName = deck
	}
	if limitStr := r.URL.Query().Get("limit"); limitStr != "" {
		if n, err := strconv.Atoi(limitStr); err == nil && n > 0 {
			filter.Limit = n
		}
	}
	if filter.Limit <= 0 {
		filter.Limit = 100
	}
	if filter.Limit > goanki.MaxCardsPerQuery {
		filter.Limit = goanki.MaxCardsPerQuery
	}

	cards, err := col.GetDueCards(filter)
	if err != nil {
		errorResponse(w, http.StatusInternalServerError, sanitizeErr(err))
		return
	}
	jsonResponse(w, addCollection(r, map[string]interface{}{"cards": cards}))
}

// handleGetStats returns collection statistics.
func (s *Server) handleGetStats(col *collection.Collection, w http.ResponseWriter, r *http.Request) {
	stats, err := col.GetStats()
	if err != nil {
		errorResponse(w, http.StatusInternalServerError, sanitizeErr(err))
		return
	}
	jsonResponse(w, addCollection(r, map[string]interface{}{"stats": stats}))
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
	jsonResponse(w, addCollection(r, map[string]interface{}{"card": card}))
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
	jsonResponse(w, addCollection(r, map[string]interface{}{
		"card":   answer.Card,
		"review": answer.Review,
	}))
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
	jsonResponse(w, addCollection(r, map[string]interface{}{"deck_id": deckID}))
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
	jsonResponse(w, addCollection(r, map[string]interface{}{"note_id": noteID}))
}

// handleSyncDownload performs a full download from AnkiWeb.
func (s *Server) handleSyncDownload(w http.ResponseWriter, r *http.Request) {
	// Use per-collection lock in multi-collection mode for concurrent sync isolation
	s.lockForCollection(r)
	defer s.unlockForCollection(r)

	// Block write handlers from opening the DB while sync replaces the file
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	if s.syncConfig == nil {
		errorResponse(w, http.StatusServiceUnavailable, "sync not configured: set SyncConfig")
		return
	}

	dbPath := s.dbPathFromContext(r)

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

	_, err = client.FullDownload(ctx, dbPath, s.mediaDir)
	if err != nil {
		errorResponse(w, http.StatusInternalServerError, sanitizeErr(err))
		return
	}

	// Count cards in the downloaded collection
	cardCount := 0
	col, err := collection.Open(dbPath, collection.ReadOnly)
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

	jsonResponse(w, addCollection(r, map[string]interface{}{
		"status": "ok",
		"cards":  cardCount,
	}))
}

// handleSyncUpload performs a full upload to AnkiWeb.
func (s *Server) handleSyncUpload(w http.ResponseWriter, r *http.Request) {
	// Use per-collection lock in multi-collection mode for concurrent sync isolation
	s.lockForCollection(r)
	defer s.unlockForCollection(r)

	// Block write handlers from opening the DB while sync reads it
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	if s.syncConfig == nil {
		errorResponse(w, http.StatusServiceUnavailable, "sync not configured: set SyncConfig")
		return
	}

	dbPath := s.dbPathFromContext(r)

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

	if err := client.FullUpload(ctx, dbPath, s.mediaDir); err != nil {
		errorResponse(w, http.StatusInternalServerError, sanitizeErr(err))
		return
	}

	jsonResponse(w, addCollection(r, map[string]interface{}{"status": "ok"}))
}

// handleSyncDelta performs an incremental delta sync with AnkiWeb.
func (s *Server) handleSyncDelta(w http.ResponseWriter, r *http.Request) {
	// Use per-collection lock in multi-collection mode for concurrent sync isolation
	s.lockForCollection(r)
	defer s.unlockForCollection(r)

	// Block write handlers from opening the DB while sync operates
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	if s.syncConfig == nil {
		errorResponse(w, http.StatusServiceUnavailable, "sync not configured: set SyncConfig")
		return
	}

	dbPath := s.dbPathFromContext(r)

	client, err := sync.NewDeltaClient(*s.syncConfig)
	if err != nil {
		log.Printf("create delta sync client: %v", err)
		errorResponse(w, http.StatusInternalServerError, "sync client initialization failed")
		return
	}
	ctx := r.Context()

	if err := client.Authenticate(ctx); err != nil {
		errorResponse(w, http.StatusUnauthorized, "sync authentication failed")
		return
	}

	// Count cards before sync
	cardsBefore := 0
	col, err := collection.Open(dbPath, collection.ReadOnly)
	if err == nil {
		stats, statsErr := col.GetStats()
		if statsErr == nil {
			cardsBefore = stats.TotalCards
		}
		_ = col.Close()
	}

	if err := client.FullSync(ctx, dbPath); err != nil {
		errorResponse(w, http.StatusInternalServerError, sanitizeErr(err))
		return
	}

	// Count cards after sync
	cardsAfter := 0
	col, err = collection.Open(dbPath, collection.ReadOnly)
	if err == nil {
		stats, statsErr := col.GetStats()
		if statsErr == nil {
			cardsAfter = stats.TotalCards
		}
		_ = col.Close()
	}

	jsonResponse(w, addCollection(r, map[string]interface{}{
		"status":       "ok",
		"cards_before": cardsBefore,
		"cards_after":  cardsAfter,
	}))
}
