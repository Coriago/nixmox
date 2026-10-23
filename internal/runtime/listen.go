package runtime

import (
	"fmt"
	"net"
	"slices"
)

// HTTPAddresses binds only explicit management addresses, never a wildcard.
// Loopback remains available for the local CLI even with management interfaces.
func (c Config) HTTPAddresses() ([]string, error) {
	addresses := []string{net.JoinHostPort("127.0.0.1", fmt.Sprint(c.HTTP.Port))}
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
			address := net.JoinHostPort(ip.String(), fmt.Sprint(c.HTTP.Port))
			if !slices.Contains(addresses, address) {
				addresses = append(addresses, address)
			}
		}
	}
	return addresses, nil
}
