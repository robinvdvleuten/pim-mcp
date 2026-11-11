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
	"regexp"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/term"

	"github.com/robinvdvleuten/pim-mcp/internal/account"
	"github.com/robinvdvleuten/pim-mcp/internal/calendar"
	"github.com/robinvdvleuten/pim-mcp/internal/mail"
	"github.com/robinvdvleuten/pim-mcp/internal/mcpserver"
)

const usage = `usage:
  pim-mcp account add --email ADDRESS [--username NAME] [--imap HOST:PORT]
                      [--imap-security tls|starttls|none] [--caldav URL]
                      [--no-ispdb]
  pim-mcp account show                               list the accounts; the first is the default
  pim-mcp account test [--email ADDRESS]             log in to all accounts, or one
  pim-mcp account remove [--email ADDRESS]           --email is needed with several accounts
  pim-mcp account default --email ADDRESS            make an account the default
  pim-mcp serve [--read-only] [--tools NAME,NAME]    run the MCP server over stdio
  pim-mcp version                                    print the build revision
`

const (
	readOnlyEnv = "PIM_MCP_READ_ONLY"
	debugEnv    = "PIM_MCP_DEBUG"
)

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

	// dialIMAP opens a mail connection. It is mail.Dial unless a test replaces it.
	dialIMAP func(context.Context, mail.Config) (*mail.Client, error)

	// calendarClients holds each account's last calendar client, by email
	// address, with the config it was built from. See dialCalendar.
	calendarMu      sync.Mutex
	calendarClients map[string]cachedCalendar
}

type cachedCalendar struct {
	config calendar.Config
	client *calendar.Client
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
	tools := flags.String("tools", "", "")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		_, _ = fmt.Fprint(c.stderr, usage)
		return 2
	}

	allowed, err := parseTools(*tools)
	if err != nil {
		return c.fail(err)
	}

	options := mcpserver.Options{
		Tools:        allowed,
		Version:      version,
		ReadOnly:     readOnly,
		Accounts:     c.loadAccounts,
		DialMail:     c.dialMail,
		DialCalendar: c.dialCalendar,
	}
	if err := serve(ctx, options, c.stderr); err != nil {
		return c.fail(err)
	}
	return 0
}

// parseTools refuses a name it doesn't know, as a typo would otherwise silently
// leave a tool out.
func parseTools(value string) ([]string, error) {
	if value == "" {
		return nil, nil
	}
	known := mcpserver.KnownTools()
	names := strings.Split(value, ",")
	for _, name := range names {
		if !slices.Contains(known, name) {
			return nil, fmt.Errorf("--tools: unknown tool %q, known tools are: %s", name, strings.Join(known, ", "))
		}
	}
	return names, nil
}

func (c *cli) loadAccounts() ([]account.Account, error) {
	if c.store == nil {
		return nil, errNoConfigDir
	}
	return c.store.List()
}

// dialMail connects to the mail server of the account with that email address,
// with that account's username and password and no other's.
func (c *cli) dialMail(ctx context.Context, email string) (*mail.Client, error) {
	a, password, _, err := c.credentials(email)
	if err != nil {
		return nil, err
	}
	dial := c.dialIMAP
	if dial == nil {
		dial = mail.Dial
	}
	return dial(ctx, mailConfig(&a, password))
}

// dialCalendar reads the accounts and the password on every call, as either may
// change while the server runs. An account's client is only reused while they
// produce the same config for it, so its connections and its calendar home are
// kept across calls. The clients are kept per email address, so one account is
// never handed another's, whatever their config has in common.
func (c *cli) dialCalendar(email string) (*calendar.Client, error) {
	a, password, accounts, err := c.credentials(email)
	if err != nil {
		return nil, err
	}
	config := calendarConfig(&a, password)

	c.calendarMu.Lock()
	defer c.calendarMu.Unlock()
	// Forget removed accounts, so the map can't grow without bound.
	for key := range c.calendarClients {
		if !slices.Contains(account.Emails(accounts), key) {
			delete(c.calendarClients, key)
		}
	}
	if kept, ok := c.calendarClients[a.Email]; ok && kept.config == config {
		return kept.client, nil
	}
	client, err := calendar.New(config)
	if err != nil {
		return nil, err
	}
	if c.calendarClients == nil {
		c.calendarClients = map[string]cachedCalendar{}
	}
	c.calendarClients[a.Email] = cachedCalendar{config: config, client: client}
	return client, nil
}

// credentials finds the account with that email address (the default for an
// empty one) and its password. It also returns every account.
func (c *cli) credentials(email string) (account.Account, string, []account.Account, error) {
	accounts, err := c.loadAccounts()
	if err != nil {
		return account.Account{}, "", nil, err
	}
	a, err := account.Select(accounts, email)
	if err != nil {
		return account.Account{}, "", nil, err
	}
	password, _, err := account.Password(a.Email, len(accounts))
	if err != nil {
		return account.Account{}, "", nil, err
	}
	return a, password, accounts, nil
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
	level := slog.LevelInfo
	if os.Getenv(debugEnv) != "" {
		level = slog.LevelDebug
	}
	logger := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: level}))
	options.Logger = logger
	logger.Info("starting server", "version", options.Version, "read_only", options.ReadOnly)

	if err := mcpserver.New(options).Run(ctx, &mcp.StdioTransport{}); err != nil {
		return fmt.Errorf("run server: %w", err)
	}
	return nil
}

// pseudoVersion matches the module versions the go command invents for
// untagged commits, with or without a +dirty suffix. Before any tag they look
// like v0.0.0-20261105160743-0a9722d511fa, and after one like
// v0.1.1-0.20261105160743-0a9722d511fa.
var pseudoVersion = regexp.MustCompile(`[-.]\d{14}-[0-9a-f]{12}(\+dirty)?$`)

// versionString reports the release tag when the binary was built from one,
// either by `go install module@version` or from a tagged checkout. Otherwise it
// reports the VCS revision. The go command stamps untagged builds with a
// pseudo-version, which says less than the full revision, so it only serves as
// a last resort for binaries without VCS settings.
func versionString(info *debug.BuildInfo) string {
	if info == nil {
		return "unknown"
	}

	module := info.Main.Version
	if module == "(devel)" {
		module = ""
	}
	if module != "" && !pseudoVersion.MatchString(module) {
		return module
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
	case module != "":
		return module
	default:
		return "unknown"
	}
}
