package sync

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	goankitypes "github.com/vpul/go-anki/pkg/types"
	"github.com/klauspost/compress/zstd"
)

func TestNewClient(t *testing.T) {
	config := goankitypes.SyncConfig{
		Username: "test@example.com",
		Password: "secret",
	}
	client, err := NewClient(config)
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}

	if client.baseURL != DefaultSyncURL {
		t.Errorf("expected baseURL %q, got %q", DefaultSyncURL, client.baseURL)
	}
	if client.sessionKey != "" {
		t.Errorf("expected empty session key, got %q", client.sessionKey)
	}
}

func TestNewClientWithURL(t *testing.T) {
	config := goankitypes.SyncConfig{
		Username: "test@example.com",
		Password: "secret",
	}
	customURL := "http://localhost:8080/sync/"
	client, err := NewClientWithURL(config, customURL)
	if err != nil {
		t.Fatalf("NewClientWithURL failed: %v", err)
	}

	if client.baseURL != customURL {
		t.Errorf("expected baseURL %q, got %q", customURL, client.baseURL)
	}
}

func TestAuthenticateSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/sync/hostKey" {
			t.Errorf("expected path /sync/hostKey, got %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"key":"test-session-key-123"}`))
	}))
	defer server.Close()

	client, err := NewClientWithURL(goankitypes.SyncConfig{
		Username: "test@example.com",
		Password: "secret",
	}, server.URL+"/sync/")
	if err != nil {
		t.Fatalf("NewClientWithURL failed: %v", err)
	}

	err = client.Authenticate(context.Background())
	if err != nil {
		t.Fatalf("Authenticate failed: %v", err)
	}
	if client.SessionKey() != "test-session-key-123" {
		t.Errorf("expected session key 'test-session-key-123', got %q", client.SessionKey())
	}
}

func TestAuthenticateFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()

	client, err := NewClientWithURL(goankitypes.SyncConfig{
		Username: "bad@example.com",
		Password: "wrong",
	}, server.URL+"/sync/")
	if err != nil {
		t.Fatalf("NewClientWithURL failed: %v", err)
	}

	err = client.Authenticate(context.Background())
	if err == nil {
		t.Fatal("expected authentication to fail, but it succeeded")
	}
}

func TestAuthenticateEmptyKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"key":""}`))
	}))
	defer server.Close()

	client, err := NewClientWithURL(goankitypes.SyncConfig{
		Username: "test@example.com",
		Password: "secret",
	}, server.URL+"/sync/")
	if err != nil {
		t.Fatalf("NewClientWithURL failed: %v", err)
	}

	err = client.Authenticate(context.Background())
	if err == nil {
		t.Fatal("expected error for empty session key")
	}
}

func TestMetaWithoutAuth(t *testing.T) {
	client, err := NewClient(goankitypes.SyncConfig{
		Username: "test@example.com",
		Password: "secret",
	})
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}
	_, err = client.Meta(context.Background())
	if err == nil {
		t.Fatal("expected error when calling Meta without authentication")
	}
}

func TestMetaSuccess(t *testing.T) {
	var sessionKey string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/sync/hostKey":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"key":"meta-test-key"}`))
			sessionKey = "meta-test-key"
		default:
			query := r.URL.Query()
			if query.Get("meta") != "1" {
				t.Errorf("expected meta=1 query parameter")
			}
			if query.Get("k") != sessionKey {
				t.Errorf("expected session key %q, got %q", sessionKey, query.Get("k"))
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"mod":1700000000,"scm":1700000000,"usn":42,"musn":10,"ts":1700000100}`))
		}
	}))
	defer server.Close()

	client, err := NewClientWithURL(goankitypes.SyncConfig{
		Username: "test@example.com",
		Password: "secret",
	}, server.URL+"/sync/")
	if err != nil {
		t.Fatalf("NewClientWithURL: %v", err)
	}

	if err := client.Authenticate(context.Background()); err != nil {
		t.Fatalf("Authenticate: %v", err)
	}

	meta, err := client.Meta(context.Background())
	if err != nil {
		t.Fatalf("Meta: %v", err)
	}

	if meta.Modified != 1700000000 {
		t.Errorf("expected mod=1700000000, got %d", meta.Modified)
	}
	if meta.USN != 42 {
		t.Errorf("expected usn=42, got %d", meta.USN)
	}
	if meta.MediaUSN != 10 {
		t.Errorf("expected musn=10, got %d", meta.MediaUSN)
	}
}

func TestDownloadWithoutAuth(t *testing.T) {
	client, err := NewClient(goankitypes.SyncConfig{
		Username: "test@example.com",
		Password: "secret",
	})
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}
	_, err = client.FullDownload(context.Background(), "/tmp/test.anki2", "/tmp/media")
	if err == nil {
		t.Fatal("expected error when calling FullDownload without authentication")
	}
}

func TestUploadWithoutAuth(t *testing.T) {
	client, err := NewClient(goankitypes.SyncConfig{
		Username: "test@example.com",
		Password: "secret",
	})
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}
	err = client.FullUpload(context.Background(), "/tmp/test.anki2", "/tmp/media")
	if err == nil {
		t.Fatal("expected error when calling FullUpload without authentication")
	}
}

func TestSetHTTPClient(t *testing.T) {
	client, err := NewClient(goankitypes.SyncConfig{
		Username: "test",
		Password: "test",
	})
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}
	customClient := &http.Client{Timeout: 0}
	client.SetHTTPClient(customClient)
	if client.httpClient != customClient {
		t.Error("SetHTTPClient did not set the client")
	}
}

func TestAuthURLConstruction(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify the URL path is correct for auth
		if r.URL.Path != "/sync/hostKey" {
			t.Errorf("expected path /sync/hostKey, got %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"key":"url-test"}`))
	}))
	defer server.Close()

	client, err := NewClientWithURL(goankitypes.SyncConfig{
		Username: "user",
		Password: "pass",
	}, server.URL+"/sync/")
	if err != nil {
		t.Fatalf("NewClientWithURL failed: %v", err)
	}

	if err := client.Authenticate(context.Background()); err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
}

func TestSyncURLWithTrailingSlash(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/sync/hostKey" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"key":"slash-test"}`))
		} else {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"mod":1,"usn":0}`))
		}
	}))
	defer server.Close()

	client, err := NewClientWithURL(goankitypes.SyncConfig{
		Username: "user",
		Password: "pass",
	}, server.URL+"/sync/")
	if err != nil {
		t.Fatalf("NewClientWithURL: %v", err)
	}

	if err := client.Authenticate(context.Background()); err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
}

// compressZstdTest compresses data using zstd for test fixtures.
func compressZstdTest(t *testing.T, data []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	writer, err := zstd.NewWriter(&buf)
	if err != nil {
		t.Fatalf("create zstd writer: %v", err)
	}
	if _, err := writer.Write(data); err != nil {
		t.Fatalf("write zstd data: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close zstd writer: %v", err)
	}
	return buf.Bytes()
}

// createTestColpkg creates a minimal .colpkg file for testing.
func createTestColpkg(t *testing.T, withMedia bool) []byte {
	t.Helper()

	buf := &bytes.Buffer{}
	zipWriter := zip.NewWriter(buf)

	// Create a minimal SQLite database placeholder
	dbData := append([]byte("SQLite format 3\x00"), make([]byte, 100)...)

	// Compress with zstd
	compressed := compressZstdTest(t, dbData)

	f, err := zipWriter.Create("collection.anki21b")
	if err != nil {
		t.Fatalf("create anki21b entry: %v", err)
	}
	if _, err := f.Write(compressed); err != nil {
		t.Fatalf("write anki21b entry: %v", err)
	}

	// Add placeholder
	f, err = zipWriter.Create("collection.anki2")
	if err != nil {
		t.Fatalf("create anki2 entry: %v", err)
	}
	if _, err := f.Write([]byte("")); err != nil {
		t.Fatalf("write anki2 entry: %v", err)
	}

	if withMedia {
		// Add media map
		mediaMap := map[string]string{"0": "test.png"}
		mediaMapData, _ := json.Marshal(mediaMap)
		f, err = zipWriter.Create("media")
		if err != nil {
			t.Fatalf("create media entry: %v", err)
		}
		if _, err := f.Write(mediaMapData); err != nil {
			t.Fatalf("write media entry: %v", err)
		}

		// Add media file
		f, err = zipWriter.Create("0")
		if err != nil {
			t.Fatalf("create media file entry: %v", err)
		}
		if _, err := f.Write([]byte("fake-png-data")); err != nil {
			t.Fatalf("write media file entry: %v", err)
		}
	} else {
		// Add empty media map
		mediaMap := map[string]string{}
		mediaMapData, _ := json.Marshal(mediaMap)
		f, err = zipWriter.Create("media")
		if err != nil {
			t.Fatalf("create media entry: %v", err)
		}
		if _, err := f.Write(mediaMapData); err != nil {
			t.Fatalf("write media entry: %v", err)
		}
	}

	if err := zipWriter.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}

	return buf.Bytes()
}

func TestFullDownloadSuccess(t *testing.T) {
	colpkgData := createTestColpkg(t, false)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/sync/hostKey":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"key":"download-test-key"}`))
		default:
			// Verify download query parameter
			if r.URL.Query().Get("download") != "1" {
				t.Errorf("expected download=1 query parameter, got %v", r.URL.Query())
			}
			if r.URL.Query().Get("k") == "" {
				t.Error("expected session key in query parameters")
			}
			// Verify request body contains protocol version
			body, _ := io.ReadAll(r.Body)
			var reqBody map[string]int
			if err := json.Unmarshal(body, &reqBody); err != nil {
				t.Errorf("failed to parse request body: %v", err)
			} else if reqBody["v"] != SyncProtocolVersion {
				t.Errorf("expected v=%d, got %d", SyncProtocolVersion, reqBody["v"])
			}
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Header().Set("X-Database-Modification-Timestamp", "1700000000")
			w.Header().Set("X-Database-Update-Sequence-Number", "42")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(colpkgData)
		}
	}))
	defer server.Close()

	client, err := NewClientWithURL(goankitypes.SyncConfig{
		Username: "test@example.com",
		Password: "secret",
	}, server.URL+"/sync/")
	if err != nil {
		t.Fatalf("NewClientWithURL: %v", err)
	}

	if err := client.Authenticate(context.Background()); err != nil {
		t.Fatalf("Authenticate: %v", err)
	}

	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "collection.anki2")
	mediaDir := filepath.Join(tmpDir, "collection.media")

	result, err := client.FullDownload(context.Background(), dbPath, mediaDir)
	if err != nil {
		t.Fatalf("FullDownload: %v", err)
	}

	// Verify header parsing
	if result.ModifiedTimestamp != 1700000000 {
		t.Errorf("expected ModifiedTimestamp=1700000000, got %d", result.ModifiedTimestamp)
	}
	if result.UpdateSequenceNumber != 42 {
		t.Errorf("expected UpdateSequenceNumber=42, got %d", result.UpdateSequenceNumber)
	}

	// Verify the database file exists (ImportColpkg extracts collection.anki2)
	if _, err := os.Stat(result.DBPath); os.IsNotExist(err) {
		t.Errorf("database file does not exist at %s", result.DBPath)
	}

	// Verify the media directory exists
	if _, err := os.Stat(result.MediaDir); os.IsNotExist(err) {
		t.Errorf("media directory does not exist at %s", result.MediaDir)
	}
}

func TestFullDownloadMediaRelocation(t *testing.T) {
	colpkgData := createTestColpkg(t, true)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/sync/hostKey":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"key":"media-test-key"}`))
		default:
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Header().Set("X-Database-Modification-Timestamp", "1700001000")
			w.Header().Set("X-Database-Update-Sequence-Number", "55")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(colpkgData)
		}
	}))
	defer server.Close()

	client, err := NewClientWithURL(goankitypes.SyncConfig{
		Username: "test@example.com",
		Password: "secret",
	}, server.URL+"/sync/")
	if err != nil {
		t.Fatalf("NewClientWithURL: %v", err)
	}

	if err := client.Authenticate(context.Background()); err != nil {
		t.Fatalf("Authenticate: %v", err)
	}

	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "collection.anki2")
	customMediaDir := filepath.Join(tmpDir, "custom_media")

	result, err := client.FullDownload(context.Background(), dbPath, customMediaDir)
	if err != nil {
		t.Fatalf("FullDownload: %v", err)
	}

	// Verify media files were relocated
	if result.MediaFilesImported != 1 {
		t.Errorf("expected 1 media file imported, got %d", result.MediaFilesImported)
	}

	// Verify the media file exists in the custom media dir
	mediaFile := filepath.Join(customMediaDir, "test.png")
	if _, err := os.Stat(mediaFile); os.IsNotExist(err) {
		t.Errorf("media file does not exist at %s", mediaFile)
	}

	// Verify the database was saved to the specified path
	if result.DBPath != dbPath {
		t.Errorf("expected DBPath=%q, got %q", dbPath, result.DBPath)
	}

	// Verify header parsing
	if result.ModifiedTimestamp != 1700001000 {
		t.Errorf("expected ModifiedTimestamp=1700001000, got %d", result.ModifiedTimestamp)
	}
	if result.UpdateSequenceNumber != 55 {
		t.Errorf("expected UpdateSequenceNumber=55, got %d", result.UpdateSequenceNumber)
	}
}

func TestFullDownloadServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/sync/hostKey":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"key":"error-test-key"}`))
		default:
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte("internal server error"))
		}
	}))
	defer server.Close()

	client, err := NewClientWithURL(goankitypes.SyncConfig{
		Username: "test@example.com",
		Password: "secret",
	}, server.URL+"/sync/")
	if err != nil {
		t.Fatalf("NewClientWithURL: %v", err)
	}

	if err := client.Authenticate(context.Background()); err != nil {
		t.Fatalf("Authenticate: %v", err)
	}

	tmpDir := t.TempDir()
	_, err = client.FullDownload(context.Background(), filepath.Join(tmpDir, "test.anki2"), filepath.Join(tmpDir, "media"))
	if err == nil {
		t.Fatal("expected error for server error response")
	}
}

func TestFullUploadSuccess(t *testing.T) {
	tmpDir := t.TempDir()
	sourceDB := filepath.Join(tmpDir, "collection.anki2")
	mediaDir := filepath.Join(tmpDir, "media")

	dbContent := []byte("SQLite format 3\x00" + string(make([]byte, 100)))

	// Create a minimal SQLite-like file for the upload
	if err := os.WriteFile(sourceDB, dbContent, 0644); err != nil {
		t.Fatal(err)
	}

	// Create media directory with a test file (should be ignored by streaming upload)
	if err := os.MkdirAll(mediaDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mediaDir, "test.png"), []byte("fake-png-data"), 0644); err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/sync/hostKey":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"key":"upload-test-key"}`))
		default:
			// Verify upload query parameters
			if r.URL.Query().Get("upload") != "1" {
				t.Errorf("expected upload=1 query parameter, got %v", r.URL.Query())
			}
			if r.URL.Query().Get("k") == "" {
				t.Error("expected session key in query parameters")
			}
			// Parse multipart form to verify "data" field with zstd content
			if err := r.ParseMultipartForm(10 << 20); err != nil {
				t.Errorf("parse multipart form: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			f, _, err := r.FormFile("data")
			if err != nil {
				t.Errorf("read 'data' field from multipart form: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			defer func() { _ = f.Close() }()
			// Decompress and verify the content matches the original DB
			dec, err := zstd.NewReader(f)
			if err != nil {
				t.Errorf("create zstd reader: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			defer dec.Close()
			decompressed, err := io.ReadAll(dec)
			if err != nil {
				t.Errorf("decompress upload body: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if !bytes.Equal(decompressed, dbContent) {
				t.Errorf("decompressed content mismatch: got %d bytes, want %d bytes", len(decompressed), len(dbContent))
			}
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	client, err := NewClientWithURL(goankitypes.SyncConfig{
		Username: "test@example.com",
		Password: "secret",
	}, server.URL+"/sync/")
	if err != nil {
		t.Fatalf("NewClientWithURL: %v", err)
	}

	if err := client.Authenticate(context.Background()); err != nil {
		t.Fatalf("Authenticate: %v", err)
	}

	err = client.FullUpload(context.Background(), sourceDB, mediaDir)
	if err != nil {
		t.Fatalf("FullUpload: %v", err)
	}
}

func TestFullUploadWithEmptyMediaDir(t *testing.T) {
	tmpDir := t.TempDir()
	sourceDB := filepath.Join(tmpDir, "collection.anki2")

	// Create a minimal SQLite-like file for the upload
	if err := os.WriteFile(sourceDB, []byte("SQLite format 3\x00"+string(make([]byte, 100))), 0644); err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/sync/hostKey":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"key":"upload-nomedia-key"}`))
		default:
			// Accept the upload
			_, _ = io.ReadAll(r.Body)
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	client, err := NewClientWithURL(goankitypes.SyncConfig{
		Username: "test@example.com",
		Password: "secret",
	}, server.URL+"/sync/")
	if err != nil {
		t.Fatalf("NewClientWithURL: %v", err)
	}

	if err := client.Authenticate(context.Background()); err != nil {
		t.Fatalf("Authenticate: %v", err)
	}

	err = client.FullUpload(context.Background(), sourceDB, "")
	if err != nil {
		t.Fatalf("FullUpload without media: %v", err)
	}
}

func TestFullUploadServerError(t *testing.T) {
	tmpDir := t.TempDir()
	sourceDB := filepath.Join(tmpDir, "collection.anki2")

	// Create a minimal file
	if err := os.WriteFile(sourceDB, []byte("SQLite format 3\x00"+string(make([]byte, 100))), 0644); err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/sync/hostKey":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"key":"upload-error-key"}`))
		default:
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte("forbidden"))
		}
	}))
	defer server.Close()

	client, err := NewClientWithURL(goankitypes.SyncConfig{
		Username: "test@example.com",
		Password: "secret",
	}, server.URL+"/sync/")
	if err != nil {
		t.Fatalf("NewClientWithURL: %v", err)
	}

	if err := client.Authenticate(context.Background()); err != nil {
		t.Fatalf("Authenticate: %v", err)
	}

	err = client.FullUpload(context.Background(), sourceDB, "")
	if err == nil {
		t.Fatal("expected error for server error response")
	}
}

func TestBuildURL(t *testing.T) {
	client := &Client{baseURL: "https://sync.ankiweb.net/sync/"}

	// Test with path only
	u, err := client.buildURL(hostKeyEndpoint, nil)
	if err != nil {
		t.Fatalf("buildURL: %v", err)
	}
	if u != "https://sync.ankiweb.net/sync/hostKey" {
		t.Errorf("expected https://sync.ankiweb.net/sync/hostKey, got %s", u)
	}

	// Test with query parameters only
	query := url.Values{}
	query.Set("k", "testkey")
	query.Set("meta", "1")
	u, err = client.buildURL("", query)
	if err != nil {
		t.Fatalf("buildURL: %v", err)
	}
	if u != "https://sync.ankiweb.net/sync/?k=testkey&meta=1" {
		t.Errorf("unexpected URL: %s", u)
	}

	// Test with both path and query
	query = url.Values{}
	query.Set("k", "testkey")
	u, err = client.buildURL(hostKeyEndpoint, query)
	if err != nil {
		t.Fatalf("buildURL: %v", err)
	}
	if u != "https://sync.ankiweb.net/sync/hostKey?k=testkey" {
		t.Errorf("unexpected URL: %s", u)
	}
}

func TestCopyFile(t *testing.T) {
	tmpDir := t.TempDir()
	src := filepath.Join(tmpDir, "source.txt")
	dst := filepath.Join(tmpDir, "dest.txt")

	content := []byte("test content for copy")
	if err := os.WriteFile(src, content, 0644); err != nil {
		t.Fatal(err)
	}

	if err := copyFile(dst, src, 0644); err != nil {
		t.Fatalf("copyFile: %v", err)
	}

	result, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read dest: %v", err)
	}
	if !bytes.Equal(result, content) {
		t.Errorf("expected %q, got %q", content, result)
	}
}

func TestCopyFileErrors(t *testing.T) {
	// Test missing source file
	err := copyFile("/nonexistent/dest.txt", "/nonexistent/source.txt", 0644)
	if err == nil {
		t.Error("expected error for missing source file")
	}

	// Test destination directory doesn't exist
	tmpDir := t.TempDir()
	src := filepath.Join(tmpDir, "source.txt")
	if err := os.WriteFile(src, []byte("test"), 0644); err != nil {
		t.Fatal(err)
	}
	err = copyFile("/nonexistent/path/dest.txt", src, 0644)
	if err == nil {
		t.Error("expected error for invalid destination path")
	}
}
