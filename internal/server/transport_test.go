package server

import "testing"

func TestMessageWriterRejectsOversizeResponse(t *testing.T) {
	w := &messageWriter{limit: 4}
	if _, err := w.Write([]byte("123")); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("45")); err == nil {
		t.Fatal("unbounded response accepted")
	}
	if !w.exceeded || w.body.String() != "123" {
		t.Fatal("response silently truncated")
	}
}
