package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/lexfrei/mcp-raker/internal/config"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// OpenAI rejects parameter schemas that omit properties, even for no-arg tools.
// Check the advertised wire representation, rather than the Go schema value.
func TestDiagnosticInputSchemasHaveProperties(t *testing.T) {
	for name, tool := range listTools(t, false) {
		raw, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatal(err)
		}
		var schema map[string]json.RawMessage
		if err = json.Unmarshal(raw, &schema); err != nil {
			t.Fatal(err)
		}
		if properties, ok := schema["properties"]; !ok || string(properties) == "null" {
			t.Errorf("%s: input schema lacks properties object: %s", name, raw)
		}
	}
}

func TestDiagnosticCatalogueCannotEnableOperations(t *testing.T) {
	for _, admin := range []bool{false, true} {
		catalogue := listToolNames(t, admin)
		want := []string{"moonraker_mcp_version", "moonraker_server_info", "moonraker_printer_info", "moonraker_objects_list", "moonraker_objects_query", "moonraker_temperature_store", "moonraker_gcode_store", "moonraker_gcode_help", "moonraker_files_list", "moonraker_files_directory", "moonraker_files_roots", "moonraker_files_metadata", "moonraker_files_download", "moonraker_history_list", "moonraker_history_totals", "moonraker_history_job", "moonraker_jobqueue_status", "moonraker_system_info", "moonraker_proc_stats", "moonraker_sensors_list", "moonraker_sensors_info", "moonraker_sensors_measurements"}
		if len(catalogue) != len(want) {
			t.Fatalf("unexpected catalogue size %d", len(catalogue))
		}
		for _, name := range want {
			if !catalogue[name] {
				t.Errorf("missing reviewed tool %s", name)
			}
		}
		for _, name := range []string{"moonraker_printer_info", "moonraker_objects_query", "moonraker_files_download", "moonraker_gcode_store", "moonraker_history_list"} {
			if !catalogue[name] {
				t.Errorf("missing diagnostic %s", name)
			}
		}
		for _, name := range []string{"moonraker_gcode_script", "moonraker_emergency_stop", "moonraker_print_start", "moonraker_power_off", "moonraker_files_upload", "moonraker_files_delete", "moonraker_access_api_key", "moonraker_mqtt_publish", "moonraker_extensions_request", "moonraker_machine_shutdown"} {
			if catalogue[name] {
				t.Errorf("operational/sensitive tool advertised: %s", name)
			}
		}
	}
}

type authRoundTripper struct{ token string }

func TestDiagnosticHTTPModernDiscovery(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: serverName, Version: "test"}, newServerOptions(testLogger(), false))
	// Version/catalogue operations do not contact Moonraker.
	registerTools(server, nil, false)
	httpServer := httptest.NewServer(mcpHTTPHandler(server, "test-token"))
	defer httpServer.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "modern-test", Version: "test"}, nil)
	session, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{
		Endpoint: httpServer.URL + "/mcp", HTTPClient: &http.Client{Transport: authRoundTripper{"test-token"}},
		DisableStandaloneSSE: true,
	}, &mcp.ClientSessionOptions{ProtocolVersion: "2026-07-28"})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if got := session.InitializeResult().ProtocolVersion; got != "2026-07-28" {
		t.Fatalf("modern discovery negotiated %s, want 2026-07-28", got)
	}
	catalogue, err := session.ListTools(t.Context(), nil)
	if err != nil || len(catalogue.Tools) != 22 {
		t.Fatalf("modern catalogue: %v, %v", catalogue, err)
	}
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "moonraker_mcp_version", Arguments: map[string]any{}})
	if err != nil || result.IsError {
		t.Fatalf("modern version read: %v, %v", result, err)
	}
	result, err = session.CallTool(t.Context(), &mcp.CallToolParams{Name: "moonraker_gcode_script", Arguments: map[string]any{"script": "G28"}})
	if err == nil && !result.IsError {
		t.Fatal("modern transport exposed printer operations")
	}
}

func (a authRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+a.token)
	resp, err := http.DefaultTransport.RoundTrip(r)
	if err != nil {
		return resp, fmt.Errorf("test transport: %w", err)
	}
	return resp, nil
}

func TestDiagnosticHTTPMCPReadAndDeniedCall(t *testing.T) {
	var hits atomic.Int64
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.Method != http.MethodGet {
			t.Errorf("backend write %s", r.Method)
		}
		if r.URL.Path == "/server/files/logs/klippy.log" {
			if r.Header.Get("Range") != "bytes=-65536" {
				t.Errorf("range=%s", r.Header.Get("Range"))
			}
			w.Header().Set("Content-Range", "bytes 0-20/21")
			w.WriteHeader(http.StatusPartialContent)
			fmt.Fprint(w, "historical MMU error\n")
			return
		}
		if r.URL.Path != "/printer/objects/query" || r.URL.Query().Get("mmu") != "enabled,tool" {
			t.Errorf("unexpected backend request %s", r.URL)
		}
		fmt.Fprint(w, `{"result":{"eventtime":1,"status":{"mmu":{"enabled":true,"tool":2}}}}`)
	}))
	defer backend.Close()
	api, err := newDiagnosticClient(&config.Config{URL: backend.URL}, testLogger(), nil)
	if err != nil {
		t.Fatal(err)
	}
	// A direct client mutation is denied independently of the MCP catalogue.
	if _, err = api.Post(t.Context(), "/printer/gcode/script", nil, nil); err == nil {
		t.Fatal("production factory permitted write")
	}
	server := mcp.NewServer(&mcp.Implementation{Name: serverName, Version: "test"}, newServerOptions(testLogger(), false))
	registerTools(server, api, false)
	httpServer := httptest.NewServer(mcpHTTPHandler(server, "test-token"))
	defer httpServer.Close()
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, httpServer.URL+"/mcp", strings.NewReader(`{}`))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status %d", resp.StatusCode)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "test"}, nil)
	session, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: httpServer.URL + "/mcp", HTTPClient: &http.Client{Transport: authRoundTripper{"test-token"}}, DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "moonraker_objects_query", Arguments: map[string]any{"objects": map[string][]string{"mmu": {"enabled", "tool"}}}})
	if err != nil || result.IsError {
		t.Fatalf("MMU read %v %v", result, err)
	}
	result, err = session.CallTool(t.Context(), &mcp.CallToolParams{Name: "moonraker_files_download", Arguments: map[string]any{"root": "logs", "filename": "klippy.log"}})
	if err != nil || result.IsError {
		t.Fatalf("log read %v %v", result, err)
	}
	for _, name := range []string{"moonraker_gcode_script", "moonraker_emergency_stop", "moonraker_files_upload", "moonraker_extensions_request"} {
		result, err = session.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: map[string]any{"script": "G28"}})
		if err == nil && !result.IsError {
			t.Fatalf("forbidden call succeeded %s", name)
		}
	}
	if hits.Load() != 2 {
		t.Fatalf("unexpected requests reached backend: %d", hits.Load())
	}
}

func TestMCPHTTPMetadataRoutesAreAbsent(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: serverName, Version: "test"}, nil)
	handler := mcpHTTPHandler(server, "test-token")
	for _, path := range []string{"/.well-known/oauth-protected-resource", "/health", "/printer/gcode/script"} {
		req := httptest.NewRequestWithContext(t.Context(), "GET", path, nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != 404 {
			t.Errorf("non-MCP path %s returned %d, want404", path, rec.Code)
		}
	}
}
