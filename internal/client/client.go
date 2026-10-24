// Package client is the shared HTTP-only client for CLI and future interfaces.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"nixmox/internal/domain"
)

type Client struct {
	endpoint string
	http     *http.Client
}

type APIError struct {
	StatusCode int
	Response   domain.ErrorResponse
}

func (e *APIError) Error() string { return e.Response.Error.Error() }
func (e *APIError) Unwrap() error { return e.Response.Error }

// UnknownSubmission carries the original identity, never an instruction to
// generate a new command. A lost HTTP response cannot establish rejection.
type UnknownSubmission struct{ CommandID domain.ID }

func (e *UnknownSubmission) Error() string {
	return fmt.Sprintf("submission outcome unknown; query command %s before retrying the same ID", e.CommandID)
}

func New(endpoint string, transport *http.Client) (*Client, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, &domain.Error{Code: "INVALID_INPUT", Message: "Endpoint must be an HTTP(S) origin without credentials"}
	}
	if transport == nil {
		transport = &http.Client{Timeout: 10 * time.Second}
	}
	// Never forward a mutation to a redirect target or turn it into a GET.
	owned := *transport
	owned.Transport = otelhttp.NewTransport(owned.Transport)
	owned.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{endpoint: strings.TrimRight(endpoint, "/"), http: &owned}, nil
}

// Retry only reads after transient disconnects/gateway unavailability. Mutations
// retain their original ID and use receipt reconciliation, never blind replay.
func (c *Client) request(ctx context.Context, method, path string, input, output any) error {
	for attempt := 0; ; attempt++ {
		err := c.requestOnce(ctx, method, path, input, output)
		if err == nil || method != http.MethodGet || attempt == 2 || ctx.Err() != nil {
			return err
		}
		var apiErr *APIError
		var networkErr net.Error
		retry := (errors.As(err, &apiErr) && apiErr.Response.Error.Retryable) || errors.As(err, &networkErr)
		if !retry {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(attempt+1) * 100 * time.Millisecond):
		}
	}
}
func (c *Client) requestOnce(ctx context.Context, method, path string, input, output any) error {
	var body io.Reader
	if input != nil {
		data, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	request, err := http.NewRequestWithContext(ctx, method, c.endpoint+"/api/v1/"+path, body)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	decoder := json.NewDecoder(io.LimitReader(response.Body, domain.QueryTotalLimit+1))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		failure := &APIError{StatusCode: response.StatusCode}
		if err = decoder.Decode(&failure.Response); err != nil || failure.Response.Error == nil {
			return fmt.Errorf("HTTP %d: invalid error response", response.StatusCode)
		}
		return failure
	}
	if err = decoder.Decode(output); err != nil {
		return fmt.Errorf("invalid API response: %w", err)
	}
	if decoder.Decode(new(any)) != io.EOF {
		return errors.New("API returned multiple JSON values or exceeded the response limit")
	}
	return nil
}

func get[T any](ctx context.Context, c *Client, path string) (T, error) {
	var value T
	err := c.request(ctx, http.MethodGet, path, nil, &value)
	return value, err
}

func (c *Client) Submit(ctx context.Context, command domain.Command) (domain.Receipt, error) {
	var receipt domain.Receipt
	err := c.request(ctx, http.MethodPost, "commands", command, &receipt)
	if err == nil {
		if receipt.CommandID != command.ID || receipt.Admission != "accepted" || receipt.TaskID == nil {
			return receipt, &UnknownSubmission{CommandID: command.ID}
		}
		return receipt, nil
	}
	var failure *APIError
	if errors.As(err, &failure) && failure.Response.Error.Code != "SUBMISSION_UNKNOWN" && failure.StatusCode < 500 {
		return receipt, err
	}
	// The same command ID is authoritative, including after a caller timeout.
	lookup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	if receipt, lookupErr := c.Receipt(lookup, command.ID); lookupErr == nil && receipt.Admission == "accepted" && receipt.TaskID != nil {
		return receipt, nil
	}
	if failure != nil && failure.Response.Error.Code == "UNSUPPORTED_CAPABILITY" {
		return receipt, failure
	}
	return receipt, &UnknownSubmission{CommandID: command.ID}
}

func (c *Client) Receipt(ctx context.Context, id domain.ID) (domain.Receipt, error) {
	var result domain.Receipt
	err := c.request(ctx, http.MethodGet, "commands/"+url.PathEscape(string(id)), nil, &result)
	if err == nil && result.CommandID != id {
		err = errors.New("receipt command identity mismatch")
	}
	return result, err
}

func (c *Client) Topology(ctx context.Context) (domain.SignedTopology, error) {
	return get[domain.SignedTopology](ctx, c, "membership-topology")
}
func (c *Client) Removal(ctx context.Context, node domain.ID, epoch uint64) (domain.SignedRemoval, error) {
	return get[domain.SignedRemoval](ctx, c, "membership-status?node_id="+url.QueryEscape(string(node))+"&membership_epoch="+strconv.FormatUint(epoch, 10))
}
func (c *Client) Enroll(ctx context.Context, request domain.EnrollmentSubmission) (domain.EnrollmentStatus, error) {
	var result domain.EnrollmentStatus
	err := c.request(ctx, http.MethodPost, "enrollment/requests", request, &result)
	return result, err
}
func (c *Client) Enrollment(ctx context.Context, id domain.ID) (domain.EnrollmentStatus, error) {
	return get[domain.EnrollmentStatus](ctx, c, "enrollment/requests/"+url.PathEscape(string(id)))
}
func (c *Client) Certificates(ctx context.Context, id domain.ID) (domain.Issuance, error) {
	var result domain.Issuance
	err := c.request(ctx, http.MethodPost, "enrollment/requests/"+url.PathEscape(string(id))+"/certificates", domain.Empty{}, &result)
	return result, err
}
func (c *Client) Requests(ctx context.Context, page ...domain.PageRequest) (domain.Page[domain.JoinRequest], error) {
	return listPage[domain.JoinRequest](ctx, c, "cluster/requests", page...)
}

func (c *Client) Discoveries(ctx context.Context, page ...domain.PageRequest) (domain.Page[domain.Discovery], error) {
	return listPage[domain.Discovery](ctx, c, "discoveries", page...)
}

func (c *Client) Identity(ctx context.Context) (domain.Identity, error) {
	return get[domain.Identity](ctx, c, "identity")
}

func (c *Client) Host(ctx context.Context, id domain.ID) (domain.Host, error) {
	return get[domain.Host](ctx, c, "hosts/"+url.PathEscape(string(id)))
}

func (c *Client) Hosts(ctx context.Context, page ...domain.PageRequest) (domain.Page[domain.Host], error) {
	return listPage[domain.Host](ctx, c, "hosts", page...)
}

func (c *Client) Telemetry(ctx context.Context, id domain.ID) (domain.HostTelemetry, error) {
	return get[domain.HostTelemetry](ctx, c, "hosts/"+url.PathEscape(string(id))+"/telemetry")
}

func (c *Client) Network(ctx context.Context) (domain.NetworkStatus, error) {
	return get[domain.NetworkStatus](ctx, c, "network/status")
}

func (c *Client) Cluster(ctx context.Context) (domain.ClusterStatus, error) {
	return get[domain.ClusterStatus](ctx, c, "cluster/status")
}

func (c *Client) Task(ctx context.Context, id domain.ID) (domain.Task, error) {
	return get[domain.Task](ctx, c, "tasks/"+url.PathEscape(string(id)))
}

func (c *Client) Tasks(ctx context.Context, page domain.PageRequest) (domain.Page[domain.Task], error) {
	return listPage[domain.Task](ctx, c, "tasks", page)
}

// A single list encoder keeps filters/cursors intact for every frontend.
func listPage[T any](ctx context.Context, c *Client, path string, pages ...domain.PageRequest) (domain.Page[T], error) {
	var result domain.Page[T]
	if len(pages) > 1 {
		return result, &domain.Error{Code: "INVALID_INPUT", Message: "Expected one page request"}
	}
	query := url.Values{}
	if len(pages) == 1 {
		page := pages[0]
		for key, value := range page.Filters {
			if key == "cursor" || key == "limit" {
				return result, &domain.Error{Code: "INVALID_INPUT", Message: "Reserved filter name"}
			}
			query.Set(key, value)
		}
		if page.Cursor != "" {
			query.Set("cursor", page.Cursor)
		}
		if page.Limit != 0 {
			query.Set("limit", strconv.Itoa(page.Limit))
		}
	}
	if len(query) > 0 {
		path += "?" + query.Encode()
	}
	err := c.request(ctx, http.MethodGet, path, nil, &result)
	return result, err
}
func (c *Client) Logs(ctx context.Context, id domain.ID, offset int64, limit int) (domain.LogChunk, error) {
	var result domain.LogChunk
	query := url.Values{"offset": {strconv.FormatInt(offset, 10)}, "limit": {strconv.Itoa(limit)}}
	err := c.request(ctx, http.MethodGet, "tasks/"+url.PathEscape(string(id))+"/logs?"+query.Encode(), nil, &result)
	return result, err
}

// Wait observes executor state; it does not cancel work when the caller leaves.
func (c *Client) Wait(ctx context.Context, id domain.ID) (domain.Task, error) {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		current, err := c.Task(ctx, id)
		if err != nil {
			return current, err
		}
		switch current.State {
		case domain.Succeeded, domain.Failed, domain.Cancelled, domain.Blocked, domain.Reconciling:
			return current, nil
		}
		select {
		case <-ctx.Done():
			return current, ctx.Err()
		case <-ticker.C:
		}
	}
}
