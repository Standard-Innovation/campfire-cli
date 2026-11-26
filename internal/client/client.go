// Package client provides an HTTP client for the Campfire API.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Standard-Innovation/campfire-cli/internal/errors"
	"github.com/basecamp/cli/output"
)

// Client is an HTTP client for the Campfire API.
type Client struct {
	BaseURL    string
	Token      string
	UserAgent  string
	HTTPClient *http.Client
	Verbose    bool
	// Sleeper is called for retry delays. Defaults to time.Sleep.
	// Override in tests with a no-op or recording function.
	Sleeper func(time.Duration)
}

// APIResponse represents a response from the API.
type APIResponse struct {
	StatusCode int
	Body       []byte
	Location   string
	LinkNext   string
	// TotalCount is the X-Total-Count header, or -1 when absent.
	TotalCount int
	Data       any
}

// New creates a new API client.
func New(baseURL, token string) *Client {
	return &Client{
		BaseURL:   strings.TrimSuffix(baseURL, "/"),
		Token:     token,
		UserAgent: "campfire-cli/dev",
		HTTPClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// buildURL constructs the full API URL. Absolute URLs (from Link headers) are
// only followed on the configured origin, so credentials never leave it.
func (c *Client) buildURL(path string) (string, error) {
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		if !sameOrigin(path, c.BaseURL) {
			return "", errors.NewError(fmt.Sprintf("Refusing to follow %s: not on %s", RedactBotKeys(path), c.BaseURL))
		}
		return path, nil
	}
	// Ensure path starts with /
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return c.BaseURL + path, nil
}

func sameOrigin(a, b string) bool {
	ua, errA := url.Parse(a)
	ub, errB := url.Parse(b)
	return errA == nil && errB == nil && ua.Scheme == ub.Scheme && strings.EqualFold(ua.Host, ub.Host)
}

// Get performs a GET request.
func (c *Client) Get(path string) (*APIResponse, error) {
	return c.request("GET", path, nil, true)
}

// Post performs a POST request with JSON body.
func (c *Client) Post(path string, body any) (*APIResponse, error) {
	return c.request("POST", path, body, false)
}

// Patch performs a PATCH request with JSON body.
func (c *Client) Patch(path string, body any) (*APIResponse, error) {
	return c.request("PATCH", path, body, false)
}

// Put performs a PUT request with JSON body.
func (c *Client) Put(path string, body any) (*APIResponse, error) {
	return c.request("PUT", path, body, true)
}

// PutOnce performs a PUT request that is never retried after a server or
// network error, for a change whose result is shown only once.
func (c *Client) PutOnce(path string, body any) (*APIResponse, error) {
	return c.request("PUT", path, body, false)
}

// Delete performs a DELETE request.
func (c *Client) Delete(path string) (*APIResponse, error) {
	return c.request("DELETE", path, nil, true)
}

// PostText performs a POST request with a plain-text body, as Campfire's bot API expects.
func (c *Client) PostText(path, text string) (*APIResponse, error) {
	return c.send("POST", path, strings.NewReader(text), "text/plain; charset=utf-8", false)
}

// request sends a JSON request. retry allows retrying after a 5xx or a
// network error; a 429 is always retried, as the server did nothing.
func (c *Client) request(method, path string, body any, retry bool) (*APIResponse, error) {
	if body == nil {
		return c.send(method, path, nil, "", retry)
	}
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, errors.NewError(fmt.Sprintf("Failed to marshal request body: %v", err))
	}
	return c.send(method, path, bytes.NewReader(jsonBody), "application/json", retry)
}

func (c *Client) send(method, path string, reqBody io.Reader, contentType string, retry bool) (*APIResponse, error) {
	requestURL, err := c.buildURL(path)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(context.Background(), method, requestURL, reqBody)
	if err != nil {
		return nil, errors.NewNetworkError(RedactBotKeys(fmt.Sprintf("Failed to create request: %v", err)))
	}

	c.setHeaders(req)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}

	if c.Verbose {
		fmt.Fprintf(os.Stderr, "> %s %s\n", method, RedactBotKeys(requestURL))
	}

	resp, err := c.doWithRetry(req, retry)
	if err != nil {
		return nil, errors.NewNetworkError(RedactBotKeys(fmt.Sprintf("Request failed: %v", err)))
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, errors.NewNetworkError(fmt.Sprintf("Failed to read response: %v", err))
	}

	if c.Verbose {
		fmt.Fprintf(os.Stderr, "< %d %s\n", resp.StatusCode, http.StatusText(resp.StatusCode))
	}

	apiResp := &APIResponse{
		StatusCode: resp.StatusCode,
		Body:       respBody,
		Location:   resp.Header.Get("Location"),
		LinkNext:   parseLinkNext(resp.Header.Get("Link")),
		TotalCount: parseTotalCount(resp.Header.Get("X-Total-Count")),
	}

	// Check for error status codes before parsing JSON,
	// since error responses may not be JSON (e.g. HTML 401 pages)
	if resp.StatusCode >= 400 {
		return apiResp, c.errorFromResponse(resp.StatusCode, respBody, resp.Header)
	}

	// Parse JSON body if present
	if len(respBody) > 0 {
		if err := json.Unmarshal(respBody, &apiResp.Data); err != nil {
			return apiResp, errors.NewError(fmt.Sprintf("Failed to parse JSON response: %v", err))
		}
	}

	return apiResp, nil
}

func (c *Client) setHeaders(req *http.Request) {
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.UserAgent)
}

func (c *Client) sleep(d time.Duration) {
	if c.Sleeper != nil {
		c.Sleeper(d)
	} else {
		time.Sleep(d)
	}
}

const maxRetries = 3

var linkNextRe = regexp.MustCompile(`<([^>]+)>;\s*rel="next"`)

// doWithRetry wraps HTTPClient.Do with retry logic for 429 and 5xx responses.
// With retry set it also retries on 5xx or a network error; without it, only on 429.
func (c *Client) doWithRetry(req *http.Request, retry bool) (*http.Response, error) {
	for attempt := range maxRetries + 1 {
		resp, err := c.HTTPClient.Do(req)

		// Network error: retry with backoff when allowed
		if err != nil {
			if attempt < maxRetries && retry {
				c.sleep(time.Duration(1<<uint(attempt)) * time.Second)
				resetBody(req)
				continue
			}
			return nil, err
		}

		// 429: always retry (server explicitly says "try again")
		if resp.StatusCode == 429 {
			if attempt < maxRetries {
				delay := parseRetryAfter(resp.Header.Get("Retry-After"))
				_ = resp.Body.Close()
				c.sleep(delay)
				resetBody(req)
				continue
			}
		}

		// 5xx: retry with exponential backoff when allowed
		if resp.StatusCode >= 500 && retry && attempt < maxRetries {
			_ = resp.Body.Close()
			c.sleep(time.Duration(1<<uint(attempt)) * time.Second)
			resetBody(req)
			continue
		}

		return resp, nil
	}

	// unreachable, but the compiler needs it
	return c.HTTPClient.Do(req)
}

// resetBody rewinds the request body for retry. Uses GetBody (set automatically
// by http.NewRequestWithContext for *bytes.Reader, *bytes.Buffer, *strings.Reader).
func resetBody(req *http.Request) {
	if req.GetBody != nil {
		req.Body, _ = req.GetBody()
	}
}

// parseRetryAfter parses the Retry-After header value as seconds.
// Returns a default of 1 second if the header is missing or unparseable.
func parseRetryAfter(value string) time.Duration {
	const maxRetryDelay = 300 // 5 minutes; anything larger is almost certainly bogus

	if value == "" {
		return time.Second
	}
	if seconds, err := strconv.Atoi(value); err == nil && seconds > 0 {
		if seconds > maxRetryDelay {
			seconds = maxRetryDelay
		}
		return time.Duration(seconds) * time.Second
	}
	// Try HTTP-date format
	if t, err := http.ParseTime(value); err == nil {
		delay := time.Until(t)
		if delay > 0 {
			if delay > maxRetryDelay*time.Second {
				return maxRetryDelay * time.Second
			}
			return delay
		}
	}
	return time.Second
}

func (c *Client) errorFromResponse(status int, body []byte, header http.Header) error {
	// Try to parse error message from response
	var errResp struct {
		Error string `json:"error"`
	}

	message := http.StatusText(status)
	if json.Unmarshal(body, &errResp) == nil && errResp.Error != "" {
		message = errResp.Error
	}

	if status == 429 {
		retryAfter := 0
		if ra := header.Get("Retry-After"); ra != "" {
			if seconds, err := strconv.Atoi(ra); err == nil {
				retryAfter = seconds
			}
		}
		e := errors.FromHTTPStatus(429, message)
		if retryAfter > 0 {
			e = output.ErrRateLimit(retryAfter)
			if message != "" && message != http.StatusText(http.StatusTooManyRequests) {
				e.Message = message
			}
		}
		return e
	}

	return errors.FromHTTPStatus(status, message)
}

// parseLinkNext extracts the "next" URL from a Link header.
func parseLinkNext(linkHeader string) string {
	if linkHeader == "" {
		return ""
	}
	matches := linkNextRe.FindStringSubmatch(linkHeader)
	if len(matches) > 1 {
		return matches[1]
	}
	return ""
}

func parseTotalCount(value string) int {
	if n, err := strconv.Atoi(strings.TrimSpace(value)); err == nil && n >= 0 {
		return n
	}
	return -1
}

// GetWithPagination fetches all pages of a paginated endpoint.
func (c *Client) GetWithPagination(path string, fetchAll bool) (*APIResponse, error) {
	resp, err := c.Get(path)
	if err != nil {
		return resp, err
	}

	if !fetchAll || resp.LinkNext == "" {
		return resp, nil
	}

	// Collect all data
	var allData []any
	if arr, ok := resp.Data.([]any); ok {
		allData = append(allData, arr...)
	}

	// Fetch remaining pages
	nextURL := resp.LinkNext
	for nextURL != "" {
		pageResp, err := c.Get(nextURL)
		if err != nil {
			return nil, err
		}

		if arr, ok := pageResp.Data.([]any); ok {
			allData = append(allData, arr...)
		}

		nextURL = pageResp.LinkNext
	}

	resp.Data = allData
	resp.LinkNext = ""
	return resp, nil
}
