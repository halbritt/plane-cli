package client

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func testClient(srvURL string) (*Client, *[]time.Duration) {
	c := New(srvURL, "k")
	var sleeps []time.Duration
	c.Sleep = func(d time.Duration) { sleeps = append(sleeps, d) }
	return c, &sleeps
}

func Test429RetriedForUnsafeMethodsAndHonorsRetryAfter(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "3")
			w.WriteHeader(429)
			return
		}
		w.WriteHeader(201)
		w.Write([]byte(`{"id":"x"}`))
	}))
	defer srv.Close()

	c, sleeps := testClient(srv.URL)
	resp, err := c.Do(context.Background(), http.MethodPost, "/p", nil, map[string]any{"a": 1})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if resp.Status != 201 {
		t.Errorf("status = %d", resp.Status)
	}
	if calls.Load() != 2 {
		t.Errorf("calls = %d, want 2 (429 must retry even POST)", calls.Load())
	}
	if len(*sleeps) != 1 || (*sleeps)[0] < 3*time.Second {
		t.Errorf("sleeps = %v, want one wait >= Retry-After (3s)", *sleeps)
	}
}

func Test429ExhaustedReturnsRateLimitedError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(429)
		w.Write([]byte(`{"detail":"throttled"}`))
	}))
	defer srv.Close()

	c, _ := testClient(srv.URL)
	c.MaxRetries = 2
	_, err := c.Do(context.Background(), http.MethodGet, "/p", nil, nil)
	var rl *RateLimitedError
	if !errors.As(err, &rl) {
		t.Fatalf("err = %T %v, want RateLimitedError", err, err)
	}
}

func Test5xxNotRetriedForPOSTByDefault(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(502)
	}))
	defer srv.Close()

	c, _ := testClient(srv.URL)
	_, err := c.Do(context.Background(), http.MethodPost, "/p", nil, nil)
	var api *APIError
	if !errors.As(err, &api) || api.Status != 502 {
		t.Fatalf("err = %v", err)
	}
	if calls.Load() != 1 {
		t.Errorf("calls = %d, want 1 (unsafe method must not retry 5xx)", calls.Load())
	}
}

func Test5xxRetriedForPOSTWithRetryUnsafe(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(502)
			return
		}
		w.WriteHeader(200)
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	c, _ := testClient(srv.URL)
	c.RetryUnsafe = true
	resp, err := c.Do(context.Background(), http.MethodPost, "/p", nil, nil)
	if err != nil || resp.Status != 200 {
		t.Fatalf("resp=%v err=%v", resp, err)
	}
	if calls.Load() != 2 {
		t.Errorf("calls = %d, want 2", calls.Load())
	}
}

func Test5xxRetriedForGET(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) < 3 {
			w.WriteHeader(500)
			return
		}
		w.WriteHeader(200)
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	c, sleeps := testClient(srv.URL)
	resp, err := c.Do(context.Background(), http.MethodGet, "/p", nil, nil)
	if err != nil || resp.Status != 200 {
		t.Fatalf("resp=%v err=%v", resp, err)
	}
	if len(*sleeps) != 2 {
		t.Fatalf("sleeps = %v", *sleeps)
	}
	// Exponential: second wait should exceed the first (jitter is +[0,250ms)).
	if (*sleeps)[1] <= (*sleeps)[0] {
		t.Errorf("backoff not increasing: %v", *sleeps)
	}
}

func TestNetworkErrorRetriedForGETOnly(t *testing.T) {
	c, _ := testClient("http://127.0.0.1:1") // closed port
	c.MaxRetries = 1
	c.HTTP.Timeout = 500 * time.Millisecond

	_, err := c.Do(context.Background(), http.MethodGet, "/p", nil, nil)
	var ne *NetError
	if !errors.As(err, &ne) {
		t.Fatalf("err = %T, want NetError", err)
	}

	_, err = c.Do(context.Background(), http.MethodPost, "/p", nil, nil)
	if !errors.As(err, &ne) {
		t.Fatalf("err = %T, want NetError", err)
	}
}

func TestAPIErrorMessageExtraction(t *testing.T) {
	cases := []struct {
		body string
		want string
	}{
		{`{"error":"boom"}`, "boom"},
		{`{"detail":"denied"}`, "denied"},
		{`{"name":["This field is required."]}`, "name: This field is required."},
		{`not json`, "Bad Request"},
	}
	for _, tc := range cases {
		e := &APIError{Status: 400, Body: []byte(tc.body)}
		if got := e.Message(); got != tc.want {
			t.Errorf("Message(%s) = %q, want %q", tc.body, got, tc.want)
		}
	}
}

func TestLocationCapturesRedirectWithoutFollowing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redir" {
			http.Redirect(w, r, "https://storage.example/presigned?sig=x", 302)
			return
		}
		t.Errorf("redirect was followed to %s", r.URL.Path)
	}))
	defer srv.Close()

	c, _ := testClient(srv.URL)
	loc, err := c.Location(context.Background(), "/redir")
	if err != nil {
		t.Fatal(err)
	}
	if loc != "https://storage.example/presigned?sig=x" {
		t.Errorf("loc = %s", loc)
	}
}
