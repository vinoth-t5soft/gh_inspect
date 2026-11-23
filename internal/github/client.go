// Package github is a small, dependency-free GitHub REST API client with
// pagination, rate-limit handling and retries.
package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// DefaultBaseURL is the public GitHub API. For GitHub Enterprise Server use
// https://<host>/api/v3.
const DefaultBaseURL = "https://api.github.com"

// Client talks to the GitHub REST API.
type Client struct {
	baseURL    string
	token      string
	httpClient *http.Client

	// MaxRetries is the number of retries for rate-limited / 5xx responses.
	MaxRetries int
	// Logf receives diagnostic messages (rate-limit waits, retries).
	Logf func(format string, args ...any)
	// sleep is overridable for tests.
	sleep func(ctx context.Context, d time.Duration) error
}

// NewClient builds a client. baseURL may be empty for api.github.com.
func NewClient(baseURL, token string) *Client {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	return &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		token:      token,
		httpClient: &http.Client{Timeout: 60 * time.Second},
		MaxRetries: 6,
		Logf:       func(string, ...any) {},
		sleep:      sleepCtx,
	}
}

// APIError is a non-2xx response from GitHub.
type APIError struct {
	StatusCode int
	URL        string
	Message    string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("GET %s: %d %s", e.URL, e.StatusCode, e.Message)
}

// StatusCode returns the HTTP status of an APIError, or 0.
func StatusCode(err error) int {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode
	}
	return 0
}

func (c *Client) resolve(path string) string {
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		return path
	}
	return c.baseURL + path
}

// get performs a GET with retries and returns body + headers.
func (c *Client) get(ctx context.Context, rawURL string) ([]byte, http.Header, error) {
	var lastErr error
	for attempt := 0; attempt <= c.MaxRetries; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
		if err != nil {
			return nil, nil, err
		}
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
		req.Header.Set("User-Agent", "gh-inspect")
		if c.token != "" {
			req.Header.Set("Authorization", "Bearer "+c.token)
		}

		resp, err := c.httpClient.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return nil, nil, ctx.Err()
			}
			lastErr = err
			d := backoff(attempt)
			c.Logf("request error (%v), retrying in %s", err, d)
			if err := c.sleep(ctx, d); err != nil {
				return nil, nil, err
			}
			continue
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			lastErr = err
			continue
		}
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return body, resp.Header, nil
		}

		apiErr := &APIError{StatusCode: resp.StatusCode, URL: rawURL, Message: errorMessage(body)}
		if d, ok := retryDelay(resp, body, attempt); ok && attempt < c.MaxRetries {
			lastErr = apiErr
			c.Logf("%d from %s (%s); waiting %s before retry", resp.StatusCode, rawURL, apiErr.Message, d.Round(time.Second))
			if err := c.sleep(ctx, d); err != nil {
				return nil, nil, err
			}
			continue
		}
		return nil, nil, apiErr
	}
	return nil, nil, fmt.Errorf("giving up after %d retries: %w", c.MaxRetries, lastErr)
}

// GetJSON fetches a single object.
func (c *Client) GetJSON(ctx context.Context, path string, out any) error {
	body, _, err := c.get(ctx, c.resolve(path))
	if err != nil {
		return err
	}
	return json.Unmarshal(body, out)
}

// GetAll fetches every page of a list endpoint, following Link: rel="next".
func GetAll[T any](ctx context.Context, c *Client, path string) ([]T, error) {
	next := c.resolve(withPerPage(path))
	var all []T
	for next != "" {
		body, hdr, err := c.get(ctx, next)
		if err != nil {
			return all, err
		}
		var page []T
		if err := json.Unmarshal(body, &page); err != nil {
			return all, fmt.Errorf("decoding %s: %w", next, err)
		}
		all = append(all, page...)
		next = nextLink(hdr.Get("Link"))
	}
	return all, nil
}

func withPerPage(path string) string {
	u, err := url.Parse(path)
	if err != nil {
		return path
	}
	q := u.Query()
	if q.Get("per_page") == "" {
		q.Set("per_page", "100")
		u.RawQuery = q.Encode()
	}
	return u.String()
}

// nextLink extracts the rel="next" URL from a Link header.
func nextLink(link string) string {
	for _, part := range strings.Split(link, ",") {
		segs := strings.Split(strings.TrimSpace(part), ";")
		if len(segs) < 2 {
			continue
		}
		target := strings.Trim(strings.TrimSpace(segs[0]), "<>")
		for _, s := range segs[1:] {
			if strings.TrimSpace(s) == `rel="next"` {
				return target
			}
		}
	}
	return ""
}

func retryDelay(resp *http.Response, body []byte, attempt int) (time.Duration, bool) {
	switch {
	case resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests:
		if ra := resp.Header.Get("Retry-After"); ra != "" {
			if secs, err := strconv.Atoi(ra); err == nil {
				return time.Duration(max(secs, 1)) * time.Second, true
			}
		}
		if resp.Header.Get("X-RateLimit-Remaining") == "0" {
			if reset, err := strconv.ParseInt(resp.Header.Get("X-RateLimit-Reset"), 10, 64); err == nil {
				d := time.Until(time.Unix(reset, 0)) + 2*time.Second
				return max(d, 2*time.Second), true
			}
			return time.Minute, true
		}
		if strings.Contains(strings.ToLower(string(body)), "secondary rate limit") {
			// GitHub recommends waiting at least a minute, then backing off.
			return time.Minute * time.Duration(attempt+1), true
		}
		return 0, false
	case resp.StatusCode >= 500:
		return backoff(attempt), true
	}
	return 0, false
}

func backoff(attempt int) time.Duration {
	d := time.Duration(math.Pow(2, float64(attempt))) * time.Second
	return min(d, 60*time.Second)
}

func errorMessage(body []byte) string {
	var e struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &e) == nil && e.Message != "" {
		return e.Message
	}
	s := strings.TrimSpace(string(body))
	if len(s) > 200 {
		s = s[:200] + "..."
	}
	return s
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
