//go:build integration

package sync

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	goankitypes "github.com/vpul/go-anki/pkg/types"
)

// Integration tests require real AnkiWeb credentials.
// Set ANKI_USERNAME and ANKI_PASSWORD environment variables to run.
//
// Example:
//   ANKI_USERNAME=user@example.com ANKI_PASSWORD=secret go test -tags=integration ./pkg/sync/

func getCredentials(t *testing.T) (string, string) {
	t.Helper()
	username := os.Getenv("ANKI_USERNAME")
	password := os.Getenv("ANKI_PASSWORD")
	if username == "" || password == "" {
		t.Skip("ANKI_USERNAME and ANKI_PASSWORD environment variables required")
	}
	return username, password
}

func TestIntegrationAuthenticate(t *testing.T) {
	username, password := getCredentials(t)

	client, err := NewClient(goankitypes.SyncConfig{
		Username: username,
		Password: password,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	ctx := context.Background()
	if err := client.Authenticate(ctx); err != nil {
		t.Fatalf("Authenticate failed: %v", err)
	}
	if client.SessionKey() == "" {
		t.Fatal("expected non-empty session key after authentication")
	}
	t.Logf("Session key obtained: %s...%s", client.SessionKey()[:4], client.SessionKey()[len(client.SessionKey())-4:])
}

func TestIntegrationAuthenticateBadCredentials(t *testing.T) {
	client, err := NewClient(goankitypes.SyncConfig{
		Username: "definitely-not-a-real-user@example.com",
		Password: "wrong-password-12345",
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	ctx := context.Background()
	err := client.Authenticate(ctx)
	if err == nil {
		t.Fatal("expected authentication to fail with bad credentials")
	}
	t.Logf("Bad credentials correctly rejected: %v", err)
}

func TestIntegrationMeta(t *testing.T) {
	username, password := getCredentials(t)

	client, err := NewClient(goankitypes.SyncConfig{
		Username: username,
		Password: password,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	ctx := context.Background()
	if err := client.Authenticate(ctx); err != nil {
		t.Fatalf("Authenticate: %v", err)
	}

	meta, err := client.Meta(ctx)
	if err != nil {
		t.Fatalf("Meta: %v", err)
	}

	t.Logf("Server metadata: mod=%d usn=%d musn=%d", meta.Modified, meta.USN, meta.MediaUSN)

	if meta.Modified == 0 {
		t.Log("Warning: server modification timestamp is 0")
	}
}

func TestIntegrationFullDownload(t *testing.T) {
	username, password := getCredentials(t)

	client, err := NewClient(goankitypes.SyncConfig{
		Username: username,
		Password: password,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	ctx := context.Background()
	if err := client.Authenticate(ctx); err != nil {
		t.Fatalf("Authenticate: %v", err)
	}

	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "collection.anki2")
	mediaDir := filepath.Join(tmpDir, "collection.media")

	result, err := client.FullDownload(ctx, dbPath, mediaDir)
	if err != nil {
		t.Fatalf("FullDownload: %v", err)
	}

	t.Logf("Download complete: mod=%d usn=%d media=%d",
		result.ModifiedTimestamp, result.UpdateSequenceNumber, result.MediaFilesImported)

	// Verify the database file exists
	if _, err := os.Stat(result.DBPath); os.IsNotExist(err) {
		t.Fatal("downloaded database file does not exist")
	}

	// Verify the media directory exists
	if _, err := os.Stat(result.MediaDir); os.IsNotExist(err) {
		t.Fatal("media directory does not exist")
	}
}

func TestIntegrationMetaWithoutAuth(t *testing.T) {
	client, err := NewClient(goankitypes.SyncConfig{
		Username: "test@example.com",
		Password: "test",
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	_, err = client.Meta(context.Background())
	if err == nil {
		t.Fatal("expected error when calling Meta without authentication")
	}
}
