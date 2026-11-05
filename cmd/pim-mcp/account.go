package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/robinvdvleuten/pim-mcp/internal/account"
)

func (c *cli) runAccount(args []string) int {
	if len(args) == 0 {
		_, _ = fmt.Fprint(c.stderr, usage)
		return 2
	}
	switch args[0] {
	case "add":
		return c.accountAdd(args[1:])
	case "show", "remove":
		if len(args) != 1 {
			_, _ = fmt.Fprint(c.stderr, usage)
			return 2
		}
		if args[0] == "show" {
			return c.accountShow()
		}
		return c.accountRemove()
	default:
		_, _ = fmt.Fprint(c.stderr, usage)
		return 2
	}
}

func (c *cli) accountAdd(args []string) int {
	flags := flag.NewFlagSet("account add", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	email := flags.String("email", "", "")
	username := flags.String("username", "", "")
	imap := flags.String("imap", "", "")
	imapSecurity := flags.String("imap-security", "", "")
	caldav := flags.String("caldav", "", "")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		_, _ = fmt.Fprint(c.stderr, usage)
		return 2
	}

	switch {
	case *email == "":
		return c.fail(errors.New("--email is required"))
	case *imap == "":
		return c.fail(errors.New("--imap is required"))
	case *caldav == "":
		return c.fail(errors.New("--caldav is required"))
	}
	a := account.Account{
		Email:    *email,
		Username: *username,
		IMAP:     account.IMAP{Addr: *imap, Security: *imapSecurity},
		CalDAV:   account.CalDAV{URL: *caldav},
	}
	if a.Username == "" {
		a.Username = a.Email
	}
	if a.IMAP.Security == "" {
		a.IMAP.Security = account.DefaultIMAPSecurity(a.IMAP.Addr)
	}
	if err := a.Validate(); err != nil {
		return c.fail(err)
	}

	existing, err := c.store.Load()
	if err == nil {
		return c.fail(fmt.Errorf("account %s already exists, run: pim-mcp account remove", existing.Email))
	}
	if !errors.Is(err, account.ErrNoAccount) {
		return c.fail(err)
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
