package mail

import (
	"encoding/base64"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

func TestServerCriteriaWidensDatesByADay(t *testing.T) {
	plus5 := time.FixedZone("", 5*3600)
	tests := []struct {
		name          string
		criteria      Criteria
		wantSince     time.Time
		wantBefore    time.Time
		wantNoSince   bool
		wantNoBefore  bool
		wantHeaders   []imap.SearchCriteriaHeaderField
		wantText      []string
		wantNoHeaders bool
	}{
		{
			name:         "since goes one day back",
			criteria:     Criteria{Since: time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)},
			wantSince:    time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC),
			wantNoBefore: true, wantNoHeaders: true,
		},
		{
			// IMAP compares the date a message gives in its own zone, so the day
			// before the UTC date is the safe lower bound.
			name:         "since is widened from its UTC date",
			criteria:     Criteria{Since: time.Date(2026, 11, 10, 1, 0, 0, 0, plus5)},
			wantSince:    time.Date(2026, 11, 8, 20, 0, 0, 0, time.UTC),
			wantNoBefore: true, wantNoHeaders: true,
		},
		{
			// SentBefore is exclusive: the day itself needs one day, the zone another.
			name:        "before goes two days forward",
			criteria:    Criteria{Before: time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)},
			wantBefore:  time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC),
			wantNoSince: true, wantNoHeaders: true,
		},
		{
			name: "text fields go to the server as they are",
			criteria: Criteria{From: "jan@example.org", To: "piet@example.org", Subject: "Lunch", Text: "soup",
				// Not sent to the server, as only reading a message tells.
				HasCalendar: new(true), Limit: 5},
			wantNoSince: true, wantNoBefore: true,
			wantHeaders: []imap.SearchCriteriaHeaderField{
				{Key: "From", Value: "jan@example.org"},
				{Key: "To", Value: "piet@example.org"},
				{Key: "Subject", Value: "Lunch"},
			},
			wantText: []string{"soup"},
		},
		{
			name:        "nothing set asks for everything",
			criteria:    Criteria{},
			wantNoSince: true, wantNoBefore: true, wantNoHeaders: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.criteria.serverCriteria()

			if tt.wantNoSince != got.SentSince.IsZero() || (!tt.wantNoSince && !got.SentSince.Equal(tt.wantSince)) {
				t.Errorf("SentSince = %v, want %v (none: %v)", got.SentSince, tt.wantSince, tt.wantNoSince)
			}
			if tt.wantNoBefore != got.SentBefore.IsZero() || (!tt.wantNoBefore && !got.SentBefore.Equal(tt.wantBefore)) {
				t.Errorf("SentBefore = %v, want %v (none: %v)", got.SentBefore, tt.wantBefore, tt.wantNoBefore)
			}
			if tt.wantNoHeaders && len(got.Header) != 0 {
				t.Errorf("Header = %v, want none", got.Header)
			}
			if !tt.wantNoHeaders && !slices.Equal(got.Header, tt.wantHeaders) {
				t.Errorf("Header = %v, want %v", got.Header, tt.wantHeaders)
			}
			if !slices.Equal(got.Text, tt.wantText) {
				t.Errorf("Text = %v, want %v", got.Text, tt.wantText)
			}
		})
	}
}

func TestNeedsReading(t *testing.T) {
	tests := []struct {
		name     string
		criteria Criteria
		want     bool
	}{
		{"server criteria only", Criteria{From: "a", Subject: "b", Text: "c"}, false},
		{"calendar filter, either way", Criteria{HasCalendar: new(false)}, true},
		{"since", Criteria{Since: time.Now()}, true},
		{"before", Criteria{Before: time.Now()}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.criteria.needsReading(); got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCriteriaMatches(t *testing.T) {
	noon := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name     string
		criteria Criteria
		summary  Summary
		want     bool
	}{
		{"nothing to check", Criteria{}, Summary{}, true},
		{"wants a calendar and has one", Criteria{HasCalendar: new(true)}, Summary{HasCalendar: true}, true},
		{"wants a calendar and has none", Criteria{HasCalendar: new(true)}, Summary{}, false},
		{"wants none and has one", Criteria{HasCalendar: new(false)}, Summary{HasCalendar: true}, false},
		{"wants none and has none", Criteria{HasCalendar: new(false)}, Summary{}, true},
		{"since includes its own instant", Criteria{Since: noon}, Summary{Date: noon}, true},
		{"since excludes a second before", Criteria{Since: noon}, Summary{Date: noon.Add(-time.Second)}, false},
		{"before excludes its own instant", Criteria{Before: noon}, Summary{Date: noon}, false},
		{"before includes a second earlier", Criteria{Before: noon}, Summary{Date: noon.Add(-time.Second)}, true},
		{
			// Widened server bounds let this one through, so exact bounds must stop it.
			"a message the widened range returned but the exact range excludes",
			Criteria{Since: noon, Before: noon.Add(24 * time.Hour)}, Summary{Date: noon.Add(-12 * time.Hour)}, false,
		},
		{"a message without a date is before any since", Criteria{Since: noon}, Summary{}, false},
		{"all must hold", Criteria{HasCalendar: new(true), Since: noon}, Summary{HasCalendar: true, Date: noon.Add(-time.Hour)}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.criteria.matches(tt.summary); got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMessageIDRoundTrip(t *testing.T) {
	tests := []struct {
		folder   string
		validity uint32
		uid      imap.UID
	}{
		{"INBOX", 1, 1},
		{"INBOX", 4294967295, 4294967295},
		{"Archive/2026:Q4", 0, 7},
		{"Verzonden items", 99, 3},
		{"Entwürfe/日本語", 5, 12},
		{"a:b:c", 8, 9},
	}
	seen := map[string]bool{}
	for _, tt := range tests {
		id := encodeID(tt.folder, tt.validity, tt.uid)
		folder, validity, uid, err := decodeID(id)

		if err != nil || folder != tt.folder || validity != tt.validity || uid != tt.uid {
			t.Errorf("%q: got %q %d %d, %v", id, folder, validity, uid, err)
		}
		if strings.ContainsAny(id, "+/=") {
			t.Errorf("%q isn't safe to pass around", id)
		}
		if seen[id] {
			t.Errorf("%q is used twice", id)
		}
		seen[id] = true
	}
	// A recreated folder gets a new UIDVALIDITY, which must make a new ID.
	if encodeID("INBOX", 1, 5) == encodeID("INBOX", 2, 5) {
		t.Error("UIDVALIDITY isn't part of the ID")
	}
}

func TestDecodeIDRejectsMalformedIDs(t *testing.T) {
	encode := func(raw string) string { return encodeIDRaw(raw) }
	tests := map[string]string{
		"padding":               encode("1:1:INBOX") + "=",
		"standard alphabet":     "Pz8-Pz8_", // not an ID, and not decodable as one
		"no folder":             encode("1:1:"),
		"two fields":            encode("1:1"),
		"one field":             encode("INBOX"),
		"UIDVALIDITY too large": encode("4294967296:1:INBOX"),
		"UID too large":         encode("1:4294967296:INBOX"),
		"UID zero":              encode("1:0:INBOX"),
		"negative UID":          encode("1:-1:INBOX"),
		"negative UIDVALIDITY":  encode("-1:1:INBOX"),
		"space in a number":     encode(" 1:1:INBOX"),
		"sign in a number":      encode("+1:1:INBOX"),
		"hex number":            encode("0x1:1:INBOX"),
		"empty UID":             encode("1::INBOX"),
		"empty UIDVALIDITY":     encode(":1:INBOX"),
		"text for UID":          encode("1:abc:INBOX"),
	}
	for name, id := range tests {
		t.Run(name, func(t *testing.T) {
			if folder, validity, uid, err := decodeID(id); err == nil {
				t.Errorf("accepted %q as %q %d %d", id, folder, validity, uid)
			}
		})
	}
}

func encodeIDRaw(raw string) string {
	// The same encoding encodeID uses, for IDs it would never make.
	return strings.TrimRight(base64URL(raw), "=")
}

func TestFolderRole(t *testing.T) {
	tests := []struct {
		name    string
		mailbox string
		attrs   []imap.MailboxAttr
		want    string
	}{
		{"inbox by name", "INBOX", nil, RoleInbox},
		{"inbox in any case", "Inbox", nil, RoleInbox},
		{"sent", "Verzonden", []imap.MailboxAttr{imap.MailboxAttrSent}, RoleSent},
		{"drafts", "Concepten", []imap.MailboxAttr{imap.MailboxAttrDrafts}, RoleDrafts},
		{"trash", "Prullenbak", []imap.MailboxAttr{imap.MailboxAttrTrash}, RoleTrash},
		{"junk", "Spam", []imap.MailboxAttr{imap.MailboxAttrJunk}, RoleJunk},
		{"archive", "Archief", []imap.MailboxAttr{imap.MailboxAttrArchive}, RoleArchive},
		{"all mail", "All Mail", []imap.MailboxAttr{imap.MailboxAttrAll}, RoleAll},
		{"flagged", "Starred", []imap.MailboxAttr{imap.MailboxAttrFlagged}, RoleFlagged},
		{"other attributes are not roles", "Work", []imap.MailboxAttr{imap.MailboxAttrHasNoChildren}, ""},
		{"the role is found among other attributes", "Sent Items", []imap.MailboxAttr{imap.MailboxAttrHasNoChildren, imap.MailboxAttrSent}, RoleSent},
		{"a name alone says nothing", "Sent", nil, ""},
		{"inbox by name wins over an attribute", "INBOX", []imap.MailboxAttr{imap.MailboxAttrArchive}, RoleInbox},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := folderRole(&imap.ListData{Mailbox: tt.mailbox, Attrs: tt.attrs})

			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSummarize(t *testing.T) {
	date := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	invite := &imap.BodyStructureMultiPart{Subtype: "mixed", Children: []imap.BodyStructure{
		&imap.BodyStructureSinglePart{Type: "text", Subtype: "plain"},
		&imap.BodyStructureSinglePart{Type: "text", Subtype: "calendar"},
	}}
	message := &imapclient.FetchMessageBuffer{
		UID: 42,
		Envelope: &imap.Envelope{
			Subject: "Lunch",
			Date:    date,
			From:    []imap.Address{{Name: "Jan", Mailbox: "jan", Host: "example.org"}},
		},
		BodyStructure: invite,
	}

	got := summarize("Archive", 7, message)

	folder, validity, uid, err := decodeID(got.ID)
	if err != nil || folder != "Archive" || validity != 7 || uid != 42 {
		t.Errorf("ID %q decodes to %q %d %d, %v", got.ID, folder, validity, uid, err)
	}
	if got.Subject != "Lunch" || got.From != "Jan <jan@example.org>" || !got.Date.Equal(date) || !got.HasCalendar {
		t.Errorf("got %+v", got)
	}

	t.Run("a message the server gave no envelope for", func(t *testing.T) {
		got := summarize("INBOX", 1, &imapclient.FetchMessageBuffer{UID: 1})

		if got.ID == "" || got.Subject != "" || got.From != "" || !got.Date.IsZero() || got.HasCalendar {
			t.Errorf("got %+v", got)
		}
	})
}

func TestMessageParts(t *testing.T) {
	structure := &imap.BodyStructureMultiPart{Subtype: "mixed", Children: []imap.BodyStructure{
		&imap.BodyStructureMultiPart{Subtype: "alternative", Children: []imap.BodyStructure{
			&imap.BodyStructureSinglePart{Type: "TEXT", Subtype: "Plain", Encoding: "quoted-printable", Size: 10,
				Params: map[string]string{"charset": "iso-8859-1"}},
			&imap.BodyStructureSinglePart{Type: "text", Subtype: "html", Encoding: "base64", Size: 20},
		}},
		&imap.BodyStructureSinglePart{
			Type: "application", Subtype: "pdf", Size: 3000, Encoding: "base64",
			Params:   map[string]string{"name": "old-style.pdf"},
			Extended: &imap.BodyStructureSinglePartExt{Disposition: &imap.BodyStructureDisposition{Value: "ATTACHMENT", Params: map[string]string{"filename": "a.pdf"}}},
		},
	}}

	got := messageParts(structure)

	if len(got) != 3 {
		t.Fatalf("got %d parts: %+v", len(got), got)
	}
	plain, html, pdf := got[0], got[1], got[2]
	if plain.Path != "1.1" || plain.MediaType != "text/plain" || plain.charset != "iso-8859-1" || plain.encoding != "quoted-printable" || plain.attachment {
		t.Errorf("plain = %+v", plain)
	}
	if html.Path != "1.2" || html.MediaType != "text/html" || html.encoding != "base64" || html.charset != "" {
		t.Errorf("html = %+v", html)
	}
	if pdf.Path != "2" || pdf.Filename != "a.pdf" || !pdf.attachment || pdf.Size != 3000 || !slices.Equal(pdf.section, []int{2}) {
		t.Errorf("pdf = %+v", pdf)
	}
	// What bodyPart picks is the one that is not an attachment.
	if body, ok := bodyPart(got); !ok || body.Path != "1.1" {
		t.Errorf("body = %+v, %v", body, ok)
	}

	t.Run("a message without structure", func(t *testing.T) {
		if got := messageParts(nil); got == nil || len(got) != 0 {
			t.Errorf("got %#v, want an empty, non-nil list", got)
		}
	})
	t.Run("a single part is path 1", func(t *testing.T) {
		got := messageParts(&imap.BodyStructureSinglePart{Type: "text", Subtype: "plain"})

		if len(got) != 1 || got[0].Path != "1" {
			t.Errorf("got %+v", got)
		}
	})
}

func TestIsCalendar(t *testing.T) {
	tests := []struct {
		name string
		part Part
		want bool
	}{
		{"text/calendar", Part{MediaType: "text/calendar"}, true},
		{"application/ics", Part{MediaType: "application/ics"}, true},
		{"an attachment named for one", Part{MediaType: "application/octet-stream", Filename: "Invite.ICS"}, true},
		{"plain text", Part{MediaType: "text/plain"}, false},
		{"a file that only mentions ics", Part{MediaType: "application/pdf", Filename: "ics-guide.pdf"}, false},
		{"nothing", Part{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.part.isCalendar(); got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestFirstAddress(t *testing.T) {
	if got := firstAddress(nil); got != "" {
		t.Errorf("got %q for no addresses", got)
	}
	got := firstAddress([]imap.Address{{Name: "Group start"}, {Name: "Jan", Mailbox: "jan", Host: "example.org"}})
	if got != "Jan <jan@example.org>" {
		t.Errorf("got %q, want the first real address", got)
	}
}

func TestBodyTextWithAWrongCharsetLabel(t *testing.T) {
	tests := []struct {
		name    string
		raw     []byte
		charset string
		want    string
	}{
		{"Latin-1 bytes labelled UTF-8", []byte("Caf\xe9"), "utf-8", "Caf�"},
		{"UTF-8 bytes labelled Latin-1 come out garbled but valid", []byte("Caf\xc3\xa9"), "iso-8859-1", "CafÃ©"},
		{"high bytes labelled ASCII", []byte("na\xefve"), "us-ascii", "na�ve"},
		{"a label with quotes is unknown, so the bytes are used", []byte("Caf\xc3\xa9"), `"utf-8"`, "Café"},
		{"an empty label after trimming is not special", []byte("x"), " ", "x"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := bodyText(tt.raw, tt.charset)

			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
			if !utf8.ValidString(got) {
				t.Error("the result isn't valid UTF-8, so it can't go into JSON")
			}
		})
	}

	t.Run("UTF-16 with a truncated unit still gives valid UTF-8", func(t *testing.T) {
		got := bodyText([]byte{'h', 0, 'i', 0, 'x'}, "utf-16le")

		if !utf8.ValidString(got) || !strings.HasPrefix(got, "hi") {
			t.Errorf("got %q", got)
		}
	})
}

func TestTruncateBodyAtMultiByteBoundaries(t *testing.T) {
	// The limit falls 1, 2 or 3 bytes into a four-byte character.
	for inside := 1; inside <= 3; inside++ {
		prefix := strings.Repeat("a", maxBodyBytes-inside)

		text, truncated := truncateBody(prefix + "😀" + "tail")

		if !truncated || text != prefix {
			t.Errorf("%d bytes into the character: got %d bytes (valid %v), truncated %v", inside, len(text), utf8.ValidString(text), truncated)
		}
	}

	t.Run("a character that ends at the limit is kept", func(t *testing.T) {
		prefix := strings.Repeat("a", maxBodyBytes-3)

		text, truncated := truncateBody(prefix + "€" + "tail")

		if !truncated || text != prefix+"€" {
			t.Errorf("got %d bytes, truncated %v", len(text), truncated)
		}
	})
	t.Run("text that is all multi-byte", func(t *testing.T) {
		text, truncated := truncateBody(strings.Repeat("日", maxBodyBytes))

		if !truncated || !utf8.ValidString(text) || len(text) > maxBodyBytes || len(text) < maxBodyBytes-2 {
			t.Errorf("got %d bytes, valid %v, truncated %v", len(text), utf8.ValidString(text), truncated)
		}
	})
}

func TestDecodeMoreEncodings(t *testing.T) {
	tests := []struct {
		name, raw, encoding, want string
	}{
		{"upper case 7BIT", "plain", "7BIT", "plain"},
		{"binary is untouched", "a\x00b", "binary", "a\x00b"},
		{"8bit is untouched", "caf\xc3\xa9", "8bit", "caf\xc3\xa9"},
		{"quoted-printable keeps hard line breaks", "a\r\nb", "Quoted-Printable", "a\r\nb"},
		{"quoted-printable with a soft break inside a word", "ab=\r\ncd", "quoted-printable", "abcd"},
		{"base64 with only noise", "!!!\r\n", "base64", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := decode([]byte(tt.raw), tt.encoding)
			if err != nil || string(got) != tt.want {
				t.Errorf("got %q, %v, want %q", got, err, tt.want)
			}
		})
	}

	t.Run("broken quoted-printable is passed through, as mail clients do", func(t *testing.T) {
		got, err := decode([]byte("caf=ZZ"), "quoted-printable")

		if err != nil || string(got) != "caf=ZZ" {
			t.Errorf("got %q, %v", got, err)
		}
	})
	t.Run("the unknown encoding is named", func(t *testing.T) {
		_, err := decode([]byte("x"), "x-uuencode")

		if err == nil || !strings.Contains(err.Error(), "x-uuencode") {
			t.Errorf("err = %v", err)
		}
	})
}

func TestJoinPath(t *testing.T) {
	tests := map[string][]int{"": nil, "1": {1}, "1.2.10": {1, 2, 10}}
	for want, path := range tests {
		if got := joinPath(path); got != want {
			t.Errorf("joinPath(%v) = %q, want %q", path, got, want)
		}
	}
}

func base64URL(raw string) string { return base64.RawURLEncoding.EncodeToString([]byte(raw)) }
