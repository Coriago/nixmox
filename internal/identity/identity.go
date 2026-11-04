// Package identity owns private, persistent node identity and enrollment proofs.
package identity

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	"golang.org/x/sys/unix"
)

type State struct {
	NodeID        string   `json:"nodeId"`
	Name          string   `json:"name"`
	ClusterID     string   `json:"clusterId,omitempty"`
	Status        string   `json:"status"`
	Environment   string   `json:"environment"`
	ResumeTaskIDs []string `json:"resumeTaskIds"`
}

// Node holds the exclusive process lock until Close. Never serialize its key.
type Node struct {
	State State
	Key   ed25519.PrivateKey
	dir   string
	lock  *os.File
}

// Open refuses corrupt or incomplete existing identities rather than silently
// replacing keys. The key is committed before state so first-start interruption
// can safely reuse it on the next start.
func Open(dir, name string) (*Node, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return nil, err
	}
	lock, err := os.OpenFile(filepath.Join(dir, "identity.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		lock.Close()
		return nil, fmt.Errorf("identity already in use: %w", err)
	}
	n := &Node{dir: dir, lock: lock}
	success := false
	defer func() {
		if !success {
			n.Close()
		}
	}()
	state, stateErr := os.ReadFile(filepath.Join(dir, "state.json"))
	if stateErr != nil && !errors.Is(stateErr, os.ErrNotExist) {
		return nil, stateErr
	}
	key, err := os.ReadFile(filepath.Join(dir, "node.key"))
	if errors.Is(err, os.ErrNotExist) && errors.Is(stateErr, os.ErrNotExist) {
		_, private, e := ed25519.GenerateKey(rand.Reader)
		if e != nil {
			return nil, e
		}
		der, e := x509.MarshalPKCS8PrivateKey(private)
		if e != nil {
			return nil, e
		}
		key = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
		err = WritePrivate(filepath.Join(dir, "node.key"), key)
	}
	if err != nil {
		return nil, fmt.Errorf("read node key: %w", err)
	}
	block, rest := pem.Decode(key)
	if block == nil || block.Type != "PRIVATE KEY" || len(rest) != 0 {
		return nil, errors.New("invalid node private key")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	var ok bool
	n.Key, ok = parsed.(ed25519.PrivateKey)
	if !ok {
		return nil, errors.New("node key must be Ed25519")
	}
	if stateErr == nil {
		if err = json.Unmarshal(state, &n.State); err != nil {
			return nil, err
		}
		if n.State.NodeID == "" || n.State.Name == "" {
			return nil, errors.New("incomplete node state")
		}
	} else {
		if name == "" {
			name, err = os.Hostname()
			if err != nil {
				return nil, err
			}
		}
		n.State = State{NodeID: uuid.NewString(), Name: name, Status: "unjoined", Environment: "installed", ResumeTaskIDs: []string{}}
		if err = n.Save(); err != nil {
			return nil, err
		}
	}
	success = true
	return n, nil
}

func (n *Node) Save() error {
	data, err := json.MarshalIndent(n.State, "", "  ")
	if err != nil {
		return err
	}
	return WritePrivate(filepath.Join(n.dir, "state.json"), append(data, '\n'))
}

func (n *Node) Close() error {
	if n.lock == nil {
		return nil
	}
	err := n.lock.Close()
	n.lock = nil
	return err
}

// WritePrivate atomically replaces one private file and fsyncs the containing
// directory. Callers must keep the parent directory private and trusted.
func WritePrivate(path string, data []byte) error {
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer root.Close()
	return WritePrivateAt(root, filepath.Base(path), data)
}

// WritePrivateAt performs the same atomic private write beneath a pinned root.
// Destination symlinks cannot redirect installation secrets into the live host.
func WritePrivateAt(root *os.Root, name string, data []byte) error {
	dir := filepath.Dir(name)
	temporary := filepath.Join(dir, ".nixmox-"+uuid.NewString())
	f, err := root.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer root.Remove(temporary)
	if _, err = f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = root.Rename(temporary, name); err != nil {
		return err
	}
	directory, err := root.Open(dir)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func (n *Node) CSR() ([]byte, error) {
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: n.State.NodeID}}, n.Key)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der}), nil
}

func Thumbprint(public any) (string, error) {
	der, err := x509.MarshalPKIXPublicKey(public)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(der)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func ParseCertificate(data []byte) (*x509.Certificate, error) {
	block, rest := pem.Decode(data)
	if block == nil || block.Type != "CERTIFICATE" || len(rest) != 0 {
		return nil, errors.New("invalid certificate PEM")
	}
	return x509.ParseCertificate(block.Bytes)
}

type Authority struct {
	Certificate *x509.Certificate
	Key         ed25519.PrivateKey
	PEM         []byte
}

func serial() (*big.Int, error) { return rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128)) }

func NewAuthority(clusterID string, now time.Time) (*Authority, error) {
	if clusterID == "" {
		return nil, errors.New("cluster ID required")
	}
	public, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	number, err := serial()
	if err != nil {
		return nil, err
	}
	template := &x509.Certificate{
		SerialNumber:          number,
		Subject:               pkix.Name{CommonName: clusterID},
		NotBefore:             now.Add(-time.Minute),
		NotAfter:              now.AddDate(10, 0, 0),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, public, key)
	if err != nil {
		return nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	return &Authority{Certificate: cert, Key: key, PEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})}, nil
}

// Issue takes connection names from the trusted enrollment coordinator, never
// from untrusted CSR extensions. Identity and key continuity are both checked.
func (a *Authority) Issue(csrPEM []byte, nodeID, expectedThumbprint string, names []string, now time.Time) ([]byte, error) {
	block, rest := pem.Decode(csrPEM)
	if block == nil || block.Type != "CERTIFICATE REQUEST" || len(rest) != 0 {
		return nil, errors.New("invalid CSR PEM")
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		return nil, err
	}
	if err = csr.CheckSignature(); err != nil {
		return nil, err
	}
	if nodeID == "" || csr.Subject.CommonName != nodeID {
		return nil, errors.New("CSR node identity mismatch")
	}
	fingerprint, err := Thumbprint(csr.PublicKey)
	if err != nil {
		return nil, err
	}
	if expectedThumbprint == "" || fingerprint != expectedThumbprint {
		return nil, errors.New("CSR public key mismatch")
	}
	number, err := serial()
	if err != nil {
		return nil, err
	}
	expiry := now.AddDate(1, 0, 0)
	if expiry.After(a.Certificate.NotAfter) {
		expiry = a.Certificate.NotAfter
	}
	if !expiry.After(now) {
		return nil, errors.New("cluster CA expired")
	}
	template := &x509.Certificate{
		SerialNumber: number,
		Subject:      pkix.Name{CommonName: nodeID},
		NotBefore:    now.Add(-time.Minute),
		NotAfter:     expiry,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
	}
	for _, name := range names {
		if ip := net.ParseIP(name); ip != nil {
			template.IPAddresses = append(template.IPAddresses, ip)
		} else {
			template.DNSNames = append(template.DNSNames, name)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, template, a.Certificate, csr.PublicKey, a.Key)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), nil
}

// VerifyEnrollment must run only after checking the persisted enrollment request
// ID. It validates the saved CA, node ID and local key before accepting a cert.
func (n *Node) VerifyEnrollment(certificate, caPEM []byte, now time.Time) error {
	cert, err := ParseCertificate(certificate)
	if err != nil {
		return err
	}
	ca, err := ParseCertificate(caPEM)
	if err != nil {
		return err
	}
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	if _, err = cert.Verify(x509.VerifyOptions{Roots: roots, CurrentTime: now, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		return err
	}
	if cert.Subject.CommonName != n.State.NodeID {
		return errors.New("certificate node identity mismatch")
	}
	public, ok := cert.PublicKey.(ed25519.PublicKey)
	if !ok || !public.Equal(n.Key.Public()) {
		return errors.New("certificate public key mismatch")
	}
	return nil
}
