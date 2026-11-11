package mail

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/net/html"
)

// voidElements have no end tag, so they never open a hidden region.
var voidElements = map[string]bool{
	"area": true, "base": true, "br": true, "col": true, "embed": true, "hr": true,
	"img": true, "input": true, "link": true, "meta": true, "source": true,
	"track": true, "wbr": true,
}

// droppedElements never show as text to a reader.
var droppedElements = map[string]bool{
	"head": true, "script": true, "style": true, "template": true, "title": true,
}

// headElements may appear inside a head. The first other element ends it, since
// HTML lets a head go without its end tag.
var headElements = map[string]bool{
	"title": true, "meta": true, "link": true, "style": true, "script": true,
	"base": true, "noscript": true, "template": true,
}

// paragraphElements are set apart by a blank line, other block elements by a
// line break.
var paragraphElements = map[string]bool{
	"p": true, "h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true,
	"blockquote": true, "pre": true, "ul": true, "ol": true, "table": true,
}

var blockElements = map[string]bool{
	"address": true, "article": true, "aside": true, "center": true, "dd": true,
	"details": true, "div": true, "dl": true, "dt": true, "fieldset": true,
	"figcaption": true, "figure": true, "footer": true, "form": true, "header": true,
	"hr": true, "li": true, "main": true, "nav": true, "section": true,
	"summary": true, "tr": true,
}

// htmlToText turns HTML mail into the text a person would read. It uses the
// tokenizer, which keeps no tree, so deep nesting costs no stack, and it drops
// what the reader can't see because mail is hostile input: text hidden in the
// markup would reach the agent but not the person. It can't know about every
// trick, such as white text on a white background.
func htmlToText(src string) string {
	w := &htmlWriter{}
	z := html.NewTokenizer(strings.NewReader(src))
	for {
		switch z.Next() {
		case html.ErrorToken:
			w.closeLink()
			return string(w.buf)
		case html.TextToken:
			if !w.hidden {
				w.text(string(z.Text()))
			}
		case html.StartTagToken:
			w.start(z.Token(), false)
		case html.SelfClosingTagToken:
			w.start(z.Token(), true)
		case html.EndTagToken:
			w.end(z.Token().Data)
		}
	}
}

type htmlWriter struct {
	buf      []byte
	newlines int  // line breaks waiting to be written
	space    bool // a space waiting to be written
	// hidden is set inside an element the reader can't see. hiddenName is that
	// element and hiddenDepth counts the ones of the same name open inside it,
	// so its own end tag is found whatever else is left unclosed. Counting,
	// rather than keeping every open element, costs the same for any markup.
	hidden      bool
	hiddenName  string
	hiddenDepth int
	link        *openLink
}

type openLink struct {
	href  string
	start int // where its text begins in buf
}

func (w *htmlWriter) start(tag html.Token, selfClosing bool) {
	name := tag.Data
	if w.hidden && w.hiddenName == "head" && !headElements[name] {
		w.hidden = false
	}
	if w.hidden {
		if name == w.hiddenName && !selfClosing {
			w.hiddenDepth++
		}
		return
	}
	if droppedElements[name] || isHiddenByAttrs(tag.Attr) {
		if !selfClosing && !voidElements[name] {
			w.hidden, w.hiddenName, w.hiddenDepth = true, name, 1
		}
		return
	}
	switch {
	case name == "br":
		w.newlines = min(w.newlines+1, 2)
	case name == "a":
		w.closeLink()
		if href := cleanHref(attr(tag.Attr, "href")); href != "" {
			w.link = &openLink{href: href, start: len(w.buf)}
		}
	case name == "li":
		w.breakLine(1)
		w.literal("- ")
	case name == "td" || name == "th":
		w.space = true
	case paragraphElements[name]:
		w.breakLine(2)
	case blockElements[name]:
		w.breakLine(1)
	}
}

func (w *htmlWriter) end(name string) {
	if w.hidden {
		if name == w.hiddenName {
			if w.hiddenDepth--; w.hiddenDepth == 0 {
				w.hidden = false
			}
		}
		return
	}
	switch {
	case name == "a":
		w.closeLink()
	case name == "td" || name == "th":
		w.space = true
	case paragraphElements[name]:
		w.breakLine(2)
	case blockElements[name]:
		w.breakLine(1)
	}
}

func (w *htmlWriter) breakLine(n int) {
	w.newlines = max(w.newlines, n)
}

// flush writes the break or space that came before the next text. Nothing is
// written at the start, and a line break wins over a space.
func (w *htmlWriter) flush() {
	if len(w.buf) > 0 {
		if w.newlines > 0 {
			w.buf = append(w.buf, strings.Repeat("\n", w.newlines)...)
		} else if w.space {
			w.buf = append(w.buf, ' ')
		}
	}
	w.newlines, w.space = 0, false
}

func (w *htmlWriter) literal(s string) {
	w.flush()
	w.buf = append(w.buf, s...)
}

// text collapses runs of whitespace into one space. Zero-width characters are
// dropped: mail uses them to pad a preview, and they are invisible.
func (w *htmlWriter) text(s string) {
	for _, r := range s {
		switch {
		case unicode.IsSpace(r):
			w.space = true
		case isInvisible(r):
		default:
			w.flush()
			w.buf = utf8.AppendRune(w.buf, r)
		}
	}
}

func isInvisible(r rune) bool {
	switch r {
	case 0xAD, 0x34F, 0xFEFF, 0x2060:
		return true
	}
	return r >= 0x200B && r <= 0x200F
}

// closeLink adds the target of a link after its text, unless the text already
// is the target.
func (w *htmlWriter) closeLink() {
	l := w.link
	if l == nil {
		return
	}
	w.link = nil
	text := strings.TrimSpace(string(w.buf[l.start:]))
	switch {
	case text == "":
		w.literal(l.href)
	case !strings.EqualFold(bareURL(text), bareURL(l.href)):
		w.buf = append(w.buf, " ("+l.href+")"...)
	}
}

func bareURL(s string) string {
	s = strings.ToLower(s)
	for _, prefix := range []string{"https://", "http://", "mailto:", "tel:"} {
		s = strings.TrimPrefix(s, prefix)
	}
	return strings.TrimRight(s, "/")
}

// cleanHref keeps targets a reader can follow. A script or data URL is no use
// as text and is left out. Whitespace and control characters are removed, as
// browsers do, so a target can't carry prose.
func cleanHref(href string) string {
	href = strings.Map(func(r rune) rune {
		if r <= ' ' || r == 0x7f {
			return -1
		}
		return r
	}, href)
	lower := strings.ToLower(href)
	for _, scheme := range []string{"http://", "https://", "mailto:", "tel:"} {
		if strings.HasPrefix(lower, scheme) {
			return href
		}
	}
	return ""
}

func attr(attrs []html.Attribute, key string) string {
	for _, a := range attrs {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

func isHiddenByAttrs(attrs []html.Attribute) bool {
	for _, a := range attrs {
		switch a.Key {
		case "hidden":
			return true
		case "style":
			if styleHides(a.Val) {
				return true
			}
		}
	}
	return false
}

// styleHides spots the inline styles mail uses to keep text from the reader:
// display:none, visibility:hidden, and text made invisible or sizeless.
func styleHides(style string) bool {
	for _, declaration := range strings.Split(style, ";") {
		property, value, ok := strings.Cut(declaration, ":")
		if !ok {
			continue
		}
		property = strings.ToLower(strings.TrimSpace(property))
		value = strings.ToLower(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(value), "!important")))
		switch {
		case property == "display" && value == "none",
			property == "visibility" && value == "hidden",
			property == "opacity" && (value == "0" || value == "0.0"),
			property == "font-size" && (value == "0" || value == "0px"),
			property == "mso-hide" && value == "all":
			return true
		}
	}
	return false
}
