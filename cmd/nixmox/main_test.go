package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"nixmox/internal/config"
	"nixmox/internal/domain"
	"nixmox/internal/httpapi"
	"nixmox/internal/network"
	"nixmox/internal/service"
	"nixmox/internal/storage"
)

func TestCLIHTTPWorkflow(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	st, err := storage.Open(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	s, err := service.Open(st, network.Inspector{Config: config.Default()})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	server := httptest.NewServer(httpapi.New(s))
	defer server.Close()
	var out, errs bytes.Buffer
	args := []string{"--endpoint", server.URL, "network", "check", "--wait", "--json", "--command-id", "cmd_cli"}
	code := run(context.Background(), args, &out, &errs)
	if code != 0 {
		t.Fatal(code, errs.String())
	}
	var task domain.Task
	if err := json.Unmarshal(out.Bytes(), &task); err != nil || task.State != "succeeded" || task.CommandID != "cmd_cli" {
		t.Fatal(out.String(), err)
	}
	out.Reset()
	errs.Reset()
	if code := run(context.Background(), args, &out, &errs); code != 0 || len(s.Tasks()) != 1 {
		t.Fatal("retry duplicated", code, errs.String())
	}
	out.Reset()
	errs.Reset()
	if code := run(context.Background(), []string{"--endpoint", server.URL, "tasks", "cancel", task.ID}, &out, &errs); code != 2 {
		t.Fatal("accepted missing reviewed version", code)
	}
	out.Reset()
	errs.Reset()
	if code := run(context.Background(), []string{"--endpoint", server.URL, "network", "check", "--json", "--unexpected"}, &out, &errs); code != 2 {
		t.Fatal("unknown flag", code)
	}
}
