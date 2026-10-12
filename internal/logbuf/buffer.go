// Package logbuf keeps a bounded daemon log tail for the portal. The journal
// remains the durable log store; this buffer never grows with daemon uptime.
package logbuf

import "sync"

type Buffer struct {
	mu    sync.Mutex
	data  []byte
	limit int
}

func New(limit int) *Buffer {
	if limit < 1 {
		limit = 1
	}
	return &Buffer{limit: limit}
}
func (b *Buffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	if len(p) >= b.limit {
		b.data = append(b.data[:0], p[len(p)-b.limit:]...)
	} else {
		overflow := len(b.data) + len(p) - b.limit
		if overflow > 0 {
			copy(b.data, b.data[overflow:])
			b.data = b.data[:len(b.data)-overflow]
		}
		b.data = append(b.data, p...)
	}
	return n, nil
}
func (b *Buffer) String() string { b.mu.Lock(); defer b.mu.Unlock(); return string(b.data) }
