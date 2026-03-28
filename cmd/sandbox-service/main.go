// Command sandbox-service is a lightweight HTTP server that executes code
// in an isolated environment. It's designed to run in a pod with the gVisor
// RuntimeClass for kernel-level isolation.
//
// Endpoints:
//   POST /execute  — run code and return stdout/stderr
//   GET  /healthz  — health check
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
	"time"
)

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

func main() {
	addr := os.Getenv("SANDBOX_ADDR")
	if addr == "" {
		addr = ":8090"
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /execute", handleExecute)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"status":"ok"}`)
	})

	slog.Info("sandbox service starting", "addr", addr)
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

	workDir, err := os.MkdirTemp("", "sandbox-*")
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, executeResponse{Error: "create workdir: " + err.Error()})
		return
	}
	defer os.RemoveAll(workDir)

	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()

	var cmd *exec.Cmd
	switch req.Language {
	case "python":
		f := filepath.Join(workDir, "code.py")
		os.WriteFile(f, []byte(req.Code), 0600)
		cmd = exec.CommandContext(ctx, "python3", f)
	case "bash":
		f := filepath.Join(workDir, "code.sh")
		os.WriteFile(f, []byte(req.Code), 0700)
		cmd = exec.CommandContext(ctx, "bash", f)
	case "javascript":
		f := filepath.Join(workDir, "code.js")
		os.WriteFile(f, []byte(req.Code), 0600)
		cmd = exec.CommandContext(ctx, "node", f)
	default:
		writeJSON(w, http.StatusBadRequest, executeResponse{Error: "unsupported language: " + req.Language})
		return
	}

	cmd.Dir = workDir
	// Restrict environment — only pass essential vars.
	cmd.Env = []string{
		"PATH=/usr/local/bin:/usr/bin:/bin",
		"HOME=" + workDir,
		"TMPDIR=" + workDir,
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

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
