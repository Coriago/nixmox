// Package client is the HTTP transport used by CLI and TUI.
package client

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"nixmox/internal/domain"
)

type Client struct {
	Endpoint string
	HTTP     *http.Client
}

func New(endpoint string) (*Client, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "http" || u.Host == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("endpoint must be an HTTP console origin")
	}
	return &Client{Endpoint: strings.TrimRight(endpoint, "/"), HTTP: &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(r *http.Request, via []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (c *Client) request(ctx context.Context, method, path string, body any, out any) error {
	var b []byte
	var err error
	if body != nil {
		b, err = json.Marshal(body)
		if err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, c.Endpoint+path, bytes.NewReader(b))
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return domain.Err("OUTCOME_UNKNOWN", "Console request failed; query the command receipt before retrying.", 503)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8*1024*1024+1))
	if err != nil || len(data) > 8*1024*1024 {
		return domain.Err("UNAVAILABLE", "Console response unavailable or too large.", 503)
	}
	if resp.StatusCode >= 300 {
		var e struct {
			Error domain.Error `json:"error"`
		}
		if json.Unmarshal(data, &e) != nil || e.Error.Code == "" {
			return domain.Err("UNAVAILABLE", "Invalid console response.", 503)
		}
		e.Error.Status = resp.StatusCode
		return &e.Error
	}
	if err := json.Unmarshal(data, out); err != nil {
		return domain.Err("UNAVAILABLE", "Invalid console response.", 503)
	}
	return nil
}
func (c *Client) Read(ctx context.Context, path string, out any) error {
	return c.request(ctx, http.MethodGet, path, nil, out)
}
func (c *Client) Identity(ctx context.Context) (domain.Identity, error) {
	var v domain.Identity
	err := c.Read(ctx, "/api/v1/identity", &v)
	return v, err
}
func (c *Client) Network(ctx context.Context) (domain.NetworkStatus, error) {
	var v domain.NetworkStatus
	err := c.Read(ctx, "/api/v1/network/status", &v)
	return v, err
}
func (c *Client) Cluster(ctx context.Context) (domain.ClusterStatus, error) {
	var v domain.ClusterStatus
	err := c.Read(ctx, "/api/v1/cluster/status", &v)
	return v, err
}
func (c *Client) JoinRequests(ctx context.Context, query url.Values) (domain.Page[domain.JoinRequest], error) {
	var v domain.Page[domain.JoinRequest]
	err := c.Read(ctx, "/api/v1/cluster/requests?"+query.Encode(), &v)
	return v, err
}
func (c *Client) JoinRequest(ctx context.Context, id string) (domain.JoinRequest, error) {
	var v domain.JoinRequest
	if !domain.SafeID.MatchString(id) {
		return v, domain.Err("INVALID_INPUT", "Invalid request ID.", 422)
	}
	err := c.Read(ctx, "/api/v1/cluster/requests/"+id, &v)
	return v, err
}
func (c *Client) Task(ctx context.Context, id string) (domain.Task, error) {
	var v domain.Task
	if !domain.SafeID.MatchString(id) {
		return v, domain.Err("INVALID_INPUT", "Invalid task ID.", 422)
	}
	err := c.Read(ctx, "/api/v1/tasks/"+id, &v)
	return v, err
}
func (c *Client) Tasks(ctx context.Context, query string) (domain.Page[domain.Task], error) {
	var v domain.Page[domain.Task]
	err := c.Read(ctx, "/api/v1/tasks"+query, &v)
	return v, err
}
func (c *Client) Submit(ctx context.Context, cmd domain.Command) (domain.Receipt, error) {
	var r domain.Receipt
	if !domain.SafeID.MatchString(cmd.ID) {
		return r, domain.Err("INVALID_INPUT", "Invalid command ID.", 422)
	}
	// Save the exact envelope before transport. A failed/unknown submission remains
	// recoverable under XDG_STATE_HOME/nixmox/pending, including after client exit.
	pending, err := c.savePending(cmd)
	if err != nil {
		return r, fmt.Errorf("persist retry identity: %w", err)
	}
	err = c.request(ctx, http.MethodPost, "/api/v1/commands", cmd, &r)
	if err == nil {
		_ = os.Remove(pending)
	}
	return r, err
}
func (c *Client) savePending(cmd domain.Command) (string, error) {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".local/state")
	}
	hash := sha256.Sum256([]byte(c.Endpoint))
	dir := filepath.Join(base, "nixmox/pending", hex.EncodeToString(hash[:16]))
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	b, err := json.Marshal(cmd)
	if err != nil {
		return "", err
	}
	dest := filepath.Join(dir, cmd.ID+".json")
	// Exclusive creation prevents two clients from changing a pending retry identity.
	f, err := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if os.IsExist(err) {
		old, readErr := os.ReadFile(dest)
		if readErr != nil {
			return "", readErr
		}
		if !bytes.Equal(old, b) {
			return "", domain.Err("IDEMPOTENCY_CONFLICT", "Pending command ID has another request.", 409)
		}
		return dest, nil
	}
	if err != nil {
		return "", err
	}
	if _, err = f.Write(b); err != nil {
		f.Close()
		return "", err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return "", err
	}
	if err = f.Close(); err != nil {
		return "", err
	}
	d, err := os.Open(dir)
	if err != nil {
		return "", err
	}
	defer d.Close()
	return dest, d.Sync()
}
func (c *Client) Wait(ctx context.Context, id string) (domain.Task, error) {
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		t, err := c.Task(ctx, id)
		if err != nil || t.Terminal() || t.State == "blocked" {
			return t, err
		}
		select {
		case <-ctx.Done():
			return t, ctx.Err()
		case <-ticker.C:
		}
	}
}

// Events coalesces snapshots for slow renderers and reconnects with a fresh snapshot.
func (c *Client) Events(ctx context.Context) <-chan error {
	out := make(chan error, 1)
	put := func(err error) {
		select {
		case out <- err:
		default:
		}
	}
	go func() {
		defer close(out)
		transport := *c.HTTP
		transport.Timeout = 0
		for ctx.Err() == nil {
			// Periodically renew even a silent/stalled peer; reconnect always begins
			// with a fresh bounded snapshot.
			streamContext, stopStream := context.WithTimeout(ctx, 45*time.Second)
			req, err := http.NewRequestWithContext(streamContext, "GET", c.Endpoint+"/api/v1/events?topics=network.status,tasks", nil)
			if err != nil {
				stopStream()
				put(err)
				return
			}
			resp, err := transport.Do(req)
			if err == nil {
				if resp.StatusCode == 200 {
					scan := bufio.NewScanner(resp.Body)
					scan.Buffer(make([]byte, 4096), 1024*1024)
					for scan.Scan() {
						if strings.HasPrefix(scan.Text(), "data: ") {
							put(nil)
						}
					}
				}
				resp.Body.Close()
			}
			stopStream()
			if ctx.Err() != nil {
				return
			}
			put(errors.New("disconnected; displayed observations may be stale"))
			timer := time.NewTimer(time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
	}()
	return out
}
