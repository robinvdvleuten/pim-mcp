package main

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/robinvdvleuten/pim-mcp/internal/account"
	"github.com/robinvdvleuten/pim-mcp/internal/calendar"
	"github.com/robinvdvleuten/pim-mcp/internal/mail"
)

func addArgsFor(email string) []string {
	return []string{"account", "add", "--email", email, "--imap", "mail.work.example:993", "--caldav", "https://dav.work.example/"}
}

// twoAccounts adds the usual account and a second one, with the passwords
// given to the prompt in turn.
func twoAccounts(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t)
	passwords := []string{"first-password", "second-password"}
	h.cli.readPassword = func() (string, error) {
		h.prompts++
		return passwords[h.prompts-1], nil
	}
	if code := h.run(addArgs...); code != 0 {
		t.Fatalf("first add: %d %s", code, h.stderr.String())
	}
	if code := h.run(addArgsFor("me@work.example")...); code != 0 {
		t.Fatalf("second add: %d %s", code, h.stderr.String())
	}
	return h
}

func TestAccountAddKeepsEachPasswordUnderItsOwnEmail(t *testing.T) {
	twoAccounts(t)

	for email, want := range map[string]string{"me@example.org": "first-password", "me@work.example": "second-password"} {
		got, _, err := account.Password(email, 2)
		if err != nil || got != want {
			t.Errorf("%s: password %q, err %v, want %q", email, got, err, want)
		}
	}
}

func TestAccountAddRefusesADuplicateEmail(t *testing.T) {
	h := newHarness(t)
	if code := h.run(addArgs...); code != 0 {
		t.Fatalf("first add: %d", code)
	}
	prompts := h.prompts

	code := h.run("account", "add", "--email", "ME@Example.ORG", "--imap", "other.example.org:993", "--caldav", "https://other.example.org/")

	if code == 0 {
		t.Fatal("a duplicate was added")
	}
	if !strings.Contains(h.stderr.String(), "already exists") {
		t.Errorf("stderr = %q", h.stderr.String())
	}
	if h.prompts != prompts {
		t.Error("asked for a password for a duplicate")
	}
	if got, _ := h.cli.store.List(); len(got) != 1 || got[0].IMAP.Addr != "mail.example.org:993" {
		t.Errorf("accounts = %+v, want the first one untouched", got)
	}
	// The existing account keeps its password.
	if password, _, err := account.Password("me@example.org", 1); err != nil || password != testPassword {
		t.Errorf("password = %q, err %v", password, err)
	}
}

func TestAccountAddRefusesToGiveTheEnvironmentPasswordToASecondAccount(t *testing.T) {
	h := newHarness(t)
	t.Setenv(account.PasswordEnv, "from-env")
	if code := h.run(addArgs...); code != 0 {
		t.Fatalf("first add with the variable set: %d %s", code, h.stderr.String())
	}

	code := h.run(addArgsFor("me@work.example")...)

	if code == 0 {
		t.Fatal("the second account was added while the variable was set")
	}
	for _, want := range []string{account.PasswordEnv, "single account", "keychain"} {
		if !strings.Contains(h.stderr.String(), want) {
			t.Errorf("stderr = %q, want it to mention %q", h.stderr.String(), want)
		}
	}
	if got, _ := h.cli.store.List(); len(got) != 1 {
		t.Errorf("accounts = %+v", got)
	}
	if h.prompts != 0 {
		t.Error("prompted for a password")
	}
	// Nothing may be in the keychain for the refused account either.
	t.Setenv(account.PasswordEnv, "")
	if _, _, err := account.Password("me@work.example", 2); !errors.Is(err, account.ErrNoPassword) {
		t.Errorf("keychain holds something for the refused account: err = %v", err)
	}
}

func TestAccountShowWithOneAccountIsAsItWas(t *testing.T) {
	h := newHarness(t)
	if code := h.run(addArgs...); code != 0 {
		t.Fatalf("add: %d", code)
	}

	if code := h.run("account", "show"); code != 0 {
		t.Fatalf("exit code = %d", code)
	}

	want := "email:    me@example.org\nusername: me@example.org\nimap:     mail.example.org:993 (tls)\ncaldav:   https://dav.example.org/\npassword: keychain\n"
	if h.stdout.String() != want {
		t.Errorf("output = %q, want %q", h.stdout.String(), want)
	}
}

func TestAccountShowListsEveryAccountAndMarksTheDefault(t *testing.T) {
	h := twoAccounts(t)

	if code := h.run("account", "show"); code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, h.stderr.String())
	}

	want := "email:    me@example.org (default)\nusername: me@example.org\nimap:     mail.example.org:993 (tls)\ncaldav:   https://dav.example.org/\npassword: keychain\n" +
		"\n" +
		"email:    me@work.example\nusername: me@work.example\nimap:     mail.work.example:993 (tls)\ncaldav:   https://dav.work.example/\npassword: keychain\n"
	if h.stdout.String() != want {
		t.Errorf("output = %q, want %q", h.stdout.String(), want)
	}
	if strings.Contains(h.stdout.String(), "first-password") || strings.Contains(h.stdout.String(), "second-password") {
		t.Error("output contains a password")
	}
}

func TestAccountShowWithSeveralAccountsExplainsTheEnvironmentPassword(t *testing.T) {
	h := twoAccounts(t)
	t.Setenv(account.PasswordEnv, "from-env")

	if code := h.run("account", "show"); code != 0 {
		t.Fatalf("exit code = %d", code)
	}

	if strings.Count(h.stdout.String(), "single account") != 2 || strings.Contains(h.stdout.String(), "from-env") {
		t.Errorf("output = %q", h.stdout.String())
	}
}

func TestAccountTestChecksEveryAccountWithItsOwnPassword(t *testing.T) {
	h := twoAccounts(t)
	checked := map[string]string{}
	h.cli.checkIMAP = func(_ context.Context, a *account.Account, password string) error {
		checked[a.Email+" imap"] = password
		return nil
	}
	h.cli.checkCalDAV = func(_ context.Context, a *account.Account, password string) error {
		checked[a.Email+" caldav"] = password
		if a.Email == "me@work.example" {
			return errors.New("login refused")
		}
		return nil
	}

	code := h.run("account", "test")

	if code != 1 {
		t.Errorf("exit code = %d, want 1 because one account failed", code)
	}
	want := map[string]string{
		"me@example.org imap": "first-password", "me@example.org caldav": "first-password",
		"me@work.example imap": "second-password", "me@work.example caldav": "second-password",
	}
	for key, password := range want {
		if checked[key] != password {
			t.Errorf("%s was checked with %q, want %q", key, checked[key], password)
		}
	}
	wantOutput := "me@example.org\n  imap:   ok\n  caldav: ok\n\nme@work.example\n  imap:   ok\n  caldav: failed: login refused\n"
	if h.stdout.String() != wantOutput {
		t.Errorf("output = %q, want %q", h.stdout.String(), wantOutput)
	}
}

func TestAccountTestOneAccount(t *testing.T) {
	h := twoAccounts(t)
	var checked []string
	h.cli.checkIMAP = func(_ context.Context, a *account.Account, _ string) error {
		checked = append(checked, a.Email)
		return nil
	}

	if code := h.run("account", "test", "--email", "ME@work.example"); code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, h.stderr.String())
	}
	if !slices.Equal(checked, []string{"me@work.example"}) {
		t.Errorf("checked %v", checked)
	}

	if code := h.run("account", "test", "--email", "nobody@example.org"); code != 1 || !strings.Contains(h.stderr.String(), "me@example.org, me@work.example") {
		t.Errorf("unknown: exit code = %d, stderr = %q", code, h.stderr.String())
	}
}

func TestAccountTestKeepsGoingAfterAMissingPassword(t *testing.T) {
	h := twoAccounts(t)
	if err := account.DeletePassword("me@example.org"); err != nil {
		t.Fatal(err)
	}
	var checked []string
	h.cli.checkIMAP = func(_ context.Context, a *account.Account, _ string) error {
		checked = append(checked, a.Email)
		return nil
	}

	code := h.run("account", "test")

	if code != 1 || !slices.Equal(checked, []string{"me@work.example"}) {
		t.Errorf("exit code = %d, checked %v", code, checked)
	}
	if !strings.Contains(h.stderr.String(), "me@example.org") {
		t.Errorf("stderr = %q, want it to say which account", h.stderr.String())
	}
}

func TestAccountTestWithSeveralAccountsRefusesTheEnvironmentPassword(t *testing.T) {
	h := twoAccounts(t)
	t.Setenv(account.PasswordEnv, "from-env")
	checks := 0
	h.cli.checkIMAP = func(context.Context, *account.Account, string) error { checks++; return nil }

	code := h.run("account", "test")

	if code != 1 || checks != 0 {
		t.Errorf("exit code = %d, checks = %d, want no login at all", code, checks)
	}
	if !strings.Contains(h.stderr.String(), "single account") {
		t.Errorf("stderr = %q", h.stderr.String())
	}
}

func TestAccountRemoveWithSeveralAccountsNeedsAnEmail(t *testing.T) {
	h := twoAccounts(t)

	code := h.run("account", "remove")

	if code == 0 {
		t.Fatal("removed an account without being told which")
	}
	if !strings.Contains(h.stderr.String(), "me@example.org, me@work.example") {
		t.Errorf("stderr = %q, want the configured addresses", h.stderr.String())
	}
	if got, _ := h.cli.store.List(); len(got) != 2 {
		t.Errorf("accounts = %+v", got)
	}
}

func TestAccountRemoveOneOfSeveral(t *testing.T) {
	h := twoAccounts(t)

	if code := h.run("account", "remove", "--email", "me@example.org"); code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, h.stderr.String())
	}

	got, _ := h.cli.store.List()
	if len(got) != 1 || got[0].Email != "me@work.example" {
		t.Errorf("accounts = %+v", got)
	}
	if _, _, err := account.Password("me@example.org", 1); !errors.Is(err, account.ErrNoPassword) {
		t.Errorf("removed account's password: err = %v, want it gone", err)
	}
	if password, _, err := account.Password("me@work.example", 1); err != nil || password != "second-password" {
		t.Errorf("the other password: %q, err %v, want it kept", password, err)
	}
	if !strings.Contains(h.stdout.String(), "Removed me@example.org.") || !strings.Contains(h.stdout.String(), "default account is now me@work.example") {
		t.Errorf("output = %q", h.stdout.String())
	}
}

func TestAccountRemoveUnknownEmail(t *testing.T) {
	h := twoAccounts(t)

	if code := h.run("account", "remove", "--email", "nobody@example.org"); code != 1 {
		t.Errorf("exit code = %d", code)
	}
	if got, _ := h.cli.store.List(); len(got) != 2 {
		t.Errorf("accounts = %+v", got)
	}
}

func TestAccountRemoveOnlyAccountDoesNotNeedAnEmail(t *testing.T) {
	// Already covered by TestAccountRemove; this adds a wrong address.
	h := newHarness(t)
	if code := h.run(addArgs...); code != 0 {
		t.Fatalf("add: %d", code)
	}

	if code := h.run("account", "remove", "--email", "nobody@example.org"); code != 1 {
		t.Errorf("a wrong address removed the only account: exit code = %d", code)
	}
	if _, err := h.cli.store.Load(); err != nil {
		t.Errorf("load: %v", err)
	}
}

func TestAccountDefault(t *testing.T) {
	h := twoAccounts(t)

	if code := h.run("account", "default", "--email", "me@work.example"); code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, h.stderr.String())
	}

	got, _ := h.cli.store.List()
	if emails := account.Emails(got); !slices.Equal(emails, []string{"me@work.example", "me@example.org"}) {
		t.Errorf("accounts = %v", emails)
	}
	if !strings.Contains(h.stdout.String(), "me@work.example") {
		t.Errorf("output = %q", h.stdout.String())
	}

	if code := h.run("account", "default", "--email", "nobody@example.org"); code != 1 || !strings.Contains(h.stderr.String(), "me@example.org") {
		t.Errorf("unknown: exit code = %d, stderr = %q", code, h.stderr.String())
	}
	if code := h.run("account", "default"); code != 2 {
		t.Errorf("no email: exit code = %d, want 2", code)
	}
}

func TestAccountCommandsTakeNoStrayArguments(t *testing.T) {
	for _, args := range [][]string{
		{"account", "test", "extra"}, {"account", "remove", "extra"}, {"account", "default", "extra"},
		{"account", "test", "--nope"}, {"account", "show", "--email", "x"},
	} {
		h := newHarness(t)
		if code := h.run(args...); code != 2 {
			t.Errorf("%v: exit code = %d, want 2", args, code)
		}
	}
}

func TestUsageNamesTheAccountCommands(t *testing.T) {
	for _, want := range []string{"account test [--email", "account remove [--email", "account default --email"} {
		if !strings.Contains(usage, want) {
			t.Errorf("usage doesn't mention %q", want)
		}
	}
}

// Isolation: a call for one account never uses another's credentials or client.

func dialTwoAccounts(t *testing.T, sameEndpoint bool) *cli {
	t.Helper()
	keyring.MockInit()
	t.Setenv(account.PasswordEnv, "")
	store := account.NewStore(t.TempDir())
	first := account.Account{Email: "me@example.org", Username: "me", IMAP: account.IMAP{Addr: "mail.example.org:993", Security: account.SecurityTLS}, CalDAV: account.CalDAV{URL: "https://dav.example.org/"}}
	second := account.Account{Email: "me@work.example", Username: "work-me", IMAP: account.IMAP{Addr: "mail.work.example:993", Security: account.SecurityStartTLS}, CalDAV: account.CalDAV{URL: "https://dav.work.example/"}}
	if sameEndpoint {
		second.Username, second.CalDAV = first.Username, first.CalDAV
	}
	for _, a := range []account.Account{first, second} {
		if err := store.Add(a); err != nil {
			t.Fatal(err)
		}
	}
	if err := account.SavePassword(first.Email, "first-password"); err != nil {
		t.Fatal(err)
	}
	if err := account.SavePassword(second.Email, "second-password"); err != nil {
		t.Fatal(err)
	}
	return &cli{store: store}
}

func TestDialMailUsesOnlyTheNamedAccountsDetails(t *testing.T) {
	c := dialTwoAccounts(t, false)
	var got []mail.Config
	c.dialIMAP = func(_ context.Context, config mail.Config) (*mail.Client, error) {
		got = append(got, config)
		return nil, errors.New("stop")
	}

	_, _ = c.dialMail(context.Background(), "me@work.example")
	_, _ = c.dialMail(context.Background(), "")
	_, _ = c.dialMail(context.Background(), "ME@EXAMPLE.ORG")

	want := []mail.Config{
		{Addr: "mail.work.example:993", Security: mail.SecurityStartTLS, Username: "work-me", Password: "second-password"},
		{Addr: "mail.example.org:993", Security: mail.SecurityTLS, Username: "me", Password: "first-password"},
		{Addr: "mail.example.org:993", Security: mail.SecurityTLS, Username: "me", Password: "first-password"},
	}
	if !slices.Equal(got, want) {
		t.Errorf("dialed with %+v,\nwant %+v", got, want)
	}
}

func TestDialMailForAnUnknownAccountSendsNothing(t *testing.T) {
	c := dialTwoAccounts(t, false)
	c.dialIMAP = func(context.Context, mail.Config) (*mail.Client, error) {
		t.Error("dialed for an unknown account")
		return nil, errors.New("stop")
	}

	_, err := c.dialMail(context.Background(), "nobody@example.org")

	if err == nil || !strings.Contains(err.Error(), "me@example.org, me@work.example") {
		t.Errorf("err = %v", err)
	}
}

func TestDialRefusesTheEnvironmentPasswordWithSeveralAccounts(t *testing.T) {
	c := dialTwoAccounts(t, false)
	t.Setenv(account.PasswordEnv, "from-env")
	c.dialIMAP = func(context.Context, mail.Config) (*mail.Client, error) {
		t.Error("dialed with the environment password")
		return nil, errors.New("stop")
	}

	_, mailErr := c.dialMail(context.Background(), "me@work.example")
	_, calErr := c.dialCalendar("me@example.org")
	_, defaultErr := c.dialCalendar("")

	for _, err := range []error{mailErr, calErr, defaultErr} {
		if !errors.Is(err, account.ErrEnvSeveralAccounts) {
			t.Errorf("err = %v, want ErrEnvSeveralAccounts", err)
		}
	}
}

func TestDialCalendarBuildsEachClientFromItsOwnAccount(t *testing.T) {
	c := dialTwoAccounts(t, false)

	first, err := c.dialCalendar("me@example.org")
	if err != nil {
		t.Fatal(err)
	}
	second, err := c.dialCalendar("me@work.example")
	if err != nil {
		t.Fatal(err)
	}

	if first == second {
		t.Fatal("two accounts share a client")
	}
	if got, want := c.calendarClients["me@example.org"].config, (calendar.Config{URL: "https://dav.example.org/", Username: "me", Password: "first-password"}); got != want {
		t.Errorf("first config = %+v, want %+v", got, want)
	}
	if got, want := c.calendarClients["me@work.example"].config, (calendar.Config{URL: "https://dav.work.example/", Username: "work-me", Password: "second-password"}); got != want {
		t.Errorf("second config = %+v, want %+v", got, want)
	}
}

func TestDialCalendarNeverReturnsAnotherEmailsClientEvenWithTheSameURLAndUsername(t *testing.T) {
	c := dialTwoAccounts(t, true)
	// Same password too, so the whole config is equal.
	if err := account.SavePassword("me@work.example", "first-password"); err != nil {
		t.Fatal(err)
	}

	first, err := c.dialCalendar("me@example.org")
	if err != nil {
		t.Fatal(err)
	}
	second, err := c.dialCalendar("me@work.example")
	if err != nil {
		t.Fatal(err)
	}

	if c.calendarClients["me@example.org"].config != c.calendarClients["me@work.example"].config {
		t.Fatal("the configs differ, so this proves nothing")
	}
	if first == second {
		t.Error("a cached client was returned for another email")
	}
	if again, _ := c.dialCalendar("me@example.org"); again != first {
		t.Error("the first account lost its client")
	}
}

func TestDialCalendarDefaultIsTheFirstAccount(t *testing.T) {
	c := dialTwoAccounts(t, false)

	byDefault, err := c.dialCalendar("")
	if err != nil {
		t.Fatal(err)
	}
	named, err := c.dialCalendar("me@example.org")
	if err != nil {
		t.Fatal(err)
	}

	if byDefault != named {
		t.Error("the default isn't the first account")
	}
}

func TestDialCalendarForgetsRemovedAccounts(t *testing.T) {
	c := dialTwoAccounts(t, false)
	if _, err := c.dialCalendar("me@example.org"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.dialCalendar("me@work.example"); err != nil {
		t.Fatal(err)
	}

	if _, err := c.store.Remove("me@example.org"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.dialCalendar("me@work.example"); err != nil {
		t.Fatal(err)
	}

	if len(c.calendarClients) != 1 || c.calendarClients["me@work.example"].client == nil {
		t.Errorf("kept clients for %d accounts, want only the remaining one", len(c.calendarClients))
	}
}

func TestDialCalendarKeychainEntriesAreOnePerEmail(t *testing.T) {
	c := dialTwoAccounts(t, false)
	if err := account.DeletePassword("me@example.org"); err != nil {
		t.Fatal(err)
	}

	if _, err := c.dialCalendar("me@example.org"); !errors.Is(err, account.ErrNoPassword) {
		t.Errorf("first account: err = %v, want no password, not the other's", err)
	}
	if _, err := c.dialCalendar("me@work.example"); err != nil {
		t.Errorf("second account: %v", err)
	}
}
