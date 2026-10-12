package config

import (
	"fmt"
	"net"
	"regexp"
	"strings"
)

var nodeName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,252}$`)

func (c *Config) Validate() error {
	if !nodeName.MatchString(c.Hostname) || strings.Contains(c.Hostname, "..") {
		return fmt.Errorf("invalid hostname %q", c.Hostname)
	}
	if _, _, err := net.SplitHostPort(c.HTTPAddr); err != nil {
		return fmt.Errorf("http_addr: %w", err)
	}
	if c.GitPollInterval < 0 {
		return fmt.Errorf("git poll interval cannot be negative")
	}
	if c.NATSPort < 0 || c.NATSPort > 65535 || c.ClusterPort < 0 || c.ClusterPort > 65535 {
		return fmt.Errorf("invalid NATS port")
	}
	switch c.ApplyMode {
	case "switch", "boot", "test", "dry-activate":
	default:
		return fmt.Errorf("invalid apply mode %q", c.ApplyMode)
	}
	return nil
}

// ResolveAdvertise selects an up, non-loopback IPv4 interface for live media.
// Explicit advertise_addr always wins on multihomed systems.
func (c *Config) ResolveAdvertise() {
	if c.AdvertiseAddr != "" {
		return
	}
	ifaces, _ := net.Interfaces()
	for _, i := range ifaces {
		if i.Flags&net.FlagUp == 0 || i.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := i.Addrs()
		for _, a := range addrs {
			ip, _, err := net.ParseCIDR(a.String())
			if err == nil && ip.To4() != nil && ip.IsGlobalUnicast() {
				c.AdvertiseAddr = ip.String()
				return
			}
		}
	}
}
