package mail

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime/quotedprintable"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-message/charset"
)

const (
	maxBodyBytes         = 64 * 1024
	maxMessageBytes      = 25 * 1024 * 1024
	maxCalendarPartBytes = 1024 * 1024
)

// Errors callers can act on.
var (
	// ErrMessageTooLarge means a message or part is over the size the server reads.
	ErrMessageTooLarge = errors.New("over the 25 MiB limit")
	// ErrNoCalendarPart means a message holds no calendar invite.
	ErrNoCalendarPart = errors.New("this message has no calendar part")
	// ErrCalendarPartTooLarge means an invite is bigger than any real one.
	ErrCalendarPartTooLarge = errors.New("the calendar part is over the 1 MiB limit")
)

// Part describes one leaf of a message's MIME structure.
type Part struct {
	Path      string `json:"path"`
	MediaType string `json:"media_type"`
	Size      uint32 `json:"size"`
	Filename  string `json:"filename,omitempty"`

	section    []int
	encoding   string
	charset    string
	attachment bool
}

// Message holds the headers and structure of a message, without its content.
type Message struct {
	ID          string    `json:"id"`
	UID         uint32    `json:"-"`
	Subject     string    `json:"subject"`
	From        string    `json:"from"`
	To          []string  `json:"to,omitempty"`
	Cc          []string  `json:"cc,omitempty"`
	ReplyTo     []string  `json:"reply_to,omitempty"`
	Date        time.Time `json:"date"`
	MessageID   string    `json:"message_id,omitempty"`
	HasCalendar bool      `json:"has_calendar"`
	Parts       []Part    `json:"parts"`
}

// Content is a message together with its readable text. BodyType says whether
// Body is text/plain or text/html, and is empty if there is no readable text.
type Content struct {
	Message
	Body          string `json:"body"`
	BodyType      string `json:"body_type"`
	BodyTruncated bool   `json:"body_truncated"`
}

// Message returns a message's headers and MIME structure. It refuses messages
// over the size limit, which also bounds what reading their parts can cost.
func (c *Client) Message(ctx context.Context, id string) (*Message, error) {
	defer c.bind(ctx)()

	uid, err := c.open(ctx, id)
	if err != nil {
		return nil, err
	}
	buf, err := c.fetchOne(ctx, uid, &imap.FetchOptions{
		Envelope:      true,
		RFC822Size:    true,
		BodyStructure: &imap.FetchItemBodyStructure{Extended: true},
	})
	if err != nil {
		return nil, err
	}
	if buf.RFC822Size > maxMessageBytes {
		return nil, fmt.Errorf("message is %d MiB: %w", buf.RFC822Size/(1024*1024), ErrMessageTooLarge)
	}

	parts := messageParts(buf.BodyStructure)
	return &Message{
		ID:          id,
		UID:         uint32(uid),
		Subject:     buf.Envelope.Subject,
		From:        firstAddress(buf.Envelope.From),
		To:          formatAddresses(buf.Envelope.To),
		Cc:          formatAddresses(buf.Envelope.Cc),
		ReplyTo:     formatAddresses(buf.Envelope.ReplyTo),
		Date:        buf.Envelope.Date,
		MessageID:   buf.Envelope.MessageID,
		HasCalendar: slices.ContainsFunc(parts, Part.isCalendar),
		Parts:       parts,
	}, nil
}

// Read returns a message with its readable text.
func (c *Client) Read(ctx context.Context, id string) (*Content, error) {
	message, err := c.Message(ctx, id)
	if err != nil {
		return nil, err
	}
	content := &Content{Message: *message}
	part, ok := bodyPart(message.Parts)
	if !ok {
		return content, nil
	}
	raw, err := c.PartContent(ctx, id, part)
	if err != nil {
		return nil, err
	}
	content.BodyType = part.MediaType
	content.Body, content.BodyTruncated = truncateBody(bodyText(raw, part.charset))
	return content, nil
}

// CalendarPart returns a message's invite as UTF-8. If a message has several
// calendar parts, such as the same invite as text and as an attachment, the
// first is used, which RFC 6047 makes the one meant for reading.
func (c *Client) CalendarPart(ctx context.Context, id string) ([]byte, error) {
	message, err := c.Message(ctx, id)
	if err != nil {
		return nil, err
	}
	i := slices.IndexFunc(message.Parts, Part.isCalendar)
	if i < 0 {
		return nil, ErrNoCalendarPart
	}
	part := message.Parts[i]
	if part.Size > maxCalendarPartBytes {
		return nil, ErrCalendarPartTooLarge
	}
	raw, err := c.PartContent(ctx, id, part)
	if err != nil {
		return nil, err
	}
	if len(raw) > maxCalendarPartBytes {
		return nil, ErrCalendarPartTooLarge
	}
	return []byte(bodyText(raw, part.charset)), nil
}

// PartContent returns the decoded bytes of a part from Message. Reading does
// not mark the message as seen.
func (c *Client) PartContent(ctx context.Context, id string, part Part) ([]byte, error) {
	defer c.bind(ctx)()

	uid, err := c.open(ctx, id)
	if err != nil {
		return nil, err
	}
	section := &imap.FetchItemBodySection{Part: part.section, Peek: true}
	buf, err := c.fetchOne(ctx, uid, &imap.FetchOptions{BodySection: []*imap.FetchItemBodySection{section}})
	if err != nil {
		return nil, err
	}
	content, err := decode(buf.FindBodySection(section), part.encoding)
	if err != nil {
		return nil, fmt.Errorf("decode part %s: %w", part.Path, err)
	}
	return content, nil
}

// bodyPart picks the part to read as the message's text: plain text if there is
// any, or else HTML. A text file sent along isn't the message.
func bodyPart(parts []Part) (Part, bool) {
	for _, mediaType := range []string{"text/plain", "text/html"} {
		for _, part := range parts {
			if part.MediaType == mediaType && !part.attachment && part.Filename == "" {
				return part, true
			}
		}
	}
	return Part{}, false
}

// bodyText converts to valid UTF-8, because what a stranger's mail claims about
// its charset can be wrong, and the result has to survive JSON.
func bodyText(raw []byte, label string) string {
	switch strings.ToLower(label) {
	case "", "utf-8", "utf8", "us-ascii":
		return strings.ToValidUTF8(string(raw), "�")
	}
	reader, err := charset.Reader(label, bytes.NewReader(raw))
	if err == nil {
		if converted, err := io.ReadAll(reader); err == nil {
			return strings.ToValidUTF8(string(converted), "�")
		}
	}
	return strings.ToValidUTF8(string(raw), "�")
}

func truncateBody(text string) (string, bool) {
	if len(text) <= maxBodyBytes {
		return text, false
	}
	cut := maxBodyBytes
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut], true
}

func firstAddress(addresses []imap.Address) string {
	if formatted := formatAddresses(addresses); len(formatted) > 0 {
		return formatted[0]
	}
	return ""
}

func formatAddresses(addresses []imap.Address) []string {
	var formatted []string
	for _, address := range addresses {
		switch {
		case address.Addr() == "":
			// A group's start or end marker holds no address.
		case address.Name == "" || strings.EqualFold(address.Name, address.Addr()):
			formatted = append(formatted, address.Addr())
		default:
			formatted = append(formatted, address.Name+" <"+address.Addr()+">")
		}
	}
	return formatted
}

// isCalendar spots invites in the forms mail clients send them, including an
// attachment that only its name marks as one.
func (p Part) isCalendar() bool {
	return p.MediaType == "text/calendar" || p.MediaType == "application/ics" ||
		strings.HasSuffix(strings.ToLower(p.Filename), ".ics")
}

func messageParts(structure imap.BodyStructure) []Part {
	parts := []Part{}
	if structure == nil {
		return parts
	}
	structure.Walk(func(path []int, part imap.BodyStructure) bool {
		if single, ok := part.(*imap.BodyStructureSinglePart); ok {
			disposition := single.Disposition()
			parts = append(parts, Part{
				Path:       joinPath(path),
				MediaType:  single.MediaType(),
				Size:       single.Size,
				Filename:   single.Filename(),
				section:    path,
				encoding:   single.Encoding,
				charset:    single.Params["charset"],
				attachment: disposition != nil && strings.EqualFold(disposition.Value, "attachment"),
			})
		}
		return true
	})
	return parts
}

func joinPath(path []int) string {
	parts := make([]string, len(path))
	for i, n := range path {
		parts[i] = strconv.Itoa(n)
	}
	return strings.Join(parts, ".")
}

func decode(raw []byte, encoding string) ([]byte, error) {
	reader := bytes.NewReader(raw)
	switch strings.ToLower(encoding) {
	case "base64":
		return io.ReadAll(base64.NewDecoder(base64.StdEncoding, reader))
	case "quoted-printable":
		return io.ReadAll(quotedprintable.NewReader(reader))
	case "", "7bit", "8bit", "binary":
		return raw, nil
	default:
		return nil, fmt.Errorf("unsupported encoding %q", encoding)
	}
}
