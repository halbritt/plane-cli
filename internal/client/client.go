// Package client is the HTTP core for the Plane public API (/api/v1):
// X-Api-Key auth, 429/5xx retry with exponential backoff and jitter,
// cursor-pagination draining, and redacted --debug tracing to stderr.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// APIError is a non-2xx response from the Plane API, carrying the raw body
// so the CLI can surface the server's error payload verbatim.
type APIError struct {
	Status int
	Body   []byte
}

func (e *APIError) Error() string {
	return fmt.Sprintf("plane API: HTTP %d: %s", e.Status, truncate(string(e.Body), 200))
}

// Message extracts a human-readable message from the API error body.
// Plane uses {"error": "..."} for most errors and {"detail": "..."} for
// DRF-level ones (auth, throttle); either may be absent.
func (e *APIError) Message() string {
	var m map[string]any
	if json.Unmarshal(e.Body, &m) == nil {
		for _, k := range []string{"error", "detail", "message"} {
			if s, ok := m[k].(string); ok && s != "" {
				return s
			}
		}
		// Field-keyed validation errors, e.g. {"name": ["This field is required."]}
		for k, v := range m {
			if s, ok := v.(string); ok {
				return k + ": " + s
			}
			if l, ok := v.([]any); ok && len(l) > 0 {
				if s, ok := l[0].(string); ok {
					return k + ": " + s
				}
			}
		}
	}
	return http.StatusText(e.Status)
}

// Details returns the raw JSON body if it is valid JSON, else nil.
func (e *APIError) Details() json.RawMessage {
	if json.Valid(e.Body) {
		return json.RawMessage(e.Body)
	}
	return nil
}

// NetError is a transport-level failure (DNS, refused, timeout).
type NetError struct{ Err error }

func (e *NetError) Error() string { return "network: " + e.Err.Error() }
func (e *NetError) Unwrap() error { return e.Err }

// RateLimitedError is returned when retries were exhausted on HTTP 429.
type RateLimitedError struct{ APIError }

type Client struct {
	BaseURL     string // e.g. https://plane.example.com (no trailing slash)
	apiKey      string
	HTTP        *http.Client
	MaxRetries  int  // retry attempts after the first try
	RetryUnsafe bool // retry non-idempotent methods on network errors/5xx
	Debug       bool
	Stderr      io.Writer

	// Sleep is injectable for tests; defaults to time.Sleep.
	Sleep func(time.Duration)
}

func New(baseURL, apiKey string) *Client {
	return &Client{
		BaseURL:    strings.TrimRight(baseURL, "/"),
		apiKey:     apiKey,
		HTTP:       &http.Client{Timeout: 60 * time.Second},
		MaxRetries: 4,
		Stderr:     io.Discard,
		Sleep:      time.Sleep,
	}
}

// Resp is a successful (2xx) API response.
type Resp struct {
	Status int
	Header http.Header
	Body   []byte
}

// JSON returns the body as raw JSON, normalizing an empty body (204) to null.
func (r *Resp) JSON() json.RawMessage {
	if len(bytes.TrimSpace(r.Body)) == 0 {
		return json.RawMessage("null")
	}
	return json.RawMessage(r.Body)
}

// Do performs one API call with retries. path is under the base URL
// (e.g. "/api/v1/workspaces/foo/projects/"). body (if non-nil) is JSON-marshaled.
// Returns *APIError / *RateLimitedError / *NetError on failure.
func (c *Client) Do(ctx context.Context, method, path string, query url.Values, body any) (*Resp, error) {
	var payload []byte
	if body != nil {
		var err error
		payload, err = json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("encode request body: %w", err)
		}
	}

	u := c.BaseURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}

	idempotent := method == http.MethodGet || method == http.MethodHead ||
		method == http.MethodOptions || method == http.MethodDelete

	var lastErr error
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, method, u, bytes.NewReader(payload))
		if err != nil {
			return nil, err
		}
		req.Header.Set("X-Api-Key", c.apiKey)
		if payload != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		c.traceRequest(req, payload, attempt)

		resp, err := c.HTTP.Do(req)
		if err != nil {
			lastErr = &NetError{Err: err}
			c.tracef("transport error: %v", err)
			if ctx.Err() != nil || attempt >= c.MaxRetries || !(idempotent || c.RetryUnsafe) {
				return nil, lastErr
			}
			c.backoff(attempt, 0)
			continue
		}

		respBody, readErr := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
		resp.Body.Close()
		c.traceResponse(resp, respBody)
		if readErr != nil {
			return nil, &NetError{Err: readErr}
		}

		switch {
		case resp.StatusCode >= 200 && resp.StatusCode < 300:
			return &Resp{Status: resp.StatusCode, Header: resp.Header, Body: respBody}, nil

		case resp.StatusCode == http.StatusTooManyRequests:
			// 429 means the request was not processed; safe to retry any method.
			lastErr = &RateLimitedError{APIError{Status: resp.StatusCode, Body: respBody}}
			if attempt >= c.MaxRetries {
				return nil, lastErr
			}
			c.backoff(attempt, retryAfter(resp))
			continue

		case resp.StatusCode >= 500:
			lastErr = &APIError{Status: resp.StatusCode, Body: respBody}
			if attempt >= c.MaxRetries || !(idempotent || c.RetryUnsafe) {
				return nil, lastErr
			}
			c.backoff(attempt, 0)
			continue

		default:
			return nil, &APIError{Status: resp.StatusCode, Body: respBody}
		}
	}
}

// retryAfter derives a server-directed wait from 429 response headers:
// Retry-After (seconds) or X-RateLimit-Reset (unix timestamp).
func retryAfter(resp *http.Response) time.Duration {
	if s := resp.Header.Get("Retry-After"); s != "" {
		if secs, err := strconv.Atoi(s); err == nil && secs > 0 {
			return time.Duration(secs) * time.Second
		}
	}
	if s := resp.Header.Get("X-RateLimit-Reset"); s != "" {
		if ts, err := strconv.ParseInt(s, 10, 64); err == nil {
			if d := time.Until(time.Unix(ts, 0)); d > 0 {
				return d
			}
		}
	}
	return 0
}

// backoff sleeps for exponential backoff with jitter, honoring a
// server-directed minimum (capped at 90s).
func (c *Client) backoff(attempt int, serverWait time.Duration) {
	d := time.Duration(1<<uint(attempt)) * 500 * time.Millisecond
	if d > 8*time.Second {
		d = 8 * time.Second
	}
	if serverWait > d {
		d = serverWait
	}
	if d > 90*time.Second {
		d = 90 * time.Second
	}
	d += time.Duration(rand.Int63n(int64(250 * time.Millisecond)))
	c.tracef("retrying in %s", d.Round(time.Millisecond))
	c.Sleep(d)
}

// ListPage is Plane's paginated list envelope (the fields the CLI needs).
type ListPage struct {
	TotalCount      int               `json:"total_count"`
	NextCursor      string            `json:"next_cursor"`
	PrevCursor      string            `json:"prev_cursor"`
	NextPageResults bool              `json:"next_page_results"`
	TotalPages      int               `json:"total_pages"`
	Results         []json.RawMessage `json:"results"`
}

// ListMeta summarizes a (possibly capped) drain for the output envelope.
type ListMeta struct {
	TotalCount int
	Fetched    int
	Pages      int
	Drained    bool
	NextCursor string // set when not drained, for --cursor resume
}

const maxPerPage = 1000 // server cap; also its default

// ListAll GETs a paginated endpoint and auto-drains it by following
// next_cursor. limit > 0 caps the number of results; startCursor resumes a
// prior capped run. extra query params are preserved across pages.
func (c *Client) ListAll(ctx context.Context, path string, query url.Values, limit int, startCursor string) ([]json.RawMessage, ListMeta, error) {
	perPage := maxPerPage
	if limit > 0 && limit < perPage {
		perPage = limit
	}
	cursor := startCursor
	if cursor == "" {
		cursor = fmt.Sprintf("%d:0:0", perPage)
	}

	var results []json.RawMessage
	meta := ListMeta{}
	for {
		q := url.Values{}
		for k, v := range query {
			q[k] = v
		}
		q.Set("per_page", strconv.Itoa(perPage))
		q.Set("cursor", cursor)

		resp, err := c.Do(ctx, http.MethodGet, path, q, nil)
		if err != nil {
			return nil, meta, err
		}
		var page ListPage
		if err := json.Unmarshal(resp.Body, &page); err != nil {
			return nil, meta, fmt.Errorf("parse list page: %w", err)
		}
		meta.Pages++
		meta.TotalCount = page.TotalCount

		if limit > 0 && len(results)+len(page.Results) >= limit {
			need := limit - len(results)
			results = append(results, page.Results[:need]...)
			meta.Fetched = len(results)
			switch {
			case need == len(page.Results) && !page.NextPageResults:
				meta.Drained = true
			case need == len(page.Results):
				meta.NextCursor = page.NextCursor
			default:
				// Page partially consumed: resuming from its own cursor
				// re-reads it (cursor granularity is per page).
				meta.NextCursor = cursor
			}
			return results, meta, nil
		}
		results = append(results, page.Results...)
		if !page.NextPageResults {
			meta.Fetched = len(results)
			meta.Drained = true
			return results, meta, nil
		}
		cursor = page.NextCursor
	}
}

func (c *Client) tracef(format string, args ...any) {
	if c.Debug {
		fmt.Fprintf(c.Stderr, "[debug] "+format+"\n", args...)
	}
}

func (c *Client) traceRequest(req *http.Request, body []byte, attempt int) {
	if !c.Debug {
		return
	}
	note := ""
	if attempt > 0 {
		note = fmt.Sprintf(" (retry %d)", attempt)
	}
	fmt.Fprintf(c.Stderr, "[debug] > %s %s%s\n", req.Method, req.URL, note)
	fmt.Fprintf(c.Stderr, "[debug] > X-Api-Key: <redacted len=%d>\n", len(c.apiKey))
	if len(body) > 0 {
		fmt.Fprintf(c.Stderr, "[debug] > body: %s\n", truncate(string(body), 2048))
	}
}

func (c *Client) traceResponse(resp *http.Response, body []byte) {
	if !c.Debug {
		return
	}
	fmt.Fprintf(c.Stderr, "[debug] < HTTP %d", resp.StatusCode)
	if v := resp.Header.Get("X-RateLimit-Remaining"); v != "" {
		fmt.Fprintf(c.Stderr, " (ratelimit remaining=%s)", v)
	}
	fmt.Fprintln(c.Stderr)
	if len(body) > 0 {
		fmt.Fprintf(c.Stderr, "[debug] < body: %s\n", truncate(string(body), 2048))
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…(truncated)"
}

var ErrNoRedirect = errors.New("no redirect")

// Location performs a GET that expects a 302 redirect (attachment download)
// and returns the Location header without following it — the presigned URL
// must be fetched without the API key header.
func (c *Client) Location(ctx context.Context, path string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+path, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("X-Api-Key", c.apiKey)
	c.traceRequest(req, nil, 0)

	noFollow := *c.HTTP
	noFollow.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := noFollow.Do(req)
	if err != nil {
		return "", &NetError{Err: err}
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	c.traceResponse(resp, body)

	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		loc := resp.Header.Get("Location")
		if loc == "" {
			return "", &APIError{Status: resp.StatusCode, Body: body}
		}
		return loc, nil
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return "", ErrNoRedirect
	}
	return "", &APIError{Status: resp.StatusCode, Body: body}
}
