// Package client shares HTTP requests and replayable SSE across interfaces.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

const ResponseLimit = 8 << 20

type Client struct {
	endpoint *url.URL
	http     *http.Client
}

type Error struct {
	Status  int
	Message string
}

func (e *Error) Error() string {
	return fmt.Sprintf("daemon returned HTTP %d: %s", e.Status, e.Message)
}

// UnknownSubmission means a mutation may have reached the daemon. Interfaces
// must inspect state rather than automatically repeating a non-idempotent call.
type UnknownSubmission struct{ Cause error }

func (e *UnknownSubmission) Error() string {
	return "request outcome is unknown; inspect daemon state before retrying"
}

func (e *UnknownSubmission) Unwrap() error { return e.Cause }

func New(endpoint string, httpClient *http.Client) (*Client, error) {
	base, err := url.Parse(endpoint)
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return nil, errors.New("endpoint must be an HTTP(S) URL without credentials, query, or fragment")
	}
	var transport http.RoundTripper
	var copyClient http.Client
	if httpClient != nil {
		copyClient = *httpClient
		transport = httpClient.Transport
	} else {
		configured := http.DefaultTransport.(*http.Transport).Clone()
		configured.ResponseHeaderTimeout = 10 * time.Second
		transport = configured
	}
	copyClient.Transport = otelhttp.NewTransport(transport)
	// Never redirect credential submissions or mutations to another endpoint.
	copyClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{endpoint: base, http: &copyClient}, nil
}

func (c *Client) Close() { c.http.CloseIdleConnections() }

func (c *Client) request(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	relative, err := url.Parse(path)
	if err != nil || !strings.HasPrefix(path, "/") || relative.IsAbs() || relative.Host != "" || relative.Fragment != "" {
		return nil, errors.New("API path must be relative to the daemon endpoint")
	}
	endpoint := *c.endpoint
	endpoint.Path = strings.TrimRight(endpoint.Path, "/") + relative.Path
	endpoint.RawPath = strings.TrimRight(c.endpoint.EscapedPath(), "/") + relative.EscapedPath()
	endpoint.RawQuery = relative.RawQuery
	request, err := http.NewRequestWithContext(ctx, method, endpoint.String(), body)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	return request, nil
}

func (c *Client) Do(ctx context.Context, method, path string, input, output any) error {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	var data []byte
	var err error
	if input != nil {
		data, err = json.Marshal(input)
		if err != nil {
			return err
		}
		defer clear(data)
	}
	var body io.Reader
	if input != nil {
		body = bytes.NewReader(data)
	}
	request, err := c.request(ctx, method, path, body)
	if err != nil {
		return err
	}
	response, err := c.http.Do(request)
	if err != nil {
		if method != http.MethodGet && method != http.MethodHead {
			return &UnknownSubmission{Cause: err}
		}
		return errors.New("daemon request failed")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return responseError(response)
	}
	payload, err := io.ReadAll(io.LimitReader(response.Body, ResponseLimit+1))
	if err != nil {
		return incomplete(method, err)
	}
	defer clear(payload)
	if len(payload) > ResponseLimit {
		return incomplete(method, errors.New("daemon response exceeds size limit"))
	}
	if output == nil || len(bytes.TrimSpace(payload)) == 0 {
		return nil
	}
	if err = json.Unmarshal(payload, output); err != nil {
		return incomplete(method, errors.New("invalid daemon JSON response"))
	}
	return nil
}

func incomplete(method string, err error) error {
	if method != http.MethodGet && method != http.MethodHead {
		return &UnknownSubmission{Cause: err}
	}
	return err
}

func responseError(response *http.Response) error {
	var payload struct {
		Error string `json:"error"`
	}
	// Error bodies are parsed only as the documented envelope, not echoed whole.
	_ = json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&payload)
	if payload.Error == "" {
		payload.Error = http.StatusText(response.StatusCode)
	}
	if len(payload.Error) > 1024 {
		payload.Error = payload.Error[:1024]
	}
	return &Error{Status: response.StatusCode, Message: payload.Error}
}

type Page[T any] struct {
	Items      []T    `json:"items"`
	NextCursor string `json:"nextCursor"`
}
