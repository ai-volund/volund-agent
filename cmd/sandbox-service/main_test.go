package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func setupWorkspace(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	old := workspaceDir
	workspaceDir = dir
	t.Cleanup(func() { workspaceDir = old })
	return dir
}

// newRequest builds an HTTP request with the given method, path, and optional JSON body.
func newRequest(t *testing.T, method, path, body string) *http.Request {
	t.Helper()
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	r.Header.Set("Content-Type", "application/json")
	return r
}

// ---------- handleExecute ----------

func TestHandleExecute_ValidBash(t *testing.T) {
	setupWorkspace(t)

	body := `{"language":"bash","code":"echo hello world"}`
	w := httptest.NewRecorder()
	handleExecute(w, newRequest(t, http.MethodPost, "/execute", body))

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp executeResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got := strings.TrimSpace(resp.Stdout); got != "hello world" {
		t.Errorf("stdout = %q, want %q", got, "hello world")
	}
	if resp.ExitCode != 0 {
		t.Errorf("exit_code = %d, want 0", resp.ExitCode)
	}
}

func TestHandleExecute_ValidPython(t *testing.T) {
	if _, err := os.Stat("/usr/bin/python3"); err != nil {
		if _, err2 := os.Stat("/usr/local/bin/python3"); err2 != nil {
			t.Skip("python3 not available")
		}
	}
	setupWorkspace(t)

	body := `{"language":"python","code":"print(2+2)"}`
	w := httptest.NewRecorder()
	handleExecute(w, newRequest(t, http.MethodPost, "/execute", body))

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp executeResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got := strings.TrimSpace(resp.Stdout); got != "4" {
		t.Errorf("stdout = %q, want %q", got, "4")
	}
	if resp.ExitCode != 0 {
		t.Errorf("exit_code = %d, want 0", resp.ExitCode)
	}
}

func TestHandleExecute_MissingLanguage(t *testing.T) {
	setupWorkspace(t)

	body := `{"code":"echo hi"}`
	w := httptest.NewRecorder()
	handleExecute(w, newRequest(t, http.MethodPost, "/execute", body))

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
	var resp executeResponse
	json.NewDecoder(w.Body).Decode(&resp)
	if resp.Error == "" {
		t.Error("expected non-empty error")
	}
}

func TestHandleExecute_MissingCode(t *testing.T) {
	setupWorkspace(t)

	body := `{"language":"bash"}`
	w := httptest.NewRecorder()
	handleExecute(w, newRequest(t, http.MethodPost, "/execute", body))

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
	var resp executeResponse
	json.NewDecoder(w.Body).Decode(&resp)
	if !strings.Contains(resp.Error, "language and code are required") {
		t.Errorf("error = %q, want 'language and code are required'", resp.Error)
	}
}

func TestHandleExecute_UnsupportedLanguage(t *testing.T) {
	setupWorkspace(t)

	body := `{"language":"ruby","code":"puts 1"}`
	w := httptest.NewRecorder()
	handleExecute(w, newRequest(t, http.MethodPost, "/execute", body))

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
	var resp executeResponse
	json.NewDecoder(w.Body).Decode(&resp)
	if !strings.Contains(resp.Error, "unsupported language") {
		t.Errorf("error = %q, want to contain 'unsupported language'", resp.Error)
	}
}

func TestHandleExecute_Timeout(t *testing.T) {
	setupWorkspace(t)

	// Use a 1-second timeout with a command that sleeps longer.
	body := `{"language":"bash","code":"sleep 10","timeout":1}`
	w := httptest.NewRecorder()
	handleExecute(w, newRequest(t, http.MethodPost, "/execute", body))

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp executeResponse
	json.NewDecoder(w.Body).Decode(&resp)
	if resp.ExitCode != 124 {
		t.Errorf("exit_code = %d, want 124 for timeout", resp.ExitCode)
	}
	if !strings.Contains(resp.Error, "timed out") {
		t.Errorf("error = %q, want to contain 'timed out'", resp.Error)
	}
}

func TestHandleExecute_NonZeroExit(t *testing.T) {
	setupWorkspace(t)

	body := `{"language":"bash","code":"exit 42"}`
	w := httptest.NewRecorder()
	handleExecute(w, newRequest(t, http.MethodPost, "/execute", body))

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp executeResponse
	json.NewDecoder(w.Body).Decode(&resp)
	if resp.ExitCode != 42 {
		t.Errorf("exit_code = %d, want 42", resp.ExitCode)
	}
}

func TestHandleExecute_InvalidJSON(t *testing.T) {
	setupWorkspace(t)

	w := httptest.NewRecorder()
	handleExecute(w, newRequest(t, http.MethodPost, "/execute", "{bad json"))

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

// ---------- handleUpload ----------

func TestHandleUpload_Valid(t *testing.T) {
	setupWorkspace(t)

	body := `{"path":"hello.txt","content":"hello content"}`
	w := httptest.NewRecorder()
	handleUpload(w, newRequest(t, http.MethodPost, "/upload", body))

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	data, err := os.ReadFile(filepath.Join(workspaceDir, "hello.txt"))
	if err != nil {
		t.Fatalf("read uploaded file: %v", err)
	}
	if string(data) != "hello content" {
		t.Errorf("file content = %q, want %q", string(data), "hello content")
	}
}

func TestHandleUpload_NestedPath(t *testing.T) {
	setupWorkspace(t)

	body := `{"path":"sub/dir/file.txt","content":"nested"}`
	w := httptest.NewRecorder()
	handleUpload(w, newRequest(t, http.MethodPost, "/upload", body))

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	data, err := os.ReadFile(filepath.Join(workspaceDir, "sub", "dir", "file.txt"))
	if err != nil {
		t.Fatalf("read nested file: %v", err)
	}
	if string(data) != "nested" {
		t.Errorf("file content = %q, want %q", string(data), "nested")
	}
}

func TestHandleUpload_MissingPath(t *testing.T) {
	setupWorkspace(t)

	body := `{"content":"hello"}`
	w := httptest.NewRecorder()
	handleUpload(w, newRequest(t, http.MethodPost, "/upload", body))

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestHandleUpload_MissingContent(t *testing.T) {
	setupWorkspace(t)

	body := `{"path":"file.txt"}`
	w := httptest.NewRecorder()
	handleUpload(w, newRequest(t, http.MethodPost, "/upload", body))

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestHandleUpload_PathTraversal(t *testing.T) {
	setupWorkspace(t)

	body := `{"path":"../../../etc/passwd","content":"pwned"}`
	w := httptest.NewRecorder()
	handleUpload(w, newRequest(t, http.MethodPost, "/upload", body))

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}

	var result map[string]string
	json.NewDecoder(w.Body).Decode(&result)
	if result["error"] != "invalid path" {
		t.Errorf("error = %q, want %q", result["error"], "invalid path")
	}
}

// ---------- handleDownload ----------

func TestHandleDownload_Valid(t *testing.T) {
	dir := setupWorkspace(t)
	os.WriteFile(filepath.Join(dir, "readme.txt"), []byte("file contents"), 0644)

	w := httptest.NewRecorder()
	handleDownload(w, newRequest(t, http.MethodGet, "/download?path=readme.txt", ""))

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if got := w.Body.String(); got != "file contents" {
		t.Errorf("body = %q, want %q", got, "file contents")
	}
	ct := w.Header().Get("Content-Type")
	if ct != "application/octet-stream" {
		t.Errorf("Content-Type = %q, want application/octet-stream", ct)
	}
}

func TestHandleDownload_MissingFile(t *testing.T) {
	setupWorkspace(t)

	w := httptest.NewRecorder()
	handleDownload(w, newRequest(t, http.MethodGet, "/download?path=nonexistent.txt", ""))

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestHandleDownload_MissingParam(t *testing.T) {
	setupWorkspace(t)

	w := httptest.NewRecorder()
	handleDownload(w, newRequest(t, http.MethodGet, "/download", ""))

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestHandleDownload_PathTraversal(t *testing.T) {
	setupWorkspace(t)

	w := httptest.NewRecorder()
	handleDownload(w, newRequest(t, http.MethodGet, "/download?path=../../etc/passwd", ""))

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

// ---------- handleList ----------

func TestHandleList_Valid(t *testing.T) {
	dir := setupWorkspace(t)
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0644)
	os.WriteFile(filepath.Join(dir, "b.txt"), []byte("bb"), 0644)

	w := httptest.NewRecorder()
	handleList(w, newRequest(t, http.MethodGet, "/list", ""))

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var result struct {
		Files []fileInfo `json:"files"`
	}
	if err := json.NewDecoder(w.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(result.Files) != 2 {
		t.Errorf("got %d files, want 2", len(result.Files))
	}
}

func TestHandleList_EmptyDirectory(t *testing.T) {
	setupWorkspace(t)

	w := httptest.NewRecorder()
	handleList(w, newRequest(t, http.MethodGet, "/list", ""))

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var result struct {
		Files []fileInfo `json:"files"`
	}
	json.NewDecoder(w.Body).Decode(&result)
	if len(result.Files) != 0 {
		t.Errorf("got %d files, want 0", len(result.Files))
	}
}

func TestHandleList_Subdirectory(t *testing.T) {
	dir := setupWorkspace(t)
	sub := filepath.Join(dir, "subdir")
	os.Mkdir(sub, 0755)
	os.WriteFile(filepath.Join(sub, "c.txt"), []byte("c"), 0644)

	w := httptest.NewRecorder()
	handleList(w, newRequest(t, http.MethodGet, "/list?path=subdir", ""))

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var result struct {
		Files []fileInfo `json:"files"`
	}
	json.NewDecoder(w.Body).Decode(&result)
	if len(result.Files) != 1 {
		t.Errorf("got %d files, want 1", len(result.Files))
	}
	if result.Files[0].Name != "c.txt" {
		t.Errorf("file name = %q, want %q", result.Files[0].Name, "c.txt")
	}
}

func TestHandleList_InvalidPath(t *testing.T) {
	setupWorkspace(t)

	w := httptest.NewRecorder()
	handleList(w, newRequest(t, http.MethodGet, "/list?path=nonexistent", ""))

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestHandleList_PathTraversal(t *testing.T) {
	setupWorkspace(t)

	w := httptest.NewRecorder()
	handleList(w, newRequest(t, http.MethodGet, "/list?path=../../", ""))

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

// ---------- handleExists ----------

func TestHandleExists_FileExists(t *testing.T) {
	dir := setupWorkspace(t)
	os.WriteFile(filepath.Join(dir, "present.txt"), []byte("yes"), 0644)

	w := httptest.NewRecorder()
	handleExists(w, newRequest(t, http.MethodGet, "/exists?path=present.txt", ""))

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var result map[string]any
	json.NewDecoder(w.Body).Decode(&result)
	if result["exists"] != true {
		t.Errorf("exists = %v, want true", result["exists"])
	}
}

func TestHandleExists_FileNotFound(t *testing.T) {
	setupWorkspace(t)

	w := httptest.NewRecorder()
	handleExists(w, newRequest(t, http.MethodGet, "/exists?path=missing.txt", ""))

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var result map[string]any
	json.NewDecoder(w.Body).Decode(&result)
	if result["exists"] != false {
		t.Errorf("exists = %v, want false", result["exists"])
	}
}

func TestHandleExists_MissingParam(t *testing.T) {
	setupWorkspace(t)

	w := httptest.NewRecorder()
	handleExists(w, newRequest(t, http.MethodGet, "/exists", ""))

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestHandleExists_PathTraversal(t *testing.T) {
	setupWorkspace(t)

	w := httptest.NewRecorder()
	handleExists(w, newRequest(t, http.MethodGet, "/exists?path=../../../etc/passwd", ""))

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestHandleExists_DirectoryExists(t *testing.T) {
	dir := setupWorkspace(t)
	os.Mkdir(filepath.Join(dir, "mydir"), 0755)

	w := httptest.NewRecorder()
	handleExists(w, newRequest(t, http.MethodGet, "/exists?path=mydir", ""))

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var result map[string]any
	json.NewDecoder(w.Body).Decode(&result)
	if result["exists"] != true {
		t.Errorf("exists = %v, want true", result["exists"])
	}
	if result["is_dir"] != true {
		t.Errorf("is_dir = %v, want true", result["is_dir"])
	}
}

// ---------- safePath ----------

func TestSafePath(t *testing.T) {
	setupWorkspace(t)

	tests := []struct {
		name    string
		input   string
		wantOK  bool
	}{
		{"simple file", "file.txt", true},
		{"nested path", "sub/dir/file.txt", true},
		{"dot current dir", ".", true},
		{"parent traversal", "../secret", false},
		{"deep traversal", "../../../etc/passwd", false},
		{"absolute path", "/etc/passwd", false},
		{"embedded traversal", "foo/../../etc/passwd", false},
		{"dot dot alone", "..", false},
		{"backtrack from subdir", "sub/../..", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := safePath(tt.input)
			if tt.wantOK && result == "" {
				t.Errorf("safePath(%q) = empty, want valid path", tt.input)
			}
			if !tt.wantOK && result != "" {
				t.Errorf("safePath(%q) = %q, want empty (rejected)", tt.input, result)
			}
			// When a path is accepted, verify it stays inside the workspace.
			if result != "" && !strings.HasPrefix(result, workspaceDir) {
				t.Errorf("safePath(%q) = %q, escapes workspace %q", tt.input, result, workspaceDir)
			}
		})
	}
}

// ---------- healthz ----------

func TestHealthz(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var result map[string]string
	json.NewDecoder(resp.Body).Decode(&result)
	if result["status"] != "ok" {
		t.Errorf("status = %q, want %q", result["status"], "ok")
	}
}

// ---------- writeJSON ----------

func TestWriteJSON(t *testing.T) {
	w := httptest.NewRecorder()
	writeJSON(w, http.StatusCreated, map[string]string{"key": "value"})

	if w.Code != http.StatusCreated {
		t.Errorf("status = %d, want %d", w.Code, http.StatusCreated)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}

	var result map[string]string
	json.NewDecoder(w.Body).Decode(&result)
	if result["key"] != "value" {
		t.Errorf("key = %q, want %q", result["key"], "value")
	}
}
