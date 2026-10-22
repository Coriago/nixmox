package tests

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"nixmox/internal/client"
	"nixmox/internal/domain"
)

func TestCompiledDaemonRestartAndPortConflict(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "nixmox")
	build := exec.Command("go", "build", "-o", binary, "./cmd/nixmox")
	build.Dir = ".."
	if b, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %s %v", b, err)
	}
	reserve, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := reserve.Addr().(*net.TCPAddr).Port
	reserve.Close()
	cfg := filepath.Join(root, "config.yaml")
	data := filepath.Join(root, "state")
	if err := os.WriteFile(cfg, []byte(fmt.Sprintf("schema_version: 1\ndata_dir: %s\nhttp:\n  port: %d\n", data, port)), 0600); err != nil {
		t.Fatal(err)
	}
	endpoint := "http://127.0.0.1:" + strconv.Itoa(port)
	c, err := client.New(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	c.HTTP.Timeout = 250 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var process *exec.Cmd
	var output bytes.Buffer
	start := func() {
		output.Reset()
		process = exec.CommandContext(ctx, binary, "serve", "--config", cfg)
		process.Stdout = &output
		process.Stderr = &output
		if err := process.Start(); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if _, err := c.Identity(ctx); err == nil {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("daemon did not become reachable")
	}
	stop := func(kill bool) {
		if process == nil {
			return
		}
		if kill {
			_ = process.Process.Kill()
		} else {
			_ = process.Process.Signal(syscall.SIGTERM)
		}
		_ = process.Wait()
		process = nil
	}
	defer func() { stop(true) }()
	start()
	identity, err := c.Identity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cli := exec.CommandContext(ctx, binary, "--endpoint", endpoint, "network", "check", "--wait", "--json", "--command-id", "cmd_e2e")
	cli.Env = append(os.Environ(), "XDG_STATE_HOME="+filepath.Join(root, "client"))
	b, err := cli.Output()
	if err != nil {
		t.Fatal(string(b), err)
	}
	var task domain.Task
	if err := json.Unmarshal(b, &task); err != nil || task.State != "succeeded" {
		t.Fatal(string(b), err)
	}
	second := exec.CommandContext(ctx, binary, "serve", "--config", cfg)
	b, err = second.CombinedOutput()
	if err == nil || !strings.Contains(string(b), "already in use") {
		t.Fatal("failed exclusion", string(b), err)
	}
	stop(true)
	start()
	restored, err := c.Identity(ctx)
	if err != nil || restored.NodeID != identity.NodeID || restored.PublicKey != identity.PublicKey {
		t.Fatal("identity changed", restored, err)
	}
	recovered, err := c.Task(ctx, task.ID)
	if err != nil || recovered.State != "succeeded" || recovered.Version != task.Version {
		t.Fatal("lost terminal result", recovered, err)
	}
	cli = exec.CommandContext(ctx, binary, "--endpoint", endpoint, "network", "check", "--wait", "--json", "--command-id", "cmd_e2e")
	cli.Env = append(os.Environ(), "XDG_STATE_HOME="+filepath.Join(root, "client"))
	b, err = cli.Output()
	if err != nil {
		t.Fatal(string(b), err)
	}
	var retry domain.Task
	if json.Unmarshal(b, &retry) != nil || retry.ID != task.ID {
		t.Fatal("replayed effect", string(b))
	}
	response, err := http.Get(endpoint + "/ui/network")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatal(response.StatusCode)
	}
	stop(false)
	occupied, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	conflict := exec.CommandContext(ctx, binary, "serve", "--config", cfg)
	b, err = conflict.CombinedOutput()
	if err == nil || !strings.Contains(string(b), "http.port") || strings.Contains(string(b), "Nixmox ready") {
		t.Fatal("bind conflict not explicit", string(b), err)
	}
}
