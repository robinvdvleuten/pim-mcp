package mail

import (
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/emersion/go-imap/v2"
)

func TestBodyPart(t *testing.T) {
	plain := Part{Path: "1", MediaType: "text/plain"}
	html := Part{Path: "2", MediaType: "text/html"}
	calendar := Part{Path: "3", MediaType: "text/calendar"}
	attachedText := Part{Path: "4", MediaType: "text/plain", attachment: true}
	namedText := Part{Path: "5", MediaType: "text/plain", Filename: "notes.txt"}
	attachedHTML := Part{Path: "6", MediaType: "text/html", attachment: true}
	pdf := Part{Path: "7", MediaType: "application/pdf"}

	tests := []struct {
		name  string
		parts []Part
		want  string // path, empty for none
	}{
		{"plain text is preferred", []Part{html, plain}, "1"},
		{"HTML when there is nothing else", []Part{pdf, html}, "2"},
		{"an attached text file isn't the body", []Part{attachedText, html}, "2"},
		{"a named text file isn't the body", []Part{namedText, html}, "2"},
		{"an attached HTML file isn't the body", []Part{attachedHTML}, ""},
		{"a calendar invite isn't the body", []Part{calendar}, ""},
		{"nothing readable", []Part{pdf}, ""},
		{"no parts", nil, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := bodyPart(tt.parts)

			if tt.want == "" {
				if ok {
					t.Errorf("got part %s, want none", got.Path)
				}
				return
			}
			if !ok || got.Path != tt.want {
				t.Errorf("got part %q (found %v), want %q", got.Path, ok, tt.want)
			}
		})
	}
}

func TestBodyText(t *testing.T) {
	tests := []struct {
		name    string
		raw     []byte
		charset string
		want    string
	}{
		{"UTF-8 stays as it is", []byte("Caf\xc3\xa9"), "utf-8", "Café"},
		{"no charset means UTF-8", []byte("Caf\xc3\xa9"), "", "Café"},
		{"upper case label", []byte("Caf\xc3\xa9"), "UTF-8", "Café"},
		{"Windows-1252", []byte("Caf\xe9 \x80"), "windows-1252", "Café €"},
		{"ISO-8859-15", []byte("Caf\xe9 \xa4"), "iso-8859-15", "Café €"},
		{"invalid UTF-8 can't reach JSON", []byte("a\xffb"), "utf-8", "a�b"},
		{"an unknown charset still gives something readable", []byte("abc\xff"), "x-klingon", "abc�"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := bodyText(tt.raw, tt.charset); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestTruncateBody(t *testing.T) {
	t.Run("short text is untouched", func(t *testing.T) {
		text, truncated := truncateBody("hello")

		if text != "hello" || truncated {
			t.Errorf("got %q, truncated %v", text, truncated)
		}
	})

	t.Run("exactly at the limit is untouched", func(t *testing.T) {
		body := strings.Repeat("a", maxBodyBytes)

		text, truncated := truncateBody(body)

		if text != body || truncated {
			t.Errorf("got %d bytes, truncated %v", len(text), truncated)
		}
	})

	t.Run("over the limit is cut and says so", func(t *testing.T) {
		text, truncated := truncateBody(strings.Repeat("a", maxBodyBytes+1))

		if !truncated || len(text) != maxBodyBytes {
			t.Errorf("got %d bytes, truncated %v", len(text), truncated)
		}
	})

	t.Run("never cuts a character in half", func(t *testing.T) {
		// A three-byte character straddles the limit.
		text, truncated := truncateBody(strings.Repeat("a", maxBodyBytes-1) + "€")

		if !truncated || !utf8.ValidString(text) || len(text) != maxBodyBytes-1 {
			t.Errorf("got %d bytes, valid %v, truncated %v", len(text), utf8.ValidString(text), truncated)
		}
	})
}

func TestDecode(t *testing.T) {
	tests := []struct {
		name     string
		raw      string
		encoding string
		want     string
	}{
		{"7bit", "plain", "7bit", "plain"},
		{"no encoding", "plain", "", "plain"},
		{"base64 with line breaks", "aGVs\r\nbG8=\r\n", "base64", "hello"},
		{"upper case name", "aGVsbG8=", "BASE64", "hello"},
		{"quoted-printable", "caf=C3=A9=\r\n!", "quoted-printable", "café!"},
		{"base64 with stray characters, which RFC 2045 says to ignore", "aGV s\tbG8=\r\n!", "base64", "hello"},
		{"base64 glued together from separately padded pieces", "aGk=\r\nYWI=\r\nYw==", "base64", "hiabc"},
		{"base64 with no padding", "aGVsbG8", "base64", "hello"},
		{"empty base64", "\r\n", "base64", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := decode([]byte(tt.raw), tt.encoding)
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if string(got) != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}

	if _, err := decode([]byte("x"), "x-uuencode"); err == nil {
		t.Error("an unknown encoding passed")
	}
}

func TestDecodeIDRejectsForgeries(t *testing.T) {
	for _, id := range []string{"", "not base64!", "bm90IGFuIGlk", "MToyOg", "eDoyOklOQk9Y", "MTp4OklOQk9Y", "MTowOklOQk9Y"} {
		if _, _, _, err := decodeID(id); err == nil {
			t.Errorf("decodeID(%q) passed", id)
		}
	}
}

func TestFormatAddresses(t *testing.T) {
	got := formatAddresses([]imap.Address{
		{Name: "Jan", Mailbox: "jan", Host: "example.org"},
		{Mailbox: "plain", Host: "example.org"},
		{Name: "same@example.org", Mailbox: "same", Host: "example.org"},
		{Name: "SHOUT@EXAMPLE.ORG", Mailbox: "shout", Host: "example.org"},
		{Name: "Group start"},
	})

	want := []string{"Jan <jan@example.org>", "plain@example.org", "same@example.org", "shout@example.org"}
	if !slices.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestNewestFirst(t *testing.T) {
	older := time.Date(2019, 1, 28, 10, 0, 0, 0, time.UTC)
	// Later on the clock, but earlier in time.
	newer := time.Date(2026, 11, 2, 9, 0, 0, 0, time.FixedZone("", 5*3600))
	newest := time.Date(2026, 11, 2, 8, 0, 0, 0, time.UTC)
	messages := []Summary{{Subject: "no date"}, {Subject: "older", Date: older}, {Subject: "newest", Date: newest}, {Subject: "newer", Date: newer}}

	newestFirst(messages)

	got := []string{messages[0].Subject, messages[1].Subject, messages[2].Subject, messages[3].Subject}
	if !slices.Equal(got, []string{"newest", "newer", "older", "no date"}) {
		t.Errorf("got %v", got)
	}
}

func TestDecodeRefusesBase64ThatCannotBeRead(t *testing.T) {
	// One leftover character holds too few bits to be a byte.
	if got, err := decode([]byte("aGVsb"), "base64"); err == nil {
		t.Errorf("got %q, want an error", got)
	}
}
