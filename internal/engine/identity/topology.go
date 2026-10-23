package identity

import (
	"crypto/ed25519"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strconv"
	"time"

	"nixmox/internal/domain"
	"nixmox/internal/engine/internal/shared"
)

func CertificatePins(value string) (domain.Digest, domain.Digest, error) {
	cert, err := parseCertificate(value)
	if err != nil {
		return "", "", err
	}
	return shared.Fingerprint(cert.RawSubjectPublicKeyInfo), shared.Fingerprint(cert.Raw), nil
}

func (l *Local) issuer() (issuerState, ed25519.PrivateKey, error) {
	var issuer issuerState
	if err := l.load("pki/issuer/genesis.json", &issuer); err != nil {
		return issuer, nil, err
	}
	pair, err := tls.X509KeyPair([]byte(issuer.CA), []byte(issuer.Key))
	if err != nil {
		return issuer, nil, err
	}
	key, ok := pair.PrivateKey.(ed25519.PrivateKey)
	if !ok {
		return issuer, nil, errors.New("invalid issuer key")
	}
	return issuer, key, nil
}

// Route certificates are distinct from pending leaf credentials, but use the
// existing locally-held broker key. A common cluster SAN permits a routed mesh
// without disabling standard TLS name/CA verification; exact pins constrain peers.
func (l *Local) RouteCertificate(node domain.ID, epoch uint64, csrPEM string, expected domain.Digest) (string, error) {
	csr, err := parseCSR(csrPEM)
	if err != nil {
		return "", err
	}
	if shared.Fingerprint(csr.RawSubjectPublicKeyInfo) != expected {
		return "", errors.New("route CSR differs from admitted broker key")
	}
	path := "pki/issuer/routes/" + shared.Filename(node) + "-" + strconv.FormatUint(epoch, 10) + ".json"
	var cert string
	err = l.load(path, &cert)
	if err == nil {
		return cert, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	issuer, key, err := l.issuer()
	if err != nil {
		return "", err
	}
	ca, err := parseCertificate(issuer.CA)
	if err != nil {
		return "", err
	}
	now := time.Now()
	cert, err = certificatePEM(&x509.Certificate{Subject: pkix.Name{CommonName: shared.Token(node)}, DNSNames: []string{shared.Token(node), shared.Token(issuer.Cluster.ID)}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(domain.CertificateLifetime), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth}}, ca, csr.PublicKey, key)
	if err != nil {
		return "", err
	}
	return cert, l.save(path, cert)
}
func (l *Local) SignTopology(topology domain.Topology) (domain.SignedTopology, error) {
	issuer, key, err := l.issuer()
	if err != nil {
		return domain.SignedTopology{}, err
	}
	if topology.ClusterID != issuer.Cluster.ID {
		return domain.SignedTopology{}, errors.New("topology issuer mismatch")
	}
	data, err := json.Marshal(topology)
	if err != nil {
		return domain.SignedTopology{}, err
	}
	return domain.SignedTopology{Topology: topology, Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(key, data))}, nil
}
func verifyTopology(signed domain.SignedTopology, trust domain.ClusterTrust) error {
	t := signed.Topology
	if t.ClusterID != trust.Cluster.ID || t.Version == 0 || (t.Replicas != 1 && t.Replicas != 3) || len(t.Peers) < 1 || len(t.Peers) > 3 {
		return errors.New("invalid routed topology")
	}
	ca, err := parseCertificate(trust.Issuance.ClusterCA)
	if err != nil {
		return err
	}
	key, ok := ca.PublicKey.(ed25519.PublicKey)
	if !ok {
		return errors.New("invalid topology signer")
	}
	signature, err := base64.StdEncoding.DecodeString(signed.Signature)
	if err != nil {
		return err
	}
	data, err := json.Marshal(t)
	if err != nil {
		return err
	}
	if !ed25519.Verify(key, data, signature) {
		return errors.New("invalid topology signature")
	}
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	for id, peer := range t.Peers {
		if id != peer.NodeID || peer.Epoch == 0 {
			return errors.New("invalid route peer identity")
		}
		cert, err := parseCertificate(peer.Certificate)
		if err != nil {
			return err
		}
		for _, usage := range []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth} {
			if _, err = cert.Verify(x509.VerifyOptions{Roots: pool, DNSName: shared.Token(trust.Cluster.ID), KeyUsages: []x509.ExtKeyUsage{usage}}); err != nil {
				return err
			}
		}
		if err = cert.VerifyHostname(shared.Token(id)); err != nil {
			return err
		}
	}
	return nil
}
func (l *Local) ApplyTopology(signed domain.SignedTopology) error {
	if l.Trust == nil {
		return errors.New("topology requires enrolled trust")
	}
	copy := *l.Trust
	copy.Topology = &signed
	return l.Install(copy)
}
func (l *Local) validateTopology(trust domain.ClusterTrust) error {
	if trust.Topology == nil {
		if l.Trust != nil && l.Trust.Topology != nil {
			return errors.New("cannot discard routed topology")
		}
		return nil
	}
	if err := verifyTopology(*trust.Topology, trust); err != nil {
		return err
	}
	if l.Trust != nil && l.Trust.Topology != nil {
		old, next := l.Trust.Topology, trust.Topology
		if next.Topology.Version < old.Topology.Version || (next.Topology.Version == old.Topology.Version && !reflect.DeepEqual(old, next)) {
			return errors.New("topology version regressed or conflicted")
		}
	}
	if peer, ok := trust.Topology.Topology.Peers[l.Identity.NodeID]; ok {
		cert, err := parseCertificate(peer.Certificate)
		if err != nil {
			return err
		}
		if peer.Epoch != trust.Issuance.MembershipEpoch || shared.Fingerprint(cert.RawSubjectPublicKeyInfo) != l.BrokerKey() {
			return errors.New("local route credential binding mismatch")
		}
	}
	return nil
}
func (l *Local) RouteTLS() (*tls.Config, error) {
	if l.Trust == nil || l.Trust.Topology == nil {
		return nil, errors.New("routed topology unavailable")
	}
	peer, ok := l.Trust.Topology.Topology.Peers[l.Identity.NodeID]
	if !ok {
		return nil, errors.New("node has no route authorization")
	}
	cert, err := parseCertificate(peer.Certificate)
	if err != nil {
		return nil, err
	}
	cfg := l.PeerTLS(l.Trust.Cluster.ID)
	cfg.Certificates = []tls.Certificate{{Certificate: [][]byte{cert.Raw}, PrivateKey: l.Broker.PrivateKey, Leaf: cert}}
	return cfg, nil
}
