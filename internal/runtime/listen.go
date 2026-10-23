package runtime

import (
	"fmt"
	"net"
	"slices"

	"nixmox/internal/domain"
)

// HTTPEndpoints supplies both listener addresses and their advertised interfaces.
// Only explicit management addresses are bound; loopback remains available locally.
func (c Config) HTTPEndpoints() ([]domain.Endpoint, error) {
	endpoint := func(ip, name string) domain.Endpoint {
		return domain.Endpoint{Service: "api", Transport: "http", Address: net.JoinHostPort(ip, fmt.Sprint(c.HTTP.Port)), Interface: name, Verified: true}
	}
	endpoints := []domain.Endpoint{endpoint("127.0.0.1", "")}
	for _, name := range c.Network.ManagementInterfaces {
		iface, err := net.InterfaceByName(name)
		if err != nil {
			return nil, fmt.Errorf("network.management_interfaces %s: %w", name, err)
		}
		values, err := iface.Addrs()
		if err != nil {
			return nil, err
		}
		for _, value := range values {
			ip, _, err := net.ParseCIDR(value.String())
			if err != nil || (!ip.IsGlobalUnicast() && !ip.IsLoopback()) {
				continue
			}
			next := endpoint(ip.String(), name)
			if !slices.ContainsFunc(endpoints, func(e domain.Endpoint) bool { return e.Address == next.Address }) {
				endpoints = append(endpoints, next)
			}
		}
	}
	return endpoints, nil
}
