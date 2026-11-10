// Command mcp-raker is an MCP server exposing the Moonraker 3D-printer API
// (the web server that fronts the Klipper firmware) over stdio and, optionally,
// HTTP.
package main

import (
	"context"
	"crypto/subtle"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"runtime/debug"
	"syscall"
	"time"

	"github.com/cockroachdb/errors"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/sync/errgroup"

	"github.com/lexfrei/mcp-raker/internal/config"
	"github.com/lexfrei/mcp-raker/internal/moonraker"
	"github.com/lexfrei/mcp-raker/internal/tools"
)

const (
	serverName        = "mcp-raker-readonly"
	readHeaderTimeout = 10 * time.Second
	shutdownTimeout   = 5 * time.Second
)

// defaultVersion and defaultRevision are the unstamped placeholders. resolveBuild
// treats version == defaultVersion as "not stamped by ldflags" and falls back to
// the embedded build info.
const (
	defaultVersion  = "dev"
	defaultRevision = "unknown"
)

// version and revision are set via ldflags at build time (container builds). For
// `go install` and `go build` from a checkout, resolveBuild fills them in from
// the embedded build info instead.
var (
	version  = defaultVersion
	revision = defaultRevision
)

func main() {
	info, ok := debug.ReadBuildInfo()
	version, revision = resolveBuild(version, revision, info, ok)

	logger := newLogger()

	err := run(logger)
	if err != nil {
		logger.Error("server failed", slog.Any("error", err))
		os.Exit(1)
	}
}

// resolveBuild determines the version and revision to report. ldflags-set values
// (container builds) win. Otherwise it falls back to the embedded build info, so
// a `go install module@vX.Y.Z` reports vX.Y.Z and a `go build` from a checkout
// reports the VCS commit (suffixed -dirty when the tree had uncommitted edits).
func resolveBuild(version, revision string, info *debug.BuildInfo, ok bool) (string, string) {
	if version != defaultVersion {
		return version, revision
	}

	if !ok || info == nil {
		return version, revision
	}

	if info.Main.Version != "" && info.Main.Version != "(devel)" {
		version = info.Main.Version
	}

	gotRevision := false
	modified := false

	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			if setting.Value != "" {
				revision = shortRevision(setting.Value)
				gotRevision = true
			}
		case "vcs.modified":
			modified = setting.Value == "true"
		}
	}

	if gotRevision && modified {
		revision += "-dirty"
	}

	return version, revision
}

// shortRevision trims a git commit hash to a readable prefix.
func shortRevision(rev string) string {
	const shortLen = 12

	if len(rev) > shortLen {
		return rev[:shortLen]
	}

	return rev
}

// newLogger builds the structured JSON logger. Logs go to stderr because stdout
// carries the JSON-RPC stream.
func newLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
}

func run(logger *slog.Logger) error {
	cfg, cfgErr := config.Load()
	if cfgErr != nil {
		return errors.Wrap(cfgErr, "invalid configuration")
	}

	httpErr := cfg.ValidateHTTP()
	if httpErr != nil {
		return errors.Wrap(httpErr, "invalid HTTP configuration")
	}

	if cfg.EnableAdmin {
		return errors.New("operational/admin tools are disabled in the read-only fork")
	}

	transport, transportErr := cfg.ProxyTransport()
	if transportErr != nil {
		return errors.Wrap(transportErr, "invalid proxy configuration")
	}

	client, clientErr := newDiagnosticClient(cfg, logger, transport)
	if clientErr != nil {
		return errors.Wrap(clientErr, "failed to create moonraker client")
	}

	server := mcp.NewServer(
		&mcp.Implementation{
			Name:    serverName,
			Version: version + "+" + revision,
		},
		newServerOptions(logger, cfg.EnableAdmin),
	)

	registerTools(server, client, cfg.EnableAdmin)

	logger.Info("starting server",
		slog.String("url", cfg.URL),
		slog.Bool("admin", cfg.EnableAdmin),
		slog.Bool("authenticated", cfg.HasAuth()))

	return serve(logger, server, cfg)
}

func newDiagnosticClient(cfg *config.Config, logger *slog.Logger, transport http.RoundTripper) (*moonraker.Client, error) {
	client, err := moonraker.New(&moonraker.Options{
		ReadOnly: true, BaseURL: cfg.URL, APIKey: cfg.APIKey, Token: cfg.Token,
		Username: cfg.Username, Password: cfg.Password, TokenPath: cfg.TokenFile,
		UserAgent: cfg.UserAgent, Timeout: cfg.Timeout, Transport: transport, Logger: logger,
	})

	return client, errors.Wrap(err, "initialize read-only client")
}

// newServerOptions wires the shared logger into the MCP server and describes the
// available tools and configuration surfaced to clients.
func newServerOptions(logger *slog.Logger, _ bool) *mcp.ServerOptions {
	return &mcp.ServerOptions{
		Instructions: "Read-only Moonraker diagnostics. Query printer/MMU objects, temperatures, history and system state. Read config, gcodes and logs in Moonraker-registered printer_data roots; file data may contain secrets. No G-code execution, operational controls, writes or generic API forwarding are available. Backend request allowlisting is always enforced.",
		Logger:       logger,
	}
}

// registerTools is the reviewed diagnostic catalogue. The retained argument
// keeps upstream tests/call sites compatible; it can never enable operations.
func registerTools(server *mcp.Server, api moonraker.API, _ bool) {
	addDiagnosticTool(server, tools.MCPVersionTool(), tools.NewMCPVersionHandler(version, revision, runtime.Version()))
	addDiagnosticTool(server, tools.ServerInfoTool(), tools.NewServerInfoHandler(api))
	addDiagnosticTool(server, tools.PrinterInfoTool(), tools.NewPrinterInfoHandler(api))
	addDiagnosticTool(server, tools.ObjectsListTool(), tools.NewObjectsListHandler(api))
	addDiagnosticTool(server, tools.ObjectsQueryTool(), tools.NewObjectsQueryHandler(api))
	addDiagnosticTool(server, tools.TemperatureStoreTool(), tools.NewTemperatureStoreHandler(api))
	addDiagnosticTool(server, tools.GcodeStoreTool(), tools.NewGcodeStoreHandler(api))
	addDiagnosticTool(server, tools.GcodeHelpTool(), tools.NewGcodeHelpHandler(api))
	addDiagnosticTool(server, tools.FilesListTool(), tools.NewFilesListHandler(api))
	addDiagnosticTool(server, tools.FilesDirectoryTool(), tools.NewFilesDirectoryHandler(api))
	addDiagnosticTool(server, tools.FilesRootsTool(), tools.NewFilesRootsHandler(api))
	addDiagnosticTool(server, tools.FilesMetadataTool(), tools.NewFilesMetadataHandler(api))
	addDiagnosticTool(server, diagnosticFileTool(), diagnosticFileHandler(api))
	addDiagnosticTool(server, tools.HistoryListTool(), tools.NewHistoryListHandler(api))
	addDiagnosticTool(server, tools.HistoryTotalsTool(), tools.NewHistoryTotalsHandler(api))
	addDiagnosticTool(server, tools.HistoryJobTool(), tools.NewHistoryJobHandler(api))
	addDiagnosticTool(server, tools.JobQueueStatusTool(), tools.NewJobQueueStatusHandler(api))
	addDiagnosticTool(server, tools.SystemInfoTool(), tools.NewSystemInfoHandler(api))
	addDiagnosticTool(server, tools.ProcStatsTool(), tools.NewProcStatsHandler(api))
	addDiagnosticTool(server, tools.SensorsListTool(), tools.NewSensorsListHandler(api))
	addDiagnosticTool(server, tools.SensorsInfoTool(), tools.NewSensorsInfoHandler(api))
	addDiagnosticTool(server, tools.SensorsMeasurementsTool(), tools.NewSensorsMeasurementsHandler(api))
}

// serve runs the stdio transport and, when configured, an HTTP transport.
func serve(logger *slog.Logger, server *mcp.Server, cfg *config.Config) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		select {
		case <-sigChan:
			cancel()
		case <-ctx.Done():
		}

		signal.Stop(sigChan)
	}()

	group, groupCtx := errgroup.WithContext(ctx)
	httpEnabled := cfg.HTTPEnabled()

	group.Go(func() error {
		runErr := server.Run(groupCtx, &mcp.StdioTransport{})
		if runErr != nil && groupCtx.Err() == nil {
			return errors.Wrap(runErr, "stdio server failed")
		}

		if !httpEnabled {
			cancel()
		}

		return nil
	})

	if httpEnabled {
		group.Go(func() error {
			return runHTTPServer(groupCtx, logger, server, cfg.HTTPAddr(), cfg.HTTPToken)
		})
	}

	//nolint:wrapcheck // errors are already wrapped inside the group goroutines.
	return group.Wait()
}

// runHTTPServer starts an HTTP transport for the MCP server. Sharing a single
// *mcp.Server across transports is safe: the SDK guards internal state with a
// mutex. When token is set, every request must carry a matching Bearer token;
// otherwise config validation has confined the transport to a loopback host.
func runHTTPServer(ctx context.Context, logger *slog.Logger, server *mcp.Server, addr, token string) error {
	handler := mcpHTTPHandler(server, token)

	httpServer := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: readHeaderTimeout,
	}

	//nolint:gosec // G118: shutdown uses a fresh context because ctx is already cancelled.
	go func() {
		<-ctx.Done()

		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer shutdownCancel()

		shutdownErr := httpServer.Shutdown(shutdownCtx) //nolint:contextcheck // fresh context for graceful shutdown.
		if shutdownErr != nil {
			logger.Error("http server shutdown failed", slog.Any("error", shutdownErr))
		}
	}()

	logger.Info("http server listening", slog.String("addr", addr))

	listenErr := httpServer.ListenAndServe()
	if errors.Is(listenErr, http.ErrServerClosed) {
		return nil
	}

	return errors.Wrap(listenErr, "HTTP listen failed")
}

func mcpHTTPHandler(server *mcp.Server, token string) http.Handler {
	mux := http.NewServeMux()
	// Modern MCP discovery requires sessionless HTTP in the Go SDK. The
	// diagnostic tools keep no client session state; legacy clients also work.
	mux.Handle("/mcp", bearerAuth(mcp.NewStreamableHTTPHandler(func(_ *http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true}), token))

	return mux
}

// bearerAuth wraps next so every request must present a matching
// "Authorization: Bearer <token>" header. An empty token disables the check;
// config validation has already confined that case to a loopback host.
func bearerAuth(next http.Handler, token string) http.Handler {
	if token == "" {
		return next
	}

	want := []byte("Bearer " + token)

	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		got := []byte(request.Header.Get("Authorization"))
		if subtle.ConstantTimeCompare(got, want) != 1 {
			http.Error(writer, "unauthorized", http.StatusUnauthorized)

			return
		}

		next.ServeHTTP(writer, request)
	})
}
