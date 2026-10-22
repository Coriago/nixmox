package network

import (
	"context"
	"errors"
	"net"
	"nixmox/internal/config"
	"nixmox/internal/domain"
	"slices"
	"sort"
	"strconv"
	"syscall"
	"time"
)

// The peer's published port is authoritative. Loopback advertisements cannot
// identify another node, and no URL supplied by a narinfo can become a peer.
func MachineAddresses(endpoints []domain.Endpoint, policy config.Network) []string {
	rows := []domain.Endpoint{}
	seen := map[string]bool{}
	for _, e := range endpoints {
		if e.Service != "machine" || e.Transport != "https" {
			continue
		}
		host, port, err := net.SplitHostPort(e.Address)
		number, portErr := strconv.Atoi(port)
		if err != nil || portErr != nil || number < 1 || number > 65535 {
			continue
		}
		ip := net.ParseIP(host)
		if ip == nil || ip.IsLoopback() || ip.IsUnspecified() {
			continue
		}
		if seen[e.Address] {
			continue
		}
		seen[e.Address] = true
		rows = append(rows, e)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := endpointPreference(rows[i].Address, policy), endpointPreference(rows[j].Address, policy)
		if a != b {
			return a < b
		}
		return rows[i].Address < rows[j].Address
	})
	result := []string{}
	for _, e := range rows {
		result = append(result, e.Address)
		if len(result) == 8 {
			break
		}
	}
	return result
}
func policyInterfaces(policy config.Network) []string {
	names := append([]string{}, policy.PreferredInterfaces...)
	if len(names) == 0 || policy.AllowFallback {
		for _, name := range policy.ManagementInterfaces {
			if !slices.Contains(names, name) {
				names = append(names, name)
			}
		}
	}
	return names
}
func endpointPreference(address string, policy config.Network) int {
	host, _, _ := net.SplitHostPort(address)
	ip := net.ParseIP(host)
	for i, name := range policyInterfaces(policy) {
		iface, err := net.InterfaceByName(name)
		if err != nil {
			continue
		}
		addresses, _ := iface.Addrs()
		for _, address := range addresses {
			_, subnet, err := net.ParseCIDR(address.String())
			if err == nil && subnet.Contains(ip) {
				return i
			}
		}
	}
	return 1000
}

// Binding the socket to a selected management interface enforces the same
// boundary for LAN and virtual interfaces; a failed route cannot widen it.
func PeerDialContext(policy config.Network) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		ip := net.ParseIP(host)
		if ip == nil {
			return nil, errors.New("machine endpoint must advertise an IP address")
		}
		if ip.IsLoopback() {
			return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, address)
		}
		for _, name := range policyInterfaces(policy) {
			if _, err = net.InterfaceByName(name); err != nil {
				continue
			}
			dialer := &net.Dialer{Timeout: 5 * time.Second, Control: func(_, _ string, raw syscall.RawConn) error {
				var bindErr error
				err := raw.Control(func(fd uintptr) {
					bindErr = syscall.SetsockoptString(int(fd), syscall.SOL_SOCKET, syscall.SO_BINDTODEVICE, name)
				})
				if err != nil {
					return err
				}
				return bindErr
			}}
			connection, err := dialer.DialContext(ctx, network, address)
			if err == nil {
				return connection, nil
			}
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
		}
		return nil, errors.New("no configured management interface can reach the machine")
	}
}
