// Copyright radarmodel.com 2026
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// NewHTTPClient returns a client with the given per-request timeout that also
// reads file:// URLs from the local disk, for offline runs and tests.
func NewHTTPClient(timeout time.Duration) *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.RegisterProtocol("file", http.NewFileTransport(http.Dir("/")))
	return &http.Client{Timeout: timeout, Transport: tr}
}

// Fetcher gets JSON over HTTP and retries the failures that are worth
// retrying: network errors, 408, 429 and 5xx. Any other status, a body that
// isn't valid JSON, or running out of attempts is an error, which the caller
// must treat as "couldn't scan", never as "the source is empty".
type Fetcher struct {
	Client   *http.Client
	Attempts int           // 0 means 3
	MaxWait  time.Duration // longest wait between attempts; 0 means 30s
	MaxBody  int64         // 0 means 32 MiB
	// Backoff is the first wait between attempts, doubled each time; 0
	// means 1s. A Retry-After header overrides it, up to MaxWait.
	Backoff time.Duration
	// Sleep waits between attempts; nil means a real, cancellable wait.
	Sleep func(ctx context.Context, d time.Duration) error
}

// StatusError is an HTTP response with a status the fetcher won't accept.
type StatusError struct {
	Status int
	Text   string
	URL    string
	Body   string
}

func (e *StatusError) Error() string {
	msg := fmt.Sprintf("GET %s returned %s", e.URL, e.Text)
	if e.Body != "" {
		msg += ": " + e.Body
	}
	return msg
}

// Redact hides query parameters that carry secrets, so URLs can go into
// error messages and logs.
func Redact(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "(unparseable URL)"
	}
	q := u.Query()
	changed := false
	for k := range q {
		switch strings.ToLower(k) {
		case "key", "api_key", "apikey", "token", "access_token":
			q.Set(k, "REDACTED")
			changed = true
		}
	}
	if changed {
		u.RawQuery = q.Encode()
	}
	u.User = nil
	return u.String()
}

func (f *Fetcher) sleep(ctx context.Context, d time.Duration) error {
	if f.Sleep != nil {
		return f.Sleep(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// GetJSON fetches rawURL with the given headers and decodes the body into
// out.
func (f *Fetcher) GetJSON(ctx context.Context, rawURL string, headers map[string]string, out any) error {
	attempts := f.Attempts
	if attempts <= 0 {
		attempts = 3
	}
	maxWait := f.MaxWait
	if maxWait <= 0 {
		maxWait = 30 * time.Second
	}
	wait := f.Backoff
	if wait <= 0 {
		wait = time.Second
	}
	var last error
	for i := 0; i < attempts; i++ {
		if i > 0 {
			if err := f.sleep(ctx, wait); err != nil {
				return err
			}
			wait *= 2
			if wait > maxWait {
				wait = maxWait
			}
		}
		retry, after, err := f.once(ctx, rawURL, headers, out)
		if err == nil {
			return nil
		}
		last = err
		if !retry || ctx.Err() != nil {
			return err
		}
		if after > 0 {
			wait = min(after, maxWait)
		}
	}
	return fmt.Errorf("gave up after %d attempts: %w", attempts, last)
}

func (f *Fetcher) once(ctx context.Context, rawURL string, headers map[string]string, out any) (retry bool, after time.Duration, err error) {
	shown := Redact(rawURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return false, 0, fmt.Errorf("bad URL %s: %v", shown, err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := f.Client.Do(req)
	if err != nil {
		// url.Error repeats the full URL, key included; report only the cause.
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return true, 0, fmt.Errorf("GET %s: %v", shown, err)
	}
	defer resp.Body.Close()
	limit := f.MaxBody
	if limit <= 0 {
		limit = 32 << 20
	}
	if resp.StatusCode/100 != 2 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		se := &StatusError{Status: resp.StatusCode, Text: resp.Status, URL: shown,
			Body: strings.TrimSpace(strings.ReplaceAll(string(snippet), "\n", " "))}
		retry := resp.StatusCode == 408 || resp.StatusCode == 429 || resp.StatusCode >= 500
		return retry, retryAfter(resp.Header.Get("Retry-After")), se
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return true, 0, fmt.Errorf("GET %s: reading body: %v", shown, err)
	}
	if int64(len(body)) > limit {
		return false, 0, fmt.Errorf("GET %s: body larger than %d bytes", shown, limit)
	}
	if err := json.Unmarshal(body, out); err != nil {
		// A cut-off or garbled body is retried: proxies and overloaded
		// servers produce them, and a later attempt usually comes back whole.
		return true, 0, fmt.Errorf("GET %s: response is not valid JSON: %v", shown, err)
	}
	return false, 0, nil
}

func retryAfter(v string) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if n, err := strconv.Atoi(v); err == nil && n >= 0 {
		return time.Duration(n) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}
