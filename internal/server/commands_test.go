package server

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGuardedCommandsUseJobRunner(t *testing.T) {
	s := operationServer(t)
	s.Config.EnablePower = true
	s.Config.EnableProvision = true
	bin := t.TempDir()
	for _, command := range []string{"shutdown", "nixos-anywhere"} {
		if err := os.WriteFile(filepath.Join(bin, command), []byte("#!/bin/sh\nprintf '%s\\n' \"$@\"\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	r := httptest.NewRequest("POST", "/api/v1/hosts/local/actions/shutdown", strings.NewReader("confirm=local"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	s.Mux.ServeHTTP(w, r)
	if w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	s.Runner.Stop()
	w = call(s, "POST", "/api/v1/provision", `{"target":"root@192.0.2.1","confirm":"root@192.0.2.1","flake":"github:example/config#node"}`)
	if w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	s.Runner.Stop()
	list, err := s.Store.List()
	if err != nil || len(list) != 2 {
		t.Fatal(list, err)
	}
	for _, j := range list {
		if j.State != "applied" {
			t.Fatal(j)
		}
		log, err := os.ReadFile(j.LogPath)
		if err != nil {
			t.Fatal(err)
		}
		if j.Kind == "shutdown" && !strings.Contains(string(log), "-h\n+1\n") {
			t.Fatal(string(log))
		}
		if j.Kind == "provision" && !strings.Contains(string(log), "--target-host\nroot@192.0.2.1") {
			t.Fatal(string(log))
		}
	}
}
