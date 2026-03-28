package builtin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeSearchHTML is a minimal DuckDuckGo-like HTML response for testing.
const fakeSearchHTML = `
<html>
<body>
<div class="result results_links results_links_deep web-result">
<div>
<a class="result__a" href="/l/?uddg=https%3A%2F%2Fexample.com%2Fpage1&amp;rut=abc">Example Page One</a>
<a class="result__snippet">This is the first result snippet about testing.</a>
</div>
</div>
<div class="result results_links results_links_deep web-result">
<div>
<a class="result__a" href="/l/?uddg=https%3A%2F%2Fexample.com%2Fpage2&amp;rut=def">Example Page Two</a>
<a class="result__snippet">This is the second result with more info.</a>
</div>
</div>
<div class="result results_links results_links_deep web-result">
<div>
<a class="result__a" href="/l/?uddg=https%3A%2F%2Fexample.com%2Fpage3&amp;rut=ghi">Third &amp; Final Page</a>
<a class="result__snippet">A snippet with &lt;html&gt; entities.</a>
</div>
</div>
</body>
</html>
`

func newTestSearchServer(body string, statusCode int) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(statusCode)
		w.Write([]byte(body))
	}))
}

func TestWebSearch_Definition(t *testing.T) {
	ws := &WebSearch{}
	def := ws.Definition()

	if def.Name != "web_search" {
		t.Fatalf("expected name 'web_search', got %q", def.Name)
	}
	if def.Description == "" {
		t.Fatal("expected non-empty description")
	}
	if !strings.Contains(def.InputSchemaJson, "query") {
		t.Fatal("expected schema to contain 'query'")
	}
}

func TestWebSearch_InvalidJSON(t *testing.T) {
	ws := &WebSearch{}
	_, err := ws.Execute(context.Background(), `{not valid json}`)
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
	if !strings.Contains(err.Error(), "invalid web_search input") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestWebSearch_Execute_WithResults(t *testing.T) {
	srv := newTestSearchServer(fakeSearchHTML, http.StatusOK)
	defer srv.Close()

	ws := &WebSearch{SearchURL: srv.URL}
	out, err := ws.Execute(context.Background(), `{"query":"test query","limit":3}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(out, "Example Page One") {
		t.Fatalf("expected result title in output, got %q", out)
	}
	if !strings.Contains(out, "example.com/page1") {
		t.Fatalf("expected decoded URL in output, got %q", out)
	}
	if !strings.Contains(out, "first result snippet") {
		t.Fatalf("expected snippet in output, got %q", out)
	}
}

func TestWebSearch_Execute_HTMLEntities(t *testing.T) {
	srv := newTestSearchServer(fakeSearchHTML, http.StatusOK)
	defer srv.Close()

	ws := &WebSearch{SearchURL: srv.URL}
	out, err := ws.Execute(context.Background(), `{"query":"entities","limit":10}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Third result has HTML entities that should be decoded
	if !strings.Contains(out, "Third & Final Page") {
		t.Fatalf("expected decoded '&amp;' in title, got %q", out)
	}
	if !strings.Contains(out, "<html>") {
		t.Fatalf("expected decoded entities in snippet, got %q", out)
	}
}

func TestWebSearch_Execute_NoResults(t *testing.T) {
	srv := newTestSearchServer("<html><body>No results</body></html>", http.StatusOK)
	defer srv.Close()

	ws := &WebSearch{SearchURL: srv.URL}
	out, err := ws.Execute(context.Background(), `{"query":"xyznonexistent"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "No results found") {
		t.Fatalf("expected 'No results found' message, got %q", out)
	}
}

func TestWebSearch_Execute_ServerError(t *testing.T) {
	srv := newTestSearchServer("Internal Server Error", http.StatusInternalServerError)
	defer srv.Close()

	ws := &WebSearch{SearchURL: srv.URL}
	out, err := ws.Execute(context.Background(), `{"query":"test"}`)
	if err != nil {
		t.Fatalf("unexpected error (should gracefully return): %v", err)
	}
	if !strings.Contains(out, "Search failed") {
		t.Fatalf("expected 'Search failed' message, got %q", out)
	}
}

func TestWebSearch_Execute_DefaultLimit(t *testing.T) {
	srv := newTestSearchServer(fakeSearchHTML, http.StatusOK)
	defer srv.Close()

	ws := &WebSearch{SearchURL: srv.URL}
	out, err := ws.Execute(context.Background(), `{"query":"test"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should return results (up to default 5, but our mock only has 3)
	if strings.Contains(out, "No results found") {
		t.Fatalf("expected results with default limit, got %q", out)
	}
}

func TestWebSearch_Execute_LimitCapped(t *testing.T) {
	srv := newTestSearchServer(fakeSearchHTML, http.StatusOK)
	defer srv.Close()

	ws := &WebSearch{SearchURL: srv.URL}
	// Request limit=1 should only return 1 result
	out, err := ws.Execute(context.Background(), `{"query":"test","limit":1}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Count numbered results (lines starting with "N. ")
	lines := strings.Split(out, "\n")
	count := 0
	for _, line := range lines {
		if len(line) > 2 && line[0] >= '1' && line[0] <= '9' && line[1] == '.' {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("expected 1 result with limit=1, got %d results in output:\n%s", count, out)
	}
}

func TestParseResults(t *testing.T) {
	results := parseResults(fakeSearchHTML, 10)
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}

	r := results[0]
	if r.Title != "Example Page One" {
		t.Fatalf("expected title 'Example Page One', got %q", r.Title)
	}
	if r.URL != "https://example.com/page1" {
		t.Fatalf("expected URL 'https://example.com/page1', got %q", r.URL)
	}
	if !strings.Contains(r.Snippet, "first result snippet") {
		t.Fatalf("expected snippet, got %q", r.Snippet)
	}
}

func TestStripHTML(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"<b>bold</b>", "bold"},
		{"&amp; &lt; &gt;", "& < >"},
		{"no tags", "no tags"},
		{"  spaces  ", "spaces"},
		{"<a href='x'>link</a> text", "link text"},
	}
	for _, tc := range tests {
		got := stripHTML(tc.input)
		if got != tc.expected {
			t.Errorf("stripHTML(%q) = %q, want %q", tc.input, got, tc.expected)
		}
	}
}
