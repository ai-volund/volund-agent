package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	volundv1 "github.com/ai-volund/volund-proto/gen/go/volund/v1"
)

// WebSearch performs web searches using the DuckDuckGo HTML API (no API key needed).
// The SearchURL field can be overridden for testing.
type WebSearch struct {
	// SearchURL is the base URL for the search endpoint.
	// Defaults to "https://html.duckduckgo.com/html/" if empty.
	SearchURL string

	// Client is the HTTP client to use. Defaults to http.DefaultClient if nil.
	Client *http.Client
}

type webSearchInput struct {
	Query string `json:"query"`
	Limit int    `json:"limit,omitempty"`
}

type searchResult struct {
	Title   string
	URL     string
	Snippet string
}

func (w *WebSearch) Name() string { return "web_search" }

func (w *WebSearch) Definition() *volundv1.ToolDefinition {
	return &volundv1.ToolDefinition{
		Name:        "web_search",
		Description: "Search the web for current information. Returns a list of relevant results with titles, URLs, and snippets.",
		InputSchemaJson: `{
			"type": "object",
			"required": ["query"],
			"properties": {
				"query": {
					"type": "string",
					"description": "The search query"
				},
				"limit": {
					"type": "integer",
					"description": "Max number of results to return (default 5, max 10)",
					"minimum": 1,
					"maximum": 10
				}
			}
		}`,
	}
}

func (w *WebSearch) Execute(ctx context.Context, inputJSON string) (string, error) {
	var input webSearchInput
	if err := json.Unmarshal([]byte(inputJSON), &input); err != nil {
		return "", fmt.Errorf("invalid web_search input: %w", err)
	}

	limit := input.Limit
	if limit <= 0 {
		limit = 5
	}
	if limit > 10 {
		limit = 10
	}

	results, err := w.fetchResults(ctx, input.Query, limit)
	if err != nil {
		return fmt.Sprintf("Search failed: %v. Query was: %q", err, input.Query), nil
	}

	if len(results) == 0 {
		return fmt.Sprintf("No results found for: %q", input.Query), nil
	}

	var sb strings.Builder
	for i, r := range results {
		fmt.Fprintf(&sb, "%d. %s\n   URL: %s\n   %s\n", i+1, r.Title, r.URL, r.Snippet)
		if i < len(results)-1 {
			sb.WriteString("\n")
		}
	}
	return sb.String(), nil
}

func (w *WebSearch) fetchResults(ctx context.Context, query string, limit int) ([]searchResult, error) {
	baseURL := w.SearchURL
	if baseURL == "" {
		baseURL = "https://html.duckduckgo.com/html/"
	}

	client := w.Client
	if client == nil {
		client = http.DefaultClient
	}

	formData := url.Values{}
	formData.Set("q", query)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL, strings.NewReader(formData.Encode()))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; VolundAgent/1.0)")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("executing search request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("search returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}

	return parseResults(string(body), limit), nil
}

// parseResults extracts search results from DuckDuckGo HTML response.
// The HTML contains result blocks with class "result" containing:
//   - a.result__a (title + URL)
//   - a.result__snippet (snippet text)
func parseResults(html string, limit int) []searchResult {
	var results []searchResult

	// Match result blocks - each contains a title link and snippet
	// DuckDuckGo HTML uses class="result results_links results_links_deep web-result"
	resultBlockRe := regexp.MustCompile(`(?s)<div[^>]*class="[^"]*result[^"]*"[^>]*>(.*?)</div>\s*</div>`)
	blocks := resultBlockRe.FindAllStringSubmatch(html, limit*2) // grab extra in case some don't parse

	// Title/URL pattern
	titleRe := regexp.MustCompile(`<a[^>]*class="result__a"[^>]*href="([^"]*)"[^>]*>(.*?)</a>`)
	// Snippet pattern
	snippetRe := regexp.MustCompile(`(?s)<a[^>]*class="result__snippet"[^>]*>(.*?)</a>`)

	for _, block := range blocks {
		if len(results) >= limit {
			break
		}
		content := block[1]

		titleMatch := titleRe.FindStringSubmatch(content)
		if titleMatch == nil {
			continue
		}

		resultURL := titleMatch[1]
		// DuckDuckGo wraps URLs in a redirect; extract the actual URL
		if u, err := url.Parse(resultURL); err == nil {
			if actual := u.Query().Get("uddg"); actual != "" {
				resultURL = actual
			}
		}

		title := stripHTML(titleMatch[2])
		if title == "" {
			continue
		}

		snippet := ""
		snippetMatch := snippetRe.FindStringSubmatch(content)
		if snippetMatch != nil {
			snippet = stripHTML(snippetMatch[1])
		}

		results = append(results, searchResult{
			Title:   title,
			URL:     resultURL,
			Snippet: snippet,
		})
	}

	return results
}

// stripHTML removes HTML tags and decodes common HTML entities.
func stripHTML(s string) string {
	tagRe := regexp.MustCompile(`<[^>]*>`)
	s = tagRe.ReplaceAllString(s, "")

	replacer := strings.NewReplacer(
		"&amp;", "&",
		"&lt;", "<",
		"&gt;", ">",
		"&quot;", `"`,
		"&#39;", "'",
		"&apos;", "'",
		"&#x27;", "'",
		"&nbsp;", " ",
	)
	s = replacer.Replace(s)
	s = strings.TrimSpace(s)
	return s
}
