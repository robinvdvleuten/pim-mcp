package calendar

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/robinvdvleuten/pim-mcp/internal/ical"
)

// storeServer holds one event and behaves like a CalDAV server about ETags.
type storeServer struct {
	etag    string
	data    string
	puts    int
	deletes int
	headers http.Header
	client  *Client
}

func newStoreServer(t *testing.T, data string) *storeServer {
	t.Helper()
	store := &storeServer{etag: "v1", data: data}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			if store.data == "" {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Header().Set("ETag", `"`+store.etag+`"`)
			_, _ = w.Write([]byte(store.data))
		case http.MethodPut:
			store.puts++
			store.headers = r.Header.Clone()
			if r.Header.Get("If-Match") != `"`+store.etag+`"` {
				w.WriteHeader(http.StatusPreconditionFailed)
				return
			}
			body, _ := io.ReadAll(r.Body)
			store.data = string(body)
			store.etag = "v2"
			w.Header().Set("ETag", `"v2"`)
			w.WriteHeader(http.StatusNoContent)
		case http.MethodDelete:
			store.deletes++
			store.headers = r.Header.Clone()
			if r.Header.Get("If-Match") != `"`+store.etag+`"` {
				w.WriteHeader(http.StatusPreconditionFailed)
				return
			}
			store.data = ""
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	t.Cleanup(server.Close)
	client, err := New(Config{URL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	store.client = client
	return store
}

const storedSingle = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//t//t//EN\r\nBEGIN:VEVENT\r\nUID:one\r\nDTSTAMP:20261001T120000Z\r\nDTSTART:20261110T090000Z\r\nDTEND:20261110T100000Z\r\nSUMMARY:Old\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"

func newSummary(s string) ical.EventPatch { return ical.EventPatch{Summary: &s} }

func TestUpdateEventWritesWithTheETagItWasGiven(t *testing.T) {
	store := newStoreServer(t, storedSingle)

	got, err := store.client.UpdateEvent(t.Context(), "/cal/one.ics", "v1", ScopeSeries, "", newSummary("New"), time.UTC)
	if err != nil {
		t.Fatalf("update: %v", err)
	}

	if got.ETag != "v2" || got.EventID != "/cal/one.ics" {
		t.Errorf("got %+v", got)
	}
	if got.First == nil || got.First.Summary != "New" {
		t.Errorf("first = %+v", got.First)
	}
	if !strings.Contains(store.data, "SUMMARY:New") {
		t.Errorf("stored %s", store.data)
	}
	if store.headers.Get("If-Match") != `"v1"` || store.headers.Get("Schedule-Reply") != "F" {
		t.Errorf("headers = %v", store.headers)
	}
}

func TestUpdateEventWithStaleETagWritesNothing(t *testing.T) {
	// The caller read v0 and someone has changed it since.
	store := newStoreServer(t, storedSingle)

	_, err := store.client.UpdateEvent(t.Context(), "/cal/one.ics", "v0", ScopeSeries, "", newSummary("New"), time.UTC)

	if !errors.Is(err, ErrConflict) {
		t.Errorf("err = %v, want ErrConflict", err)
	}
	if store.puts != 0 || !strings.Contains(store.data, "SUMMARY:Old") {
		t.Errorf("puts = %d, stored %s", store.puts, store.data)
	}
}

func TestUpdateEventNeedsAnETagAndAKnownScope(t *testing.T) {
	store := newStoreServer(t, storedSingle)

	if _, err := store.client.UpdateEvent(t.Context(), "/cal/one.ics", "", ScopeSeries, "", newSummary("New"), time.UTC); err == nil {
		t.Error("update without an etag passed")
	}
	if _, err := store.client.UpdateEvent(t.Context(), "/cal/one.ics", "v1", "everything", "", newSummary("New"), time.UTC); err == nil {
		t.Error("update with an unknown scope passed")
	}
	if store.puts != 0 {
		t.Errorf("puts = %d", store.puts)
	}
}

func TestUpdateEventThatIsGone(t *testing.T) {
	store := newStoreServer(t, "")

	_, err := store.client.UpdateEvent(t.Context(), "/cal/one.ics", "v1", ScopeSeries, "", newSummary("New"), time.UTC)

	if !errors.Is(err, ErrEventNotFound) {
		t.Errorf("err = %v, want ErrEventNotFound", err)
	}
}

func TestDeleteEvent(t *testing.T) {
	t.Run("current etag", func(t *testing.T) {
		store := newStoreServer(t, storedSingle)

		if _, err := store.client.DeleteEvent(t.Context(), "/cal/one.ics", "v1", ScopeSeries, ""); err != nil {
			t.Fatalf("delete: %v", err)
		}

		if store.data != "" || store.headers.Get("If-Match") != `"v1"` || store.headers.Get("Schedule-Reply") != "F" {
			t.Errorf("stored %q, headers %v", store.data, store.headers)
		}
	})
	t.Run("stale etag", func(t *testing.T) {
		store := newStoreServer(t, storedSingle)

		_, err := store.client.DeleteEvent(t.Context(), "/cal/one.ics", "v0", ScopeSeries, "")

		if !errors.Is(err, ErrConflict) || store.data == "" {
			t.Errorf("err = %v, stored %q", err, store.data)
		}
	})
	t.Run("no etag", func(t *testing.T) {
		store := newStoreServer(t, storedSingle)

		if _, err := store.client.DeleteEvent(t.Context(), "/cal/one.ics", "", ScopeSeries, ""); err == nil {
			t.Error("delete without an etag passed")
		}
		if store.deletes != 0 {
			t.Errorf("deletes = %d", store.deletes)
		}
	})
}
