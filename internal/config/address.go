package config

import "net"

// PeerAddress must be reachable by other nodes, not the HTTP bind wildcard.
func (c *Config) PeerAddress() string {
	host, port, err := net.SplitHostPort(c.HTTPAddr)
	if err != nil {
		return ""
	}
	if c.AdvertiseAddr != "" {
		host = c.AdvertiseAddr
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		return ""
	}
	return net.JoinHostPort(host, port)
}
