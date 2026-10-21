// Package netlimit bounds the number of accepted network connections.
package netlimit

import (
	"net"
	"sync"
)

// Limiter caps concurrent connections across one or more listeners. A zero or
// negative limit disables limiting.
type Limiter struct {
	slots chan struct{}
	done  chan struct{}
	once  sync.Once
}

// New returns a limiter for max concurrent connections.
func New(max int) *Limiter {
	l := &Limiter{done: make(chan struct{})}
	if max > 0 {
		l.slots = make(chan struct{}, max)
	}
	return l
}

// Wrap returns a listener that acquires a slot before Accept and releases it
// when the returned connection closes.
func (l *Limiter) Wrap(ln net.Listener) net.Listener {
	if l == nil {
		return ln
	}
	return &limitListener{Listener: ln, limiter: l}
}

// Close unblocks listeners waiting for a connection slot.
func (l *Limiter) Close() {
	if l == nil {
		return
	}
	l.once.Do(func() { close(l.done) })
}

func (l *Limiter) acquire() bool {
	if l.slots == nil {
		return true
	}
	select {
	case l.slots <- struct{}{}:
		return true
	case <-l.done:
		return false
	}
}

func (l *Limiter) release() {
	if l.slots != nil {
		<-l.slots
	}
}

type limitListener struct {
	net.Listener
	limiter *Limiter
}

func (l *limitListener) Accept() (net.Conn, error) {
	if !l.limiter.acquire() {
		return nil, net.ErrClosed
	}
	conn, err := l.Listener.Accept()
	if err != nil {
		l.limiter.release()
		return nil, err
	}
	return &limitedConn{Conn: conn, release: l.limiter.release}, nil
}

type limitedConn struct {
	net.Conn
	release func()
	once    sync.Once
}

func (c *limitedConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(c.release)
	return err
}
