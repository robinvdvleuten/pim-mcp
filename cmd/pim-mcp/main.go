// Command pim-mcp is a local MCP server for mail and calendar access.
package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/robinvdvleuten/pim-mcp/internal/mcpserver"
)

const usage = `usage:
  pim-mcp serve      run the MCP server over stdio
  pim-mcp version    print the build revision
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		_, _ = fmt.Fprint(stderr, usage)
		return 2
	}

	info, _ := debug.ReadBuildInfo()
	version := versionString(info)

	switch args[0] {
	case "version":
		if _, err := fmt.Fprintf(stdout, "pim-mcp %s\n", version); err != nil {
			return 1
		}
		return 0
	case "serve":
		if err := serve(ctx, version, stderr); err != nil {
			_, _ = fmt.Fprintf(stderr, "pim-mcp: %v\n", err)
			return 1
		}
		return 0
	default:
		_, _ = fmt.Fprint(stderr, usage)
		return 2
	}
}

// serve blocks until the client disconnects. Stdout belongs to the MCP
// transport, so logs go to stderr.
func serve(ctx context.Context, version string, stderr io.Writer) error {
	logger := slog.New(slog.NewTextHandler(stderr, nil))
	logger.Info("starting server", "version", version)

	if err := mcpserver.New(version).Run(ctx, &mcp.StdioTransport{}); err != nil {
		return fmt.Errorf("run server: %w", err)
	}
	return nil
}

// versionString prefers the VCS revision because the project has no tags.
// Binaries from `go install` carry no VCS settings, only a module version.
func versionString(info *debug.BuildInfo) string {
	if info == nil {
		return "unknown"
	}

	var revision string
	var modified bool
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			revision = setting.Value
		case "vcs.modified":
			modified = setting.Value == "true"
		}
	}

	switch {
	case revision != "" && modified:
		return revision + "-dirty"
	case revision != "":
		return revision
	case info.Main.Version != "" && info.Main.Version != "(devel)":
		return info.Main.Version
	default:
		return "unknown"
	}
}
