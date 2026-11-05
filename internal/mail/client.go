// Package mail reads messages over IMAP. It never changes a mailbox.
package mail

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime/quotedprintable"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"

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

// Client is a logged-in IMAP connection. It is not safe for concurrent use.
type Client struct {
	conn net.Conn
	imap *imapclient.Client
}

// Part describes one leaf of a message's MIME structure.
type Part struct {
	Path      string
	MediaType string
	Size      uint32

	section  []int
	encoding string
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

	var client *imapclient.Client
	if cfg.Security == SecurityStartTLS {
		client, err = imapclient.NewStartTLS(conn, &imapclient.Options{TLSConfig: tlsConfig})
		if err != nil {
			_ = conn.Close()
			return nil, fmt.Errorf("start TLS with %s: %w", cfg.Addr, contextError(ctx, err))
		}
	} else {
		client = imapclient.New(conn, nil)
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

// Folders returns the name of every mailbox.
func (c *Client) Folders(ctx context.Context) ([]string, error) {
	defer c.bind(ctx)()

	mailboxes, err := c.imap.List("", "*", nil).Collect()
	if err != nil {
		return nil, fmt.Errorf("list folders: %w", contextError(ctx, err))
	}
	names := make([]string, 0, len(mailboxes))
	for _, mailbox := range mailboxes {
		names = append(names, mailbox.Mailbox)
	}
	return names, nil
}

// SearchSubject returns the UIDs of messages whose subject contains text.
func (c *Client) SearchSubject(ctx context.Context, folder, text string) ([]uint32, error) {
	defer c.bind(ctx)()

	if err := c.examine(ctx, folder); err != nil {
		return nil, err
	}
	data, err := c.imap.UIDSearch(&imap.SearchCriteria{
		Header: []imap.SearchCriteriaHeaderField{{Key: "Subject", Value: text}},
	}, nil).Wait()
	if err != nil {
		return nil, fmt.Errorf("search %s: %w", folder, contextError(ctx, err))
	}
	uids := make([]uint32, 0, len(data.AllUIDs()))
	for _, uid := range data.AllUIDs() {
		uids = append(uids, uint32(uid))
	}
	return uids, nil
}

// Message returns a message's headers and MIME structure.
func (c *Client) Message(ctx context.Context, folder string, uid uint32) (*Message, error) {
	defer c.bind(ctx)()

	if err := c.examine(ctx, folder); err != nil {
		return nil, err
	}
	buf, err := c.fetchOne(ctx, uid, &imap.FetchOptions{
		Envelope:      true,
		BodyStructure: &imap.FetchItemBodyStructure{Extended: true},
	})
	if err != nil {
		return nil, err
	}

	msg := &Message{UID: uid, Subject: buf.Envelope.Subject, Date: buf.Envelope.Date}
	if len(buf.Envelope.From) > 0 {
		msg.From = buf.Envelope.From[0].Addr()
	}
	buf.BodyStructure.Walk(func(path []int, part imap.BodyStructure) bool {
		if single, ok := part.(*imap.BodyStructureSinglePart); ok {
			msg.Parts = append(msg.Parts, Part{
				Path:      joinPath(path),
				MediaType: single.MediaType(),
				Size:      single.Size,
				section:   path,
				encoding:  single.Encoding,
			})
		}
		return true
	})
	return msg, nil
}

// PartContent returns the decoded bytes of a part from Message. Reading does
// not mark the message as seen.
func (c *Client) PartContent(ctx context.Context, folder string, uid uint32, part Part) ([]byte, error) {
	defer c.bind(ctx)()

	if err := c.examine(ctx, folder); err != nil {
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

// bind closes the connection if ctx ends first, and returns a function that
// releases this. A client whose context ended cannot be used again.
func (c *Client) bind(ctx context.Context) func() {
	stop := context.AfterFunc(ctx, func() { _ = c.conn.Close() })
	return func() { stop() }
}

// examine opens a folder read-only, which guards against accidental changes.
func (c *Client) examine(ctx context.Context, folder string) error {
	if _, err := c.imap.Select(folder, &imap.SelectOptions{ReadOnly: true}).Wait(); err != nil {
		return fmt.Errorf("open %s: %w", folder, contextError(ctx, err))
	}
	return nil
}

func (c *Client) fetchOne(ctx context.Context, uid uint32, options *imap.FetchOptions) (*imapclient.FetchMessageBuffer, error) {
	msgs, err := c.imap.Fetch(imap.UIDSetNum(imap.UID(uid)), options).Collect()
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
