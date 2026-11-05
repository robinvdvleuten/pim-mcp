package account

import (
	"errors"
	"fmt"
	"os"

	"github.com/zalando/go-keyring"
)

// PasswordEnv names the variable that supplies the password where no keychain
// is available, such as in Docker and CI.
const PasswordEnv = "PIM_MCP_PASSWORD"

const keyringService = "pim-mcp"

// Where a password came from.
const (
	SourceKeychain    = "keychain"
	SourceEnvironment = "environment"
)

// ErrNoPassword means neither the environment nor the keychain has a password.
var ErrNoPassword = fmt.Errorf("no password found, run: pim-mcp account add, or set %s", PasswordEnv)

// Password returns the password for an account and where it came from. The
// environment wins, like it does over the config file, and it means the
// keychain isn't touched where there is none.
func Password(email string) (password, source string, err error) {
	if password := os.Getenv(PasswordEnv); password != "" {
		return password, SourceEnvironment, nil
	}
	password, err = keyring.Get(keyringService, email)
	if errors.Is(err, keyring.ErrNotFound) {
		return "", "", ErrNoPassword
	}
	if err != nil {
		return "", "", fmt.Errorf("read keychain: %w", err)
	}
	return password, SourceKeychain, nil
}

// SavePassword stores the password in the keychain.
func SavePassword(email, password string) error {
	if err := keyring.Set(keyringService, email, password); err != nil {
		return fmt.Errorf("write keychain: %w", err)
	}
	return nil
}

// DeletePassword removes the password from the keychain, if it is there.
func DeletePassword(email string) error {
	if err := keyring.Delete(keyringService, email); err != nil && !errors.Is(err, keyring.ErrNotFound) {
		return fmt.Errorf("delete from keychain: %w", err)
	}
	return nil
}
