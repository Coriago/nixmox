package pki

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"nixmox/internal/domain"
	"nixmox/internal/storage"
)

type Authority struct {
	Certificate *x509.Certificate
	Key         ed25519.PrivateKey
	PEM         []byte
	ClusterID   string
}

func OpenAuthority(st *storage.Store, cluster string, create bool) (*Authority, error) {
	if !domain.SafeID.MatchString(cluster) {
		return nil, errors.New("invalid cluster ID")
	}
	if create {
		if err := os.MkdirAll(filepath.Join(st.Root, "pki/issuer"), 0700); err != nil {
			return nil, err
		}
	}
	path := "pki/issuer/cert.pem"
	b, err := os.ReadFile(filepath.Join(st.Root, path))
	if err != nil && (!create || !errors.Is(err, os.ErrNotExist)) {
		return nil, errors.New("issuer unavailable")
	}
	if b != nil {
		if _, err := os.Stat(filepath.Join(st.Root, "pki/issuer/key.pem")); err != nil {
			return nil, errors.New("issuer private key unavailable")
		}
	}
	key, err := loadOrCreateKey(st, "pki/issuer/key.pem")
	if err != nil {
		return nil, err
	}
	if b == nil {
		sn, err := serial()
		if err != nil {
			return nil, err
		}
		template := &x509.Certificate{SerialNumber: sn, Subject: pkix.Name{CommonName: cluster}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().AddDate(10, 0, 0), IsCA: true, BasicConstraintsValid: true, MaxPathLenZero: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign}
		der, err := x509.CreateCertificate(rand.Reader, template, template, key.Public(), key)
		if err != nil {
			return nil, err
		}
		b = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
		if err = st.WriteBytes(path, b); err != nil {
			return nil, err
		}
	}
	p, _ := pem.Decode(b)
	if p == nil {
		return nil, errors.New("invalid issuer certificate")
	}
	c, err := x509.ParseCertificate(p.Bytes)
	if err != nil {
		return nil, err
	}
	if !c.IsCA || c.Subject.CommonName != cluster || Fingerprint(c.PublicKey) != Fingerprint(key.Public()) {
		return nil, errors.New("issuer identity mismatch")
	}
	return &Authority{Certificate: c, Key: key, PEM: b, ClusterID: cluster}, nil
}

// Issue ignores caller-selected CSR subjects, roles, SANs, and extensions.
// The caller must durably record the returned issuance before sending it.
func (a *Authority) Issue(csr *x509.CertificateRequest, node, role string, epoch int) ([]byte, error) {
	if !domain.SafeID.MatchString(node) || (role != "daemon" && role != "broker") || epoch < 1 || csr.CheckSignature() != nil {
		return nil, errors.New("invalid issuance binding")
	}
	sn, err := serial()
	if err != nil {
		return nil, err
	}
	identity := &url.URL{Scheme: "spiffe", Host: a.ClusterID, Path: "/node/" + node + "/" + role + "/" + strconv.Itoa(epoch)}
	c := &x509.Certificate{SerialNumber: sn, Subject: pkix.Name{CommonName: node}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(30 * 24 * time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}, URIs: []*url.URL{identity}, PublicKey: csr.PublicKey}
	c.DNSNames = []string{ServerName(c), role + ".nixmox"}
	der, err := x509.CreateCertificate(rand.Reader, c, a.Certificate, csr.PublicKey, a.Key)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), nil
}
func Bound(cert *x509.Certificate, cluster, node, role string, epoch int) bool {
	return len(cert.URIs) == 1 && cert.URIs[0].String() == "spiffe://"+cluster+"/node/"+node+"/"+role+"/"+strconv.Itoa(epoch)
}
