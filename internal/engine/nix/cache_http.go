package nix

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/nix-community/go-nix/pkg/narinfo"
	"github.com/nix-community/go-nix/pkg/narinfo/signature"
	"nixmox/internal/domain"
	"nixmox/internal/engine"
)

// CacheHTTP implements only the bounded peer protocol. Membership and endpoint
// selection belong to the service; callers supply an authenticated peer's base.
type CacheHTTP struct {
	Client *http.Client
	slots  chan struct{}
}

func NewCacheHTTP(limit int) *CacheHTTP {
	transport := &http.Transport{Proxy: nil, DisableCompression: true, MaxConnsPerHost: limit, MaxIdleConns: 64, MaxIdleConnsPerHost: limit, IdleConnTimeout: 30 * time.Second, ResponseHeaderTimeout: 5 * time.Second, DialContext: (&net.Dialer{Timeout: 3 * time.Second, KeepAlive: 30 * time.Second}).DialContext}
	return &CacheHTTP{slots: make(chan struct{}, limit), Client: &http.Client{Transport: transport, Timeout: 30 * time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}
func (c *CacheHTTP) Close()      { c.Client.CloseIdleConnections() }
func (c *CacheHTTP) Active() int { return len(c.slots) }
func cacheHTTPError() error {
	return &domain.Error{Code: "CACHE_UNAVAILABLE", Message: "Peer cache unavailable", Retryable: true}
}
func (c *CacheHTTP) request(ctx context.Context, method, url string) (*http.Response, func(), error) {
	select {
	case c.slots <- struct{}{}:
	default:
		return nil, nil, cacheHTTPError()
	}
	release := sync.OnceFunc(func() { <-c.slots })
	request, err := http.NewRequestWithContext(ctx, method, url, nil)
	if err != nil {
		release()
		return nil, nil, cacheHTTPError()
	}
	response, err := c.Client.Do(request)
	if err != nil {
		release()
		return nil, nil, cacheHTTPError()
	}
	close := sync.OnceFunc(func() { response.Body.Close(); release() })
	if response.StatusCode == http.StatusNotFound {
		close()
		return nil, nil, &domain.Error{Code: "NOT_FOUND", Message: "Store output not found"}
	}
	if response.StatusCode != http.StatusOK || response.Header.Get("Content-Encoding") != "" {
		close()
		return nil, nil, cacheHTTPError()
	}
	return response, close, nil
}
func (c *CacheHTTP) Info(ctx context.Context, base, hash, key string) (*narinfo.NarInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	response, close, err := c.request(ctx, "GET", base+hash+".narinfo")
	if err != nil {
		return nil, err
	}
	defer close()
	data, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil || len(data) > 1<<20 {
		return nil, cacheHTTPError()
	}
	info, err := narinfo.Parse(bytes.NewReader(data))
	if err != nil {
		return nil, cacheHTTPError()
	}
	if CheckCacheInfo(info, hash) != nil {
		return nil, cacheHTTPError()
	}
	public, err := signature.ParsePublicKey(key)
	if err != nil || !signature.VerifyFirst(info.Fingerprint(), info.Signatures, []signature.PublicKey{public}) {
		return nil, cacheHTTPError()
	}
	return info, nil
}

// Open checks response headers and the first byte before committing a body.
// HEAD never reads archive bytes. Closing releases both the connection and slot.
func (c *CacheHTTP) Open(ctx context.Context, base string, info *narinfo.NarInfo, head bool) (engine.StoreExport, error) {
	pullCtx, cancel := context.WithCancel(ctx)
	firstByte := time.AfterFunc(5*time.Second, cancel)
	method := "GET"
	if head {
		method = "HEAD"
	}
	response, release, err := c.request(pullCtx, method, base+info.URL)
	if err != nil {
		firstByte.Stop()
		cancel()
		return nil, err
	}
	close := sync.OnceFunc(func() { firstByte.Stop(); cancel(); release() })
	if response.ContentLength != int64(info.FileSize) {
		close()
		return nil, cacheHTTPError()
	}
	if head {
		close()
		return nil, nil
	}
	first := make([]byte, 1)
	if _, err = io.ReadFull(response.Body, first); err != nil {
		close()
		return nil, cacheHTTPError()
	}
	if !firstByte.Stop() {
		close()
		return nil, cacheHTTPError()
	}
	return &httpCacheStream{Reader: io.MultiReader(bytes.NewReader(first), idleCacheReader{response.Body, cancel}), close: close}, nil
}

type idleCacheReader struct {
	io.Reader
	cancel context.CancelFunc
}

func (r idleCacheReader) Read(p []byte) (int, error) {
	timer := time.AfterFunc(30*time.Second, r.cancel)
	defer timer.Stop()
	return r.Reader.Read(p)
}

type httpCacheStream struct {
	io.Reader
	close func()
}

func (s *httpCacheStream) Close() error                       { s.close(); return nil }
func (s *httpCacheStream) WriteTo(w io.Writer) (int64, error) { return io.Copy(w, s.Reader) }
