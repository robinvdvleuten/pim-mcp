package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/robinvdvleuten/pim-mcp/internal/account"
)

const testPassword = "correct-horse-battery"

var addArgs = []string{
	"account", "add",
	"--email", "me@example.org",
	"--imap", "mail.example.org:993",
	"--caldav", "https://dav.example.org/",
}

// fakeDiscoverer finds what it is given, and nothing if it is given nothing.
type fakeDiscoverer struct {
	imap     *account.FoundIMAP
	caldav   *account.FoundCalDAV
	useISPDB bool
	calls    int
}

func (f *fakeDiscoverer) DiscoverIMAP(_ context.Context, _ string, useISPDB bool) (*account.FoundIMAP, error) {
	f.calls++
	f.useISPDB = useISPDB
	if f.imap == nil {
		return nil, account.ErrNotDiscovered
	}
	return f.imap, nil
}

func (f *fakeDiscoverer) DiscoverCalDAV(context.Context, string) (*account.FoundCalDAV, error) {
	f.calls++
	if f.caldav == nil {
		return nil, account.ErrNotDiscovered
	}
	return f.caldav, nil
}

type harness struct {
	stdout, stderr bytes.Buffer
	cli            *cli
	dir            string
	prompts        int
	confirms       int
	answer         bool
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	keyring.MockInit()
	t.Setenv(account.PasswordEnv, "")
	h := &harness{dir: t.TempDir()}
	h.cli = &cli{
		stdout:     &h.stdout,
		stderr:     &h.stderr,
		store:      account.NewStore(h.dir),
		discoverer: &fakeDiscoverer{},
		confirm: func() (bool, error) {
			h.confirms++
			return h.answer, nil
		},
		readPassword: func() (string, error) {
			h.prompts++
			return testPassword, nil
		},
	}
	return h
}

func (h *harness) run(args ...string) int {
	h.stdout.Reset()
	h.stderr.Reset()
	return h.cli.run(context.Background(), args)
}

func (h *harness) config(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(h.dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestAccountAddSavesAccountAndPassword(t *testing.T) {
	h := newHarness(t)

	if code := h.run(addArgs...); code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, h.stderr.String())
	}

	got, err := h.cli.store.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	want := account.Account{
		Email:    "me@example.org",
		Username: "me@example.org",
		IMAP:     account.IMAP{Addr: "mail.example.org:993", Security: account.SecurityTLS},
		CalDAV:   account.CalDAV{URL: "https://dav.example.org/"},
	}
	if *got != want {
		t.Errorf("account = %+v, want %+v", *got, want)
	}
	password, source, err := account.Password("me@example.org")
	if err != nil || password != testPassword || source != account.SourceKeychain {
		t.Errorf("password = %q from %q, err %v", password, source, err)
	}
}

func TestAccountAddNeverWritesThePassword(t *testing.T) {
	h := newHarness(t)

	if code := h.run(addArgs...); code != 0 {
		t.Fatalf("exit code = %d", code)
	}

	if strings.Contains(h.config(t), testPassword) {
		t.Error("config.json contains the password")
	}
	if strings.Contains(h.stdout.String()+h.stderr.String(), testPassword) {
		t.Error("output contains the password")
	}
}

func TestAccountAddUsesEnvironmentPasswordWithoutPromptOrKeychain(t *testing.T) {
	h := newHarness(t)
	keyring.MockInitWithError(errors.New("no keychain in this container"))
	t.Setenv(account.PasswordEnv, "from-env")

	if code := h.run(addArgs...); code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, h.stderr.String())
	}

	if h.prompts != 0 {
		t.Errorf("prompted %d times, want none", h.prompts)
	}
	if strings.Contains(h.config(t), "from-env") {
		t.Error("config.json contains the password")
	}
}

func TestAccountAddOptions(t *testing.T) {
	h := newHarness(t)

	code := h.run("account", "add", "--email", "me@example.org", "--username", "tester",
		"--imap", "127.0.0.1:31143", "--imap-security", "none", "--caldav", "http://127.0.0.1:5232/")

	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, h.stderr.String())
	}
	got, err := h.cli.store.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.Username != "tester" || got.IMAP.Security != account.SecurityNone {
		t.Errorf("account = %+v", *got)
	}
}

func TestAccountAddRefusals(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"no email", []string{"account", "add", "--imap", "m.example.org:993", "--caldav", "https://d.example.org/"}, "email"},
		{"no IMAP", []string{"account", "add", "--email", "me@example.org", "--caldav", "https://d.example.org/"}, "--imap"},
		{"no CalDAV", []string{"account", "add", "--email", "me@example.org", "--imap", "m.example.org:993"}, "--caldav"},
		{"plain IMAP off loopback", []string{"account", "add", "--email", "me@example.org", "--imap", "m.example.org:143", "--imap-security", "none", "--caldav", "https://d.example.org/"}, "loopback"},
		{"HTTP off loopback", []string{"account", "add", "--email", "me@example.org", "--imap", "m.example.org:993", "--caldav", "http://d.example.org/"}, "loopback"},
		{"stray argument", append(append([]string{}, addArgs...), "extra"), "usage:"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)

			code := h.run(tt.args...)

			if code == 0 {
				t.Fatal("exit code = 0, want a failure")
			}
			if !strings.Contains(h.stderr.String(), tt.want) {
				t.Errorf("stderr = %q, want it to mention %q", h.stderr.String(), tt.want)
			}
			if h.prompts != 0 {
				t.Errorf("prompted for a password before checking the input")
			}
			if _, err := h.cli.store.Load(); !errors.Is(err, account.ErrNoAccount) {
				t.Errorf("load: err = %v, want no account saved", err)
			}
		})
	}
}

func TestAccountAddRefusesToReplaceAnAccount(t *testing.T) {
	h := newHarness(t)
	if code := h.run(addArgs...); code != 0 {
		t.Fatalf("first add: exit code = %d", code)
	}

	code := h.run("account", "add", "--email", "other@example.org",
		"--imap", "m.example.org:993", "--caldav", "https://d.example.org/")

	if code == 0 {
		t.Fatal("second add succeeded")
	}
	if !strings.Contains(h.stderr.String(), "account remove") {
		t.Errorf("stderr = %q, want it to point at account remove", h.stderr.String())
	}
	got, err := h.cli.store.Load()
	if err != nil || got.Email != "me@example.org" {
		t.Errorf("account = %+v, err %v, want the first one kept", got, err)
	}
}

func TestAccountAddEmptyPasswordIsRefused(t *testing.T) {
	h := newHarness(t)
	h.cli.readPassword = func() (string, error) { return "", nil }

	if code := h.run(addArgs...); code == 0 {
		t.Fatal("add succeeded with an empty password")
	}
	if _, err := h.cli.store.Load(); !errors.Is(err, account.ErrNoAccount) {
		t.Errorf("load: err = %v, want no account saved", err)
	}
}

func TestAccountAddKeychainFailureSavesNothing(t *testing.T) {
	h := newHarness(t)
	keyring.MockInitWithError(errors.New("keychain locked"))

	code := h.run(addArgs...)

	if code == 0 {
		t.Fatal("add succeeded without storing the password")
	}
	if !strings.Contains(h.stderr.String(), account.PasswordEnv) {
		t.Errorf("stderr = %q, want it to point at %s", h.stderr.String(), account.PasswordEnv)
	}
	if _, err := h.cli.store.Load(); !errors.Is(err, account.ErrNoAccount) {
		t.Errorf("load: err = %v, want no account saved", err)
	}
}

func TestAccountAddWithoutTerminalExplainsWhatToDo(t *testing.T) {
	h := newHarness(t)
	h.cli.readPassword = func() (string, error) { return "", errors.New("inappropriate ioctl for device") }

	code := h.run(addArgs...)

	if code == 0 {
		t.Fatal("add succeeded without a password")
	}
	if !strings.Contains(h.stderr.String(), account.PasswordEnv) {
		t.Errorf("stderr = %q, want it to point at %s", h.stderr.String(), account.PasswordEnv)
	}
}

func TestAccountShow(t *testing.T) {
	h := newHarness(t)
	if code := h.run(addArgs...); code != 0 {
		t.Fatalf("add: exit code = %d", code)
	}

	code := h.run("account", "show")

	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, h.stderr.String())
	}
	out := h.stdout.String()
	for _, want := range []string{"me@example.org", "mail.example.org:993", "tls", "https://dav.example.org/", "keychain"} {
		if !strings.Contains(out, want) {
			t.Errorf("output %q doesn't mention %q", out, want)
		}
	}
	if strings.Contains(out, testPassword) {
		t.Error("output contains the password")
	}
}

func TestAccountShowReportsEnvironmentWithoutItsValue(t *testing.T) {
	h := newHarness(t)
	if code := h.run(addArgs...); code != 0 {
		t.Fatalf("add: exit code = %d", code)
	}
	t.Setenv(account.PasswordEnv, "from-env")

	if code := h.run("account", "show"); code != 0 {
		t.Fatalf("exit code = %d", code)
	}

	if !strings.Contains(h.stdout.String(), account.PasswordEnv) {
		t.Errorf("output %q doesn't name the source", h.stdout.String())
	}
	if strings.Contains(h.stdout.String(), "from-env") {
		t.Error("output contains the password")
	}
}

func TestAccountShowReportsMissingPassword(t *testing.T) {
	h := newHarness(t)
	if code := h.run(addArgs...); code != 0 {
		t.Fatalf("add: exit code = %d", code)
	}
	if err := account.DeletePassword("me@example.org"); err != nil {
		t.Fatal(err)
	}

	if code := h.run("account", "show"); code != 0 {
		t.Fatalf("exit code = %d", code)
	}

	if !strings.Contains(h.stdout.String(), "not found") {
		t.Errorf("output = %q", h.stdout.String())
	}
}

func TestAccountRemove(t *testing.T) {
	h := newHarness(t)
	if code := h.run(addArgs...); code != 0 {
		t.Fatalf("add: exit code = %d", code)
	}

	if code := h.run("account", "remove"); code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, h.stderr.String())
	}

	if _, err := h.cli.store.Load(); !errors.Is(err, account.ErrNoAccount) {
		t.Errorf("load: err = %v, want no account", err)
	}
	if _, _, err := account.Password("me@example.org"); !errors.Is(err, account.ErrNoPassword) {
		t.Errorf("password: err = %v, want it gone", err)
	}
}

func TestAccountRemoveKeychainFailureIsReported(t *testing.T) {
	h := newHarness(t)
	if code := h.run(addArgs...); code != 0 {
		t.Fatalf("add: exit code = %d", code)
	}
	keyring.MockInitWithError(errors.New("keychain locked"))

	code := h.run("account", "remove")

	if code == 0 {
		t.Error("exit code = 0, but the password is still in the keychain")
	}
	if !strings.Contains(h.stderr.String(), "keychain locked") {
		t.Errorf("stderr = %q", h.stderr.String())
	}
	if _, err := h.cli.store.Load(); !errors.Is(err, account.ErrNoAccount) {
		t.Errorf("load: err = %v, want the account removed anyway", err)
	}
}

func TestAccountCommandsWithoutAccount(t *testing.T) {
	for _, command := range []string{"show", "remove"} {
		t.Run(command, func(t *testing.T) {
			h := newHarness(t)

			code := h.run("account", command)

			if code != 1 {
				t.Errorf("exit code = %d, want 1", code)
			}
			if !strings.Contains(h.stderr.String(), "account add") {
				t.Errorf("stderr = %q, want it to point at account add", h.stderr.String())
			}
		})
	}
}

func TestAccountBadUsage(t *testing.T) {
	for _, args := range [][]string{{"account"}, {"account", "nonsense"}, {"account", "show", "extra"}} {
		h := newHarness(t)

		if code := h.run(args...); code != 2 {
			t.Errorf("%v: exit code = %d, want 2", args, code)
		}
	}
}

func discovered() *fakeDiscoverer {
	return &fakeDiscoverer{
		imap: &account.FoundIMAP{
			IMAP:     account.IMAP{Addr: "imap.found.example.org:143", Security: account.SecurityStartTLS},
			Username: "found-user",
			Source:   account.SourceAutoconfig,
		},
		caldav: &account.FoundCalDAV{
			CalDAV: account.CalDAV{URL: "https://dav.found.example.org/"},
			Source: account.SourceWellKnown,
		},
	}
}

func TestAccountAddDiscoversAndConfirmsBeforeAskingForThePassword(t *testing.T) {
	h := newHarness(t)
	h.cli.discoverer = discovered()
	var order []string
	h.answer = true
	confirm := h.cli.confirm
	h.cli.confirm = func() (bool, error) {
		order = append(order, "confirm")
		return confirm()
	}
	readPassword := h.cli.readPassword
	h.cli.readPassword = func() (string, error) {
		order = append(order, "password")
		return readPassword()
	}

	code := h.run("account", "add", "--email", "me@example.org")

	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, h.stderr.String())
	}
	if strings.Join(order, ",") != "confirm,password" {
		t.Errorf("order = %v, want the confirmation before the password", order)
	}
	for _, want := range []string{"imap.found.example.org:143", "starttls", "https://dav.found.example.org/", "found-user", account.SourceAutoconfig, account.SourceWellKnown} {
		if !strings.Contains(h.stdout.String(), want) {
			t.Errorf("output %q doesn't mention %q", h.stdout.String(), want)
		}
	}
	got, err := h.cli.store.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	want := account.Account{
		Email:    "me@example.org",
		Username: "found-user",
		IMAP:     account.IMAP{Addr: "imap.found.example.org:143", Security: account.SecurityStartTLS},
		CalDAV:   account.CalDAV{URL: "https://dav.found.example.org/"},
	}
	if *got != want {
		t.Errorf("account = %+v, want %+v", *got, want)
	}
}

func TestAccountAddDecliningSavesNothing(t *testing.T) {
	h := newHarness(t)
	h.cli.discoverer = discovered()
	h.answer = false

	code := h.run("account", "add", "--email", "me@example.org")

	if code == 0 {
		t.Error("exit code = 0 after declining")
	}
	if h.prompts != 0 {
		t.Error("asked for the password after declining")
	}
	if _, err := h.cli.store.Load(); !errors.Is(err, account.ErrNoAccount) {
		t.Errorf("load: err = %v, want no account saved", err)
	}
}

func TestAccountAddConfirmFailureSavesNothing(t *testing.T) {
	h := newHarness(t)
	h.cli.discoverer = discovered()
	h.cli.confirm = func() (bool, error) { return false, errors.New("no terminal") }

	if code := h.run("account", "add", "--email", "me@example.org"); code == 0 {
		t.Error("exit code = 0 without a confirmation")
	}
	if !strings.Contains(h.stderr.String(), "--imap") {
		t.Errorf("stderr = %q, want it to point at the flags", h.stderr.String())
	}
	if _, err := h.cli.store.Load(); !errors.Is(err, account.ErrNoAccount) {
		t.Errorf("load: err = %v, want no account saved", err)
	}
}

func TestAccountAddFlagsSkipDiscoveryForThatService(t *testing.T) {
	h := newHarness(t)
	fake := discovered()
	h.cli.discoverer = fake
	h.answer = true

	code := h.run("account", "add", "--email", "me@example.org", "--username", "flag-user",
		"--imap", "imap.flag.example.org:993")

	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, h.stderr.String())
	}
	if fake.calls != 1 {
		t.Errorf("discovery calls = %d, want only CalDAV's", fake.calls)
	}
	got, err := h.cli.store.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.IMAP.Addr != "imap.flag.example.org:993" || got.CalDAV.URL != "https://dav.found.example.org/" || got.Username != "flag-user" {
		t.Errorf("account = %+v", *got)
	}
}

func TestAccountAddFlagsForEverythingNeedNoConfirmation(t *testing.T) {
	h := newHarness(t)
	fake := discovered()
	h.cli.discoverer = fake

	if code := h.run(addArgs...); code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, h.stderr.String())
	}

	if fake.calls != 0 || h.confirms != 0 {
		t.Errorf("discovery calls = %d, confirmations = %d, want none", fake.calls, h.confirms)
	}
}

func TestAccountAddDiscoveredPlainTransportIsRefused(t *testing.T) {
	h := newHarness(t)
	fake := discovered()
	fake.caldav.CalDAV.URL = "http://dav.found.example.org/"
	h.cli.discoverer = fake
	h.answer = true

	if code := h.run("account", "add", "--email", "me@example.org"); code == 0 {
		t.Error("saved an account with plain HTTP")
	}
	if h.confirms != 0 || h.prompts != 0 {
		t.Errorf("confirmations = %d, prompts = %d, want the refusal first", h.confirms, h.prompts)
	}
}

func TestAccountAddISPDBFlag(t *testing.T) {
	tests := map[string]struct {
		args []string
		want bool
	}{
		"on by default": {nil, true},
		"turned off":    {[]string{"--no-ispdb"}, false},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			fake := discovered()
			h.cli.discoverer = fake
			h.answer = true

			args := append([]string{"account", "add", "--email", "me@example.org"}, tt.args...)
			if code := h.run(args...); code != 0 {
				t.Fatalf("exit code = %d", code)
			}

			if fake.useISPDB != tt.want {
				t.Errorf("useISPDB = %v, want %v", fake.useISPDB, tt.want)
			}
		})
	}
}
