package moonraker

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// defaultTimeout bounds a single HTTP request/response cycle.
const defaultTimeout = 30 * time.Second

// defaultUserAgent identifies the client to Moonraker when none is configured.
const defaultUserAgent = "mcp-raker"

// fallbackURL is the base URL used when Options leaves it empty.
const fallbackURL = "http://localhost:7125"

// Doer is the subset of *http.Client the client relies on, so tests can inject
// a custom round-tripper.
type Doer interface {
	Do(req *http.Request) (*http.Response, error)
}

// API is the behaviour the MCP tools depend on. Every Moonraker endpoint is
// reached through one of these verbs; the path and parameters carry the
// specifics. It is satisfied by *Client and mocked in tests.
type API interface {
	// Get issues a GET request and returns the unwrapped result payload.
	Get(ctx context.Context, path string, query url.Values) (json.RawMessage, error)
	// Post issues a POST request with optional query parameters and an optional
	// JSON body (nil for none). Moonraker accepts arguments in either place.
	Post(ctx context.Context, path string, query url.Values, body any) (json.RawMessage, error)
	// Delete issues a DELETE request.
	Delete(ctx context.Context, path string, query url.Values) (json.RawMessage, error)
	// Upload streams a multipart file upload to the file manager.
	Upload(ctx context.Context, opts *UploadOptions) (json.RawMessage, error)
	// GetRaw issues a GET request and returns the raw, unwrapped response body
	// (used for file downloads that are not JSON-enveloped).
	GetRaw(ctx context.Context, path string, query url.Values) ([]byte, error)
}

// UploadOptions describes a multipart upload to the file manager.
type UploadOptions struct {
	// Root is the registered root directory (e.g. "gcodes").
	Root string
	// Path is the destination path within the root (optional).
	Path string
	// Filename is the uploaded file's name.
	Filename string
	// Content is the file body.
	Content []byte
	// StartPrint requests that the print start immediately after upload.
	StartPrint bool
}

// Options configures a Client.
type Options struct {
	// ReadOnly enforces the diagnostic request allowlist. The fork executable
	// always enables it; the zero value preserves upstream library test behavior.
	ReadOnly bool
	// BaseURL is the Moonraker base URL; empty falls back to localhost:7125.
	BaseURL string
	// APIKey, when set, is sent as the X-Api-Key header on every request.
	APIKey string
	// Token is a pre-obtained Bearer (JWT) token used instead of a login.
	Token string
	// Username and Password authenticate via /access/login when set.
	Username string
	Password string
	// TokenPath persists the session token between runs (empty disables it).
	TokenPath string
	// UserAgent overrides defaultUserAgent.
	UserAgent string
	// Timeout overrides defaultTimeout.
	Timeout time.Duration
	// Transport overrides the HTTP round-tripper (e.g. for a proxy).
	Transport http.RoundTripper
	// Logger receives warnings such as a failed token-cache write; nil discards.
	Logger *slog.Logger
}

// Ensure *Client satisfies the API interface.
var _ API = (*Client)(nil)

// Client is the concrete moonraker.API backed by net/http.
type Client struct {
	readOnly  bool
	baseURL   string
	http      Doer
	apiKey    string
	username  string
	password  string
	tokenPath string
	userAgent string

	logger *slog.Logger

	authMu       sync.Mutex   // serialises logins
	mu           sync.RWMutex // guards token and refreshToken
	token        string
	refreshToken string
}

// New builds a Client from opts. It seeds the token from the explicit override
// or the on-disk cache, but defers any login until the first request.
func New(opts *Options) (*Client, error) {
	if opts == nil {
		opts = &Options{}
	}

	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}

	logger := opts.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}

	client := &Client{
		readOnly:  opts.ReadOnly,
		baseURL:   strings.TrimRight(orDefault(opts.BaseURL, fallbackURL), "/"),
		http:      &http.Client{Timeout: timeout, Transport: opts.Transport},
		apiKey:    opts.APIKey,
		username:  opts.Username,
		password:  opts.Password,
		tokenPath: opts.TokenPath,
		userAgent: orDefault(opts.UserAgent, defaultUserAgent),
		logger:    logger,
	}
	if opts.ReadOnly {
		err := client.enforceReadOnly(opts)
		if err != nil {
			return nil, err
		}
	}

	if opts.Token != "" {
		client.token = opts.Token
	} else {
		client.loadToken()
	}

	return client, nil
}

// orDefault returns value, or fallback when value is empty.
func orDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}

	return value
}

// usesJWT reports whether the client should obtain and refresh a JWT via
// username/password before authed calls.
func (c *Client) usesJWT() bool {
	return c.apiKey == "" && c.username != "" && c.password != ""
}

// canReauth reports whether a rejected token can be refreshed by logging in.
// This holds exactly when the client uses JWT auth: an API key takes precedence
// over a Bearer token (see attachAuth), so with a key there is nothing to
// refresh and a 401 is terminal.
func (c *Client) canReauth() bool {
	return c.usesJWT()
}

// currentToken returns the active Bearer token, or "" when unauthenticated.
func (c *Client) currentToken() string {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return c.token
}

// refreshTokenValue returns the stored refresh token, or "" when none.
func (c *Client) refreshTokenValue() string {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return c.refreshToken
}

// storeTokens replaces the active and refresh tokens and persists them.
func (c *Client) storeTokens(token, refresh string) {
	c.mu.Lock()
	c.token = token
	c.refreshToken = refresh
	c.mu.Unlock()

	c.saveToken(token, refresh)
}
