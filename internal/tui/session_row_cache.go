package tui

// sessionRowCache is a tiny capped render cache for session/project rows.
// Bubble Tea asks delegates to render visible rows often; caching avoids
// repeating Lip Gloss styling and truncation work when the row identity,
// width, selection state, and active filter have not changed.
type sessionRowCache struct {
	items map[string]string
	order []string
	cap   int
}

func newSessionRowCache(capacity int) *sessionRowCache {
	if capacity <= 0 {
		capacity = 512
	}
	return &sessionRowCache{items: make(map[string]string, capacity), cap: capacity}
}

func (c *sessionRowCache) Get(key string) (string, bool) {
	if c == nil || c.items == nil {
		return "", false
	}
	v, ok := c.items[key]
	return v, ok
}

func (c *sessionRowCache) Set(key, value string) {
	if c == nil {
		return
	}
	if c.items == nil {
		c.items = make(map[string]string, c.cap)
	}
	if _, exists := c.items[key]; exists {
		c.items[key] = value
		return
	}
	if c.cap <= 0 {
		c.cap = 512
	}
	if len(c.order) >= c.cap {
		oldest := c.order[0]
		copy(c.order, c.order[1:])
		c.order = c.order[:len(c.order)-1]
		delete(c.items, oldest)
	}
	c.items[key] = value
	c.order = append(c.order, key)
}

func (c *sessionRowCache) Clear() {
	if c == nil {
		return
	}
	clear(c.items)
	c.order = c.order[:0]
}
