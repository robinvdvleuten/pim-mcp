// Package account stores the user's accounts: their endpoints in a config file
// and their passwords in the OS keychain.
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
	"slices"
	"strings"

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
	// The config file holds no secrets, and account_info shows this URL.
	if u.User != nil {
		return errors.New("CalDAV URL must not contain a username or password")
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

// configFile holds a list of accounts. The first is the default.
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

// ErrDuplicate means an account with that email address exists already.
var ErrDuplicate = errors.New("an account with that email address already exists")

// List returns every configured account, the default first, or ErrNoAccount if
// there is none.
func (s *Store) List() ([]Account, error) {
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
	return file.Accounts, nil
}

// Load returns the default account, or ErrNoAccount if there is none.
func (s *Store) Load() (*Account, error) {
	accounts, err := s.List()
	if err != nil {
		return nil, err
	}
	return &accounts[0], nil
}

// Emails lists the addresses of accounts, for messages that name them.
func Emails(accounts []Account) []string {
	emails := make([]string, len(accounts))
	for i, a := range accounts {
		emails[i] = a.Email
	}
	return emails
}

func errUnknown(accounts []Account, email string) error {
	return fmt.Errorf("no account %q, the configured accounts are: %s", email, strings.Join(Emails(accounts), ", "))
}

func indexOf(accounts []Account, email string) int {
	return slices.IndexFunc(accounts, func(a Account) bool { return strings.EqualFold(a.Email, email) })
}

// Select returns the account with that email address, in any letter case, or
// the default account for an empty one. An unknown address is an error that
// names the configured ones.
func Select(accounts []Account, email string) (Account, error) {
	if len(accounts) == 0 {
		return Account{}, ErrNoAccount
	}
	if email == "" {
		return accounts[0], nil
	}
	i := indexOf(accounts, email)
	if i < 0 {
		return Account{}, errUnknown(accounts, email)
	}
	return accounts[i], nil
}

// Add appends an account, so the first one stays the default. A second account
// with the same email address, in any letter case, is refused.
func (s *Store) Add(a Account) error {
	accounts, err := s.List()
	if err != nil && !errors.Is(err, ErrNoAccount) {
		return err
	}
	if indexOf(accounts, a.Email) >= 0 {
		return fmt.Errorf("%w: %s", ErrDuplicate, a.Email)
	}
	return s.write(append(accounts, a))
}

// Remove deletes the account with that email address. When it was the last one,
// the file goes too. It returns the account as it was stored.
func (s *Store) Remove(email string) (Account, error) {
	accounts, err := s.List()
	if err != nil {
		return Account{}, err
	}
	i := indexOf(accounts, email)
	if i < 0 {
		return Account{}, errUnknown(accounts, email)
	}
	removed := accounts[i]
	if len(accounts) == 1 {
		if err := os.Remove(s.path()); err != nil {
			return Account{}, fmt.Errorf("remove config: %w", err)
		}
		return removed, nil
	}
	return removed, s.write(slices.Delete(slices.Clone(accounts), i, i+1))
}

// SetDefault moves the account with that email address to the front.
func (s *Store) SetDefault(email string) (Account, error) {
	accounts, err := s.List()
	if err != nil {
		return Account{}, err
	}
	i := indexOf(accounts, email)
	if i < 0 {
		return Account{}, errUnknown(accounts, email)
	}
	chosen := accounts[i]
	rest := slices.Delete(slices.Clone(accounts), i, i+1)
	return chosen, s.write(append([]Account{chosen}, rest...))
}

// write replaces the file in one step, so a failure can't leave a half-written
// config behind.
func (s *Store) write(accounts []Account) error {
	data, err := json.MarshalIndent(configFile{Accounts: accounts}, "", "  ")
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
