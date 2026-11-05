// Package mcpserver exposes pim-mcp's capabilities as MCP tools.
package mcpserver

import (
	"context"
	"slices"
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
	// Tools narrows the tools to these names, if set. Read-only mode still applies.
	Tools []string
	// Zone is the user's zone, for floating times and all-day events. It defaults
	// to this machine's.
	Zone *time.Location
}

// Server holds what the tools share.
type Server struct {
	options Options
	mcp     *mcp.Server
	known   []string
}

// New returns the MCP server.
func New(options Options) *mcp.Server {
	if options.Zone == nil {
		options.Zone = ical.LocalZone()
	}
	return newServer(options).mcp
}

func newServer(options Options) *Server {
	s := &Server{
		options: options,
		mcp:     mcp.NewServer(&mcp.Implementation{Name: "pim-mcp", Version: options.Version}, nil),
	}
	addTool(s, &mcp.Tool{
		Name:        "account_info",
		Description: "Show the configured account's email address, username, server endpoints, and whether the server is read-only.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, s.accountInfo)
	s.registerMail()
	s.registerCalendar()
	return s
}

// KnownTools names every tool the server can offer.
func KnownTools() []string {
	return newServer(Options{}).known
}

// addTool is the one way a tool gets registered, so read-only mode can't be
// forgotten for a new tool: without a read-only annotation, it counts as a write.
func addTool[In, Out any](s *Server, tool *mcp.Tool, handler mcp.ToolHandlerFor[In, Out]) {
	s.known = append(s.known, tool.Name)
	if s.options.ReadOnly && (tool.Annotations == nil || !tool.Annotations.ReadOnlyHint) {
		return
	}
	if s.options.Tools != nil && !slices.Contains(s.options.Tools, tool.Name) {
		return
	}
	mcp.AddTool(s.mcp, tool, handler)
}
