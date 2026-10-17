package server

import (
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestOnboardingBrowser(t *testing.T) {
	if os.Getenv("NIXMOX_BROWSER_TEST") != "1" {
		t.Skip("set NIXMOX_BROWSER_TEST=1 in the dev shell for Chromium workflow coverage")
	}
	s := operationServer(t)
	httpServer := httptest.NewServer(s.Mux)
	defer httpServer.Close()
	browser, err := exec.LookPath("chromium")
	if err != nil {
		t.Fatal(err)
	}
	artifacts := os.Getenv("NIXMOX_TEST_ARTIFACTS")
	if artifacts == "" {
		artifacts = t.TempDir()
	}
	script, err := filepath.Abs("../../e2e/onboarding_browser.py")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("python3", script, httpServer.URL, artifacts)
	cmd.Env = append(os.Environ(), "NIXMOX_E2E_CHROMIUM="+browser)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("browser workflow: %v\n%s", err, output)
	}
}
