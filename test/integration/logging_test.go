//go:build integration

package integration

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Debug logging is what someone turns on to chase a problem, and pastes into a bug
// report. It runs the real binary against real servers.
func TestDebugLogsHoldNoSecretsOrContent(t *testing.T) {
	const (
		body    = "the-secret-body-of-this-message"
		subject = "the-private-subject"
		summary = "the-private-event"
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

	id := firstMessageID(t, session)
	got := callTool[map[string]any](t, session, "mail_get", map[string]any{"id": id})
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
