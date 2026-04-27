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
	"log"
	"mime/multipart"
	"net"
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
	// maxDownloadSize is the maximum size (500MB) of a response body from AnkiWeb.
	maxDownloadSize int64 = 500 * 1024 * 1024

	// downloadTimeout is the overall timeout for download operations.
	downloadTimeout = 5 * time.Minute
)

const (
	// DefaultSyncURL is the default AnkiWeb sync server URL.
	DefaultSyncURL = "https://sync.ankiweb.net/sync/"

	// SyncProtocolVersion is the AnkiWeb protocol version.
	SyncProtocolVersion = 11

	// hostKeyEndpoint is the authentication endpoint.
	hostKeyEndpoint = "hostKey"

	// downloadEndpoint is the full download endpoint suffix.
	downloadEndpoint = "download"

	// uploadEndpoint is the full upload endpoint suffix.
	uploadEndpoint = "upload"
)

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
	config     goankitypes.SyncConfig
	password   string // unexported to prevent leaking via JSON/string representations
	baseURL    string
	httpClient *http.Client
	sessionKey string
}

// SetPassword sets the sync password. Use this instead of setting Password
// directly on SyncConfig to keep the credential in an unexported field.
func (c *Client) SetPassword(pw string) {
	c.password = pw
}

// NewClient creates a new AnkiWeb sync client with the given configuration.
func NewClient(config goankitypes.SyncConfig) (*Client, error) {
	if err := validateURL(DefaultSyncURL); err != nil {
		return nil, fmt.Errorf("validate default sync URL: %w", err)
	}
	transport := &http.Transport{
		DialContext: ssrfSafeDialContext,
	}
	return &Client{
		config:   config,
		password: config.Password,
		baseURL:  DefaultSyncURL,
		httpClient: &http.Client{
			Timeout:   5 * time.Minute, // Large downloads may take time
			Transport: transport,
		},
	}, nil
}

// NewClientWithURL creates a new AnkiWeb sync client with a custom base URL.
// This is useful for testing against a local ankisyncd server.
func NewClientWithURL(config goankitypes.SyncConfig, baseURL string) (*Client, error) {
	if err := validateURL(baseURL); err != nil {
		return nil, fmt.Errorf("validate sync URL: %w", err)
	}
	transport := &http.Transport{
		DialContext: ssrfSafeDialContext,
	}
	return &Client{
		config:   config,
		password: config.Password,
		baseURL:  baseURL,
		httpClient: &http.Client{
			Timeout:   5 * time.Minute,
			Transport: transport,
		},
	}, nil
}

// SetHTTPClient sets a custom HTTP client for the sync operations.
// This allows configuring proxies, TLS settings, timeouts, etc.
func (c *Client) SetHTTPClient(client *http.Client) {
	c.httpClient = client
}

// buildURL constructs a sync endpoint URL with the given path and query parameters.
func (c *Client) buildURL(path string, query url.Values) (string, error) {
	u, err := url.Parse(c.baseURL)
	if err != nil {
		return "", fmt.Errorf("parse base URL: %w", err)
	}
	if path != "" {
		u = u.JoinPath(path)
	}
	if query != nil {
		q := u.Query()
		for k, vs := range query {
			for _, v := range vs {
				q.Set(k, v)
			}
		}
		u.RawQuery = q.Encode()
	}
	return u.String(), nil
}

// Authenticate sends credentials to AnkiWeb and obtains a session key.
// The session key is stored internally and used for subsequent requests.
func (c *Client) Authenticate(ctx context.Context) error {
	authURL, err := c.buildURL(hostKeyEndpoint, nil)
	if err != nil {
		return fmt.Errorf("build auth URL: %w", err)
	}

	data := url.Values{}
	data.Set("u", c.config.Username)
	data.Set("p", c.password)

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
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
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
func (c *Client) Meta(ctx context.Context) (*goankitypes.SyncMeta, error) {
	if c.sessionKey == "" {
		return nil, fmt.Errorf("not authenticated; call Authenticate() first")
	}

	query := url.Values{}
	query.Set("k", c.sessionKey)
	query.Set("meta", "1")
	syncURL, err := c.buildURL("", query)
	if err != nil {
		return nil, fmt.Errorf("build meta URL: %w", err)
	}

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
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
		return nil, fmt.Errorf("meta request failed (status %d): %s", resp.StatusCode, string(respBody))
	}

	var meta goankitypes.SyncMeta
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

	// Apply overall download timeout
	dlCtx, cancel := context.WithTimeout(ctx, downloadTimeout)
	defer cancel()

	// Create temporary file for the downloaded .colpkg
	tmpDir, err := os.MkdirTemp("", "anki-sync-download-*")
	if err != nil {
		return nil, fmt.Errorf("create temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	colpkgPath := filepath.Join(tmpDir, "download.colpkg")

	query := url.Values{}
	query.Set("k", c.sessionKey)
	query.Set(downloadEndpoint, "1")
	syncURL, err := c.buildURL("", query)
	if err != nil {
		return nil, fmt.Errorf("build download URL: %w", err)
	}

	body, err := json.Marshal(map[string]int{"v": SyncProtocolVersion})
	if err != nil {
		return nil, fmt.Errorf("marshal download request: %w", err)
	}

	req, err := http.NewRequestWithContext(dlCtx, http.MethodPost, syncURL, bytes.NewReader(body))
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
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
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

	// Wrap the response body with a LimitedReader to enforce max download size.
	// Use maxDownloadSize+1 so we can detect responses that exceed the limit
	// (an exact-size response should leave N=1, not N=0 which is ambiguous).
	limitedReader := &io.LimitedReader{R: resp.Body, N: maxDownloadSize + 1}

	// Save the .colpkg to temp file
	colpkgFile, err := os.Create(colpkgPath)
	if err != nil {
		return nil, fmt.Errorf("create temp colpkg file: %w", err)
	}

	if _, err := io.Copy(colpkgFile, limitedReader); err != nil {
		_ = colpkgFile.Close()
		return nil, fmt.Errorf("save downloaded colpkg: %w", err)
	}
	_ = colpkgFile.Close()

	// Check if we hit the download size limit
	if limitedReader.N < 1 {
		return nil, fmt.Errorf("download exceeded maximum size of %d bytes", maxDownloadSize)
	}

	// Extract the .colpkg using the existing ImportColpkg function.
	// We extract to a temporary directory first, then atomically move
	// the database file to the final location. This prevents data loss
	// if extraction fails partway through — the original file remains intact.
	extractDir := filepath.Join(tmpDir, "extracted")
	if err := os.MkdirAll(extractDir, 0755); err != nil {
		return nil, fmt.Errorf("create extraction dir: %w", err)
	}

	_, err = goanki.ImportColpkg(colpkgPath, extractDir)
	if err != nil {
		return nil, fmt.Errorf("import downloaded colpkg: %w", err)
	}

	// Atomically move the extracted database to the final location.
	extractedDB := filepath.Join(extractDir, "collection.anki2")
	if err := os.MkdirAll(filepath.Dir(dbPath), 0700); err != nil {
		return nil, fmt.Errorf("create db directory: %w", err)
	}
	if err := renameWithCopy(extractedDB, dbPath, 0600); err != nil {
		return nil, fmt.Errorf("move database to final location: %w", err)
	}

	// Set up media directory
	if mediaDir == "" {
		mediaDir = filepath.Join(filepath.Dir(dbPath), "collection.media")
	}
	if err := os.MkdirAll(mediaDir, 0755); err != nil {
		return nil, fmt.Errorf("create media directory: %w", err)
	}

	// Move media files from extraction dir to the specified mediaDir
	extractedMedia := filepath.Join(extractDir, "collection.media")
	movedCount := moveMediaFiles(extractedMedia, mediaDir)

	return &DownloadResult{
		ModifiedTimestamp:    modTimestamp,
		UpdateSequenceNumber: usn,
		DBPath:              dbPath,
		MediaDir:            mediaDir,
		MediaFilesImported:   movedCount,
	}, nil
}

// ssrfSafeDialContext is a custom DialContext that resolves the hostname and
// validates the IP against private ranges at connection time, preventing DNS
// rebinding attacks where the IP changes between validation and connection.
func ssrfSafeDialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("invalid address %q: %w", addr, err)
	}

	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("resolve %q: %w", host, err)
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("no IP addresses found for %q", host)
	}

	for _, ipAddr := range ips {
		ip := ipAddr.IP
		if v4 := ip.To4(); v4 != nil {
			ip = v4
		}
		if isPrivateIP(ip) {
			if !ip.IsLoopback() {
				return nil, fmt.Errorf("hostname %q resolves to private/reserved IP %s", host, ip)
			}
			// Allow loopback only for localhost/127.0.0.1/::1
			if host != "localhost" && host != "127.0.0.1" && host != "::1" {
				return nil, fmt.Errorf("hostname %q resolves to loopback IP %s", host, ip)
			}
		}
	}

	// Connect directly to the first resolved IP only (no fallback to ips[1..n]).
	// This is intentional: we validate ALL resolved IPs above, and connecting to
	// a single deterministic IP prevents an attacker from observing which IP
	// was reached. Fallback would also complicate the security reasoning.
	dialer := &net.Dialer{Timeout: 30 * time.Second}
	return dialer.DialContext(ctx, network, net.JoinHostPort(ips[0].IP.String(), port))
}

// validateURL checks that a URL is safe to connect to, preventing SSRF attacks.
// It requires HTTPS scheme (or HTTP only for localhost/loopback), and blocks
// connections to private/reserved IP ranges.
func validateURL(u string) error {
	parsed, err := url.Parse(u)
	if err != nil {
		return fmt.Errorf("parse URL: %w", err)
	}

	// Require HTTPS unless hostname is localhost or loopback
	isLocalhost := parsed.Hostname() == "localhost" || parsed.Hostname() == "127.0.0.1" || parsed.Hostname() == "::1"
	switch parsed.Scheme {
	case "https":
		// ok
	case "http":
		if !isLocalhost {
			return fmt.Errorf("insecure URL scheme %q: only https is allowed for remote hosts", parsed.Scheme)
		}
	default:
		return fmt.Errorf("unsupported URL scheme %q: must be http or https", parsed.Scheme)
	}

	hostname := parsed.Hostname()
	if hostname == "" {
		return fmt.Errorf("URL has no hostname")
	}

	// Resolve hostname to catch DNS-based SSRF
	addrs, err := net.LookupHost(hostname)
	if err != nil {
		return fmt.Errorf("resolve hostname %q: %w", hostname, err)
	}

	for _, addr := range addrs {
		ip := net.ParseIP(addr)
		if ip == nil {
			return fmt.Errorf("resolved non-IP address %q for hostname %q", addr, hostname)
		}
		// Normalize to 4-byte form for IPv4 addresses. This converts
		// ::ffff:a.b.c.d (IPv4-mapped IPv6) to plain 4-byte a.b.c.d, so that
		// IPv4-mapped addresses are checked against IPv4 private ranges instead
		// of being caught by an over-broad ::ffff:0:0/96 net.
		if v4 := ip.To4(); v4 != nil {
			ip = v4
		}
		if isPrivateIP(ip) {
			// Allow loopback only if explicitly using localhost
			if ip.IsLoopback() && isLocalhost {
				continue
			}
			return fmt.Errorf("hostname %q resolves to private/reserved IP %s", hostname, ip)
		}
	}

	return nil
}

// isPrivateIP checks whether an IP address is in a private or reserved range.
func isPrivateIP(ip net.IP) bool {
	privateRanges := []struct {
		network *net.IPNet
	}{
		{mustParseCIDR("10.0.0.0/8")},
		{mustParseCIDR("172.16.0.0/12")},
		{mustParseCIDR("192.168.0.0/16")},
		{mustParseCIDR("169.254.0.0/16")},
		{mustParseCIDR("127.0.0.0/8")},
		{mustParseCIDR("0.0.0.0/32")},
		{mustParseCIDR("::1/128")},
		{mustParseCIDR("fc00::/7")},  // IPv6 ULA (RFC 4193)
		{mustParseCIDR("fe80::/10")}, // IPv6 link-local
	}

	for _, r := range privateRanges {
		if r.network.Contains(ip) {
			return true
		}
	}
	return false
}

// mustParseCIDR parses a CIDR string or panics. Used for static CIDR ranges.
func mustParseCIDR(s string) *net.IPNet {
	_, network, err := net.ParseCIDR(s)
	if err != nil {
		panic(err)
	}
	return network
}

// copyFile copies a file from src to dst with the given permissions.
func copyFile(dst, src string, perm os.FileMode) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("read source file: %w", err)
	}
	if err := os.WriteFile(dst, data, perm); err != nil {
		return fmt.Errorf("write destination file: %w", err)
	}
	return nil
}

// renameWithCopy renames src to dst, falling back to copy+delete if the
// rename crosses filesystem boundaries (e.g., /tmp → /data).
// The perm parameter is used when falling back to copy.
func renameWithCopy(src, dst string, perm os.FileMode) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	// Fallback: copy then delete (cross-filesystem move)
	if err := copyFile(dst, src, perm); err != nil {
		return fmt.Errorf("copy: %w", err)
	}
	if err := os.Remove(src); err != nil {
		// Non-fatal: the copy succeeded, just a leftover temp file
		log.Printf("warning: failed to remove temp file %s: %v", src, err)
	}
	return nil
}

// moveMediaFiles moves media files from srcDir to dstDir.
// Missing srcDir is non-fatal (no media to extract).
// Returns the number of files successfully moved.
func moveMediaFiles(srcDir, dstDir string) int {
	moved := 0
	entries, err := os.ReadDir(srcDir)
	if err != nil {
		return 0 // missing or unreadable source dir is non-fatal
	}
	for _, f := range entries {
		if f.IsDir() {
			continue
		}
		src := filepath.Join(srcDir, f.Name())
		dst := filepath.Join(dstDir, f.Name())
		if err := renameWithCopy(src, dst, 0644); err != nil {
			log.Printf("warning: failed to move media file %s: %v", f.Name(), err)
		} else {
			moved++
		}
	}
	return moved
}

// FullUpload uploads the full collection to AnkiWeb.
// It creates a .colpkg from the local collection database and media directory,
// then uploads it to AnkiWeb. The file is streamed from disk using io.Pipe
// to avoid loading the entire .colpkg into memory.
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

	// Build the upload URL
	query := url.Values{}
	query.Set("k", c.sessionKey)
	query.Set(uploadEndpoint, "1")
	syncURL, err := c.buildURL("", query)
	if err != nil {
		return fmt.Errorf("build upload URL: %w", err)
	}

	// Open the colpkg file for streaming upload
	colpkgFile, err := os.Open(colpkgPath)
	if err != nil {
		return fmt.Errorf("open colpkg file: %w", err)
	}
	defer func() { _ = colpkgFile.Close() }()

	// Stream the multipart form using a pipe to avoid reading the entire file into memory
	pr, pw := io.Pipe()
	formWriter := multipart.NewWriter(pw)

	go func() {
		defer func() { _ = pw.Close() }()

		part, err := formWriter.CreateFormFile("file", filepath.Base(colpkgPath))
		if err != nil {
			pw.CloseWithError(fmt.Errorf("create form file field: %w", err))
			return
		}
		if _, err := io.Copy(part, colpkgFile); err != nil {
			pw.CloseWithError(fmt.Errorf("copy colpkg to form: %w", err))
			return
		}
		if err := formWriter.Close(); err != nil {
			pw.CloseWithError(fmt.Errorf("close form writer: %w", err))
			return
		}
	}()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, syncURL, pr)
	if err != nil {
		pw.CloseWithError(fmt.Errorf("create upload request: %w", err))
		return fmt.Errorf("create upload request: %w", err)
	}
	req.Header.Set("Content-Type", formWriter.FormDataContentType())

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("upload request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
		return fmt.Errorf("upload failed (status %d): %s", resp.StatusCode, string(respBody))
	}

	return nil
}
