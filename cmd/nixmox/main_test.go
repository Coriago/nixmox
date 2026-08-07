package main

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"nixmox/internal/config"
)

func TestRootHelp(t *testing.T) {
	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetErr(buf)
	rootCmd.SetArgs([]string{"--help"})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("Execute help: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "nixmox") {
		t.Errorf("help output missing 'nixmox': %q", out)
	}
	if !strings.Contains(out, "serve") {
		t.Errorf("help output missing 'serve' subcommand: %q", out)
	}
	if !strings.Contains(out, "version") {
		t.Errorf("help output missing 'version' subcommand: %q", out)
	}
}

func TestVersion(t *testing.T) {
	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetErr(buf)
	rootCmd.SetArgs([]string{"version"})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("Execute version: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "version:") {
		t.Errorf("version output missing 'version:': %q", out)
	}
	if !strings.Contains(out, "commit:") {
		t.Errorf("version output missing 'commit:': %q", out)
	}
}

func TestServe(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	addr := ln.Addr().String()
	ln.Close()

	prev := cfg
	t.Cleanup(func() { cfg = prev })
	cfg = &config.Config{DataDir: t.TempDir(), HTTPAddr: addr, LogLevel: "error"}

	ctx, cancel := context.WithCancel(context.Background())
	serveCmd.SetContext(ctx)
	t.Cleanup(func() { serveCmd.SetContext(context.Background()) })

	errCh := make(chan error, 1)
	go func() { errCh <- serveCmd.RunE(serveCmd, nil) }()

	healthURL := "http://" + addr + "/healthz"
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp, err := http.Get(healthURL)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("server did not start serving /healthz")
		}
		time.Sleep(20 * time.Millisecond)
	}

	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("serve returned error after cancel: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serve did not shut down after context cancel")
	}
}

func TestJoinStub(t *testing.T) {
	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetErr(buf)
	rootCmd.SetArgs([]string{"join", "192.168.1.1:6222"})

	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("join stub returned nil error, expected non-nil")
	}
	if !strings.Contains(err.Error(), "not yet implemented") {
		t.Errorf("join error missing 'not yet implemented': %v", err)
	}
	if !strings.Contains(err.Error(), "192.168.1.1:6222") {
		t.Errorf("join error missing peer address: %v", err)
	}
}

func TestStatusStub(t *testing.T) {
	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetErr(buf)
	rootCmd.SetArgs([]string{"status"})

	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("status stub returned nil error, expected non-nil")
	}
	if !strings.Contains(err.Error(), "not yet implemented") {
		t.Errorf("status error missing 'not yet implemented': %v", err)
	}
}
