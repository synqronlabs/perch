package netutil

import "testing"

func TestHost(t *testing.T) {
	cases := map[string]string{
		"127.0.0.1:143": "127.0.0.1",
		"[::1]:993":     "::1",
		"localhost:587": "localhost",
		":8080":         "",
		"example.com":   "example.com",
	}
	for addr, want := range cases {
		if got := Host(addr); got != want {
			t.Errorf("Host(%q) = %q, want %q", addr, got, want)
		}
	}
}

func TestIsLoopback(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:143", "[::1]:993", "localhost:587", "127.0.0.1"} {
		if !IsLoopback(addr) {
			t.Errorf("IsLoopback(%q) = false, want true", addr)
		}
	}
	for _, addr := range []string{"", ":143", "0.0.0.0:143", "example.com:143"} {
		if IsLoopback(addr) {
			t.Errorf("IsLoopback(%q) = true, want false", addr)
		}
	}
}
