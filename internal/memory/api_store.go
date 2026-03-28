package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// APIStore implements LongTermStore by calling the gateway memory API.
type APIStore struct {
	baseURL  string
	tenantID string
	userID   string
	token    string
	client   *http.Client
}

// NewAPIStore creates a long-term memory client that talks to the gateway.
func NewAPIStore(gatewayURL, tenantID, userID, token string) *APIStore {
	return &APIStore{
		baseURL:  gatewayURL,
		tenantID: tenantID,
		userID:   userID,
		token:    token,
		client:   &http.Client{Timeout: 10 * time.Second},
	}
}

// Store sends a memory to the gateway for embedding + pgvector storage.
func (a *APIStore) Store(ctx context.Context, mem Memory) error {
	payload := map[string]any{
		"content":    mem.Content,
		"type":       mem.Type,
		"importance": mem.Importance,
		"tenant_id":  a.tenantID,
		"user_id":    a.userID,
	}
	body, _ := json.Marshal(payload)

	req, err := http.NewRequestWithContext(ctx, "POST", a.baseURL+"/v1/memory", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if a.token != "" {
		req.Header.Set("Authorization", "Bearer "+a.token)
	}

	resp, err := a.client.Do(req)
	if err != nil {
		return fmt.Errorf("memory store request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("memory store failed (HTTP %d): %s", resp.StatusCode, string(respBody))
	}
	return nil
}

// Search queries the gateway for similar memories.
func (a *APIStore) Search(ctx context.Context, query string, limit int) ([]Memory, error) {
	payload := map[string]any{
		"query":     query,
		"limit":     limit,
		"tenant_id": a.tenantID,
		"user_id":   a.userID,
	}
	body, _ := json.Marshal(payload)

	req, err := http.NewRequestWithContext(ctx, "POST", a.baseURL+"/v1/memory/search", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if a.token != "" {
		req.Header.Set("Authorization", "Bearer "+a.token)
	}

	resp, err := a.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("memory search request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("memory search failed (HTTP %d): %s", resp.StatusCode, string(respBody))
	}

	var results []struct {
		ID         string  `json:"id"`
		Content    string  `json:"content"`
		Type       string  `json:"type"`
		Importance float64 `json:"importance"`
		Similarity float64 `json:"similarity"`
		CreatedAt  string  `json:"created_at"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&results); err != nil {
		return nil, fmt.Errorf("decode search results: %w", err)
	}

	mems := make([]Memory, len(results))
	for i, r := range results {
		t, _ := time.Parse(time.RFC3339, r.CreatedAt)
		mems[i] = Memory{
			ID:         r.ID,
			Content:    r.Content,
			Type:       r.Type,
			Importance: r.Importance,
			CreatedAt:  t,
		}
	}
	return mems, nil
}
