//go:build integration

package integration

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/robinvdvleuten/pim-mcp/internal/calendar"
	"github.com/robinvdvleuten/pim-mcp/internal/importer"
	"github.com/robinvdvleuten/pim-mcp/internal/mail"
	"github.com/robinvdvleuten/pim-mcp/internal/mcpserver"
)

// importSession has mail and calendar tools, as the real server does.
func importSession(t *testing.T, user string, s caldavServer) *mcp.ClientSession {
	t.Helper()
	server := mcpserver.New(mcpserver.Options{
		Version: "test",
		Zone:    time.UTC,
		DialMail: func(ctx context.Context, _ string) (*mail.Client, error) {
			return mail.Dial(ctx, dovecotConfig(user))
		},
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

var inviteMails atomic.Int64

// inviteMail wraps calendar lines in a mail, the way mail clients send invites.
func inviteMail(method string, components ...string) string {
	lines := []string{"BEGIN:VCALENDAR", "VERSION:2.0", "PRODID:-//t//t//EN", "METHOD:" + method}
	lines = append(lines, components...)
	lines = append(lines, "END:VCALENDAR")
	// Each one later than the last, as tests read the newest message.
	sent := time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC).Add(time.Duration(inviteMails.Add(1)) * time.Second)
	return "From: Jan <jan@example.org>\nTo: me@example.org\nSubject: An invite\nDate: " + sent.Format(time.RFC1123Z) + "\nMIME-Version: 1.0\n" +
		"Content-Type: multipart/mixed; boundary=\"b\"\n\n--b\nContent-Type: text/plain; charset=utf-8\n\nPlease come.\n--b\n" +
		"Content-Type: text/calendar; charset=utf-8; method=" + method + "\n\n" + strings.Join(lines, "\n") + "\n--b--\n"
}

func vevent(uid string, lines ...string) string {
	return strings.Join(append(append([]string{"BEGIN:VEVENT", "UID:" + uid, "DTSTAMP:20261001T120000Z"}, lines...), "END:VEVENT"), "\n")
}

func eachImportServer(t *testing.T, test func(t *testing.T, s caldavServer, c *calendar.Client, user string, session *mcp.ClientSession)) {
	t.Helper()
	eachCalDAVServer(t, func(t *testing.T, s caldavServer, c *calendar.Client) {
		user := newMailbox(t)
		test(t, s, c, user, importSession(t, user, s))
	})
}

func newUID(t *testing.T) string {
	t.Helper()
	return newMailbox(t) + "@example.org"
}

func preview(t *testing.T, session *mcp.ClientSession, calendarID string) importer.Result {
	t.Helper()
	return callTool[importer.Result](t, session, "calendar_preview_import", map[string]any{
		"message_id": firstMessageID(t, session), "calendar_id": calendarID,
	})
}

func eventsNamed(t *testing.T, c *calendar.Client, s caldavServer, uid string, start time.Time) []calendar.Event {
	t.Helper()
	got, err := c.Events(t.Context(), []string{s.calendar}, start.AddDate(0, 0, -1), start.AddDate(0, 0, 30), time.UTC)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	return eventsOf(got.Events, uid)
}

func TestImportPreviewOfANewInviteWritesNothing(t *testing.T) {
	eachImportServer(t, func(t *testing.T, s caldavServer, c *calendar.Client, user string, session *mcp.ClientSession) {
		uid := newUID(t)
		seed(t, user, inviteMail("REQUEST", vevent(uid, "SEQUENCE:0", "DTSTART:20310310T090000Z", "DTEND:20310310T100000Z", `SUMMARY:Kickoff\, phase 1`, "ORGANIZER:mailto:jan@example.org")))

		got := preview(t, session, s.calendar)

		if got.Method != "REQUEST" || got.Written {
			t.Errorf("result = %+v", got)
		}
		if len(got.Events) != 1 {
			t.Fatalf("events = %+v", got.Events)
		}
		e := got.Events[0]
		if e.Action != importer.ActionCreate || e.UID != uid || e.Summary != "Kickoff, phase 1" || e.Organizer != "jan@example.org" {
			t.Errorf("event = %+v", e)
		}
		if !e.Start.Equal(time.Date(2031, 3, 10, 9, 0, 0, 0, time.UTC)) {
			t.Errorf("start = %s", e.Start)
		}
		if found := eventsNamed(t, c, s, uid, e.Start); len(found) != 0 {
			t.Errorf("preview wrote %+v", found)
		}
	})
}

func TestImportPreviewReportsBusyOverlap(t *testing.T) {
	eachImportServer(t, func(t *testing.T, s caldavServer, c *calendar.Client, user string, session *mcp.ClientSession) {
		createEvent(t, c, s, "BEGIN:VEVENT", "UID:@", "DTSTAMP:20261001T120000Z", "DTSTART:20310410T093000Z", "DTEND:20310410T103000Z", "SUMMARY:In the way", "END:VEVENT")
		seed(t, user, inviteMail("REQUEST", vevent(newUID(t), "DTSTART:20310410T090000Z", "DTEND:20310410T100000Z", "SUMMARY:Kickoff")))

		got := preview(t, session, s.calendar)

		if len(got.Events) != 1 || len(got.Events[0].BusyOverlap) == 0 {
			t.Fatalf("events = %+v", got.Events)
		}
		if !got.Events[0].BusyOverlap[0].Start.Equal(time.Date(2031, 4, 10, 9, 30, 0, 0, time.UTC)) {
			t.Errorf("overlap = %+v", got.Events[0].BusyOverlap)
		}
	})
}

func TestImportPreviewFindsExistingEvents(t *testing.T) {
	eachImportServer(t, func(t *testing.T, s caldavServer, c *calendar.Client, _ string, _ *mcp.ClientSession) {
		tests := []struct {
			name     string
			incoming int
			stored   int
			want     string
		}{
			{"same", 2, 2, importer.ActionSkip},
			{"newer", 3, 2, importer.ActionUpdate},
			{"older", 1, 2, importer.ActionSkip},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				path, uid := createEvent(t, c, s, "BEGIN:VEVENT", "UID:@", "DTSTAMP:20261001T120000Z", fmt.Sprintf("SEQUENCE:%d", tt.stored), "DTSTART:20310510T090000Z", "DTEND:20310510T100000Z", "SUMMARY:Stored", "END:VEVENT")
				mailbox := newMailbox(t)
				seed(t, mailbox, inviteMail("REQUEST", vevent(uid, fmt.Sprintf("SEQUENCE:%d", tt.incoming), "DTSTART:20310510T090000Z", "DTEND:20310510T100000Z", "SUMMARY:Incoming")))

				got := preview(t, importSession(t, mailbox, s), s.calendar)

				if len(got.Events) != 1 {
					t.Fatalf("events = %+v", got.Events)
				}
				e := got.Events[0]
				if e.Action != tt.want || e.ExistingEventID != path || e.ExistingSequence == nil || *e.ExistingSequence != tt.stored {
					t.Errorf("event = %+v, want action %s on %s", e, tt.want, path)
				}
			})
		}
	})
}

func TestImportPreviewOfACancellation(t *testing.T) {
	eachImportServer(t, func(t *testing.T, s caldavServer, c *calendar.Client, user string, session *mcp.ClientSession) {
		_, uid := createEvent(t, c, s, "BEGIN:VEVENT", "UID:@", "DTSTAMP:20261001T120000Z", "DTSTART:20310610T090000Z", "DTEND:20310610T100000Z", "SUMMARY:Stored", "END:VEVENT")
		seed(t, user, inviteMail("CANCEL", vevent(uid, "SEQUENCE:1", "DTSTART:20310610T090000Z", "SUMMARY:Stored", "STATUS:CANCELLED")))

		got := preview(t, session, s.calendar)

		if len(got.Events) != 1 || got.Events[0].Action != importer.ActionCancel {
			t.Errorf("events = %+v", got.Events)
		}
	})
}

func TestImportCannotBreakOutOfTheUIDQuery(t *testing.T) {
	eachImportServer(t, func(t *testing.T, s caldavServer, _ *calendar.Client, user string, session *mcp.ClientSession) {
		seed(t, user, inviteMail("REQUEST", vevent(`x</c:text-match><c:evil/>&amp;"`, "DTSTART:20310710T090000Z", "SUMMARY:Odd")))

		got := preview(t, session, s.calendar)

		if len(got.Events) != 1 || got.Events[0].Action != importer.ActionCreate {
			t.Errorf("events = %+v", got.Events)
		}
	})
}

func TestImportRefusals(t *testing.T) {
	s := caldavServers[0]
	createRadicaleCalendar(t, s)
	big := "DESCRIPTION:" + strings.Repeat("a", 74)
	var huge []string
	for range 16 * 1024 {
		huge = append(huge, " "+big)
	}

	tests := []struct {
		name    string
		message string
		want    string
	}{
		{"no calendar in the message", plainMail("Hello", "a@example.org", "b@example.org", "Mon, 02 Nov 2026 10:00:00 +0100", "just text"), "no calendar"},
		{"a reply, not an invite", inviteMail("REPLY", vevent("one", "DTSTART:20310710T090000Z")), "REPLY"},
		{"a broken calendar", "From: a@example.org\nSubject: x\nDate: Mon, 02 Nov 2026 10:00:00 +0100\nMIME-Version: 1.0\nContent-Type: text/calendar; charset=utf-8\n\nnot a calendar\n", "iCalendar"},
		{"a calendar over 1 MiB", inviteMail("PUBLISH", vevent("one", "DTSTART:20310710T090000Z", strings.Join(huge, "\n"))), "1 MiB"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			user := newMailbox(t)
			seed(t, user, tt.message)
			session := importSession(t, user, s)

			result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "calendar_preview_import", Arguments: map[string]any{
				"message_id": firstMessageID(t, session), "calendar_id": s.calendar,
			}})
			if err != nil {
				t.Fatal(err)
			}

			if !result.IsError || !strings.Contains(toolText(result), tt.want) {
				t.Errorf("error = %v, text = %q", result.IsError, toolText(result))
			}
		})
	}
}

func confirmImport(t *testing.T, session *mcp.ClientSession, calendarID string) importer.Result {
	t.Helper()
	return callTool[importer.Result](t, session, "calendar_import_from_email", map[string]any{
		"message_id": firstMessageID(t, session), "calendar_id": calendarID,
	})
}

func TestImportCreatesTheEvent(t *testing.T) {
	eachImportServer(t, func(t *testing.T, s caldavServer, c *calendar.Client, user string, session *mcp.ClientSession) {
		uid := newUID(t)
		seed(t, user, inviteMail("REQUEST", vevent(uid, "SEQUENCE:0", "DTSTART;TZID=Europe/Amsterdam:20320310T090000", "DTEND;TZID=Europe/Amsterdam:20320310T100000",
			"RRULE:FREQ=WEEKLY;COUNT=2", "SUMMARY:Kickoff, phase 1", "LOCATION:Room 4", "ORGANIZER:mailto:jan@example.org")))

		got := confirmImport(t, session, s.calendar)

		if !got.Written || len(got.Events) != 1 {
			t.Fatalf("result = %+v", got)
		}
		e := got.Events[0]
		if e.Action != importer.ActionCreate || e.EventID == "" || e.ETag == "" {
			t.Errorf("event = %+v", e)
		}
		found := eventsNamed(t, c, s, uid, time.Date(2032, 3, 10, 0, 0, 0, 0, time.UTC))
		if len(found) != 2 {
			t.Fatalf("found %+v", found)
		}
		// The comma is the point: Radicale would cut the text at a bare one.
		if found[0].Summary != "Kickoff, phase 1" || found[0].Location != "Room 4" || found[0].Timezone != "Europe/Amsterdam" || found[0].Organizer != "jan@example.org" {
			t.Errorf("stored = %+v", found[0])
		}
		if found[0].EventID != e.EventID || found[0].ETag != e.ETag {
			t.Errorf("stored as %s %s, reported %s %s", found[0].EventID, found[0].ETag, e.EventID, e.ETag)
		}
	})
}

func TestImportingTwiceIsANoOp(t *testing.T) {
	eachImportServer(t, func(t *testing.T, s caldavServer, c *calendar.Client, user string, session *mcp.ClientSession) {
		uid := newUID(t)
		seed(t, user, inviteMail("REQUEST", vevent(uid, "SEQUENCE:1", "DTSTART:20320410T090000Z", "DTEND:20320410T100000Z", "SUMMARY:Once")))
		confirmImport(t, session, s.calendar)
		before := eventsNamed(t, c, s, uid, time.Date(2032, 4, 10, 0, 0, 0, 0, time.UTC))

		got := confirmImport(t, session, s.calendar)

		if got.Written || got.Events[0].Action != importer.ActionSkip {
			t.Errorf("result = %+v", got)
		}
		after := eventsNamed(t, c, s, uid, time.Date(2032, 4, 10, 0, 0, 0, 0, time.UTC))
		if len(after) != 1 || after[0].ETag != before[0].ETag {
			t.Errorf("before %+v, after %+v", before, after)
		}
	})
}

func TestImportingANewerInviteUpdates(t *testing.T) {
	eachImportServer(t, func(t *testing.T, s caldavServer, c *calendar.Client, user string, session *mcp.ClientSession) {
		uid := newUID(t)
		seed(t, user, inviteMail("REQUEST", vevent(uid, "SEQUENCE:1", "DTSTART:20320510T090000Z", "DTEND:20320510T100000Z", "SUMMARY:First version")))
		confirmImport(t, session, s.calendar)
		seed(t, user, inviteMail("REQUEST", vevent(uid, "SEQUENCE:2", "DTSTART:20320510T140000Z", "DTEND:20320510T150000Z", "SUMMARY:Second version")))

		got := confirmImport(t, session, s.calendar)

		if !got.Written || got.Events[0].Action != importer.ActionUpdate {
			t.Errorf("result = %+v", got)
		}
		found := eventsNamed(t, c, s, uid, time.Date(2032, 5, 10, 0, 0, 0, 0, time.UTC))
		if len(found) != 1 || found[0].Summary != "Second version" || found[0].Start.UTC().Hour() != 14 {
			t.Errorf("found = %+v", found)
		}
	})
}

func TestImportingAnOlderInviteLeavesTheEvent(t *testing.T) {
	eachImportServer(t, func(t *testing.T, s caldavServer, c *calendar.Client, user string, session *mcp.ClientSession) {
		uid := newUID(t)
		seed(t, user, inviteMail("REQUEST", vevent(uid, "SEQUENCE:5", "DTSTART:20320610T090000Z", "DTEND:20320610T100000Z", "SUMMARY:Newer")))
		confirmImport(t, session, s.calendar)
		seed(t, user, inviteMail("REQUEST", vevent(uid, "SEQUENCE:2", "DTSTART:20320610T140000Z", "DTEND:20320610T150000Z", "SUMMARY:Older")))

		got := confirmImport(t, session, s.calendar)

		if got.Written {
			t.Errorf("result = %+v", got)
		}
		found := eventsNamed(t, c, s, uid, time.Date(2032, 6, 10, 0, 0, 0, 0, time.UTC))
		if len(found) != 1 || found[0].Summary != "Newer" {
			t.Errorf("found = %+v", found)
		}
	})
}

func TestImportingAChangedOccurrenceKeepsTheSeries(t *testing.T) {
	eachImportServer(t, func(t *testing.T, s caldavServer, c *calendar.Client, user string, session *mcp.ClientSession) {
		uid := newUID(t)
		seed(t, user, inviteMail("REQUEST", vevent(uid, "SEQUENCE:1", "DTSTART:20320710T090000Z", "DTEND:20320710T100000Z", "RRULE:FREQ=DAILY;COUNT=3", "SUMMARY:Series")))
		confirmImport(t, session, s.calendar)
		seed(t, user, inviteMail("REQUEST", vevent(uid, "SEQUENCE:2", "RECURRENCE-ID:20320711T090000Z", "DTSTART:20320711T150000Z", "DTEND:20320711T160000Z", "SUMMARY:Changed")))

		confirmImport(t, session, s.calendar)

		found := eventsNamed(t, c, s, uid, time.Date(2032, 7, 10, 0, 0, 0, 0, time.UTC))
		if len(found) != 3 || found[0].Summary != "Series" || found[1].Summary != "Changed" || found[2].Summary != "Series" {
			t.Errorf("found = %+v", found)
		}
	})
}

func TestImportMapsWindowsZones(t *testing.T) {
	eachImportServer(t, func(t *testing.T, s caldavServer, c *calendar.Client, user string, session *mcp.ClientSession) {
		uid := newUID(t)
		seed(t, user, inviteMail("REQUEST", vevent(uid, "DTSTART;TZID=W. Europe Standard Time:20320810T090000", "DTEND;TZID=W. Europe Standard Time:20320810T100000", "SUMMARY:From Outlook")))

		confirmImport(t, session, s.calendar)

		found := eventsNamed(t, c, s, uid, time.Date(2032, 8, 10, 0, 0, 0, 0, time.UTC))
		if len(found) != 1 || found[0].Timezone != "Europe/Berlin" || !found[0].Start.Equal(time.Date(2032, 8, 10, 7, 0, 0, 0, time.UTC)) {
			t.Errorf("found = %+v", found)
		}
	})
}

// baikalSchedulingMessages counts what Baïkal has delivered to users' schedule
// inboxes, which is where a reply to a local organizer ends up.
func baikalSchedulingMessages(t *testing.T) int {
	t.Helper()
	out, err := exec.Command("docker", "compose", "-f", "compose.yaml", "exec", "-T", "baikal", "php", "-r",
		`echo (new PDO("sqlite:/var/www/baikal/Specific/db/db.sqlite"))->query("SELECT COUNT(*) FROM schedulingobjects")->fetchColumn();`).CombinedOutput()
	if err != nil {
		t.Fatalf("count: %v\n%s", err, out)
	}
	count, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		t.Fatalf("count %q: %v", out, err)
	}
	return count
}

// When an attendee's answer changes, a server with implicit scheduling tells the
// organizer. pim-mcp must never cause a message to be sent.
func TestImportSendsNoSchedulingReply(t *testing.T) {
	s := caldavServers[1]
	answering := func(uid, partstat string, sequence int) []string {
		return []string{"BEGIN:VEVENT", "UID:" + uid, "DTSTAMP:20261001T120000Z", fmt.Sprintf("SEQUENCE:%d", sequence), "DTSTART:20320910T090000Z", "DTEND:20320910T100000Z",
			"SUMMARY:Scheduling", "ORGANIZER:mailto:owner@example.org", "ATTENDEE;PARTSTAT=" + partstat + ":mailto:tester@example.org", "END:VEVENT"}
	}
	plainPut := func(path string, lines []string) {
		t.Helper()
		body := strings.Join(append(append([]string{"BEGIN:VCALENDAR", "VERSION:2.0", "PRODID:-//t//t//EN"}, lines...), "END:VCALENDAR", ""), "\r\n")
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPut, strings.TrimSuffix(baikalURL, "/")+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.SetBasicAuth(caldavUser, caldavPassword)
		req.Header.Set("Content-Type", "text/calendar; charset=utf-8")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode/100 != 2 {
			t.Fatalf("put: %s", resp.Status)
		}
	}

	// First show that this setup does send one, or the test would prove nothing.
	control := newUID(t)
	plainPut(s.calendar+"control-"+control+".ics", answering(control, "NEEDS-ACTION", 0))
	before := baikalSchedulingMessages(t)
	plainPut(s.calendar+"control-"+control+".ics", answering(control, "ACCEPTED", 0))
	if baikalSchedulingMessages(t) != before+1 {
		t.Fatalf("a plain PUT sent no reply, so this setup can't show the difference")
	}

	user := newMailbox(t)
	uid := newUID(t)
	session := importSession(t, user, s)
	seed(t, user, inviteMail("REQUEST", strings.Join(answering(uid, "NEEDS-ACTION", 1), "\n")))
	confirmImport(t, session, s.calendar)
	before = baikalSchedulingMessages(t)
	seed(t, user, inviteMail("REQUEST", strings.Join(answering(uid, "ACCEPTED", 2), "\n")))

	got := confirmImport(t, session, s.calendar)

	if !got.Written || got.Events[0].Action != importer.ActionUpdate {
		t.Fatalf("result = %+v", got)
	}
	if after := baikalSchedulingMessages(t); after != before {
		t.Errorf("scheduling messages went from %d to %d", before, after)
	}
}

func TestImportCancellationDeletesTheEvent(t *testing.T) {
	eachImportServer(t, func(t *testing.T, s caldavServer, c *calendar.Client, user string, session *mcp.ClientSession) {
		uid := newUID(t)
		seed(t, user, inviteMail("REQUEST", vevent(uid, "SEQUENCE:1", "DTSTART:20330110T090000Z", "DTEND:20330110T100000Z", "RRULE:FREQ=DAILY;COUNT=3", "SUMMARY:Called off")))
		imported := confirmImport(t, session, s.calendar)
		seed(t, user, inviteMail("CANCEL", vevent(uid, "SEQUENCE:2", "DTSTART:20330110T090000Z", "STATUS:CANCELLED", "SUMMARY:Called off")))

		got := confirmImport(t, session, s.calendar)

		if !got.Written || got.Events[0].Action != importer.ActionCancel {
			t.Errorf("result = %+v", got)
		}
		if found := eventsNamed(t, c, s, uid, time.Date(2033, 1, 10, 0, 0, 0, 0, time.UTC)); len(found) != 0 {
			t.Errorf("found = %+v", found)
		}
		if _, err := c.Get(t.Context(), imported.Events[0].EventID); !errors.Is(err, calendar.ErrEventNotFound) {
			t.Errorf("get: %v, want it gone", err)
		}
	})
}

func TestImportCancellationOfOneOccurrenceKeepsTheRest(t *testing.T) {
	eachImportServer(t, func(t *testing.T, s caldavServer, c *calendar.Client, user string, session *mcp.ClientSession) {
		uid := newUID(t)
		seed(t, user, inviteMail("REQUEST", vevent(uid, "SEQUENCE:1", "DTSTART:20330210T090000Z", "DTEND:20330210T100000Z", "RRULE:FREQ=DAILY;COUNT=3", "SUMMARY:Series")))
		confirmImport(t, session, s.calendar)
		seed(t, user, inviteMail("CANCEL", vevent(uid, "SEQUENCE:2", "RECURRENCE-ID:20330211T090000Z", "DTSTART:20330211T090000Z", "STATUS:CANCELLED", "SUMMARY:Series")))

		got := confirmImport(t, session, s.calendar)

		if !got.Written || got.Events[0].Action != importer.ActionCancel || got.Events[0].ETag == "" {
			t.Errorf("result = %+v", got)
		}
		found := eventsNamed(t, c, s, uid, time.Date(2033, 2, 10, 0, 0, 0, 0, time.UTC))
		if len(found) != 2 || found[0].OccurrenceID != "20330210T090000Z" || found[1].OccurrenceID != "20330212T090000Z" {
			t.Errorf("found = %+v", found)
		}
	})
}

func TestImportCancellationNeedsNoStoredEvent(t *testing.T) {
	eachImportServer(t, func(t *testing.T, s caldavServer, _ *calendar.Client, user string, session *mcp.ClientSession) {
		seed(t, user, inviteMail("CANCEL", vevent(newUID(t), "SEQUENCE:2", "DTSTART:20330310T090000Z", "SUMMARY:Unknown")))

		got := confirmImport(t, session, s.calendar)

		if got.Written || got.Events[0].Action != importer.ActionSkip {
			t.Errorf("result = %+v", got)
		}
	})
}

func TestImportCancellationOfAnOccurrenceAlreadyGone(t *testing.T) {
	eachImportServer(t, func(t *testing.T, s caldavServer, c *calendar.Client, user string, session *mcp.ClientSession) {
		uid := newUID(t)
		seed(t, user, inviteMail("REQUEST", vevent(uid, "SEQUENCE:1", "DTSTART:20330410T090000Z", "DTEND:20330410T100000Z", "RRULE:FREQ=DAILY;COUNT=2", "SUMMARY:Series")))
		confirmImport(t, session, s.calendar)
		seed(t, user, inviteMail("CANCEL", vevent(uid, "SEQUENCE:2", "RECURRENCE-ID:20330411T090000Z", "DTSTART:20330411T090000Z", "SUMMARY:Series")))
		confirmImport(t, session, s.calendar)

		got := confirmImport(t, session, s.calendar)

		if got.Written {
			t.Errorf("result = %+v", got)
		}
		if found := eventsNamed(t, c, s, uid, time.Date(2033, 4, 10, 0, 0, 0, 0, time.UTC)); len(found) != 1 {
			t.Errorf("found = %+v", found)
		}
	})
}

func TestImportCancellationPreviewWritesNothing(t *testing.T) {
	eachImportServer(t, func(t *testing.T, s caldavServer, c *calendar.Client, user string, session *mcp.ClientSession) {
		uid := newUID(t)
		seed(t, user, inviteMail("REQUEST", vevent(uid, "SEQUENCE:1", "DTSTART:20330510T090000Z", "DTEND:20330510T100000Z", "SUMMARY:Still here")))
		confirmImport(t, session, s.calendar)
		seed(t, user, inviteMail("CANCEL", vevent(uid, "SEQUENCE:2", "DTSTART:20330510T090000Z", "SUMMARY:Still here")))

		got := preview(t, session, s.calendar)

		if got.Written || got.Events[0].Action != importer.ActionCancel {
			t.Errorf("result = %+v", got)
		}
		if found := eventsNamed(t, c, s, uid, time.Date(2033, 5, 10, 0, 0, 0, 0, time.UTC)); len(found) != 1 {
			t.Errorf("found = %+v", found)
		}
	})
}

func TestImportNeedsACalendar(t *testing.T) {
	user := newMailbox(t)
	seed(t, user, inviteMail("REQUEST", vevent(newUID(t), "DTSTART:20340110T090000Z", "SUMMARY:x")))
	session := importSession(t, user, caldavServers[1])

	for _, tool := range []string{"calendar_preview_import", "calendar_import_from_email"} {
		result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: tool, Arguments: map[string]any{
			"message_id": firstMessageID(t, session), "calendar_id": "",
		}})
		if err != nil {
			t.Fatal(err)
		}
		if !result.IsError {
			t.Errorf("%s: call succeeded: %s", tool, toolText(result))
		}
	}
}

func TestImportCancellationOfTheOnlyOccurrenceRemovesTheEvent(t *testing.T) {
	eachImportServer(t, func(t *testing.T, s caldavServer, c *calendar.Client, user string, session *mcp.ClientSession) {
		uid := newUID(t)
		seed(t, user, inviteMail("REQUEST", vevent(uid, "SEQUENCE:1", "RECURRENCE-ID:20340210T090000Z", "DTSTART:20340210T090000Z", "DTEND:20340210T100000Z", "SUMMARY:Just this one")))
		imported := confirmImport(t, session, s.calendar)
		seed(t, user, inviteMail("CANCEL", vevent(uid, "SEQUENCE:2", "RECURRENCE-ID:20340210T090000Z", "DTSTART:20340210T090000Z", "SUMMARY:Just this one")))

		got := confirmImport(t, session, s.calendar)

		if !got.Written {
			t.Errorf("result = %+v", got)
		}
		if _, err := c.Get(t.Context(), imported.Events[0].EventID); !errors.Is(err, calendar.ErrEventNotFound) {
			t.Errorf("get: %v, want it gone", err)
		}
	})
}

func TestImportUpdateReportsTheStoredETag(t *testing.T) {
	eachImportServer(t, func(t *testing.T, s caldavServer, c *calendar.Client, user string, session *mcp.ClientSession) {
		uid := newUID(t)
		seed(t, user, inviteMail("REQUEST", vevent(uid, "SEQUENCE:1", "DTSTART:20340310T090000Z", "DTEND:20340310T100000Z", "SUMMARY:First")))
		first := confirmImport(t, session, s.calendar)
		seed(t, user, inviteMail("REQUEST", vevent(uid, "SEQUENCE:2", "DTSTART:20340310T090000Z", "DTEND:20340310T100000Z", "SUMMARY:Second")))

		second := confirmImport(t, session, s.calendar)

		if second.Events[0].EventID != first.Events[0].EventID || second.Events[0].ETag == first.Events[0].ETag {
			t.Errorf("first = %+v, second = %+v", first.Events[0], second.Events[0])
		}
		now, err := c.Get(t.Context(), first.Events[0].EventID)
		if err != nil {
			t.Fatal(err)
		}
		if now.ETag != second.Events[0].ETag {
			t.Errorf("stored etag = %s, reported %s", now.ETag, second.Events[0].ETag)
		}
	})
}

func TestImportUsesTheCalendarPartThatCanBeRead(t *testing.T) {
	s := caldavServers[0]
	createRadicaleCalendar(t, s)
	uid := newUID(t)
	calendarBody := "BEGIN:VCALENDAR\nVERSION:2.0\nPRODID:-//t//t//EN\n" + vevent(uid, "DTSTART:20370110T090000Z", "SUMMARY:In the second part") + "\nEND:VCALENDAR"
	user := newMailbox(t)
	seed(t, user, "From: a@example.org\nSubject: Two\nDate: Mon, 02 Nov 2026 10:00:00 +0100\nMIME-Version: 1.0\nContent-Type: multipart/mixed; boundary=\"b\"\n\n--b\nContent-Type: text/calendar\n\n\n--b\n"+
		"Content-Type: application/octet-stream; name=\"invite.ics\"\nContent-Disposition: attachment; filename=\"invite.ics\"\n\n"+calendarBody+"\n--b--\n")

	got := preview(t, importSession(t, user, s), s.calendar)

	if len(got.Events) != 1 || got.Events[0].UID != uid {
		t.Errorf("events = %+v", got.Events)
	}
}

func TestImportOfAnEmptyCalendarPartSaysSo(t *testing.T) {
	s := caldavServers[0]
	createRadicaleCalendar(t, s)
	user := newMailbox(t)
	seed(t, user, "From: a@example.org\nSubject: Empty\nDate: Mon, 02 Nov 2026 10:00:00 +0100\nMIME-Version: 1.0\nContent-Type: multipart/mixed; boundary=\"b\"\n\n--b\nContent-Type: text/calendar\n\n\n--b--\n")
	session := importSession(t, user, s)

	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "calendar_preview_import", Arguments: map[string]any{
		"message_id": firstMessageID(t, session), "calendar_id": s.calendar,
	}})
	if err != nil {
		t.Fatal(err)
	}

	if !result.IsError || !strings.Contains(toolText(result), "empty") {
		t.Errorf("error = %v, text = %q", result.IsError, toolText(result))
	}
}

// A real invite had a UID holding an escaped newline. It has to be found again, or
// importing it a second time wouldn't be a no-op.
func TestImportingAnOddUIDTwiceIsANoOp(t *testing.T) {
	eachImportServer(t, func(t *testing.T, s caldavServer, _ *calendar.Client, user string, session *mcp.ClientSession) {
		uid := `UID:` + newUID(t) + `\n`
		seed(t, user, inviteMail("PUBLISH", vevent(uid, "DTSTART:20370210T090000Z", "DTEND:20370210T100000Z", "SUMMARY:Odd")))
		if got := confirmImport(t, session, s.calendar); !got.Written {
			t.Fatalf("first import = %+v", got)
		}

		got := confirmImport(t, session, s.calendar)

		if got.Written || got.Events[0].Action != importer.ActionSkip {
			t.Errorf("second import = %+v", got)
		}
	})
}

func TestImportingAnEventWithoutUIDTwiceIsANoOp(t *testing.T) {
	eachImportServer(t, func(t *testing.T, s caldavServer, _ *calendar.Client, user string, session *mcp.ClientSession) {
		summary := "Haircut " + newUID(t)
		seed(t, user, inviteMail("PUBLISH", "BEGIN:VEVENT\nDTSTAMP:20261001T120000Z\nDTSTART:20370310T090000Z\nDTEND:20370310T100000Z\nSUMMARY:"+summary+"\nEND:VEVENT"))
		if got := confirmImport(t, session, s.calendar); !got.Written {
			t.Fatalf("first import = %+v", got)
		}

		got := confirmImport(t, session, s.calendar)

		if got.Written || got.Events[0].Action != importer.ActionSkip {
			t.Errorf("second import = %+v", got)
		}
	})
}
