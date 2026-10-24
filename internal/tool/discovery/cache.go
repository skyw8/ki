package discovery

import (
	"reflect"
	"slices"
	"sync"

	toolapi "ki/internal/tool"
)

type CacheIdentity interface {
	DiscoveryCacheIdentity() any
}

type cachedCatalog struct {
	tools   []toolapi.Tool
	catalog *Catalog
	bytes   int
}

type Cache struct {
	mu      sync.Mutex
	entries []cachedCatalog
	bytes   int
}

func (c *Cache) Get(tools []toolapi.Tool) *Catalog {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i, cached := range c.entries {
		if sameTools(cached.tools, tools) {
			copy(c.entries[i:], c.entries[i+1:])
			c.entries[len(c.entries)-1] = cached
			return cached.catalog
		}
	}
	catalog := New(tools)
	bytes := 0
	for _, e := range catalog.entries {
		bytes += len(e.raw) + e.length*32
	}
	if bytes > 32<<20 {
		return catalog
	}
	for len(c.entries) > 0 && (len(c.entries) >= 8 || c.bytes+bytes > 32<<20) {
		c.bytes -= c.entries[0].bytes
		copy(c.entries, c.entries[1:])
		c.entries[len(c.entries)-1] = cachedCatalog{}
		c.entries = c.entries[:len(c.entries)-1]
	}
	c.entries = append(c.entries, cachedCatalog{tools: slices.Clone(tools), catalog: catalog, bytes: bytes})
	c.bytes += bytes
	return catalog
}

func sameTools(a, b []toolapi.Tool) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		left, leftOK := a[i].(CacheIdentity)
		right, rightOK := b[i].(CacheIdentity)
		if !leftOK || !rightOK || a[i].Name() != b[i].Name() {
			return false
		}
		x, y := left.DiscoveryCacheIdentity(), right.DiscoveryCacheIdentity()
		if x == nil || y == nil || reflect.TypeOf(x) != reflect.TypeOf(y) || !reflect.TypeOf(x).Comparable() || x != y {
			return false
		}
	}
	return true
}
