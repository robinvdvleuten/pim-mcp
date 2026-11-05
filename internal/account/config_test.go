package account

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/robinvdvleuten/pim-mcp/internal/transport"
)

func validAccount() Account {
	return Account{
		Email:    "me@example.org",
		Username: "me",
		IMAP:     IMAP{Addr: "mail.example.org:993", Security: SecurityTLS},
		CalDAV:   CalDAV{URL: "https://dav.example.org/"},
	}
}

func TestStoreRoundTrip(t *testing.T) {
	store := NewStore(t.TempDir())

	if err := store.Save(validAccount()); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := store.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	if *got != validAccount() {
		t.Errorf("got %+v, want %+v", *got, validAccount())
	}
}

func TestStoreLoadWithoutAccount(t *testing.T) {
	_, err := NewStore(t.TempDir()).Load()

	if !errors.Is(err, ErrNoAccount) {
		t.Errorf("err = %v, want ErrNoAccount", err)
	}
}

func TestStoreKeepsFilesPrivate(t *testing.T) {
	// A directory that already exists with loose permissions gets tightened.
	dir := filepath.Join(t.TempDir(), "pim-mcp")
	if err := os.Mkdir(dir, 0o755); err != nil { //nolint:gosec // Loose on purpose.
		t.Fatal(err)
	}
	store := NewStore(dir)

	if err := store.Save(validAccount()); err != nil {
		t.Fatalf("save: %v", err)
	}

	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o700 {
		t.Errorf("directory mode = %o, want 700", mode)
	}
	info, err = os.Stat(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("file mode = %o, want 600", mode)
	}
}

func TestStoreLeavesNoTemporaryFiles(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	if err := store.Save(validAccount()); err != nil {
		t.Fatalf("save: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("directory holds %v, want only config.json", entries)
	}
}

func TestStoreFormatHoldsAListOfAccounts(t *testing.T) {
	// Multiple accounts are planned. A list from the start avoids a migration.
	dir := t.TempDir()
	if err := NewStore(dir).Save(validAccount()); err != nil {
		t.Fatalf("save: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"accounts": [`) {
		t.Errorf("config = %s", data)
	}
}

func TestStoreRemove(t *testing.T) {
	store := NewStore(t.TempDir())
	if err := store.Save(validAccount()); err != nil {
		t.Fatalf("save: %v", err)
	}

	if err := store.Remove(); err != nil {
		t.Fatalf("remove: %v", err)
	}

	if _, err := store.Load(); !errors.Is(err, ErrNoAccount) {
		t.Errorf("load after remove: err = %v, want ErrNoAccount", err)
	}
}

func TestStoreLoadRejectsCorruptFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := NewStore(dir).Load()

	if err == nil || errors.Is(err, ErrNoAccount) {
		t.Errorf("err = %v, want a parse error", err)
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name   string
		change func(*Account)
		want   string // part of the error, empty for valid
	}{
		{"valid", func(*Account) {}, ""},
		{"no email", func(a *Account) { a.Email = "" }, "email"},
		{"not an email", func(a *Account) { a.Email = "me" }, "email"},
		{"email with a display name", func(a *Account) { a.Email = "Me <me@example.org>" }, "email"},
		{"no username", func(a *Account) { a.Username = "" }, "username"},
		{"IMAP address without port", func(a *Account) { a.IMAP.Addr = "mail.example.org" }, "IMAP address"},
		{"unknown IMAP security", func(a *Account) { a.IMAP.Security = "ssl" }, "IMAP security"},
		{"STARTTLS", func(a *Account) { a.IMAP.Security = SecurityStartTLS }, ""},
		{"plain IMAP on loopback", func(a *Account) { a.IMAP = IMAP{Addr: "127.0.0.1:143", Security: SecurityNone} }, ""},
		{"CalDAV over HTTP on loopback", func(a *Account) { a.CalDAV.URL = "http://localhost:5232/" }, ""},
		{"bad CalDAV URL", func(a *Account) { a.CalDAV.URL = "://" }, "CalDAV URL"},
		{"CalDAV URL with no host", func(a *Account) { a.CalDAV.URL = "https:///dav" }, "CalDAV URL"},
		{"CalDAV URL holding a password", func(a *Account) { a.CalDAV.URL = "https://me:secret@dav.example.org/" }, "CalDAV URL"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := validAccount()
			tt.change(&a)

			err := a.Validate()

			if tt.want == "" {
				if err != nil {
					t.Errorf("err = %v, want none", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v, want it to mention %q", err, tt.want)
			}
		})
	}
}

func TestValidateRefusesUnencryptedConnectionsOffLoopback(t *testing.T) {
	tests := map[string]func(*Account){
		"plain IMAP":  func(a *Account) { a.IMAP = IMAP{Addr: "mail.example.org:143", Security: SecurityNone} },
		"HTTP CalDAV": func(a *Account) { a.CalDAV.URL = "http://dav.example.org/" },
	}

	for name, change := range tests {
		t.Run(name, func(t *testing.T) {
			a := validAccount()
			change(&a)

			if err := a.Validate(); !errors.Is(err, transport.ErrInsecure) {
				t.Errorf("err = %v, want transport.ErrInsecure", err)
			}
		})
	}
}

func TestDefaultIMAPSecurity(t *testing.T) {
	tests := map[string]string{
		"mail.example.org:993": SecurityTLS,
		"mail.example.org:143": SecurityStartTLS,
		"mail.example.org:587": SecurityStartTLS,
	}

	for addr, want := range tests {
		if got := DefaultIMAPSecurity(addr); got != want {
			t.Errorf("DefaultIMAPSecurity(%q) = %q, want %q", addr, got, want)
		}
	}
}

func TestPasswordFromKeychain(t *testing.T) {
	keyring.MockInit()
	t.Setenv(PasswordEnv, "")
	if err := SavePassword("me@example.org", "s3cret"); err != nil {
		t.Fatalf("save: %v", err)
	}

	password, source, err := Password("me@example.org")
	if err != nil {
		t.Fatalf("password: %v", err)
	}

	if password != "s3cret" || source != SourceKeychain {
		t.Errorf("got %q from %q", password, source)
	}
}

func TestPasswordEnvironmentWinsAndAvoidsKeychain(t *testing.T) {
	keyring.MockInitWithError(errors.New("no keychain in this container"))
	t.Setenv(PasswordEnv, "from-env")

	password, source, err := Password("me@example.org")
	if err != nil {
		t.Fatalf("password: %v", err)
	}

	if password != "from-env" || source != SourceEnvironment {
		t.Errorf("got %q from %q", password, source)
	}
}

func TestPasswordMissing(t *testing.T) {
	keyring.MockInit()
	t.Setenv(PasswordEnv, "")

	_, _, err := Password("me@example.org")

	if !errors.Is(err, ErrNoPassword) {
		t.Errorf("err = %v, want ErrNoPassword", err)
	}
	if !strings.Contains(err.Error(), PasswordEnv) {
		t.Errorf("err = %v, want it to point at %s", err, PasswordEnv)
	}
}

func TestPasswordKeychainFailureIsNotHidden(t *testing.T) {
	keyring.MockInitWithError(errors.New("keychain locked"))
	t.Setenv(PasswordEnv, "")

	_, _, err := Password("me@example.org")

	if err == nil || !strings.Contains(err.Error(), "keychain locked") {
		t.Errorf("err = %v, want the keychain's error", err)
	}
}

func TestDeletePasswordWhenAlreadyGone(t *testing.T) {
	keyring.MockInit()

	if err := DeletePassword("me@example.org"); err != nil {
		t.Errorf("err = %v, want none", err)
	}
}
