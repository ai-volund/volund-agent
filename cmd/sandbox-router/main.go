// Command sandbox-router is an HTTP service deployed per-cluster that manages
// sandbox claim lifecycle via the Kubernetes API and proxies execution requests
// to the correct sandbox pod endpoint.
//
// API:
//
//	POST   /v1/sandboxes                       — create a SandboxClaim, wait for binding
//	GET    /v1/sandboxes/{sandbox_id}           — return claim status + endpoint
//	DELETE /v1/sandboxes/{sandbox_id}           — delete the SandboxClaim CR
//	POST   /v1/sandboxes/{sandbox_id}/execute   — proxy to sandbox pod /execute
//	POST   /v1/sandboxes/{sandbox_id}/upload    — proxy to sandbox pod /upload
//	GET    /v1/sandboxes/{sandbox_id}/download  — proxy to sandbox pod /download
//	GET    /v1/sandboxes/{sandbox_id}/list      — proxy to sandbox pod /list
//	GET    /healthz                             — health check
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"
)

// ---------------------------------------------------------------------------
// Configuration
// ---------------------------------------------------------------------------

type routerConfig struct {
	Addr      string // listen address
	Namespace string // Kubernetes namespace for SandboxClaim CRs
	PoolRef   string // default SandboxWarmPool reference
	ClaimTTL  int    // TTL in seconds for claims
}

func loadConfig() routerConfig {
	return routerConfig{
		Addr:      envOrDefault("SANDBOX_ROUTER_ADDR", ":8091"),
		Namespace: envOrDefault("SANDBOX_NAMESPACE", inferNamespace()),
		PoolRef:   envOrDefault("SANDBOX_POOL_REF", "tool-execution-pool"),
		ClaimTTL:  envOrDefaultInt("SANDBOX_CLAIM_TTL", 3600),
	}
}

func inferNamespace() string {
	// Downward API writes the namespace to this well-known path.
	if data, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/namespace"); err == nil {
		return strings.TrimSpace(string(data))
	}
	return "default"
}

// ---------------------------------------------------------------------------
// SandboxSession — in-memory cache entry
// ---------------------------------------------------------------------------

// SandboxSession tracks a claimed sandbox pod.
type SandboxSession struct {
	SandboxID      string    `json:"sandbox_id"`
	ConversationID string    `json:"conversation_id"`
	TenantID       string    `json:"tenant_id"`
	Endpoint       string    `json:"endpoint"`
	Phase          string    `json:"phase"`
	CreatedAt      time.Time `json:"created_at"`
}

// ---------------------------------------------------------------------------
// ClaimManager — interface for testability
// ---------------------------------------------------------------------------

// ClaimManager abstracts Kubernetes SandboxClaim operations so we can mock
// them in unit tests.
type ClaimManager interface {
	// Create creates a SandboxClaim CR and waits for it to become Bound.
	// Returns the sandbox endpoint (pod IP + port) on success.
	Create(ctx context.Context, name, namespace, tenantID, poolRef string, ttl int) (endpoint string, err error)

	// Delete deletes a SandboxClaim CR.
	Delete(ctx context.Context, name, namespace string) error

	// GetStatus returns the phase and endpoint of an existing claim.
	GetStatus(ctx context.Context, name, namespace string) (phase, endpoint string, err error)
}

// ---------------------------------------------------------------------------
// Router — core HTTP service
// ---------------------------------------------------------------------------

// Router is the main sandbox router service.
type Router struct {
	cfg      routerConfig
	claims   ClaimManager
	mu       sync.RWMutex
	sessions map[string]*SandboxSession // key = sandbox_id (claim name)
	client   *http.Client               // for proxying to sandbox pods
}

// NewRouter creates a new sandbox router.
func NewRouter(cfg routerConfig, claims ClaimManager) *Router {
	return &Router{
		cfg:      cfg,
		claims:   claims,
		sessions: make(map[string]*SandboxSession),
		client:   &http.Client{Timeout: 70 * time.Second},
	}
}

// Handler returns the top-level HTTP handler.
func (rt *Router) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("POST /v1/sandboxes", rt.handleCreate)
	mux.HandleFunc("GET /v1/sandboxes/{sandbox_id}", rt.handleGet)
	mux.HandleFunc("DELETE /v1/sandboxes/{sandbox_id}", rt.handleDelete)
	mux.HandleFunc("POST /v1/sandboxes/{sandbox_id}/execute", rt.handleProxyExecute)
	mux.HandleFunc("POST /v1/sandboxes/{sandbox_id}/upload", rt.handleProxyUpload)
	mux.HandleFunc("GET /v1/sandboxes/{sandbox_id}/download", rt.handleProxyDownload)
	mux.HandleFunc("GET /v1/sandboxes/{sandbox_id}/list", rt.handleProxyList)
	mux.HandleFunc("GET /healthz", rt.handleHealthz)

	return mux
}

// ActiveSessions returns a snapshot of currently tracked sessions.
func (rt *Router) ActiveSessions() []*SandboxSession {
	rt.mu.RLock()
	defer rt.mu.RUnlock()
	out := make([]*SandboxSession, 0, len(rt.sessions))
	for _, s := range rt.sessions {
		out = append(out, s)
	}
	return out
}

// ---------------------------------------------------------------------------
// Handlers
// ---------------------------------------------------------------------------

type createRequest struct {
	ConversationID string `json:"conversation_id"`
	TenantID       string `json:"tenant_id"`
	PoolRef        string `json:"pool_ref"`
}

type createResponse struct {
	SandboxID string `json:"sandbox_id"`
	Endpoint  string `json:"endpoint"`
}

func (rt *Router) handleCreate(w http.ResponseWriter, r *http.Request) {
	var req createRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body: " + err.Error()})
		return
	}
	if req.ConversationID == "" || req.TenantID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "conversation_id and tenant_id are required"})
		return
	}

	poolRef := req.PoolRef
	if poolRef == "" {
		poolRef = rt.cfg.PoolRef
	}

	claimName := fmt.Sprintf("claim-%s-%d", req.ConversationID, time.Now().UnixMilli())

	// Create claim with a 10-second timeout.
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	endpoint, err := rt.claims.Create(ctx, claimName, rt.cfg.Namespace, req.TenantID, poolRef, rt.cfg.ClaimTTL)
	if err != nil {
		slog.Error("claim creation failed", "claim", claimName, "error", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "sandbox claim failed: " + err.Error(),
		})
		return
	}

	session := &SandboxSession{
		SandboxID:      claimName,
		ConversationID: req.ConversationID,
		TenantID:       req.TenantID,
		Endpoint:       endpoint,
		Phase:          "Bound",
		CreatedAt:      time.Now(),
	}

	rt.mu.Lock()
	rt.sessions[claimName] = session
	rt.mu.Unlock()

	slog.Info("sandbox claimed", "sandbox_id", claimName, "endpoint", endpoint, "conversation_id", req.ConversationID)

	writeJSON(w, http.StatusOK, createResponse{
		SandboxID: claimName,
		Endpoint:  endpoint,
	})
}

func (rt *Router) handleGet(w http.ResponseWriter, r *http.Request) {
	sandboxID := r.PathValue("sandbox_id")

	rt.mu.RLock()
	session, ok := rt.sessions[sandboxID]
	rt.mu.RUnlock()

	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "sandbox not found"})
		return
	}

	writeJSON(w, http.StatusOK, session)
}

func (rt *Router) handleDelete(w http.ResponseWriter, r *http.Request) {
	sandboxID := r.PathValue("sandbox_id")

	rt.mu.Lock()
	_, ok := rt.sessions[sandboxID]
	if ok {
		delete(rt.sessions, sandboxID)
	}
	rt.mu.Unlock()

	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "sandbox not found"})
		return
	}

	if err := rt.claims.Delete(r.Context(), sandboxID, rt.cfg.Namespace); err != nil {
		slog.Error("claim deletion failed", "sandbox_id", sandboxID, "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "claim deletion failed: " + err.Error()})
		return
	}

	slog.Info("sandbox released", "sandbox_id", sandboxID)
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// ---------------------------------------------------------------------------
// Proxy handlers
// ---------------------------------------------------------------------------

func (rt *Router) handleProxyExecute(w http.ResponseWriter, r *http.Request) {
	rt.proxyRequest(w, r, "/execute", "POST")
}

func (rt *Router) handleProxyUpload(w http.ResponseWriter, r *http.Request) {
	rt.proxyRequest(w, r, "/upload", "POST")
}

func (rt *Router) handleProxyDownload(w http.ResponseWriter, r *http.Request) {
	rt.proxyRequest(w, r, "/download"+"?"+r.URL.RawQuery, "GET")
}

func (rt *Router) handleProxyList(w http.ResponseWriter, r *http.Request) {
	rt.proxyRequest(w, r, "/list"+"?"+r.URL.RawQuery, "GET")
}

func (rt *Router) proxyRequest(w http.ResponseWriter, r *http.Request, targetPath, method string) {
	sandboxID := r.PathValue("sandbox_id")

	rt.mu.RLock()
	session, ok := rt.sessions[sandboxID]
	rt.mu.RUnlock()

	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "sandbox not found"})
		return
	}

	targetURL := session.Endpoint + targetPath

	var body io.Reader
	if method == "POST" {
		body = r.Body
	}

	proxyReq, err := http.NewRequestWithContext(r.Context(), method, targetURL, body)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to create proxy request"})
		return
	}

	// Forward content type for POST requests.
	if method == "POST" {
		proxyReq.Header.Set("Content-Type", r.Header.Get("Content-Type"))
	}

	resp, err := rt.client.Do(proxyReq)
	if err != nil {
		slog.Error("proxy request failed", "sandbox_id", sandboxID, "target", targetURL, "error", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "sandbox pod unreachable: " + err.Error()})
		return
	}
	defer resp.Body.Close()

	// Copy response headers and status.
	for k, vv := range resp.Header {
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

func (rt *Router) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, `{"status":"ok"}`)
}

// ---------------------------------------------------------------------------
// Kubernetes ClaimManager implementation
// ---------------------------------------------------------------------------

// K8sClaimManager implements ClaimManager using the Kubernetes API.
// It creates/deletes SandboxClaim custom resources and watches for binding.
type K8sClaimManager struct {
	// In production this would hold a dynamic client from client-go.
	// The interface is designed so the actual K8s integration is a thin layer.
	// For now, this is a placeholder struct — the real implementation uses
	// client-go dynamic client to create/watch unstructured SandboxClaim CRs.
}

// Create creates a SandboxClaim CR and watches for it to become Bound.
// This is the production implementation outline — in unit tests we use a mock.
func (k *K8sClaimManager) Create(ctx context.Context, name, namespace, tenantID, poolRef string, ttl int) (string, error) {
	// Production implementation:
	// 1. Build unstructured SandboxClaim object
	// 2. Create via dynamic client
	// 3. Watch for status.phase == "Bound"
	// 4. Return status.endpoint
	//
	// claim := &unstructured.Unstructured{
	//     Object: map[string]interface{}{
	//         "apiVersion": "sandbox.volund.ai/v1alpha1",
	//         "kind":       "SandboxClaim",
	//         "metadata": map[string]interface{}{
	//             "name":      name,
	//             "namespace": namespace,
	//             "labels": map[string]interface{}{
	//                 "volund.ai/tenant": tenantID,
	//             },
	//         },
	//         "spec": map[string]interface{}{
	//             "poolRef":  poolRef,
	//             "tenantId": tenantID,
	//             "ttl":      ttl,
	//         },
	//     },
	// }
	//
	// Use fieldSelector on metadata.name + watch for status.phase=Bound
	// with the context timeout (max 10s from caller).

	return "", fmt.Errorf("K8sClaimManager.Create: not yet wired to client-go (use mock for testing)")
}

// Delete deletes a SandboxClaim CR.
func (k *K8sClaimManager) Delete(ctx context.Context, name, namespace string) error {
	// Production: dynamic client Delete on the SandboxClaim resource.
	return fmt.Errorf("K8sClaimManager.Delete: not yet wired to client-go (use mock for testing)")
}

// GetStatus returns the current phase and endpoint of a SandboxClaim.
func (k *K8sClaimManager) GetStatus(ctx context.Context, name, namespace string) (string, string, error) {
	return "", "", fmt.Errorf("K8sClaimManager.GetStatus: not yet wired to client-go (use mock for testing)")
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envOrDefaultInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil {
			return n
		}
	}
	return fallback
}

// ---------------------------------------------------------------------------
// main
// ---------------------------------------------------------------------------

func main() {
	cfg := loadConfig()

	slog.Info("sandbox router starting",
		"addr", cfg.Addr,
		"namespace", cfg.Namespace,
		"pool_ref", cfg.PoolRef,
		"claim_ttl", cfg.ClaimTTL,
	)

	// In production, initialize the real K8s claim manager:
	//   config, _ := rest.InClusterConfig()
	//   dynClient, _ := dynamic.NewForConfig(config)
	//   claims := &K8sClaimManager{client: dynClient}
	claims := &K8sClaimManager{}

	router := NewRouter(cfg, claims)

	srv := &http.Server{
		Addr:         cfg.Addr,
		Handler:      router.Handler(),
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 75 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	// Graceful shutdown.
	done := make(chan os.Signal, 1)
	signal.Notify(done, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("server error", "error", err)
			os.Exit(1)
		}
	}()

	<-done
	slog.Info("shutting down sandbox router")

	// Log active sessions — claims have TTL so we don't delete them.
	active := router.ActiveSessions()
	if len(active) > 0 {
		slog.Info("active sessions at shutdown", "count", len(active))
		for _, s := range active {
			slog.Info("session", "sandbox_id", s.SandboxID, "conversation_id", s.ConversationID, "age", time.Since(s.CreatedAt).Round(time.Second))
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("shutdown error", "error", err)
	}
}
