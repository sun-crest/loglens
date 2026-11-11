package mcp_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/sun-crest/loglens/internal/mcp"
)

// newHTTPTest builds a real Streamable HTTP endpoint (like loglens serve)
// over the test fixture logs, with an optional bearer token.
func newHTTPTest(t *testing.T, token string) (*httptest.Server, string) {
	t.Helper()
	_, cfg, dir := newTestServer(t)
	ts := httptest.NewServer(mcp.NewHTTPHandler(cfg, "test", mcp.HTTPOptions{Token: token}))
	t.Cleanup(ts.Close)
	return ts, dir
}

// connectHTTP dials the endpoint through the SDK's StreamableClientTransport,
// optionally attaching an auth header to every request.
func connectHTTP(t *testing.T, ts *httptest.Server, authHeader string) *sdk.ClientSession {
	t.Helper()
	hc := ts.Client()
	if authHeader != "" {
		hc = &http.Client{Transport: headerRoundTripper{base: ts.Client().Transport, hdr: authHeader}}
	}
	tr := &sdk.StreamableClientTransport{
		Endpoint:             ts.URL + "/mcp",
		HTTPClient:           hc,
		DisableStandaloneSSE: true,
	}
	c := sdk.NewClient(&sdk.Implementation{Name: "test-client", Version: "0.0.1"}, nil)
	cs, err := c.Connect(context.Background(), tr, nil)
	if err != nil {
		t.Fatalf("client connect over HTTP: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

// headerRoundTripper injects a header on every outgoing request.
type headerRoundTripper struct {
	base http.RoundTripper
	hdr  string
}

func (rt headerRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", rt.hdr)
	return rt.base.RoundTrip(r)
}

func TestServeStreamableHTTPEndToEnd(t *testing.T) {
	ts, dir := newHTTPTest(t, "")
	cs := connectHTTP(t, ts, "")

	// Tools are advertised over the HTTP transport.
	var names []string
	for tool, err := range cs.Tools(context.Background(), nil) {
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, tool.Name)
	}
	if !containsStr(names, "get_log_slice") {
		t.Errorf("tools = %v, want get_log_slice", names)
	}

	// The core operation works through the full HTTP round trip.
	out, isErr := call(t, cs, "get_log_slice", map[string]any{
		"file_path":   filepath.Join(dir, "api.log"),
		"limit_lines": 2,
	})
	if isErr {
		t.Fatalf("get_log_slice over HTTP errored: %v", out["_error"])
	}
	entries := out["entries"].([]any)
	if len(entries) != 2 {
		t.Fatalf("tail entries = %d, want 2", len(entries))
	}
	last := entries[len(entries)-1].(map[string]any)
	if !strings.Contains(last["text"].(string), "db timeout") {
		t.Errorf("tail should end at newest line, got %v", last["text"])
	}
}

func TestServeAuth(t *testing.T) {
	ts, _ := newHTTPTest(t, "secret")

	cases := []struct {
		name string
		hdr  string
		want int
	}{
		{"no header", "", http.StatusUnauthorized},
		{"wrong scheme", "Basic abc", http.StatusUnauthorized},
		{"wrong token", "Bearer nope", http.StatusUnauthorized},
		{"correct token", "Bearer secret", -1}, // -1 = anything except 401
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequest("POST", ts.URL+"/mcp",
				strings.NewReader(`{"jsonrpc":"2.0","method":"initialize","params":{},"id":1}`))
			if err != nil {
				t.Fatal(err)
			}
			if tc.hdr != "" {
				req.Header.Set("Authorization", tc.hdr)
			}
			resp, err := ts.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if tc.want == -1 {
				if resp.StatusCode == http.StatusUnauthorized {
					t.Errorf("correct token still got 401")
				}
			} else if resp.StatusCode != tc.want {
				t.Errorf("status = %d, want %d", resp.StatusCode, tc.want)
			}
		})
	}

	// Authenticated client can actually call tools.
	cs := connectHTTP(t, ts, "Bearer secret")
	out, isErr := call(t, cs, "list_log_files", map[string]any{})
	if isErr {
		t.Fatalf("list_log_files with token errored: %v", out["_error"])
	}
}

func TestServeRejectsNonMCPPath(t *testing.T) {
	ts, _ := newHTTPTest(t, "")
	resp, err := http.Get(ts.URL + "/other")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("GET /other status = %d, want 404", resp.StatusCode)
	}
}

func containsStr(hay []string, needle string) bool {
	for _, s := range hay {
		if s == needle {
			return true
		}
	}
	return false
}
