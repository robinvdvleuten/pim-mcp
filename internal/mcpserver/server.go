// Package mcpserver exposes pim-mcp's capabilities as MCP tools.
package mcpserver

import (
	"context"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/robinvdvleuten/pim-mcp/internal/account"
	"github.com/robinvdvleuten/pim-mcp/internal/calendar"
	"github.com/robinvdvleuten/pim-mcp/internal/ical"
	"github.com/robinvdvleuten/pim-mcp/internal/mail"
)

// Options configures the server.
type Options struct {
	// Version is reported to clients during initialize.
	Version string
	// Account returns the configured account. It runs on each call, so a user
	// who adds an account while the server runs doesn't have to restart it, and
	// an agent gets told how to add one instead of finding a server that won't
	// start.
	Account func() (*account.Account, error)
	// ReadOnly keeps the server from changing anything.
	ReadOnly bool
	// DialMail opens a logged-in mail connection, for one tool call.
	DialMail func(context.Context) (*mail.Client, error)
	// DialCalendar returns a calendar client, for one tool call.
	DialCalendar func() (*calendar.Client, error)
	// Zone is the user's zone, for floating times and all-day events. It defaults
	// to this machine's.
	Zone *time.Location
}

// Server holds what the tools share.
type Server struct {
	options Options
}

// New returns the MCP server.
func New(options Options) *mcp.Server {
	if options.Zone == nil {
		options.Zone = ical.LocalZone()
	}
	s := &Server{options: options}
	server := mcp.NewServer(&mcp.Implementation{Name: "pim-mcp", Version: options.Version}, nil)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "account_info",
		Description: "Show the configured account's email address, username, server endpoints, and whether the server is read-only.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, s.accountInfo)
	s.registerMail(server)
	s.registerCalendar(server)
	return server
}
