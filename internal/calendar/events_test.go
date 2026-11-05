package calendar

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/robinvdvleuten/pim-mcp/internal/ical"
)

func icsEvent(uid, start string, extra ...string) string {
	lines := []string{"BEGIN:VCALENDAR", "VERSION:2.0", "PRODID:-//t//t//EN", "BEGIN:VEVENT", "UID:" + uid, "DTSTAMP:20261101T120000Z", "DTSTART:" + start}
	lines = append(lines, extra...)
	lines = append(lines, "END:VEVENT", "END:VCALENDAR", "")
	return strings.Join(lines, "\r\n")
}

func eventResponse(href, etag, data string) string {
	data = strings.NewReplacer("&", "&amp;", "<", "&lt;").Replace(data)
	return fmt.Sprintf(`<d:response><d:href>%s</d:href><d:propstat><d:prop>
<d:getetag>"%s"</d:getetag><c:calendar-data>%s</c:calendar-data>
</d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>`, href, etag, data)
}

// reportServer answers REPORT per calendar path and records the requests.
func reportServer(t *testing.T, responses map[string]string) (*Client, *[]string) {
	t.Helper()
	var bodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "REPORT" {
			t.Errorf("method = %s", r.Method)
		}
		if r.Header.Get("Depth") != "1" {
			t.Errorf("depth = %q", r.Header.Get("Depth"))
		}
		body, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(body))
		response, ok := responses[r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusMultiStatus)
		_, _ = w.Write([]byte(multistatusHead + response + "</d:multistatus>"))
	}))
	t.Cleanup(server.Close)
	client, err := New(Config{URL: server.URL + "/"})
	if err != nil {
		t.Fatal(err)
	}
	return client, &bodies
}

var (
	rangeStart = time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	rangeEnd   = time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
)

func TestEventsCarryWhatWritesNeedAcrossCalendars(t *testing.T) {
	client, _ := reportServer(t, map[string]string{
		"/cal/a/": eventResponse("/cal/a/later.ics", "etag-later", icsEvent("later", "20261110T150000Z")),
		"/cal/b/": eventResponse("/cal/b/early.ics", "etag-early", icsEvent("early", "20261110T080000Z")),
	})

	got, err := client.Events(t.Context(), []string{"/cal/a/", "/cal/b/"}, rangeStart, rangeEnd, time.UTC)
	if err != nil {
		t.Fatalf("events: %v", err)
	}

	if len(got.Events) != 2 {
		t.Fatalf("events = %+v", got.Events)
	}
	first, second := got.Events[0], got.Events[1]
	if first.UID != "early" || first.EventID != "/cal/b/early.ics" || first.ETag != "etag-early" || first.CalendarID != "/cal/b/" {
		t.Errorf("first = %+v", first)
	}
	if second.UID != "later" || second.EventID != "/cal/a/later.ics" || second.ETag != "etag-later" || second.CalendarID != "/cal/a/" {
		t.Errorf("second = %+v", second)
	}
	if len(got.Skipped) != 0 {
		t.Errorf("skipped = %+v", got.Skipped)
	}
}

func TestEventsAsksTheServerForAWiderRange(t *testing.T) {
	// A server reads floating times in a zone of its own, so it may place an event
	// up to a day away from where the user's zone puts it.
	client, bodies := reportServer(t, map[string]string{"/cal/": ""})

	if _, err := client.Events(t.Context(), []string{"/cal/"}, rangeStart, rangeEnd, time.UTC); err != nil {
		t.Fatalf("events: %v", err)
	}

	if len(*bodies) != 1 {
		t.Fatalf("requests = %d", len(*bodies))
	}
	body := (*bodies)[0]
	for _, want := range []string{`start="20261109T000000Z"`, `end="20261112T000000Z"`, `name="VEVENT"`, "calendar-data", "getetag"} {
		if !strings.Contains(body, want) {
			t.Errorf("request %q doesn't contain %q", body, want)
		}
	}
}

func TestEventsIsExactAboutTheRange(t *testing.T) {
	// The server's wider answer holds an event just outside the requested range.
	client, _ := reportServer(t, map[string]string{
		"/cal/": eventResponse("/cal/in.ics", "1", icsEvent("in", "20261110T120000Z")) +
			eventResponse("/cal/out.ics", "2", icsEvent("out", "20261109T120000Z")),
	})

	got, err := client.Events(t.Context(), []string{"/cal/"}, rangeStart, rangeEnd, time.UTC)
	if err != nil {
		t.Fatalf("events: %v", err)
	}

	if len(got.Events) != 1 || got.Events[0].UID != "in" {
		t.Errorf("events = %+v", got.Events)
	}
}

func TestEventsSkipsBrokenObjectsAndSaysSo(t *testing.T) {
	client, _ := reportServer(t, map[string]string{
		"/cal/": eventResponse("/cal/good.ics", "1", icsEvent("good", "20261110T120000Z")) +
			eventResponse("/cal/garbage.ics", "2", "this is not a calendar") +
			eventResponse("/cal/no-start.ics", "3", strings.Replace(icsEvent("no-start", "20261110T120000Z"), "DTSTART:20261110T120000Z\r\n", "", 1)) +
			eventResponse("/cal/mars.ics", "4", icsEvent("mars", "20261110T120000", "RRULE:FREQ=NEVER")) +
			`<d:response><d:href>/cal/no-data.ics</d:href><d:propstat><d:prop><d:getetag>"5"</d:getetag></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>`,
	})

	got, err := client.Events(t.Context(), []string{"/cal/"}, rangeStart, rangeEnd, time.UTC)
	if err != nil {
		t.Fatalf("events: %v", err)
	}

	if len(got.Events) != 1 || got.Events[0].UID != "good" {
		t.Errorf("events = %+v", got.Events)
	}
	skipped := map[string]string{}
	for _, skip := range got.Skipped {
		skipped[skip.EventID] = skip.Reason
	}
	for _, id := range []string{"/cal/garbage.ics", "/cal/no-start.ics", "/cal/mars.ics", "/cal/no-data.ics"} {
		if skipped[id] == "" {
			t.Errorf("%s isn't reported: %+v", id, got.Skipped)
		}
	}
}

func TestEventsCapAppliesAcrossObjects(t *testing.T) {
	// 600 occurrences in each of two objects: each fits the cap, together they don't.
	daily := func(uid string) string {
		return icsEvent(uid, "20260101T000000Z", "RRULE:FREQ=HOURLY;COUNT=600")
	}
	client, _ := reportServer(t, map[string]string{
		"/a/": eventResponse("/a/1.ics", "1", daily("one")),
		"/b/": eventResponse("/b/2.ics", "2", daily("two")),
	})

	_, err := client.Events(t.Context(), []string{"/a/", "/b/"},
		time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), time.UTC)

	if !errors.Is(err, ical.ErrTooManyOccurrences) {
		t.Errorf("err = %v, want ErrTooManyOccurrences", err)
	}
}

func TestEventsTooManyInOneObjectIsAnError(t *testing.T) {
	client, _ := reportServer(t, map[string]string{
		"/a/": eventResponse("/a/1.ics", "1", icsEvent("one", "20260101T000000Z", "RRULE:FREQ=HOURLY")),
	})

	_, err := client.Events(t.Context(), []string{"/a/"},
		time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), time.UTC)

	if !errors.Is(err, ical.ErrTooManyOccurrences) {
		t.Errorf("err = %v, want it not to be skipped silently", err)
	}
}

func TestEventsRefusesBadRangesWithoutAsking(t *testing.T) {
	client, bodies := reportServer(t, map[string]string{"/a/": ""})

	_, err := client.Events(t.Context(), []string{"/a/"}, rangeEnd, rangeStart, time.UTC)
	if !errors.Is(err, ical.ErrInvalidRange) {
		t.Errorf("err = %v, want ErrInvalidRange", err)
	}
	_, err = client.Events(t.Context(), []string{"/a/"}, rangeStart, rangeStart.AddDate(2, 0, 0), time.UTC)
	if !errors.Is(err, ical.ErrRangeTooLarge) {
		t.Errorf("err = %v, want ErrRangeTooLarge", err)
	}
	if len(*bodies) != 0 {
		t.Errorf("sent %d requests", len(*bodies))
	}
}

func TestEventsUnknownCalendarIsAnError(t *testing.T) {
	client, _ := reportServer(t, nil)

	_, err := client.Events(t.Context(), []string{"/nope/"}, rangeStart, rangeEnd, time.UTC)

	if err == nil || !strings.Contains(err.Error(), "/nope/") {
		t.Errorf("err = %v", err)
	}
}
