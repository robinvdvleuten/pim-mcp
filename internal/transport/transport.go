// Package transport holds the rule for when a connection may be unencrypted.
package transport

import (
	"errors"
	"net"
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
