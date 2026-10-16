package server

import (
	"encoding/json"
	"fmt"
	"nixmox/internal/jobs"
	"strings"
	"testing"
)

func TestLogCatchupUsesByteOffsetsAndBoundedTail(t *testing.T) {
	s := operationServer(t)
	if err := s.Store.Create(jobs.Job{ID: "log-stream", Host: "local"}); err != nil {
		t.Fatal(err)
	}
	text := strings.Repeat("café 🔧\n", 10000)
	if err := s.Store.AppendLog("log-stream", text); err != nil {
		t.Fatal(err)
	}
	read := func(offset int64) jobs.LogChunk {
		t.Helper()
		w := call(s, "GET", fmt.Sprintf("/api/v1/hosts/local/jobs/log-stream/log/chunk?offset=%d", offset), "")
		var chunk jobs.LogChunk
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		if err := json.Unmarshal(w.Body.Bytes(), &chunk); err != nil {
			t.Fatal(err)
		}
		return chunk
	}
	tail := read(-1)
	if !tail.Reset || len(tail.Data) != 32<<10 || tail.Next != int64(len(text)) || string(tail.Data) != text[tail.Offset:] {
		t.Fatal("invalid initial tail")
	}
	suffix := "new output\n"
	s.Store.AppendLog("log-stream", suffix)
	next := read(tail.Next)
	if next.Reset || string(next.Data) != suffix || next.Offset != tail.Next {
		t.Fatal("catch-up duplicated old output")
	}
	if len(read(next.Next).Data) != 0 {
		t.Fatal("end of log repeated data")
	}
	if !read(next.Next + 100).Reset {
		t.Fatal("truncated log did not reset offset")
	}
	var all []byte
	for offset := int64(0); offset < next.Next; {
		chunk := read(offset)
		all = append(all, chunk.Data...)
		offset = chunk.Next
	}
	if string(all) != text+suffix {
		t.Fatal("byte chunks lost multibyte output")
	}
	if w := call(s, "GET", "/api/v1/hosts/local/jobs/log-stream/log/chunk?offset=-2", ""); w.Code != 400 {
		t.Fatal(w.Code)
	}
}
