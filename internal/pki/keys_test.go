package pki

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"net/url"
	"nixmox/internal/storage"
	"os"
	"path/filepath"
	"testing"
)

func testStore(t *testing.T) *storage.Store {
	t.Helper()
	s, err := storage.Open(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestIssuanceIgnoresRequestedPrivilegesAndPreservesKeys(t *testing.T) {
	st := testStore(t)
	k, err := Open(st)
	if err != nil {
		t.Fatal(err)
	}
	if Fingerprint(k.Daemon.Leaf.PublicKey) == Fingerprint(k.Broker.Leaf.PublicKey) {
		t.Fatal("shared keys")
	}
	a, err := OpenAuthority(st, "c_test", true)
	if err != nil {
		t.Fatal(err)
	}
	_, foreign, _ := ed25519.GenerateKey(rand.Reader)
	uri, _ := url.Parse("spiffe://c_other/node/admin/broker/999")
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: "admin"}, DNSNames: []string{"attacker.example"}, URIs: []*url.URL{uri}}, foreign)
	if err != nil {
		t.Fatal(err)
	}
	csr, err := ParseCSR(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der}))
	if err != nil {
		t.Fatal(err)
	}
	b, err := a.Issue(csr, "n_cedar", "daemon", 2)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(b)
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if !Bound(cert, "c_test", "n_cedar", "daemon", 2) || cert.Subject.CommonName != "n_cedar" || cert.IsCA {
		t.Fatal("caller-controlled privilege")
	}
	if _, err := cert.Verify(x509.VerifyOptions{Roots: Pool(a.Certificate), DNSName: ServerName(cert)}); err != nil {
		t.Fatal(err)
	}
	k2, err := Open(st)
	if err != nil {
		t.Fatal(err)
	}
	if Fingerprint(k2.Broker.Leaf.PublicKey) != Fingerprint(k.Broker.Leaf.PublicKey) {
		t.Fatal("broker identity changed")
	}
	if err = os.Remove(filepath.Join(st.Root, "pki/issuer/key.pem")); err != nil {
		t.Fatal(err)
	}
	if _, err = OpenAuthority(st, "c_test", false); err == nil {
		t.Fatal("missing issuer key accepted")
	}
	if _, err = os.Stat(filepath.Join(st.Root, "pki/issuer/key.pem")); !os.IsNotExist(err) {
		t.Fatal("missing issuer key regenerated")
	}
}
