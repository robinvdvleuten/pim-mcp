package mcpserver

import (
	"context"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func connect(t *testing.T, server *mcp.Server) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()

	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("connect server: %v", err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "test"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("connect client: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func TestNewIdentifiesServerDuringInitialize(t *testing.T) {
	session := connect(t, New("abc123"))

	info := session.InitializeResult().ServerInfo

	if info.Name != "pim-mcp" {
		t.Errorf("server name = %q, want %q", info.Name, "pim-mcp")
	}
	if info.Version != "abc123" {
		t.Errorf("server version = %q, want %q", info.Version, "abc123")
	}
}

func TestNewHasNoToolsYet(t *testing.T) {
	session := connect(t, New("abc123"))

	result, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}

	if len(result.Tools) != 0 {
		t.Errorf("tools = %v, want none", result.Tools)
	}
}
