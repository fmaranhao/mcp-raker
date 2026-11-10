// Package config loads the mcp-raker configuration from environment variables.
package config

import (
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/cockroachdb/errors"
)

const maxPort = 65535

// DefaultURL is the Moonraker base URL used when MOONRAKER_URL is unset.
const DefaultURL = "http://localhost:7125"

// defaultUserAgent identifies the client to Moonraker when none is configured.
const defaultUserAgent = "mcp-raker"

// defaultTimeout bounds a single HTTP request/response cycle.
const defaultTimeout = 30 * time.Second

// ErrInvalidURL is returned when MOONRAKER_URL is not a valid base URL.
var ErrInvalidURL = errors.New("MOONRAKER_URL must be a valid http(s) URL with a host")

// ErrInvalidHTTPPort is returned when MCP_HTTP_PORT is not a valid port number.
var ErrInvalidHTTPPort = errors.New("MCP_HTTP_PORT must be a valid port number (1-65535)")

// ErrInvalidProxy is returned when MOONRAKER_PROXY is not a valid URL.
var ErrInvalidProxy = errors.New("MOONRAKER_PROXY must be a valid proxy URL")

// ErrInvalidTimeout is returned when MOONRAKER_TIMEOUT is not a valid duration.
var ErrInvalidTimeout = errors.New("MOONRAKER_TIMEOUT must be a valid Go duration (e.g. 30s)")

// ErrInsecureHTTP is returned when the HTTP transport would bind to a
// non-loopback interface without an MCP_HTTP_TOKEN to authenticate requests.
var ErrInsecureHTTP = errors.New(
	"refusing to expose the unauthenticated HTTP transport on a non-loopback host; " +
		"set MCP_HTTP_TOKEN or bind MCP_HTTP_HOST to a loopback address")

// Config holds the application configuration loaded from environment variables.
type Config struct {
	// URL is the Moonraker base URL (scheme://host[:port]).
	URL string
	// APIKey, when set, is sent as the X-Api-Key header on every request.
	APIKey string
	// Token is a pre-obtained Bearer (JWT) token used instead of a login.
	Token string
	// Username and Password authenticate against the Moonraker /access/login
	// endpoint when password authentication (force_logins) is enabled.
	Username string
	Password string
	// TokenFile persists the JWT access/refresh tokens between runs.
	TokenFile string
	// EnableAdmin gates the destructive OS, service, update, and user-management
	// tools behind MOONRAKER_ENABLE_ADMIN.
	EnableAdmin bool
	// UserAgent overrides the default User-Agent.
	UserAgent string
	// Proxy is an optional HTTP/SOCKS5 proxy URL.
	Proxy string
	// Timeout bounds a single HTTP request/response cycle.
	Timeout time.Duration
	// HTTPPort and HTTPHost configure the optional HTTP transport.
	HTTPPort string
	HTTPHost string
	// HTTPToken, when set, is the Bearer token required on every HTTP request.
	HTTPToken string
}

// Load reads configuration from environment variables and validates it.
func Load() (*Config, error) {
	cfg := &Config{
		URL:         envOrDefault("MOONRAKER_URL", DefaultURL),
		APIKey:      os.Getenv("MOONRAKER_API_KEY"),
		Token:       os.Getenv("MOONRAKER_TOKEN"),
		Username:    os.Getenv("MOONRAKER_USERNAME"),
		Password:    os.Getenv("MOONRAKER_PASSWORD"),
		TokenFile:   resolveTokenFile(os.LookupEnv("MOONRAKER_TOKEN_FILE")),
		EnableAdmin: envBool("MOONRAKER_ENABLE_ADMIN"),
		UserAgent:   envOrDefault("MOONRAKER_USER_AGENT", defaultUserAgent),
		Proxy:       os.Getenv("MOONRAKER_PROXY"),
		HTTPPort:    os.Getenv("MCP_HTTP_PORT"),
		HTTPHost:    envOrDefault("MCP_HTTP_HOST", "127.0.0.1"),
		HTTPToken:   os.Getenv("MCP_HTTP_TOKEN"),
	}

	timeout, err := parseTimeout(os.Getenv("MOONRAKER_TIMEOUT"))
	if err != nil {
		return nil, err
	}

	cfg.Timeout = timeout

	validateErr := cfg.validate()
	if validateErr != nil {
		return nil, validateErr
	}

	return cfg, nil
}

// parseTimeout parses a Go duration, defaulting to defaultTimeout when unset.
func parseTimeout(raw string) (time.Duration, error) {
	if raw == "" {
		return defaultTimeout, nil
	}

	parsed, err := time.ParseDuration(raw)
	if err != nil || parsed <= 0 {
		return 0, ErrInvalidTimeout
	}

	return parsed, nil
}

// envOrDefault returns the environment value for key, or fallback when unset.
func envOrDefault(key, fallback string) string {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}

	return value
}

// envBool reports whether the environment value for key parses to true.
func envBool(key string) bool {
	parsed, err := strconv.ParseBool(os.Getenv(key))
	if err != nil {
		return false
	}

	return parsed
}

// resolveTokenFile returns the token cache path. When MOONRAKER_TOKEN_FILE is
// set (present is true) its value is honoured verbatim, including an empty
// string which explicitly disables the on-disk cache. When it is unset, it
// defaults to ~/.mcp-raker/token.json if a home directory is available.
func resolveTokenFile(configured string, present bool) string {
	if present {
		return configured
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}

	return filepath.Join(home, ".mcp-raker", "token.json")
}

// HasAuth reports whether any authentication method is configured. Moonraker on
// a trusted LAN often needs none; this only informs the client instructions.
func (c *Config) HasAuth() bool {
	return c.APIKey != "" || c.Token != "" || (c.Username != "" && c.Password != "")
}

// ProxyTransport builds an HTTP round-tripper honouring the configured proxy,
// or returns nil when no proxy is set.
func (c *Config) ProxyTransport() (http.RoundTripper, error) {
	if c.Proxy == "" {
		return nil, nil //nolint:nilnil // no proxy configured means no custom transport.
	}

	proxyURL, err := parseProxy(c.Proxy)
	if err != nil {
		return nil, err
	}

	// Clone the default transport so HTTP/2, connection pooling, and the
	// dial/TLS-handshake timeouts are preserved; only the proxy is overridden.
	transport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return &http.Transport{Proxy: http.ProxyURL(proxyURL)}, nil
	}

	cloned := transport.Clone()
	cloned.Proxy = http.ProxyURL(proxyURL)

	return cloned, nil
}

// parseProxy validates and parses a proxy URL, requiring a scheme and host.
func parseProxy(raw string) (*url.URL, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, ErrInvalidProxy
	}

	return parsed, nil
}

// HTTPEnabled reports whether the HTTP transport should be started.
func (c *Config) HTTPEnabled() bool {
	return c.HTTPPort != ""
}

// ValidateHTTP guards the unauthenticated HTTP transport. The MCP server has no
// per-request auth of its own and exposes write tools, so binding it to a
// non-loopback interface without a token would hand printer control to anyone
// who can reach the port. It is allowed only on a loopback host or with a token.
func (c *Config) ValidateHTTP() error {
	if !c.HTTPEnabled() || c.HTTPToken != "" || isLoopbackHost(c.HTTPHost) {
		return nil
	}

	return errors.Wrapf(ErrInsecureHTTP, "host %q", c.HTTPHost)
}

// isLoopbackHost reports whether host is the loopback interface.
func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}

	parsed := net.ParseIP(host)

	return parsed != nil && parsed.IsLoopback()
}

// HTTPAddr returns the host:port address for the HTTP server.
func (c *Config) HTTPAddr() string {
	return net.JoinHostPort(c.HTTPHost, c.HTTPPort)
}

// validate checks the URL, HTTP port, and proxy values.
func (c *Config) validate() error {
	parsed, err := url.Parse(c.URL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return ErrInvalidURL
	}

	if c.HTTPPort != "" {
		port, convErr := strconv.Atoi(c.HTTPPort)
		if convErr != nil || port < 1 || port > maxPort {
			return ErrInvalidHTTPPort
		}
	}

	if c.Proxy != "" {
		_, proxyErr := parseProxy(c.Proxy)
		if proxyErr != nil {
			return proxyErr
		}
	}

	return nil
}
