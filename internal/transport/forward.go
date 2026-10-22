// Package transport owns listeners and bounded byte transport, never domain policy.
package transport

import (
	"context"
	"io"
	"net"
	"sync"
	"time"
)

// Forward exposes an embedded broker on one explicitly selected interface.
// TLS remains end-to-end with NATS; no certificate is terminated here.
type Forward struct {
	listener net.Listener
	cancel   context.CancelFunc
	done     chan struct{}
}

func ListenForward(address, target string) (*Forward, error) {
	l, err := net.Listen("tcp", address)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	f := &Forward{listener: l, cancel: cancel, done: make(chan struct{})}
	go f.serve(ctx, target)
	return f, nil
}
func (f *Forward) Close() { f.cancel(); f.listener.Close(); <-f.done }
func (f *Forward) serve(ctx context.Context, target string) {
	defer close(f.done)
	var wg sync.WaitGroup
	defer wg.Wait()
	capacity := make(chan struct{}, 64)
	for {
		peer, err := f.listener.Accept()
		if err != nil {
			return
		}
		select {
		case capacity <- struct{}{}:
		default:
			peer.Close()
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-capacity }()
			defer peer.Close()
			upstream, err := (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "tcp", target)
			if err != nil {
				return
			}
			defer upstream.Close()
			stop := context.AfterFunc(ctx, func() { peer.Close(); upstream.Close() })
			defer stop()
			done := make(chan struct{})
			go func() { io.Copy(idleConn{upstream}, idleConn{peer}); upstream.Close(); peer.Close(); close(done) }()
			io.Copy(idleConn{peer}, idleConn{upstream})
			upstream.Close()
			peer.Close()
			<-done
		}()
	}
}

type idleConn struct{ net.Conn }

func (c idleConn) Read(p []byte) (int, error) {
	c.SetReadDeadline(time.Now().Add(45 * time.Second))
	return c.Conn.Read(p)
}
func (c idleConn) Write(p []byte) (int, error) {
	c.SetWriteDeadline(time.Now().Add(5 * time.Second))
	return c.Conn.Write(p)
}
