//go:build integration

package integration

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
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
		url:      radicaleURL,
		calendar: "/tester/default/",
		setup:    createRadicaleCalendar,
	},
	{
		name:     "baikal",
		url:      baikalURL + "dav.php/",
		calendar: "/dav.php/calendars/tester/default/",
	},
}

// Nextcloud is heavy, so it only joins in when its compose profile is running.
func init() {
	if os.Getenv("PIM_MCP_TEST_NEXTCLOUD") != "" {
		caldavServers = append(caldavServers, caldavServer{
			name:     "nextcloud",
			url:      nextcloudURL + "remote.php/dav/",
			calendar: "/remote.php/dav/calendars/tester/personal/",
		})
	}
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
		DialCalendar: func(string) (*calendar.Client, error) {
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
		{"radicale root", radicaleURL, "/tester/default/"},
		{"radicale user path", radicaleURL + "tester/", "/tester/default/"},
		{"baikal without trailing slash", baikalURL + "dav.php", "/dav.php/calendars/tester/default/"},
		{"baikal with trailing slash", baikalURL + "dav.php/", "/dav.php/calendars/tester/default/"},
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

func TestCalendarCreateEventRoundTrip(t *testing.T) {
	eachCalDAVServer(t, func(t *testing.T, s caldavServer, c *calendar.Client) {
		session := calendarSession(t, s)
		summary := "Lunch, then coffee; maybe\\more"

		created := callTool[calendar.Event](t, session, "calendar_create_event", map[string]any{
			"calendar_id": s.calendar,
			"summary":     summary,
			"location":    "Café, Room 4",
			"description": "line one\nline two",
			"start":       "2029-05-10T12:00",
			"end":         "2029-05-10T13:00",
			"timezone":    "Europe/Amsterdam",
		})

		if created.EventID == "" || created.ETag == "" || created.CalendarID != s.calendar {
			t.Errorf("created = %+v", created)
		}
		got, err := c.Events(t.Context(), []string{s.calendar},
			time.Date(2029, 5, 10, 0, 0, 0, 0, time.UTC), time.Date(2029, 5, 11, 0, 0, 0, 0, time.UTC), time.UTC)
		if err != nil {
			t.Fatalf("events: %v", err)
		}
		events := eventsOf(got.Events, created.UID)
		if len(events) != 1 {
			t.Fatalf("events = %+v", got.Events)
		}
		e := events[0]
		if e.Summary != summary || e.Location != "Café, Room 4" || e.Description != "line one\nline two" {
			t.Errorf("stored text = %q, %q, %q", e.Summary, e.Location, e.Description)
		}
		if !e.Start.Equal(time.Date(2029, 5, 10, 10, 0, 0, 0, time.UTC)) {
			t.Errorf("stored start = %s", e.Start)
		}
		if e.ETag != created.ETag || e.EventID != created.EventID {
			t.Errorf("read back %+v, created %+v", e, created)
		}
	})
}

func TestCalendarCreateAllDayEventRoundTrip(t *testing.T) {
	eachCalDAVServer(t, func(t *testing.T, s caldavServer, c *calendar.Client) {
		session := calendarSession(t, s)

		created := callTool[calendar.Event](t, session, "calendar_create_event", map[string]any{
			"calendar_id": s.calendar, "summary": "Holiday", "date": "2029-06-10", "end_date": "2029-06-12",
		})

		got, err := c.Events(t.Context(), []string{s.calendar},
			time.Date(2029, 6, 1, 0, 0, 0, 0, time.UTC), time.Date(2029, 7, 1, 0, 0, 0, 0, time.UTC), time.UTC)
		if err != nil {
			t.Fatalf("events: %v", err)
		}
		events := eventsOf(got.Events, created.UID)
		if len(events) != 1 || !events[0].AllDay || !events[0].End.Equal(time.Date(2029, 6, 13, 0, 0, 0, 0, time.UTC)) {
			t.Errorf("events = %+v", events)
		}
	})
}

func TestCalendarCreateRecurringEventAcrossDST(t *testing.T) {
	eachCalDAVServer(t, func(t *testing.T, s caldavServer, c *calendar.Client) {
		session := calendarSession(t, s)
		amsterdam, err := time.LoadLocation("Europe/Amsterdam")
		if err != nil {
			t.Fatal(err)
		}

		// The clocks go forward on Sunday 31 March 2030, between the second and third Monday.
		created := callTool[calendar.Event](t, session, "calendar_create_event", map[string]any{
			"calendar_id": s.calendar, "summary": "Weekly, in Amsterdam", "recurrence": "FREQ=WEEKLY;BYDAY=MO;COUNT=4",
			"start": "2030-03-18T09:00", "end": "2030-03-18T09:30", "timezone": "Europe/Amsterdam",
		})
		if !created.Recurring || created.EventID == "" || created.ETag == "" {
			t.Fatalf("created = %+v", created)
		}

		list := func() []calendar.Event {
			got, err := c.Events(t.Context(), []string{s.calendar},
				time.Date(2030, 3, 1, 0, 0, 0, 0, time.UTC), time.Date(2030, 5, 1, 0, 0, 0, 0, time.UTC), time.UTC)
			if err != nil {
				t.Fatalf("events: %v", err)
			}
			return eventsOf(got.Events, created.UID)
		}
		events := list()
		if len(events) != 4 {
			t.Fatalf("events = %+v", events)
		}
		wantUTC := []string{"08:00", "08:00", "07:00", "07:00"}
		for i, e := range events {
			if local := e.Start.In(amsterdam).Format("15:04"); local != "09:00" {
				t.Errorf("occurrence %d is at %s in Amsterdam, want 09:00", i, local)
			}
			if utc := e.Start.UTC().Format("15:04"); utc != wantUTC[i] {
				t.Errorf("occurrence %d is at %s UTC, want %s", i, utc, wantUTC[i])
			}
			if !e.Recurring || e.OccurrenceID == "" || e.Timezone != "Europe/Amsterdam" {
				t.Errorf("occurrence %d = %+v", i, e)
			}
		}

		// Delete the first one after the change.
		callTool[calendar.DeletedEvent](t, session, "calendar_delete_event", map[string]any{
			"event_id": created.EventID, "etag": events[2].ETag, "scope": "occurrence", "occurrence_id": events[2].OccurrenceID,
		})

		got := list()
		if len(got) != 3 {
			t.Fatalf("after delete: %+v", got)
		}
		for i, want := range []string{"2030-03-18", "2030-03-25", "2030-04-08"} {
			if day := got[i].Start.In(amsterdam).Format(time.DateOnly); day != want || got[i].Start.In(amsterdam).Format("15:04") != "09:00" {
				t.Errorf("occurrence %d = %s, want %s at 09:00", i, got[i].Start.In(amsterdam).Format(time.RFC3339), want)
			}
		}
	})
}

func TestCreateEventOnReadOnlyCalendarIsRefused(t *testing.T) {
	session := calendarSession(t, caldavServers[1])

	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "calendar_create_event", Arguments: map[string]any{
		"calendar_id": "/dav.php/calendars/tester/shared/", "summary": "x",
		"start": "2029-05-10T12:00:00Z", "end": "2029-05-10T13:00:00Z",
	}})
	if err != nil {
		t.Fatal(err)
	}

	if !result.IsError {
		t.Errorf("created an event on a read-only calendar: %s", toolText(result))
	}
}

func createWeekly(t *testing.T, c *calendar.Client, s caldavServer) (path, uid string) {
	t.Helper()
	return createEvent(t, c, s,
		"BEGIN:VEVENT", "UID:@", "DTSTAMP:20261101T120000Z", "DTSTART;TZID=Europe/Amsterdam:20300107T100000",
		"DTEND;TZID=Europe/Amsterdam:20300107T110000", "RRULE:FREQ=WEEKLY;COUNT=3", "SUMMARY:Weekly", "END:VEVENT",
		"BEGIN:VEVENT", "UID:@", "DTSTAMP:20261101T120000Z", "RECURRENCE-ID;TZID=Europe/Amsterdam:20300114T100000",
		"DTSTART;TZID=Europe/Amsterdam:20300114T100000", "DTEND;TZID=Europe/Amsterdam:20300114T110000", "SUMMARY:Special", "END:VEVENT")
}

func seriesOf(t *testing.T, c *calendar.Client, s caldavServer, uid string) []calendar.Event {
	t.Helper()
	got, err := c.Events(t.Context(), []string{s.calendar},
		time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2030, 2, 1, 0, 0, 0, 0, time.UTC), time.UTC)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	return eventsOf(got.Events, uid)
}

func TestCalendarUpdateSeriesKeepsOverrides(t *testing.T) {
	eachCalDAVServer(t, func(t *testing.T, s caldavServer, c *calendar.Client) {
		path, uid := createWeekly(t, c, s)
		events := seriesOf(t, c, s, uid)
		session := calendarSession(t, s)

		updated := callTool[calendar.UpdatedEvent](t, session, "calendar_update_event", map[string]any{
			"event_id": path, "etag": events[0].ETag, "scope": "series", "summary": "Renamed, with a comma",
		})

		if updated.ETag == "" || updated.ETag == events[0].ETag {
			t.Errorf("etag = %q, was %q", updated.ETag, events[0].ETag)
		}
		got := seriesOf(t, c, s, uid)
		want := []string{"Renamed, with a comma", "Special", "Renamed, with a comma"}
		if len(got) != len(want) {
			t.Fatalf("events = %+v", got)
		}
		for i := range want {
			if got[i].Summary != want[i] {
				t.Errorf("event %d = %q, want %q", i, got[i].Summary, want[i])
			}
		}
		if got[0].ETag != updated.ETag {
			t.Errorf("etag from a read = %q, from the update = %q", got[0].ETag, updated.ETag)
		}
	})
}

// A second client edits the event between our read and our write, the case the
// whole design is about.
func TestCalendarWritesNeverOverwriteAnotherClient(t *testing.T) {
	eachCalDAVServer(t, func(t *testing.T, s caldavServer, c *calendar.Client) {
		tests := []struct {
			tool string
			args map[string]any
		}{
			{"calendar_update_event", map[string]any{"scope": "series", "summary": "From the agent"}},
			{"calendar_delete_event", map[string]any{"scope": "series"}},
		}
		for _, tt := range tests {
			t.Run(tt.tool, func(t *testing.T) {
				path, uid := createWeekly(t, c, s)
				stale := seriesOf(t, c, s, uid)[0].ETag
				// The phone.
				phone, err := c.Get(t.Context(), path)
				if err != nil {
					t.Fatal(err)
				}
				edited := strings.Replace(string(phone.Data), "SUMMARY:Weekly", "SUMMARY:From the phone", 1)
				if _, err := c.Update(t.Context(), path, phone.ETag, []byte(edited)); err != nil {
					t.Fatalf("phone: %v", err)
				}
				session := calendarSession(t, s)
				args := map[string]any{"event_id": path, "etag": stale}
				for k, v := range tt.args {
					args[k] = v
				}

				result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: tt.tool, Arguments: args})
				if err != nil {
					t.Fatal(err)
				}

				if !result.IsError || !strings.Contains(toolText(result), "conflict") {
					t.Errorf("result = %v: %s", result.IsError, toolText(result))
				}
				now, err := c.Get(t.Context(), path)
				if err != nil {
					t.Fatalf("the event is gone: %v", err)
				}
				if !strings.Contains(string(now.Data), "From the phone") {
					t.Errorf("stored %s", now.Data)
				}
			})
		}
	})
}

func TestCalendarDeleteSeries(t *testing.T) {
	eachCalDAVServer(t, func(t *testing.T, s caldavServer, c *calendar.Client) {
		path, uid := createWeekly(t, c, s)
		events := seriesOf(t, c, s, uid)
		session := calendarSession(t, s)

		callTool[calendar.DeletedEvent](t, session, "calendar_delete_event", map[string]any{
			"event_id": path, "etag": events[0].ETag, "scope": "series",
		})

		if got := seriesOf(t, c, s, uid); len(got) != 0 {
			t.Errorf("events = %+v", got)
		}
		if _, err := c.Get(t.Context(), path); !errors.Is(err, calendar.ErrEventNotFound) {
			t.Errorf("get: %v, want ErrEventNotFound", err)
		}
	})
}

func TestCalendarUpdateMovesSeriesWithItsExceptions(t *testing.T) {
	eachCalDAVServer(t, func(t *testing.T, s caldavServer, c *calendar.Client) {
		path, uid := createWeekly(t, c, s)
		events := seriesOf(t, c, s, uid)
		session := calendarSession(t, s)

		callTool[calendar.UpdatedEvent](t, session, "calendar_update_event", map[string]any{
			"event_id": path, "etag": events[0].ETag, "scope": "series",
			"start": "2030-01-07T15:00", "timezone": "Europe/Amsterdam",
		})

		got := seriesOf(t, c, s, uid)
		if len(got) != 3 {
			t.Fatalf("events = %+v", got)
		}
		for i, want := range []string{"Weekly", "Special", "Weekly"} {
			if got[i].Summary != want || got[i].Start.Format("15:04") != "15:00" {
				t.Errorf("event %d = %q at %s", i, got[i].Summary, got[i].Start.Format(time.RFC3339))
			}
		}
	})
}

func TestCalendarUpdateOneOccurrence(t *testing.T) {
	eachCalDAVServer(t, func(t *testing.T, s caldavServer, c *calendar.Client) {
		path, uid := createWeekly(t, c, s)
		events := seriesOf(t, c, s, uid)
		session := calendarSession(t, s)

		callTool[calendar.UpdatedEvent](t, session, "calendar_update_event", map[string]any{
			"event_id": path, "etag": events[2].ETag, "scope": "occurrence", "occurrence_id": events[2].OccurrenceID,
			"summary": "Just this one", "start": "2030-01-25T15:00", "timezone": "Europe/Amsterdam",
		})

		got := seriesOf(t, c, s, uid)
		if len(got) != 3 {
			t.Fatalf("events = %+v", got)
		}
		want := []struct{ summary, start string }{
			{"Weekly", "2030-01-07T09:00:00Z"},
			{"Special", "2030-01-14T09:00:00Z"},
			{"Just this one", "2030-01-25T14:00:00Z"},
		}
		for i, w := range want {
			if got[i].Summary != w.summary || got[i].Start.UTC().Format(time.RFC3339) != w.start {
				t.Errorf("event %d = %q at %s, want %+v", i, got[i].Summary, got[i].Start.UTC().Format(time.RFC3339), w)
			}
		}
		if got[2].OccurrenceID != events[2].OccurrenceID {
			t.Errorf("id = %s, want it kept: %s", got[2].OccurrenceID, events[2].OccurrenceID)
		}
	})
}

func TestCalendarDeleteOneOccurrence(t *testing.T) {
	eachCalDAVServer(t, func(t *testing.T, s caldavServer, c *calendar.Client) {
		path, uid := createWeekly(t, c, s)
		events := seriesOf(t, c, s, uid)
		session := calendarSession(t, s)

		deleted := callTool[calendar.DeletedEvent](t, session, "calendar_delete_event", map[string]any{
			"event_id": path, "etag": events[0].ETag, "scope": "occurrence", "occurrence_id": events[0].OccurrenceID,
		})

		got := seriesOf(t, c, s, uid)
		if len(got) != 2 || got[0].Summary != "Special" || got[1].Summary != "Weekly" {
			t.Errorf("events = %+v", got)
		}
		if deleted.ETag == "" || deleted.ETag != got[0].ETag {
			t.Errorf("etag = %q, a read says %q", deleted.ETag, got[0].ETag)
		}
	})
}

func TestCalendarDeleteAMovedOccurrence(t *testing.T) {
	eachCalDAVServer(t, func(t *testing.T, s caldavServer, c *calendar.Client) {
		path, uid := createWeekly(t, c, s)
		events := seriesOf(t, c, s, uid)
		session := calendarSession(t, s)

		callTool[calendar.DeletedEvent](t, session, "calendar_delete_event", map[string]any{
			"event_id": path, "etag": events[1].ETag, "scope": "occurrence", "occurrence_id": events[1].OccurrenceID,
		})

		got := seriesOf(t, c, s, uid)
		if len(got) != 2 || got[0].Summary != "Weekly" || got[1].Summary != "Weekly" {
			t.Errorf("events = %+v", got)
		}
	})
}
