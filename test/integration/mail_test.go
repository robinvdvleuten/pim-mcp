//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/robinvdvleuten/pim-mcp/internal/account"
	"github.com/robinvdvleuten/pim-mcp/internal/mail"
	"github.com/robinvdvleuten/pim-mcp/internal/mcpserver"
)

// mailSession is a real MCP client talking to the server's tools, which log in
// to Dovecot as user.
func mailSession(t *testing.T, user string) *mcp.ClientSession {
	t.Helper()
	server := mcpserver.New(mcpserver.Options{
		Version: "test",
		Account: func() (*account.Account, error) { return nil, account.ErrNoAccount },
		DialMail: func(ctx context.Context) (*mail.Client, error) {
			return mail.Dial(ctx, dovecotConfig(user))
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

func callTool[T any](t *testing.T, session *mcp.ClientSession, name string, args map[string]any) T {
	t.Helper()
	var out T
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("call %s: %v", name, err)
	}
	if result.IsError {
		t.Fatalf("%s: tool error: %s", name, toolText(result))
	}
	data, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("decode %s: %v", data, err)
	}
	return out
}

func toolText(result *mcp.CallToolResult) string {
	var text strings.Builder
	for _, content := range result.Content {
		if c, ok := content.(*mcp.TextContent); ok {
			text.WriteString(c.Text)
		}
	}
	return text.String()
}

// plainMail builds a message. Dates matter: search filters on them.
func plainMail(subject, from, to, date, body string) string {
	return fmt.Sprintf("From: %s\nTo: %s\nSubject: %s\nDate: %s\nMIME-Version: 1.0\nContent-Type: text/plain; charset=utf-8\n\n%s\n",
		from, to, subject, date, body)
}

func subjects(found mcpserver.SearchResult) []string {
	names := make([]string, len(found.Messages))
	for i, message := range found.Messages {
		names[i] = message.Subject
	}
	return names
}

func TestMailListFoldersReportsRoles(t *testing.T) {
	session := mailSession(t, newMailbox(t))

	got := callTool[mcpserver.FolderList](t, session, "mail_list_folders", nil)

	roles := map[string]string{}
	for _, folder := range got.Folders {
		roles[folder.Name] = folder.Role
	}
	want := map[string]string{"INBOX": "inbox", "Sent": "sent", "Drafts": "drafts", "Trash": "trash", "Junk": "junk"}
	for name, role := range want {
		if roles[name] != role {
			t.Errorf("role of %s = %q, want %q (all: %v)", name, roles[name], role, roles)
		}
	}
}

func TestMailSearchFilters(t *testing.T) {
	user := newMailbox(t)
	seed(t, user, plainMail("Budget 2027", "Jan <jan@example.org>", "me@example.org", "Mon, 02 Nov 2026 10:00:00 +0100", "numbers attached"))
	seed(t, user, plainMail("Lunch", "Piet <piet@example.org>", "other@example.org", "Wed, 04 Nov 2026 12:00:00 +0100", "see you at noon"))
	seed(t, user, plainMail("Budget review", "Piet <piet@example.org>", "me@example.org", "Fri, 06 Nov 2026 09:00:00 +0100", "bring the sheet"))
	seed(t, user, multipartInvite)
	seed(t, user, singlePartInvite)
	session := mailSession(t, user)

	tests := []struct {
		name string
		args map[string]any
		want []string
	}{
		{"nothing filtered, newest arrival first", map[string]any{}, []string{"Bare invite", "Project kickoff", "Budget review", "Lunch", "Budget 2027"}},
		{"subject", map[string]any{"subject": "budget"}, []string{"Budget review", "Budget 2027"}},
		{"from", map[string]any{"from": "jan@"}, []string{"Bare invite", "Project kickoff", "Budget 2027"}},
		{"to", map[string]any{"to": "other@example.org"}, []string{"Lunch"}},
		{"text in the body", map[string]any{"text": "noon"}, []string{"Lunch"}},
		{"filters combine", map[string]any{"from": "piet", "subject": "budget"}, []string{"Budget review"}},
		{"since, exact to the moment", map[string]any{"subject": "budget", "since": "2026-11-06T08:30:00+01:00"}, []string{"Budget review"}},
		{"since excludes earlier the same day", map[string]any{"subject": "budget", "since": "2026-11-06T09:30:00+01:00"}, []string{}},
		{"before, exact to the moment", map[string]any{"subject": "budget", "before": "2026-11-06T08:30:00+01:00"}, []string{"Budget 2027"}},
		{"before includes earlier the same day", map[string]any{"subject": "budget", "before": "2026-11-06T09:30:00+01:00"}, []string{"Budget review", "Budget 2027"}},
		{"since as a plain date", map[string]any{"subject": "budget", "since": "2026-11-05"}, []string{"Budget review"}},
		{"has a calendar part", map[string]any{"has_calendar": true}, []string{"Bare invite", "Project kickoff"}},
		{"has no calendar part", map[string]any{"has_calendar": false}, []string{"Budget review", "Lunch", "Budget 2027"}},
		{"no match", map[string]any{"subject": "nothing like this"}, []string{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := callTool[mcpserver.SearchResult](t, session, "mail_search", tt.args)

			if !slices.Equal(subjects(got), tt.want) {
				t.Errorf("subjects = %v, want %v", subjects(got), tt.want)
			}
			if got.More {
				t.Error("more = true, want false")
			}
		})
	}
}

func TestMailSearchSummary(t *testing.T) {
	user := newMailbox(t)
	seed(t, user, multipartInvite)
	session := mailSession(t, user)

	got := callTool[mcpserver.SearchResult](t, session, "mail_search", map[string]any{})

	if len(got.Messages) != 1 {
		t.Fatalf("messages = %+v", got.Messages)
	}
	message := got.Messages[0]
	if message.ID == "" {
		t.Error("no id")
	}
	if message.From != "Jan <jan@example.org>" {
		t.Errorf("from = %q", message.From)
	}
	if !message.HasCalendar {
		t.Error("has_calendar = false")
	}
	if message.Date.Format("2006-01-02T15:04:05Z07:00") != "2026-11-04T10:00:00+01:00" {
		t.Errorf("date = %s", message.Date)
	}
}

func TestMailSearchLimit(t *testing.T) {
	user := newMailbox(t)
	for i := range 5 {
		seed(t, user, plainMail(fmt.Sprintf("Message %d", i), "a@example.org", "b@example.org", "Mon, 02 Nov 2026 10:00:00 +0100", "x"))
	}
	session := mailSession(t, user)

	tests := []struct {
		name     string
		limit    int
		want     []string
		wantMore bool
	}{
		{"fewer than exist", 2, []string{"Message 4", "Message 3"}, true},
		{"exactly as many as exist", 5, []string{"Message 4", "Message 3", "Message 2", "Message 1", "Message 0"}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := callTool[mcpserver.SearchResult](t, session, "mail_search", map[string]any{"limit": tt.limit})

			if !slices.Equal(subjects(got), tt.want) {
				t.Errorf("subjects = %v, want %v", subjects(got), tt.want)
			}
			if got.More != tt.wantMore {
				t.Errorf("more = %v, want %v", got.More, tt.wantMore)
			}
		})
	}
}

func TestMailSearchLimitAppliesAfterClientSideFilters(t *testing.T) {
	// A later page of plain mail must not hide older invites.
	user := newMailbox(t)
	seed(t, user, multipartInvite)
	for i := range 120 {
		seed(t, user, plainMail(fmt.Sprintf("Noise %d", i), "a@example.org", "b@example.org", "Mon, 02 Nov 2026 10:00:00 +0100", "x"))
	}
	session := mailSession(t, user)

	got := callTool[mcpserver.SearchResult](t, session, "mail_search", map[string]any{"has_calendar": true, "limit": 1})

	if !slices.Equal(subjects(got), []string{"Project kickoff"}) {
		t.Errorf("subjects = %v", subjects(got))
	}
	if got.More {
		t.Error("more = true, but the whole folder was read")
	}
}

func TestMailSearchOtherFolder(t *testing.T) {
	user := newMailbox(t)
	seed(t, user, plainMail("In the inbox", "a@example.org", "b@example.org", "Mon, 02 Nov 2026 10:00:00 +0100", "x"))
	seedFolder(t, user, "Sent", plainMail("In sent", "a@example.org", "b@example.org", "Mon, 02 Nov 2026 10:00:00 +0100", "x"))
	session := mailSession(t, user)

	got := callTool[mcpserver.SearchResult](t, session, "mail_search", map[string]any{"folder": "Sent"})

	if !slices.Equal(subjects(got), []string{"In sent"}) {
		t.Errorf("subjects = %v", subjects(got))
	}
}

func TestMailSearchUnknownFolderIsAnError(t *testing.T) {
	session := mailSession(t, newMailbox(t))

	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "mail_search", Arguments: map[string]any{"folder": "Nope"}})
	if err != nil {
		t.Fatal(err)
	}

	if !result.IsError || !strings.Contains(toolText(result), "Nope") {
		t.Errorf("error = %v, text = %q", result.IsError, toolText(result))
	}
}

func TestMailSearchDecodesOldCharsets(t *testing.T) {
	// Windows-1252, as Go's standard library only handles UTF-8 and ISO-8859-1.
	user := newMailbox(t)
	seed(t, user, plainMail("=?windows-1252?Q?Caf=E9_morgen?=", "=?windows-1252?Q?Ren=E9?= <rene@example.org>", "b@example.org", "Mon, 02 Nov 2026 10:00:00 +0100", "x"))
	session := mailSession(t, user)

	got := callTool[mcpserver.SearchResult](t, session, "mail_search", map[string]any{})

	if len(got.Messages) != 1 {
		t.Fatalf("messages = %+v", got.Messages)
	}
	if got.Messages[0].Subject != "Café morgen" {
		t.Errorf("subject = %q", got.Messages[0].Subject)
	}
	if got.Messages[0].From != "René <rene@example.org>" {
		t.Errorf("from = %q", got.Messages[0].From)
	}
}

func TestMailSearchCannotInjectCommands(t *testing.T) {
	user := newMailbox(t)
	seed(t, user, plainMail("Keep me", "a@example.org", "b@example.org", "Mon, 02 Nov 2026 10:00:00 +0100", "x"))
	session := mailSession(t, user)

	// May match nothing or fail, but must not run the command in it.
	_, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "mail_search", Arguments: map[string]any{
		"subject": "x\" )\r\nA999 DELETE INBOX\r\nA998 SEARCH (SUBJECT \"",
	}})
	if err != nil {
		t.Fatal(err)
	}

	got := callTool[mcpserver.SearchResult](t, session, "mail_search", map[string]any{})
	if !slices.Equal(subjects(got), []string{"Keep me"}) {
		t.Errorf("subjects = %v, want the message untouched", subjects(got))
	}
}

func TestMailSearchLeavesMessagesUnseen(t *testing.T) {
	user := newMailbox(t)
	seed(t, user, multipartInvite)
	session := mailSession(t, user)

	callTool[mcpserver.SearchResult](t, session, "mail_search", map[string]any{"has_calendar": true})

	c, err := mail.Dial(t.Context(), dovecotConfig(user))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	found, err := c.Search(t.Context(), mail.Criteria{Folder: "INBOX", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	message, err := c.Message(t.Context(), found.Messages[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := flags(t, user, message.UID); slices.Contains(got, imap.FlagSeen) {
		t.Errorf("flags = %v", got)
	}
}

func TestParallelToolCallsDontInterfere(t *testing.T) {
	user := newMailbox(t)
	seed(t, user, plainMail("In the inbox", "a@example.org", "b@example.org", "Mon, 02 Nov 2026 10:00:00 +0100", "x"))
	seedFolder(t, user, "Sent", plainMail("In sent", "a@example.org", "b@example.org", "Mon, 02 Nov 2026 10:00:00 +0100", "x"))
	session := mailSession(t, user)

	errs := make(chan error, 20)
	for i := range 20 {
		folder, want := "INBOX", "In the inbox"
		if i%2 == 1 {
			folder, want = "Sent", "In sent"
		}
		go func() {
			result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "mail_search", Arguments: map[string]any{"folder": folder}})
			switch {
			case err != nil:
				errs <- err
			case result.IsError:
				errs <- errors.New(toolText(result))
			case !strings.Contains(toolText(result), want):
				errs <- fmt.Errorf("%s returned %s", folder, toolText(result))
			default:
				errs <- nil
			}
		}()
	}
	for range 20 {
		if err := <-errs; err != nil {
			t.Error(err)
		}
	}
}

func TestMessageIDStopsWorkingWhenFolderIsRecreated(t *testing.T) {
	user := newMailbox(t)
	const folder = "Project"
	raw, err := imapclient.DialInsecure(dovecotPlainAddr, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	if err := raw.Login(user, dovecotPassword).Wait(); err != nil {
		t.Fatal(err)
	}
	if err := raw.Create(folder, nil).Wait(); err != nil {
		t.Fatal(err)
	}
	seedFolder(t, user, folder, plainMail("Old message", "a@example.org", "b@example.org", "Mon, 02 Nov 2026 10:00:00 +0100", "x"))
	c, err := mail.Dial(t.Context(), dovecotConfig(user))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	found, err := c.Search(t.Context(), mail.Criteria{Folder: folder, Limit: 1})
	if err != nil || len(found.Messages) != 1 {
		t.Fatalf("search: %+v, %v", found, err)
	}
	id := found.Messages[0].ID
	if _, err := c.Message(t.Context(), id); err != nil {
		t.Fatalf("message before: %v", err)
	}

	// The same UID now means a different message.
	if err := raw.Delete(folder).Wait(); err != nil {
		t.Fatal(err)
	}
	if err := raw.Create(folder, nil).Wait(); err != nil {
		t.Fatal(err)
	}
	seedFolder(t, user, folder, plainMail("New message", "a@example.org", "b@example.org", "Mon, 02 Nov 2026 10:00:00 +0100", "x"))

	_, err = c.Message(t.Context(), id)

	if !errors.Is(err, mail.ErrStaleMessageID) {
		t.Errorf("err = %v, want ErrStaleMessageID", err)
	}
}

func TestMailSearchSaysSoWhenItStopsReading(t *testing.T) {
	// Only reading tells whether a message has a calendar part, and reading stops
	// at 2000 messages. The invite is older than those, so it can't be found, and
	// the result has to admit that.
	user := newMailbox(t)
	seed(t, user, multipartInvite)
	raw, err := imapclient.DialInsecure(dovecotPlainAddr, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	if err := raw.Login(user, dovecotPassword).Wait(); err != nil {
		t.Fatal(err)
	}
	message := strings.ReplaceAll(plainMail("Noise", "a@example.org", "b@example.org", "Mon, 02 Nov 2026 10:00:00 +0100", "x"), "\n", "\r\n")
	for range 2001 {
		cmd := raw.Append("INBOX", int64(len(message)), nil)
		if _, err := cmd.Write([]byte(message)); err != nil {
			t.Fatal(err)
		}
		if err := cmd.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := cmd.Wait(); err != nil {
			t.Fatal(err)
		}
	}
	session := mailSession(t, user)

	got := callTool[mcpserver.SearchResult](t, session, "mail_search", map[string]any{"has_calendar": true})

	if len(got.Messages) != 0 {
		t.Errorf("subjects = %v", subjects(got))
	}
	if !got.More {
		t.Error("more = false, but part of the folder wasn't read")
	}
}
