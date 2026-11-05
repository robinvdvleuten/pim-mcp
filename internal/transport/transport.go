// Package transport holds the rule for when a connection may be unencrypted.
package transport

import (
	"errors"
	"fmt"
	"net"
	"net/url"
)

// ErrInsecure means an unencrypted connection was requested for a host that
// isn't loopback.
var ErrInsecure = errors.New("unencrypted connections are only allowed on loopback")

// IsLoopback reports whether host is this machine, as a name or an address.
func IsLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// CheckURL refuses a URL that would send credentials unencrypted.
func CheckURL(u *url.URL) error {
	switch {
	case u.Scheme == "https":
		return nil
	case u.Scheme == "http" && IsLoopback(u.Hostname()):
		return nil
	case u.Scheme == "http":
		return fmt.Errorf("connect to %s: %w", u.Hostname(), ErrInsecure)
	default:
		return fmt.Errorf("unsupported URL scheme %q", u.Scheme)
	}
}
