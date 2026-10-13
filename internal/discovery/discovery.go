// Package discovery advertises untrusted LAN hints, never membership or
// authorization. Joining always requires a separate explicit user action.
package discovery

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"sort"
	"sync"
	"syscall"
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
	iface, ip, err := advertisedInterface(build().Address)
	if err != nil {
		return err
	}
	group := &net.UDPAddr{IP: net.IPv4(239, 255, 80, 99), Port: Port}
	c, err := net.ListenMulticastUDP("udp4", iface, group)
	if err != nil {
		return err
	}
	defer c.Close()
	out, err := net.ListenUDP("udp4", &net.UDPAddr{IP: ip})
	if err != nil {
		return err
	}
	defer out.Close()
	raw, err := out.SyscallConn()
	if err != nil {
		return err
	}
	var socketErr error
	if err = raw.Control(func(fd uintptr) {
		socketErr = syscall.SetsockoptInet4Addr(int(fd), syscall.IPPROTO_IP, syscall.IP_MULTICAST_IF, [4]byte(ip.To4()))
	}); err != nil {
		return err
	}
	if socketErr != nil {
		return socketErr
	}
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
			out.WriteToUDP(b, group)
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

// Bind discovery to the advertised LAN, rather than the default (often NAT)
// interface on multi-homed machines.
func advertisedInterface(address string) (*net.Interface, net.IP, error) {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return nil, nil, err
	}
	resolved, err := net.ResolveIPAddr("ip4", host)
	if err != nil {
		return nil, nil, err
	}
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil, nil, err
	}
	for _, iface := range interfaces {
		addresses, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, address := range addresses {
			ip, _, err := net.ParseCIDR(address.String())
			if err == nil && ip.Equal(resolved.IP) && ip.To4() != nil {
				return &iface, ip.To4(), nil
			}
		}
	}
	return nil, nil, fmt.Errorf("discovery address %q is not a local IPv4 interface", host)
}
