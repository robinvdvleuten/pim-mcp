package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/robinvdvleuten/hearsay"
	"github.com/robinvdvleuten/pim-mcp/internal/account"
	"github.com/robinvdvleuten/pim-mcp/internal/calendar"
	"github.com/robinvdvleuten/pim-mcp/internal/mail"
)

const checkTimeout = 20 * time.Second

func (c *cli) runAccount(ctx context.Context, args []string) int {
	if len(args) == 0 {
		_, _ = fmt.Fprint(c.stderr, usage)
		return 2
	}
	switch args[0] {
	case "add":
		return c.accountAdd(ctx, args[1:])
	case "show":
		if len(args) != 1 {
			_, _ = fmt.Fprint(c.stderr, usage)
			return 2
		}
		return c.accountShow()
	case "test", "remove", "default":
		email, ok := c.emailFlag(args[0], args[1:])
		if !ok {
			return 2
		}
		switch args[0] {
		case "test":
			return c.accountTest(ctx, email)
		case "remove":
			return c.accountRemove(email)
		default:
			return c.accountDefault(email)
		}
	default:
		_, _ = fmt.Fprint(c.stderr, usage)
		return 2
	}
}

// emailFlag reads the one flag these commands take. It prints the usage when
// the arguments are not that.
func (c *cli) emailFlag(command string, args []string) (string, bool) {
	flags := flag.NewFlagSet("account "+command, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	email := flags.String("email", "", "")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		_, _ = fmt.Fprint(c.stderr, usage)
		return "", false
	}
	return *email, true
}

func (c *cli) accountAdd(ctx context.Context, args []string) int {
	flags := flag.NewFlagSet("account add", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	email := flags.String("email", "", "")
	username := flags.String("username", "", "")
	imap := flags.String("imap", "", "")
	imapSecurity := flags.String("imap-security", "", "")
	caldav := flags.String("caldav", "", "")
	noISPDB := flags.Bool("no-ispdb", false, "")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		_, _ = fmt.Fprint(c.stderr, usage)
		return 2
	}
	if *email == "" {
		return c.fail(errors.New("--email is required"))
	}

	existing, err := c.store.List()
	if err != nil && !errors.Is(err, account.ErrNoAccount) {
		return c.fail(err)
	}
	for _, other := range existing {
		if strings.EqualFold(other.Email, *email) {
			return c.fail(fmt.Errorf("account %s already exists, run: pim-mcp account remove --email %s", other.Email, other.Email))
		}
	}
	// The environment password must not become a second account's password.
	if err := account.CheckEnvForNewAccount(len(existing)); err != nil {
		return c.fail(fmt.Errorf("can't add a second account: %w", err))
	}

	a := account.Account{
		Email:    *email,
		Username: *username,
		IMAP:     account.IMAP{Addr: *imap, Security: *imapSecurity},
		CalDAV:   account.CalDAV{URL: *caldav},
	}
	if a.IMAP.Addr != "" && a.IMAP.Security == "" {
		a.IMAP.Security = account.DefaultIMAPSecurity(a.IMAP.Addr)
	}
	found, err := c.discover(ctx, &a, !*noISPDB)
	if err != nil {
		return c.fail(err)
	}
	if a.Username == "" {
		a.Username = a.Email
	}
	if err := a.Validate(); err != nil {
		return c.fail(err)
	}

	if found != "" {
		_, _ = fmt.Fprintf(c.stdout, "Found for %s:\n%s  username: %s\n", a.Email, found, a.Username)
		confirmed, err := c.confirm()
		if err != nil {
			return c.fail(fmt.Errorf("read answer: %w (without a terminal, pass --imap and --caldav)", err))
		}
		if !confirmed {
			return c.fail(errors.New("nothing saved"))
		}
	}

	storedPassword, err := c.storePassword(a.Email)
	if err != nil {
		return c.fail(err)
	}
	if err := c.store.Add(a); err != nil {
		// A duplicate made by a racing command owns the password in the keychain.
		if storedPassword && !errors.Is(err, account.ErrDuplicate) {
			_ = account.DeletePassword(a.Email)
		}
		return c.fail(err)
	}
	_, _ = fmt.Fprintf(c.stdout, "Added %s.\n", a.Email)
	return 0
}

// discover fills in the services the flags left out, and describes what it
// found. An explicit flag always wins.
func (c *cli) discover(ctx context.Context, a *account.Account, useISPDB bool) (string, error) {
	var found strings.Builder
	opts := hearsay.Options{SkipISPDB: !useISPDB}
	if a.IMAP.Addr == "" {
		imap, err := c.discoverer.DiscoverIMAP(ctx, a.Email, opts)
		if err != nil {
			return "", fmt.Errorf("%w, pass --imap", err)
		}
		a.IMAP = account.IMAP{Addr: imap.Addr, Security: string(imap.Security)}
		if a.Username == "" {
			a.Username = imap.Username
		}
		fmt.Fprintf(&found, "  imap:     %s (%s), from %s\n", a.IMAP.Addr, a.IMAP.Security, imap.Source)
	}
	if a.CalDAV.URL == "" {
		caldav, err := c.discoverer.DiscoverCalDAV(ctx, a.Email, opts)
		if err != nil {
			return "", fmt.Errorf("%w, pass --caldav", err)
		}
		a.CalDAV = account.CalDAV{URL: caldav.URL}
		fmt.Fprintf(&found, "  caldav:   %s, from %s\n", a.CalDAV.URL, caldav.Source)
	}
	return found.String(), nil
}

// storePassword asks for the password and puts it in the keychain, unless the
// environment already supplies it. It reports whether it stored anything.
func (c *cli) storePassword(email string) (bool, error) {
	if os.Getenv(account.PasswordEnv) != "" {
		return false, nil
	}
	password, err := c.readPassword()
	if err != nil {
		return false, fmt.Errorf("read password: %w (without a terminal, set %s)", err, account.PasswordEnv)
	}
	if password == "" {
		return false, errors.New("the password is empty")
	}
	if err := account.SavePassword(email, password); err != nil {
		return false, fmt.Errorf("%w (without a keychain, set %s)", err, account.PasswordEnv)
	}
	return true, nil
}

// accountShow lists every account and marks the default. With one account
// there is nothing to mark, so the output is the plain block.
func (c *cli) accountShow() int {
	accounts, err := c.store.List()
	if err != nil {
		return c.fail(err)
	}
	for i, a := range accounts {
		marker := ""
		if len(accounts) > 1 && i == 0 {
			marker = " (default)"
		}
		if i > 0 {
			_, _ = fmt.Fprintln(c.stdout)
		}
		_, err = fmt.Fprintf(c.stdout, "email:    %s%s\nusername: %s\nimap:     %s (%s)\ncaldav:   %s\npassword: %s\n",
			a.Email, marker, a.Username, a.IMAP.Addr, a.IMAP.Security, a.CalDAV.URL, passwordStatus(a.Email, len(accounts)))
		if err != nil {
			return 1
		}
	}
	return 0
}

// passwordStatus describes where the password is, never what it is.
func passwordStatus(email string, accounts int) string {
	_, source, err := account.Password(email, accounts)
	switch {
	case errors.Is(err, account.ErrNoPassword):
		return "not found"
	case err != nil:
		return err.Error()
	case source == account.SourceEnvironment:
		return account.PasswordEnv
	default:
		return source
	}
}

// accountTest logs in to each service of each account and reports each result,
// so one broken service doesn't hide the state of the others. email picks one
// account.
func (c *cli) accountTest(ctx context.Context, email string) int {
	accounts, err := c.store.List()
	if err != nil {
		return c.fail(err)
	}
	targets := accounts
	if email != "" {
		chosen, err := account.Select(accounts, email)
		if err != nil {
			return c.fail(err)
		}
		targets = []account.Account{chosen}
	}

	code := 0
	for i, a := range targets {
		// A single account prints as it always did, without a heading.
		indent := ""
		if len(targets) > 1 {
			if i > 0 {
				_, _ = fmt.Fprintln(c.stdout)
			}
			_, _ = fmt.Fprintf(c.stdout, "%s\n", a.Email)
			indent = "  "
		}
		if c.testAccount(ctx, a, len(accounts), indent) != 0 {
			code = 1
		}
	}
	return code
}

func (c *cli) testAccount(ctx context.Context, a account.Account, accounts int, indent string) int {
	password, _, err := account.Password(a.Email, accounts)
	if err != nil {
		if indent != "" {
			err = fmt.Errorf("%s: %w", a.Email, err)
		}
		return c.fail(err)
	}
	code := 0
	for _, check := range []struct {
		label string
		run   func(context.Context, *account.Account, string) error
	}{
		{"imap:  ", c.checkIMAP},
		{"caldav:", c.checkCalDAV},
	} {
		checkCtx, cancel := context.WithTimeout(ctx, checkTimeout)
		err := check.run(checkCtx, &a, password)
		cancel()
		if err != nil {
			code = 1
			_, _ = fmt.Fprintf(c.stdout, "%s%s failed: %v\n", indent, check.label, err)
			continue
		}
		_, _ = fmt.Fprintf(c.stdout, "%s%s ok\n", indent, check.label)
	}
	return code
}

func checkIMAP(ctx context.Context, a *account.Account, password string) error {
	client, err := mail.Dial(ctx, mailConfig(a, password))
	if err != nil {
		return err
	}
	return client.Close()
}

func mailConfig(a *account.Account, password string) mail.Config {
	return mail.Config{
		Addr:     a.IMAP.Addr,
		Security: mailSecurity(a.IMAP.Security),
		Username: a.Username,
		Password: password,
	}
}

// mailSecurity falls through to TLS, the safest choice, on anything unknown.
func mailSecurity(security string) mail.Security {
	switch security {
	case account.SecurityStartTLS:
		return mail.SecurityStartTLS
	case account.SecurityNone:
		return mail.SecurityNone
	default:
		return mail.SecurityTLS
	}
}

func checkCalDAV(ctx context.Context, a *account.Account, password string) error {
	client, err := calendar.New(calendarConfig(a, password))
	if err != nil {
		return err
	}
	return client.CheckLogin(ctx)
}

func calendarConfig(a *account.Account, password string) calendar.Config {
	return calendar.Config{URL: a.CalDAV.URL, Username: a.Username, Password: password}
}

// accountRemove removes one account. With a single account naming it is
// optional, as it always was. With several it is required, as removing the
// wrong one deletes its password too.
func (c *cli) accountRemove(email string) int {
	accounts, err := c.store.List()
	if err != nil {
		return c.fail(err)
	}
	if email == "" {
		if len(accounts) > 1 {
			return c.fail(fmt.Errorf("several accounts are configured, pass --email with one of: %s", strings.Join(account.Emails(accounts), ", ")))
		}
		email = accounts[0].Email
	}
	wasDefault := strings.EqualFold(accounts[0].Email, email)
	removed, err := c.store.Remove(email)
	if err != nil {
		return c.fail(err)
	}
	// The keychain entry is named by the address as it was stored.
	if err := account.DeletePassword(removed.Email); err != nil {
		return c.fail(fmt.Errorf("removed the account, but its password is still stored: %w", err))
	}
	_, _ = fmt.Fprintf(c.stdout, "Removed %s.\n", removed.Email)
	if wasDefault && len(accounts) > 1 {
		_, _ = fmt.Fprintf(c.stdout, "The default account is now %s.\n", accounts[1].Email)
	}
	return 0
}

// accountDefault makes one account the default, the one tools use when the
// agent names none.
func (c *cli) accountDefault(email string) int {
	if email == "" {
		_, _ = fmt.Fprint(c.stderr, usage)
		return 2
	}
	chosen, err := c.store.SetDefault(email)
	if err != nil {
		return c.fail(err)
	}
	_, _ = fmt.Fprintf(c.stdout, "The default account is now %s.\n", chosen.Email)
	return 0
}
