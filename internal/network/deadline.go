package network

import (
	"context"
	"net"
	"time"
)

// Each socket operation gets an idle bound; request contexts bound the whole
// exchange. This includes stalled bodies after HTTP headers have arrived.
func BoundedDial(dial func(context.Context, string, string) (net.Conn, error)) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		conn, err := dial(ctx, network, address)
		if err != nil {
			return nil, err
		}
		return &idleConn{Conn: conn}, nil
	}
}

type idleConn struct{ net.Conn }

func (c *idleConn) Read(p []byte) (int, error) {
	_ = c.SetReadDeadline(time.Now().Add(15 * time.Second))
	return c.Conn.Read(p)
}
func (c *idleConn) Write(p []byte) (int, error) {
	_ = c.SetWriteDeadline(time.Now().Add(15 * time.Second))
	return c.Conn.Write(p)
}
