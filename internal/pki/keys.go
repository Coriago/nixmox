// Package pki owns machine keys and X.509 mechanics. Membership and enrollment
// decisions belong to the cluster service, not certificate names supplied by peers.
package pki

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"time"

	"nixmox/internal/domain"
	"nixmox/internal/storage"
)

type Keys struct{ Daemon, Broker tls.Certificate }

func Fingerprint(key any) string {
	b, err := x509.MarshalPKIXPublicKey(key)
	if err != nil {
		return ""
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func ServerName(cert *x509.Certificate) string {
	return "key-" + Fingerprint(cert.PublicKey) + ".nixmox"
}
func serial() (*big.Int, error) { return rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128)) }

func LoadPair(st *storage.Store, directory string) (tls.Certificate, error) {
	pair, err := tls.LoadX509KeyPair(filepath.Join(st.Root, directory, "cert.pem"), filepath.Join(st.Root, directory, "key.pem"))
	if err != nil {
		return pair, errors.New("machine key pair unavailable")
	}
	if _, ok := pair.PrivateKey.(ed25519.PrivateKey); !ok {
		return pair, errors.New("unsupported machine key type")
	}
	pair.Leaf, err = x509.ParseCertificate(pair.Certificate[0])
	return pair, err
}

func Open(st *storage.Store) (Keys, error) {
	var keys Keys
	id, err := st.Identity()
	if err != nil {
		return keys, err
	}
	keys.Daemon, err = LoadPair(st, "pki/daemon")
	if err != nil {
		return keys, err
	}
	if err = os.MkdirAll(filepath.Join(st.Root, "pki/broker"), 0700); err != nil {
		return keys, err
	}
	_, err = os.Stat(filepath.Join(st.Root, "pki/broker/cert.pem"))
	if errors.Is(err, os.ErrNotExist) {
		// An enrolled node must never silently replace a missing credential.
		if id.ClusterID != nil {
			return keys, errors.New("enrolled broker credentials unavailable")
		}
		key, err := loadOrCreateKey(st, "pki/broker/key.pem")
		if err != nil {
			return keys, err
		}
		cert, err := selfSigned(id.NodeID, key)
		if err != nil {
			return keys, err
		}
		if err = st.WriteBytes("pki/broker/cert.pem", cert); err != nil {
			return keys, err
		}
	} else if err != nil {
		return keys, err
	}
	keys.Broker, err = LoadPair(st, "pki/broker")
	if err == nil && Fingerprint(keys.Daemon.Leaf.PublicKey) == Fingerprint(keys.Broker.Leaf.PublicKey) {
		return keys, errors.New("daemon and broker keys must be separate")
	}
	return keys, err
}

func loadOrCreateKey(st *storage.Store, path string) (ed25519.PrivateKey, error) {
	b, err := os.ReadFile(filepath.Join(st.Root, path))
	if err == nil {
		p, _ := pem.Decode(b)
		if p == nil {
			return nil, errors.New("invalid private key")
		}
		v, err := x509.ParsePKCS8PrivateKey(p.Bytes)
		if err != nil {
			return nil, errors.New("invalid private key")
		}
		key, ok := v.(ed25519.PrivateKey)
		if !ok {
			return nil, errors.New("unsupported private key")
		}
		return key, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	b, err = x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, err
	}
	err = st.WriteBytes(path, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: b}))
	return key, err
}
func selfSigned(node string, key ed25519.PrivateKey) ([]byte, error) {
	if !domain.SafeID.MatchString(node) {
		return nil, errors.New("invalid node ID")
	}
	sn, err := serial()
	if err != nil {
		return nil, err
	}
	cert := &x509.Certificate{SerialNumber: sn, Subject: pkix.Name{CommonName: node}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().AddDate(1, 0, 0), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}, PublicKey: key.Public()}
	cert.DNSNames = []string{ServerName(cert), "broker.nixmox"}
	b, err := x509.CreateCertificate(rand.Reader, cert, cert, key.Public(), key)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: b}), nil
}
func CSR(pair tls.Certificate) ([]byte, error) {
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, pair.PrivateKey)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der}), nil
}
func ParseCSR(data []byte) (*x509.CertificateRequest, error) {
	if len(data) > 16<<10 {
		return nil, errors.New("CSR exceeds limit")
	}
	p, rest := pem.Decode(data)
	if p == nil || p.Type != "CERTIFICATE REQUEST" || len(rest) != 0 {
		return nil, errors.New("invalid CSR encoding")
	}
	csr, err := x509.ParseCertificateRequest(p.Bytes)
	if err != nil || csr.CheckSignature() != nil {
		return nil, errors.New("invalid CSR signature")
	}
	if _, ok := csr.PublicKey.(ed25519.PublicKey); !ok {
		return nil, errors.New("unsupported CSR key")
	}
	return csr, nil
}
