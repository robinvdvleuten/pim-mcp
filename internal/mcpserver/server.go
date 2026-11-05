// Package mcpserver exposes pim-mcp's capabilities as MCP tools.
package mcpserver

import "github.com/modelcontextprotocol/go-sdk/mcp"

// New returns the MCP server, reporting version to clients during initialize.
func New(version string) *mcp.Server {
	return mcp.NewServer(&mcp.Implementation{Name: "pim-mcp", Version: version}, nil)
}
