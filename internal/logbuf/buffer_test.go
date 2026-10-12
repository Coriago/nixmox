package logbuf

import (
	"sync"
	"testing"
)

func TestBoundedConcurrentWrites(t *testing.T) {
	b := New(10)
	b.Write([]byte("0123456789"))
	b.Write([]byte("abc"))
	if b.String() != "3456789abc" {
		t.Fatal(b.String())
	}
	b.Write([]byte("abcdefghijklm"))
	if b.String() != "defghijklm" {
		t.Fatal(b.String())
	}
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); b.Write([]byte("abc")); _ = b.String() }()
	}
	wg.Wait()
	if len(b.String()) > 10 {
		t.Fatal(b.String())
	}
}
