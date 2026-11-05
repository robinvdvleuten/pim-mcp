package mcpserver

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// AccountInfo is what account_info returns. It holds no secrets.
type AccountInfo struct {
	Email        string `json:"email"`
	Username     string `json:"username"`
	IMAPAddress  string `json:"imap_address"`
	IMAPSecurity string `json:"imap_security"`
	CalDAVURL    string `json:"caldav_url"`
	ReadOnly     bool   `json:"read_only"`
}

func (s *Server) accountInfo(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, AccountInfo, error) {
	a, err := s.options.Account()
	if err != nil {
		return nil, AccountInfo{}, err
	}
	return nil, AccountInfo{
		Email:        a.Email,
		Username:     a.Username,
		IMAPAddress:  a.IMAP.Addr,
		IMAPSecurity: a.IMAP.Security,
		CalDAVURL:    a.CalDAV.URL,
		ReadOnly:     s.options.ReadOnly,
	}, nil
}
