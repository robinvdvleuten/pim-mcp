// Package calendar reads and writes events over CalDAV.
package calendar

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/robinvdvleuten/pim-mcp/internal/transport"
)

// ErrConflict means the event changed on the server since it was read.
var ErrConflict = errors.New("conflict: event changed elsewhere, re-read it")

const maxRedirects = 10

// Config holds what is needed to reach a CalDAV server.
type Config struct {
	URL      string
	Username string
	Password string
}

// Client talks to one CalDAV server.
//
// Writes use net/http directly because go-webdav's PutCalendarObject can't send
// preconditions and hides the status code in an internal error type.
type Client struct {
	base     *url.URL
	http     *http.Client
	username string
	password string
}

// Object is a stored calendar resource. ETag is unquoted, as go-webdav's
// queries return it.
type Object struct {
	Path string
	ETag string
	Data []byte
}

// New returns a client for the server at cfg.URL.
func New(cfg Config) (*Client, error) {
	base, err := url.Parse(cfg.URL)
	if err != nil {
		return nil, errors.New("parse CalDAV URL")
	}
	if err := transport.CheckURL(base); err != nil {
		return nil, err
	}
	return &Client{
		base:     base,
		http:     &http.Client{CheckRedirect: checkRedirect},
		username: cfg.Username,
		password: cfg.Password,
	}, nil
}

// checkRedirect applies the transport rule to redirects too, because
// net/http keeps credentials when a redirect only downgrades the scheme.
func checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= maxRedirects {
		return fmt.Errorf("stopped after %d redirects", maxRedirects)
	}
	return transport.CheckURL(req.URL)
}

// Get returns the stored resource at path.
func (c *Client) Get(ctx context.Context, path string) (*Object, error) {
	req, err := c.newRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("get %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("get %s: %s", path, resp.Status)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("get %s: %w", path, err)
	}
	return &Object{Path: path, ETag: unquoteETag(resp.Header.Get("ETag")), Data: data}, nil
}

// Create stores a new resource and returns its ETag. It returns ErrConflict if
// the path is already taken.
func (c *Client) Create(ctx context.Context, path string, data []byte) (string, error) {
	return c.put(ctx, path, data, "If-None-Match", "*")
}

// Update replaces the resource at path if it still has the given ETag, and
// returns the new ETag. It returns ErrConflict if the resource has changed.
func (c *Client) Update(ctx context.Context, path, etag string, data []byte) (string, error) {
	if etag == "" {
		return "", fmt.Errorf("update %s: an etag is required", path)
	}
	return c.put(ctx, path, data, "If-Match", `"`+etag+`"`)
}

func (c *Client) put(ctx context.Context, path string, data []byte, precondition, value string) (string, error) {
	req, err := c.newRequest(ctx, http.MethodPut, path, bytes.NewReader(data))
	if err != nil {
		return "", err
	}
	req.Header.Set(precondition, value)
	req.Header.Set("Content-Type", "text/calendar; charset=utf-8")
	// pim-mcp never sends mail, so the server shouldn't send replies for it
	// (RFC 6638).
	req.Header.Set("Schedule-Reply", "F")

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("put %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)

	switch {
	case resp.StatusCode == http.StatusPreconditionFailed:
		return "", fmt.Errorf("put %s: %w", path, ErrConflict)
	case resp.StatusCode/100 != 2:
		return "", fmt.Errorf("put %s: %s", path, resp.Status)
	}

	if etag := unquoteETag(resp.Header.Get("ETag")); etag != "" {
		return etag, nil
	}
	// A server that altered the data sends no ETag (RFC 4791 5.3.4).
	object, err := c.Get(ctx, path)
	if err != nil {
		return "", fmt.Errorf("read back etag: %w", err)
	}
	return object.ETag, nil
}

func (c *Client) newRequest(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	target := c.base.ResolveReference(&url.URL{Path: path})
	req, err := http.NewRequestWithContext(ctx, method, target.String(), body)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", method, path, err)
	}
	if c.username != "" {
		req.SetBasicAuth(c.username, c.password)
	}
	return req, nil
}

// unquoteETag drops a weak marker too. A weak ETag then fails If-Match, which
// is the safe outcome: the write reports a conflict instead of overwriting.
func unquoteETag(etag string) string {
	return strings.Trim(strings.TrimPrefix(etag, "W/"), `"`)
}
