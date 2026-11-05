package transport

import (
	"net/url"
	"testing"
)

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

func TestCheckURL(t *testing.T) {
	tests := map[string]bool{
		"https://dav.example.org/":  true,
		"http://127.0.0.1:5232/":    true,
		"http://localhost:5232/":    true,
		"http://dav.example.org/":   false,
		"http://localhost.evil.org": false,
		"ftp://dav.example.org/":    false,
		"dav.example.org":           false,
	}

	for raw, want := range tests {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		if got := CheckURL(u) == nil; got != want {
			t.Errorf("CheckURL(%q) allowed = %v, want %v", raw, got, want)
		}
	}
}
