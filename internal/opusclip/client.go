// Package opusclip is a typed client for the OpusClip REST API
// (https://api.opus.pro/api). It is pure transport: rate limiting, retries,
// typed errors, and request shapes copied from the vendor CLI. No judgment
// lives here.
//
// Auth is a bearer token read from OPUSCLIP_API_KEY. The key is never logged;
// the Logger only ever sees method, path, status and timing, and
// RedactingLogger scrubs any bearer token that slips into a message.
package opusclip

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"
)

// DefaultBaseURL is the production API root. Override with OPUSCLIP_API_URL.
const DefaultBaseURL = "https://api.opus.pro/api"

// EnvAPIKey and EnvAPIURL are the environment variables the client reads.
const (
	EnvAPIKey = "OPUSCLIP_API_KEY"
	EnvAPIURL = "OPUSCLIP_API_URL"
)

// ErrNoAPIKey is returned by NewFromEnv when OPUSCLIP_API_KEY is unset.
var ErrNoAPIKey = errors.New("opusclip: " + EnvAPIKey + " is not set (Pro or Enterprise plan required)")

// Logger receives one line per request. Implementations must assume the
// message may be shown to a user; the client never passes the key.
type Logger interface {
	Printf(format string, args ...any)
}

// APIError is a non-2xx response. Body holds at most 512 bytes of the
// response so an error message never dumps a whole payload.
type APIError struct {
	Status int
	Method string
	Path   string
	Body   string
}

func (e *APIError) Error() string {
	b := strings.TrimSpace(e.Body)
	if b == "" {
		return fmt.Sprintf("opusclip: %s %s: HTTP %d", e.Method, e.Path, e.Status)
	}
	return fmt.Sprintf("opusclip: %s %s: HTTP %d: %s", e.Method, e.Path, e.Status, b)
}

// Temporary reports whether the error is a 429 or 5xx, i.e. retrying later
// might succeed.
func (e *APIError) Temporary() bool { return e.Status == 429 || e.Status >= 500 }

// IsStatus reports whether err is an *APIError with the given status.
func IsStatus(err error, status int) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.Status == status
}

// Client talks to the OpusClip API.
type Client struct {
	BaseURL string
	HTTP    *http.Client
	Logger  Logger

	// MaxRetries is the number of additional attempts after a 429/5xx or a
	// transport error. Backoff is the base delay, doubled per attempt with
	// jitter, capped at MaxBackoff.
	MaxRetries int
	Backoff    time.Duration
	MaxBackoff time.Duration

	apiKey  string
	bucket  *limiter // 30 req/min across every API call
	pacer   *limiter // 1 req/s for the publish-schedule family
	sleep   func(ctx context.Context, d time.Duration) error
	now     func() time.Time
	userAgt string
}

// Option configures a Client.
type Option func(*Client)

// WithBaseURL overrides the API root (tests point this at httptest).
func WithBaseURL(u string) Option { return func(c *Client) { c.BaseURL = strings.TrimRight(u, "/") } }

// WithHTTPClient replaces the transport.
func WithHTTPClient(h *http.Client) Option { return func(c *Client) { c.HTTP = h } }

// WithLogger sets the request logger. It is wrapped in RedactingLogger.
func WithLogger(l Logger) Option { return func(c *Client) { c.Logger = RedactingLogger{l} } }

// WithRetry sets retry count and base backoff.
func WithRetry(max int, base time.Duration) Option {
	return func(c *Client) { c.MaxRetries = max; c.Backoff = base }
}

// WithNoRateLimit disables both limiters (tests only).
func WithNoRateLimit() Option { return func(c *Client) { c.bucket = nil; c.pacer = nil } }

// withClock injects a fake clock and sleeper into the client and limiters.
func withClock(now func() time.Time, sleep func(context.Context, time.Duration) error) Option {
	return func(c *Client) {
		c.now, c.sleep = now, sleep
		if c.bucket != nil {
			c.bucket.now, c.bucket.sleep = now, sleep
		}
		if c.pacer != nil {
			c.pacer.now, c.pacer.sleep = now, sleep
		}
	}
}

// New builds a client with the given key. The key is stored privately and
// only ever written into the Authorization header of API requests; it is not
// sent to the upload (GCS) host.
func New(apiKey string, opts ...Option) *Client {
	c := &Client{
		BaseURL:    DefaultBaseURL,
		HTTP:       &http.Client{Timeout: 2 * time.Minute},
		MaxRetries: 4,
		Backoff:    time.Second,
		MaxBackoff: 30 * time.Second,
		apiKey:     apiKey,
		bucket:     newLimiter(30, time.Minute/30),
		pacer:      newLimiter(1, time.Second),
		sleep:      sleepCtx,
		now:        time.Now,
		userAgt:    "longform-pipeline/opusclip-go",
	}
	if u := os.Getenv(EnvAPIURL); u != "" {
		c.BaseURL = strings.TrimRight(u, "/")
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// NewFromEnv builds a client from OPUSCLIP_API_KEY / OPUSCLIP_API_URL.
func NewFromEnv(opts ...Option) (*Client, error) {
	key := strings.TrimSpace(os.Getenv(EnvAPIKey))
	if key == "" {
		return nil, ErrNoAPIKey
	}
	return New(key, opts...), nil
}

// HasKey reports whether a key is configured (never the key itself).
func (c *Client) HasKey() bool { return c.apiKey != "" }

func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (c *Client) logf(format string, args ...any) {
	if c.Logger != nil {
		c.Logger.Printf(format, args...)
	}
}

// doJSON performs one API call with rate limiting and retries. body (if
// non-nil) is JSON-encoded; the response body is returned raw so callers can
// decode typed views while keeping the verbatim payload.
func (c *Client) doJSON(ctx context.Context, method, path string, body any, paced bool) (json.RawMessage, error) {
	var payload []byte
	if body != nil {
		var err error
		payload, err = json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("opusclip: encode %s %s: %w", method, path, err)
		}
	}
	var lastErr error
	for attempt := 0; attempt <= c.MaxRetries; attempt++ {
		if attempt > 0 {
			d := c.backoff(attempt)
			c.logf("opusclip: retry %d/%d for %s %s after %s (%v)", attempt, c.MaxRetries, method, path, d, lastErr)
			if err := c.sleep(ctx, d); err != nil {
				return nil, err
			}
		}
		if c.bucket != nil {
			if err := c.bucket.wait(ctx); err != nil {
				return nil, err
			}
		}
		if paced && c.pacer != nil {
			if err := c.pacer.wait(ctx); err != nil {
				return nil, err
			}
		}
		raw, err := c.once(ctx, method, path, payload)
		if err == nil {
			return raw, nil
		}
		lastErr = err
		var ae *APIError
		if errors.As(err, &ae) && !ae.Temporary() {
			return nil, err
		}
		if ctx.Err() != nil {
			return nil, err
		}
	}
	return nil, lastErr
}

func (c *Client) once(ctx context.Context, method, path string, payload []byte) (json.RawMessage, error) {
	var rdr io.Reader
	if payload != nil {
		rdr = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, rdr)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.userAgt)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	start := c.now()
	resp, err := c.HTTP.Do(req)
	if err != nil {
		c.logf("opusclip: %s %s -> transport error after %s: %v", method, path, c.now().Sub(start).Round(time.Millisecond), err)
		return nil, fmt.Errorf("opusclip: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	c.logf("opusclip: %s %s -> %d (%d bytes, %s)", method, path, resp.StatusCode, len(data), c.now().Sub(start).Round(time.Millisecond))
	if err != nil {
		return nil, fmt.Errorf("opusclip: %s %s: read body: %w", method, path, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, &APIError{Status: resp.StatusCode, Method: method, Path: path, Body: snippet(data, 512)}
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return json.RawMessage("null"), nil
	}
	return json.RawMessage(data), nil
}

func (c *Client) backoff(attempt int) time.Duration {
	d := c.Backoff << (attempt - 1)
	if c.MaxBackoff > 0 && d > c.MaxBackoff {
		d = c.MaxBackoff
	}
	if d <= 0 {
		return 0
	}
	// Up to 25% jitter so parallel jobs do not thunder.
	return d + time.Duration(rand.Int63n(int64(d)/4+1))
}

func snippet(b []byte, n int) string {
	s := strings.TrimSpace(string(b))
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}

// get / post / del are thin wrappers used by the typed methods.
func (c *Client) get(ctx context.Context, path string) (json.RawMessage, error) {
	return c.doJSON(ctx, http.MethodGet, path, nil, false)
}

func (c *Client) post(ctx context.Context, path string, body any) (json.RawMessage, error) {
	return c.doJSON(ctx, http.MethodPost, path, body, false)
}

func (c *Client) postPaced(ctx context.Context, path string, body any) (json.RawMessage, error) {
	return c.doJSON(ctx, http.MethodPost, path, body, true)
}

func (c *Client) delPaced(ctx context.Context, path string) (json.RawMessage, error) {
	return c.doJSON(ctx, http.MethodDelete, path, nil, true)
}

// unwrapData returns raw["data"] when the payload is an object with a data
// member, else raw itself. The API wraps some responses and not others.
func unwrapData(raw json.RawMessage) json.RawMessage {
	var env struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err == nil && len(env.Data) > 0 && string(env.Data) != "null" {
		return env.Data
	}
	return raw
}

// stringField pulls the first present string among keys from a JSON object,
// looking at the top level and then under "data".
func stringField(raw json.RawMessage, keys ...string) string {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return ""
	}
	for _, k := range keys {
		if v, ok := m[k]; ok {
			var s string
			if json.Unmarshal(v, &s) == nil && s != "" {
				return s
			}
		}
	}
	if d, ok := m["data"]; ok {
		var inner map[string]json.RawMessage
		if json.Unmarshal(d, &inner) == nil {
			for _, k := range keys {
				if v, ok := inner[k]; ok {
					var s string
					if json.Unmarshal(v, &s) == nil && s != "" {
						return s
					}
				}
			}
		}
	}
	return ""
}

// RedactingLogger wraps a Logger and replaces any bearer token, Authorization
// header value, or OPUSCLIP_API_KEY-looking assignment in the formatted
// message with a placeholder. The client never logs the key, but a caller
// that logs a dumped request through this wrapper is still safe.
type RedactingLogger struct{ L Logger }

var redactRe = regexp.MustCompile(`(?i)((?:authorization:\s*)?bearer\s+|authorization:\s*|` + EnvAPIKey + `=)[^\s"']+`)

// Redact scrubs secrets from s.
func Redact(s string) string { return redactRe.ReplaceAllString(s, "${1}[REDACTED]") }

func (r RedactingLogger) Printf(format string, args ...any) {
	if r.L == nil {
		return
	}
	r.L.Printf("%s", Redact(fmt.Sprintf(format, args...)))
}
