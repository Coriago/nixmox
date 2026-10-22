package httpapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nixmox/internal/client"
	"nixmox/internal/config"
	"nixmox/internal/domain"
	"nixmox/internal/network"
	"nixmox/internal/presentation"
	"nixmox/internal/service"
	"nixmox/internal/storage"
)

func fixture(t *testing.T) (*service.Service, *httptest.Server, *client.Client) {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	st, err := storage.Open(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	s, err := service.Open(st, network.Inspector{Config: config.Default()})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(New(s))
	c, err := client.New(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { server.Close(); s.Close(); st.Close() })
	return s, server, c
}
func TestHTTPAndFormAdmissionParity(t *testing.T) {
	s, server, c := fixture(t)
	ctx := context.Background()
	id := s.Identity().NodeID
	cmd := domain.Command{ID: "cmd_parity", Type: "network.check", TargetID: id, Payload: json.RawMessage(`{}`)}
	receipt, err := c.Submit(ctx, cmd)
	if err != nil {
		t.Fatal(err)
	}
	task, err := c.Wait(ctx, receipt.TaskID)
	if err != nil || task.State != "succeeded" {
		t.Fatal(task, err)
	}
	values := url.Values{"id": {cmd.ID}, "type": {cmd.Type}, "target_id": {cmd.TargetID}}
	req, _ := http.NewRequest("POST", server.URL+"/ui/commands", strings.NewReader(values.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 202 || !bytes.Contains(body, []byte(receipt.TaskID)) || len(s.Tasks()) != 1 {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	resp, err = server.Client().Get(server.URL + "/ui/network")
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if !bytes.Contains(body, []byte(presentation.Network(s.Network()))) || !bytes.Contains(body, []byte("/assets/htmx.min.js")) {
		t.Fatalf("missing shared presentation: %s", body)
	}
	req, _ = http.NewRequest("GET", server.URL+"/ui/network", nil)
	req.Header.Set("HX-Request", "true")
	resp, _ = server.Client().Do(req)
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if bytes.Contains(body, []byte("<!doctype")) || resp.Header.Get("Vary") != "HX-Request" {
		t.Fatal("wrong fragment")
	}
	var read domain.Receipt
	if err := c.Read(ctx, "/api/v1/commands/"+cmd.ID, &read); err != nil || read.TaskID != receipt.TaskID {
		t.Fatal(read, err)
	}
}
func TestHTTPValidationAndNoMachineRoutes(t *testing.T) {
	s, server, _ := fixture(t)
	for _, tc := range []struct {
		path, body string
		want       int
	}{
		{"/api/v1/commands", `{"id":"x","id":"y"}`, 400},
		{"/api/v1/commands", `{"id":"cmd_bad","type":"network.check","target_id":"` + s.Identity().NodeID + `","payload":{"password":"secret-sentinel"}}`, 422},
		{"/api/v1/commands", `{"id":"cmd_unknown","type":"network.check","target_id":"` + s.Identity().NodeID + `","payload":{},"scope":"node_local"}`, 400},
		{"/api/v1/commands", strings.Repeat("x", domain.MaxCommandBytes+1), 400},
	} {
		resp, err := server.Client().Post(server.URL+tc.path, "application/json", strings.NewReader(tc.body))
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != tc.want || bytes.Contains(body, []byte("secret-sentinel")) {
			t.Fatalf("%d %s", resp.StatusCode, body)
		}
	}
	for _, path := range []string{"/api/v1/tasks?limit=501", "/api/v1/tasks?unknown=1", "/api/v1/events?topics=hosts.telemetry", "/api/v1/events?task_id=task_1", "/api/v1/events?topics=tasks&topics=identity"} {
		resp, err := server.Client().Get(server.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 400 {
			t.Fatal(path, resp.StatusCode)
		}
	}
	for _, path := range []string{"/store-cache/abc", "/machine/enroll"} {
		resp, _ := server.Client().Get(server.URL + path)
		resp.Body.Close()
		if resp.StatusCode != 404 {
			t.Fatal("exposed machine route")
		}
	}
	req, _ := http.NewRequest("POST", server.URL+"/api/v1/commands", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "http://attacker.example")
	resp, _ := server.Client().Do(req)
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatal("cross-origin accepted")
	}
}
func TestSSESnapshotAndReconnect(t *testing.T) {
	s, server, c := fixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	open := func() (*http.Response, *bufio.Scanner) {
		req, _ := http.NewRequestWithContext(ctx, "GET", server.URL+"/api/v1/events?topics=tasks,network.status", nil)
		resp, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp, bufio.NewScanner(resp.Body)
	}
	read := func(scan *bufio.Scanner) string {
		for scan.Scan() {
			if strings.HasPrefix(scan.Text(), "data: ") {
				return scan.Text()
			}
		}
		t.Fatal("no snapshot", scan.Err())
		return ""
	}
	resp, scan := open()
	if first := read(scan); !strings.Contains(first, `"type":"snapshot"`) {
		t.Fatal(first)
	}
	r, err := c.Submit(ctx, domain.Command{ID: "cmd_stream", Type: "network.check", TargetID: s.Identity().NodeID, Payload: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Wait(ctx, r.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for range 5 {
		if strings.Contains(read(scan), r.TaskID) {
			found = true
			break
		}
	}
	resp.Body.Close()
	if !found {
		t.Fatal("no task update")
	}
	resp, scan = open()
	defer resp.Body.Close()
	if got := read(scan); !strings.Contains(got, `"state":"succeeded"`) {
		t.Fatal("reconnect lost final state", got)
	}
}
func TestEmptyLogsBounds(t *testing.T) {
	s, server, c := fixture(t)
	r, err := c.Submit(context.Background(), domain.Command{ID: "cmd_log", Type: "network.check", TargetID: s.Identity().NodeID, Payload: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = c.Wait(context.Background(), r.TaskID)
	for _, tc := range []struct {
		query  string
		status int
	}{{"", 200}, {"?offset=1", 416}, {"?limit=0", 400}, {"?offset=-1", 400}, {"?limit=1048577", 400}} {
		resp, err := server.Client().Get(server.URL + "/api/v1/tasks/" + r.TaskID + "/logs" + tc.query)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != tc.status {
			t.Fatal(tc, resp.StatusCode)
		}
	}
}

func TestBrowserTaskControlsUseReviewedVersion(t *testing.T) {
	s, server, c := fixture(t)
	ctx := context.Background()
	r, err := c.Submit(ctx, domain.Command{ID: "cmd_control_source", Type: "network.check", TargetID: s.Identity().NodeID, Payload: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	task, err := c.Wait(ctx, r.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	response, err := server.Client().Get(server.URL + "/ui/tasks/" + task.ID)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	for _, action := range presentation.TaskActions(task) {
		if !bytes.Contains(body, []byte(action.Label)) || (action.Reason != "" && !bytes.Contains(body, []byte(action.Reason))) {
			t.Fatalf("task action missing from HTML: %s", body)
		}
	}
	if !bytes.Contains(body, []byte(task.ID)) || !bytes.Contains(body, []byte("task-status")) {
		t.Fatalf("task rendering failed: %s", body)
	}
	values := url.Values{"id": {"cmd_control_retry"}, "type": {"task.retry"}, "target_id": {task.TargetID}, "task_id": {task.ID}, "expected_version": {"1"}}
	post := func() int {
		response, err := server.Client().PostForm(server.URL+"/ui/commands", values)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		return response.StatusCode
	}
	if got := post(); got != 409 {
		t.Fatalf("stale review accepted: %d", got)
	}
	values.Set("expected_version", fmt.Sprint(task.Version))
	if got := post(); got != 202 {
		t.Fatal(got)
	}
	if got := post(); got != 202 {
		t.Fatal("retry was not idempotent", got)
	}
	receipt, err := s.Receipt("cmd_control_retry")
	if err != nil {
		t.Fatal(err)
	}
	child, err := c.Wait(ctx, receipt.TaskID)
	if err != nil || child.ParentID == nil || *child.ParentID != task.ID || len(s.Tasks()) != 2 {
		t.Fatal(child, err)
	}
}
