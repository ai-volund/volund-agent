// Command sandbox-service is a lightweight HTTP server that executes code
// in an isolated environment. It's designed to run in a pod with the gVisor
// RuntimeClass for kernel-level isolation.
//
// Runtime API:
//
//	POST /execute          — run code and return stdout/stderr
//	POST /upload           — upload a file to the workspace
//	GET  /download?path=   — download a file from the workspace
//	GET  /list?path=       — list files in the workspace
//	GET  /exists?path=     — check if a file exists in the workspace
//	GET  /healthz          — health check
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// workspaceDir is the persistent workspace for this sandbox instance.
// Files uploaded or created by code execution persist here across requests.
var workspaceDir string

func init() {
	workspaceDir = os.Getenv("SANDBOX_WORKSPACE")
	if workspaceDir == "" {
		workspaceDir = "/tmp/sandbox-workspace"
	}
}

type executeRequest struct {
	Language string `json:"language"`
	Code     string `json:"code"`
	Timeout  int    `json:"timeout"`
}

type executeResponse struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exit_code"`
	Error    string `json:"error,omitempty"`
}

type uploadRequest struct {
	Path    string `json:"path"`
	Content string `json:"content"` // base64 or plain text
	Mode    int    `json:"mode"`    // file mode, defaults to 0644
}

type fileInfo struct {
	Name    string `json:"name"`
	Size    int64  `json:"size"`
	IsDir   bool   `json:"is_dir"`
	ModTime string `json:"mod_time"`
}

func main() {
	addr := os.Getenv("SANDBOX_ADDR")
	if addr == "" {
		addr = ":8090"
	}

	// Ensure workspace directory exists.
	if err := os.MkdirAll(workspaceDir, 0755); err != nil {
		slog.Error("failed to create workspace", "dir", workspaceDir, "error", err)
		os.Exit(1)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /execute", handleExecute)
	mux.HandleFunc("POST /upload", handleUpload)
	mux.HandleFunc("GET /download", handleDownload)
	mux.HandleFunc("GET /list", handleList)
	mux.HandleFunc("GET /exists", handleExists)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"status":"ok"}`)
	})

	slog.Info("sandbox service starting", "addr", addr, "workspace", workspaceDir)
	if err := http.ListenAndServe(addr, mux); err != nil {
		slog.Error("server error", "error", err)
		os.Exit(1)
	}
}

func handleExecute(w http.ResponseWriter, r *http.Request) {
	var req executeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, executeResponse{Error: "invalid request: " + err.Error()})
		return
	}

	if req.Language == "" || req.Code == "" {
		writeJSON(w, http.StatusBadRequest, executeResponse{Error: "language and code are required"})
		return
	}

	timeout := 10 * time.Second
	if req.Timeout > 0 && req.Timeout <= 60 {
		timeout = time.Duration(req.Timeout) * time.Second
	}

	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()

	var cmd *exec.Cmd
	switch req.Language {
	case "python":
		f := filepath.Join(workspaceDir, "code.py")
		os.WriteFile(f, []byte(req.Code), 0600)
		cmd = exec.CommandContext(ctx, "python3", f)
	case "bash":
		f := filepath.Join(workspaceDir, "code.sh")
		os.WriteFile(f, []byte(req.Code), 0700)
		cmd = exec.CommandContext(ctx, "bash", f)
	case "javascript":
		f := filepath.Join(workspaceDir, "code.js")
		os.WriteFile(f, []byte(req.Code), 0600)
		cmd = exec.CommandContext(ctx, "node", f)
	default:
		writeJSON(w, http.StatusBadRequest, executeResponse{Error: "unsupported language: " + req.Language})
		return
	}

	cmd.Dir = workspaceDir
	// Restrict environment — only pass essential vars.
	cmd.Env = []string{
		"PATH=/usr/local/bin:/usr/bin:/bin",
		"HOME=" + workspaceDir,
		"TMPDIR=" + workspaceDir,
	}
	// Apply process group isolation for clean process tree cleanup.
	applySandboxLimits(cmd)

	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf

	runErr := cmd.Run()

	resp := executeResponse{
		Stdout: outBuf.String(),
		Stderr: errBuf.String(),
	}

	if runErr != nil {
		if ctx.Err() == context.DeadlineExceeded {
			resp.Error = fmt.Sprintf("timed out after %v", timeout)
			resp.ExitCode = 124
		} else if exitErr, ok := runErr.(*exec.ExitError); ok {
			resp.ExitCode = exitErr.ExitCode()
		} else {
			resp.Error = runErr.Error()
			resp.ExitCode = 1
		}
	}

	slog.Info("code executed", "language", req.Language, "exit_code", resp.ExitCode, "timeout", timeout)
	writeJSON(w, http.StatusOK, resp)
}

// handleUpload writes a file to the workspace.
func handleUpload(w http.ResponseWriter, r *http.Request) {
	var req uploadRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	if req.Path == "" || req.Content == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "path and content are required"})
		return
	}

	// Prevent path traversal.
	target := safePath(req.Path)
	if target == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid path"})
		return
	}

	mode := os.FileMode(0644)
	if req.Mode > 0 {
		mode = os.FileMode(req.Mode)
	}

	// Ensure parent directories exist.
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if err := os.WriteFile(target, []byte(req.Content), mode); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "uploaded", "path": req.Path})
}

// handleDownload returns a file from the workspace.
func handleDownload(w http.ResponseWriter, r *http.Request) {
	relPath := r.URL.Query().Get("path")
	if relPath == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "path query parameter required"})
		return
	}

	target := safePath(relPath)
	if target == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid path"})
		return
	}

	data, err := os.ReadFile(target)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "file not found"})
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filepath.Base(target)))
	w.Write(data)
}

// handleList returns a listing of files in the workspace.
func handleList(w http.ResponseWriter, r *http.Request) {
	relPath := r.URL.Query().Get("path")
	if relPath == "" {
		relPath = "."
	}

	target := safePath(relPath)
	if target == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid path"})
		return
	}

	entries, err := os.ReadDir(target)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "directory not found"})
		return
	}

	files := make([]fileInfo, 0, len(entries))
	for _, e := range entries {
		info, infoErr := e.Info()
		if infoErr != nil {
			continue
		}
		files = append(files, fileInfo{
			Name:    e.Name(),
			Size:    info.Size(),
			IsDir:   e.IsDir(),
			ModTime: info.ModTime().Format(time.RFC3339),
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{"files": files})
}

// handleExists checks if a file or directory exists in the workspace.
func handleExists(w http.ResponseWriter, r *http.Request) {
	relPath := r.URL.Query().Get("path")
	if relPath == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "path query parameter required"})
		return
	}

	target := safePath(relPath)
	if target == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid path"})
		return
	}

	info, err := os.Stat(target)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"exists": false})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"exists": true,
		"is_dir": info.IsDir(),
		"size":   info.Size(),
	})
}

// safePath resolves a relative path within the workspace, rejecting traversal attempts.
func safePath(relPath string) string {
	// Clean and join to prevent traversal.
	cleaned := filepath.Clean(relPath)
	if strings.HasPrefix(cleaned, "..") || strings.HasPrefix(cleaned, "/") {
		return ""
	}
	full := filepath.Join(workspaceDir, cleaned)
	// Double-check the result is inside the workspace.
	if !strings.HasPrefix(full, workspaceDir) {
		return ""
	}
	return full
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
