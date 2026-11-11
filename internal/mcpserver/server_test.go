package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/robinvdvleuten/pim-mcp/internal/account"
	"github.com/robinvdvleuten/pim-mcp/internal/calendar"
	"github.com/robinvdvleuten/pim-mcp/internal/mail"
)

func connect(t *testing.T, server *mcp.Server) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()

	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("connect server: %v", err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "test"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("connect client: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func TestNewIdentifiesServerDuringInitialize(t *testing.T) {
	session := connect(t, New(Options{Version: "abc123"}))

	info := session.InitializeResult().ServerInfo

	if info.Name != "pim-mcp" {
		t.Errorf("server name = %q, want %q", info.Name, "pim-mcp")
	}
	if info.Version != "abc123" {
		t.Errorf("server version = %q, want %q", info.Version, "abc123")
	}
}

func callTool[T any](t *testing.T, session *mcp.ClientSession, name string, out *T) *mcp.CallToolResult {
	t.Helper()
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name})
	if err != nil {
		t.Fatalf("call %s: %v", name, err)
	}
	if out != nil && !result.IsError {
		data, err := json.Marshal(result.StructuredContent)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(data, out); err != nil {
			t.Fatalf("decode %s: %v", data, err)
		}
	}
	return result
}

func errorText(result *mcp.CallToolResult) string {
	var text strings.Builder
	for _, content := range result.Content {
		if c, ok := content.(*mcp.TextContent); ok {
			text.WriteString(c.Text)
		}
	}
	return text.String()
}

func testAccount() (*account.Account, error) {
	return &account.Account{
		Email:    "me@example.org",
		Username: "me",
		IMAP:     account.IMAP{Addr: "mail.example.org:993", Security: account.SecurityTLS},
		CalDAV:   account.CalDAV{URL: "https://dav.example.org/"},
	}, nil
}

func testAccounts() ([]account.Account, error) {
	a, err := testAccount()
	return []account.Account{*a}, err
}

func TestAccountInfoListsAsReadOnlyTool(t *testing.T) {
	session := connect(t, New(Options{Version: "abc123", Accounts: testAccounts}))

	result, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}

	i := slices.IndexFunc(result.Tools, func(tool *mcp.Tool) bool { return tool.Name == "account_info" })
	if i < 0 {
		t.Fatalf("tools = %v, want account_info", result.Tools)
	}
	if tool := result.Tools[i]; tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
		t.Errorf("annotations = %+v, want a read-only hint", tool.Annotations)
	}
}

func TestAccountInfoReturnsTheAccount(t *testing.T) {
	tests := map[string]bool{"read-write": false, "read-only": true}

	for name, readOnly := range tests {
		t.Run(name, func(t *testing.T) {
			zone, err := time.LoadLocation("Pacific/Auckland")
			if err != nil {
				t.Fatal(err)
			}
			session := connect(t, New(Options{Version: "abc123", Accounts: testAccounts, ReadOnly: readOnly, Zone: zone}))

			var info AccountInfo
			result := callTool(t, session, "account_info", &info)

			if result.IsError {
				t.Fatalf("tool error: %s", errorText(result))
			}
			want := AccountInfo{
				Email:        "me@example.org",
				Username:     "me",
				IMAPAddress:  "mail.example.org:993",
				IMAPSecurity: account.SecurityTLS,
				CalDAVURL:    "https://dav.example.org/",
				ReadOnly:     readOnly,
				Timezone:     "Pacific/Auckland",
				Accounts:     []AccountEntry{{Email: "me@example.org", Default: true}},
			}
			if !reflect.DeepEqual(info, want) {
				t.Errorf("info = %+v, want %+v", info, want)
			}
		})
	}
}

func TestAccountInfoReadsTheAccountOnEachCall(t *testing.T) {
	// A user who adds the account while the server runs doesn't have to restart it.
	var configured bool
	session := connect(t, New(Options{Version: "abc123", Accounts: func() ([]account.Account, error) {
		if !configured {
			return nil, account.ErrNoAccount
		}
		return testAccounts()
	}}))

	first := callTool[AccountInfo](t, session, "account_info", nil)
	if !first.IsError || !strings.Contains(errorText(first), "account add") {
		t.Errorf("before adding: error = %v, text = %q", first.IsError, errorText(first))
	}

	configured = true
	if second := callTool[AccountInfo](t, session, "account_info", nil); second.IsError {
		t.Errorf("after adding: %s", errorText(second))
	}
}

func TestMailSearchRefusesBadInputWithoutConnecting(t *testing.T) {
	tests := map[string]map[string]any{
		"limit too high":    {"limit": 51},
		"negative limit":    {"limit": -1},
		"bad since":         {"since": "tomorrow"},
		"bad before":        {"before": "2026-13-45"},
		"time without zone": {"since": "2026-11-05T09:00:00"},
	}

	for name, args := range tests {
		t.Run(name, func(t *testing.T) {
			var dials int
			session := connect(t, New(Options{Version: "test", DialMail: func(context.Context, string) (*mail.Client, error) {
				dials++
				return nil, errors.New("must not connect")
			}}))

			result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "mail_search", Arguments: args})

			if err == nil && !result.IsError {
				t.Error("call succeeded")
			}
			if dials != 0 {
				t.Errorf("connected %d times", dials)
			}
		})
	}
}

func TestMailToolsReportConnectionFailure(t *testing.T) {
	session := connect(t, New(Options{Version: "test", DialMail: func(context.Context, string) (*mail.Client, error) {
		return nil, errors.New("no account configured")
	}}))

	for _, name := range []string{"mail_list_folders", "mail_search"} {
		t.Run(name, func(t *testing.T) {
			result := callTool[any](t, session, name, nil)

			if !result.IsError || !strings.Contains(errorText(result), "no account configured") {
				t.Errorf("error = %v, text = %q", result.IsError, errorText(result))
			}
		})
	}
}

func TestCalendarListEventsRefusesBadInputWithoutConnecting(t *testing.T) {
	tests := map[string]map[string]any{
		"bad start":      {"start": "soon", "end": "2026-11-10"},
		"bad end":        {"start": "2026-11-10", "end": "later"},
		"no zone":        {"start": "2026-11-10T09:00:00", "end": "2026-11-11"},
		"missing end":    {"start": "2026-11-10"},
		"end first":      {"start": "2026-11-11", "end": "2026-11-10"},
		"range too long": {"start": "2026-01-01", "end": "2027-06-01"},
	}

	for name, args := range tests {
		t.Run(name, func(t *testing.T) {
			var requests int
			server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests++ }))
			t.Cleanup(server.Close)
			session := connect(t, New(Options{Version: "test", DialCalendar: func(string) (*calendar.Client, error) {
				return calendar.New(calendar.Config{URL: server.URL})
			}}))

			result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "calendar_list_events", Arguments: args})

			if err == nil && !result.IsError {
				t.Error("call succeeded")
			}
			if requests != 0 {
				t.Errorf("sent %d requests", requests)
			}
		})
	}
}

func toolNames(t *testing.T, options Options) []string {
	t.Helper()
	session := connect(t, New(options))
	result, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	names := []string{}
	for _, tool := range result.Tools {
		names = append(names, tool.Name)
	}
	slices.Sort(names)
	return names
}

func TestReadOnlyModeHidesEveryWriteTool(t *testing.T) {
	session := connect(t, New(Options{Version: "test", ReadOnly: true}))

	result, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}

	for _, tool := range result.Tools {
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
			t.Errorf("%s is available in read-only mode", tool.Name)
		}
	}
	if len(result.Tools) == 0 {
		t.Error("no tools at all")
	}
}

func TestReadOnlyModeRefusesAWriteToolCalledAnyway(t *testing.T) {
	session := connect(t, New(Options{Version: "test", ReadOnly: true}))

	_, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "calendar_create_event", Arguments: map[string]any{
		"calendar_id": "/cal/", "summary": "x", "start": "2026-11-10T09:00:00Z", "end": "2026-11-10T10:00:00Z",
	}})

	if err == nil {
		t.Error("the call reached a tool")
	}
}

func TestWriteToolsExistWithoutReadOnlyMode(t *testing.T) {
	if names := toolNames(t, Options{Version: "test"}); !slices.Contains(names, "calendar_create_event") {
		t.Errorf("tools = %v", names)
	}
}

func TestToolsOptionNarrowsTheSet(t *testing.T) {
	got := toolNames(t, Options{Version: "test", Tools: []string{"mail_search", "calendar_create_event"}})
	if !slices.Equal(got, []string{"calendar_create_event", "mail_search"}) {
		t.Errorf("tools = %v", got)
	}

	got = toolNames(t, Options{Version: "test", ReadOnly: true, Tools: []string{"mail_search", "calendar_create_event"}})
	if !slices.Equal(got, []string{"mail_search"}) {
		t.Errorf("read-only tools = %v, read-only must win", got)
	}
}

func TestKnownTools(t *testing.T) {
	known := KnownTools()

	for _, want := range []string{"account_info", "mail_get", "calendar_list_events", "calendar_create_event"} {
		if !slices.Contains(known, want) {
			t.Errorf("known = %v, want %s", known, want)
		}
	}
}

func TestCreateEventInputs(t *testing.T) {
	timed := map[string]any{"calendar_id": "/cal/", "summary": "x", "start": "2026-11-10T09:00:00Z", "end": "2026-11-10T10:00:00Z"}
	with := func(changes map[string]any) map[string]any {
		out := map[string]any{}
		for k, v := range timed {
			out[k] = v
		}
		for k, v := range changes {
			if v == nil {
				delete(out, k)
			} else {
				out[k] = v
			}
		}
		return out
	}
	tests := []struct {
		name   string
		args   map[string]any
		start  string
		allDay bool
		bad    bool
		inBody []string // what the stored calendar object must hold
	}{
		{name: "RFC 3339", args: timed, start: "2026-11-10T09:00:00Z"},
		{name: "local time and a zone", args: with(map[string]any{"start": "2026-11-10T09:00:00", "end": "2026-11-10T10:00:00", "timezone": "Europe/Amsterdam"}), start: "2026-11-10T08:00:00Z"},
		{name: "local time without seconds", args: with(map[string]any{"start": "2026-11-10T09:00", "end": "2026-11-10T10:00", "timezone": "Europe/Amsterdam"}), start: "2026-11-10T08:00:00Z"},
		{name: "one all-day date", args: with(map[string]any{"start": nil, "end": nil, "date": "2026-11-10"}), start: "2026-11-10T00:00:00Z", allDay: true},
		{name: "several days", args: with(map[string]any{"start": nil, "end": nil, "date": "2026-11-10", "end_date": "2026-11-12"}), start: "2026-11-10T00:00:00Z", allDay: true},
		{name: "local time without a zone", args: with(map[string]any{"start": "2026-11-10T09:00:00"}), bad: true},
		{name: "unknown zone", args: with(map[string]any{"start": "2026-11-10T09:00:00", "end": "2026-11-10T10:00:00", "timezone": "Mars/Olympus"}), bad: true},
		{name: "no end", args: with(map[string]any{"end": nil}), bad: true},
		{name: "date and times", args: with(map[string]any{"date": "2026-11-10"}), bad: true},
		{name: "neither", args: with(map[string]any{"start": nil, "end": nil}), bad: true},
		{name: "bad date", args: with(map[string]any{"start": nil, "end": nil, "date": "soon"}), bad: true},
		{name: "last day before the first", args: with(map[string]any{"start": nil, "end": nil, "date": "2026-11-10", "end_date": "2026-11-09"}), bad: true},
		{name: "weekly in a zone", args: with(map[string]any{"start": "2026-11-10T09:00", "end": "2026-11-10T10:00", "timezone": "Europe/Amsterdam", "recurrence": "FREQ=WEEKLY;BYDAY=TU;COUNT=4"}),
			start: "2026-11-10T08:00:00Z", inBody: []string{"DTSTART;TZID=Europe/Amsterdam:20261110T090000", "BEGIN:VTIMEZONE", "RRULE:FREQ=WEEKLY;COUNT=4;BYDAY=TU"}},
		{name: "RFC 3339 is converted into the zone", args: with(map[string]any{"start": "2026-11-10T08:00:00Z", "end": "2026-11-10T09:00:00Z", "timezone": "Europe/Amsterdam", "recurrence": "FREQ=WEEKLY;BYDAY=TU"}),
			start: "2026-11-10T08:00:00Z", inBody: []string{"DTSTART;TZID=Europe/Amsterdam:20261110T090000", "DTEND;TZID=Europe/Amsterdam:20261110T100000"}},
		{name: "all-day weekly", args: with(map[string]any{"start": nil, "end": nil, "date": "2026-11-10", "recurrence": "FREQ=WEEKLY;BYDAY=TU;UNTIL=20261231"}),
			start: "2026-11-10T00:00:00Z", allDay: true, inBody: []string{"DTSTART;VALUE=DATE:20261110", "RRULE:FREQ=WEEKLY;BYDAY=TU;UNTIL=20261231\r\n"}},
		{name: "recurring without a zone", args: with(map[string]any{"recurrence": "FREQ=WEEKLY;BYDAY=TU"}), bad: true},
		{name: "recurring with a bad zone", args: with(map[string]any{"timezone": "Mars/Olympus", "recurrence": "FREQ=WEEKLY;BYDAY=TU"}), bad: true},
		{name: "recurrence that starts on another day", args: with(map[string]any{"timezone": "Europe/Amsterdam", "recurrence": "FREQ=WEEKLY;BYDAY=MO"}), bad: true},
		{name: "hourly", args: with(map[string]any{"timezone": "Europe/Amsterdam", "recurrence": "FREQ=HOURLY"}), bad: true},
		{name: "a property smuggled in", args: with(map[string]any{"timezone": "Europe/Amsterdam", "recurrence": "FREQ=WEEKLY;BYDAY=TU\r\nATTENDEE:mailto:x@example.org"}), bad: true},
		{name: "end date without a date", args: with(map[string]any{"end_date": "2026-11-12"}), bad: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var body string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// A write is only sent once the calendar home is known.
				if r.Method == "PROPFIND" {
					w.WriteHeader(http.StatusMultiStatus)
					_, _ = w.Write([]byte(`<d:multistatus xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav"><d:response><d:propstat><d:prop>
<d:current-user-principal><d:href>/</d:href></d:current-user-principal>
<c:calendar-home-set><d:href>/</d:href></c:calendar-home-set>
</d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response></d:multistatus>`))
					return
				}
				data, _ := io.ReadAll(r.Body)
				body = string(data)
				w.Header().Set("ETag", `"e"`)
				w.WriteHeader(http.StatusCreated)
			}))
			t.Cleanup(server.Close)
			session := connect(t, New(Options{Version: "test", Zone: time.UTC, DialCalendar: func(string) (*calendar.Client, error) {
				return calendar.New(calendar.Config{URL: server.URL})
			}}))

			var got calendar.Event
			result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "calendar_create_event", Arguments: tt.args})
			if err != nil {
				if !tt.bad {
					t.Fatalf("call: %v", err)
				}
				return
			}
			if tt.bad {
				if !result.IsError {
					t.Errorf("call succeeded: %s", errorText(result))
				}
				if body != "" {
					t.Errorf("wrote %q", body)
				}
				return
			}
			if result.IsError {
				t.Fatalf("tool error: %s", errorText(result))
			}
			data, err := json.Marshal(result.StructuredContent)
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(data, &got); err != nil {
				t.Fatal(err)
			}
			if got.Start.UTC().Format(time.RFC3339) != tt.start || got.AllDay != tt.allDay {
				t.Errorf("event = %+v, want start %s", got, tt.start)
			}
			for _, want := range tt.inBody {
				if !strings.Contains(body, want) {
					t.Errorf("stored %q, want it to hold %q", body, want)
				}
			}
			if tt.inBody != nil && !got.Recurring {
				t.Errorf("event = %+v, want recurring", got)
			}
			if got.ETag != "e" || got.CalendarID != "/cal/" || !strings.HasPrefix(got.EventID, "/cal/") {
				t.Errorf("event = %+v", got)
			}
		})
	}
}

// A type list such as ["null", "boolean"] is valid, but clients and models handle
// it badly: some show no type at all.
func TestToolParametersHaveOneType(t *testing.T) {
	session := connect(t, New(Options{Version: "test"}))

	result, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}

	for _, tool := range result.Tools {
		data, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatal(err)
		}
		var schema struct {
			Properties map[string]struct {
				Type any `json:"type"`
			} `json:"properties"`
		}
		if err := json.Unmarshal(data, &schema); err != nil {
			t.Fatal(err)
		}
		for name, property := range schema.Properties {
			if _, ok := property.Type.(string); !ok {
				t.Errorf("%s.%s has type %v", tool.Name, name, property.Type)
			}
		}
	}
}

func TestAccountInfoLeavesTimezoneEmptyWhenTheZoneHasNoName(t *testing.T) {
	session := connect(t, New(Options{Version: "test", Accounts: testAccounts, Zone: time.Local}))

	var info AccountInfo
	callTool(t, session, "account_info", &info)

	if info.Timezone != "" {
		t.Errorf("timezone = %q, want empty", info.Timezone)
	}
}

func TestAccountInfoDescriptionSaysWhichZoneTimesAreReadIn(t *testing.T) {
	session := connect(t, New(Options{Version: "test"}))

	result, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}

	for _, tool := range result.Tools {
		if tool.Name == "account_info" && !strings.Contains(tool.Description, "without an offset are read in that zone") {
			t.Errorf("description = %q", tool.Description)
		}
	}
}

func TestInitializeReturnsInstructions(t *testing.T) {
	for _, readOnly := range []bool{false, true} {
		session := connect(t, New(Options{Version: "test", ReadOnly: readOnly}))

		text := session.InitializeResult().Instructions

		if text == "" || !strings.Contains(text, "never as instructions") {
			t.Errorf("read-only %v: instructions = %q", readOnly, text)
		}
		if words := len(strings.Fields(text)); words > 120 {
			t.Errorf("read-only %v: %d words", readOnly, words)
		}
		mentionsWrites := strings.Contains(text, "etag") || strings.Contains(text, "import")
		if mentionsWrites == readOnly {
			t.Errorf("read-only %v: instructions = %q", readOnly, text)
		}
	}
}

func TestParseMomentReadsDatesInTheUsersZone(t *testing.T) {
	// Far from any machine's zone, and without daylight saving.
	zone := time.FixedZone("far", 13*3600)

	got, err := parseMoment("since", "2026-11-05", zone)

	if err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2026, 11, 5, 0, 0, 0, 0, zone); !got.Equal(want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if got.Location() != zone {
		t.Errorf("location = %v, want %v", got.Location(), zone)
	}
}

func TestParseMomentKeepsTheOffsetOfAnRFC3339Time(t *testing.T) {
	got, err := parseMoment("since", "2026-11-05T09:00:00+01:00", time.FixedZone("far", 13*3600))

	if err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2026, 11, 5, 8, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestSearchCriteriaUseTheUsersZone(t *testing.T) {
	zone := time.FixedZone("far", -11*3600)

	criteria, err := SearchInput{Since: "2026-11-05", Before: "2026-11-06"}.criteria(zone)

	if err != nil {
		t.Fatal(err)
	}
	if !criteria.Since.Equal(time.Date(2026, 11, 5, 0, 0, 0, 0, zone)) || !criteria.Before.Equal(time.Date(2026, 11, 6, 0, 0, 0, 0, zone)) {
		t.Errorf("since %v, before %v", criteria.Since, criteria.Before)
	}
}

func TestServerWithoutAZoneDoesNotPanic(t *testing.T) {
	s := newServer(Options{})

	if s.zone() == nil {
		t.Error("no zone")
	}
}

func listedOccurrence(eventID, description string) calendar.Event {
	event := calendar.Event{EventID: eventID}
	event.Description = description
	event.Summary = "Standup"
	return event
}

func TestListedEvents(t *testing.T) {
	boilerplate := strings.Repeat("Join the meeting. ", 100)
	list := calendar.EventList{
		Events: []calendar.Event{
			listedOccurrence("/c/series.ics", boilerplate),
			listedOccurrence("/c/series.ics", boilerplate),
			listedOccurrence("/c/series.ics", "Moved: bring slides"),
			listedOccurrence("/c/series.ics", boilerplate),
			listedOccurrence("/c/other.ics", boilerplate),
			listedOccurrence("/c/single.ics", "Once"),
			listedOccurrence("/c/empty.ics", ""),
			listedOccurrence("/c/empty.ics", ""),
		},
		Skipped: []calendar.SkippedEvent{{EventID: "/c/bad.ics", Reason: "unknown zone"}},
	}

	got := listedEvents(list)

	type view struct {
		description string
		repeated    bool
	}
	want := []view{
		{boilerplate, false},
		{"", true},
		{"Moved: bring slides", false},
		{"", true},
		{boilerplate, false}, // another event with the same text
		{"Once", false},
		{"", false},
		{"", false},
	}
	if len(got.Events) != len(want) {
		t.Fatalf("got %d events, want %d", len(got.Events), len(want))
	}
	for i, w := range want {
		if got.Events[i].Description != w.description || got.Events[i].DescriptionRepeated != w.repeated {
			t.Errorf("event %d: description %.20q, repeated %v; want %.20q, %v", i, got.Events[i].Description, got.Events[i].DescriptionRepeated, w.description, w.repeated)
		}
	}
	if len(got.Skipped) != 1 {
		t.Errorf("skipped = %v", got.Skipped)
	}
	if list.Events[1].Description != boilerplate {
		t.Error("the input was changed")
	}
}

func TestListedEventsJSONShape(t *testing.T) {
	got := listedEvents(calendar.EventList{Events: []calendar.Event{
		listedOccurrence("/c/a.ics", "text"), listedOccurrence("/c/a.ics", "text"),
	}})

	data, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Events  []map[string]any `json:"events"`
		Skipped []any            `json:"skipped"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	plain, err := json.Marshal(calendar.EventList{Events: []calendar.Event{listedOccurrence("/c/a.ics", "text")}})
	if err != nil {
		t.Fatal(err)
	}
	var original struct {
		Events []map[string]any `json:"events"`
	}
	if err := json.Unmarshal(plain, &original); err != nil {
		t.Fatal(err)
	}
	if _, ok := decoded.Events[0]["description_repeated"]; ok {
		t.Error("the first occurrence has description_repeated")
	}
	if decoded.Events[1]["description_repeated"] != true || decoded.Events[1]["description"] != "" {
		t.Errorf("repeat = %v", decoded.Events[1])
	}
	for key := range original.Events[0] {
		if _, ok := decoded.Events[0][key]; !ok {
			t.Errorf("field %q is gone", key)
		}
	}
	if len(decoded.Events[0]) != len(original.Events[0]) {
		t.Errorf("fields differ: %v vs %v", decoded.Events[0], original.Events[0])
	}
}
