// Command pim-mcp is a local MCP server for mail and calendar access.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"runtime/debug"
	"strconv"
	"strings"
	"syscall"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/term"

	"github.com/robinvdvleuten/pim-mcp/internal/account"
	"github.com/robinvdvleuten/pim-mcp/internal/mail"
	"github.com/robinvdvleuten/pim-mcp/internal/mcpserver"
)

const usage = `usage:
  pim-mcp account add --email ADDRESS [--username NAME] [--imap HOST:PORT]
                      [--imap-security tls|starttls|none] [--caldav URL]
                      [--no-ispdb]
  pim-mcp account show
  pim-mcp account test
  pim-mcp account remove
  pim-mcp serve [--read-only]    run the MCP server over stdio
  pim-mcp version                print the build revision
`

const readOnlyEnv = "PIM_MCP_READ_ONLY"

var errNoConfigDir = errors.New("can't find the config directory, is HOME set?")

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

// cli holds what commands reach for outside themselves, so tests can replace it.
type cli struct {
	stdout, stderr io.Writer
	store          *account.Store
	discoverer     discoverer
	checkIMAP      func(context.Context, *account.Account, string) error
	checkCalDAV    func(context.Context, *account.Account, string) error
	readPassword   func() (string, error)
	confirm        func() (bool, error)
}

type discoverer interface {
	DiscoverIMAP(ctx context.Context, email string, useISPDB bool) (*account.FoundIMAP, error)
	DiscoverCalDAV(ctx context.Context, email string) (*account.FoundCalDAV, error)
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	c := &cli{
		stdout:      stdout,
		stderr:      stderr,
		discoverer:  &account.Discoverer{},
		checkIMAP:   checkIMAP,
		checkCalDAV: checkCalDAV,
	}
	c.readPassword = c.promptPassword
	c.confirm = c.promptConfirm
	// A missing home directory only matters to the commands that need it.
	c.store, _ = account.DefaultStore()
	return c.run(ctx, args)
}

func (c *cli) run(ctx context.Context, args []string) int {
	if len(args) == 0 {
		_, _ = fmt.Fprint(c.stderr, usage)
		return 2
	}

	info, _ := debug.ReadBuildInfo()
	version := versionString(info)

	switch {
	case args[0] == "account":
		if c.store == nil {
			return c.fail(errNoConfigDir)
		}
		return c.runAccount(ctx, args[1:])
	case args[0] == "version" && len(args) == 1:
		if _, err := fmt.Fprintf(c.stdout, "pim-mcp %s\n", version); err != nil {
			return 1
		}
		return 0
	case args[0] == "serve":
		return c.runServe(ctx, args[1:], version)
	default:
		_, _ = fmt.Fprint(c.stderr, usage)
		return 2
	}
}

func (c *cli) fail(err error) int {
	_, _ = fmt.Fprintf(c.stderr, "pim-mcp: %v\n", err)
	return 1
}

// promptConfirm asks before a password goes to servers that DNS or a website
// named. Anything but an explicit yes declines.
func (c *cli) promptConfirm() (bool, error) {
	_, _ = fmt.Fprint(c.stderr, "Send your password to these servers? [y/N] ")
	answer, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return false, err
	}
	answer = strings.ToLower(strings.TrimSpace(answer))
	return answer == "y" || answer == "yes", nil
}

// promptPassword reads from the terminal without echoing, and prompts on stderr
// to leave stdout to the command's output.
func (c *cli) promptPassword() (string, error) {
	_, _ = fmt.Fprint(c.stderr, "Password: ")
	password, err := term.ReadPassword(int(os.Stdin.Fd())) //nolint:gosec // File descriptors fit in an int.
	_, _ = fmt.Fprintln(c.stderr)
	if err != nil {
		return "", err
	}
	return string(password), nil
}

func (c *cli) runServe(ctx context.Context, args []string, version string) int {
	readOnly, err := readOnlyFromEnv()
	if err != nil {
		return c.fail(err)
	}
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.BoolVar(&readOnly, "read-only", readOnly, "")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		_, _ = fmt.Fprint(c.stderr, usage)
		return 2
	}

	options := mcpserver.Options{
		Version:  version,
		ReadOnly: readOnly,
		Account:  c.loadAccount,
		DialMail: c.dialMail,
	}
	if err := serve(ctx, options, c.stderr); err != nil {
		return c.fail(err)
	}
	return 0
}

func (c *cli) loadAccount() (*account.Account, error) {
	if c.store == nil {
		return nil, errNoConfigDir
	}
	return c.store.Load()
}

func (c *cli) dialMail(ctx context.Context) (*mail.Client, error) {
	a, err := c.loadAccount()
	if err != nil {
		return nil, err
	}
	password, _, err := account.Password(a.Email)
	if err != nil {
		return nil, err
	}
	return mail.Dial(ctx, mailConfig(a, password))
}

// readOnlyFromEnv refuses a value it can't read, rather than guess which way
// someone meant to set a safety switch.
func readOnlyFromEnv() (bool, error) {
	value := os.Getenv(readOnlyEnv)
	if value == "" {
		return false, nil
	}
	readOnly, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("%s must be 1 or 0, not %q", readOnlyEnv, value)
	}
	return readOnly, nil
}

// serve blocks until the client disconnects. Stdout belongs to the MCP
// transport, so logs go to stderr.
func serve(ctx context.Context, options mcpserver.Options, stderr io.Writer) error {
	logger := slog.New(slog.NewTextHandler(stderr, nil))
	logger.Info("starting server", "version", options.Version, "read_only", options.ReadOnly)

	if err := mcpserver.New(options).Run(ctx, &mcp.StdioTransport{}); err != nil {
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
