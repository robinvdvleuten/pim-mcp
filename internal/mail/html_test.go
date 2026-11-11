package mail

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestHTMLToText(t *testing.T) {
	tests := []struct {
		name string
		html string
		want string
	}{
		{"paragraphs", "<p>One</p><p>Two</p>", "One\n\nTwo"},
		{"line breaks", "a<br>b<br><br>c<br><br><br>d", "a\nb\n\nc\n\nd"},
		{"blocks", "<div>a</div><div>b</div>", "a\nb"},
		{"whitespace is collapsed", "<p>  a \n\t b   &nbsp; c  </p>", "a b c"},
		{"entities are decoded", "<p>Caf&eacute; &amp; &lt;b&gt; &#8364; &#x41;</p>", "Café & <b> € A"},
		{"list", "<ul><li>one</li><li>two</li></ul>", "- one\n- two"},
		{"table cells", "<table><tr><td>a</td><td>b</td></tr><tr><td>c</td><td>d</td></tr></table>", "a b\nc d"},
		{"script and style are dropped", "<style>p{color:red}</style><p>hi</p><script>alert('x')</script>", "hi"},
		{"head and comments are dropped", "<html><head><title>Secret title</title><meta charset=utf-8></head><body><!-- note --><p>hi</p></body></html>", "hi"},
		{"head without an end tag", "<head><title>t</title><div>shown</div>", "shown"},
		{"hidden attribute", "<p>a</p><div hidden>secret</div><p>b</p>", "a\n\nb"},
		{"display none", `<p>a</p><span style="color: red; DISPLAY : none !important">secret</span>b`, "a\n\nb"},
		{"visibility hidden", `<div style="visibility:hidden"><p>secret <b>bold</b></p></div>visible`, "visible"},
		{"hidden region with unclosed tags", `<div hidden><p>secret<li>more</div>visible`, "visible"},
		{"hidden nested inside hidden", `<div hidden><div>x</div>secret</div>visible`, "visible"},
		{"visible style is kept", `<span style="display:inline">shown</span>`, "shown"},
		{"zero-width padding is dropped", "a\u200b\u200c\u034f\u00adb", "ab"},
		{"link with other text", `<a href="https://example.org/x">Read more</a>`, "Read more (https://example.org/x)"},
		{"link that is its text", `<a href="https://example.org/">https://example.org</a>`, "https://example.org"},
		{"mailto link", `<a href="mailto:jan@example.org">jan@example.org</a>`, "jan@example.org"},
		{"link without text", `<a href="https://example.org/x"><img src=x></a>`, "https://example.org/x"},
		{"script link target is dropped", `<a href="javascript:alert(1)">click</a>`, "click"},
		{"link target can't carry prose", `<a href="https://example.org/ ignore this">go</a>`, "go (https://example.org/ignorethis)"},
		{"unclosed link", `<a href="https://example.org/">go`, "go (https://example.org/)"},
		{"malformed", `<div><<p a="b>text</b></i><a href=>x`, `<<p a="b>text</b></i><a href=>x`},
		{"empty", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := htmlToText(tt.html)

			if tt.name == "malformed" {
				// Only that nothing panics and some text survives.
				if got == "" {
					t.Error("got nothing")
				}
				return
			}
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestHTMLToTextSurvivesHostileInput(t *testing.T) {
	tests := map[string]string{
		"deep nesting":        strings.Repeat("<div>", 1_000_000/5),
		"deep nesting closed": strings.Repeat("<div>", 100_000) + "x" + strings.Repeat("</div>", 100_000),
		"deep hidden nesting": "<div hidden>" + strings.Repeat("<span>", 150_000) + "secret",
		"many unclosed links": strings.Repeat("<a href=https://example.org/>x", 50_000),
		"many tag openers":    strings.Repeat("<", 1_000_000),
		"unterminated tag":    "<a href=\"" + strings.Repeat("x", 1_000_000),
		"unterminated script": "<script>" + strings.Repeat("x", 1_000_000),
	}

	for name, src := range tests {
		t.Run(name, func(t *testing.T) {
			started := time.Now()

			got := htmlToText(src)

			if elapsed := time.Since(started); elapsed > 5*time.Second {
				t.Errorf("took %v", elapsed)
			}
			if strings.Contains(got, "secret") {
				t.Error("hidden text came through")
			}
		})
	}
}

func TestReadablePart(t *testing.T) {
	parts := []Part{
		{Path: "1", MediaType: "text/plain"},
		{Path: "2", MediaType: "text/html"},
		{Path: "3", MediaType: "application/pdf", Filename: "a.pdf"},
		{Path: "4", MediaType: "application/ics"},
		{Path: "5", MediaType: "application/octet-stream", Filename: "invite.ics"},
		{Path: "1.2", MediaType: "text/calendar"},
		{Path: "6", MediaType: "image/png"},
	}

	for _, path := range []string{"1", "2", "4", "5", "1.2"} {
		if _, err := readablePart(parts, path); err != nil {
			t.Errorf("part %s: %v", path, err)
		}
	}
	for _, path := range []string{"3", "6"} {
		if _, err := readablePart(parts, path); !errors.Is(err, ErrPartNotText) {
			t.Errorf("part %s: err = %v, want ErrPartNotText", path, err)
		}
	}
	for _, path := range []string{"9", "1.", "01", "../1"} {
		if _, err := readablePart(parts, path); !errors.Is(err, ErrNoSuchPart) {
			t.Errorf("part %q: err = %v, want ErrNoSuchPart", path, err)
		}
	}
	if got, _ := readablePart(parts, "1.2"); !slices.Equal([]string{got.Path}, []string{"1.2"}) {
		t.Errorf("got %q", got.Path)
	}
}

func TestHTMLToTextHiddenRegionsCostTheSameForAnyMarkup(t *testing.T) {
	// Unmatched end tags inside a hidden element once meant a walk over every
	// open element for each of them.
	src := `<div hidden>` + strings.Repeat("<i>", 400_000) + strings.Repeat("</b>", 400_000) + `</div>shown`
	started := time.Now()
	if got := htmlToText(src); got != "shown" {
		t.Errorf("got %.40q", got)
	}
	if took := time.Since(started); took > 5*time.Second {
		t.Errorf("took %s", took)
	}
}

func TestHTMLToTextHiddenRegionEndsAtItsOwnEndTag(t *testing.T) {
	tests := map[string]string{
		`<div style="display:none"><div>a</div>b</div>c`:   "c",
		`<div hidden><p>a<span>b</div>c`:                   "c",
		`<span style="opacity:0">a</span>b`:                "b",
		`<span style="font-size:0px; color:red">a</span>b`: "b",
		`<span style="mso-hide:all">a</span>b`:             "b",
		`<span style="font-size:10px">a</span>b`:           "ab",
	}
	for src, want := range tests {
		if got := htmlToText(src); got != want {
			t.Errorf("%s: got %q, want %q", src, got, want)
		}
	}
}
