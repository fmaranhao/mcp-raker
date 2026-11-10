package moonraker

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
)

func TestReadOnlyPolicy(t *testing.T) {
	tests := []struct {
		method, path string
		q            url.Values
		allow        bool
	}{
		{"GET", "/printer/info", nil, true},
		{"GET", "/printer/objects/query", url.Values{"mmu": {"enabled,tool"}}, true},
		{"GET", "/server/files/list", url.Values{"root": {"logs"}}, true},
		{"GET", "/server/files/directory", url.Values{"path": {"config/mmu/base"}}, true},
		{"GET", "/server/files/config/mmu/base/mmu_parameters.cfg", nil, true},
		{"GET", "/server/files/logs/klippy.log", nil, true},
		{"POST", "/printer/info", nil, false},
		{"DELETE", "/server/files/config/printer.cfg", nil, false},
		{"PUT", "/server/files/config/printer.cfg", nil, false},
		{"GET", "/printer/gcode/script", url.Values{"script": {"G28"}}, false},
		{"GET", "/printer/emergency_stop", nil, false},
		{"GET", "/machine/device_power/device", url.Values{"device": {"printer"}, "action": {"off"}}, false},
		{"GET", "/machine/update/status", url.Values{"refresh": {"true"}}, false},
		{"GET", "/access/api_key", nil, false},
		{"GET", "/websocket", nil, false},
		{"GET", "/server/jsonrpc", nil, false},
		{"GET", "/new/upstream/endpoint", nil, false},
		{"GET", "/printer/info", url.Values{"script": {"G28"}}, false},
		{"GET", "/server/files/list", url.Values{"root": {"config", "database"}}, false},
		{"GET", "/server/files/list", url.Values{"root": {"database"}}, false},
		{"GET", "/server/files/directory", url.Values{"path": {"config/../../etc"}}, false},
		{"GET", "/server/files/metadata", url.Values{"filename": {"../../etc/passwd"}}, false},
		{"GET", "/server/files/config/../../etc/passwd", nil, false},
		{"GET", "/server/files/config/%2e%2e/secret", nil, false},
		{"GET", "/server/files/config/%252e%252e/secret", nil, false},
		{"GET", "/server/files/config/a%2f..%2fsecret", nil, false},
		{"GET", "/server/files/config/a\\..\\secret", nil, false},
		{"GET", "//other.invalid/printer/info", nil, false},
	}
	for _, tc := range tests {
		t.Run(fmt.Sprint(tc.method, tc.path, tc.q), func(t *testing.T) {
			err := validateReadRequest(tc.method, tc.path, tc.q)
			if (err == nil) != tc.allow {
				t.Fatalf("allow=%v error=%v", tc.allow, err)
			}
		})
	}
}

func TestReadOnlyClientBlocksBeforeNetwork(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1); fmt.Fprint(w, `{"result":{}}`) }))
	defer srv.Close()
	c, err := New(&Options{BaseURL: srv.URL, ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = c.Post(t.Context(), "/printer/gcode/script", nil, map[string]string{"script": "G28"})
	_, _ = c.Delete(t.Context(), "/server/files/config/printer.cfg", nil)
	_, _ = c.Get(t.Context(), "/printer/gcode/script", nil)
	_, _ = c.Upload(t.Context(), &UploadOptions{Filename: "x.gcode", Content: []byte("G28")})
	if hits.Load() != 0 {
		t.Fatalf("forbidden operations reached Moonraker: %d", hits.Load())
	}
	_, err = c.Get(t.Context(), "/printer/info", nil)
	if err != nil {
		t.Fatal(err)
	}
	if hits.Load() != 1 {
		t.Fatalf("safe read hits=%d", hits.Load())
	}
}

func TestReadOnlyRedirectNeverFollowed(t *testing.T) {
	var hits atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1); fmt.Fprint(w, `{"result":{}}`) }))
	defer target.Close()
	for _, destination := range []string{target.URL + "/printer/info", "/printer/gcode/script?script=G28"} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, destination, http.StatusFound) }))
		c, err := New(&Options{BaseURL: srv.URL, ReadOnly: true})
		if err != nil {
			t.Fatal(err)
		}
		_, err = c.Get(t.Context(), "/printer/info", nil)
		if err == nil {
			t.Fatal("redirect accepted")
		}
		srv.Close()
	}
	if hits.Load() != 0 {
		t.Fatal("redirect destination contacted")
	}
}

func TestReadOnlyRejectsLoginAndURLTricks(t *testing.T) {
	for _, opts := range []*Options{
		{BaseURL: "http://host/base", ReadOnly: true},
		{BaseURL: "http://host?x=y", ReadOnly: true},
		{BaseURL: "http://user:secret@host", ReadOnly: true},
		{BaseURL: "http://host#fragment", ReadOnly: true},
		{BaseURL: "http://host", Username: "user", Password: "secret", ReadOnly: true},
	} {
		_, err := New(opts)
		if err == nil {
			t.Fatal("unsafe config accepted")
		}
	}
}

func TestReadOnlyFileRangesBounded(t *testing.T) {
	for _, tc := range []struct {
		rangeHeader string
		size        int
		fail        bool
	}{
		{"bytes=-65536", 65536, false},
		{"bytes=-65536", 300000, true},
	} {
		t.Run(fmt.Sprint(tc.size), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/server/files/logs/klippy.log" || r.Header.Get("Range") != tc.rangeHeader {
					t.Errorf("unexpected request %s range=%s", r.URL, r.Header.Get("Range"))
				}
				w.Header().Set("Content-Range", "bytes 100-65635/65636")
				w.WriteHeader(http.StatusPartialContent)
				fmt.Fprint(w, strings.Repeat("x", tc.size))
			}))
			defer srv.Close()
			c, err := New(&Options{BaseURL: srv.URL, ReadOnly: true})
			if err != nil {
				t.Fatal(err)
			}
			data, err := c.ReadFile(context.Background(), "logs", "klippy.log", 0, 65536, true)
			if (err != nil) != tc.fail {
				t.Fatalf("unexpected range result len=%d err=%v", len(data), err)
			}
		})
	}
}

func TestReadOnlyJSONResponseBound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, strings.Repeat("x", 5<<20)) }))
	defer srv.Close()
	c, err := New(&Options{BaseURL: srv.URL, ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Get(t.Context(), "/printer/info", nil)
	if err == nil {
		t.Fatal("unbounded response accepted")
	}
}

func TestReadOnlyRejectsIncorrectContentRange(t *testing.T) {
	for _, header := range []string{"", "bytes 0-2/100", "bytes 97-99/*", "bytes 97-99/100 garbage"} {
		t.Run(header, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Range", header)
				w.WriteHeader(http.StatusPartialContent)
				fmt.Fprint(w, "abc")
			}))
			defer srv.Close()
			c, err := New(&Options{BaseURL: srv.URL, ReadOnly: true})
			if err != nil {
				t.Fatal(err)
			}
			_, err = c.ReadFile(t.Context(), "logs", "klippy.log", 0, 3, true)
			if err == nil {
				t.Fatal("incorrect suffix range accepted")
			}
		})
	}
}
