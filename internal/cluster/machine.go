package cluster

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"nixmox/internal/domain"
	"nixmox/internal/network"
	"nixmox/internal/pki"
	"strings"
	"time"
)

func (m *Manager) FullMember(ctx context.Context, node string) bool {
	if ctx.Err() != nil {
		return false
	}
	member, err := m.member(node)
	return err == nil && member.State == "full"
}

// Machine TLS permits enrollment certificates at the listener. Every ordinary
// machine route additionally verifies CA, node key, epoch and current scope.
func (m *Manager) AuthenticateMachine(ctx context.Context, cert *x509.Certificate, full bool) (Member, error) {
	var empty Member
	if cert == nil || len(cert.URIs) != 1 {
		return empty, domain.Err("UNAUTHORIZED", "An authorized cluster machine certificate is required.", 401)
	}
	parts := strings.Split(strings.Trim(cert.URIs[0].Path, "/"), "/")
	if len(parts) != 4 || parts[0] != "node" || parts[2] != "daemon" || !domain.SafeID.MatchString(parts[1]) {
		return empty, domain.Err("FORBIDDEN", "Invalid machine identity binding.", 403)
	}
	m.mu.RLock()
	if m.membership == nil {
		m.mu.RUnlock()
		return empty, unavailable()
	}
	membership := *m.membership
	m.mu.RUnlock()
	member, err := m.member(parts[1])
	if err != nil {
		return empty, err
	}
	if member.State != "full" && member.State != "pending" || full && member.State != "full" {
		return empty, domain.Err("FORBIDDEN", "This machine operation requires full membership.", 403)
	}
	block, _ := pem.Decode([]byte(membership.CA))
	if block == nil {
		return empty, unavailable()
	}
	ca, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return empty, unavailable()
	}
	if _, err = cert.Verify(x509.VerifyOptions{Roots: pki.Pool(ca), KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, DNSName: "daemon.nixmox"}); err != nil || !pki.Bound(cert, membership.ClusterID, member.NodeID, "daemon", member.Epoch) || pki.Fingerprint(cert.PublicKey) != member.DaemonKey {
		return empty, domain.Err("FORBIDDEN", "Machine certificate is not a current authorized member.", 403)
	}
	return member, ctx.Err()
}
func (m *Manager) MachineClient(ctx context.Context, node string) (*http.Client, error) {
	member, err := m.member(node)
	if err != nil {
		return nil, err
	}
	m.mu.RLock()
	if m.membership == nil {
		m.mu.RUnlock()
		return nil, unavailable()
	}
	membership, pair := *m.membership, m.keys.Daemon
	m.mu.RUnlock()
	block, _ := pem.Decode([]byte(membership.CA))
	if block == nil {
		return nil, unavailable()
	}
	ca, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, unavailable()
	}
	tlsConfig := pki.MutualClient(pair, pki.Pool(ca), "daemon.nixmox", func(cert *x509.Certificate) bool {
		current, err := m.member(node)
		return err == nil && (current.State == "full" || current.State == "pending") && current.Epoch == member.Epoch && pki.Bound(cert, membership.ClusterID, node, "daemon", member.Epoch) && pki.Fingerprint(cert.PublicKey) == current.DaemonKey
	})
	tlsConfig.MinVersion = tls.VersionTLS13
	transport := &http.Transport{TLSClientConfig: tlsConfig, DialContext: network.BoundedDial(network.PeerDialContext(m.config.Network)), TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 5 * time.Second, MaxResponseHeaderBytes: 64 << 10, MaxConnsPerHost: 4, MaxIdleConns: 8, IdleConnTimeout: 30 * time.Second, DisableCompression: true}
	return &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, nil
}
