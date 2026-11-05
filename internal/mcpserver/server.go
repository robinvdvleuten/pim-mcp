// Package mcpserver exposes pim-mcp's capabilities as MCP tools.
package mcpserver

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
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
	// Logger receives one record per request at debug level. Tool arguments and
	// results aren't in it, as those hold people's mail and calendars.
	Logger *slog.Logger
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
	s.registerImport()
	if options.Logger != nil {
		s.mcp.AddReceivingMiddleware(logRequests(options.Logger))
	}
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
	tool.InputSchema = inputSchema[In]()
	mcp.AddTool(s.mcp, tool, handler)
}

// inputSchema gives optional parameters one type. Go's pointers and slices would
// otherwise come out as ["null", "boolean"] and the like, which is valid but which
// clients handle badly. An optional parameter is simply left out, never null.
func inputSchema[In any]() *jsonschema.Schema {
	schema, err := jsonschema.For[In](nil)
	if err != nil {
		panic(fmt.Sprintf("schema for %T: %v", *new(In), err))
	}
	for _, property := range schema.Properties {
		if types := slices.DeleteFunc(property.Types, func(t string) bool { return t == "null" }); len(types) == 1 {
			property.Type, property.Types = types[0], nil
		}
	}
	return schema
}

func logRequests(logger *slog.Logger) mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			started := time.Now()
			result, err := next(ctx, method, req)
			attrs := []any{"method", method, "duration", time.Since(started).Round(time.Millisecond), "failed", err != nil}
			if call, ok := req.(*mcp.CallToolRequest); ok && call.Params != nil {
				attrs = append(attrs, "tool", call.Params.Name)
			}
			if call, ok := result.(*mcp.CallToolResult); ok && call != nil {
				attrs = append(attrs, "tool_error", call.IsError)
			}
			logger.Debug("request", attrs...)
			return result, err
		}
	}
}
