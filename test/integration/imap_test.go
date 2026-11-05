//go:build integration

package integration

import (
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"

	"github.com/robinvdvleuten/pim-mcp/internal/mail"
)

const (
	dovecotPlainAddr = "127.0.0.1:31143"
	dovecotTLSAddr   = "127.0.0.1:31993"
	dovecotPassword  = "test-password"
)

const icsBody = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nMETHOD:REQUEST\r\nEND:VCALENDAR\r\n"

// icsBodyBase64 is icsBody with the line wrapping mail clients produce.
const icsBodyBase64 = "QkVHSU46VkNBTEVOREFSDQpWRVJTSU9OOjIuMA0KTUVUSE9EOlJFUVVF\r\n" +
	"U1QNCkVORDpWQ0FMRU5EQVINCg==\r\n"

func dovecotConfig(user string) mail.Config {
	return mail.Config{
		Addr:     dovecotPlainAddr,
		Security: mail.SecurityNone,
		Username: user,
		Password: dovecotPassword,
	}
}

// newMailbox returns a user of its own, so tests share no messages.
func newMailbox(t *testing.T) string {
	t.Helper()
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return "test-" + hex.EncodeToString(b)
}

func seed(t *testing.T, user, message string) {
	t.Helper()
	seedFolder(t, user, "INBOX", message)
}

func seedFolder(t *testing.T, user, folder, message string) {
	t.Helper()
	c, err := imapclient.DialInsecure(dovecotPlainAddr, nil)
	if err != nil {
		t.Fatalf("seed dial: %v", err)
	}
	defer func() { _ = c.Close() }()
	if err := c.Login(user, dovecotPassword).Wait(); err != nil {
		t.Fatalf("seed login: %v", err)
	}
	message = strings.ReplaceAll(message, "\n", "\r\n")
	cmd := c.Append(folder, int64(len(message)), nil)
	if _, err := cmd.Write([]byte(message)); err != nil {
		t.Fatalf("seed write: %v", err)
	}
	if err := cmd.Close(); err != nil {
		t.Fatalf("seed close: %v", err)
	}
	if _, err := cmd.Wait(); err != nil {
		t.Fatalf("seed append: %v", err)
	}
}

func flags(t *testing.T, user string, uid uint32) []imap.Flag {
	t.Helper()
	c, err := imapclient.DialInsecure(dovecotPlainAddr, nil)
	if err != nil {
		t.Fatalf("flags dial: %v", err)
	}
	defer func() { _ = c.Close() }()
	if err := c.Login(user, dovecotPassword).Wait(); err != nil {
		t.Fatalf("flags login: %v", err)
	}
	if _, err := c.Select("INBOX", &imap.SelectOptions{ReadOnly: true}).Wait(); err != nil {
		t.Fatalf("flags select: %v", err)
	}
	msgs, err := c.Fetch(imap.UIDSetNum(imap.UID(uid)), &imap.FetchOptions{Flags: true}).Collect()
	if err != nil || len(msgs) != 1 {
		t.Fatalf("flags fetch: %d messages, err %v", len(msgs), err)
	}
	return msgs[0].Flags
}

const multipartInvite = `From: Jan <jan@example.org>
To: me@example.org
Subject: Project kickoff
Date: Wed, 04 Nov 2026 10:00:00 +0100
MIME-Version: 1.0
Content-Type: multipart/mixed; boundary="b1"

--b1
Content-Type: text/plain; charset=utf-8

See you there.
--b1
Content-Type: text/calendar; charset=utf-8; method=REQUEST
Content-Transfer-Encoding: base64

` + icsBodyBase64 + `--b1--
`

const singlePartInvite = `From: Jan <jan@example.org>
To: me@example.org
Subject: Bare invite
Date: Wed, 04 Nov 2026 10:00:00 +0100
MIME-Version: 1.0
Content-Type: text/calendar; charset=utf-8; method=REQUEST
Content-Transfer-Encoding: quoted-printable

BEGIN:VCALENDAR
VERSION:2.=30
METHOD:REQUEST
END:VCALENDAR
`

func TestIMAPReadsCalendarPart(t *testing.T) {
	tests := []struct {
		name     string
		message  string
		subject  string
		wantPath string
	}{
		{"nested in multipart, base64", multipartInvite, "kickoff", "2"},
		{"whole message, quoted-printable", singlePartInvite, "Bare", "1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			user := newMailbox(t)
			seed(t, user, tt.message)
			c, err := mail.Dial(t.Context(), dovecotConfig(user))
			if err != nil {
				t.Fatalf("dial: %v", err)
			}
			t.Cleanup(func() { _ = c.Close() })
			found, err := c.Search(t.Context(), mail.Criteria{Folder: "INBOX", Subject: tt.subject, Limit: 10})
			if err != nil || len(found.Messages) != 1 {
				t.Fatalf("search: %+v, err %v", found, err)
			}
			id := found.Messages[0].ID

			msg, err := c.Message(t.Context(), id)
			if err != nil {
				t.Fatalf("message: %v", err)
			}
			i := slices.IndexFunc(msg.Parts, func(p mail.Part) bool { return p.MediaType == "text/calendar" })
			if i < 0 {
				t.Fatalf("parts = %+v, want a text/calendar part", msg.Parts)
			}
			part := msg.Parts[i]
			if part.Path != tt.wantPath {
				t.Errorf("path = %q, want %q", part.Path, tt.wantPath)
			}

			content, err := c.PartContent(t.Context(), id, part)
			if err != nil {
				t.Fatalf("part content: %v", err)
			}
			if got := string(content); got != icsBody {
				t.Errorf("content = %q, want %q", got, icsBody)
			}

			if got := flags(t, user, msg.UID); slices.Contains(got, imap.FlagSeen) {
				t.Errorf("flags = %v: reading must not mark the message as seen", got)
			}
		})
	}
}

func TestIMAPRejectsWrongPassword(t *testing.T) {
	cfg := dovecotConfig(newMailbox(t))
	cfg.Password = "wrong"

	_, err := mail.Dial(t.Context(), cfg)

	if err == nil {
		t.Fatal("dial succeeded with a wrong password")
	}
	if strings.Contains(err.Error(), "wrong") {
		t.Errorf("err = %q must not contain the password", err)
	}
}

func TestIMAPVerifiesServerCertificate(t *testing.T) {
	tests := []struct {
		name string
		addr string
		sec  mail.Security
	}{
		{"implicit TLS", dovecotTLSAddr, mail.SecurityTLS},
		{"STARTTLS", dovecotPlainAddr, mail.SecurityStartTLS},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := dovecotConfig(newMailbox(t))
			cfg.Addr = tt.addr
			cfg.Security = tt.sec

			_, err := mail.Dial(t.Context(), cfg)

			var verification *tls.CertificateVerificationError
			if !errors.As(err, &verification) {
				t.Errorf("err = %v, want a certificate verification error for Dovecot's self-signed certificate", err)
			}
		})
	}
}

// TestIMAPLiveAccount checks a real server over implicit TLS, read-only. It
// only runs when PIM_MCP_LIVE_IMAP_ADDR is set.
func TestIMAPLiveAccount(t *testing.T) {
	addr := os.Getenv("PIM_MCP_LIVE_IMAP_ADDR")
	if addr == "" {
		t.Skip("PIM_MCP_LIVE_IMAP_ADDR is not set")
	}
	c, err := mail.Dial(t.Context(), mail.Config{
		Addr:     addr,
		Security: mail.SecurityTLS,
		Username: os.Getenv("PIM_MCP_LIVE_IMAP_USER"),
		Password: os.Getenv("PIM_MCP_LIVE_IMAP_PASSWORD"),
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	folders, err := c.Folders(t.Context())
	if err != nil {
		t.Fatalf("folders: %v", err)
	}
	if !slices.ContainsFunc(folders, func(f mail.Folder) bool { return f.Role == mail.RoleInbox }) {
		t.Errorf("found %d folders, none is the inbox", len(folders))
	}

	found, err := c.Search(t.Context(), mail.Criteria{Folder: "INBOX", Limit: 1})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(found.Messages) == 0 {
		t.Skip("no message to read")
	}
	msg, err := c.Message(t.Context(), found.Messages[0].ID)
	if err != nil {
		t.Fatalf("message: %v", err)
	}
	if len(msg.Parts) == 0 {
		t.Fatal("message has no parts")
	}
	if _, err := c.PartContent(t.Context(), found.Messages[0].ID, msg.Parts[0]); err != nil {
		t.Fatalf("part content: %v", err)
	}
}
