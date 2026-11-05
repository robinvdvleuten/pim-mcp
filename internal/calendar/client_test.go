package calendar

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/robinvdvleuten/pim-mcp/internal/transport"
)

func TestNewRefusesPlainHTTPOffLoopback(t *testing.T) {
	urls := []string{
		"http://dav.example.org/",
		"http://192.0.2.1/",
		"http://localhost.example.org/",
	}

	for _, url := range urls {
		t.Run(url, func(t *testing.T) {
			_, err := New(Config{URL: url})

			if !errors.Is(err, transport.ErrInsecure) {
				t.Errorf("err = %v, want transport.ErrInsecure", err)
			}
		})
	}
}

func TestNewAcceptsHTTPSAndLoopback(t *testing.T) {
	for _, url := range []string{"https://dav.example.org/", "http://127.0.0.1:5232/", "http://localhost:5232/"} {
		if _, err := New(Config{URL: url}); err != nil {
			t.Errorf("New(%q) = %v, want no error", url, err)
		}
	}
}

func TestRedirectToPlainHTTPIsRefused(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://dav.example.org/stolen", http.StatusTemporaryRedirect)
	}))
	t.Cleanup(server.Close)
	client, err := New(Config{URL: server.URL, Username: "u", Password: "secret"})
	if err != nil {
		t.Fatal(err)
	}

	_, err = client.Get(t.Context(), "/event.ics")

	if !errors.Is(err, transport.ErrInsecure) {
		t.Errorf("err = %v, want transport.ErrInsecure", err)
	}
}

func TestWriteHeaders(t *testing.T) {
	const etag = `abc`
	tests := []struct {
		name            string
		write           func(*Client, *testing.T) error
		wantIfMatch     string
		wantIfNoneMatch string
	}{
		{
			name: "create refuses to overwrite",
			write: func(c *Client, t *testing.T) error {
				_, err := c.Create(t.Context(), "/event.ics", []byte("data"))
				return err
			},
			wantIfNoneMatch: "*",
		},
		{
			name: "update requires the etag it read",
			write: func(c *Client, t *testing.T) error {
				_, err := c.Update(t.Context(), "/event.ics", etag, []byte("data"))
				return err
			},
			wantIfMatch: `"abc"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got http.Header
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				got = r.Header.Clone()
				w.Header().Set("ETag", `"new"`)
				w.WriteHeader(http.StatusCreated)
			}))
			t.Cleanup(server.Close)
			client, err := New(Config{URL: server.URL})
			if err != nil {
				t.Fatal(err)
			}

			if err := tt.write(client, t); err != nil {
				t.Fatalf("write: %v", err)
			}

			if v := got.Get("If-Match"); v != tt.wantIfMatch {
				t.Errorf("If-Match = %q, want %q", v, tt.wantIfMatch)
			}
			if v := got.Get("If-None-Match"); v != tt.wantIfNoneMatch {
				t.Errorf("If-None-Match = %q, want %q", v, tt.wantIfNoneMatch)
			}
			if v := got.Get("Schedule-Reply"); v != "F" {
				t.Errorf("Schedule-Reply = %q, want %q", v, "F")
			}
			if v := got.Get("Content-Type"); v != "text/calendar; charset=utf-8" {
				t.Errorf("Content-Type = %q", v)
			}
		})
	}
}

func TestWriteReadsETagBackWhenResponseOmitsIt(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// RFC 4791 5.3.4: a server that alters the data sends no ETag on PUT.
		if r.Method == http.MethodPut {
			w.WriteHeader(http.StatusCreated)
			return
		}
		w.Header().Set("ETag", `"after-get"`)
		_, _ = w.Write([]byte("data"))
	}))
	t.Cleanup(server.Close)
	client, err := New(Config{URL: server.URL})
	if err != nil {
		t.Fatal(err)
	}

	etag, err := client.Create(t.Context(), "/event.ics", []byte("data"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	if etag != "after-get" {
		t.Errorf("etag = %q, want %q", etag, "after-get")
	}
}

func TestWritePreconditionFailedIsConflict(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusPreconditionFailed)
	}))
	t.Cleanup(server.Close)
	client, err := New(Config{URL: server.URL})
	if err != nil {
		t.Fatal(err)
	}

	_, err = client.Update(t.Context(), "/event.ics", "stale", []byte("data"))

	if !errors.Is(err, ErrConflict) {
		t.Errorf("err = %v, want ErrConflict", err)
	}
}

func TestUpdateWithoutETagIsRefused(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests++ }))
	t.Cleanup(server.Close)
	client, err := New(Config{URL: server.URL})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := client.Update(t.Context(), "/event.ics", "", []byte("data")); err == nil {
		t.Error("update without an etag succeeded")
	}
	if requests != 0 {
		t.Errorf("sent %d requests, want none", requests)
	}
}
