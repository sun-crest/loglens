package mcp

import (
	"crypto/subtle"
	"net/http"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/sun-crest/loglens/internal/security"
)

// HTTPOptions configures the Streamable HTTP endpoint served by loglens serve.
type HTTPOptions struct {
	// Token, when non-empty, requires every request to carry an
	// "Authorization: Bearer <token>" header. Empty allows any request —
	// intended for loopback-only or SSH-tunnel deployments.
	Token string
	// Path is the URL path served (default "/mcp").
	Path string
}

// NewHTTPHandler builds the MCP endpoint over Streamable HTTP (go-sdk's
// StreamableHTTPHandler), wrapped with path matching and optional bearer-token
// auth on top of the standard LogLens security bounds (whitelist + limits).
// The returned handler is a plain http.Handler; serve it with http.Server.
func NewHTTPHandler(cfg *security.Config, version string, opts HTTPOptions) http.Handler {
	path := opts.Path
	if path == "" {
		path = "/mcp"
	}
	s := NewServer(cfg, version)
	mux := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return s }, &sdk.StreamableHTTPOptions{})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != path {
			http.NotFound(w, r)
			return
		}
		if !authorized(r, opts.Token) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="loglens"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

// authorized checks request auth. An empty configured token lets every request
// through (loopback-only default). Otherwise the request must carry
// "Authorization: Bearer <token>", compared in constant time.
func authorized(r *http.Request, token string) bool {
	if token == "" {
		return true
	}
	h := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if len(h) <= len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		return false
	}
	got := strings.TrimSpace(h[len(prefix):])
	return subtle.ConstantTimeCompare([]byte(got), []byte(token)) == 1
}
