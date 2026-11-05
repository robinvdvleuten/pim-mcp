package main

import (
	"bytes"
	"context"
	"os/exec"
	"path/filepath"
	"runtime/debug"
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

// TestServeSpeaksMCPOverStdio runs the real binary, because stdout carrying
// nothing but the protocol can only be proven across a process boundary.
func TestServeSpeaksMCPOverStdio(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "pim-mcp")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}

	var stderr bytes.Buffer
	cmd := exec.Command(binary, "serve")
	cmd.Stderr = &stderr

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "test"}, nil)
	session, err := client.Connect(context.Background(), &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		t.Fatalf("connect: %v\nstderr: %s", err, stderr.String())
	}

	result, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	if len(result.Tools) != 0 {
		t.Errorf("tools = %v, want none", result.Tools)
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
