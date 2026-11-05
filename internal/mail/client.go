// Package mail reads messages over IMAP. It never changes a mailbox.
package mail

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/quotedprintable"
	"net"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-message/charset"

	"github.com/robinvdvleuten/pim-mcp/internal/transport"
)

// Security selects how the connection is protected. The zero value is TLS.
type Security int

// Supported transports.
const (
	SecurityTLS Security = iota
	SecurityStartTLS
	SecurityNone
)

// Config holds what is needed to reach and log in to an IMAP server.
type Config struct {
	Addr     string
	Security Security
	Username string
	Password string
}

// Errors callers can act on.
var (
	// ErrStaleMessageID means the folder was recreated since the ID was issued,
	// so its UID may now belong to a different message.
	ErrStaleMessageID = errors.New("message ID is out of date, search again")
	// ErrBadMessageID means the ID didn't come from a search.
	ErrBadMessageID = errors.New("not a valid message ID")
)

// Folder roles, from the server's SPECIAL-USE attributes (RFC 6154).
const (
	RoleInbox   = "inbox"
	RoleSent    = "sent"
	RoleDrafts  = "drafts"
	RoleTrash   = "trash"
	RoleJunk    = "junk"
	RoleArchive = "archive"
	RoleAll     = "all"
	RoleFlagged = "flagged"
)

const (
	inbox = "INBOX"
	// searchBatch is how many messages to read at once when only reading them
	// can tell whether they match.
	searchBatch = 100
	// maxSearchScan bounds that reading on huge folders. Results then say there
	// may be more.
	maxSearchScan = 2000
)

// Client is a logged-in IMAP connection. It is not safe for concurrent use.
type Client struct {
	conn net.Conn
	imap *imapclient.Client
}

// Folder is a mailbox that can be searched.
type Folder struct {
	Name string `json:"name"`
	Role string `json:"role,omitempty"`
}

// Criteria selects messages. Every field that is set must match.
type Criteria struct {
	Folder      string // defaults to INBOX
	From        string
	To          string
	Subject     string
	Text        string
	Since       time.Time
	Before      time.Time
	HasCalendar *bool
	Limit       int
}

// Summary describes a message found by Search.
type Summary struct {
	ID          string    `json:"id"`
	From        string    `json:"from"`
	Subject     string    `json:"subject"`
	Date        time.Time `json:"date"`
	HasCalendar bool      `json:"has_calendar"`
}

// SearchResult holds a page of messages, newest arrival first. More says that
// more may match than are shown.
type SearchResult struct {
	Messages []Summary `json:"messages"`
	More     bool      `json:"more"`
}

// Part describes one leaf of a message's MIME structure.
type Part struct {
	Path      string
	MediaType string
	Size      uint32

	section  []int
	encoding string
	filename string
}

// Message holds the headers and structure of a message, without its content.
type Message struct {
	UID     uint32
	Subject string
	From    string
	Date    time.Time
	Parts   []Part
}

// Dial connects and logs in. The certificate is always verified.
func Dial(ctx context.Context, cfg Config) (*Client, error) {
	host, _, err := net.SplitHostPort(cfg.Addr)
	if err != nil {
		return nil, fmt.Errorf("parse address: %w", err)
	}
	if cfg.Security == SecurityNone && !transport.IsLoopback(host) {
		return nil, fmt.Errorf("connect to %s: %w", host, transport.ErrInsecure)
	}
	tlsConfig := &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}

	var conn net.Conn
	if cfg.Security == SecurityTLS {
		conn, err = (&tls.Dialer{Config: tlsConfig}).DialContext(ctx, "tcp", cfg.Addr)
	} else {
		conn, err = (&net.Dialer{}).DialContext(ctx, "tcp", cfg.Addr)
	}
	if err != nil {
		return nil, fmt.Errorf("connect to %s: %w", cfg.Addr, err)
	}

	// go-imap commands take no context, so ending one closes the connection.
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()

	options := &imapclient.Options{
		TLSConfig:   tlsConfig,
		WordDecoder: &mime.WordDecoder{CharsetReader: charset.Reader},
	}
	var client *imapclient.Client
	if cfg.Security == SecurityStartTLS {
		client, err = imapclient.NewStartTLS(conn, options)
		if err != nil {
			_ = conn.Close()
			return nil, fmt.Errorf("start TLS with %s: %w", cfg.Addr, contextError(ctx, err))
		}
	} else {
		client = imapclient.New(conn, options)
	}

	if err := client.Login(cfg.Username, cfg.Password).Wait(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("log in to %s: %w", cfg.Addr, contextError(ctx, err))
	}
	return &Client{conn: conn, imap: client}, nil
}

// Close logs out and closes the connection.
func (c *Client) Close() error {
	_ = c.imap.Logout().Wait()
	if err := c.imap.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
		return fmt.Errorf("close connection: %w", err)
	}
	return nil
}

// Folders returns the mailboxes that can be opened, with their roles.
func (c *Client) Folders(ctx context.Context) ([]Folder, error) {
	defer c.bind(ctx)()

	options := &imap.ListOptions{ReturnSpecialUse: c.imap.Caps().Has(imap.CapSpecialUse)}
	mailboxes, err := c.imap.List("", "*", options).Collect()
	if err != nil {
		return nil, fmt.Errorf("list folders: %w", contextError(ctx, err))
	}
	folders := []Folder{}
	for _, mailbox := range mailboxes {
		if !slices.Contains(mailbox.Attrs, imap.MailboxAttrNoSelect) {
			folders = append(folders, Folder{Name: mailbox.Mailbox, Role: folderRole(mailbox)})
		}
	}
	return folders, nil
}

func folderRole(mailbox *imap.ListData) string {
	if strings.EqualFold(mailbox.Mailbox, inbox) {
		return RoleInbox
	}
	roles := map[imap.MailboxAttr]string{
		imap.MailboxAttrSent:    RoleSent,
		imap.MailboxAttrDrafts:  RoleDrafts,
		imap.MailboxAttrTrash:   RoleTrash,
		imap.MailboxAttrJunk:    RoleJunk,
		imap.MailboxAttrArchive: RoleArchive,
		imap.MailboxAttrAll:     RoleAll,
		imap.MailboxAttrFlagged: RoleFlagged,
	}
	for _, attr := range mailbox.Attrs {
		if role, ok := roles[attr]; ok {
			return role
		}
	}
	return ""
}

// Search returns messages matching criteria, newest arrival first. The server
// narrows them down. Whether a message has a calendar part, and exact dates, can
// only be told by reading the message, which happens here in batches.
func (c *Client) Search(ctx context.Context, criteria Criteria) (*SearchResult, error) {
	defer c.bind(ctx)()

	folder := criteria.Folder
	if folder == "" {
		folder = inbox
	}
	validity, err := c.examine(ctx, folder)
	if err != nil {
		return nil, err
	}
	data, err := c.imap.UIDSearch(criteria.serverCriteria(), nil).Wait()
	if err != nil {
		return nil, fmt.Errorf("search %s: %w", folder, contextError(ctx, err))
	}
	uids := data.AllUIDs()
	slices.SortFunc(uids, func(a, b imap.UID) int { return int(int64(b) - int64(a)) })

	result := &SearchResult{Messages: []Summary{}}
	batch := searchBatch
	if !criteria.needsReading() {
		// The server already said which match, so a page is all there is to read.
		batch = max(criteria.Limit, 1)
		result.More = len(uids) > criteria.Limit
		uids = uids[:min(len(uids), criteria.Limit)]
	}
	for scanned := 0; len(uids) > 0; scanned += batch {
		if scanned >= maxSearchScan {
			result.More = true
			break
		}
		page := uids[:min(batch, len(uids))]
		uids = uids[len(page):]
		messages, err := c.imap.Fetch(imap.UIDSetNum(page...), &imap.FetchOptions{
			UID:           true,
			Envelope:      true,
			BodyStructure: &imap.FetchItemBodyStructure{Extended: true},
		}).Collect()
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", folder, contextError(ctx, err))
		}
		slices.SortFunc(messages, func(a, b *imapclient.FetchMessageBuffer) int { return int(int64(b.UID) - int64(a.UID)) })
		for _, message := range messages {
			summary := summarize(folder, validity, message)
			if !criteria.matches(summary) {
				continue
			}
			if len(result.Messages) == criteria.Limit {
				result.More = true
				return result, nil
			}
			result.Messages = append(result.Messages, summary)
		}
	}
	return result, nil
}

// serverCriteria widens date bounds by a day each way, because IMAP compares
// dates only and in each message's own zone. matches applies the exact ones.
func (criteria Criteria) serverCriteria() *imap.SearchCriteria {
	search := &imap.SearchCriteria{}
	for _, header := range []struct{ key, value string }{
		{"From", criteria.From},
		{"To", criteria.To},
		{"Subject", criteria.Subject},
	} {
		if header.value != "" {
			search.Header = append(search.Header, imap.SearchCriteriaHeaderField{Key: header.key, Value: header.value})
		}
	}
	if criteria.Text != "" {
		search.Text = []string{criteria.Text}
	}
	if !criteria.Since.IsZero() {
		search.SentSince = criteria.Since.UTC().AddDate(0, 0, -1)
	}
	if !criteria.Before.IsZero() {
		search.SentBefore = criteria.Before.UTC().AddDate(0, 0, 2)
	}
	return search
}

func (criteria Criteria) needsReading() bool {
	return criteria.HasCalendar != nil || !criteria.Since.IsZero() || !criteria.Before.IsZero()
}

func (criteria Criteria) matches(summary Summary) bool {
	if criteria.HasCalendar != nil && *criteria.HasCalendar != summary.HasCalendar {
		return false
	}
	if !criteria.Since.IsZero() && summary.Date.Before(criteria.Since) {
		return false
	}
	return criteria.Before.IsZero() || summary.Date.Before(criteria.Before)
}

func summarize(folder string, validity uint32, message *imapclient.FetchMessageBuffer) Summary {
	summary := Summary{ID: encodeID(folder, validity, message.UID)}
	if message.Envelope != nil {
		summary.Subject = message.Envelope.Subject
		summary.Date = message.Envelope.Date
		summary.From = formatFrom(message.Envelope.From)
	}
	summary.HasCalendar = slices.ContainsFunc(messageParts(message.BodyStructure), Part.isCalendar)
	return summary
}

func formatFrom(addresses []imap.Address) string {
	if len(addresses) == 0 {
		return ""
	}
	if addresses[0].Name == "" {
		return addresses[0].Addr()
	}
	return addresses[0].Name + " <" + addresses[0].Addr() + ">"
}

// isCalendar spots invites in the forms mail clients send them, including an
// attachment that only its name marks as one.
func (p Part) isCalendar() bool {
	return p.MediaType == "text/calendar" || p.MediaType == "application/ics" ||
		strings.HasSuffix(strings.ToLower(p.filename), ".ics")
}

func messageParts(structure imap.BodyStructure) []Part {
	var parts []Part
	if structure == nil {
		return parts
	}
	structure.Walk(func(path []int, part imap.BodyStructure) bool {
		if single, ok := part.(*imap.BodyStructureSinglePart); ok {
			parts = append(parts, Part{
				Path:      joinPath(path),
				MediaType: single.MediaType(),
				Size:      single.Size,
				section:   path,
				encoding:  single.Encoding,
				filename:  single.Filename(),
			})
		}
		return true
	})
	return parts
}

// Message returns a message's headers and MIME structure.
func (c *Client) Message(ctx context.Context, id string) (*Message, error) {
	defer c.bind(ctx)()

	uid, err := c.open(ctx, id)
	if err != nil {
		return nil, err
	}
	buf, err := c.fetchOne(ctx, uid, &imap.FetchOptions{
		Envelope:      true,
		BodyStructure: &imap.FetchItemBodyStructure{Extended: true},
	})
	if err != nil {
		return nil, err
	}
	return &Message{
		UID:     uint32(uid),
		Subject: buf.Envelope.Subject,
		Date:    buf.Envelope.Date,
		From:    formatFrom(buf.Envelope.From),
		Parts:   messageParts(buf.BodyStructure),
	}, nil
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

// encodeID ties a UID to its folder and UIDVALIDITY, which together are what
// identify a message over time (RFC 9051).
func encodeID(folder string, validity uint32, uid imap.UID) string {
	raw := fmt.Sprintf("%d:%d:%s", validity, uid, folder)
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func decodeID(id string) (folder string, validity uint32, uid imap.UID, err error) {
	raw, err := base64.RawURLEncoding.DecodeString(id)
	if err != nil {
		return "", 0, 0, ErrBadMessageID
	}
	parts := strings.SplitN(string(raw), ":", 3)
	if len(parts) != 3 || parts[2] == "" {
		return "", 0, 0, ErrBadMessageID
	}
	validityNumber, err := strconv.ParseUint(parts[0], 10, 32)
	if err != nil {
		return "", 0, 0, ErrBadMessageID
	}
	uidNumber, err := strconv.ParseUint(parts[1], 10, 32)
	if err != nil || uidNumber == 0 {
		return "", 0, 0, ErrBadMessageID
	}
	return parts[2], uint32(validityNumber), imap.UID(uidNumber), nil
}

// open selects the folder an ID belongs to and returns its UID, if the ID still
// means the same message.
func (c *Client) open(ctx context.Context, id string) (imap.UID, error) {
	folder, validity, uid, err := decodeID(id)
	if err != nil {
		return 0, err
	}
	current, err := c.examine(ctx, folder)
	if err != nil {
		return 0, err
	}
	if current != validity {
		return 0, ErrStaleMessageID
	}
	return uid, nil
}

// bind closes the connection if ctx ends first, and returns a function that
// releases this. A client whose context ended cannot be used again.
func (c *Client) bind(ctx context.Context) func() {
	stop := context.AfterFunc(ctx, func() { _ = c.conn.Close() })
	return func() { stop() }
}

// examine opens a folder read-only, which guards against accidental changes,
// and returns its UIDVALIDITY.
func (c *Client) examine(ctx context.Context, folder string) (uint32, error) {
	data, err := c.imap.Select(folder, &imap.SelectOptions{ReadOnly: true}).Wait()
	if err != nil {
		return 0, fmt.Errorf("open %s: %w", folder, contextError(ctx, err))
	}
	return data.UIDValidity, nil
}

func (c *Client) fetchOne(ctx context.Context, uid imap.UID, options *imap.FetchOptions) (*imapclient.FetchMessageBuffer, error) {
	msgs, err := c.imap.Fetch(imap.UIDSetNum(uid), options).Collect()
	if err != nil {
		return nil, fmt.Errorf("fetch message %d: %w", uid, contextError(ctx, err))
	}
	if len(msgs) != 1 {
		return nil, fmt.Errorf("fetch message %d: not found", uid)
	}
	return msgs[0], nil
}

// contextError prefers the context's error, because a cancelled command
// otherwise surfaces as an unhelpful closed-connection error.
func contextError(ctx context.Context, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	return err
}

func joinPath(path []int) string {
	parts := make([]string, len(path))
	for i, n := range path {
		parts[i] = strconv.Itoa(n)
	}
	return strings.Join(parts, ".")
}

func decode(raw []byte, encoding string) ([]byte, error) {
	reader := strings.NewReader(string(raw))
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
