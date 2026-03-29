package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Mock ClaimManager
// ---------------------------------------------------------------------------

type mockClaimManager struct {
	createFn    func(ctx context.Context, name, namespace, tenantID, poolRef string, ttl int) (string, error)
	deleteFn    func(ctx context.Context, name, namespace string) error
	getStatusFn func(ctx context.Context, name, namespace string) (string, string, error)
}

func (m *mockClaimManager) Create(ctx context.Context, name, namespace, tenantID, poolRef string, ttl int) (string, error) {
	if m.createFn != nil {
		return m.createFn(ctx, name, namespace, tenantID, poolRef, ttl)
	}
	return "http://10.0.1.5:8090", nil
}

func (m *mockClaimManager) Delete(ctx context.Context, name, namespace string) error {
	if m.deleteFn != nil {
		return m.deleteFn(ctx, name, namespace)
	}
	return nil
}

func (m *mockClaimManager) GetStatus(ctx context.Context, name, namespace string) (string, string, error) {
	if m.getStatusFn != nil {
		return m.getStatusFn(ctx, name, namespace)
	}
	return "Bound", "http://10.0.1.5:8090", nil
}

// newTestRouter creates a Router with default config and the given mock.
func newTestRouter(mock *mockClaimManager) *Router {
	cfg := routerConfig{
		Addr:      ":8091",
		Namespace: "test-ns",
		PoolRef:   "tool-execution-pool",
		ClaimTTL:  3600,
	}
	return NewRouter(cfg, mock)
}

// ---------------------------------------------------------------------------
// TestCreateSandbox_Success
// ---------------------------------------------------------------------------

func TestCreateSandbox_Success(t *testing.T) {
	var gotName, gotNS, gotTenant, gotPool string
	var gotTTL int

	mock := &mockClaimManager{
		createFn: func(ctx context.Context, name, namespace, tenantID, poolRef string, ttl int) (string, error) {
			gotName = name
			gotNS = namespace
			gotTenant = tenantID
			gotPool = poolRef
			gotTTL = ttl
			return "http://10.0.1.5:8090", nil
		},
	}

	router := newTestRouter(mock)
	srv := httptest.NewServer(router.Handler())
	defer srv.Close()

	body, _ := json.Marshal(createRequest{
		ConversationID: "conv-123",
		TenantID:       "tenant-abc",
		PoolRef:        "custom-pool",
	})

	resp, err := http.Post(srv.URL+"/v1/sandboxes", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, string(b))
	}

	var result createResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if result.SandboxID == "" {
		t.Fatal("sandbox_id is empty")
	}
	if result.Endpoint != "http://10.0.1.5:8090" {
		t.Fatalf("endpoint = %q, want http://10.0.1.5:8090", result.Endpoint)
	}

	// Verify claim manager was called with correct params.
	if gotNS != "test-ns" {
		t.Errorf("namespace = %q, want test-ns", gotNS)
	}
	if gotTenant != "tenant-abc" {
		t.Errorf("tenantID = %q, want tenant-abc", gotTenant)
	}
	if gotPool != "custom-pool" {
		t.Errorf("poolRef = %q, want custom-pool", gotPool)
	}
	if gotTTL != 3600 {
		t.Errorf("ttl = %d, want 3600", gotTTL)
	}
	if gotName == "" {
		t.Error("claim name should not be empty")
	}

	// Session should be cached.
	sessions := router.ActiveSessions()
	if len(sessions) != 1 {
		t.Fatalf("active sessions = %d, want 1", len(sessions))
	}
	if sessions[0].ConversationID != "conv-123" {
		t.Errorf("session conversation_id = %q, want conv-123", sessions[0].ConversationID)
	}
}

// ---------------------------------------------------------------------------
// TestCreateSandbox_ClaimTimeout
// ---------------------------------------------------------------------------

func TestCreateSandbox_ClaimTimeout(t *testing.T) {
	mock := &mockClaimManager{
		createFn: func(ctx context.Context, name, namespace, tenantID, poolRef string, ttl int) (string, error) {
			// Simulate a claim that never binds by waiting for context cancellation.
			<-ctx.Done()
			return "", fmt.Errorf("timed out waiting for claim to bind")
		},
	}

	router := newTestRouter(mock)
	srv := httptest.NewServer(router.Handler())
	defer srv.Close()

	body, _ := json.Marshal(createRequest{
		ConversationID: "conv-timeout",
		TenantID:       "tenant-abc",
	})

	resp, err := http.Post(srv.URL+"/v1/sandboxes", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", resp.StatusCode)
	}

	var result map[string]string
	json.NewDecoder(resp.Body).Decode(&result)
	if result["error"] == "" {
		t.Fatal("expected error message in response")
	}
}

// ---------------------------------------------------------------------------
// TestCreateSandbox_MissingFields
// ---------------------------------------------------------------------------

func TestCreateSandbox_MissingFields(t *testing.T) {
	router := newTestRouter(&mockClaimManager{})
	srv := httptest.NewServer(router.Handler())
	defer srv.Close()

	body, _ := json.Marshal(createRequest{ConversationID: "", TenantID: ""})
	resp, err := http.Post(srv.URL+"/v1/sandboxes", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

// ---------------------------------------------------------------------------
// TestDeleteSandbox_Success
// ---------------------------------------------------------------------------

func TestDeleteSandbox_Success(t *testing.T) {
	var deletedName, deletedNS string
	mock := &mockClaimManager{
		deleteFn: func(ctx context.Context, name, namespace string) error {
			deletedName = name
			deletedNS = namespace
			return nil
		},
	}

	router := newTestRouter(mock)

	// Pre-populate a session.
	router.mu.Lock()
	router.sessions["claim-xyz"] = &SandboxSession{
		SandboxID:      "claim-xyz",
		ConversationID: "conv-1",
		TenantID:       "t-1",
		Endpoint:       "http://10.0.1.5:8090",
		Phase:          "Bound",
		CreatedAt:      time.Now(),
	}
	router.mu.Unlock()

	srv := httptest.NewServer(router.Handler())
	defer srv.Close()

	req, _ := http.NewRequest("DELETE", srv.URL+"/v1/sandboxes/claim-xyz", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, string(b))
	}

	if deletedName != "claim-xyz" {
		t.Errorf("deleted claim name = %q, want claim-xyz", deletedName)
	}
	if deletedNS != "test-ns" {
		t.Errorf("deleted namespace = %q, want test-ns", deletedNS)
	}

	// Session should be removed from cache.
	if len(router.ActiveSessions()) != 0 {
		t.Fatal("session should have been removed from cache")
	}
}

// ---------------------------------------------------------------------------
// TestDeleteSandbox_NotFound
// ---------------------------------------------------------------------------

func TestDeleteSandbox_NotFound(t *testing.T) {
	router := newTestRouter(&mockClaimManager{})
	srv := httptest.NewServer(router.Handler())
	defer srv.Close()

	req, _ := http.NewRequest("DELETE", srv.URL+"/v1/sandboxes/nonexistent", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

// ---------------------------------------------------------------------------
// TestProxyExecute_Success
// ---------------------------------------------------------------------------

func TestProxyExecute_Success(t *testing.T) {
	// Stand up a fake sandbox pod.
	sandboxPod := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("sandbox pod method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/execute" {
			t.Errorf("sandbox pod path = %s, want /execute", r.URL.Path)
		}

		var req struct {
			Language string `json:"language"`
			Code     string `json:"code"`
			Timeout  int    `json:"timeout"`
		}
		json.NewDecoder(r.Body).Decode(&req)

		if req.Language != "python" {
			t.Errorf("language = %q, want python", req.Language)
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"stdout":    "42\n",
			"stderr":    "",
			"exit_code": 0,
		})
	}))
	defer sandboxPod.Close()

	router := newTestRouter(&mockClaimManager{})

	// Pre-populate a session pointing at the fake sandbox pod.
	router.mu.Lock()
	router.sessions["claim-exec"] = &SandboxSession{
		SandboxID: "claim-exec",
		Endpoint:  sandboxPod.URL,
		Phase:     "Bound",
		CreatedAt: time.Now(),
	}
	router.mu.Unlock()

	srv := httptest.NewServer(router.Handler())
	defer srv.Close()

	body, _ := json.Marshal(map[string]any{
		"language": "python",
		"code":     "print(42)",
		"timeout":  10,
	})

	resp, err := http.Post(srv.URL+"/v1/sandboxes/claim-exec/execute", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, string(b))
	}

	var result map[string]any
	json.NewDecoder(resp.Body).Decode(&result)
	if result["stdout"] != "42\n" {
		t.Errorf("stdout = %q, want %q", result["stdout"], "42\n")
	}
}

// ---------------------------------------------------------------------------
// TestProxyExecute_NoSandbox
// ---------------------------------------------------------------------------

func TestProxyExecute_NoSandbox(t *testing.T) {
	router := newTestRouter(&mockClaimManager{})
	srv := httptest.NewServer(router.Handler())
	defer srv.Close()

	body, _ := json.Marshal(map[string]any{
		"language": "python",
		"code":     "print(1)",
		"timeout":  5,
	})

	resp, err := http.Post(srv.URL+"/v1/sandboxes/nonexistent/execute", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

// ---------------------------------------------------------------------------
// TestProxyUpload_Success
// ---------------------------------------------------------------------------

func TestProxyUpload_Success(t *testing.T) {
	sandboxPod := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/upload" {
			t.Errorf("path = %s, want /upload", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"status": "uploaded"})
	}))
	defer sandboxPod.Close()

	router := newTestRouter(&mockClaimManager{})
	router.mu.Lock()
	router.sessions["claim-up"] = &SandboxSession{
		SandboxID: "claim-up",
		Endpoint:  sandboxPod.URL,
		Phase:     "Bound",
		CreatedAt: time.Now(),
	}
	router.mu.Unlock()

	srv := httptest.NewServer(router.Handler())
	defer srv.Close()

	body, _ := json.Marshal(map[string]string{"path": "main.py", "content": "code"})
	resp, err := http.Post(srv.URL+"/v1/sandboxes/claim-up/upload", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
}

// ---------------------------------------------------------------------------
// TestProxyDownload_Success
// ---------------------------------------------------------------------------

func TestProxyDownload_Success(t *testing.T) {
	sandboxPod := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/download" {
			t.Errorf("path = %s, want /download", r.URL.Path)
		}
		if got := r.URL.Query().Get("path"); got != "output.txt" {
			t.Errorf("query path = %q, want output.txt", got)
		}
		w.Write([]byte("file contents"))
	}))
	defer sandboxPod.Close()

	router := newTestRouter(&mockClaimManager{})
	router.mu.Lock()
	router.sessions["claim-dl"] = &SandboxSession{
		SandboxID: "claim-dl",
		Endpoint:  sandboxPod.URL,
		Phase:     "Bound",
		CreatedAt: time.Now(),
	}
	router.mu.Unlock()

	srv := httptest.NewServer(router.Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/v1/sandboxes/claim-dl/download?path=output.txt")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	data, _ := io.ReadAll(resp.Body)
	if string(data) != "file contents" {
		t.Fatalf("body = %q, want %q", string(data), "file contents")
	}
}

// ---------------------------------------------------------------------------
// TestProxyList_Success
// ---------------------------------------------------------------------------

func TestProxyList_Success(t *testing.T) {
	sandboxPod := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/list" {
			t.Errorf("path = %s, want /list", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"files": []map[string]any{
			{"name": "test.py", "size": 100, "is_dir": false},
		}})
	}))
	defer sandboxPod.Close()

	router := newTestRouter(&mockClaimManager{})
	router.mu.Lock()
	router.sessions["claim-ls"] = &SandboxSession{
		SandboxID: "claim-ls",
		Endpoint:  sandboxPod.URL,
		Phase:     "Bound",
		CreatedAt: time.Now(),
	}
	router.mu.Unlock()

	srv := httptest.NewServer(router.Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/v1/sandboxes/claim-ls/list?path=.")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
}

// ---------------------------------------------------------------------------
// TestGetSandbox_Success
// ---------------------------------------------------------------------------

func TestGetSandbox_Success(t *testing.T) {
	router := newTestRouter(&mockClaimManager{})
	router.mu.Lock()
	router.sessions["claim-get"] = &SandboxSession{
		SandboxID:      "claim-get",
		ConversationID: "conv-get",
		TenantID:       "t-get",
		Endpoint:       "http://10.0.1.5:8090",
		Phase:          "Bound",
		CreatedAt:      time.Now(),
	}
	router.mu.Unlock()

	srv := httptest.NewServer(router.Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/v1/sandboxes/claim-get")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var session SandboxSession
	json.NewDecoder(resp.Body).Decode(&session)
	if session.SandboxID != "claim-get" {
		t.Errorf("sandbox_id = %q, want claim-get", session.SandboxID)
	}
	if session.ConversationID != "conv-get" {
		t.Errorf("conversation_id = %q, want conv-get", session.ConversationID)
	}
}

// ---------------------------------------------------------------------------
// TestGetSandbox_NotFound
// ---------------------------------------------------------------------------

func TestGetSandbox_NotFound(t *testing.T) {
	router := newTestRouter(&mockClaimManager{})
	srv := httptest.NewServer(router.Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/v1/sandboxes/nonexistent")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

// ---------------------------------------------------------------------------
// TestHealthz
// ---------------------------------------------------------------------------

func TestHealthz(t *testing.T) {
	router := newTestRouter(&mockClaimManager{})
	srv := httptest.NewServer(router.Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	var result map[string]string
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if result["status"] != "ok" {
		t.Errorf("status = %q, want ok", result["status"])
	}
}
