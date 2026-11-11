//go:build integration

package integration

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/robinvdvleuten/pim-mcp/internal/calendar"
	"github.com/robinvdvleuten/pim-mcp/internal/importer"
	"github.com/robinvdvleuten/pim-mcp/internal/mcpserver"
)

// Debug logging is what someone turns on to chase a problem, and pastes into a bug
// report. It runs the real binary against real servers.
func TestDebugLogsHoldNoSecretsOrContent(t *testing.T) {
	// The mailbox and the calendar outlive a run, so this run's data is told
	// apart by a mark of its own.
	mark := newUID(t)
	var (
		body    = "the-secret-body-of-this-message-" + mark
		subject = "the-private-subject-" + mark
		summary = "the-private-event-" + mark
	)
	s := caldavServers[0]
	createRadicaleCalendar(t, s)
	home := t.TempDir()
	binary := buildBinary(t)
	// Dovecot takes any user name, so the account's one, which Radicale knows, works
	// for both.
	if out, err := runBinary(t, binary, home, caldavPassword, "account", "add",
		"--email", "tester@example.org", "--username", caldavUser,
		"--imap", dovecotPlainAddr, "--imap-security", "none", "--caldav", s.url); err != nil {
		t.Fatalf("add: %v\n%s", err, out)
	}
	seed(t, caldavUser, plainMail(subject, "a@example.org", "b@example.org", "Mon, 02 Nov 2026 10:00:00 +0100", body))

	var stderr bytes.Buffer
	cmd := exec.Command(binary, "serve")
	cmd.Env = append(os.Environ(), "HOME="+home, "XDG_CONFIG_HOME="+filepath.Join(home, "config"),
		"PIM_MCP_PASSWORD="+caldavPassword, "PIM_MCP_DEBUG=1")
	cmd.Stderr = &stderr
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "test"}, nil)
	session, err := client.Connect(t.Context(), &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}

	found := callTool[mcpserver.SearchResult](t, session, "mail_search", map[string]any{"subject": subject})
	if len(found.Messages) != 1 {
		t.Fatalf("messages = %+v", found.Messages)
	}
	got := callTool[map[string]any](t, session, "mail_get", map[string]any{"id": found.Messages[0].ID})
	if !strings.Contains(got["body"].(string), body) {
		t.Errorf("mail_get didn't return the body: %v", got)
	}
	callTool[map[string]any](t, session, "calendar_create_event", map[string]any{
		"calendar_id": s.calendar, "summary": summary, "start": "2034-01-10T09:00:00Z", "end": "2034-01-10T10:00:00Z",
	})
	callTool[map[string]any](t, session, "calendar_list_events", map[string]any{
		"start": "2034-01-10T00:00:00Z", "end": "2034-01-11T00:00:00Z", "calendar_ids": []string{s.calendar},
	})
	if err := session.Close(); err != nil {
		t.Errorf("close: %v", err)
	}

	logs := stderr.String()
	if !strings.Contains(logs, "tool=mail_get") || !strings.Contains(logs, "level=DEBUG") {
		t.Errorf("debug logging isn't on, so this proves nothing: %s", logs)
	}
	for _, secret := range []string{caldavPassword, body, subject, summary} {
		if strings.Contains(logs, secret) {
			t.Errorf("the logs contain %q:\n%s", secret, logs)
		}
	}
}

// serveBinary starts the real binary with an account, as an MCP client would.
func serveBinary(t *testing.T, s caldavServer, user string, args ...string) *mcp.ClientSession {
	t.Helper()
	home := t.TempDir()
	binary := buildBinary(t)
	if out, err := runBinary(t, binary, home, caldavPassword, "account", "add",
		"--email", "tester@example.org", "--username", user,
		"--imap", dovecotPlainAddr, "--imap-security", "none", "--caldav", s.url); err != nil {
		t.Fatalf("add: %v\n%s", err, out)
	}
	cmd := exec.Command(binary, append([]string{"serve"}, args...)...)
	cmd.Env = append(os.Environ(), "HOME="+home, "XDG_CONFIG_HOME="+filepath.Join(home, "config"), "PIM_MCP_PASSWORD="+caldavPassword)
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "test"}, nil)
	session, err := client.Connect(t.Context(), &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

// The story the project exists for, from a mail in the inbox to an event in the
// calendar, through the binary an MCP client launches.
func TestCoreStoryThroughTheRealBinary(t *testing.T) {
	s := caldavServers[0]
	createRadicaleCalendar(t, s)
	uid := newUID(t)
	subject := "Kickoff " + uid
	message := strings.Replace(inviteMail("REQUEST", vevent(uid, "SEQUENCE:0", "DTSTART;TZID=W. Europe Standard Time:20350115T090000",
		"DTEND;TZID=W. Europe Standard Time:20350115T100000", "RRULE:FREQ=WEEKLY;COUNT=2", "SUMMARY:Kickoff, phase 1")), "Subject: An invite", "Subject: "+subject, 1)
	seed(t, caldavUser, message)
	session := serveBinary(t, s, caldavUser)

	found := callTool[mcpserver.SearchResult](t, session, "mail_search", map[string]any{"subject": subject, "has_calendar": true})
	if len(found.Messages) != 1 {
		t.Fatalf("messages = %+v", found.Messages)
	}
	calendars := callTool[mcpserver.CalendarList](t, session, "calendar_list_calendars", nil)
	if !slices.ContainsFunc(calendars.Calendars, func(c calendar.Calendar) bool { return c.ID == s.calendar }) {
		t.Fatalf("calendars = %+v", calendars.Calendars)
	}
	args := map[string]any{"message_id": found.Messages[0].ID, "calendar_id": s.calendar}

	previewed := callTool[importer.Result](t, session, "calendar_preview_import", args)
	if previewed.Written || previewed.Events[0].Action != importer.ActionCreate {
		t.Fatalf("preview = %+v", previewed)
	}
	imported := callTool[importer.Result](t, session, "calendar_import_from_email", args)
	if !imported.Written {
		t.Fatalf("import = %+v", imported)
	}

	listed := callTool[calendar.EventList](t, session, "calendar_list_events", map[string]any{
		"start": "2035-01-01T00:00:00Z", "end": "2035-02-01T00:00:00Z", "calendar_ids": []string{s.calendar},
	})
	events := eventsOf(listed.Events, uid)
	if len(events) != 2 {
		t.Fatalf("events = %+v", listed.Events)
	}
	if events[0].Summary != "Kickoff, phase 1" || !events[0].Recurring || events[0].Start.UTC().Format(time.RFC3339) != "2035-01-15T08:00:00Z" {
		t.Errorf("first = %+v", events[0])
	}

	// Later: the agent moves one occurrence, using what it just read.
	callTool[calendar.UpdatedEvent](t, session, "calendar_update_event", map[string]any{
		"event_id": events[1].EventID, "etag": events[1].ETag, "scope": "occurrence", "occurrence_id": events[1].OccurrenceID, "summary": "Just this one",
	})
	listed = callTool[calendar.EventList](t, session, "calendar_list_events", map[string]any{
		"start": "2035-01-01T00:00:00Z", "end": "2035-02-01T00:00:00Z", "calendar_ids": []string{s.calendar},
	})
	events = eventsOf(listed.Events, uid)
	if len(events) != 2 || events[0].Summary != "Kickoff, phase 1" || events[1].Summary != "Just this one" {
		t.Errorf("after the edit = %+v", events)
	}
}

func TestReadOnlyBinaryCannotWrite(t *testing.T) {
	s := caldavServers[0]
	createRadicaleCalendar(t, s)
	session := serveBinary(t, s, caldavUser, "--read-only")

	tools, err := session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}

	names := make([]string, 0, len(tools.Tools))
	for _, tool := range tools.Tools {
		names = append(names, tool.Name)
	}
	slices.Sort(names)
	want := []string{"account_info", "calendar_free_busy", "calendar_list_calendars", "calendar_list_events", "calendar_preview_import", "mail_get", "mail_list_folders", "mail_search"}
	if !slices.Equal(names, want) {
		t.Errorf("tools = %v, want %v", names, want)
	}
}

// Read-only mode exists so nothing can change, while a preview still has to work,
// as it's how someone decides whether to allow the write.
func TestReadOnlyBinaryCanPreviewAnImport(t *testing.T) {
	s := caldavServers[0]
	createRadicaleCalendar(t, s)
	uid := newUID(t)
	subject := "Preview " + uid
	seed(t, caldavUser, strings.Replace(inviteMail("REQUEST", vevent(uid, "DTSTART:20360115T090000Z", "DTEND:20360115T100000Z", "SUMMARY:Read only")), "Subject: An invite", "Subject: "+subject, 1))
	session := serveBinary(t, s, caldavUser, "--read-only")
	found := callTool[mcpserver.SearchResult](t, session, "mail_search", map[string]any{"subject": subject})
	if len(found.Messages) != 1 {
		t.Fatalf("messages = %+v", found.Messages)
	}

	got := callTool[importer.Result](t, session, "calendar_preview_import", map[string]any{
		"message_id": found.Messages[0].ID, "calendar_id": s.calendar,
	})

	if got.Written || len(got.Events) != 1 || got.Events[0].Action != importer.ActionCreate {
		t.Errorf("preview = %+v", got)
	}
	c, err := calendar.New(calendar.Config{URL: s.url, Username: caldavUser, Password: caldavPassword})
	if err != nil {
		t.Fatal(err)
	}
	if events := eventsNamed(t, c, s, uid, time.Date(2036, 1, 15, 0, 0, 0, 0, time.UTC)); len(events) != 0 {
		t.Errorf("a preview wrote %+v", events)
	}
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "calendar_import_from_email", Arguments: map[string]any{
		"message_id": found.Messages[0].ID, "calendar_id": s.calendar,
	}})
	if err == nil && !result.IsError {
		t.Error("the import went through in read-only mode")
	}
}
