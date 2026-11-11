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
		if serveDiscovery(w, r, wideHome) {
			return
		}
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

		if _, err := store.client.DeleteEvent(t.Context(), "/cal/one.ics", "v1", ScopeSeries, "", time.UTC); err != nil {
			t.Fatalf("delete: %v", err)
		}

		if store.data != "" || store.headers.Get("If-Match") != `"v1"` || store.headers.Get("Schedule-Reply") != "F" {
			t.Errorf("stored %q, headers %v", store.data, store.headers)
		}
	})
	t.Run("stale etag", func(t *testing.T) {
		store := newStoreServer(t, storedSingle)

		_, err := store.client.DeleteEvent(t.Context(), "/cal/one.ics", "v0", ScopeSeries, "", time.UTC)

		if !errors.Is(err, ErrConflict) || store.data == "" {
			t.Errorf("err = %v, stored %q", err, store.data)
		}
	})
	t.Run("no etag", func(t *testing.T) {
		store := newStoreServer(t, storedSingle)

		if _, err := store.client.DeleteEvent(t.Context(), "/cal/one.ics", "", ScopeSeries, "", time.UTC); err == nil {
			t.Error("delete without an etag passed")
		}
		if store.deletes != 0 {
			t.Errorf("deletes = %d", store.deletes)
		}
	})
}

const storedWeekly = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//t//t//EN\r\nBEGIN:VEVENT\r\nUID:one\r\nDTSTAMP:20261001T120000Z\r\nDTSTART:20261102T090000Z\r\nDTEND:20261102T100000Z\r\nRRULE:FREQ=WEEKLY;COUNT=3\r\nSUMMARY:Old\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"

func TestScopeAndOccurrenceIDMustAgree(t *testing.T) {
	// An agent that means one occurrence must never get a whole series removed.
	tests := []struct {
		name         string
		scope        string
		occurrenceID string
	}{
		{"series with an occurrence id", ScopeSeries, "20261109T090000Z"},
		{"occurrence without an id", ScopeOccurrence, ""},
		{"unknown scope", "all", ""},
		{"empty scope", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newStoreServer(t, storedWeekly)

			if _, err := store.client.DeleteEvent(t.Context(), "/cal/one.ics", "v1", tt.scope, tt.occurrenceID, time.UTC); err == nil {
				t.Error("delete passed")
			}
			if _, err := store.client.UpdateEvent(t.Context(), "/cal/one.ics", "v1", tt.scope, tt.occurrenceID, newSummary("New"), time.UTC); err == nil {
				t.Error("update passed")
			}
			if store.deletes != 0 || store.puts != 0 {
				t.Errorf("deletes = %d, puts = %d", store.deletes, store.puts)
			}
		})
	}
}

func TestUpdateOccurrence(t *testing.T) {
	store := newStoreServer(t, storedWeekly)

	got, err := store.client.UpdateEvent(t.Context(), "/cal/one.ics", "v1", ScopeOccurrence, "20261109T090000Z", newSummary("One"), time.UTC)
	if err != nil {
		t.Fatalf("update: %v", err)
	}

	if got.ETag != "v2" || !strings.Contains(store.data, "RECURRENCE-ID:20261109T090000Z") {
		t.Errorf("got %+v, stored %s", got, store.data)
	}
	if store.headers.Get("If-Match") != `"v1"` {
		t.Errorf("headers = %v", store.headers)
	}
}

func TestDeleteOccurrenceKeepsTheRest(t *testing.T) {
	store := newStoreServer(t, storedWeekly)

	got, err := store.client.DeleteEvent(t.Context(), "/cal/one.ics", "v1", ScopeOccurrence, "20261109T090000Z", time.UTC)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}

	if got.ETag != "v2" || got.Deleted != ScopeOccurrence || store.deletes != 0 {
		t.Errorf("got %+v, deletes %d", got, store.deletes)
	}
	if !strings.Contains(store.data, "EXDATE:20261109T090000Z") || !strings.Contains(store.data, "RRULE") {
		t.Errorf("stored %s", store.data)
	}
}

func TestDeleteOccurrenceWithStaleETag(t *testing.T) {
	store := newStoreServer(t, storedWeekly)

	_, err := store.client.DeleteEvent(t.Context(), "/cal/one.ics", "v0", ScopeOccurrence, "20261109T090000Z", time.UTC)

	if !errors.Is(err, ErrConflict) || store.puts != 0 {
		t.Errorf("err = %v, puts = %d", err, store.puts)
	}
}

func TestDeleteLastOverrideRemovesTheObject(t *testing.T) {
	store := newStoreServer(t, strings.Replace(storedSingle, "DTSTART:", "RECURRENCE-ID:20261110T090000Z\r\nDTSTART:", 1))

	got, err := store.client.DeleteEvent(t.Context(), "/cal/one.ics", "v1", ScopeOccurrence, "20261110T090000Z", time.UTC)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}

	if store.deletes != 1 || store.data != "" || got.ETag != "" {
		t.Errorf("deletes = %d, stored %q, got %+v", store.deletes, store.data, got)
	}
}

func TestDeleteOccurrenceOfFloatingSeriesUsesTheUsersZone(t *testing.T) {
	// The ids from a listing were worked out in the user's zone.
	tokyo, err := time.LoadLocation("Asia/Tokyo")
	if err != nil {
		t.Fatal(err)
	}
	store := newStoreServer(t, strings.NewReplacer("DTSTART:20261102T090000Z", "DTSTART:20261102T090000", "DTEND:20261102T100000Z", "DTEND:20261102T100000").Replace(storedWeekly))

	if _, err := store.client.DeleteEvent(t.Context(), "/cal/one.ics", "v1", ScopeOccurrence, "20261109T000000Z", tokyo); err != nil {
		t.Fatalf("delete: %v", err)
	}

	if !strings.Contains(store.data, "EXDATE:20261109T090000\r\n") {
		t.Errorf("stored %s", store.data)
	}
}

func TestUpdateOfADeletedEventSaysItIsNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveDiscovery(w, r, wideHome) {
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(server.Close)
	client, err := New(Config{URL: server.URL + "/"})
	if err != nil {
		t.Fatal(err)
	}

	_, err = client.Update(t.Context(), "/cal/me/e.ics", "v1", []byte("data"))
	if !errors.Is(err, ErrEventNotFound) {
		t.Errorf("update: err = %v, want ErrEventNotFound", err)
	}
	// A create into a calendar that isn't there is not about an event.
	_, err = client.Create(t.Context(), "/cal/me/e.ics", []byte("data"))
	if err == nil || errors.Is(err, ErrEventNotFound) {
		t.Errorf("create: err = %v", err)
	}
}

func TestCreateRecurringEvent(t *testing.T) {
	var header http.Header
	var body string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveDiscovery(w, r, wideHome) {
			return
		}
		data, _ := io.ReadAll(r.Body)
		header, body = r.Header.Clone(), string(data)
		w.Header().Set("ETag", `"e1"`)
		w.WriteHeader(http.StatusCreated)
	}))
	t.Cleanup(server.Close)
	client, err := New(Config{URL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	amsterdam, _ := time.LoadLocation("Europe/Amsterdam")
	fields := ical.EventFields{
		Summary: "Standup", Recurrence: "FREQ=WEEKLY;BYDAY=MO", Zone: amsterdam,
		Start: time.Date(2026, 10, 19, 9, 0, 0, 0, amsterdam), End: time.Date(2026, 10, 19, 9, 30, 0, 0, amsterdam),
	}

	got, err := client.CreateEvent(t.Context(), "/dav/home/user/work/", fields, time.UTC)

	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if header.Get("If-None-Match") != "*" {
		t.Errorf("If-None-Match = %q", header.Get("If-None-Match"))
	}
	if !strings.Contains(body, "DTSTART;TZID=Europe/Amsterdam:20261019T090000") || !strings.Contains(body, "BEGIN:VTIMEZONE") {
		t.Errorf("stored:\n%s", body)
	}
	if !got.Recurring || !got.Start.Equal(fields.Start) || got.ETag != "e1" {
		t.Errorf("event = %+v", got)
	}
}

func TestCreateRecurringEventWithABadRuleSendsNothing(t *testing.T) {
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests++ }))
	t.Cleanup(server.Close)
	client, err := New(Config{URL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	amsterdam, _ := time.LoadLocation("Europe/Amsterdam")
	start := time.Date(2026, 10, 20, 9, 0, 0, 0, amsterdam) // A Tuesday.

	for name, fields := range map[string]ical.EventFields{
		"another weekday": {Summary: "x", Start: start, End: start.Add(time.Hour), Zone: amsterdam, Recurrence: "FREQ=WEEKLY;BYDAY=MO"},
		"no zone":         {Summary: "x", Start: start, End: start.Add(time.Hour), Recurrence: "FREQ=WEEKLY;BYDAY=TU"},
		"hourly":          {Summary: "x", Start: start, End: start.Add(time.Hour), Zone: amsterdam, Recurrence: "FREQ=HOURLY"},
	} {
		if _, err := client.CreateEvent(t.Context(), "/dav/home/user/work/", fields, time.UTC); err == nil {
			t.Errorf("%s: create succeeded", name)
		}
	}
	if requests != 0 {
		t.Errorf("sent %d requests", requests)
	}
}
