package moonraker

import (
	"context"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode"

	"github.com/cockroachdb/errors"
)

const (
	maxReadResponseBytes = 4 << 20
	MaxFileReadBytes     = 256 << 10
)

var ErrReadOnly = errors.New("request denied by read-only Moonraker policy")

// This is an operation allowlist, not a prefix/HTTP-verb heuristic. In
// particular Moonraker has GET routes which can execute G-code or change power.
//
//nolint:gochecknoglobals // Fixed, unexported operation allowlist; never modified at runtime.
var readRoutes = map[string]string{
	"/server/info":                  "",
	"/printer/info":                 "",
	"/printer/objects/list":         "",
	"/printer/objects/query":        "*objects*",
	"/printer/gcode/help":           "",
	"/server/temperature_store":     "include_monitors",
	"/server/gcode_store":           "count",
	"/server/history/list":          "limit,start,before,since,order",
	"/server/history/totals":        "",
	"/server/history/job":           "uid",
	"/server/job_queue/status":      "",
	"/machine/system_info":          "",
	"/machine/proc_stats":           "",
	"/machine/sensors/list":         "extended",
	"/machine/sensors/info":         "sensor",
	"/machine/sensors/measurements": "sensor",
	"/server/files/roots":           "",
	"/server/files/list":            "root",
	"/server/files/directory":       "path,extended",
	"/server/files/metadata":        "filename",
}

func safeRelative(value string) bool {
	if value == "" {
		return false
	}

	if strings.ContainsAny(value, "\\%?#") || strings.HasPrefix(value, "/") {
		return false
	}

	for part := range strings.SplitSeq(value, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}

		for _, r := range part {
			if unicode.IsControl(r) {
				return false
			}
		}
	}

	return true
}

func fileRoot(value string) bool { return value == "config" || value == "gcodes" || value == "logs" }

func rootedPath(value string) bool {
	if !safeRelative(value) {
		return false
	}

	return fileRoot(strings.SplitN(value, "/", 2)[0])
}

func validateReadRequest(method, path string, query url.Values) error {
	if method != http.MethodGet || !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") {
		return ErrReadOnly
	}

	decoded, err := url.PathUnescape(path)
	if err != nil || !safeRelative(strings.TrimPrefix(decoded, "/")) {
		return ErrReadOnly
	}
	// Reject escaped separators even when they would normalize to a safe path.
	lower := strings.ToLower(path)
	if strings.Contains(lower, "%2f") || strings.Contains(lower, "%5c") {
		return ErrReadOnly
	}

	keys, exists := readRoutes[decoded]
	if !exists {
		if !strings.HasPrefix(decoded, "/server/files/") || len(query) != 0 {
			return ErrReadOnly
		}

		parts := strings.SplitN(strings.TrimPrefix(decoded, "/server/files/"), "/", 2)
		if len(parts) != 2 || !fileRoot(parts[0]) || !safeRelative(parts[1]) {
			return ErrReadOnly
		}

		return nil
	}

	queryErr := validateReadQuery(keys, query)
	if queryErr != nil {
		return queryErr
	}

	return validateFileQuery(decoded, query)
}

func validateFileQuery(decoded string, query url.Values) error {
	switch decoded {
	case "/server/files/list":
		if root := query.Get("root"); root != "" && !fileRoot(root) {
			return ErrReadOnly
		}
	case "/server/files/directory":
		if path := query.Get("path"); path != "" && !rootedPath(path) {
			return ErrReadOnly
		}
	case "/server/files/metadata":
		if !safeRelative(query.Get("filename")) {
			return ErrReadOnly
		}
	}

	return nil
}

func validateReadQuery(keys string, query url.Values) error {
	for key, values := range query {
		if len(values) != 1 || len(key) > 256 || len(values[0]) > 4096 {
			return ErrReadOnly
		}

		if keys == "*objects*" {
			if !safeRelative(key) || strings.Contains(key, "/") {
				return ErrReadOnly
			}

			continue
		}

		allowed := false

		for name := range strings.SplitSeq(keys, ",") {
			if key == name && name != "" {
				allowed = true
			}
		}

		if !allowed {
			return ErrReadOnly
		}
	}

	return nil
}

type readOnlyTransport struct {
	origin *url.URL
	next   http.RoundTripper
}

func (t *readOnlyTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme != t.origin.Scheme || req.URL.Host != t.origin.Host || req.URL.User != nil || req.URL.Fragment != "" {
		return nil, ErrReadOnly
	}

	query, err := url.ParseQuery(req.URL.RawQuery)
	if err != nil {
		return nil, ErrReadOnly
	}

	err = validateReadRequest(req.Method, req.URL.EscapedPath(), query)
	if err != nil {
		return nil, err
	}

	if req.ContentLength > 0 || req.Header.Get("Upgrade") != "" || req.Header.Get("X-Http-Method-Override") != "" {
		return nil, ErrReadOnly
	}

	resp, roundErr := t.next.RoundTrip(req)

	return resp, errors.Wrap(roundErr, "read-only transport")
}

func (c *Client) enforceReadOnly(opts *Options) error {
	origin, err := url.Parse(c.baseURL)
	if err != nil || (origin.Scheme != "http" && origin.Scheme != "https") || origin.Host == "" || origin.User != nil || origin.RawQuery != "" || origin.Fragment != "" || origin.Path != "" {
		return errors.New("read-only Moonraker URL must be an http(s) origin without credentials, path, query or fragment")
	}

	if opts.Username != "" || opts.Password != "" {
		return errors.New("read-only fork accepts API key/static token or trusted-LAN auth, not username/password login")
	}

	next := opts.Transport
	if next == nil {
		// Do not silently send printer credentials through an environment proxy.
		defaultTransport, ok := http.DefaultTransport.(*http.Transport)
		if !ok {
			return errors.New("unsupported default transport")
		}

		transport := defaultTransport.Clone()
		transport.Proxy = nil
		next = transport
	}

	httpClient, ok := c.http.(*http.Client)
	if !ok {
		return errors.New("unsupported HTTP client")
	}

	c.http = &http.Client{
		Timeout:       httpClient.Timeout,
		Transport:     &readOnlyTransport{origin: origin, next: next},
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return ErrReadOnly },
	}

	return nil
}

// ReadFile reads a bounded slice or tail from a Moonraker-registered printer_data
// root. It never mounts the printer filesystem or accepts an arbitrary URL.
func (c *Client) ReadFile(ctx context.Context, root, filename string, offset, limit int64, tail bool) ([]byte, error) {
	if !validReadFileOptions(c.readOnly, root, filename, offset, limit, tail) {
		return nil, ErrReadOnly
	}

	parts := strings.Split(filename, "/")
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}

	path := "/server/files/" + root + "/" + strings.Join(parts, "/")

	policyErr := validateReadRequest(http.MethodGet, path, nil)
	if policyErr != nil {
		return nil, policyErr
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, http.NoBody)
	if err != nil {
		return nil, errors.New("invalid file request")
	}

	req.Header.Set("Range", fileRangeHeader(offset, limit, tail))

	req.Header.Set("User-Agent", c.userAgent)
	c.attachAuth(req)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, errors.New("file read failed (connection, redirect or read-only policy)")
	}

	defer func() { _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusOK:
		if offset != 0 {
			return nil, errors.New("Moonraker ignored requested file offset")
		}
	case http.StatusPartialContent:
	default:
		return nil, errors.Wrapf(ErrAPI, "file read HTTP status %d", resp.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, errors.New("file response read failed")
	}

	if int64(len(data)) > limit {
		return nil, errors.New("file response exceeds requested limit; upstream may have ignored Range")
	}

	if resp.StatusCode == http.StatusPartialContent {
		rangeErr := validateContentRange(resp.Header.Get("Content-Range"), int64(len(data)), offset, limit, tail)
		if rangeErr != nil {
			return nil, rangeErr
		}
	}

	return data, nil
}

func validReadFileOptions(readOnly bool, root, filename string, offset, limit int64, tail bool) bool {
	return readOnly && fileRoot(root) && safeRelative(filename) && offset >= 0 && limit >= 1 && limit <= MaxFileReadBytes && (!tail || offset == 0) && offset <= math.MaxInt64-limit
}

func validateContentRange(header string, size, offset, limit int64, tail bool) error {
	var start, end, total int64

	n, parseErr := fmt.Sscanf(header, "bytes %d-%d/%d", &start, &end, &total)
	if parseErr != nil || n != 3 || header != fmt.Sprintf("bytes %d-%d/%d", start, end, total) || start < 0 || end < start || end >= total || end-start+1 != size {
		return errors.New("invalid file Content-Range response")
	}

	wantStart := offset

	wantEnd := min(total-1, offset+limit-1)
	if tail {
		wantStart = max(int64(0), total-limit)
		wantEnd = total - 1
	}

	if start != wantStart || end != wantEnd {
		return errors.New("file Content-Range does not match requested slice or tail")
	}

	return nil
}

func fileRangeHeader(offset, limit int64, tail bool) string {
	if tail {
		return "bytes=-" + strconv.FormatInt(limit, 10)
	}

	return fmt.Sprintf("bytes=%d-%d", offset, offset+limit-1)
}
