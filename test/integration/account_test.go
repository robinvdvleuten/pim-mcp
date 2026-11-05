//go:build integration

package integration

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func buildBinary(t *testing.T) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "pim-mcp")
	cmd := exec.Command("go", "build", "-o", binary, "github.com/robinvdvleuten/pim-mcp/cmd/pim-mcp")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	return binary
}

// runBinary keeps the real keychain out of it, through the environment password.
func runBinary(t *testing.T, binary, home, password string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(binary, args...)
	cmd.Env = append(os.Environ(), "HOME="+home, "XDG_CONFIG_HOME="+filepath.Join(home, "config"), "PIM_MCP_PASSWORD="+password)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	return out.String(), err
}

func TestAccountTestAgainstRealServers(t *testing.T) {
	binary := buildBinary(t)

	for _, s := range caldavServers {
		t.Run(s.name, func(t *testing.T) {
			if s.setup != nil {
				s.setup(t, s)
			}
			home := t.TempDir()
			if out, err := runBinary(t, binary, home, caldavPassword, "account", "add",
				"--email", "tester@example.org", "--username", caldavUser,
				"--imap", dovecotPlainAddr, "--imap-security", "none", "--caldav", s.url); err != nil {
				t.Fatalf("add: %v\n%s", err, out)
			}

			out, err := runBinary(t, binary, home, caldavPassword, "account", "test")
			if err != nil {
				t.Errorf("right password: %v\n%s", err, out)
			}
			if !strings.Contains(out, "imap:   ok") || !strings.Contains(out, "caldav: ok") {
				t.Errorf("right password: output = %q", out)
			}

			out, err = runBinary(t, binary, home, "wrong-password", "account", "test")
			if err == nil {
				t.Errorf("wrong password: succeeded\n%s", out)
			}
			if !strings.Contains(out, "imap:   failed") || !strings.Contains(out, "caldav: failed") {
				t.Errorf("wrong password: output = %q", out)
			}
			if strings.Contains(out, "wrong-password") {
				t.Errorf("wrong password: output contains it: %q", out)
			}
		})
	}
}

func TestAccountTestAgainstUnreachableServers(t *testing.T) {
	binary := buildBinary(t)
	home := t.TempDir()
	if out, err := runBinary(t, binary, home, caldavPassword, "account", "add",
		"--email", "tester@example.org", "--imap", "127.0.0.1:1", "--imap-security", "none",
		"--caldav", "http://127.0.0.1:1/"); err != nil {
		t.Fatalf("add: %v\n%s", err, out)
	}

	out, err := runBinary(t, binary, home, caldavPassword, "account", "test")

	if err == nil || !strings.Contains(out, "imap:   failed") || !strings.Contains(out, "caldav: failed") {
		t.Errorf("err = %v, output = %q", err, out)
	}
}
