package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestVersionString(t *testing.T) {
	const revision = "ad5c02c0123456789"

	tests := []struct {
		name string
		info *debug.BuildInfo
		want string
	}{
		{
			name: "clean checkout reports the revision",
			info: &debug.BuildInfo{Settings: []debug.BuildSetting{
				{Key: "vcs.revision", Value: revision},
				{Key: "vcs.modified", Value: "false"},
			}},
			want: revision,
		},
		{
			name: "modified checkout is marked dirty",
			info: &debug.BuildInfo{Settings: []debug.BuildSetting{
				{Key: "vcs.revision", Value: revision},
				{Key: "vcs.modified", Value: "true"},
			}},
			want: revision + "-dirty",
		},
		{
			name: "go install falls back to the module version",
			info: &debug.BuildInfo{Main: debug.Module{Version: "v0.0.0-20261105-ad5c02c"}},
			want: "v0.0.0-20261105-ad5c02c",
		},
		{
			name: "no version information",
			info: &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}},
			want: "unknown",
		},
		{
			name: "no build info",
			info: nil,
			want: "unknown",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := versionString(tt.info); got != tt.want {
				t.Errorf("versionString() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRunVersionPrintsToStdout(t *testing.T) {
	var stdout, stderr bytes.Buffer

	code := run(context.Background(), []string{"version"}, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr.String())
	}
	if !strings.HasPrefix(stdout.String(), "pim-mcp ") {
		t.Errorf("stdout = %q, want it to start with %q", stdout.String(), "pim-mcp ")
	}
}

func TestRunRejectsBadUsage(t *testing.T) {
	tests := map[string][]string{
		"no command":      {},
		"unknown command": {"nonsense"},
		"version arg":     {"version", "extra"},
		"serve arg":       {"serve", "extra"},
		"serve bad flag":  {"serve", "--nonsense"},
	}

	for name, args := range tests {
		t.Run(name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer

			code := run(context.Background(), args, &stdout, &stderr)

			if code != 2 {
				t.Errorf("exit code = %d, want 2", code)
			}
			if !strings.Contains(stderr.String(), "usage:") {
				t.Errorf("stderr = %q, want usage text", stderr.String())
			}
			if stdout.Len() != 0 {
				t.Errorf("stdout = %q, want it empty", stdout.String())
			}
		})
	}
}

func buildBinary(t *testing.T) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "pim-mcp")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	return binary
}

// isolatedEnv points the binary at an empty home, with a password that keeps it
// away from the real keychain.
func isolatedEnv(t *testing.T) []string {
	t.Helper()
	home := t.TempDir()
	return append(os.Environ(), "HOME="+home, "XDG_CONFIG_HOME="+filepath.Join(home, "config"), "PIM_MCP_PASSWORD="+secretPassword)
}

const secretPassword = "stdio-test-secret" //nolint:gosec // Made up for this test.

func connectBinary(t *testing.T, cmd *exec.Cmd) *mcp.ClientSession {
	t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "test"}, nil)
	session, err := client.Connect(context.Background(), &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	return session
}

// TestServeSpeaksMCPOverStdio runs the real binary, because stdout carrying
// nothing but the protocol can only be proven across a process boundary.
func TestServeSpeaksMCPOverStdio(t *testing.T) {
	var stderr bytes.Buffer
	cmd := exec.Command(buildBinary(t), "serve")
	cmd.Env = isolatedEnv(t)
	cmd.Stderr = &stderr

	session := connectBinary(t, cmd)

	if _, err := session.ListTools(context.Background(), nil); err != nil {
		t.Fatalf("list tools: %v", err)
	}
	if name := session.InitializeResult().ServerInfo.Name; name != "pim-mcp" {
		t.Errorf("server name = %q, want %q", name, "pim-mcp")
	}

	if err := session.Close(); err != nil {
		t.Errorf("close: %v", err)
	}
	if !strings.Contains(stderr.String(), "starting server") {
		t.Errorf("stderr = %q, want the startup log line", stderr.String())
	}
}

func callAccountInfo(t *testing.T, env []string, args ...string) (string, bool) {
	t.Helper()
	var stderr bytes.Buffer
	cmd := exec.Command(buildBinary(t), append([]string{"serve"}, args...)...)
	cmd.Env = env
	cmd.Stderr = &stderr
	session := connectBinary(t, cmd)

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "account_info"})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	// The process writes to stderr until it has exited.
	if err := session.Close(); err != nil {
		t.Errorf("close: %v", err)
	}
	if strings.Contains(string(data)+stderr.String(), secretPassword) {
		t.Errorf("output contains the password: %s %s", data, stderr.String())
	}
	return string(data), result.IsError
}

func TestServeReportsTheAccountWithoutItsPassword(t *testing.T) {
	env := isolatedEnv(t)
	add := exec.Command(buildBinary(t), "account", "add", "--email", "me@example.org",
		"--imap", "mail.example.org:993", "--caldav", "https://dav.example.org/")
	add.Env = env
	if out, err := add.CombinedOutput(); err != nil {
		t.Fatalf("add: %v\n%s", err, out)
	}

	tests := []struct {
		name string
		args []string
		env  []string
		want string
	}{
		{name: "writable", want: `"read_only":false`},
		{name: "flag", args: []string{"--read-only"}, want: `"read_only":true`},
		{name: "environment", env: []string{"PIM_MCP_READ_ONLY=1"}, want: `"read_only":true`},
		{name: "environment off", env: []string{"PIM_MCP_READ_ONLY=0"}, want: `"read_only":false`},
		{name: "flag beats environment", args: []string{"--read-only=false"}, env: []string{"PIM_MCP_READ_ONLY=1"}, want: `"read_only":false`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			output, isError := callAccountInfo(t, append(slices.Clone(env), tt.env...), tt.args...)

			if isError {
				t.Fatalf("tool error: %s", output)
			}
			if !strings.Contains(output, tt.want) || !strings.Contains(output, "me@example.org") {
				t.Errorf("output = %s, want %s", output, tt.want)
			}
		})
	}
}

func TestServeWithoutAccountTellsTheAgentWhatToDo(t *testing.T) {
	output, isError := callAccountInfo(t, isolatedEnv(t))

	if !isError || !strings.Contains(output, "account add") {
		t.Errorf("error = %v, output = %s", isError, output)
	}
}

func TestServeRefusesUnknownToolNames(t *testing.T) {
	var stdout, stderr bytes.Buffer

	code := run(context.Background(), []string{"serve", "--tools", "mail_serach"}, &stdout, &stderr)

	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "mail_serach") || !strings.Contains(stderr.String(), "mail_search") {
		t.Errorf("stderr = %q", stderr.String())
	}
}
