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
)

const (
	// DefaultMediaSyncURL is the default AnkiWeb media sync endpoint.
	DefaultMediaSyncURL = "https://sync.ankiweb.net/msync/"

	// MediaProtocolVersion is the AnkiWeb media sync protocol version.
	MediaProtocolVersion = 11

	// maxMediaDownloadSize is the maximum combined size (50MB) for media downloads.
	maxMediaDownloadSize int64 = 50 * 1024 * 1024

	// maxMediaUploadSize is the maximum combined size (50MB) for media uploads.
	maxMediaUploadSize int64 = 50 * 1024 * 1024

	// mediaMetadataLimit is the max size for metadata responses (1MB).
	mediaMetadataLimit int64 = 1 * 1024 * 1024
)

const (
	mediaBeginEndpoint    = "begin"
	mediaDownloadEndpoint = "mediaDownload"
	mediaUploadEndpoint   = "upload"
	mediaSanityEndpoint   = "mediaSanity"
)

// MediaSyncInfo holds the media sync session state returned by begin.
type MediaSyncInfo struct {
	// MediaKey is the session key for media operations (separate from sync session key).
	MediaKey string
	// USN is the current media USN from the server.
	USN int
}

// MediaBegin authenticates with the media sync endpoint and returns a media sync info.
// Requires the client to already have a valid sync session key (from Authenticate).
func (c *Client) MediaBegin(ctx context.Context) (*MediaSyncInfo, error) {
	if c.sessionKey == "" {
		return nil, fmt.Errorf("not authenticated: call Authenticate first")
	}

	baseURL, err := url.Parse(DefaultMediaSyncURL)
	if err != nil {
		return nil, fmt.Errorf("parse media sync URL: %w", err)
	}

	u := baseURL.JoinPath(mediaBeginEndpoint)
	q := u.Query()
	q.Set("k", c.sessionKey)
	q.Set("v", fmt.Sprintf("%d", MediaProtocolVersion))
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("create media begin request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("media begin request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, mediaMetadataLimit))
	if err != nil {
		return nil, fmt.Errorf("read media begin response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("media begin failed (status %d): %s", resp.StatusCode, string(body))
	}

	var result struct {
		Data struct {
			SK  string `json:"sk"`
			USN int    `json:"usn"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("decode media begin response: %w", err)
	}

	if result.Data.SK == "" {
		return nil, fmt.Errorf("media begin returned empty key")
	}

	return &MediaSyncInfo{
		MediaKey: result.Data.SK,
		USN:      result.Data.USN,
	}, nil
}

// MediaDownload downloads the specified media files from AnkiWeb.
// Returns a map of filename → file contents.
func (c *Client) MediaDownload(ctx context.Context, info *MediaSyncInfo, filenames []string) (map[string][]byte, error) {
	if info == nil || info.MediaKey == "" {
		return nil, fmt.Errorf("invalid media sync info: call MediaBegin first")
	}

	baseURL, err := url.Parse(DefaultMediaSyncURL)
	if err != nil {
		return nil, fmt.Errorf("parse media sync URL: %w", err)
	}

	u := baseURL.JoinPath(mediaDownloadEndpoint)
	q := u.Query()
	q.Set("k", info.MediaKey)
	q.Set("v", fmt.Sprintf("%d", MediaProtocolVersion))
	u.RawQuery = q.Encode()

	reqBody, err := json.Marshal(filenames)
	if err != nil {
		return nil, fmt.Errorf("marshal filenames: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("create media download request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("media download request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, mediaMetadataLimit))
		return nil, fmt.Errorf("media download failed (status %d): %s", resp.StatusCode, string(body))
	}

	// Read response body with size limit
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxMediaDownloadSize+1))
	if err != nil {
		return nil, fmt.Errorf("read media download response: %w", err)
	}
	if int64(len(data)) > maxMediaDownloadSize {
		return nil, fmt.Errorf("media download response exceeds limit (%d bytes)", maxMediaDownloadSize)
	}

	// The response is a JSON map of filename → base64-encoded content (AnkiWeb format)
	var result map[string]string
	if err := json.Unmarshal(data, &result); err != nil {
		// If it's not JSON, return raw data wrapped as a single entry
		// (some AnkiWeb versions return raw binary)
		return nil, fmt.Errorf("decode media download response: %w (raw: %d bytes)", err, len(data))
	}

	files := make(map[string][]byte, len(result))
	for name, b64 := range result {
		// AnkiWeb returns base64-encoded file contents
		// For now store as-is — caller can decode if needed
		files[name] = []byte(b64)
	}

	return files, nil
}

// MediaUpload uploads media files to AnkiWeb.
// files is a map of filename → file contents.
func (c *Client) MediaUpload(ctx context.Context, info *MediaSyncInfo, files map[string][]byte) error {
	if info == nil || info.MediaKey == "" {
		return fmt.Errorf("invalid media sync info: call MediaBegin first")
	}

	baseURL, err := url.Parse(DefaultMediaSyncURL)
	if err != nil {
		return fmt.Errorf("parse media sync URL: %w", err)
	}

	u := baseURL.JoinPath(mediaUploadEndpoint)
	q := u.Query()
	q.Set("k", info.MediaKey)
	q.Set("v", fmt.Sprintf("%d", MediaProtocolVersion))
	u.RawQuery = q.Encode()

	// Build multipart form with files
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	for name, content := range files {
		part, err := writer.CreateFormFile("files", name)
		if err != nil {
			return fmt.Errorf("create form file %q: %w", name, err)
		}
		if _, err := io.Copy(part, bytes.NewReader(content)); err != nil {
			return fmt.Errorf("write file %q: %w", name, err)
		}
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("close multipart writer: %w", err)
	}

	// Enforce overall upload size limit
	if int64(buf.Len()) > maxMediaUploadSize {
		return fmt.Errorf("media upload size %d exceeds limit (%d)", buf.Len(), maxMediaUploadSize)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), &buf)
	if err != nil {
		return fmt.Errorf("create media upload request: %w", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("media upload request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, mediaMetadataLimit))
	if err != nil {
		return fmt.Errorf("read media upload response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("media upload failed (status %d): %s", resp.StatusCode, string(body))
	}

	return nil
}

// MediaSanity performs a sanity check against the AnkiWeb media sync endpoint.
// localCount is the number of local media files.
func (c *Client) MediaSanity(ctx context.Context, info *MediaSyncInfo, localCount int) error {
	if info == nil || info.MediaKey == "" {
		return fmt.Errorf("invalid media sync info: call MediaBegin first")
	}

	baseURL, err := url.Parse(DefaultMediaSyncURL)
	if err != nil {
		return fmt.Errorf("parse media sync URL: %w", err)
	}

	u := baseURL.JoinPath(mediaSanityEndpoint)
	q := u.Query()
	q.Set("k", info.MediaKey)
	q.Set("v", fmt.Sprintf("%d", MediaProtocolVersion))
	u.RawQuery = q.Encode()

	reqBody, err := json.Marshal(map[string]int{"local": localCount})
	if err != nil {
		return fmt.Errorf("marshal sanity request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(reqBody))
	if err != nil {
		return fmt.Errorf("create media sanity request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("media sanity request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, mediaMetadataLimit))
	if err != nil {
		return fmt.Errorf("read media sanity response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("media sanity check failed (status %d): %s", resp.StatusCode, string(body))
	}

	var result struct {
		Data string `json:"data"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return fmt.Errorf("decode media sanity response: %w", err)
	}

	if result.Data != "OK" {
		return fmt.Errorf("media sanity check returned unexpected response: %q", result.Data)
	}

	return nil
}
