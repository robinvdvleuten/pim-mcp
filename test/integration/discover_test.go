//go:build integration

package integration

import (
	"context"
	"net"
	"net/http"
	"strings"
	"testing"

	"github.com/robinvdvleuten/pim-mcp/internal/account"
)

type noDNS struct{}

func (noDNS) LookupSRV(context.Context, string, string, string) (string, []*net.SRV, error) {
	return "", nil, &net.DNSError{Err: "no such host", IsNotFound: true}
}

func (noDNS) LookupTXT(context.Context, string) ([]string, error) {
	return nil, &net.DNSError{Err: "no such host", IsNotFound: true}
}

// toLocal lets a plain HTTP server on this machine answer discovery's
// HTTPS-only requests for example.org. The server sees that host, and thinks
// it's on plain HTTP, so its redirects are put back to HTTPS.
type toLocal struct {
	addr string
}

func (l toLocal) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Host != "example.org" {
		return &http.Response{StatusCode: http.StatusNotFound, Body: http.NoBody, Request: req}, nil
	}
	req = req.Clone(req.Context())
	req.Host = "example.org"
	req.URL.Scheme = "http"
	req.URL.Host = l.addr
	resp, err := http.DefaultTransport.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	if location := resp.Header.Get("Location"); location != "" {
		resp.Header.Set("Location", strings.Replace(location, "http://example.org", "https://example.org", 1))
	}
	return resp, nil
}

func TestDiscoveryFindsRealCalDAVServers(t *testing.T) {
	tests := []struct {
		name string
		addr string
		want string
	}{
		{"radicale", hostOf(radicaleURL), "https://example.org/"},
		{"baikal", hostOf(baikalURL), "https://example.org/dav.php"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := &account.Discoverer{Resolver: noDNS{}, Transport: toLocal{tt.addr}}

			found, err := d.DiscoverCalDAV(t.Context(), "tester@example.org")
			if err != nil {
				t.Fatalf("discover: %v", err)
			}

			if found.Source != account.SourceWellKnown {
				t.Errorf("source = %q", found.Source)
			}
			if found.CalDAV.URL != tt.want {
				t.Errorf("url = %q, want %q", found.CalDAV.URL, tt.want)
			}
		})
	}
}
