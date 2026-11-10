package moonraker

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"

	"github.com/cockroachdb/errors"
)

// resultEnvelope is the success envelope Moonraker wraps results in.
type resultEnvelope struct {
	Result json.RawMessage `json:"result"`
}

// errorEnvelope is the error shape Moonraker returns on a failed request.
type errorEnvelope struct {
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// Get issues a GET request and returns the unwrapped result payload.
func (c *Client) Get(ctx context.Context, path string, query url.Values) (json.RawMessage, error) {
	return c.send(ctx, http.MethodGet, path, query, nil, "")
}

// Delete issues a DELETE request and returns the unwrapped result payload.
func (c *Client) Delete(ctx context.Context, path string, query url.Values) (json.RawMessage, error) {
	return c.send(ctx, http.MethodDelete, path, query, nil, "")
}

// Post issues a POST request with optional query parameters and an optional
// JSON body.
func (c *Client) Post(ctx context.Context, path string, query url.Values, body any) (json.RawMessage, error) {
	var (
		payload     []byte
		contentType string
	)

	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, errors.Wrap(err, "marshal request body")
		}

		payload = data
		contentType = "application/json"
	}

	return c.send(ctx, http.MethodPost, path, query, payload, contentType)
}

// GetRaw issues a GET request and returns the raw response body, used for file
// downloads that are not wrapped in the JSON result envelope.
func (c *Client) GetRaw(ctx context.Context, path string, query url.Values) ([]byte, error) {
	data, status, err := c.roundtrip(ctx, http.MethodGet, path, query, nil, "")
	if err != nil {
		return nil, err
	}

	if status == http.StatusUnauthorized {
		return nil, ErrNotAuthenticated
	}

	if status < http.StatusOK || status >= http.StatusMultipleChoices {
		return nil, statusErr(http.MethodGet, path, status, data)
	}

	return data, nil
}

// Upload streams a multipart file upload to the file manager.
func (c *Client) Upload(ctx context.Context, opts *UploadOptions) (json.RawMessage, error) {
	var buf bytes.Buffer

	writer := multipart.NewWriter(&buf)

	if opts.Root != "" {
		_ = writer.WriteField("root", opts.Root)
	}

	if opts.Path != "" {
		_ = writer.WriteField("path", opts.Path)
	}

	_ = writer.WriteField("print", strconv.FormatBool(opts.StartPrint))

	part, err := writer.CreateFormFile("file", opts.Filename)
	if err != nil {
		return nil, errors.Wrap(err, "create upload part")
	}

	_, writeErr := part.Write(opts.Content)
	if writeErr != nil {
		return nil, errors.Wrap(writeErr, "write upload content")
	}

	closeErr := writer.Close()
	if closeErr != nil {
		return nil, errors.Wrap(closeErr, "finalize upload")
	}

	return c.send(ctx, http.MethodPost, "/server/files/upload", nil, buf.Bytes(), writer.FormDataContentType())
}

// send performs a request and decodes the JSON result envelope.
func (c *Client) send(
	ctx context.Context,
	method, path string,
	query url.Values,
	body []byte,
	contentType string,
) (json.RawMessage, error) {
	data, status, err := c.roundtrip(ctx, method, path, query, body, contentType)
	if err != nil {
		return nil, err
	}

	return decodeEnvelope(method, path, status, data)
}

// roundtrip attaches authentication, sends the request, and retries once after
// re-authenticating when the token was rejected mid-flight.
func (c *Client) roundtrip(
	ctx context.Context,
	method, path string,
	query url.Values,
	body []byte,
	contentType string,
) ([]byte, int, error) {
	if c.readOnly {
		err := validateReadRequest(method, path, query)
		if err != nil {
			return nil, 0, err
		}
	}

	if c.usesJWT() {
		authErr := c.ensureAuth(ctx)
		if authErr != nil {
			return nil, 0, authErr
		}
	}

	token := c.currentToken()

	data, status, err := c.attempt(ctx, method, path, query, body, contentType)
	if err != nil {
		return nil, 0, err
	}

	if status == http.StatusUnauthorized && c.canReauth() {
		reErr := c.reauth(ctx, token)
		if reErr != nil {
			return nil, 0, reErr
		}

		return c.attempt(ctx, method, path, query, body, contentType)
	}

	return data, status, nil
}

// attempt issues a single HTTP request and returns the body and status.
func (c *Client) attempt(
	ctx context.Context,
	method, path string,
	query url.Values,
	body []byte,
	contentType string,
) ([]byte, int, error) {
	target := c.baseURL + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}

	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}

	req, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return nil, 0, errors.Wrap(err, "build request")
	}

	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.userAgent)

	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}

	c.attachAuth(req)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, apiErr(err, "%s %s", method, path)
	}
	defer func() { _ = resp.Body.Close() }()

	var responseReader io.Reader = resp.Body
	if c.readOnly {
		responseReader = io.LimitReader(resp.Body, maxReadResponseBytes+1)
	}

	data, readErr := io.ReadAll(responseReader)
	if readErr != nil {
		return nil, 0, apiErr(readErr, "read %s %s response", method, path)
	}

	if c.readOnly && len(data) > maxReadResponseBytes {
		return nil, 0, errors.New("diagnostic response exceeds 4 MiB limit")
	}

	return data, resp.StatusCode, nil
}

// attachAuth sets the X-Api-Key or Bearer header when configured.
func (c *Client) attachAuth(req *http.Request) {
	if c.apiKey != "" {
		req.Header.Set("X-Api-Key", c.apiKey)

		return
	}

	token := c.currentToken()
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
}

// decodeEnvelope classifies the status and unwraps the result payload.
func decodeEnvelope(method, path string, status int, data []byte) (json.RawMessage, error) {
	if status == http.StatusUnauthorized {
		return nil, ErrNotAuthenticated
	}

	if status < http.StatusOK || status >= http.StatusMultipleChoices {
		return nil, statusErr(method, path, status, data)
	}

	if len(data) == 0 {
		return nil, nil
	}

	var envelope resultEnvelope

	decErr := json.Unmarshal(data, &envelope)
	if decErr != nil {
		return nil, apiErr(decErr, "decode %s %s response", method, path)
	}

	return envelope.Result, nil
}

// statusErr builds an API error from a non-2xx response, surfacing Moonraker's
// error message when present. A 404 wraps ErrNotFound so callers can degrade
// gracefully when an optional component is not configured.
func statusErr(method, path string, status int, data []byte) error {
	// A 404 wraps ErrNotFound (which itself wraps ErrAPI) so callers can branch
	// on a missing resource while generic ErrAPI handling still matches.
	base := ErrAPI
	if status == http.StatusNotFound {
		base = ErrNotFound
	}

	var envelope errorEnvelope

	unErr := json.Unmarshal(data, &envelope)
	if unErr == nil && envelope.Error != nil && envelope.Error.Message != "" {
		return errors.Wrapf(base, "%s %s: %s (status %d)", method, path, envelope.Error.Message, status)
	}

	return errors.Wrapf(base, "%s %s: status %d", method, path, status)
}
