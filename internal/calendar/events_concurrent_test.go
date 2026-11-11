package calendar

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// concurrentServer answers every REPORT with the response for its calendar
// after behave has run, and counts how many requests are in flight at once.
type concurrentServer struct {
	client      *Client
	inFlight    atomic.Int32
	maxInFlight atomic.Int32
}

func newConcurrentServer(t *testing.T, responses map[string]string, behave func(w http.ResponseWriter, r *http.Request) bool) *concurrentServer {
	t.Helper()
	s := &concurrentServer{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveDiscovery(w, r, wideHome) {
			return
		}
		now := s.inFlight.Add(1)
		defer s.inFlight.Add(-1)
		for {
			highest := s.maxInFlight.Load()
			if now <= highest || s.maxInFlight.CompareAndSwap(highest, now) {
				break
			}
		}
		if behave != nil && !behave(w, r) {
			return
		}
		w.WriteHeader(http.StatusMultiStatus)
		_, _ = w.Write([]byte(multistatusHead + responses[r.URL.Path] + "</d:multistatus>"))
	}))
	t.Cleanup(server.Close)
	client, err := New(Config{URL: server.URL + "/"})
	if err != nil {
		t.Fatal(err)
	}
	s.client = client
	return s
}

// meet blocks each request until n have arrived, and fails the test if they
// don't within a few seconds, which is what a sequential caller would cause.
func meet(t *testing.T, n int32) func(http.ResponseWriter, *http.Request) bool {
	t.Helper()
	var arrived atomic.Int32
	all := make(chan struct{})
	return func(http.ResponseWriter, *http.Request) bool {
		if arrived.Add(1) == n {
			close(all)
		}
		select {
		case <-all:
		case <-time.After(5 * time.Second):
			t.Errorf("fewer than %d requests were in flight together", n)
		}
		return true
	}
}

func TestEventsQueriesCalendarsTogether(t *testing.T) {
	// One REPORT per calendar for the range, and one for changed occurrences.
	s := newConcurrentServer(t, nil, meet(t, int32(6)))

	_, err := s.client.Events(t.Context(), []string{"/cal/a/", "/cal/b/", "/cal/c/"}, rangeStart, rangeEnd, time.UTC)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
}

func TestEventsRunsABoundedNumberOfCalendarsAtOnce(t *testing.T) {
	s := newConcurrentServer(t, nil, func(http.ResponseWriter, *http.Request) bool {
		time.Sleep(20 * time.Millisecond)
		return true
	})
	var ids []string
	for _, name := range []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j"} {
		ids = append(ids, "/cal/"+name+"/")
	}

	if _, err := s.client.Events(t.Context(), ids, rangeStart, rangeEnd, time.UTC); err != nil {
		t.Fatalf("events: %v", err)
	}

	// Each calendar sends two requests.
	if got := s.maxInFlight.Load(); got > 2*maxCalendarsInFlight || got < 2 {
		t.Errorf("most requests in flight = %d, want 2 to %d", got, 2*maxCalendarsInFlight)
	}
}

func TestEventsKeepsCalendarOrderWhateverFinishesFirst(t *testing.T) {
	broken := func(name string) string {
		return eventResponse("/cal/"+name+"/1-bad.ics", "1", "not a calendar") +
			eventResponse("/cal/"+name+"/2-bad.ics", "2", "not a calendar") +
			eventResponse("/cal/"+name+"/ok.ics", "3", icsEvent(name, "20261110T120000Z"))
	}
	delays := map[string]time.Duration{"/cal/a/": 80 * time.Millisecond, "/cal/b/": 40 * time.Millisecond, "/cal/c/": 0}
	s := newConcurrentServer(t, map[string]string{"/cal/a/": broken("a"), "/cal/b/": broken("b"), "/cal/c/": broken("c")},
		func(_ http.ResponseWriter, r *http.Request) bool {
			time.Sleep(delays[r.URL.Path])
			return true
		})

	got, err := s.client.Events(t.Context(), []string{"/cal/a/", "/cal/b/", "/cal/c/"}, rangeStart, rangeEnd, time.UTC)
	if err != nil {
		t.Fatalf("events: %v", err)
	}

	var skipped []string
	for _, skip := range got.Skipped {
		skipped = append(skipped, skip.EventID)
	}
	want := "/cal/a/1-bad.ics /cal/a/2-bad.ics /cal/b/1-bad.ics /cal/b/2-bad.ics /cal/c/1-bad.ics /cal/c/2-bad.ics"
	if strings.Join(skipped, " ") != want {
		t.Errorf("skipped = %v", skipped)
	}
	if len(got.Events) != 3 || got.Events[0].UID != "a" || got.Events[1].UID != "b" || got.Events[2].UID != "c" {
		t.Errorf("events = %+v", got.Events)
	}
}

func TestEventsReportsTheFirstErrorInCalendarOrderAndCancelsTheRest(t *testing.T) {
	var cancelled atomic.Int32
	s := newConcurrentServer(t, nil, func(w http.ResponseWriter, r *http.Request) bool {
		switch r.URL.Path {
		case "/cal/a/":
			// Fails later than c, but comes first in the order given.
			time.Sleep(60 * time.Millisecond)
			w.WriteHeader(http.StatusInternalServerError)
		case "/cal/c/":
			w.WriteHeader(http.StatusBadGateway)
		default:
			// The server only notices a hang-up once the body is read.
			_, _ = io.Copy(io.Discard, r.Body)
			select {
			case <-r.Context().Done():
				cancelled.Add(1)
			case <-time.After(5 * time.Second):
			}
		}
		return false
	})

	_, err := s.client.Events(t.Context(), []string{"/cal/a/", "/cal/b/", "/cal/c/"}, rangeStart, rangeEnd, time.UTC)

	if err == nil || !strings.Contains(err.Error(), "/cal/a/") {
		t.Errorf("err = %v, want the error of /cal/a/", err)
	}
	// Give the server a moment to see the client hang up.
	deadline := time.Now().Add(2 * time.Second)
	for cancelled.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if cancelled.Load() == 0 {
		t.Error("the request still in flight was not cancelled")
	}
}

func TestEventsErrorInOneCalendarSurfaces(t *testing.T) {
	s := newConcurrentServer(t, nil, func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path == "/cal/b/" {
			w.WriteHeader(http.StatusInternalServerError)
			return false
		}
		return true
	})

	_, err := s.client.Events(t.Context(), []string{"/cal/a/", "/cal/b/"}, rangeStart, rangeEnd, time.UTC)

	if err == nil || !strings.Contains(err.Error(), "/cal/b/") {
		t.Errorf("err = %v", err)
	}
}

func TestEventsReturnsTheCallersCancellation(t *testing.T) {
	s := newConcurrentServer(t, nil, nil)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := s.client.Events(ctx, []string{"/cal/a/", "/cal/b/"}, rangeStart, rangeEnd, time.UTC)

	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want a cancellation", err)
	}
}
