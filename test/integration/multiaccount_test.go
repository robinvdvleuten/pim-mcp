//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/robinvdvleuten/pim-mcp/internal/account"
	"github.com/robinvdvleuten/pim-mcp/internal/calendar"
	"github.com/robinvdvleuten/pim-mcp/internal/mail"
	"github.com/robinvdvleuten/pim-mcp/internal/mcpserver"
)

// twoAccountSession serves two accounts, Radicale as the first and Baikal as the
// second, the way the binary does: each account's client comes from its own
// address, and nothing is shared between them.
func twoAccountSession(t *testing.T) (*mcp.ClientSession, [2]caldavServer, [2]string) {
	t.Helper()
	servers := [2]caldavServer{caldavServers[0], caldavServers[1]}
	emails := [2]string{"radicale@example.org", "baikal@example.org"}
	var accounts []account.Account
	byEmail := map[string]caldavServer{}
	for i, s := range servers {
		if s.setup != nil {
			s.setup(t, s)
		}
		accounts = append(accounts, account.Account{
			Email: emails[i], Username: caldavUser,
			IMAP:   account.IMAP{Addr: dovecotPlainAddr, Security: account.SecurityNone},
			CalDAV: account.CalDAV{URL: s.url},
		})
		byEmail[emails[i]] = s
	}

	server := mcpserver.New(mcpserver.Options{
		Version:  "test",
		Accounts: func() ([]account.Account, error) { return accounts, nil },
		DialMail: func(context.Context, string) (*mail.Client, error) {
			return nil, errors.New("mail is not part of this test")
		},
		DialCalendar: func(email string) (*calendar.Client, error) {
			s, ok := byEmail[email]
			if !ok {
				return nil, errors.New("no server for " + email)
			}
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
	return session, servers, emails
}

func multiCall(t *testing.T, session *mcp.ClientSession, name string, args map[string]any) (map[string]any, bool) {
	t.Helper()
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("call %s: %v", name, err)
	}
	data, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	_ = json.Unmarshal(data, &out)
	return out, result.IsError
}

func calendarIDs(t *testing.T, out map[string]any) []string {
	t.Helper()
	var ids []string
	calendars, _ := out["calendars"].([]any)
	for _, c := range calendars {
		ids = append(ids, c.(map[string]any)["id"].(string))
	}
	return ids
}

func TestCalendarListCalendarsPerAccount(t *testing.T) {
	session, servers, emails := twoAccountSession(t)

	for i := range servers {
		out, isError := multiCall(t, session, "calendar_list_calendars", map[string]any{"account": emails[i]})
		if isError {
			t.Fatalf("%s: tool error: %v", emails[i], out)
		}
		ids := calendarIDs(t, out)
		if !slices.Contains(ids, servers[i].calendar) {
			t.Errorf("%s: calendars %v, want %s", emails[i], ids, servers[i].calendar)
		}
		other := servers[1-i].calendar
		if slices.Contains(ids, other) {
			t.Errorf("%s: calendars %v include the other account's %s", emails[i], ids, other)
		}
	}

	// Leaving the argument out gives the first account's calendars.
	out, isError := multiCall(t, session, "calendar_list_calendars", nil)
	if isError || !slices.Contains(calendarIDs(t, out), servers[0].calendar) {
		t.Errorf("default account: %v", out)
	}

	if out, isError := multiCall(t, session, "calendar_list_calendars", map[string]any{"account": "nobody@example.org"}); !isError {
		t.Errorf("an unknown account was served: %v", out)
	}
}

func TestAccountInfoListsBothAccounts(t *testing.T) {
	session, _, emails := twoAccountSession(t)

	out, isError := multiCall(t, session, "account_info", map[string]any{"account": emails[1]})

	if isError || out["email"] != emails[1] {
		t.Fatalf("info = %v", out)
	}
	listed, _ := out["accounts"].([]any)
	if len(listed) != 2 || listed[0].(map[string]any)["email"] != emails[0] || listed[0].(map[string]any)["default"] != true {
		t.Errorf("accounts = %v", listed)
	}
}

// A calendar id from one server is refused when it is given with the other
// account, because it lies outside that account's calendar home.
func TestCalendarIDOfTheOtherAccountIsRefused(t *testing.T) {
	session, servers, emails := twoAccountSession(t)

	_, isError := multiCall(t, session, "calendar_create_event", map[string]any{
		"account": emails[1], "calendar_id": servers[0].calendar, "summary": "wrong account",
		"start": "2034-01-10T09:00:00Z", "end": "2034-01-10T10:00:00Z",
	})

	if !isError {
		t.Error("an id from the first account was accepted by the second")
	}
}

// The environment password is one password, so the binary won't give it to a
// second account.
func TestEnvironmentPasswordIsRefusedForASecondAccount(t *testing.T) {
	binary := buildBinary(t)
	home := t.TempDir()
	if out, err := runBinary(t, binary, home, caldavPassword, "account", "add",
		"--email", "first@example.org", "--imap", dovecotPlainAddr, "--imap-security", "none",
		"--caldav", caldavServers[0].url); err != nil {
		t.Fatalf("first add: %v\n%s", err, out)
	}

	out, err := runBinary(t, binary, home, caldavPassword, "account", "add",
		"--email", "second@example.org", "--imap", dovecotPlainAddr, "--imap-security", "none",
		"--caldav", caldavServers[1].url)

	if err == nil || !strings.Contains(out, "single account") {
		t.Errorf("err = %v, output = %q", err, out)
	}
	if shown, _ := runBinary(t, binary, home, caldavPassword, "account", "show"); strings.Contains(shown, "second@example.org") {
		t.Errorf("the refused account was saved: %s", shown)
	}
}
