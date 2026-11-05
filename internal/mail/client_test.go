package mail

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/robinvdvleuten/pim-mcp/internal/transport"
)

func TestDialRefusesPlainIMAPOffLoopback(t *testing.T) {
	addrs := []string{
		"mail.example.org:143",
		"192.0.2.1:143",
		"localhost.example.org:143",
	}

	for _, addr := range addrs {
		t.Run(addr, func(t *testing.T) {
			_, err := Dial(t.Context(), Config{Addr: addr, Security: SecurityNone})

			if !errors.Is(err, transport.ErrInsecure) {
				t.Errorf("err = %v, want transport.ErrInsecure", err)
			}
		})
	}
}

func TestDialStopsWhenContextEndsDuringGreeting(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		// Accept and stay silent, like a stalled server.
		conn, err := listener.Accept()
		if err == nil {
			t.Cleanup(func() { _ = conn.Close() })
		}
	}()

	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()

	_, err = Dial(ctx, Config{Addr: listener.Addr().String(), Security: SecurityNone})

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want context.DeadlineExceeded", err)
	}
}
