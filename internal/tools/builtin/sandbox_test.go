package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// NewSandboxExecutor
// ---------------------------------------------------------------------------

func TestNewSandboxExecutor_EmptyEndpoint(t *testing.T) {
	got := NewSandboxExecutor(SandboxConfig{Endpoint: ""})
	if got != nil {
		t.Fatal("expected nil executor for empty endpoint")
	}
}

func TestNewSandboxExecutor_ValidEndpoint(t *testing.T) {
	got := NewSandboxExecutor(SandboxConfig{Endpoint: "http://sandbox:8090"})
	if got == nil {
		t.Fatal("expected non-nil executor for valid endpoint")
	}
	if got.endpoint != "http://sandbox:8090" {
		t.Fatalf("endpoint = %q, want %q", got.endpoint, "http://sandbox:8090")
	}
	if got.client == nil {
		t.Fatal("expected non-nil http client")
	}
}

// ---------------------------------------------------------------------------
// Execute
// ---------------------------------------------------------------------------

func TestExecute_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/execute" {
			t.Errorf("path = %s, want /execute", r.URL.Path)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type = %s, want application/json", ct)
		}

		var req sandboxRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req.Language != "python" {
			t.Errorf("language = %s, want python", req.Language)
		}
		if req.Code != "print('hello')" {
			t.Errorf("code = %q, want %q", req.Code, "print('hello')")
		}
		if req.Timeout != 10 {
			t.Errorf("timeout = %d, want 10", req.Timeout)
		}

		json.NewEncoder(w).Encode(sandboxResponse{
			Stdout:   "hello\n",
			ExitCode: 0,
		})
	}))
	defer srv.Close()

	exec := NewSandboxExecutor(SandboxConfig{Endpoint: srv.URL})
	output, err := exec.Execute(context.Background(), "python", "print('hello')", 10*time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if output != "hello\n" {
		t.Fatalf("output = %q, want %q", output, "hello\n")
	}
}

func TestExecute_StderrOutput(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(sandboxResponse{
			Stdout:   "out",
			Stderr:   "warn: something",
			ExitCode: 0,
		})
	}))
	defer srv.Close()

	exec := NewSandboxExecutor(SandboxConfig{Endpoint: srv.URL})
	output, err := exec.Execute(context.Background(), "python", "code", 10*time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "out\nstderr:\nwarn: something"
	if output != want {
		t.Fatalf("output = %q, want %q", output, want)
	}
}

func TestExecute_StderrOnly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(sandboxResponse{
			Stderr:   "error output",
			ExitCode: 0,
		})
	}))
	defer srv.Close()

	exec := NewSandboxExecutor(SandboxConfig{Endpoint: srv.URL})
	output, err := exec.Execute(context.Background(), "bash", "cmd", 5*time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "stderr:\nerror output"
	if output != want {
		t.Fatalf("output = %q, want %q", output, want)
	}
}

func TestExecute_NonZeroExitCode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(sandboxResponse{
			Stdout:   "partial output",
			Stderr:   "segfault",
			ExitCode: 139,
		})
	}))
	defer srv.Close()

	exec := NewSandboxExecutor(SandboxConfig{Endpoint: srv.URL})
	output, err := exec.Execute(context.Background(), "c", "code", 10*time.Second)
	if err == nil {
		t.Fatal("expected error for non-zero exit code")
	}
	if output == "" {
		t.Fatal("expected output even on non-zero exit code")
	}
	wantErr := "execution failed (exit code 139)"
	if err.Error() != wantErr {
		t.Fatalf("error = %q, want %q", err.Error(), wantErr)
	}
	wantOutput := "partial output\nstderr:\nsegfault"
	if output != wantOutput {
		t.Fatalf("output = %q, want %q", output, wantOutput)
	}
}

func TestExecute_ErrorResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(sandboxResponse{
			Error: "execution timed out after 60s",
		})
	}))
	defer srv.Close()

	exec := NewSandboxExecutor(SandboxConfig{Endpoint: srv.URL})
	_, err := exec.Execute(context.Background(), "python", "while True: pass", 60*time.Second)
	if err == nil {
		t.Fatal("expected error for timeout response")
	}
	wantErr := "sandbox: execution timed out after 60s"
	if err.Error() != wantErr {
		t.Fatalf("error = %q, want %q", err.Error(), wantErr)
	}
}

func TestExecute_HTTPFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("pod scheduling failed"))
	}))
	defer srv.Close()

	exec := NewSandboxExecutor(SandboxConfig{Endpoint: srv.URL})
	_, err := exec.Execute(context.Background(), "python", "code", 10*time.Second)
	if err == nil {
		t.Fatal("expected error for HTTP 500")
	}
	wantErr := fmt.Sprintf("sandbox error (HTTP %d): pod scheduling failed", http.StatusInternalServerError)
	if err.Error() != wantErr {
		t.Fatalf("error = %q, want %q", err.Error(), wantErr)
	}
}

func TestExecute_ConnectionError(t *testing.T) {
	// Point at a server that was already closed to provoke a connection error.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close()

	exec := NewSandboxExecutor(SandboxConfig{Endpoint: srv.URL})
	_, err := exec.Execute(context.Background(), "python", "code", 5*time.Second)
	if err == nil {
		t.Fatal("expected error when server is unreachable")
	}
}

// ---------------------------------------------------------------------------
// Upload
// ---------------------------------------------------------------------------

func TestUpload_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/upload" {
			t.Errorf("path = %s, want /upload", r.URL.Path)
		}

		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if body["path"] != "/workspace/main.py" {
			t.Errorf("path = %q, want /workspace/main.py", body["path"])
		}
		if body["content"] != "print('hi')" {
			t.Errorf("content = %q, want %q", body["content"], "print('hi')")
		}

		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	exec := NewSandboxExecutor(SandboxConfig{Endpoint: srv.URL})
	err := exec.Upload(context.Background(), "/workspace/main.py", "print('hi')")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestUpload_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("disk full"))
	}))
	defer srv.Close()

	exec := NewSandboxExecutor(SandboxConfig{Endpoint: srv.URL})
	err := exec.Upload(context.Background(), "/workspace/data.csv", "a,b,c")
	if err == nil {
		t.Fatal("expected error for HTTP 500")
	}
	wantErr := fmt.Sprintf("upload error (HTTP %d): disk full", http.StatusInternalServerError)
	if err.Error() != wantErr {
		t.Fatalf("error = %q, want %q", err.Error(), wantErr)
	}
}

// ---------------------------------------------------------------------------
// Download
// ---------------------------------------------------------------------------

func TestDownload_Success(t *testing.T) {
	fileContent := []byte("#!/bin/bash\necho hello")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}
		if r.URL.Path != "/download" {
			t.Errorf("path = %s, want /download", r.URL.Path)
		}
		if got := r.URL.Query().Get("path"); got != "/workspace/script.sh" {
			t.Errorf("query path = %q, want /workspace/script.sh", got)
		}
		w.Write(fileContent)
	}))
	defer srv.Close()

	exec := NewSandboxExecutor(SandboxConfig{Endpoint: srv.URL})
	data, err := exec.Download(context.Background(), "/workspace/script.sh")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(data) != string(fileContent) {
		t.Fatalf("data = %q, want %q", string(data), string(fileContent))
	}
}

func TestDownload_FileNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte("file not found"))
	}))
	defer srv.Close()

	exec := NewSandboxExecutor(SandboxConfig{Endpoint: srv.URL})
	_, err := exec.Download(context.Background(), "/workspace/missing.txt")
	if err == nil {
		t.Fatal("expected error for 404")
	}
	wantErr := fmt.Sprintf("download error (HTTP %d): file not found", http.StatusNotFound)
	if err.Error() != wantErr {
		t.Fatalf("error = %q, want %q", err.Error(), wantErr)
	}
}

// ---------------------------------------------------------------------------
// ListFiles
// ---------------------------------------------------------------------------

func TestListFiles_MultipleFiles(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}
		if r.URL.Path != "/list" {
			t.Errorf("path = %s, want /list", r.URL.Path)
		}
		if got := r.URL.Query().Get("path"); got != "/workspace" {
			t.Errorf("query path = %q, want /workspace", got)
		}

		resp := struct {
			Files []FileInfo `json:"files"`
		}{
			Files: []FileInfo{
				{Name: "main.py", Size: 128, IsDir: false, ModTime: "2026-03-28T10:00:00Z"},
				{Name: "data", Size: 4096, IsDir: true, ModTime: "2026-03-28T09:00:00Z"},
				{Name: "README.md", Size: 512, IsDir: false, ModTime: "2026-03-27T15:00:00Z"},
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	exec := NewSandboxExecutor(SandboxConfig{Endpoint: srv.URL})
	files, err := exec.ListFiles(context.Background(), "/workspace")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(files) != 3 {
		t.Fatalf("len(files) = %d, want 3", len(files))
	}
	if files[0].Name != "main.py" {
		t.Errorf("files[0].Name = %q, want main.py", files[0].Name)
	}
	if files[1].IsDir != true {
		t.Error("files[1].IsDir = false, want true")
	}
	if files[2].Size != 512 {
		t.Errorf("files[2].Size = %d, want 512", files[2].Size)
	}
}

func TestListFiles_EmptyDirectory(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := struct {
			Files []FileInfo `json:"files"`
		}{
			Files: []FileInfo{},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	exec := NewSandboxExecutor(SandboxConfig{Endpoint: srv.URL})
	files, err := exec.ListFiles(context.Background(), "/workspace/empty")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(files) != 0 {
		t.Fatalf("len(files) = %d, want 0", len(files))
	}
}

func TestListFiles_EmptyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// When path is empty, no query param should be set.
		if r.URL.RawQuery != "" {
			t.Errorf("expected no query params for empty path, got %q", r.URL.RawQuery)
		}
		resp := struct {
			Files []FileInfo `json:"files"`
		}{
			Files: []FileInfo{{Name: "root.txt", Size: 10}},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	exec := NewSandboxExecutor(SandboxConfig{Endpoint: srv.URL})
	files, err := exec.ListFiles(context.Background(), "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("len(files) = %d, want 1", len(files))
	}
}

// ---------------------------------------------------------------------------
// Exists
// ---------------------------------------------------------------------------

func TestExists_True(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}
		if r.URL.Path != "/exists" {
			t.Errorf("path = %s, want /exists", r.URL.Path)
		}
		if got := r.URL.Query().Get("path"); got != "/workspace/main.py" {
			t.Errorf("query path = %q, want /workspace/main.py", got)
		}

		json.NewEncoder(w).Encode(map[string]bool{"exists": true})
	}))
	defer srv.Close()

	exec := NewSandboxExecutor(SandboxConfig{Endpoint: srv.URL})
	exists, err := exec.Exists(context.Background(), "/workspace/main.py")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !exists {
		t.Fatal("exists = false, want true")
	}
}

func TestExists_False(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]bool{"exists": false})
	}))
	defer srv.Close()

	exec := NewSandboxExecutor(SandboxConfig{Endpoint: srv.URL})
	exists, err := exec.Exists(context.Background(), "/workspace/nope.txt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if exists {
		t.Fatal("exists = true, want false")
	}
}

// ---------------------------------------------------------------------------
// SandboxServiceEndpoint
// ---------------------------------------------------------------------------

func TestSandboxServiceEndpoint(t *testing.T) {
	const envKey = "VOLUND_SANDBOX_ENDPOINT"
	t.Setenv(envKey, "http://sandbox-service:8090")

	got := SandboxServiceEndpoint()
	if got != "http://sandbox-service:8090" {
		t.Fatalf("SandboxServiceEndpoint() = %q, want %q", got, "http://sandbox-service:8090")
	}
}

func TestSandboxServiceEndpoint_Unset(t *testing.T) {
	const envKey = "VOLUND_SANDBOX_ENDPOINT"
	t.Setenv(envKey, "")

	got := SandboxServiceEndpoint()
	if got != "" {
		t.Fatalf("SandboxServiceEndpoint() = %q, want empty", got)
	}
}

// ---------------------------------------------------------------------------
// RunCodeSandboxed
// ---------------------------------------------------------------------------

func TestRunCodeSandboxed_WithSandbox(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/execute" {
			t.Errorf("path = %s, want /execute", r.URL.Path)
		}

		var req sandboxRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req.Language != "python" {
			t.Errorf("language = %s, want python", req.Language)
		}
		if req.Code != "print(42)" {
			t.Errorf("code = %q, want %q", req.Code, "print(42)")
		}

		json.NewEncoder(w).Encode(sandboxResponse{
			Stdout:   "42\n",
			ExitCode: 0,
		})
	}))
	defer srv.Close()

	sandbox := NewSandboxExecutor(SandboxConfig{Endpoint: srv.URL})
	runner := NewRunCodeSandboxed(sandbox)

	if runner.Name() != "run_code" {
		t.Fatalf("Name() = %q, want run_code", runner.Name())
	}
	if runner.Definition() == nil {
		t.Fatal("Definition() returned nil")
	}

	inputJSON := `{"language":"python","code":"print(42)"}`
	output, err := runner.Execute(context.Background(), inputJSON)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if output != "42\n" {
		t.Fatalf("output = %q, want %q", output, "42\n")
	}
}

func TestRunCodeSandboxed_WithCustomTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req sandboxRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		// Custom timeout of 30s should be passed through.
		if req.Timeout != 30 {
			t.Errorf("timeout = %d, want 30", req.Timeout)
		}

		json.NewEncoder(w).Encode(sandboxResponse{
			Stdout:   "done",
			ExitCode: 0,
		})
	}))
	defer srv.Close()

	sandbox := NewSandboxExecutor(SandboxConfig{Endpoint: srv.URL})
	runner := NewRunCodeSandboxed(sandbox)

	inputJSON := `{"language":"bash","code":"sleep 1","timeout_seconds":30}`
	output, err := runner.Execute(context.Background(), inputJSON)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if output != "done" {
		t.Fatalf("output = %q, want %q", output, "done")
	}
}

func TestRunCodeSandboxed_TimeoutCappedAt60(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req sandboxRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		// Timeout over 60 should be capped to 60.
		if req.Timeout != 60 {
			t.Errorf("timeout = %d, want 60 (capped)", req.Timeout)
		}

		json.NewEncoder(w).Encode(sandboxResponse{
			Stdout:   "ok",
			ExitCode: 0,
		})
	}))
	defer srv.Close()

	sandbox := NewSandboxExecutor(SandboxConfig{Endpoint: srv.URL})
	runner := NewRunCodeSandboxed(sandbox)

	inputJSON := `{"language":"python","code":"x=1","timeout_seconds":120}`
	_, err := runner.Execute(context.Background(), inputJSON)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunCodeSandboxed_InvalidJSON(t *testing.T) {
	sandbox := NewSandboxExecutor(SandboxConfig{Endpoint: "http://unused"})
	runner := NewRunCodeSandboxed(sandbox)

	_, err := runner.Execute(context.Background(), "{invalid json")
	if err == nil {
		t.Fatal("expected error for invalid JSON input")
	}
}

func TestRunCodeSandboxed_WithoutSandbox(t *testing.T) {
	// When sandbox is nil, RunCodeSandboxed falls back to subprocess execution
	// (RunCode). We verify it does not panic and attempts to run code.
	runner := NewRunCodeSandboxed(nil)

	if runner.Name() != "run_code" {
		t.Fatalf("Name() = %q, want run_code", runner.Name())
	}

	// Use a simple echo command that should succeed on any system.
	inputJSON := `{"language":"bash","code":"echo sandbox_fallback_test"}`
	output, err := runner.Execute(context.Background(), inputJSON)
	if err != nil {
		t.Fatalf("subprocess fallback failed: %v", err)
	}
	if output == "" {
		t.Fatal("expected non-empty output from subprocess fallback")
	}
}
