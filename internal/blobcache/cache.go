// Package blobcache provides a bounded in-memory LRU for raw message bodies.
package blobcache

import (
	"container/list"
	"sync"
)

type entry struct {
	key  string
	data []byte
}

// Cache is a concurrency-safe byte-bounded LRU.
type Cache struct {
	mu    sync.Mutex
	max   int64
	used  int64
	order *list.List
	items map[string]*list.Element
}

// New returns a cache with the given byte budget. A non-positive budget
// disables caching.
func New(max int64) *Cache {
	return &Cache{max: max, order: list.New(), items: make(map[string]*list.Element)}
}

// Get returns a cached body and marks it recently used.
func (c *Cache) Get(key string) ([]byte, bool) {
	if c == nil || c.max <= 0 {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	element, ok := c.items[key]
	if !ok {
		return nil, false
	}
	c.order.MoveToFront(element)
	return element.Value.(*entry).data, true
}

// Put stores body under key, evicting least-recently-used entries as needed.
func (c *Cache) Put(key string, body []byte) {
	if c == nil || c.max <= 0 || key == "" || int64(len(body)) > c.max {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if element, ok := c.items[key]; ok {
		c.order.MoveToFront(element)
		return
	}
	element := c.order.PushFront(&entry{key: key, data: body})
	c.items[key] = element
	c.used += int64(len(body))
	for c.used > c.max {
		back := c.order.Back()
		if back == nil {
			break
		}
		c.remove(back)
	}
}

// Used returns the current cached byte count.
func (c *Cache) Used() int64 {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.used
}

func (c *Cache) remove(element *list.Element) {
	item := element.Value.(*entry)
	c.order.Remove(element)
	delete(c.items, item.key)
	c.used -= int64(len(item.data))
}
