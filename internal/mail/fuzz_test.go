package mail

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/emersion/go-imap/v2"
)

// Message bodies come from strangers, so decoding must never panic.
func FuzzDecodeBase64(f *testing.F) {
	for _, seed := range []string{"", "aGVsbG8=", "aGk=\r\nYWI=\r\nYw==", "aGVsb", "====", "a=b=c=", "\x00\xff!!"} {
		f.Add([]byte(seed))
	}

	f.Fuzz(func(t *testing.T, raw []byte) {
		got, err := decodeBase64(raw)
		if err != nil {
			return
		}
		// Every four characters decode to at most three bytes.
		if len(got) > len(raw) {
			t.Fatalf("%d bytes decoded into %d", len(raw), len(got))
		}
	})
}

func FuzzHTMLToText(f *testing.F) {
	for _, seed := range []string{
		"<p>One</p><p>Two</p>",
		`<a href="https://example.org/x">Read more</a>`,
		`<div hidden>secret</div>visible`,
		`<span style="display:none">secret</span>visible`,
		"<head><title>t</title><div>shown</div>",
		"<p>Caf&eacute; &#8364; &#x41;</p>",
		"<ul><li>a<li>b</ul><table><tr><td>c<td>d",
		"<div hidden><p>secret<li>more</div>visible",
		"<<<<>>>> <a href=\"",
		"<style>p{}</style><script>alert('x')</script>after",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, src string) {
		got := htmlToText(src)
		if utf8.ValidString(src) && !utf8.ValidString(got) {
			t.Fatalf("invalid UTF-8 from valid input: %q", got)
		}
	})
}

// Text around a script element, however odd, can't bring the script's body into
// view. The prefix has no "<" so that it can't open a tag, comment or attribute
// that swallows the script tag itself.
func FuzzHTMLScriptNeverShown(f *testing.F) {
	f.Add("before ", " after")
	f.Add("&lt;script&gt;", "<p>x</p>")
	f.Add("", "</script>")

	f.Fuzz(func(t *testing.T, prefix, suffix string) {
		if strings.Contains(prefix, "<") {
			return
		}
		const secret = "ZQXSCRIPTBODY"
		got := htmlToText(prefix + "<script>var a = '" + secret + "';</script>" + suffix)
		if strings.Contains(got, secret) && !strings.Contains(prefix+suffix, secret) {
			t.Fatalf("script body shown: %q", got)
		}
	})
}

func FuzzMessageID(f *testing.F) {
	f.Add("INBOX", uint32(1), uint32(1))
	f.Add("a:b:c", uint32(0), uint32(4294967295))
	f.Add("Gesendet/ü", uint32(42), uint32(7))

	f.Fuzz(func(t *testing.T, folder string, validity, uid uint32) {
		if uid == 0 || folder == "" {
			return
		}
		gotFolder, gotValidity, gotUID, err := decodeID(encodeID(folder, validity, imap.UID(uid)))
		if err != nil {
			t.Fatalf("round trip failed: %v", err)
		}
		if gotFolder != folder || gotValidity != validity || gotUID != imap.UID(uid) {
			t.Fatalf("got %q %d %d, want %q %d %d", gotFolder, gotValidity, gotUID, folder, validity, uid)
		}
	})
}

// An ID is made by anyone who can write to the tool, so arbitrary text must be
// refused or decoded, never crash.
func FuzzDecodeID(f *testing.F) {
	f.Add("")
	f.Add("MToyOklOQk9Y")
	f.Add("!!!")

	f.Fuzz(func(t *testing.T, id string) {
		folder, _, uid, err := decodeID(id)
		if err == nil && (folder == "" || uid == 0) {
			t.Fatalf("accepted %q as folder %q uid %d", id, folder, uid)
		}
	})
}
