package main

import (
	"sync"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/robinvdvleuten/pim-mcp/internal/account"
	"github.com/robinvdvleuten/pim-mcp/internal/calendar"
)

func dialTestCLI(t *testing.T, caldavURL string) *cli {
	t.Helper()
	keyring.MockInit()
	t.Setenv(account.PasswordEnv, "first-password")
	store := account.NewStore(t.TempDir())
	saveDialAccount(t, store, caldavURL)
	return &cli{store: store}
}

func saveDialAccount(t *testing.T, store *account.Store, caldavURL string) {
	t.Helper()
	err := store.Add(account.Account{
		Email:    "me@example.org",
		Username: "me",
		IMAP:     account.IMAP{Addr: "mail.example.org:993", Security: account.SecurityTLS},
		CalDAV:   account.CalDAV{URL: caldavURL},
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestDialCalendarReusesTheClientWhileTheAccountIsUnchanged(t *testing.T) {
	c := dialTestCLI(t, "https://dav.example.org/")

	first, err := c.dialCalendar("")
	if err != nil {
		t.Fatal(err)
	}
	second, err := c.dialCalendar("")
	if err != nil {
		t.Fatal(err)
	}

	if first != second {
		t.Error("an unchanged account got a new client")
	}
}

func TestDialCalendarBuildsANewClientWhenTheAccountChanges(t *testing.T) {
	c := dialTestCLI(t, "https://dav.example.org/")
	first, err := c.dialCalendar("")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := c.store.Remove("me@example.org"); err != nil {
		t.Fatal(err)
	}
	saveDialAccount(t, c.store, "https://other.example.org/")
	second, err := c.dialCalendar("")
	if err != nil {
		t.Fatal(err)
	}

	if first == second {
		t.Error("a changed CalDAV URL kept the old client")
	}
}

func TestDialCalendarBuildsANewClientWhenThePasswordChanges(t *testing.T) {
	c := dialTestCLI(t, "https://dav.example.org/")
	first, err := c.dialCalendar("")
	if err != nil {
		t.Fatal(err)
	}

	t.Setenv(account.PasswordEnv, "second-password")
	second, err := c.dialCalendar("")
	if err != nil {
		t.Fatal(err)
	}

	if first == second {
		t.Error("a changed password kept the old client")
	}
}

func TestDialCalendarDoesNotKeepAClientForARefusedConfig(t *testing.T) {
	c := dialTestCLI(t, "http://dav.example.org/") // Plain HTTP beyond loopback.

	if _, err := c.dialCalendar(""); err == nil {
		t.Fatal("dial succeeded")
	}
	if len(c.calendarClients) != 0 {
		t.Error("a client was kept after a failed dial")
	}
}

func TestDialCalendarIsSafeForConcurrentCalls(t *testing.T) {
	c := dialTestCLI(t, "https://dav.example.org/")
	clients := make([]*calendar.Client, 8)
	var wg sync.WaitGroup

	for i := range clients {
		wg.Go(func() {
			var err error
			if clients[i], err = c.dialCalendar(""); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()

	for _, client := range clients {
		if client != clients[0] {
			t.Fatal("concurrent calls built different clients")
		}
	}
}
