//go:build integration

package integration

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/robinvdvleuten/pim-mcp/internal/calendar"
	"github.com/robinvdvleuten/pim-mcp/internal/mcpserver"
)

const (
	caldavUser     = "tester"
	caldavPassword = "test-password"
)

type caldavServer struct {
	name     string
	url      string
	calendar string
	setup    func(t *testing.T, s caldavServer)
}

var caldavServers = []caldavServer{
	{
		name:     "radicale",
		url:      "http://127.0.0.1:35232/",
		calendar: "/tester/default/",
		setup:    createRadicaleCalendar,
	},
	{
		name:     "baikal",
		url:      "http://127.0.0.1:35233/dav.php/",
		calendar: "/dav.php/calendars/tester/default/",
	},
}

// createRadicaleCalendar makes the calendar, which Baïkal's seed already has.
func createRadicaleCalendar(t *testing.T, s caldavServer) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), "MKCALENDAR", s.url+"tester/default/", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.SetBasicAuth(caldavUser, caldavPassword)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("create calendar: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusConflict && resp.StatusCode != http.StatusForbidden {
		t.Fatalf("create calendar: %s", resp.Status)
	}
}

func eachCalDAVServer(t *testing.T, test func(t *testing.T, s caldavServer, c *calendar.Client)) {
	t.Helper()
	for _, s := range caldavServers {
		t.Run(s.name, func(t *testing.T) {
			if s.setup != nil {
				s.setup(t, s)
			}
			c, err := calendar.New(calendar.Config{URL: s.url, Username: caldavUser, Password: caldavPassword})
			if err != nil {
				t.Fatalf("new client: %v", err)
			}
			test(t, s, c)
		})
	}
}

var (
	eventRangeStart = time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	eventRangeEnd   = time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
)

func newEventPath(t *testing.T, s caldavServer) (path, uid string) {
	t.Helper()
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	uid = hex.EncodeToString(b)
	return s.calendar + uid + ".ics", uid
}

func eventICS(uid, summary string) []byte {
	return []byte(strings.Join([]string{
		"BEGIN:VCALENDAR",
		"VERSION:2.0",
		"PRODID:-//pim-mcp//test//EN",
		"BEGIN:VEVENT",
		"UID:" + uid,
		"DTSTAMP:20261105T100000Z",
		"DTSTART:20261110T100000Z",
		"DTEND:20261110T110000Z",
		"SUMMARY:" + summary,
		"END:VEVENT",
		"END:VCALENDAR",
		"",
	}, "\r\n"))
}

func TestCalDAVCreateRefusesToOverwrite(t *testing.T) {
	eachCalDAVServer(t, func(t *testing.T, s caldavServer, c *calendar.Client) {
		path, uid := newEventPath(t, s)
		ctx := t.Context()
		if _, err := c.Create(ctx, path, eventICS(uid, "first")); err != nil {
			t.Fatalf("create: %v", err)
		}

		_, err := c.Create(ctx, path, eventICS(uid, "second"))

		if !errors.Is(err, calendar.ErrConflict) {
			t.Errorf("second create: err = %v, want ErrConflict", err)
		}
		assertSummary(ctx, t, c, path, "first")
	})
}

func TestCalDAVUpdateWithCurrentETagSucceeds(t *testing.T) {
	eachCalDAVServer(t, func(t *testing.T, s caldavServer, c *calendar.Client) {
		path, uid := newEventPath(t, s)
		ctx := t.Context()
		first, err := c.Create(ctx, path, eventICS(uid, "first"))
		if err != nil {
			t.Fatalf("create: %v", err)
		}

		second, err := c.Update(ctx, path, first, eventICS(uid, "second"))
		if err != nil {
			t.Fatalf("update: %v", err)
		}

		if second == "" || second == first {
			t.Errorf("etag after update = %q, want a new one (was %q)", second, first)
		}
		got := assertSummary(ctx, t, c, path, "second")
		if got.ETag != second {
			t.Errorf("etag from get = %q, want the one update returned, %q", got.ETag, second)
		}
	})
}

func TestCalDAVUpdateWithStaleETagLeavesServerCopy(t *testing.T) {
	eachCalDAVServer(t, func(t *testing.T, s caldavServer, c *calendar.Client) {
		path, uid := newEventPath(t, s)
		ctx := t.Context()
		ours, err := c.Create(ctx, path, eventICS(uid, "first"))
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		// Another device edits the event after we read it.
		if _, err := c.Update(ctx, path, ours, eventICS(uid, "from phone")); err != nil {
			t.Fatalf("other device: %v", err)
		}

		_, err = c.Update(ctx, path, ours, eventICS(uid, "from agent"))

		if !errors.Is(err, calendar.ErrConflict) {
			t.Errorf("err = %v, want ErrConflict", err)
		}
		assertSummary(ctx, t, c, path, "from phone")
	})
}

// Queries return ETags too, which writes then use, so both must be the same value.
func TestCalDAVETagsMatchQueries(t *testing.T) {
	eachCalDAVServer(t, func(t *testing.T, s caldavServer, c *calendar.Client) {
		path, uid := newEventPath(t, s)
		ctx := t.Context()
		etag, err := c.Create(ctx, path, eventICS(uid, "first"))
		if err != nil {
			t.Fatalf("create: %v", err)
		}

		got, err := c.Events(ctx, []string{s.calendar}, eventRangeStart, eventRangeEnd, time.UTC)
		if err != nil {
			t.Fatalf("events: %v", err)
		}
		i := slices.IndexFunc(got.Events, func(e calendar.Event) bool { return e.UID == uid })
		if i < 0 {
			t.Fatalf("events = %+v, want %s", got.Events, uid)
		}

		if got.Events[i].ETag != etag {
			t.Errorf("etag from query = %q, want %q", got.Events[i].ETag, etag)
		}
		if _, err := c.Update(ctx, path, got.Events[i].ETag, eventICS(uid, "second")); err != nil {
			t.Errorf("update using the query's etag: %v", err)
		}
	})
}

func assertSummary(ctx context.Context, t *testing.T, c *calendar.Client, path, summary string) *calendar.Object {
	t.Helper()
	object, err := c.Get(ctx, path)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if want := fmt.Sprintf("SUMMARY:%s", summary); !strings.Contains(string(object.Data), want) {
		t.Errorf("server copy = %q, want it to contain %q", object.Data, want)
	}
	return object
}

func calendarSession(t *testing.T, s caldavServer) *mcp.ClientSession {
	t.Helper()
	server := mcpserver.New(mcpserver.Options{
		Version: "test",
		DialCalendar: func() (*calendar.Client, error) {
			return calendar.New(calendar.Config{URL: s.url, Username: caldavUser, Password: caldavPassword})
		},
	})
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(t.Context(), serverTransport, nil)
	if err != nil {
		t.Fatalf("connect server: %v", err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "test"}, nil)
	session, err := client.Connect(t.Context(), clientTransport, nil)
	if err != nil {
		t.Fatalf("connect client: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func TestCalendarListCalendars(t *testing.T) {
	for _, s := range caldavServers {
		t.Run(s.name, func(t *testing.T) {
			if s.setup != nil {
				s.setup(t, s)
			}
			session := calendarSession(t, s)

			got := callTool[mcpserver.CalendarList](t, session, "calendar_list_calendars", nil)

			i := slices.IndexFunc(got.Calendars, func(c calendar.Calendar) bool { return c.ID == s.calendar })
			if i < 0 {
				t.Fatalf("calendars = %+v, want %s", got.Calendars, s.calendar)
			}
			if got.Calendars[i].ReadOnly {
				t.Errorf("calendar = %+v, want it writable", got.Calendars[i])
			}
			if got.Calendars[i].Name == "" {
				t.Errorf("calendar = %+v, want a name", got.Calendars[i])
			}
		})
	}
}

func TestCalendarIDsWorkForWrites(t *testing.T) {
	eachCalDAVServer(t, func(t *testing.T, s caldavServer, c *calendar.Client) {
		calendars, err := c.Calendars(t.Context())
		if err != nil {
			t.Fatalf("calendars: %v", err)
		}
		i := slices.IndexFunc(calendars, func(found calendar.Calendar) bool { return found.ID == s.calendar })
		if i < 0 {
			t.Fatalf("calendars = %+v", calendars)
		}
		path, uid := newEventPath(t, caldavServer{calendar: calendars[i].ID})

		if _, err := c.Create(t.Context(), path, eventICS(uid, "via found calendar")); err != nil {
			t.Errorf("create: %v", err)
		}
	})
}

func TestBaikalSharedCalendarIsReadOnly(t *testing.T) {
	s := caldavServers[1]
	session := calendarSession(t, s)

	got := callTool[mcpserver.CalendarList](t, session, "calendar_list_calendars", nil)

	i := slices.IndexFunc(got.Calendars, func(c calendar.Calendar) bool { return c.Name == "Shared" })
	if i < 0 {
		t.Fatalf("calendars = %+v, want the shared one", got.Calendars)
	}
	if !got.Calendars[i].ReadOnly {
		t.Errorf("calendar = %+v, want it read-only", got.Calendars[i])
	}
}

func TestCalendarListRefusesWrongPassword(t *testing.T) {
	for _, s := range caldavServers {
		t.Run(s.name, func(t *testing.T) {
			c, err := calendar.New(calendar.Config{URL: s.url, Username: caldavUser, Password: "wrong"})
			if err != nil {
				t.Fatal(err)
			}

			if _, err := c.Calendars(t.Context()); !errors.Is(err, calendar.ErrLoginRefused) {
				t.Errorf("err = %v, want ErrLoginRefused", err)
			}
		})
	}
}

// Discovery can store a URL at any depth, so calendars have to be found from
// each of the shapes it finds for these servers.
func TestCalendarsFoundFromDiscoveredURLs(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want string
	}{
		{"radicale root", "http://127.0.0.1:35232/", "/tester/default/"},
		{"radicale user path", "http://127.0.0.1:35232/tester/", "/tester/default/"},
		{"baikal without trailing slash", "http://127.0.0.1:35233/dav.php", "/dav.php/calendars/tester/default/"},
		{"baikal with trailing slash", "http://127.0.0.1:35233/dav.php/", "/dav.php/calendars/tester/default/"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			createRadicaleCalendar(t, caldavServers[0])
			c, err := calendar.New(calendar.Config{URL: tt.url, Username: caldavUser, Password: caldavPassword})
			if err != nil {
				t.Fatal(err)
			}

			got, err := c.Calendars(t.Context())
			if err != nil {
				t.Fatalf("calendars: %v", err)
			}

			if !slices.ContainsFunc(got, func(found calendar.Calendar) bool { return found.ID == tt.want }) {
				t.Errorf("calendars = %+v, want %s", got, tt.want)
			}
		})
	}
}

func createEvent(t *testing.T, c *calendar.Client, s caldavServer, lines ...string) (path, uid string) {
	t.Helper()
	path, uid = newEventPath(t, s)
	all := append([]string{"BEGIN:VCALENDAR", "VERSION:2.0", "PRODID:-//pim-mcp//test//EN"}, lines...)
	all = append(all, "END:VCALENDAR", "")
	data := strings.ReplaceAll(strings.Join(all, "\r\n"), "UID:@", "UID:"+uid)
	if _, err := c.Create(t.Context(), path, []byte(data)); err != nil {
		t.Fatalf("create: %v", err)
	}
	return path, uid
}

func eventsOf(events []calendar.Event, uid string) []calendar.Event {
	var found []calendar.Event
	for _, event := range events {
		if event.UID == uid {
			found = append(found, event)
		}
	}
	return found
}

func TestCalendarListEventsExpandsRecurringEvents(t *testing.T) {
	eachCalDAVServer(t, func(t *testing.T, s caldavServer, c *calendar.Client) {
		path, uid := createEvent(t, c, s,
			"BEGIN:VEVENT", "UID:@", "DTSTAMP:20261101T120000Z", "DTSTART;TZID=Europe/Amsterdam:20261102T100000",
			"DTEND;TZID=Europe/Amsterdam:20261102T110000", "RRULE:FREQ=WEEKLY;COUNT=4", `SUMMARY:Weekly\, with a comma`, "END:VEVENT",
			"BEGIN:VEVENT", "UID:@", "DTSTAMP:20261101T120000Z", "RECURRENCE-ID;TZID=Europe/Amsterdam:20261109T100000",
			"DTSTART;TZID=Europe/Amsterdam:20261120T150000", "DTEND;TZID=Europe/Amsterdam:20261120T160000", "SUMMARY:Moved", "END:VEVENT")
		session := calendarSession(t, s)

		got := callTool[calendar.EventList](t, session, "calendar_list_events", map[string]any{
			"start":        "2026-11-01T00:00:00Z",
			"end":          "2026-12-01T00:00:00Z",
			"calendar_ids": []string{s.calendar},
		})

		events := eventsOf(got.Events, uid)
		want := []struct{ occurrence, summary, start string }{
			{"20261102T090000Z", "Weekly, with a comma", "2026-11-02T10:00:00+01:00"},
			{"20261116T090000Z", "Weekly, with a comma", "2026-11-16T10:00:00+01:00"},
			{"20261109T090000Z", "Moved", "2026-11-20T15:00:00+01:00"},
			{"20261123T090000Z", "Weekly, with a comma", "2026-11-23T10:00:00+01:00"},
		}
		if len(events) != len(want) {
			t.Fatalf("events = %+v, want %d", events, len(want))
		}
		for i, w := range want {
			e := events[i]
			if e.OccurrenceID != w.occurrence || e.Summary != w.summary || e.Start.Format(time.RFC3339) != w.start {
				t.Errorf("event %d = %s %q %s, want %+v", i, e.OccurrenceID, e.Summary, e.Start.Format(time.RFC3339), w)
			}
			if e.EventID != path || e.ETag == "" || e.CalendarID != s.calendar || !e.Recurring || e.Timezone != "Europe/Amsterdam" {
				t.Errorf("event %d = %+v", i, e)
			}
		}
	})
}

func TestCalendarListEventsFindsAnOccurrenceMovedIntoTheRange(t *testing.T) {
	eachCalDAVServer(t, func(t *testing.T, s caldavServer, c *calendar.Client) {
		_, uid := createEvent(t, c, s,
			"BEGIN:VEVENT", "UID:@", "DTSTAMP:20261101T120000Z", "DTSTART:20261102T100000Z",
			"DTEND:20261102T110000Z", "RRULE:FREQ=DAILY;COUNT=2", "SUMMARY:Series", "END:VEVENT",
			"BEGIN:VEVENT", "UID:@", "DTSTAMP:20261101T120000Z", "RECURRENCE-ID:20261103T100000Z",
			"DTSTART:20270115T100000Z", "DTEND:20270115T110000Z", "SUMMARY:Far away", "END:VEVENT")

		got, err := c.Events(t.Context(), []string{s.calendar},
			time.Date(2027, 1, 15, 0, 0, 0, 0, time.UTC), time.Date(2027, 1, 16, 0, 0, 0, 0, time.UTC), time.UTC)
		if err != nil {
			t.Fatalf("events: %v", err)
		}

		events := eventsOf(got.Events, uid)
		if len(events) != 1 || events[0].Summary != "Far away" {
			t.Errorf("events = %+v", events)
		}
	})
}

func TestCalendarListEventsFromAllCalendarsByDefault(t *testing.T) {
	for _, s := range caldavServers {
		t.Run(s.name, func(t *testing.T) {
			if s.setup != nil {
				s.setup(t, s)
			}
			c, err := calendar.New(calendar.Config{URL: s.url, Username: caldavUser, Password: caldavPassword})
			if err != nil {
				t.Fatal(err)
			}
			_, uid := createEvent(t, c, s, "BEGIN:VEVENT", "UID:@", "DTSTAMP:20261101T120000Z", "DTSTART:20261105T100000Z", "SUMMARY:Found", "END:VEVENT")
			session := calendarSession(t, s)

			got := callTool[calendar.EventList](t, session, "calendar_list_events", map[string]any{
				"start": "2026-11-05T00:00:00Z",
				"end":   "2026-11-06T00:00:00Z",
			})

			if len(eventsOf(got.Events, uid)) != 1 {
				t.Errorf("events = %+v", got.Events)
			}
		})
	}
}

func TestCalendarListEventsReadsFloatingAndAllDayInTheUsersZone(t *testing.T) {
	eachCalDAVServer(t, func(t *testing.T, s caldavServer, c *calendar.Client) {
		_, floating := createEvent(t, c, s, "BEGIN:VEVENT", "UID:@", "DTSTAMP:20261101T120000Z", "DTSTART:20261110T090000", "SUMMARY:Floating", "END:VEVENT")
		_, allDay := createEvent(t, c, s, "BEGIN:VEVENT", "UID:@", "DTSTAMP:20261101T120000Z", "DTSTART;VALUE=DATE:20261110", "SUMMARY:All day", "END:VEVENT")
		tokyo, err := time.LoadLocation("Asia/Tokyo")
		if err != nil {
			t.Fatal(err)
		}

		got, err := c.Events(t.Context(), []string{s.calendar}, eventRangeStart, eventRangeEnd, tokyo)
		if err != nil {
			t.Fatalf("events: %v", err)
		}

		events := eventsOf(got.Events, floating)
		if len(events) != 1 || events[0].Start.Format(time.RFC3339) != "2026-11-10T09:00:00+09:00" {
			t.Errorf("floating = %+v", events)
		}
		events = eventsOf(got.Events, allDay)
		if len(events) != 1 || !events[0].AllDay || events[0].Start.Format(time.RFC3339) != "2026-11-10T00:00:00+09:00" {
			t.Errorf("all day = %+v", events)
		}
	})
}

func TestCalendarListEventsReturnsNoICalendar(t *testing.T) {
	eachCalDAVServer(t, func(t *testing.T, s caldavServer, c *calendar.Client) {
		createEvent(t, c, s, "BEGIN:VEVENT", "UID:@", "DTSTAMP:20261101T120000Z", "DTSTART:20261105T100000Z", "SUMMARY:Plain", "END:VEVENT")
		session := calendarSession(t, s)

		result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "calendar_list_events", Arguments: map[string]any{
			"start": "2026-11-05T00:00:00Z", "end": "2026-11-06T00:00:00Z", "calendar_ids": []string{s.calendar},
		}})
		if err != nil {
			t.Fatal(err)
		}

		if result.IsError || strings.Contains(toolText(result), "BEGIN:") || strings.Contains(toolText(result), "RRULE") {
			t.Errorf("result = %s", toolText(result))
		}
	})
}

func TestCalendarFreeBusy(t *testing.T) {
	eachCalDAVServer(t, func(t *testing.T, s caldavServer, c *calendar.Client) {
		// A day no other test uses, as servers keep events between tests.
		createEvent(t, c, s, "BEGIN:VEVENT", "UID:@", "DTSTAMP:20261101T120000Z", "DTSTART:20280310T090000Z", "DTEND:20280310T100000Z", "RRULE:FREQ=DAILY;COUNT=2", "SUMMARY:Busy", "END:VEVENT")
		createEvent(t, c, s, "BEGIN:VEVENT", "UID:@", "DTSTAMP:20261101T120000Z", "DTSTART:20280310T120000Z", "DTEND:20280310T130000Z", "TRANSP:TRANSPARENT", "SUMMARY:Free", "END:VEVENT")
		session := calendarSession(t, s)

		got := callTool[calendar.FreeBusy](t, session, "calendar_free_busy", map[string]any{
			"start": "2028-03-10T00:00:00Z", "end": "2028-03-12T00:00:00Z", "calendar_ids": []string{s.calendar},
		})

		if len(got.Busy) < 2 {
			t.Fatalf("busy = %+v", got.Busy)
		}
		first, second := got.Busy[0], got.Busy[1]
		if !first.Start.Equal(time.Date(2028, 3, 10, 9, 0, 0, 0, time.UTC)) || !first.End.Equal(time.Date(2028, 3, 10, 10, 0, 0, 0, time.UTC)) {
			t.Errorf("first = %+v", first)
		}
		if !second.Start.Equal(time.Date(2028, 3, 11, 9, 0, 0, 0, time.UTC)) {
			t.Errorf("second = %+v, the transparent event must not count", second)
		}
	})
}
