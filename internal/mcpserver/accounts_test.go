package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/robinvdvleuten/pim-mcp/internal/account"
	"github.com/robinvdvleuten/pim-mcp/internal/calendar"
	"github.com/robinvdvleuten/pim-mcp/internal/mail"
)

const (
	personal = "me@example.org"
	work     = "me@work.example"
)

func twoAccounts() ([]account.Account, error) {
	first, _ := testAccount()
	second := *first
	second.Email = work
	second.IMAP.Addr = "mail.work.example:993"
	second.CalDAV.URL = "https://dav.work.example/"
	return []account.Account{*first, second}, nil
}

// dials records which account each dial was for.
type dials struct {
	mu       sync.Mutex
	mail     []string
	calendar []string
}

func (d *dials) options() Options {
	return Options{
		Version:  "test",
		Zone:     time.UTC,
		Accounts: twoAccounts,
		DialMail: func(_ context.Context, email string) (*mail.Client, error) {
			d.mu.Lock()
			defer d.mu.Unlock()
			d.mail = append(d.mail, email)
			return nil, errors.New("stop after dialing")
		},
		DialCalendar: func(email string) (*calendar.Client, error) {
			d.mu.Lock()
			defer d.mu.Unlock()
			d.calendar = append(d.calendar, email)
			return calendar.New(calendar.Config{URL: "https://dav.example.org/"})
		},
	}
}

func call(t *testing.T, session *mcp.ClientSession, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("call %s: %v", name, err)
	}
	return result
}

func TestEveryToolTakesAnOptionalAccount(t *testing.T) {
	session := connect(t, New(Options{Version: "test"}))

	result, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}

	var names []string
	for _, tool := range result.Tools {
		names = append(names, tool.Name)
		data, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatal(err)
		}
		var schema struct {
			Properties map[string]struct {
				Type        any    `json:"type"`
				Description string `json:"description"`
			} `json:"properties"`
			Required []string `json:"required"`
		}
		if err := json.Unmarshal(data, &schema); err != nil {
			t.Fatal(err)
		}
		property, ok := schema.Properties["account"]
		if !ok {
			t.Errorf("%s has no account property: %s", tool.Name, data)
			continue
		}
		if property.Type != "string" {
			t.Errorf("%s: account has type %v, want string", tool.Name, property.Type)
		}
		if !strings.Contains(property.Description, "first account") {
			t.Errorf("%s: account description = %q", tool.Name, property.Description)
		}
		if slices.Contains(schema.Required, "account") {
			t.Errorf("%s requires account", tool.Name)
		}
	}
	// A tool that failed to register would pass the loop by being absent.
	slices.Sort(names)
	known := KnownTools()
	slices.Sort(known)
	if !slices.Equal(names, known) {
		t.Errorf("listed %v, known %v", names, known)
	}
}

func TestAccountIsOptionalInCalls(t *testing.T) {
	// Tools that took no input before still accept no arguments at all.
	d := &dials{}
	session := connect(t, New(d.options()))

	for _, name := range []string{"account_info", "mail_list_folders", "calendar_list_calendars"} {
		result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name})
		if err != nil {
			t.Errorf("%s without arguments: %v", name, err)
		} else if name == "account_info" && result.IsError {
			t.Errorf("%s: %s", name, errorText(result))
		}
	}
}

func TestAccountSelection(t *testing.T) {
	tests := []struct {
		name      string
		account   any // nil leaves the argument out
		wantEmail string
		wantError string
	}{
		{name: "left out is the default", wantEmail: personal},
		{name: "empty is the default", account: "", wantEmail: personal},
		{name: "the first by name", account: personal, wantEmail: personal},
		{name: "another by name", account: work, wantEmail: work},
		{name: "in other letters", account: "ME@Work.example", wantEmail: work},
		{name: "unknown", account: "other@example.org", wantError: personal + ", " + work},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := &dials{}
			session := connect(t, New(d.options()))
			args := map[string]any{}
			if tt.account != nil {
				args["account"] = tt.account
			}

			var got []string
			for _, tool := range []string{"mail_list_folders", "calendar_list_calendars"} {
				result := call(t, session, tool, args)
				if tt.wantError != "" {
					if !result.IsError || !strings.Contains(errorText(result), tt.wantError) {
						t.Errorf("%s: error = %v, text = %q, want it to list %q", tool, result.IsError, errorText(result), tt.wantError)
					}
				}
			}
			got = append(got, d.mail...)
			got = append(got, d.calendar...)

			if tt.wantError != "" {
				if len(got) != 0 {
					t.Errorf("dialed %v for an unknown account", got)
				}
				return
			}
			if !slices.Equal(d.mail, []string{tt.wantEmail}) || !slices.Equal(d.calendar, []string{tt.wantEmail}) {
				t.Errorf("mail dials %v, calendar dials %v, want only %s", d.mail, d.calendar, tt.wantEmail)
			}
		})
	}
}

func TestEveryToolSelectsTheAccountItWasGiven(t *testing.T) {
	// All twelve go through the same resolution: none can reach another account.
	calls := map[string]map[string]any{
		"mail_search":                {},
		"mail_get":                   {"id": "x"},
		"calendar_list_events":       {"start": "2026-11-10", "end": "2026-11-11"},
		"calendar_free_busy":         {"start": "2026-11-10", "end": "2026-11-11"},
		"calendar_create_event":      {"calendar_id": "/c/", "summary": "x", "start": "2026-11-10T09:00:00Z", "end": "2026-11-10T10:00:00Z"},
		"calendar_update_event":      {"event_id": "/c/e.ics", "etag": "e", "scope": "series", "summary": "x"},
		"calendar_delete_event":      {"event_id": "/c/e.ics", "etag": "e", "scope": "series"},
		"calendar_preview_import":    {"message_id": "m", "calendar_id": "/c/"},
		"calendar_import_from_email": {"message_id": "m", "calendar_id": "/c/"},
	}
	for name, args := range calls {
		t.Run(name, func(t *testing.T) {
			d := &dials{}
			session := connect(t, New(d.options()))
			args["account"] = work

			call(t, session, name, args)

			if len(d.mail)+len(d.calendar) == 0 {
				t.Fatal("no dial at all")
			}
			for _, email := range append(slices.Clone(d.mail), d.calendar...) {
				if email != work {
					t.Errorf("dialed %s, want only %s", email, work)
				}
			}
			args["account"] = "nobody@example.org"
			before := len(d.mail) + len(d.calendar)
			if result := call(t, session, name, args); !result.IsError {
				t.Error("an unknown account was accepted")
			}
			if after := len(d.mail) + len(d.calendar); after != before {
				t.Error("an unknown account was dialed")
			}
		})
	}
}

func TestImportUsesOneAccountForMailAndCalendar(t *testing.T) {
	// The default moves between the two lookups. Both must still agree.
	var lookups int
	d := &dials{}
	options := d.options()
	options.Accounts = func() ([]account.Account, error) {
		accounts, _ := twoAccounts()
		lookups++
		if lookups%2 == 0 {
			accounts[0], accounts[1] = accounts[1], accounts[0]
		}
		return accounts, nil
	}
	session := connect(t, New(options))

	call(t, session, "calendar_preview_import", map[string]any{"message_id": "m", "calendar_id": "/c/"})

	if len(d.calendar) != 1 || len(d.mail) != 1 || d.calendar[0] != d.mail[0] {
		t.Errorf("calendar dials %v, mail dials %v, want the same account", d.calendar, d.mail)
	}
}

func TestAccountInfoNamesTheAccountsAndShowsTheOneAsked(t *testing.T) {
	session := connect(t, New(Options{Version: "test", Accounts: twoAccounts}))

	var info AccountInfo
	callTool(t, session, "account_info", &info)
	if info.Email != personal {
		t.Errorf("default info is for %s", info.Email)
	}
	want := []AccountEntry{{Email: personal, Default: true}, {Email: work}}
	if !slices.Equal(info.Accounts, want) {
		t.Errorf("accounts = %+v, want %+v", info.Accounts, want)
	}

	result := call(t, session, "account_info", map[string]any{"account": work})
	data, _ := json.Marshal(result.StructuredContent)
	var other AccountInfo
	if err := json.Unmarshal(data, &other); err != nil {
		t.Fatal(err)
	}
	if other.Email != work || other.IMAPAddress != "mail.work.example:993" || other.CalDAVURL != "https://dav.work.example/" {
		t.Errorf("info for work = %+v", other)
	}
	if !slices.Equal(other.Accounts, want) {
		t.Errorf("accounts for work = %+v, want the same list", other.Accounts)
	}

	if result := call(t, session, "account_info", map[string]any{"account": "x@example.org"}); !result.IsError {
		t.Error("unknown account accepted")
	}
}

func TestAccountsAreReadOnEachCall(t *testing.T) {
	// Adding a second account needs no restart.
	var accounts []account.Account
	d := &dials{}
	options := d.options()
	options.Accounts = func() ([]account.Account, error) {
		if len(accounts) == 0 {
			return nil, account.ErrNoAccount
		}
		return accounts, nil
	}
	session := connect(t, New(options))

	if result := call(t, session, "mail_list_folders", nil); !result.IsError || !strings.Contains(errorText(result), "account add") {
		t.Errorf("without accounts: %q", errorText(result))
	}
	all, _ := twoAccounts()
	accounts = all[:1]
	call(t, session, "mail_list_folders", map[string]any{"account": personal})
	if result := call(t, session, "mail_list_folders", map[string]any{"account": work}); !result.IsError {
		t.Error("the second account existed before it was added")
	}
	accounts = all
	call(t, session, "mail_list_folders", map[string]any{"account": work})

	if !slices.Equal(d.mail, []string{personal, work}) {
		t.Errorf("mail dials = %v", d.mail)
	}
}

func TestTheAccountIsNotLogged(t *testing.T) {
	var logs bytes.Buffer
	d := &dials{}
	options := d.options()
	options.Logger = slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	session := connect(t, New(options))

	call(t, session, "mail_list_folders", map[string]any{"account": work})
	call(t, session, "mail_list_folders", map[string]any{"account": "stranger@example.org"})

	if !strings.Contains(logs.String(), "mail_list_folders") {
		t.Fatalf("nothing was logged, so this proves nothing: %q", logs.String())
	}
	for _, secret := range []string{work, "stranger@example.org", "example.org"} {
		if strings.Contains(logs.String(), secret) {
			t.Errorf("the logs contain %q: %s", secret, logs.String())
		}
	}
}

// A calendar id from one account, given with another, is refused when it lies
// outside that account's calendar home, before anything is sent for it.
func TestCalendarIDOfAnotherAccountIsRefusedOutsideItsHome(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The lookup of the home is the only request an id outside it may not add to.
		if r.Method == "PROPFIND" && (r.URL.Path == "/" || r.URL.Path == "/work/") {
			w.WriteHeader(http.StatusMultiStatus)
			_, _ = w.Write([]byte(`<d:multistatus xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav"><d:response><d:propstat><d:prop>
<d:current-user-principal><d:href>/work/</d:href></d:current-user-principal>
<c:calendar-home-set><d:href>/work/</d:href></c:calendar-home-set>
</d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response></d:multistatus>`))
			return
		}
		paths = append(paths, r.Method+" "+r.URL.Path)
		w.WriteHeader(http.StatusCreated)
	}))
	t.Cleanup(server.Close)
	options := Options{Version: "test", Zone: time.UTC, Accounts: twoAccounts,
		DialMail: func(context.Context, string) (*mail.Client, error) { return nil, errors.New("no mail") },
		DialCalendar: func(string) (*calendar.Client, error) {
			return calendar.New(calendar.Config{URL: server.URL})
		}}
	session := connect(t, New(options))

	for name, args := range map[string]map[string]any{
		"calendar_create_event": {"calendar_id": "/personal/default/", "summary": "x", "start": "2026-11-10T09:00:00Z", "end": "2026-11-10T10:00:00Z"},
		"calendar_delete_event": {"event_id": "/personal/default/e.ics", "etag": "e", "scope": "series"},
		"calendar_update_event": {"event_id": "/personal/default/e.ics", "etag": "e", "scope": "series", "summary": "x"},
		"calendar_list_events":  {"start": "2026-11-10", "end": "2026-11-11", "calendar_ids": []string{"/personal/default/"}},
	} {
		args["account"] = work
		result := call(t, session, name, args)
		if !result.IsError || !strings.Contains(errorText(result), calendar.ErrOutsideCalendars.Error()) {
			t.Errorf("%s: error = %v, text = %q", name, result.IsError, errorText(result))
		}
	}
	if len(paths) != 0 {
		t.Errorf("requests went out for ids outside the home: %v", paths)
	}
}
