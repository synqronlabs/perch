// Package netutil contains small network-address helpers shared by the
// listeners and the command line.
package netutil

import (
	"net"
	"strings"
)

// Host returns the host portion of an address. Addresses without a port are
// returned unchanged.
func Host(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	return host
}

// IsLoopback reports whether addr is a loopback host, including the special
// name "localhost". An empty host (for example ":143") is not loopback.
func IsLoopback(addr string) bool {
	host := Host(addr)
	if host == "" {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
