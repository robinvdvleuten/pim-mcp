package calendar

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/robinvdvleuten/pim-mcp/internal/ical"
)

const scopeHome = "/dav/home/user/"

// wideHome is the home for tests that aren't about the boundary: every path
// they use is below it.
const wideHome = "/"

// serveDiscovery answers the PROPFINDs that find the calendar home. It reports
// whether it handled the request.
func serveDiscovery(w http.ResponseWriter, r *http.Request, home string) bool {
	if r.Method != "PROPFIND" {
		return false
	}
	switch r.URL.Path {
	case "/":
		w.WriteHeader(http.StatusMultiStatus)
		_, _ = w.Write([]byte(principalResponse("/principals/me/")))
	case "/principals/me/":
		w.WriteHeader(http.StatusMultiStatus)
		_, _ = w.Write([]byte(homeResponse(home)))
	default:
		return false
	}
	return true
}

// scopeServer has its calendar home at scopeHome and keeps a log of every
// request that isn't discovery.
type scopeServer struct {
	client *Client
	mu     sync.Mutex
	hits   []string
	finds  int
}

func newScopeServer(t *testing.T) *scopeServer {
	t.Helper()
	s := &scopeServer{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if r.Method == "PROPFIND" && r.URL.Path == "/" {
			s.finds++
		}
		if serveDiscovery(w, r, scopeHome) {
			return
		}
		s.hits = append(s.hits, r.Method+" "+r.URL.Path)
		if r.Method == "PROPFIND" {
			w.WriteHeader(http.StatusMultiStatus)
			_, _ = w.Write([]byte(multistatusHead + "</d:multistatus>"))
			return
		}
		w.Header().Set("ETag", `"v1"`)
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte("data"))
			return
		}
		if r.Method == "REPORT" {
			w.WriteHeader(http.StatusMultiStatus)
			_, _ = w.Write([]byte(multistatusHead + "</d:multistatus>"))
			return
		}
		w.WriteHeader(http.StatusCreated)
	}))
	t.Cleanup(server.Close)
	client, err := New(Config{URL: server.URL + "/", Username: "me", Password: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	s.client = client
	return s
}

func (s *scopeServer) requests() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.hits...)
}

func TestCallerIDsOutsideTheCalendarHomeAreRefusedWithoutARequest(t *testing.T) {
	const home = "/dav/home/user"
	outside := []string{
		"/",
		"/dav/files/user/secret.txt",
		"/dav/files/user/",
		"/dav/home/user2/work/",      // lookalike prefix of the home
		"/dav/home/user2/work/e.ics", // lookalike prefix of the home
		home + "/../user2/work/",
		home + "/work/../../../files/user/",
		home + "/work/../../user2/work/e.ics",
		"/dav/home/user/../../files/x/",
		"dav/home/user/work/e.ics", // relative
		"",
	}
	ctx := t.Context()
	start, end := rangeStart, rangeEnd
	fields := ical.EventFields{Summary: "x", Start: start, End: end}
	data := []byte("data")

	for _, id := range outside {
		t.Run(id, func(t *testing.T) {
			s := newScopeServer(t)
			c := s.client

			check := func(name string, err error) {
				t.Helper()
				if !errors.Is(err, ErrOutsideCalendars) {
					t.Errorf("%s: err = %v, want ErrOutsideCalendars", name, err)
				}
			}
			_, err := c.Get(ctx, id)
			check("get", err)
			_, err = c.Create(ctx, id, data)
			check("create", err)
			_, err = c.Update(ctx, id, "v1", data)
			check("update", err)
			check("delete", c.Delete(ctx, id, "v1"))
			_, err = c.Events(ctx, []string{id}, start, end, time.UTC)
			check("events", err)
			_, err = c.FreeBusy(ctx, []string{id}, start, end, time.UTC)
			check("free busy", err)
			_, err = c.FindByUID(ctx, id, "uid")
			check("find by uid", err)
			_, err = c.CreateEvent(ctx, id, fields, time.UTC)
			check("create event", err)
			_, err = c.UpdateEvent(ctx, id, "v1", ScopeSeries, "", newSummary("New"), time.UTC)
			check("update event", err)
			_, err = c.DeleteEvent(ctx, id, "v1", ScopeSeries, "", time.UTC)
			check("delete event", err)
			_, err = c.DeleteEvent(ctx, id, "v1", ScopeOccurrence, "20261110T090000Z", time.UTC)
			check("delete occurrence", err)

			if got := s.requests(); len(got) != 0 {
				t.Errorf("requests reached the server: %v", got)
			}
		})
	}
}

func TestOneBadCalendarIDStopsEventsBeforeAnyRequest(t *testing.T) {
	s := newScopeServer(t)

	_, err := s.client.Events(t.Context(), []string{scopeHome + "work/", "/dav/files/user/"}, rangeStart, rangeEnd, time.UTC)

	if !errors.Is(err, ErrOutsideCalendars) {
		t.Errorf("err = %v, want ErrOutsideCalendars", err)
	}
	if got := s.requests(); len(got) != 0 {
		t.Errorf("requests = %v, the good calendar must not be queried first", got)
	}
}

func TestEventIDsMustBeBelowACalendar(t *testing.T) {
	// The home and a calendar are collections, not events: deleting one would
	// remove everything in it.
	for _, id := range []string{scopeHome, "/dav/home/user", scopeHome + "work/", scopeHome + "work"} {
		t.Run(id, func(t *testing.T) {
			s := newScopeServer(t)
			ctx := t.Context()

			_, err := s.client.Get(ctx, id)
			if !errors.Is(err, ErrOutsideCalendars) {
				t.Errorf("get: err = %v", err)
			}
			if err := s.client.Delete(ctx, id, "v1"); !errors.Is(err, ErrOutsideCalendars) {
				t.Errorf("delete: err = %v", err)
			}
			_, err = s.client.DeleteEvent(ctx, id, "v1", ScopeSeries, "", time.UTC)
			if !errors.Is(err, ErrOutsideCalendars) {
				t.Errorf("delete event: err = %v", err)
			}
			if got := s.requests(); len(got) != 0 {
				t.Errorf("requests reached the server: %v", got)
			}
		})
	}
}

func TestACalendarIDMustBeBelowTheHome(t *testing.T) {
	for _, id := range []string{scopeHome, "/dav/home/user", "/dav/home/"} {
		t.Run(id, func(t *testing.T) {
			s := newScopeServer(t)

			_, err := s.client.Events(t.Context(), []string{id}, rangeStart, rangeEnd, time.UTC)

			if !errors.Is(err, ErrOutsideCalendars) {
				t.Errorf("err = %v", err)
			}
			if got := s.requests(); len(got) != 0 {
				t.Errorf("requests reached the server: %v", got)
			}
		})
	}
}

func TestIDsInsideTheHomeAreUsedAsCleanedPaths(t *testing.T) {
	s := newScopeServer(t)
	ctx := t.Context()

	// The path that was checked is the path that is sent.
	if _, err := s.client.Create(ctx, scopeHome+"work/../work//e.ics", []byte("data")); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := s.client.Get(ctx, scopeHome+"work/e.ics"); err != nil {
		t.Fatalf("get: %v", err)
	}
	if _, err := s.client.Events(ctx, []string{"/dav/home/user/work"}, rangeStart, rangeEnd, time.UTC); err != nil {
		t.Fatalf("events: %v", err)
	}

	got := strings.Join(s.requests(), ", ")
	want := "PUT /dav/home/user/work/e.ics, GET /dav/home/user/work/e.ics, REPORT /dav/home/user/work, REPORT /dav/home/user/work"
	if got != want {
		t.Errorf("requests = %s, want %s", got, want)
	}
}

func TestACalendarWithATrailingSlashKeepsIt(t *testing.T) {
	s := newScopeServer(t)

	if _, err := s.client.Events(t.Context(), []string{scopeHome + "work/"}, rangeStart, rangeEnd, time.UTC); err != nil {
		t.Fatalf("events: %v", err)
	}

	if got := s.requests(); len(got) == 0 || got[0] != "REPORT /dav/home/user/work/" {
		t.Errorf("requests = %v", got)
	}
}

func TestCreateEventStaysInTheCalendar(t *testing.T) {
	s := newScopeServer(t)
	fields := ical.EventFields{Summary: "x", Start: rangeStart, End: rangeEnd}

	got, err := s.client.CreateEvent(t.Context(), scopeHome+"work/", fields, time.UTC)
	if err != nil {
		t.Fatalf("create event: %v", err)
	}

	if !strings.HasPrefix(got.EventID, scopeHome+"work/") || got.CalendarID != scopeHome+"work/" {
		t.Errorf("event = %+v", got)
	}
}

func TestHomeIsDiscoveredOncePerClient(t *testing.T) {
	s := newScopeServer(t)
	ctx := t.Context()

	if _, err := s.client.Get(ctx, scopeHome+"work/e.ics"); err != nil {
		t.Fatalf("get: %v", err)
	}
	if _, err := s.client.Events(ctx, []string{scopeHome + "work/"}, rangeStart, rangeEnd, time.UTC); err != nil {
		t.Fatalf("events: %v", err)
	}
	if _, err := s.client.Calendars(ctx); err != nil {
		t.Fatalf("calendars: %v", err)
	}

	if s.finds != 1 {
		t.Errorf("principal looked up %d times, want once", s.finds)
	}
}

func TestHomeIsDiscoveredOnceWhenCallsRunTogether(t *testing.T) {
	s := newScopeServer(t)
	var wg sync.WaitGroup

	for range 8 {
		wg.Go(func() {
			if _, err := s.client.Get(t.Context(), scopeHome+"work/e.ics"); err != nil {
				t.Errorf("get: %v", err)
			}
		})
	}
	wg.Wait()

	if s.finds != 1 {
		t.Errorf("principal looked up %d times, want once", s.finds)
	}
}

func TestACancelledDiscoveryIsNotRemembered(t *testing.T) {
	s := newScopeServer(t)
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()

	if _, err := s.client.Get(cancelled, scopeHome+"work/e.ics"); err == nil {
		t.Fatal("get with a cancelled context succeeded")
	}
	// The next call has a live context, and must neither fail nor see an
	// empty home.
	if _, err := s.client.Get(t.Context(), scopeHome+"work/e.ics"); err != nil {
		t.Errorf("get after a cancelled call: %v", err)
	}
	if _, err := s.client.Get(t.Context(), "/dav/files/user/x.ics"); !errors.Is(err, ErrOutsideCalendars) {
		t.Errorf("outside get: err = %v", err)
	}
}

func TestAWaitingCallStopsWaitingWhenItsOwnContextEnds(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() { close(started) })
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		if !serveDiscovery(w, r, scopeHome) {
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	client, err := New(Config{URL: server.URL + "/"})
	if err != nil {
		t.Fatal(err)
	}

	leader := make(chan error, 1)
	go func() {
		_, err := client.Get(t.Context(), scopeHome+"work/e.ics")
		leader <- err
	}()
	<-started

	waiter, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := client.Get(waiter, scopeHome+"work/e.ics")
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("waiter: err = %v, want its own deadline", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the waiter kept waiting after its context ended")
	}

	// The first call is unaffected, and its result is kept.
	close(release)
	// The event itself doesn't exist, which is all the leader's own call may fail on.
	if err := <-leader; !errors.Is(err, ErrEventNotFound) {
		t.Errorf("leader: err = %v, want ErrEventNotFound", err)
	}
	if home, err := client.homeSet(t.Context()); err != nil || home != scopeHome {
		t.Errorf("home = %q, %v", home, err)
	}
}

func TestDiscoveryFailureIsReportedNotTreatedAsOutside(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(server.Close)
	client, err := New(Config{URL: server.URL + "/"})
	if err != nil {
		t.Fatal(err)
	}

	_, err = client.Create(t.Context(), "/cal/me/e.ics", []byte("data"))

	if !errors.Is(err, ErrLoginRefused) {
		t.Errorf("err = %v, want ErrLoginRefused", err)
	}
}

func TestWithinHome(t *testing.T) {
	tests := []struct {
		home, path string
		levels     int
		want       bool
	}{
		{"/h/u", "/h/u/c", 1, true},
		{"/h/u", "/h/u/c", 2, false},
		{"/h/u", "/h/u/c/e", 2, true},
		{"/h/u", "/h/u", 1, false},
		{"/h/u", "/h/u2/c", 1, false},
		{"/h/u", "/h", 1, false},
		{"/", "/c", 1, true},
		{"/", "/c/e", 2, true},
		{"/", "/", 1, false},
	}

	for _, tt := range tests {
		if got := within(tt.home, tt.path, tt.levels); got != tt.want {
			t.Errorf("within(%q, %q, %d) = %v, want %v", tt.home, tt.path, tt.levels, got, tt.want)
		}
	}
}
