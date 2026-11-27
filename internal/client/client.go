// Package client is a small, typed HTTP client for the parts of the Xcelsior
// public API this provider uses. Every path here is in the published spec
// (api/fern/openapi.json in the Xcelsior repository); a test there fails if one
// leaves it.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultEndpoint is the production API. Acceptance tests never use it; see
// internal/provider/acc_test.go.
const DefaultEndpoint = "https://xcelsior.ca"

// Client calls the Xcelsior API with one bearer token.
type Client struct {
	baseURL   *url.URL
	token     string
	http      *http.Client
	userAgent string
}

// New returns a client for endpoint, authenticating with token.
func New(endpoint, token, userAgent string, httpClient *http.Client) (*Client, error) {
	if strings.TrimSpace(token) == "" {
		return nil, errors.New("an API token is required")
	}
	u, err := url.Parse(strings.TrimRight(endpoint, "/"))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("endpoint %q is not an absolute URL", endpoint)
	}
	if u.Scheme != "https" && !isLoopback(u.Hostname()) {
		return nil, fmt.Errorf("endpoint %q must use https (plain http is allowed only for localhost)", endpoint)
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 60 * time.Second}
	}
	return &Client{baseURL: u, token: token, http: httpClient, userAgent: userAgent}, nil
}

func isLoopback(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1" || strings.HasSuffix(host, ".localhost")
}

// URL resolves a site-relative path (such as a plan's approval_url) against
// the endpoint, so a person can open it.
func (c *Client) URL(path string) string {
	if path == "" {
		return ""
	}
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		return path
	}
	return c.baseURL.String() + "/" + strings.TrimLeft(path, "/")
}

// APIError is any non-2xx answer. Code is the RFC 9457 `code` member when the
// API sent problem+json, and empty for the older `{"detail": "..."}` shape.
type APIError struct {
	Method string
	Path   string
	Status int
	Code   string
	Detail string
	// Body is the decoded JSON body, for problem extension members such as
	// `replacement_plan`.
	Body map[string]any
}

func (e *APIError) Error() string {
	msg := e.Detail
	if msg == "" {
		msg = http.StatusText(e.Status)
	}
	if e.Code != "" {
		return fmt.Sprintf("%s %s: %d %s: %s", e.Method, e.Path, e.Status, e.Code, msg)
	}
	return fmt.Sprintf("%s %s: %d: %s", e.Method, e.Path, e.Status, msg)
}

// IsNotFound reports whether err is a 404 from the API.
func IsNotFound(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound
}

// IsMissingScope reports whether err is the API refusing a credential that
// does not hold scope.
func IsMissingScope(err error, scope string) bool {
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusForbidden {
		return false
	}
	// "Insufficient scope — required: a, b; granted: c, d"
	_, rest, ok := strings.Cut(apiErr.Detail, "required: ")
	if !ok {
		return false
	}
	required, _, _ := strings.Cut(rest, ";")
	for _, s := range strings.Split(required, ",") {
		if strings.TrimSpace(s) == scope {
			return true
		}
	}
	return false
}

// HasCode reports whether err is an API problem with the given code.
func HasCode(err error, code string) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.Code == code
}

func (c *Client) do(ctx context.Context, method, path string, query url.Values, body, out any) error {
	u := c.baseURL.String() + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	var reader io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encoding %s %s: %w", method, path, err)
		}
		reader = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json, application/problem+json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.userAgent != "" {
		req.Header.Set("User-Agent", c.userAgent)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return fmt.Errorf("%s %s: reading response: %w", method, path, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return decodeError(method, path, resp.StatusCode, raw)
	}
	if out == nil || len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("%s %s: decoding response: %w", method, path, err)
	}
	return nil
}

func decodeError(method, path string, status int, raw []byte) error {
	apiErr := &APIError{Method: method, Path: path, Status: status}
	var body map[string]any
	if json.Unmarshal(raw, &body) != nil {
		apiErr.Detail = strings.TrimSpace(string(raw))
		if len(apiErr.Detail) > 500 {
			apiErr.Detail = apiErr.Detail[:500]
		}
		return apiErr
	}
	apiErr.Body = body
	// The API's own envelope, which every HTTPException and validation error
	// is answered in: {"ok": false, "error": {"code", "message", ...}}.
	// Members beyond code and message are lifted to the top of Body, where an
	// RFC 9457 problem carries them.
	if env, ok := body["error"].(map[string]any); ok {
		if code, ok := env["code"].(string); ok && code != "http_error" {
			apiErr.Code = code
		}
		if msg, ok := env["message"].(string); ok {
			apiErr.Detail = msg
		}
		if details, ok := env["details"]; ok && details != nil {
			buf, _ := json.Marshal(details)
			apiErr.Detail += ": " + string(buf)
		}
		for k, v := range env {
			if _, taken := body[k]; !taken {
				body[k] = v
			}
		}
		return apiErr
	}
	if code, ok := body["code"].(string); ok {
		apiErr.Code = code
	}
	switch d := body["detail"].(type) {
	case string:
		apiErr.Detail = d
	case nil:
		if t, ok := body["title"].(string); ok {
			apiErr.Detail = t
		}
	default:
		// FastAPI validation errors: a list of {loc, msg}.
		buf, _ := json.Marshal(d)
		apiErr.Detail = string(buf)
	}
	return apiErr
}

// ── Spend envelope ──────────────────────────────────────────────────

// SpendEnvelope is the envelope on the calling credential, or nil.
type SpendEnvelope struct {
	ClientID string `json:"client_id"`
	Envelope *struct {
		DailySpendMaxMicros  *int64 `json:"daily_spend_max_micros"`
		HourlySpendMaxMicros *int64 `json:"hourly_spend_max_micros"`
		PerActionMaxMicros   *int64 `json:"per_action_max_micros"`
		MaxGPUsPerLaunch     *int64 `json:"max_gpus_per_launch"`
	} `json:"envelope"`
	Usage *struct {
		Available    bool  `json:"available"`
		HourlyMicros int64 `json:"hourly_micros"`
		DailyMicros  int64 `json:"daily_micros"`
	} `json:"usage"`
}

// GetSpendEnvelope reads the calling credential's envelope.
func (c *Client) GetSpendEnvelope(ctx context.Context) (*SpendEnvelope, error) {
	var out SpendEnvelope
	if err := c.do(ctx, http.MethodGet, "/api/v1/spend-envelope", nil, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
