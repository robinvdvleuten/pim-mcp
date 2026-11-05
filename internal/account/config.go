// Package account stores the user's account: its endpoints in a config file
// and its password in the OS keychain.
package account

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/mail"
	"net/url"
	"os"
	"path/filepath"

	"github.com/robinvdvleuten/pim-mcp/internal/transport"
)

// ErrNoAccount means no account has been configured yet.
var ErrNoAccount = errors.New("no account configured, run: pim-mcp account add")

// IMAP security modes.
const (
	SecurityTLS      = "tls"
	SecurityStartTLS = "starttls"
	SecurityNone     = "none"
)

const implicitTLSPort = "993"

// Account describes one person's services. It never holds a secret.
type Account struct {
	Email    string `json:"email"`
	Username string `json:"username"`
	IMAP     IMAP   `json:"imap"`
	CalDAV   CalDAV `json:"caldav"`
}

// IMAP says how to reach the mail server.
type IMAP struct {
	Addr     string `json:"addr"`
	Security string `json:"security"`
}

// CalDAV says how to reach the calendar server.
type CalDAV struct {
	URL string `json:"url"`
}

// Validate checks that the account is complete and that its connections would
// be encrypted, so a bad account is refused before anything is saved.
func (a Account) Validate() error {
	address, err := mail.ParseAddress(a.Email)
	if err != nil || address.Address != a.Email {
		return fmt.Errorf("email %q is not a plain email address", a.Email)
	}
	if a.Username == "" {
		return errors.New("username is required")
	}

	host, _, err := net.SplitHostPort(a.IMAP.Addr)
	if err != nil || host == "" {
		return fmt.Errorf("IMAP address %q must look like host:port", a.IMAP.Addr)
	}
	switch a.IMAP.Security {
	case SecurityTLS, SecurityStartTLS:
	case SecurityNone:
		if !transport.IsLoopback(host) {
			return fmt.Errorf("plain IMAP to %s: %w", host, transport.ErrInsecure)
		}
	default:
		return fmt.Errorf("IMAP security %q must be %s, %s or %s", a.IMAP.Security, SecurityTLS, SecurityStartTLS, SecurityNone)
	}

	u, err := url.Parse(a.CalDAV.URL)
	if err != nil || u.Hostname() == "" {
		return errors.New("CalDAV URL must look like https://host/path")
	}
	return transport.CheckURL(u)
}

// DefaultIMAPSecurity picks the mode a port conventionally uses. Plain IMAP is
// never the default.
func DefaultIMAPSecurity(addr string) string {
	if _, port, err := net.SplitHostPort(addr); err == nil && port == implicitTLSPort {
		return SecurityTLS
	}
	return SecurityStartTLS
}

// configFile holds a list so that adding accounts later needs no migration.
type configFile struct {
	Accounts []Account `json:"accounts"`
}

// Store reads and writes the config file in one directory.
type Store struct {
	dir string
}

// NewStore returns a store that keeps its file in dir.
func NewStore(dir string) *Store {
	return &Store{dir: dir}
}

// DefaultStore returns the store in the user's config directory.
func DefaultStore() (*Store, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return nil, fmt.Errorf("find config directory: %w", err)
	}
	return NewStore(filepath.Join(base, "pim-mcp")), nil
}

func (s *Store) path() string {
	return filepath.Join(s.dir, "config.json")
}

// Load returns the configured account, or ErrNoAccount if there is none.
func (s *Store) Load() (*Account, error) {
	data, err := os.ReadFile(s.path())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrNoAccount
	}
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var file configFile
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("parse %s: %w", s.path(), err)
	}
	if len(file.Accounts) == 0 {
		return nil, ErrNoAccount
	}
	return &file.Accounts[0], nil
}

// Save writes the account. The file is replaced in one step, so a failure
// can't leave a half-written config behind.
func (s *Store) Save(a Account) error {
	data, err := json.MarshalIndent(configFile{Accounts: []Account{a}}, "", "  ")
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	// MkdirAll leaves an existing directory's permissions as they are.
	if err := os.Chmod(s.dir, 0o700); err != nil { //nolint:gosec // A directory needs its execute bit.
		return fmt.Errorf("protect config directory: %w", err)
	}

	temp, err := os.CreateTemp(s.dir, ".config-*.json") // Created with mode 0600.
	if err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	defer func() { _ = os.Remove(temp.Name()) }()
	_, err = temp.Write(append(data, '\n'))
	if closeErr := temp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	if err := os.Rename(temp.Name(), s.path()); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	return nil
}

// Remove deletes the config file.
func (s *Store) Remove() error {
	err := os.Remove(s.path())
	if errors.Is(err, fs.ErrNotExist) {
		return ErrNoAccount
	}
	if err != nil {
		return fmt.Errorf("remove config: %w", err)
	}
	return nil
}
