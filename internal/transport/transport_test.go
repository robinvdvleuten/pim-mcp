package transport

import "testing"

func TestIsLoopback(t *testing.T) {
	tests := map[string]bool{
		"localhost":             true,
		"127.0.0.1":             true,
		"127.0.0.53":            true,
		"::1":                   true,
		"localhost.example.org": false,
		"127.0.0.1.example.org": false,
		"mail.example.org":      false,
		"192.0.2.1":             false,
		"":                      false,
	}

	for host, want := range tests {
		if got := IsLoopback(host); got != want {
			t.Errorf("IsLoopback(%q) = %v, want %v", host, got, want)
		}
	}
}
