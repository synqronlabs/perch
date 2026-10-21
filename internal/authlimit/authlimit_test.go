package authlimit

import (
	"testing"
	"time"
)

func TestLimiter(t *testing.T) {
	l := New(2, time.Minute)
	key := "127.0.0.1|user"
	if !l.Allowed(key) {
		t.Fatal("fresh key was blocked")
	}
	l.Failure(key)
	l.Failure(key)
	if l.Allowed(key) {
		t.Fatal("key was not blocked after the limit")
	}
	l.Success(key)
	if !l.Allowed(key) {
		t.Fatal("success did not clear the counter")
	}
}
