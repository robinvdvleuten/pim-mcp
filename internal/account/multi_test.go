package account

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"
)

func workAccount() Account {
	return Account{
		Email:    "me@work.example",
		Username: "me",
		IMAP:     IMAP{Addr: "mail.work.example:993", Security: SecurityTLS},
		CalDAV:   CalDAV{URL: "https://dav.work.example/"},
	}
}

func emailsOf(t *testing.T, store *Store) []string {
	t.Helper()
	accounts, err := store.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	return Emails(accounts)
}

func TestStoreAddAppendsSoTheFirstStaysTheDefault(t *testing.T) {
	store := NewStore(t.TempDir())
	for _, a := range []Account{validAccount(), workAccount()} {
		if err := store.Add(a); err != nil {
			t.Fatalf("add %s: %v", a.Email, err)
		}
	}

	if got := strings.Join(emailsOf(t, store), ","); got != "me@example.org,me@work.example" {
		t.Errorf("accounts = %s", got)
	}
	def, err := store.Load()
	if err != nil || def.Email != "me@example.org" {
		t.Errorf("default = %+v, err %v", def, err)
	}
}

func TestStoreAddRefusesADuplicateEmailInAnyCase(t *testing.T) {
	store := NewStore(t.TempDir())
	if err := store.Add(validAccount()); err != nil {
		t.Fatal(err)
	}
	other := workAccount()
	other.Email = "ME@Example.org"

	err := store.Add(other)

	if !errors.Is(err, ErrDuplicate) {
		t.Errorf("err = %v, want ErrDuplicate", err)
	}
	if got := emailsOf(t, store); len(got) != 1 {
		t.Errorf("accounts = %v, want the first one only", got)
	}
}

func TestStoreReadsAFileWrittenBeforeSeveralAccounts(t *testing.T) {
	// What the single-account version wrote, byte for byte.
	legacy := `{
  "accounts": [
    {
      "email": "me@example.org",
      "username": "me",
      "imap": {
        "addr": "mail.example.org:993",
        "security": "tls"
      },
      "caldav": {
        "url": "https://dav.example.org/"
      }
    }
  ]
}
`
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	store := NewStore(dir)

	got, err := store.Load()
	if err != nil || *got != validAccount() {
		t.Fatalf("load = %+v, err %v", got, err)
	}
	// Writing the same single account back gives the same bytes.
	if _, err := store.SetDefault("me@example.org"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil || string(data) != legacy {
		t.Errorf("config = %q, err %v, want it unchanged", data, err)
	}
	// And a second account leaves the first entry as it was.
	if err := store.Add(workAccount()); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(filepath.Join(dir, "config.json"))
	if !strings.Contains(string(data), strings.TrimSuffix(strings.TrimPrefix(legacy, "{\n  \"accounts\": [\n"), "\n  ]\n}\n")+",\n    {") {
		t.Errorf("the first entry changed: %s", data)
	}
}

func TestStoreRemoveTakesOneAccountAndKeepsTheOthers(t *testing.T) {
	store := NewStore(t.TempDir())
	_ = store.Add(validAccount())
	_ = store.Add(workAccount())

	removed, err := store.Remove("ME@example.org")
	if err != nil || removed.Email != "me@example.org" {
		t.Fatalf("remove = %+v, err %v", removed, err)
	}

	if got := emailsOf(t, store); len(got) != 1 || got[0] != "me@work.example" {
		t.Errorf("accounts = %v", got)
	}
	if def, _ := store.Load(); def.Email != "me@work.example" {
		t.Errorf("default = %s, want the one left", def.Email)
	}
	if _, err := store.Remove("me@work.example"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.List(); !errors.Is(err, ErrNoAccount) {
		t.Errorf("list after removing the last: err = %v, want ErrNoAccount", err)
	}
}

func TestStoreRemoveUnknownNamesTheConfiguredOnes(t *testing.T) {
	store := NewStore(t.TempDir())
	_ = store.Add(validAccount())
	_ = store.Add(workAccount())

	_, err := store.Remove("nobody@example.org")

	if err == nil || !strings.Contains(err.Error(), "me@example.org") || !strings.Contains(err.Error(), "me@work.example") {
		t.Errorf("err = %v, want the configured addresses", err)
	}
	if got := emailsOf(t, store); len(got) != 2 {
		t.Errorf("accounts = %v, want both kept", got)
	}
}

func TestStoreSetDefaultMovesToTheFront(t *testing.T) {
	store := NewStore(t.TempDir())
	_ = store.Add(validAccount())
	_ = store.Add(workAccount())

	if _, err := store.SetDefault("ME@WORK.example"); err != nil {
		t.Fatal(err)
	}

	if got := strings.Join(emailsOf(t, store), ","); got != "me@work.example,me@example.org" {
		t.Errorf("accounts = %s", got)
	}
	if _, err := store.SetDefault("nobody@example.org"); err == nil {
		t.Error("an unknown address became the default")
	}
}

func TestSelect(t *testing.T) {
	accounts := []Account{validAccount(), workAccount()}

	tests := []struct {
		name, email, want, wantErr string
	}{
		{name: "default", email: "", want: "me@example.org"},
		{name: "named", email: "me@work.example", want: "me@work.example"},
		{name: "any case", email: "ME@Work.Example", want: "me@work.example"},
		{name: "unknown", email: "x@example.org", wantErr: "me@example.org, me@work.example"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Select(accounts, tt.email)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("err = %v, want it to list %q", err, tt.wantErr)
				}
				return
			}
			if err != nil || got.Email != tt.want {
				t.Errorf("got %+v, err %v, want %s", got, err, tt.want)
			}
		})
	}
	if _, err := Select(nil, ""); !errors.Is(err, ErrNoAccount) {
		t.Errorf("no accounts: err = %v", err)
	}
}

func TestPasswordFromTheEnvironmentIsRefusedWithSeveralAccounts(t *testing.T) {
	keyring.MockInit()
	if err := SavePassword("me@example.org", "keychain-password"); err != nil {
		t.Fatal(err)
	}
	t.Setenv(PasswordEnv, "from-env")

	password, source, err := Password("me@example.org", 2)

	if !errors.Is(err, ErrEnvSeveralAccounts) {
		t.Errorf("err = %v, want ErrEnvSeveralAccounts", err)
	}
	if password != "" || source != "" {
		t.Errorf("got %q from %q, want nothing, not even the keychain's", password, source)
	}
	for _, want := range []string{PasswordEnv, "single account", "keychain"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, want it to mention %q", err, want)
		}
	}
}

func TestPasswordFromTheEnvironmentStillWorksWithOneAccount(t *testing.T) {
	keyring.MockInit()
	t.Setenv(PasswordEnv, "from-env")

	password, source, err := Password("me@example.org", 1)

	if err != nil || password != "from-env" || source != SourceEnvironment {
		t.Errorf("got %q from %q, err %v", password, source, err)
	}
}

func TestPasswordWithSeveralAccountsUsesTheKeychainWhenTheEnvironmentIsEmpty(t *testing.T) {
	keyring.MockInit()
	t.Setenv(PasswordEnv, "")
	_ = SavePassword("me@example.org", "first")
	_ = SavePassword("me@work.example", "second")

	for email, want := range map[string]string{"me@example.org": "first", "me@work.example": "second"} {
		password, source, err := Password(email, 2)
		if err != nil || password != want || source != SourceKeychain {
			t.Errorf("%s: got %q from %q, err %v, want %q", email, password, source, err, want)
		}
	}
}

func TestCheckEnvForNewAccount(t *testing.T) {
	tests := []struct {
		name     string
		env      string
		existing int
		refused  bool
	}{
		{"first account with the variable", "x", 0, false},
		{"second account with the variable", "x", 1, true},
		{"second account without it", "", 1, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(PasswordEnv, tt.env)
			if err := CheckEnvForNewAccount(tt.existing); (err != nil) != tt.refused {
				t.Errorf("err = %v, refused = %v", err, tt.refused)
			}
		})
	}
}

func TestKeychainEntriesAreKeptPerEmail(t *testing.T) {
	keyring.MockInit()
	t.Setenv(PasswordEnv, "")
	_ = SavePassword("me@example.org", "first")
	_ = SavePassword("me@work.example", "second")

	if err := DeletePassword("me@example.org"); err != nil {
		t.Fatal(err)
	}

	if _, _, err := Password("me@example.org", 2); !errors.Is(err, ErrNoPassword) {
		t.Errorf("deleted entry: err = %v", err)
	}
	if password, _, err := Password("me@work.example", 2); err != nil || password != "second" {
		t.Errorf("the other entry: %q, err %v", password, err)
	}
}
