package builtin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"time"

	volundv1 "github.com/ai-volund/volund-proto/gen/go/volund/v1"
)

// SandboxExecutor runs code in an isolated container pod via a sandbox API.
// The sandbox pod runs with the gVisor runtime class for kernel-level isolation.
//
// Architecture: the agent calls a sandbox service endpoint which manages
// ephemeral pods. The sandbox service runs in the cluster and exposes:
//
//	POST /execute  {language, code, timeout} → {stdout, stderr, exit_code}
//
// Two modes:
//   - Direct mode (v1): talks directly to a sandbox service endpoint.
//   - Router mode (v2): talks to the Sandbox Router which manages claim lifecycle.
type SandboxExecutor struct {
	// endpoint is the sandbox service URL for direct mode (e.g., "http://sandbox-service:8090").
	endpoint string
	client   *http.Client

	// Router mode (v2) fields — set when routerURL is non-empty.
	routerURL   string
	tenantID    string
	poolRef     string
	mu          sync.RWMutex
	routerCache map[string]*routerSession // conversation_id → session
}

// routerSession tracks a sandbox claimed through the router.
type routerSession struct {
	sandboxID string
	endpoint  string
}

// IsRouterMode returns true if the executor is configured for router mode.
func (s *SandboxExecutor) IsRouterMode() bool {
	return s.routerURL != ""
}

// SandboxConfig holds configuration for the sandbox executor.
type SandboxConfig struct {
	// Endpoint is the sandbox service URL (v1 direct mode). If empty, falls back to subprocess.
	Endpoint string
	// RouterURL is the sandbox router URL (v2 router mode). Takes precedence over Endpoint.
	RouterURL string
	// TenantID identifies the tenant for claim creation (router mode).
	TenantID string
	// PoolRef is the SandboxWarmPool name (router mode, default "tool-execution-pool").
	PoolRef string
}

// NewSandboxExecutor creates a sandbox executor.
// When RouterURL is set, uses the sandbox router for claim-based lifecycle.
// When only Endpoint is set, talks directly to a sandbox pod.
// Returns nil if neither is configured.
func NewSandboxExecutor(cfg SandboxConfig) *SandboxExecutor {
	if cfg.RouterURL != "" {
		// Router mode (v2) — claim lifecycle managed by the router.
		return &SandboxExecutor{
			endpoint:    "", // resolved per-conversation via router
			client:      &http.Client{Timeout: 70 * time.Second},
			routerURL:   cfg.RouterURL,
			tenantID:    cfg.TenantID,
			poolRef:     cfg.PoolRef,
			routerCache: make(map[string]*routerSession),
		}
	}
	if cfg.Endpoint == "" {
		return nil
	}
	return &SandboxExecutor{
		endpoint: cfg.Endpoint,
		client:   &http.Client{Timeout: 70 * time.Second}, // slightly longer than max code timeout
	}
}

type sandboxRequest struct {
	Language string `json:"language"`
	Code     string `json:"code"`
	Timeout  int    `json:"timeout"`
}

type sandboxResponse struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exit_code"`
	Error    string `json:"error,omitempty"`
}

// Execute sends code to the sandbox service for isolated execution.
func (s *SandboxExecutor) Execute(ctx context.Context, language, code string, timeout time.Duration) (string, error) {
	reqBody, _ := json.Marshal(sandboxRequest{
		Language: language,
		Code:     code,
		Timeout:  int(timeout.Seconds()),
	})

	req, err := http.NewRequestWithContext(ctx, "POST", s.endpoint+"/execute", bytes.NewReader(reqBody))
	if err != nil {
		return "", fmt.Errorf("create sandbox request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("sandbox request failed: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("sandbox error (HTTP %d): %s", resp.StatusCode, string(body))
	}

	var result sandboxResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("parse sandbox response: %w", err)
	}

	if result.Error != "" {
		return "", fmt.Errorf("sandbox: %s", result.Error)
	}

	output := result.Stdout
	if result.Stderr != "" {
		if output != "" {
			output += "\n"
		}
		output += "stderr:\n" + result.Stderr
	}

	if result.ExitCode != 0 {
		return output, fmt.Errorf("execution failed (exit code %d)", result.ExitCode)
	}

	return output, nil
}

// RunCodeSandboxed is a version of RunCode that uses the sandbox executor when
// available, falling back to subprocess execution.
type RunCodeSandboxed struct {
	sandbox *SandboxExecutor
}

// NewRunCodeSandboxed creates a sandboxed code executor.
// If sandbox is nil, falls back to subprocess execution (same as RunCode).
func NewRunCodeSandboxed(sandbox *SandboxExecutor) *RunCodeSandboxed {
	return &RunCodeSandboxed{sandbox: sandbox}
}

func (r *RunCodeSandboxed) Name() string { return "run_code" }

func (r *RunCodeSandboxed) Definition() *volundv1.ToolDefinition {
	return (RunCode{}).Definition()
}

func (r *RunCodeSandboxed) Execute(ctx context.Context, inputJSON string) (string, error) {
	var input runCodeInput
	if err := json.Unmarshal([]byte(inputJSON), &input); err != nil {
		return "", fmt.Errorf("invalid run_code input: %w", err)
	}

	timeout := defaultCodeTimeout
	if input.TimeoutSeconds > 0 {
		timeout = time.Duration(min(input.TimeoutSeconds, 60)) * time.Second
	}

	// Use sandbox if available.
	if r.sandbox != nil {
		slog.Debug("executing code in sandbox", "language", input.Language, "timeout", timeout)
		return r.sandbox.Execute(ctx, input.Language, input.Code, timeout)
	}

	// Fall back to subprocess execution.
	slog.Debug("executing code as subprocess (no sandbox configured)", "language", input.Language)
	return (RunCode{}).Execute(ctx, inputJSON)
}

// Upload sends a file to the sandbox workspace.
func (s *SandboxExecutor) Upload(ctx context.Context, path, content string) error {
	body, _ := json.Marshal(map[string]string{
		"path":    path,
		"content": content,
	})
	req, err := http.NewRequestWithContext(ctx, "POST", s.endpoint+"/upload", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create upload request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("upload request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("upload error (HTTP %d): %s", resp.StatusCode, string(respBody))
	}
	return nil
}

// Download retrieves a file from the sandbox workspace.
func (s *SandboxExecutor) Download(ctx context.Context, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", s.endpoint+"/download?path="+path, nil)
	if err != nil {
		return nil, fmt.Errorf("create download request: %w", err)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("download error (HTTP %d): %s", resp.StatusCode, string(respBody))
	}
	return io.ReadAll(resp.Body)
}

// FileInfo represents a file entry from the sandbox workspace listing.
type FileInfo struct {
	Name    string `json:"name"`
	Size    int64  `json:"size"`
	IsDir   bool   `json:"is_dir"`
	ModTime string `json:"mod_time"`
}

// ListFiles lists files in the sandbox workspace directory.
func (s *SandboxExecutor) ListFiles(ctx context.Context, path string) ([]FileInfo, error) {
	url := s.endpoint + "/list"
	if path != "" {
		url += "?path=" + path
	}
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("create list request: %w", err)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("list request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("list error (HTTP %d): %s", resp.StatusCode, string(respBody))
	}

	var result struct {
		Files []FileInfo `json:"files"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("parse list response: %w", err)
	}
	return result.Files, nil
}

// Exists checks if a file exists in the sandbox workspace.
func (s *SandboxExecutor) Exists(ctx context.Context, path string) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", s.endpoint+"/exists?path="+path, nil)
	if err != nil {
		return false, fmt.Errorf("create exists request: %w", err)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return false, fmt.Errorf("exists request failed: %w", err)
	}
	defer resp.Body.Close()

	var result struct {
		Exists bool `json:"exists"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return false, fmt.Errorf("parse exists response: %w", err)
	}
	return result.Exists, nil
}

// SandboxServiceEndpoint returns the sandbox endpoint from environment.
func SandboxServiceEndpoint() string {
	return os.Getenv("VOLUND_SANDBOX_ENDPOINT")
}

// ---------------------------------------------------------------------------
// Router client (v2) — claim lifecycle through the Sandbox Router
// ---------------------------------------------------------------------------

// claimSandbox claims a sandbox through the router for the given conversation.
// If a sandbox is already cached for this conversation, it returns the cached endpoint.
func (s *SandboxExecutor) claimSandbox(ctx context.Context, conversationID string) (*routerSession, error) {
	s.mu.RLock()
	if sess, ok := s.routerCache[conversationID]; ok {
		s.mu.RUnlock()
		return sess, nil
	}
	s.mu.RUnlock()

	// Claim a new sandbox via the router.
	reqBody, _ := json.Marshal(map[string]string{
		"conversation_id": conversationID,
		"tenant_id":       s.tenantID,
		"pool_ref":        s.poolRef,
	})

	req, err := http.NewRequestWithContext(ctx, "POST", s.routerURL+"/v1/sandboxes", bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("create claim request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("router claim request failed: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("router claim failed (HTTP %d): %s", resp.StatusCode, string(body))
	}

	var result struct {
		SandboxID string `json:"sandbox_id"`
		Endpoint  string `json:"endpoint"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("parse claim response: %w", err)
	}

	sess := &routerSession{
		sandboxID: result.SandboxID,
		endpoint:  result.Endpoint,
	}

	s.mu.Lock()
	s.routerCache[conversationID] = sess
	s.mu.Unlock()

	slog.Info("sandbox claimed via router",
		"sandbox_id", result.SandboxID,
		"endpoint", result.Endpoint,
		"conversation_id", conversationID,
	)

	return sess, nil
}

// ExecuteViaRouter sends code to the sandbox through the router's proxy endpoint.
func (s *SandboxExecutor) ExecuteViaRouter(ctx context.Context, conversationID, language, code string, timeout time.Duration) (string, error) {
	sess, err := s.claimSandbox(ctx, conversationID)
	if err != nil {
		return "", fmt.Errorf("claim sandbox: %w", err)
	}

	reqBody, _ := json.Marshal(sandboxRequest{
		Language: language,
		Code:     code,
		Timeout:  int(timeout.Seconds()),
	})

	url := s.routerURL + "/v1/sandboxes/" + sess.sandboxID + "/execute"
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(reqBody))
	if err != nil {
		return "", fmt.Errorf("create router execute request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("router execute request failed: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("router execute error (HTTP %d): %s", resp.StatusCode, string(body))
	}

	var result sandboxResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("parse sandbox response: %w", err)
	}

	if result.Error != "" {
		return "", fmt.Errorf("sandbox: %s", result.Error)
	}

	output := result.Stdout
	if result.Stderr != "" {
		if output != "" {
			output += "\n"
		}
		output += "stderr:\n" + result.Stderr
	}

	if result.ExitCode != 0 {
		return output, fmt.Errorf("execution failed (exit code %d)", result.ExitCode)
	}

	return output, nil
}

// UploadViaRouter sends a file to the sandbox through the router's proxy endpoint.
func (s *SandboxExecutor) UploadViaRouter(ctx context.Context, conversationID, path, content string) error {
	sess, err := s.claimSandbox(ctx, conversationID)
	if err != nil {
		return fmt.Errorf("claim sandbox: %w", err)
	}

	body, _ := json.Marshal(map[string]string{
		"path":    path,
		"content": content,
	})

	url := s.routerURL + "/v1/sandboxes/" + sess.sandboxID + "/upload"
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create router upload request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("router upload request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("router upload error (HTTP %d): %s", resp.StatusCode, string(respBody))
	}
	return nil
}

// DownloadViaRouter retrieves a file from the sandbox through the router.
func (s *SandboxExecutor) DownloadViaRouter(ctx context.Context, conversationID, path string) ([]byte, error) {
	sess, err := s.claimSandbox(ctx, conversationID)
	if err != nil {
		return nil, fmt.Errorf("claim sandbox: %w", err)
	}

	url := s.routerURL + "/v1/sandboxes/" + sess.sandboxID + "/download?path=" + path
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("create router download request: %w", err)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("router download request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("router download error (HTTP %d): %s", resp.StatusCode, string(respBody))
	}
	return io.ReadAll(resp.Body)
}

// ListFilesViaRouter lists files through the router.
func (s *SandboxExecutor) ListFilesViaRouter(ctx context.Context, conversationID, path string) ([]FileInfo, error) {
	sess, err := s.claimSandbox(ctx, conversationID)
	if err != nil {
		return nil, fmt.Errorf("claim sandbox: %w", err)
	}

	url := s.routerURL + "/v1/sandboxes/" + sess.sandboxID + "/list"
	if path != "" {
		url += "?path=" + path
	}
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("create router list request: %w", err)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("router list request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("router list error (HTTP %d): %s", resp.StatusCode, string(respBody))
	}

	var result struct {
		Files []FileInfo `json:"files"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("parse list response: %w", err)
	}
	return result.Files, nil
}

// Release releases a sandbox back to the pool via the router.
func (s *SandboxExecutor) Release(ctx context.Context, conversationID string) error {
	s.mu.Lock()
	sess, ok := s.routerCache[conversationID]
	if ok {
		delete(s.routerCache, conversationID)
	}
	s.mu.Unlock()

	if !ok {
		return nil // nothing to release
	}

	url := s.routerURL + "/v1/sandboxes/" + sess.sandboxID
	req, err := http.NewRequestWithContext(ctx, "DELETE", url, nil)
	if err != nil {
		return fmt.Errorf("create release request: %w", err)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("release request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("release error (HTTP %d): %s", resp.StatusCode, string(body))
	}

	slog.Info("sandbox released via router", "sandbox_id", sess.sandboxID, "conversation_id", conversationID)
	return nil
}

// CachedSession returns the cached router session for a conversation, if any.
// Exported for testing.
func (s *SandboxExecutor) CachedSession(conversationID string) (sandboxID, endpoint string, ok bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sess, ok := s.routerCache[conversationID]
	if !ok {
		return "", "", false
	}
	return sess.sandboxID, sess.endpoint, true
}
