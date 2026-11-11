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

// ErrEnvSeveralAccounts means the environment password was refused. It is one
// password, and with several accounts it would go to the servers of each.
var ErrEnvSeveralAccounts = fmt.Errorf("%s holds one password, so it only works with a single account: unset it and use the keychain", PasswordEnv)

// CheckEnvForNewAccount refuses to let the environment password serve an
// account that would be one of several. existing is how many accounts are
// configured already.
func CheckEnvForNewAccount(existing int) error {
	if existing >= 1 && os.Getenv(PasswordEnv) != "" {
		return ErrEnvSeveralAccounts
	}
	return nil
}

// Password returns the password for an account and where it came from. accounts
// is how many accounts are configured. The environment wins when there is just
// one, like it does over the config file, and it means the keychain isn't
// touched where there is none. With several accounts the environment password
// is never used, as it would be sent to the servers of every one of them, and
// asking while it is set is an error rather than a quiet fallback to the
// keychain.
func Password(email string, accounts int) (password, source string, err error) {
	if os.Getenv(PasswordEnv) != "" {
		if accounts > 1 {
			return "", "", ErrEnvSeveralAccounts
		}
		return os.Getenv(PasswordEnv), SourceEnvironment, nil
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
