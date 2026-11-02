package cache

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"
)

type Peer struct {
	NodeID   string `json:"nodeId"`
	StoreURL string `json:"storeUrl"`
}

// Gateway retains only peer endpoints and HTTP connections. The membership
// watcher calls ReplacePeers on changes and with a fresh snapshot on reconnect.
type Gateway struct {
	peers     atomic.Pointer[[]Peer]
	client    *http.Client
	transport *http.Transport
}

func NewGateway() *Gateway {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil // LAN peers must not be sent to an ambient outbound proxy.
	transport.ResponseHeaderTimeout = 5 * time.Second
	transport.MaxIdleConnsPerHost = 4
	transport.IdleConnTimeout = time.Minute
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		conn, err := (&net.Dialer{Timeout: 3 * time.Second, KeepAlive: 30 * time.Second}).DialContext(ctx, network, address)
		if err != nil {
			return nil, err
		}
		return &idleConn{Conn: conn, timeout: 30 * time.Second}, nil
	}
	return &Gateway{transport: transport, client: &http.Client{Transport: transport, Timeout: 30 * time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}
func (g *Gateway) Close() { g.transport.CloseIdleConnections() }

func (g *Gateway) ReplacePeers(peers []Peer) error {
	snapshot := make([]Peer, 0, len(peers))
	for _, peer := range peers {
		endpoint, err := url.Parse(peer.StoreURL)
		if err != nil || (endpoint.Scheme != "http" && endpoint.Scheme != "https") || endpoint.Host == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" {
			return errors.New("invalid store-cache endpoint")
		}
		peer.StoreURL = strings.TrimRight(peer.StoreURL, "/")
		snapshot = append(snapshot, peer)
	}
	g.peers.Store(&snapshot)
	g.transport.CloseIdleConnections()
	return nil
}

// ServeHTTP expects /cache to have been stripped. Failover happens before
// committing response headers. After streaming starts, a failed peer aborts the
// response instead of mixing bytes from different artifacts.
func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !strings.HasPrefix(r.URL.Path, "/") || strings.Contains(r.URL.Path, "..") || r.URL.RawQuery != "" {
		http.NotFound(w, r)
		return
	}
	peers := g.peers.Load()
	if peers == nil || len(*peers) == 0 {
		http.Error(w, "no cache peers available", http.StatusServiceUnavailable)
		return
	}
	unavailable := false
	for _, peer := range *peers {
		if r.Context().Err() != nil {
			return
		}
		request, err := http.NewRequestWithContext(r.Context(), r.Method, peer.StoreURL+r.URL.EscapedPath(), nil)
		if err != nil {
			unavailable = true
			continue
		}
		response, err := g.client.Do(request)
		if err != nil {
			unavailable = true
			continue
		}
		if response.StatusCode != http.StatusOK {
			if response.StatusCode != http.StatusNotFound {
				unavailable = true
			}
			response.Body.Close()
			continue
		}
		for _, header := range []string{"Content-Type", "Content-Length", "Cache-Control", "ETag", "Last-Modified"} {
			if value := response.Header.Get(header); value != "" {
				w.Header().Set(header, value)
			}
		}
		w.WriteHeader(http.StatusOK)
		_, err = io.Copy(w, response.Body)
		response.Body.Close()
		if err != nil {
			panic(http.ErrAbortHandler)
		}
		return
	}
	if unavailable {
		http.Error(w, "cache peers unavailable", http.StatusBadGateway)
	} else {
		http.NotFound(w, r)
	}
}

type idleConn struct {
	net.Conn
	timeout time.Duration
}

func (c *idleConn) Read(data []byte) (int, error) {
	if err := c.Conn.SetReadDeadline(time.Now().Add(c.timeout)); err != nil {
		return 0, err
	}
	return c.Conn.Read(data)
}
