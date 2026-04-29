package sync

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/vpul/go-anki/pkg/collection"
	goankitypes "github.com/vpul/go-anki/pkg/types"
)

const (
	// maxDeltaPayloadSize is the maximum delta payload size (50MB).
	maxDeltaPayloadSize int64 = 50 * 1024 * 1024

	// syncStartEndpoint is the delta sync session start endpoint.
	syncStartEndpoint = "start"

	// syncApplyChangesEndpoint is the delta sync apply-changes endpoint.
	syncApplyChangesEndpoint = "applyChanges"

	// syncApplyGravesEndpoint is the delta sync apply-graves endpoint.
	syncApplyGravesEndpoint = "applyGraves"

	// syncFinishEndpoint is the delta sync session finish endpoint.
	syncFinishEndpoint = "finish"
)

// DeltaSyncClient extends Client with incremental delta sync operations.
type DeltaSyncClient struct {
	*Client
}

// NewDeltaClient creates a new delta sync client with the given configuration.
func NewDeltaClient(config goankitypes.SyncConfig) (*DeltaSyncClient, error) {
	client, err := NewClient(config)
	if err != nil {
		return nil, err
	}
	return &DeltaSyncClient{Client: client}, nil
}

// NewDeltaClientWithURL creates a new delta sync client with a custom base URL.
func NewDeltaClientWithURL(config goankitypes.SyncConfig, baseURL string) (*DeltaSyncClient, error) {
	client, err := NewClientWithURL(config, baseURL)
	if err != nil {
		return nil, err
	}
	return &DeltaSyncClient{Client: client}, nil
}

// deltaRequest is the generic request envelope for delta sync endpoints.
type deltaRequest struct {
	Key  string      `json:"k"`
	Ver  int         `json:"v"`
	Data interface{} `json:"data,omitempty"`
}

// deltaResponse is the generic response envelope for delta sync endpoints.
type deltaResponse struct {
	Data json.RawMessage `json:"data"`
	Err  string          `json:"err,omitempty"`
}

// SyncStart begins a delta sync session and returns the server's sync state.
// POST /sync/start -> {data: {scm, usn, hostNum}}
func (d *DeltaSyncClient) SyncStart(ctx context.Context) (*goankitypes.SyncState, error) {
	if d.sessionKey == "" {
		return nil, fmt.Errorf("not authenticated; call Authenticate() first")
	}

	query := url.Values{}
	query.Set("k", d.sessionKey)
	syncURL, err := d.buildURL(syncStartEndpoint, query)
	if err != nil {
		return nil, fmt.Errorf("build sync start URL: %w", err)
	}

	reqBody := deltaRequest{
		Key: d.sessionKey,
		Ver: SyncProtocolVersion,
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal sync start request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, syncURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create sync start request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := d.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("sync start request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
		return nil, fmt.Errorf("sync start failed (status %d): %s", resp.StatusCode, string(respBody))
	}

	// Use LimitReader for metadata response (1MB max)
	limited := io.LimitReader(resp.Body, 1<<20)
	var dr deltaResponse
	if err := json.NewDecoder(limited).Decode(&dr); err != nil {
		return nil, fmt.Errorf("decode sync start response: %w", err)
	}

	if dr.Err != "" {
		return nil, fmt.Errorf("sync start error: %s", dr.Err)
	}

	var state goankitypes.SyncState
	if err := json.Unmarshal(dr.Data, &state); err != nil {
		return nil, fmt.Errorf("unmarshal sync state: %w", err)
	}

	return &state, nil
}

// ApplyChanges sends local changes to the server and receives remote changes.
// POST /sync/applyChanges -> {data: {cards: [...], notes: [...], decks: [...], graves: [...], usn: N, more: false}}
func (d *DeltaSyncClient) ApplyChanges(ctx context.Context, localDelta *goankitypes.SyncDelta) (*goankitypes.SyncDelta, error) {
	if d.sessionKey == "" {
		return nil, fmt.Errorf("not authenticated; call Authenticate() first")
	}

	query := url.Values{}
	query.Set("k", d.sessionKey)
	syncURL, err := d.buildURL(syncApplyChangesEndpoint, query)
	if err != nil {
		return nil, fmt.Errorf("build apply changes URL: %w", err)
	}

	reqBody := deltaRequest{
		Key:  d.sessionKey,
		Ver:  SyncProtocolVersion,
		Data: localDelta,
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal apply changes request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, syncURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create apply changes request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := d.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("apply changes request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
		return nil, fmt.Errorf("apply changes failed (status %d): %s", resp.StatusCode, string(respBody))
	}

	// Use LimitReader for delta payload (50MB max)
	limited := io.LimitReader(resp.Body, maxDeltaPayloadSize+1)
	var dr deltaResponse
	if err := json.NewDecoder(limited).Decode(&dr); err != nil {
		return nil, fmt.Errorf("decode apply changes response: %w", err)
	}

	if dr.Err != "" {
		return nil, fmt.Errorf("apply changes error: %s", dr.Err)
	}

	var remoteDelta goankitypes.SyncDelta
	if err := json.Unmarshal(dr.Data, &remoteDelta); err != nil {
		return nil, fmt.Errorf("unmarshal remote delta: %w", err)
	}

	return &remoteDelta, nil
}

// ApplyGraves sends deletion records to the server.
// POST /sync/applyGraves -> {data: {usn: N}}
func (d *DeltaSyncClient) ApplyGraves(ctx context.Context, graves []goankitypes.Grave) error {
	if d.sessionKey == "" {
		return fmt.Errorf("not authenticated; call Authenticate() first")
	}

	query := url.Values{}
	query.Set("k", d.sessionKey)
	syncURL, err := d.buildURL(syncApplyGravesEndpoint, query)
	if err != nil {
		return fmt.Errorf("build apply graves URL: %w", err)
	}

	reqBody := deltaRequest{
		Key:  d.sessionKey,
		Ver:  SyncProtocolVersion,
		Data: map[string]interface{}{"graves": graves},
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("marshal apply graves request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, syncURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create apply graves request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := d.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("apply graves request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
		return fmt.Errorf("apply graves failed (status %d): %s", resp.StatusCode, string(respBody))
	}

	limited := io.LimitReader(resp.Body, 1<<20)
	var dr deltaResponse
	if err := json.NewDecoder(limited).Decode(&dr); err != nil {
		return fmt.Errorf("decode apply graves response: %w", err)
	}

	if dr.Err != "" {
		return fmt.Errorf("apply graves error: %s", dr.Err)
	}

	return nil
}

// SyncFinish ends a delta sync session and returns the server's sync state.
// POST /sync/finish -> {data: {scm, usn, hostNum}}
func (d *DeltaSyncClient) SyncFinish(ctx context.Context) (*goankitypes.SyncState, error) {
	if d.sessionKey == "" {
		return nil, fmt.Errorf("not authenticated; call Authenticate() first")
	}

	query := url.Values{}
	query.Set("k", d.sessionKey)
	syncURL, err := d.buildURL(syncFinishEndpoint, query)
	if err != nil {
		return nil, fmt.Errorf("build sync finish URL: %w", err)
	}

	reqBody := deltaRequest{
		Key: d.sessionKey,
		Ver: SyncProtocolVersion,
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal sync finish request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, syncURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create sync finish request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := d.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("sync finish request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
		return nil, fmt.Errorf("sync finish failed (status %d): %s", resp.StatusCode, string(respBody))
	}

	limited := io.LimitReader(resp.Body, 1<<20)
	var dr deltaResponse
	if err := json.NewDecoder(limited).Decode(&dr); err != nil {
		return nil, fmt.Errorf("decode sync finish response: %w", err)
	}

	if dr.Err != "" {
		return nil, fmt.Errorf("sync finish error: %s", dr.Err)
	}

	var state goankitypes.SyncState
	if err := json.Unmarshal(dr.Data, &state); err != nil {
		return nil, fmt.Errorf("unmarshal finish state: %w", err)
	}

	return &state, nil
}

// FullSync orchestrates a complete delta sync cycle:
// 1. Authenticate
// 2. SyncStart
// 3. GetChanges from local collection
// 4. ApplyChanges (send local, receive remote)
// 5. ApplyGraves (send deletions)
// 6. Apply remote changes to local collection
// 7. MarkSynced with new USN
// 8. SyncFinish
func (d *DeltaSyncClient) FullSync(ctx context.Context, dbPath string) error {
	// Step 1: Authenticate
	if d.sessionKey == "" {
		if err := d.Authenticate(ctx); err != nil {
			return fmt.Errorf("authenticate: %w", err)
		}
	}

	// Step 2: SyncStart
	_, err := d.SyncStart(ctx)
	if err != nil {
		return fmt.Errorf("sync start: %w", err)
	}

	// Open the local collection
	col, err := collection.Open(dbPath, collection.ReadWrite)
	if err != nil {
		return fmt.Errorf("open collection: %w", err)
	}
	defer func() { _ = col.Close() }()

	// Step 3: Get local state and changes
	syncState, err := col.GetSyncState(ctx)
	if err != nil {
		return fmt.Errorf("get local sync state: %w", err)
	}

	localDelta, err := col.GetChanges(ctx, syncState.USN)
	if err != nil {
		return fmt.Errorf("get local changes: %w", err)
	}

	// Step 4: ApplyChanges (send local, receive remote)
	remoteDelta, err := d.ApplyChanges(ctx, localDelta)
	if err != nil {
		return fmt.Errorf("apply changes: %w", err)
	}

	// Step 5: ApplyGraves (send deletions)
	if len(localDelta.Graves) > 0 {
		if err := d.ApplyGraves(ctx, localDelta.Graves); err != nil {
			return fmt.Errorf("apply graves: %w", err)
		}
	}

	// Step 6: Apply remote changes to local collection
	if remoteDelta != nil && (len(remoteDelta.Cards) > 0 || len(remoteDelta.Notes) > 0 ||
		len(remoteDelta.Decks) > 0 || len(remoteDelta.Graves) > 0) {
		if err := col.ApplyChanges(ctx, remoteDelta); err != nil {
			return fmt.Errorf("apply remote changes: %w", err)
		}
	}

	// Step 7: MarkSynced with new USN
	newUSN := localDelta.USN
	if remoteDelta != nil && remoteDelta.USN > newUSN {
		newUSN = remoteDelta.USN
	}
	if newUSN > 0 {
		if err := col.MarkSynced(ctx, newUSN); err != nil {
			return fmt.Errorf("mark synced: %w", err)
		}
	}

	// Step 8: SyncFinish
	_, err = d.SyncFinish(ctx)
	if err != nil {
		return fmt.Errorf("sync finish: %w", err)
	}

	return nil
}
