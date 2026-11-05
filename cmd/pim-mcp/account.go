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
	case "show", "remove", "test":
		if len(args) != 1 {
			_, _ = fmt.Fprint(c.stderr, usage)
			return 2
		}
		switch args[0] {
		case "show":
			return c.accountShow()
		case "remove":
			return c.accountRemove()
		default:
			return c.accountTest(ctx)
		}
	default:
		_, _ = fmt.Fprint(c.stderr, usage)
		return 2
	}
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

	existing, err := c.store.Load()
	if err == nil {
		return c.fail(fmt.Errorf("account %s already exists, run: pim-mcp account remove", existing.Email))
	}
	if !errors.Is(err, account.ErrNoAccount) {
		return c.fail(err)
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
	if err := c.store.Save(a); err != nil {
		if storedPassword {
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
	if a.IMAP.Addr == "" {
		imap, err := c.discoverer.DiscoverIMAP(ctx, a.Email, useISPDB)
		if err != nil {
			return "", fmt.Errorf("%w, pass --imap", err)
		}
		a.IMAP = imap.IMAP
		if a.Username == "" {
			a.Username = imap.Username
		}
		fmt.Fprintf(&found, "  imap:     %s (%s), from %s\n", a.IMAP.Addr, a.IMAP.Security, imap.Source)
	}
	if a.CalDAV.URL == "" {
		caldav, err := c.discoverer.DiscoverCalDAV(ctx, a.Email)
		if err != nil {
			return "", fmt.Errorf("%w, pass --caldav", err)
		}
		a.CalDAV = caldav.CalDAV
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

func (c *cli) accountShow() int {
	a, err := c.store.Load()
	if err != nil {
		return c.fail(err)
	}
	_, err = fmt.Fprintf(c.stdout, "email:    %s\nusername: %s\nimap:     %s (%s)\ncaldav:   %s\npassword: %s\n",
		a.Email, a.Username, a.IMAP.Addr, a.IMAP.Security, a.CalDAV.URL, passwordStatus(a.Email))
	if err != nil {
		return 1
	}
	return 0
}

// passwordStatus describes where the password is, never what it is.
func passwordStatus(email string) string {
	_, source, err := account.Password(email)
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

// accountTest logs in to each service and reports each result, so one broken
// service doesn't hide the state of the other.
func (c *cli) accountTest(ctx context.Context) int {
	a, err := c.store.Load()
	if err != nil {
		return c.fail(err)
	}
	password, _, err := account.Password(a.Email)
	if err != nil {
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
		err := check.run(checkCtx, a, password)
		cancel()
		if err != nil {
			code = 1
			_, _ = fmt.Fprintf(c.stdout, "%s failed: %v\n", check.label, err)
			continue
		}
		_, _ = fmt.Fprintf(c.stdout, "%s ok\n", check.label)
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

func (c *cli) accountRemove() int {
	a, err := c.store.Load()
	if err != nil {
		return c.fail(err)
	}
	if err := c.store.Remove(); err != nil {
		return c.fail(err)
	}
	if err := account.DeletePassword(a.Email); err != nil {
		return c.fail(fmt.Errorf("removed the account, but its password is still stored: %w", err))
	}
	_, _ = fmt.Fprintf(c.stdout, "Removed %s.\n", a.Email)
	return 0
}
