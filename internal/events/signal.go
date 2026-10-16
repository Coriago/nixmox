// Package events provides coalescing invalidation signals, not an event log.
package events

import "sync"

type Signal struct {
	mu   sync.Mutex
	next chan struct{}
}

// Watch returns a generation that closes on the next change. Subscribe before
// reading state so a concurrent change cannot be lost between read and wait.
func (s *Signal) Watch() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.next == nil {
		s.next = make(chan struct{})
	}
	return s.next
}

func (s *Signal) Notify() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.next != nil {
		close(s.next)
	}
	s.next = make(chan struct{})
}
