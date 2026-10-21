package blobcache

import "testing"

func TestLRU(t *testing.T) {
	cache := New(10)
	cache.Put("a", []byte("12345"))
	cache.Put("b", []byte("67890"))
	if _, ok := cache.Get("a"); !ok {
		t.Fatal("a missing")
	}
	cache.Put("c", []byte("abcde"))
	if _, ok := cache.Get("b"); ok {
		t.Fatal("b should have been evicted")
	}
	if _, ok := cache.Get("a"); !ok {
		t.Fatal("a should remain")
	}
	if cache.Used() > 10 {
		t.Fatalf("used = %d", cache.Used())
	}
}
