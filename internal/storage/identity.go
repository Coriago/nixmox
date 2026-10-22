package storage

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"time"

	"nixmox/internal/domain"
)

func (s *Store) Identity() (domain.Identity, error) {
	var id domain.Identity
	err := s.Read("identity.json", &id)
	if err == nil {
		if id.SchemaVersion != 1 || !domain.SafeID.MatchString(id.NodeID) {
			return id, errors.New("unsupported identity state")
		}
		pair, err := tls.LoadX509KeyPair(filepath.Join(s.Root, "pki/daemon/cert.pem"), filepath.Join(s.Root, "pki/daemon/key.pem"))
		if err != nil {
			return id, errors.New("identity credentials unavailable")
		}
		key, ok := pair.PrivateKey.(ed25519.PrivateKey)
		if !ok {
			return id, errors.New("unsupported identity key")
		}
		pub := key.Public().(ed25519.PublicKey)
		sum := sha256.Sum256(pub)
		if id.PublicKey != base64.StdEncoding.EncodeToString(pub) || id.KeyID != "key_"+hex.EncodeToString(sum[:]) {
			return id, errors.New("identity key mismatch")
		}
		return id, nil
	}
	if !os.IsNotExist(err) {
		return id, err
	}
	var key ed25519.PrivateKey
	b, err := os.ReadFile(filepath.Join(s.Root, "pki/daemon/key.pem"))
	if err == nil {
		block, _ := pem.Decode(b)
		if block == nil {
			return id, errors.New("invalid daemon key")
		}
		k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return id, err
		}
		var ok bool
		key, ok = k.(ed25519.PrivateKey)
		if !ok {
			return id, errors.New("invalid daemon key type")
		}
	} else if os.IsNotExist(err) {
		_, key, err = ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return id, err
		}
		der, err := x509.MarshalPKCS8PrivateKey(key)
		if err != nil {
			return id, err
		}
		if err = s.WriteBytes("pki/daemon/key.pem", pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})); err != nil {
			return id, err
		}
	} else {
		return id, err
	}
	pub := key.Public().(ed25519.PublicKey)
	sum := sha256.Sum256(pub)
	id = domain.Identity{SchemaVersion: 1, Version: 1, NodeID: domain.NewID("n_"), KeyID: "key_" + hex.EncodeToString(sum[:]), PublicKey: base64.StdEncoding.EncodeToString(pub)}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return id, err
	}
	cert := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: id.NodeID}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().AddDate(1, 0, 0), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}, DNSNames: []string{id.NodeID}}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, pub, key)
	if err != nil {
		return id, err
	}
	if err = s.WriteBytes("pki/daemon/cert.pem", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})); err != nil {
		return id, err
	}
	if err = syncDir(filepath.Join(s.Root, "pki")); err != nil {
		return id, err
	}
	return id, s.Write("identity.json", id)
}
