// Package discovery advertises untrusted LAN hints, never membership or
// authorization. Joining always requires a separate explicit user action.
package discovery

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"sort"
	"sync"
	"syscall"
	"time"
)

const Port = 8099

type Peer struct {
	Installer bool      `json:"installer"`
	NodeID    string    `json:"node_id"`
	Address   string    `json:"address"`
	Cluster   string    `json:"cluster"`
	Seen      time.Time `json:"seen"`
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
	return r.run(ctx, build, localInterfaces, r.runInterface, 5*time.Second)
}

type endpoint struct {
	iface net.Interface
	ip    net.IP
}

func (e endpoint) key() string { return fmt.Sprintf("%d/%s", e.iface.Index, e.ip) }

// Reconcile sockets continuously: a failed VPN or a late Wi-Fi connection must
// not disable discovery on the other interfaces, or require a daemon restart.
func (r *Registry) run(ctx context.Context, build func() Peer, scan func() ([]endpoint, error), serve func(context.Context, func() Peer, endpoint) error, interval time.Duration) error {
	type worker struct {
		cancel context.CancelFunc
		done   chan error
	}
	active := map[string]worker{}
	defer func() {
		for _, w := range active {
			w.cancel()
		}
		for _, w := range active {
			<-w.done
		}
	}()
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		if ctx.Err() != nil {
			return nil
		}
		endpoints, err := scan()
		if err != nil {
			log.Printf("discovery interfaces: %v", err)
		} else {
			wanted := map[string]endpoint{}
			for _, e := range endpoints {
				wanted[e.key()] = e
			}
			for key, w := range active {
				if _, ok := wanted[key]; !ok {
					w.cancel()
					<-w.done
					delete(active, key)
					continue
				}
				select {
				case err := <-w.done:
					w.cancel()
					delete(active, key)
					if err != nil {
						log.Printf("discovery %s: %v", key, err)
					}
				default:
				}
			}
			for key, e := range wanted {
				if _, ok := active[key]; ok {
					continue
				}
				child, cancel := context.WithCancel(ctx)
				done := make(chan error, 1)
				active[key] = worker{cancel: cancel, done: done}
				go func() { done <- serve(child, build, e) }()
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
		}
	}
}

func (r *Registry) runInterface(ctx context.Context, build func() Peer, endpoint endpoint) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	iface, ip := &endpoint.iface, endpoint.ip
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
			_, port, err := net.SplitHostPort(p.Address)
			if err != nil || port == "" {
				return
			}
			p.Address = net.JoinHostPort(ip.String(), port)
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

func localInterfaces() ([]endpoint, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	var out []endpoint
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 || iface.Flags&net.FlagMulticast == 0 {
			continue
		}
		addresses, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, address := range addresses {
			ip, _, err := net.ParseCIDR(address.String())
			if err == nil && ip.To4() != nil && ip.IsGlobalUnicast() {
				out = append(out, endpoint{iface: iface, ip: ip.To4()})
			}
		}
	}
	return out, nil
}
