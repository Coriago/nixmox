// Package discovery advertises untrusted LAN hints, never membership or
// authorization. Joining always requires a separate explicit user action.
package discovery

import (
	"context"
	"encoding/json"
	"net"
	"sort"
	"sync"
	"time"
)

const Port = 8099

type Peer struct {
	NodeID  string    `json:"node_id"`
	Address string    `json:"address"`
	Cluster string    `json:"cluster"`
	Seen    time.Time `json:"seen"`
}
type Registry struct {
	mu    sync.Mutex
	peers map[string]Peer
}

func New() *Registry { return &Registry{peers: map[string]Peer{}} }
func (r *Registry) List() []Peer {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []Peer{}
	for k, p := range r.peers {
		if time.Since(p.Seen) > 35*time.Second {
			delete(r.peers, k)
		} else {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Address < out[j].Address })
	return out
}
func (r *Registry) Run(ctx context.Context, build func() Peer) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	c, err := net.ListenMulticastUDP("udp4", nil, &net.UDPAddr{IP: net.IPv4(239, 255, 80, 99), Port: Port})
	if err != nil {
		return err
	}
	defer c.Close()
	go func() { <-ctx.Done(); c.Close() }()
	go func() {
		tick := time.NewTicker(10 * time.Second)
		defer tick.Stop()
		send := func() {
			p := build()
			if p.Address == "" {
				return
			}
			b, _ := json.Marshal(p)
			out, e := net.DialUDP("udp4", nil, &net.UDPAddr{IP: net.IPv4(239, 255, 80, 99), Port: Port})
			if e == nil {
				out.Write(b)
				out.Close()
			}
		}
		send()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				send()
			}
		}
	}()
	buf := make([]byte, 2048)
	for {
		n, source, e := c.ReadFromUDP(buf)
		if e != nil {
			if ctx.Err() != nil {
				return nil
			}
			return e
		}
		var p Peer
		if json.Unmarshal(buf[:n], &p) != nil || p.NodeID == "" || len(p.NodeID) > 253 || len(p.Cluster) > 253 {
			continue
		}
		_, port, e := net.SplitHostPort(p.Address)
		if e != nil {
			continue
		}
		p.Address = net.JoinHostPort(source.IP.String(), port)
		if p.NodeID == build().NodeID {
			continue
		}
		p.Seen = time.Now()
		r.mu.Lock()
		for key, peer := range r.peers {
			if time.Since(peer.Seen) > 35*time.Second {
				delete(r.peers, key)
			}
		}
		if _, exists := r.peers[p.Address]; exists || len(r.peers) < 256 {
			r.peers[p.Address] = p
		}
		r.mu.Unlock()
	}
}
