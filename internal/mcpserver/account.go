package mcpserver

import (
	"context"
	"errors"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/robinvdvleuten/pim-mcp/internal/account"
	"github.com/robinvdvleuten/pim-mcp/internal/ical"
)

// AccountArg is embedded in every tool's input, so each takes the same optional
// account. It is the one place the argument is described.
type AccountArg struct {
	Account string `json:"account,omitempty" jsonschema:"email address of the account; for an id, the account it came from; defaults to the first account"`
}

// AccountEntry is one configured account, as account_info lists it.
type AccountEntry struct {
	Email   string `json:"email"`
	Default bool   `json:"default"`
}

// AccountInfo is what account_info returns. It holds no secrets.
type AccountInfo struct {
	Email        string `json:"email"`
	Username     string `json:"username"`
	IMAPAddress  string `json:"imap_address"`
	IMAPSecurity string `json:"imap_security"`
	CalDAVURL    string `json:"caldav_url"`
	ReadOnly     bool   `json:"read_only"`
	// Timezone is the user's IANA zone, empty when it isn't known.
	Timezone string `json:"timezone"`
	// Accounts lists every configured address, so an agent can name another.
	Accounts []AccountEntry `json:"accounts"`
}

// errNoAccounts is for a server built without any way to find accounts.
var errNoAccounts = errors.New("accounts are not available")

// accounts returns the configured accounts.
func (s *Server) accounts() ([]account.Account, error) {
	if s.options.Accounts == nil {
		return nil, errNoAccounts
	}
	return s.options.Accounts()
}

// resolve turns the account argument into the configured email address to dial:
// the default for an empty one, an error that lists the addresses for an unknown
// one. This is the only place an agent's choice of account is checked.
func (s *Server) resolve(requested string) (string, error) {
	if s.options.Accounts == nil {
		// A server built around one fixed connection has nothing to choose from.
		if requested != "" {
			return "", errNoAccounts
		}
		return "", nil
	}
	accounts, err := s.options.Accounts()
	if err != nil {
		return "", err
	}
	chosen, err := account.Select(accounts, requested)
	if err != nil {
		return "", err
	}
	return chosen.Email, nil
}

func (s *Server) accountInfo(_ context.Context, _ *mcp.CallToolRequest, in AccountArg) (*mcp.CallToolResult, AccountInfo, error) {
	accounts, err := s.accounts()
	if err != nil {
		return nil, AccountInfo{}, err
	}
	a, err := account.Select(accounts, in.Account)
	if err != nil {
		return nil, AccountInfo{}, err
	}
	entries := make([]AccountEntry, len(accounts))
	for i, other := range accounts {
		entries[i] = AccountEntry{Email: other.Email, Default: i == 0}
	}
	return nil, AccountInfo{
		Email:        a.Email,
		Username:     a.Username,
		IMAPAddress:  a.IMAP.Addr,
		IMAPSecurity: a.IMAP.Security,
		CalDAVURL:    a.CalDAV.URL,
		ReadOnly:     s.options.ReadOnly,
		Timezone:     ical.ZoneName(s.zone()),
		Accounts:     entries,
	}, nil
}
