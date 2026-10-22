package pki

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"time"
)

// MutualServer requires both chain validation and the current explicit key grant.
// CA signing alone never admits a route, leaf, or local service client.
func MutualServer(pair tls.Certificate, roots *x509.CertPool, allowed func(*x509.Certificate) bool) *tls.Config {
	return &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{pair}, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: roots, VerifyConnection: func(s tls.ConnectionState) error {
		if len(s.VerifiedChains) == 0 || len(s.PeerCertificates) == 0 || allowed == nil || !allowed(s.PeerCertificates[0]) {
			return errors.New("machine peer is not authorized")
		}
		return nil
	}}
}
func MutualClient(pair tls.Certificate, roots *x509.CertPool, serverName string, allowed func(*x509.Certificate) bool) *tls.Config {
	return &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{pair}, GetClientCertificate: func(*tls.CertificateRequestInfo) (*tls.Certificate, error) { return &pair, nil }, RootCAs: roots, ServerName: serverName, VerifyConnection: func(s tls.ConnectionState) error {
		if len(s.VerifiedChains) == 0 || len(s.PeerCertificates) == 0 || allowed == nil || !allowed(s.PeerCertificates[0]) {
			return errors.New("machine peer is not authorized")
		}
		return nil
	}}
}
func Pool(certs ...*x509.Certificate) *x509.CertPool {
	p := x509.NewCertPool()
	for _, c := range certs {
		p.AddCert(c)
	}
	return p
}
func OnlyKey(cert *x509.Certificate) func(*x509.Certificate) bool {
	key := Fingerprint(cert.PublicKey)
	return func(peer *x509.Certificate) bool { return key != "" && Fingerprint(peer.PublicKey) == key }
}

// EnrollmentServer proves possession for self-signed clients, but grants no
// membership. Its handler must authorize every route and bind requests to keys.
func EnrollmentServer(pair tls.Certificate) *tls.Config {
	return &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{pair}, ClientAuth: tls.RequestClientCert, VerifyConnection: func(s tls.ConnectionState) error {
		if len(s.PeerCertificates) == 0 {
			return nil
		} // narrow removal-status route may omit a revoked client cert
		c := s.PeerCertificates[0]
		if time.Now().Before(c.NotBefore) || time.Now().After(c.NotAfter) {
			return errors.New("machine certificate expired or not yet valid")
		}
		if Fingerprint(c.PublicKey) == "" {
			return errors.New("unsupported machine key")
		}
		return nil
	}}
}
