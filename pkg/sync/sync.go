// Package sync provides an AnkiWeb sync client for the go-anki library.
//
// It implements the AnkiWeb sync protocol for full download and upload
// of Anki collections. Incremental/delta sync (v2) is not yet supported.
//
// Usage:
//
//	client := sync.NewClient(sync.SyncConfig{
//	    Username: "user@example.com",
//	    Password: "secret",
//	})
//
//	// Authenticate
//	if err := client.Authenticate(ctx); err != nil {
//	    log.Fatal(err)
//	}
//
//	// Get server metadata
//	meta, err := client.Meta(ctx)
//
//	// Full download to local path
//	err = client.FullDownload(ctx, "/path/to/collection.anki2", "/path/to/media_dir")
//
//	// Full upload from local collection
//	err = client.FullUpload(ctx, "/path/to/collection.anki2", "/path/to/media_dir")
package sync

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"time"

	goanki "github.com/vpul/go-anki/pkg/apkg"
	goankitypes "github.com/vpul/go-anki/pkg/types"
)

const (
	// DefaultSyncURL is the default AnkiWeb sync server URL.
	DefaultSyncURL = "https://sync.ankiweb.net/sync/"

	// SyncProtocolVersion is the AnkiWeb protocol version.
	SyncProtocolVersion = 11

	// hostKeyEndpoint is the authentication endpoint.
	hostKeyEndpoint = "hostKey"

	// DownloadEndpoint is the full download endpoint suffix.
	DownloadEndpoint = "download"

	// UploadEndpoint is the full upload endpoint suffix.
	UploadEndpoint = "upload"
)

// SyncMeta holds metadata returned by the AnkiWeb sync handshake.
// This mirrors goankitypes.SyncMeta but provides local convenience.
type SyncMeta struct {
	Modified int64 `json:"mod"`  // Server modification timestamp
	SchemaMod int64 `json:"scm"` // Schema modification count
	USN      int   `json:"usn"`  // Server update sequence number
	MediaUSN int   `json:"musn"` // Media update sequence number
	Timestamp int64 `json:"ts"`  // Server timestamp
}

// DownloadResult holds the result of a full download operation.
type DownloadResult struct {
	// ModifiedTimestamp is the server modification timestamp from the response headers.
	ModifiedTimestamp int64
	// UpdateSequenceNumber is the server USN from the response headers.
	UpdateSequenceNumber int
	// DBPath is the local path where the collection database was saved.
	DBPath string
	// MediaDir is the local path where media files were extracted.
	MediaDir string
	// MediaFilesImported is the number of media files extracted.
	MediaFilesImported int
}

// Client is an AnkiWeb sync client.
type Client struct {
	config    goankitypes.SyncConfig
	baseURL   string
	httpClient *http.Client
	sessionKey string
}

// NewClient creates a new AnkiWeb sync client with the given configuration.
func NewClient(config goankitypes.SyncConfig) *Client {
	return &Client{
		config:  config,
		baseURL: DefaultSyncURL,
		httpClient: &http.Client{
			Timeout: 5 * time.Minute, // Large downloads may take time
		},
	}
}

// NewClientWithURL creates a new AnkiWeb sync client with a custom base URL.
// This is useful for testing against a local ankisyncd server.
func NewClientWithURL(config goankitypes.SyncConfig, baseURL string) *Client {
	return &Client{
		config:  config,
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: 5 * time.Minute,
		},
	}
}

// SetHTTPClient sets a custom HTTP client for the sync operations.
// This allows configuring proxies, TLS settings, timeouts, etc.
func (c *Client) SetHTTPClient(client *http.Client) {
	c.httpClient = client
}

// Authenticate sends credentials to AnkiWeb and obtains a session key.
// The session key is stored internally and used for subsequent requests.
func (c *Client) Authenticate(ctx context.Context) error {
	authURL := c.baseURL + hostKeyEndpoint

	data := url.Values{}
	data.Set("u", c.config.Username)
	data.Set("p", c.config.Password)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, authURL, bytes.NewBufferString(data.Encode()))
	if err != nil {
		return fmt.Errorf("create auth request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("auth request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("auth failed (status %d): %s", resp.StatusCode, string(body))
	}

	var result struct {
		Key string `json:"key"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("decode auth response: %w", err)
	}

	if result.Key == "" {
		return fmt.Errorf("authentication failed: no session key returned (check credentials)")
	}

	c.sessionKey = result.Key
	return nil
}

// SessionKey returns the current session key, or empty string if not authenticated.
func (c *Client) SessionKey() string {
	return c.sessionKey
}

// Meta retrieves server metadata (modification timestamp and USN).
// Requires prior authentication via Authenticate().
func (c *Client) Meta(ctx context.Context) (*SyncMeta, error) {
	if c.sessionKey == "" {
		return nil, fmt.Errorf("not authenticated; call Authenticate() first")
	}

	syncURL := c.baseURL + "?k=" + url.QueryEscape(c.sessionKey) + "&meta=1"

	body, err := json.Marshal(map[string]int{"v": SyncProtocolVersion})
	if err != nil {
		return nil, fmt.Errorf("marshal meta request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, syncURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create meta request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("meta request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("meta request failed (status %d): %s", resp.StatusCode, string(respBody))
	}

	var meta SyncMeta
	if err := json.NewDecoder(resp.Body).Decode(&meta); err != nil {
		return nil, fmt.Errorf("decode meta response: %w", err)
	}

	return &meta, nil
}

// FullDownload downloads the full collection from AnkiWeb.
// It saves the .colpkg to a temporary file, then extracts the collection
// database and media files to the specified paths.
//
// Parameters:
//   - ctx: context for cancellation
//   - dbPath: path where the collection.anki2 database will be saved
//   - mediaDir: directory where media files will be extracted (created if needed)
func (c *Client) FullDownload(ctx context.Context, dbPath string, mediaDir string) (*DownloadResult, error) {
	if c.sessionKey == "" {
		return nil, fmt.Errorf("not authenticated; call Authenticate() first")
	}

	// Create temporary file for the downloaded .colpkg
	tmpDir, err := os.MkdirTemp("", "anki-sync-download-*")
	if err != nil {
		return nil, fmt.Errorf("create temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	colpkgPath := filepath.Join(tmpDir, "download.colpkg")

	syncURL := c.baseURL + "?k=" + url.QueryEscape(c.sessionKey) + "&download=1"

	body, err := json.Marshal(map[string]int{"v": SyncProtocolVersion})
	if err != nil {
		return nil, fmt.Errorf("marshal download request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, syncURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create download request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("download failed (status %d): %s", resp.StatusCode, string(respBody))
	}

	// Parse headers for metadata
	var modTimestamp int64
	var usn int
	if v := resp.Header.Get("X-Database-Modification-Timestamp"); v != "" {
		modTimestamp, _ = strconv.ParseInt(v, 10, 64)
	}
	if v := resp.Header.Get("X-Database-Update-Sequence-Number"); v != "" {
		usn, _ = strconv.Atoi(v)
	}

	// Save the .colpkg to temp file
	colpkgFile, err := os.Create(colpkgPath)
	if err != nil {
		return nil, fmt.Errorf("create temp colpkg file: %w", err)
	}

	if _, err := io.Copy(colpkgFile, resp.Body); err != nil {
		_ = colpkgFile.Close()
		return nil, fmt.Errorf("save downloaded colpkg: %w", err)
	}
	_ = colpkgFile.Close()

	// Extract the .colpkg using the existing ImportColpkg function
	result, err := goanki.ImportColpkg(colpkgPath, filepath.Dir(dbPath))
	if err != nil {
		return nil, fmt.Errorf("import downloaded colpkg: %w", err)
	}

	// If dbPath specifies a different directory, move the extracted file
	extractedDB := filepath.Join(filepath.Dir(dbPath), "collection.anki2")
	if extractedDB != dbPath {
		if err := os.Rename(extractedDB, dbPath); err != nil {
			return nil, fmt.Errorf("rename extracted database: %w", err)
		}
	}

	// Set up media directory
	if mediaDir == "" {
		mediaDir = filepath.Join(filepath.Dir(dbPath), "collection.media")
	}
	if err := os.MkdirAll(mediaDir, 0755); err != nil {
		return nil, fmt.Errorf("create media directory: %w", err)
	}

	// Move media files from extraction dir to the specified mediaDir
	extractedMedia := filepath.Join(filepath.Dir(dbPath), "collection.media")
	if extractedMedia != mediaDir {
		// Copy media files to the target media directory
		mediaFiles, err := os.ReadDir(extractedMedia)
		if err == nil {
			for _, f := range mediaFiles {
				if f.IsDir() {
					continue
				}
				src := filepath.Join(extractedMedia, f.Name())
				dst := filepath.Join(mediaDir, f.Name())
				data, err := os.ReadFile(src)
				if err != nil {
					continue
				}
				_ = os.WriteFile(dst, data, 0644)
			}
		}
	}

	return &DownloadResult{
		ModifiedTimestamp:    modTimestamp,
		UpdateSequenceNumber: usn,
		DBPath:              dbPath,
		MediaDir:            mediaDir,
		MediaFilesImported:   result.MediaFilesImported,
	}, nil
}

// FullUpload uploads the full collection to AnkiWeb.
// It creates a .colpkg from the local collection database and media directory,
// then uploads it to AnkiWeb.
//
// Parameters:
//   - ctx: context for cancellation
//   - dbPath: path to the local collection.anki2 database
//   - mediaDir: directory containing media files (can be empty string for no media)
func (c *Client) FullUpload(ctx context.Context, dbPath string, mediaDir string) error {
	if c.sessionKey == "" {
		return fmt.Errorf("not authenticated; call Authenticate() first")
	}

	// Create temporary .colpkg file
	tmpDir, err := os.MkdirTemp("", "anki-sync-upload-*")
	if err != nil {
		return fmt.Errorf("create temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	colpkgPath := filepath.Join(tmpDir, "upload.colpkg")

	// Build export options
	opts := goanki.ExportColpkgOptions{
		SourceDB:   dbPath,
		OutputPath: colpkgPath,
	}
	if mediaDir != "" {
		opts.MediaDir = mediaDir
	}

	if err := goanki.ExportColpkg(opts); err != nil {
		return fmt.Errorf("export colpkg: %w", err)
	}

	// Read the .colpkg file
	colpkgData, err := os.ReadFile(colpkgPath)
	if err != nil {
		return fmt.Errorf("read colpkg file: %w", err)
	}

	// Upload via multipart form
	syncURL := c.baseURL + "?k=" + url.QueryEscape(c.sessionKey) + "&upload=1"

	// Build multipart form
	var formBuf bytes.Buffer
	formWriter := multipart.NewWriter(&formBuf)

	// Add the colpkg file
	fileWriter, err := formWriter.CreateFormFile("file", filepath.Base(colpkgPath))
	if err != nil {
		return fmt.Errorf("create form file field: %w", err)
	}
	if _, err := fileWriter.Write(colpkgData); err != nil {
		return fmt.Errorf("write colpkg to form: %w", err)
	}

	if err := formWriter.Close(); err != nil {
		return fmt.Errorf("close form writer: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, syncURL, &formBuf)
	if err != nil {
		return fmt.Errorf("create upload request: %w", err)
	}
	req.Header.Set("Content-Type", formWriter.FormDataContentType())

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("upload request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("upload failed (status %d): %s", resp.StatusCode, string(respBody))
	}

	return nil
}
