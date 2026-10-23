// Package platform provides narrow host observations and explicitly authorized effects.
package platform

import (
	"context"
	"net"
	"time"

	"nixmox/internal/domain"
)

// NetworkStatus observes only configured management interfaces. Loopback and
// unrelated interfaces cannot establish installer/network readiness.
func NetworkStatus(ctx context.Context, node domain.ID, interfaces []string) (domain.NetworkStatus, error) {
	status := domain.NetworkStatus{NodeID: node, Environment: domain.Bootstrap, NextScreen: "network",
		WiFiNetworks: []domain.WiFiNetwork{}, Network: domain.NetworkConnection{Link: "down", Status: "disconnected", CheckedAt: time.Now().UTC()}}
	for _, name := range interfaces {
		if err := ctx.Err(); err != nil {
			return status, err
		}
		iface, err := net.InterfaceByName(name)
		if err != nil {
			return status, err
		}
		if iface.Flags&net.FlagLoopback != 0 || iface.Flags&net.FlagUp == 0 {
			continue
		}
		addresses, err := iface.Addrs()
		if err != nil {
			return status, err
		}
		for _, address := range addresses {
			ip, _, err := net.ParseCIDR(address.String())
			if err != nil || !ip.IsGlobalUnicast() {
				continue
			}
			value := ip.String()
			status.Network.Interface, status.Network.Address = &name, &value
			status.Network.Link, status.Network.Status, status.NextScreen = "up", "ready", "clusters"
			return status, nil
		}
	}
	return status, nil
}
