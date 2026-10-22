// Package network observes OS interfaces without changing connectivity or probing the internet.
package network

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strconv"
	"time"

	"nixmox/internal/config"
	"nixmox/internal/domain"
)

type Interface struct {
	Name      string
	Up        bool
	Loopback  bool
	Addresses []net.IP
}
type Inspector struct {
	Config     config.Config
	Interfaces func() ([]Interface, error)
}

func SystemInterfaces() ([]Interface, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	result := []Interface{}
	for _, i := range interfaces {
		addrs, err := i.Addrs()
		if err != nil {
			return nil, err
		}
		v := Interface{Name: i.Name, Up: i.Flags&net.FlagUp != 0 && i.Flags&net.FlagRunning != 0, Loopback: i.Flags&net.FlagLoopback != 0}
		for _, addr := range addrs {
			ip, _, err := net.ParseCIDR(addr.String())
			if err == nil {
				v.Addresses = append(v.Addresses, ip)
			}
		}
		result = append(result, v)
	}
	return result, nil
}
func (i Inspector) Inspect(ctx context.Context, node string) (domain.NetworkStatus, error) {
	if err := ctx.Err(); err != nil {
		return domain.NetworkStatus{}, err
	}
	read := i.Interfaces
	if read == nil {
		read = SystemInterfaces
	}
	interfaces, err := read()
	if err != nil {
		return domain.NetworkStatus{}, err
	}
	if err := ctx.Err(); err != nil {
		return domain.NetworkStatus{}, err
	}
	s := domain.UnknownNetwork(node)
	now := time.Now().UTC()
	s.ObservedAt = &now
	s.Network.CheckedAt = &now
	s.Freshness = "live"
	s.Network.Status = "disconnected"
	s.Network.Link = "down"
	order := append([]string{}, i.Config.Network.PreferredInterfaces...)
	if len(order) == 0 || i.Config.Network.AllowFallback {
		for _, n := range i.Config.Network.ManagementInterfaces {
			found := false
			for _, p := range order {
				if p == n {
					found = true
				}
			}
			if !found {
				order = append(order, n)
			}
		}
	}
	for _, name := range order {
		for _, v := range interfaces {
			if v.Name != name || v.Loopback {
				continue
			}
			if s.Network.Interface == nil {
				n := name
				s.Network.Interface = &n
			}
			if !v.Up {
				continue
			}
			n := name
			s.Network.Interface = &n
			s.Network.Link = "up"
			s.Network.Status = "obtaining_address"
			sort.Slice(v.Addresses, func(a, b int) bool { return v.Addresses[a].String() < v.Addresses[b].String() })
			for _, ip := range v.Addresses {
				if !ip.IsGlobalUnicast() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
					continue
				}
				a := ip.String()
				s.Network.Address = &a
				s.Network.Status = "ready"
				s.NextScreen = "clusters"
				url := "http://" + net.JoinHostPort(a, strconv.Itoa(i.Config.HTTP.Port))
				s.ConsoleURL = &url
				return s, nil
			}
		}
	}
	return s, nil
}

// BindAddresses never widens an explicit interface policy to a wildcard listener.
func BindAddresses(c config.Config) ([]string, error) {
	addresses := []string{net.JoinHostPort("127.0.0.1", strconv.Itoa(c.HTTP.Port))}
	seen := map[string]bool{addresses[0]: true}
	for _, name := range c.Network.ManagementInterfaces {
		iface, err := net.InterfaceByName(name)
		if err != nil {
			return nil, fmt.Errorf("network.management_interfaces: %q unavailable", name)
		}
		addrs, err := iface.Addrs()
		if err != nil {
			return nil, err
		}
		for _, a := range addrs {
			ip, _, err := net.ParseCIDR(a.String())
			if err != nil || !ip.IsGlobalUnicast() {
				continue
			}
			addr := net.JoinHostPort(ip.String(), strconv.Itoa(c.HTTP.Port))
			if !seen[addr] {
				addresses = append(addresses, addr)
				seen[addr] = true
			}
		}
	}
	return addresses, nil
}
