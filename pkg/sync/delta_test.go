package sync

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	goankitypes "github.com/vpul/go-anki/pkg/types"
)

func TestNewDeltaClient(t *testing.T) {
	config := goankitypes.SyncConfig{
		Username: "test@example.com",
		Password: "secret",
	}
	client, err := NewDeltaClient(config)
	if err != nil {
		t.Fatalf("NewDeltaClient failed: %v", err)
	}

	if client.baseURL != DefaultSyncURL {
		t.Errorf("expected baseURL %q, got %q", DefaultSyncURL, client.baseURL)
	}
	if client.sessionKey != "" {
		t.Errorf("expected empty session key, got %q", client.sessionKey)
	}
}

func TestSyncStartSuccess(t *testing.T) {
	var sessionKey string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/sync/hostKey":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"key":"delta-test-key"}`))
			sessionKey = "delta-test-key"
		case "/sync/start":
			if r.URL.Query().Get("k") != sessionKey {
				t.Errorf("expected session key %q, got %q", sessionKey, r.URL.Query().Get("k"))
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":{"scm":1700000000,"usn":42,"hostNum":0}}`))
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	client, err := NewDeltaClientWithURL(goankitypes.SyncConfig{
		Username: "test@example.com",
		Password: "secret",
	}, server.URL+"/sync/")
	if err != nil {
		t.Fatalf("NewDeltaClientWithURL: %v", err)
	}

	if err := client.Authenticate(context.Background()); err != nil {
		t.Fatalf("Authenticate: %v", err)
	}

	state, err := client.SyncStart(context.Background())
	if err != nil {
		t.Fatalf("SyncStart: %v", err)
	}

	if state.SCM != 1700000000 {
		t.Errorf("expected SCM=1700000000, got %d", state.SCM)
	}
	if state.USN != 42 {
		t.Errorf("expected USN=42, got %d", state.USN)
	}
	if state.HostNum != 0 {
		t.Errorf("expected HostNum=0, got %d", state.HostNum)
	}
}

func TestSyncStartError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/sync/hostKey":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"key":"test-key"}`))
		case "/sync/start":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":{},"err":"server error"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	client, err := NewDeltaClientWithURL(goankitypes.SyncConfig{
		Username: "test",
		Password: "test",
	}, server.URL+"/sync/")
	if err != nil {
		t.Fatalf("NewDeltaClientWithURL: %v", err)
	}

	if err := client.Authenticate(context.Background()); err != nil {
		t.Fatalf("Authenticate: %v", err)
	}

	_, err = client.SyncStart(context.Background())
	if err == nil {
		t.Fatal("expected error from SyncStart, got nil")
	}
	if err.Error() != "sync start error: server error" {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestApplyChangesRoundTrip(t *testing.T) {
	var sessionKey string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/sync/hostKey":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"key":"apply-test-key"}`))
			sessionKey = "apply-test-key"
		case "/sync/applyChanges":
			if r.URL.Query().Get("k") != sessionKey {
				t.Errorf("expected session key %q", sessionKey)
			}
			// Verify the request includes our changes
			var reqBody map[string]interface{}
			if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
				t.Errorf("decode req body: %v", err)
			}
			data, ok := reqBody["data"].(map[string]interface{})
			if !ok {
				t.Error("expected data field")
			}
			cards, ok := data["cards"].([]interface{})
			if !ok || len(cards) != 1 {
				t.Errorf("expected 1 card in request, got %v", cards)
			}

			// Return remote changes
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":{"cards":[{"id":3001,"nid":3000,"did":1,"ord":0,"mod":1700000001,"usn":50,"type":0,"queue":0,"due":0,"ivl":0,"factor":0,"reps":0,"lapses":0,"left":0,"odue":0,"odid":0,"flags":0,"data":""}],"notes":[{"id":3000,"guid":"remote-guid","mid":1585323248,"mod":1700000001,"usn":50,"tags":" remote ","flds":"Q\u001fA","sfld":"Q","csum":"abc","flags":0,"data":""}],"usn":50,"more":false}}`))
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	client, err := NewDeltaClientWithURL(goankitypes.SyncConfig{
		Username: "test@example.com",
		Password: "secret",
	}, server.URL+"/sync/")
	if err != nil {
		t.Fatalf("NewDeltaClientWithURL: %v", err)
	}

	if err := client.Authenticate(context.Background()); err != nil {
		t.Fatalf("Authenticate: %v", err)
	}

	// Send local changes and receive remote changes
	localDelta := &goankitypes.SyncDelta{
		Cards: []goankitypes.Card{
			{
				ID: 2001, NID: 2000, DID: 1, ORD: 0, Mod: 1700000000, USN: -1,
				Type: 0, Queue: 0, Due: 0, IVL: 0, Factor: 0,
				Reps: 0, Lapses: 0, Left: 0, ODue: 0, ODID: 0, Flags: 0, Data: "",
			},
		},
		Notes: []goankitypes.Note{
			{
				ID: 2000, GUID: "local-guid", MID: 1585323248, Mod: 1700000000, USN: -1,
				Tags: " local ", Flds: "Q\x1fA", Sfld: "Q", Csum: "def",
				Flags: 0, Data: "",
			},
		},
		USN: -1,
	}

	remoteDelta, err := client.ApplyChanges(context.Background(), localDelta)
	if err != nil {
		t.Fatalf("ApplyChanges: %v", err)
	}

	if remoteDelta == nil {
		t.Fatal("expected non-nil remote delta")
	}
	if len(remoteDelta.Cards) != 1 {
		t.Errorf("expected 1 remote card, got %d", len(remoteDelta.Cards))
	}
	if remoteDelta.Cards[0].ID != 3001 {
		t.Errorf("expected remote card ID 3001, got %d", remoteDelta.Cards[0].ID)
	}
	if len(remoteDelta.Notes) != 1 {
		t.Errorf("expected 1 remote note, got %d", len(remoteDelta.Notes))
	}
	if remoteDelta.USN != 50 {
		t.Errorf("expected USN=50, got %d", remoteDelta.USN)
	}
	if remoteDelta.More {
		t.Error("expected More=false")
	}
}

func TestApplyGravesSuccess(t *testing.T) {
	var sessionKey string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/sync/hostKey":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"key":"grave-test-key"}`))
			sessionKey = "grave-test-key"
		case "/sync/applyGraves":
			if r.URL.Query().Get("k") != sessionKey {
				t.Errorf("expected session key %q", sessionKey)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":{}}`))
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	client, err := NewDeltaClientWithURL(goankitypes.SyncConfig{
		Username: "test@example.com",
		Password: "secret",
	}, server.URL+"/sync/")
	if err != nil {
		t.Fatalf("NewDeltaClientWithURL: %v", err)
	}

	if err := client.Authenticate(context.Background()); err != nil {
		t.Fatalf("Authenticate: %v", err)
	}

	graves := []goankitypes.Grave{
		{OID: 1001, Type: 1, USN: -1},
		{OID: 1002, Type: 2, USN: -1},
	}

	err = client.ApplyGraves(context.Background(), graves)
	if err != nil {
		t.Fatalf("ApplyGraves: %v", err)
	}
}

func TestSyncFinishSuccess(t *testing.T) {
	var sessionKey string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/sync/hostKey":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"key":"finish-test-key"}`))
			sessionKey = "finish-test-key"
		case "/sync/finish":
			if r.URL.Query().Get("k") != sessionKey {
				t.Errorf("expected session key %q", sessionKey)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":{"scm":1700000000,"usn":55,"hostNum":0}}`))
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	client, err := NewDeltaClientWithURL(goankitypes.SyncConfig{
		Username: "test@example.com",
		Password: "secret",
	}, server.URL+"/sync/")
	if err != nil {
		t.Fatalf("NewDeltaClientWithURL: %v", err)
	}

	if err := client.Authenticate(context.Background()); err != nil {
		t.Fatalf("Authenticate: %v", err)
	}

	state, err := client.SyncFinish(context.Background())
	if err != nil {
		t.Fatalf("SyncFinish: %v", err)
	}

	if state.USN != 55 {
		t.Errorf("expected USN=55, got %d", state.USN)
	}
}

func TestSyncStartWithoutAuth(t *testing.T) {
	client, err := NewDeltaClient(goankitypes.SyncConfig{
		Username: "test",
		Password: "test",
	})
	if err != nil {
		t.Fatalf("NewDeltaClient: %v", err)
	}

	_, err = client.SyncStart(context.Background())
	if err == nil {
		t.Fatal("expected error when calling SyncStart without authentication")
	}
}

func TestApplyChangesWithoutAuth(t *testing.T) {
	client, err := NewDeltaClient(goankitypes.SyncConfig{
		Username: "test",
		Password: "test",
	})
	if err != nil {
		t.Fatalf("NewDeltaClient: %v", err)
	}

	_, err = client.ApplyChanges(context.Background(), &goankitypes.SyncDelta{})
	if err == nil {
		t.Fatal("expected error when calling ApplyChanges without authentication")
	}
}

func TestFullSyncWithoutServerError(t *testing.T) {
	// This tests that FullSync correctly fails when it can't connect
	client, err := NewDeltaClient(goankitypes.SyncConfig{
		Username: "test",
		Password: "test",
	})
	if err != nil {
		t.Fatalf("NewDeltaClient: %v", err)
	}

	// Without auth, FullSync will try to authenticate first and fail
	// because no server is running
	err = client.FullSync(context.Background(), "/tmp/nonexistent.anki2")
	if err == nil {
		t.Fatal("expected error from FullSync without server")
	}
	t.Logf("FullSync error (expected): %v", err)
}
