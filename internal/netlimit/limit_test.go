package netlimit

import (
	"net"
	"testing"
	"time"
)

func TestLimiterBlocksUntilClose(t *testing.T) {
	base, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	limiter := New(1)
	defer limiter.Close()
	listener := limiter.Wrap(base)
	defer func() { _ = listener.Close() }()

	client1, err := net.Dial("tcp", base.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	accepted1, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}

	client2, err := net.Dial("tcp", base.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client2.Close() }()
	done := make(chan net.Conn, 1)
	go func() {
		conn, err := listener.Accept()
		if err == nil {
			done <- conn
		}
	}()
	select {
	case conn := <-done:
		_ = conn.Close()
		t.Fatal("second connection was accepted before the first closed")
	case <-time.After(100 * time.Millisecond):
	}
	_ = client1.Close()
	_ = accepted1.Close()
	select {
	case conn := <-done:
		_ = conn.Close()
	case <-time.After(2 * time.Second):
		t.Fatal("second connection was not accepted after release")
	}
}
