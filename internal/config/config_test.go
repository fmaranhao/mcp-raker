package config_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lexfrei/mcp-raker/internal/config"
)

const loopbackIP = "127.0.0.1"

func TestInvalidCredentialURLsAreNotEchoed(t *testing.T) {
	for _, key := range []string{"MOONRAKER_URL", "MOONRAKER_PROXY"} {
		t.Run(key, func(t *testing.T) {
			clearEnv(t)
			t.Setenv(key, "http://alice:REVIEW_FAKE_SECRET@host/%zz")
			_, err := config.Load()
			if err == nil {
				t.Fatal("invalid URL accepted")
			}
			if strings.Contains(err.Error(), "REVIEW_FAKE_SECRET") {
				t.Fatal("credential echoed in startup error")
			}
		})
	}
}

// clearEnv unsets every variable Load reads so a test starts from a clean slate.
func clearEnv(t *testing.T) {
	t.Helper()

	for _, key := range []string{
		"MOONRAKER_URL", "MOONRAKER_API_KEY", "MOONRAKER_TOKEN",
		"MOONRAKER_USERNAME", "MOONRAKER_PASSWORD", "MOONRAKER_TOKEN_FILE",
		"MOONRAKER_ENABLE_ADMIN", "MOONRAKER_USER_AGENT", "MOONRAKER_PROXY",
		"MOONRAKER_TIMEOUT", "MCP_HTTP_PORT", "MCP_HTTP_HOST", "MCP_HTTP_TOKEN",
	} {
		t.Setenv(key, "")
	}
}

func TestLoad_Defaults(t *testing.T) {
	clearEnv(t)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.URL != config.DefaultURL {
		t.Errorf("URL = %q, want %q", cfg.URL, config.DefaultURL)
	}

	if cfg.UserAgent != "mcp-raker" {
		t.Errorf("UserAgent = %q, want mcp-raker", cfg.UserAgent)
	}

	if cfg.Timeout != 30*time.Second {
		t.Errorf("Timeout = %s, want 30s", cfg.Timeout)
	}

	if cfg.EnableAdmin {
		t.Error("EnableAdmin = true, want false by default")
	}

	if cfg.HTTPHost != loopbackIP {
		t.Errorf("HTTPHost = %q, want %s", cfg.HTTPHost, loopbackIP)
	}
}

func TestLoad_EnableAdmin(t *testing.T) {
	clearEnv(t)
	t.Setenv("MOONRAKER_ENABLE_ADMIN", "true")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if !cfg.EnableAdmin {
		t.Error("EnableAdmin = false, want true")
	}
}

func TestLoad_InvalidURL(t *testing.T) {
	clearEnv(t)
	t.Setenv("MOONRAKER_URL", "://nope")

	_, err := config.Load()
	if !errors.Is(err, config.ErrInvalidURL) {
		t.Fatalf("err = %v, want ErrInvalidURL", err)
	}
}

func TestLoad_InvalidTimeout(t *testing.T) {
	clearEnv(t)
	t.Setenv("MOONRAKER_TIMEOUT", "soon")

	_, err := config.Load()
	if !errors.Is(err, config.ErrInvalidTimeout) {
		t.Fatalf("err = %v, want ErrInvalidTimeout", err)
	}
}

func TestLoad_InvalidPort(t *testing.T) {
	clearEnv(t)
	t.Setenv("MCP_HTTP_PORT", "99999")

	_, err := config.Load()
	if !errors.Is(err, config.ErrInvalidHTTPPort) {
		t.Fatalf("err = %v, want ErrInvalidHTTPPort", err)
	}
}

func TestLoad_InvalidProxy(t *testing.T) {
	clearEnv(t)
	t.Setenv("MOONRAKER_PROXY", "not a url")

	_, err := config.Load()
	if !errors.Is(err, config.ErrInvalidProxy) {
		t.Fatalf("err = %v, want ErrInvalidProxy", err)
	}
}

func TestHasAuth(t *testing.T) {
	tests := []struct {
		name string
		cfg  config.Config
		want bool
	}{
		{name: "none", cfg: config.Config{}, want: false},
		{name: "api key", cfg: config.Config{APIKey: "k"}, want: true},
		{name: "token", cfg: config.Config{Token: "t"}, want: true},
		{name: "user only", cfg: config.Config{Username: "u"}, want: false},
		{name: "user and pass", cfg: config.Config{Username: "u", Password: "p"}, want: true},
	}

	t.Parallel()

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			if got := testCase.cfg.HasAuth(); got != testCase.want {
				t.Errorf("HasAuth() = %v, want %v", got, testCase.want)
			}
		})
	}
}

func TestValidateHTTP(t *testing.T) {
	tests := []struct {
		name    string
		cfg     config.Config
		wantErr bool
	}{
		{name: "disabled", cfg: config.Config{}, wantErr: false},
		{name: "loopback no token", cfg: config.Config{HTTPPort: "8080", HTTPHost: loopbackIP}, wantErr: false},
		{name: "localhost no token", cfg: config.Config{HTTPPort: "8080", HTTPHost: "localhost"}, wantErr: false},
		{name: "public no token", cfg: config.Config{HTTPPort: "8080", HTTPHost: "0.0.0.0"}, wantErr: true},
		{name: "public with token", cfg: config.Config{HTTPPort: "8080", HTTPHost: "0.0.0.0", HTTPToken: "t"}, wantErr: false},
	}

	t.Parallel()

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			err := testCase.cfg.ValidateHTTP()
			if testCase.wantErr && err == nil {
				t.Error("ValidateHTTP() = nil, want error")
			}

			if !testCase.wantErr && err != nil {
				t.Errorf("ValidateHTTP() = %v, want nil", err)
			}
		})
	}
}

func TestProxyTransport(t *testing.T) {
	t.Parallel()

	none := config.Config{}

	transport, err := none.ProxyTransport()
	if err != nil || transport != nil {
		t.Errorf("ProxyTransport() = (%v, %v), want (nil, nil)", transport, err)
	}

	withProxy := config.Config{Proxy: "http://proxy.example:3128"}

	transport, err = withProxy.ProxyTransport()
	if err != nil || transport == nil {
		t.Errorf("ProxyTransport() = (%v, %v), want non-nil transport", transport, err)
	}
}

func TestHTTPAddr(t *testing.T) {
	t.Parallel()

	cfg := config.Config{HTTPHost: "127.0.0.1", HTTPPort: "7150"}
	if got := cfg.HTTPAddr(); got != "127.0.0.1:7150" {
		t.Errorf("HTTPAddr() = %q, want 127.0.0.1:7150", got)
	}
}

func TestLoad_TokenFileExplicitEmptyDisablesCache(t *testing.T) {
	clearEnv(t)
	t.Setenv("MOONRAKER_TOKEN_FILE", "")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.TokenFile != "" {
		t.Errorf("TokenFile = %q, want empty (explicit empty disables the cache)", cfg.TokenFile)
	}
}

func TestLoad_TokenFileExplicitPath(t *testing.T) {
	clearEnv(t)
	t.Setenv("MOONRAKER_TOKEN_FILE", "/tmp/raker-token.json")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.TokenFile != "/tmp/raker-token.json" {
		t.Errorf("TokenFile = %q, want /tmp/raker-token.json", cfg.TokenFile)
	}
}

func TestLoad_TokenFileDefaultWhenUnset(t *testing.T) {
	clearEnv(t)
	// clearEnv blanks the var (and registers its restore); genuinely unset it so
	// LookupEnv reports it as absent and the default path branch is exercised.
	_ = os.Unsetenv("MOONRAKER_TOKEN_FILE")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	wantSuffix := filepath.Join(".mcp-raker", "token.json")
	if !strings.HasSuffix(cfg.TokenFile, wantSuffix) {
		t.Errorf("TokenFile = %q, want a path ending in %q", cfg.TokenFile, wantSuffix)
	}
}
