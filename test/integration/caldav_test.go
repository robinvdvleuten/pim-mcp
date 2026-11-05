//go:build integration

package integration

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/emersion/go-webdav"
	"github.com/emersion/go-webdav/caldav"

	"github.com/robinvdvleuten/pim-mcp/internal/calendar"
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

// go-webdav's queries return ETags too. Later tasks use those for writes, so
// both must be the same value.
func TestCalDAVETagsMatchGoWebDAVQueries(t *testing.T) {
	eachCalDAVServer(t, func(t *testing.T, s caldavServer, c *calendar.Client) {
		path, uid := newEventPath(t, s)
		ctx := t.Context()
		etag, err := c.Create(ctx, path, eventICS(uid, "first"))
		if err != nil {
			t.Fatalf("create: %v", err)
		}

		dav, err := caldav.NewClient(webdav.HTTPClientWithBasicAuth(nil, caldavUser, caldavPassword), s.url)
		if err != nil {
			t.Fatalf("new caldav client: %v", err)
		}
		objects, err := dav.MultiGetCalendar(ctx, s.calendar, &caldav.CalendarMultiGet{
			Paths:       []string{path},
			CompRequest: caldav.CalendarCompRequest{Name: "VCALENDAR", AllProps: true, AllComps: true},
		})
		if err != nil {
			t.Fatalf("multiget: %v", err)
		}

		if len(objects) != 1 {
			t.Fatalf("objects = %d, want 1", len(objects))
		}
		if objects[0].ETag != etag {
			t.Errorf("go-webdav etag = %q, want %q", objects[0].ETag, etag)
		}
		if _, err := c.Update(ctx, path, objects[0].ETag, eventICS(uid, "second")); err != nil {
			t.Errorf("update using go-webdav's etag: %v", err)
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
