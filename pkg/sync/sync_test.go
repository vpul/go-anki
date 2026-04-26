package sync

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	goankitypes "github.com/vpul/go-anki/pkg/types"
)

func TestNewClient(t *testing.T) {
	config := goankitypes.SyncConfig{
		Username: "test@example.com",
		Password: "secret",
	}
	client := NewClient(config)

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
	client := NewClientWithURL(config, customURL)

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

	client := NewClientWithURL(goankitypes.SyncConfig{
		Username: "test@example.com",
		Password: "secret",
	}, server.URL+"/sync/")

	err := client.Authenticate(context.Background())
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

	client := NewClientWithURL(goankitypes.SyncConfig{
		Username: "bad@example.com",
		Password: "wrong",
	}, server.URL+"/sync/")

	err := client.Authenticate(context.Background())
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

	client := NewClientWithURL(goankitypes.SyncConfig{
		Username: "test@example.com",
		Password: "secret",
	}, server.URL+"/sync/")

	err := client.Authenticate(context.Background())
	if err == nil {
		t.Fatal("expected error for empty session key")
	}
}

func TestMetaWithoutAuth(t *testing.T) {
	client := NewClient(goankitypes.SyncConfig{
		Username: "test@example.com",
		Password: "secret",
	})
	_, err := client.Meta(context.Background())
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

	client := NewClientWithURL(goankitypes.SyncConfig{
		Username: "test@example.com",
		Password: "secret",
	}, server.URL+"/sync/")

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
	client := NewClient(goankitypes.SyncConfig{
		Username: "test@example.com",
		Password: "secret",
	})
	_, err := client.FullDownload(context.Background(), "/tmp/test.anki2", "/tmp/media")
	if err == nil {
		t.Fatal("expected error when calling FullDownload without authentication")
	}
}

func TestUploadWithoutAuth(t *testing.T) {
	client := NewClient(goankitypes.SyncConfig{
		Username: "test@example.com",
		Password: "secret",
	})
	err := client.FullUpload(context.Background(), "/tmp/test.anki2", "/tmp/media")
	if err == nil {
		t.Fatal("expected error when calling FullUpload without authentication")
	}
}

func TestSetHTTPClient(t *testing.T) {
	client := NewClient(goankitypes.SyncConfig{
		Username: "test",
		Password: "test",
	})
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

	client := NewClientWithURL(goankitypes.SyncConfig{
		Username: "user",
		Password: "pass",
	}, server.URL+"/sync/")

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

	client := NewClientWithURL(goankitypes.SyncConfig{
		Username: "user",
		Password: "pass",
	}, server.URL+"/sync/")

	if err := client.Authenticate(context.Background()); err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
}
