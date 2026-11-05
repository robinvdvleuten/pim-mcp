package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

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

func TestAccountInfoListsAsReadOnlyTool(t *testing.T) {
	session := connect(t, New(Options{Version: "abc123", Account: testAccount}))

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
			session := connect(t, New(Options{Version: "abc123", Account: testAccount, ReadOnly: readOnly}))

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
			}
			if info != want {
				t.Errorf("info = %+v, want %+v", info, want)
			}
		})
	}
}

func TestAccountInfoReadsTheAccountOnEachCall(t *testing.T) {
	// A user who adds the account while the server runs doesn't have to restart it.
	var configured bool
	session := connect(t, New(Options{Version: "abc123", Account: func() (*account.Account, error) {
		if !configured {
			return nil, account.ErrNoAccount
		}
		return testAccount()
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
			session := connect(t, New(Options{Version: "test", DialMail: func(context.Context) (*mail.Client, error) {
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
	session := connect(t, New(Options{Version: "test", DialMail: func(context.Context) (*mail.Client, error) {
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
			session := connect(t, New(Options{Version: "test", DialCalendar: func() (*calendar.Client, error) {
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
