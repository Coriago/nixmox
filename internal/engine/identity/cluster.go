package identity

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"time"

	"github.com/google/uuid"

	"nixmox/internal/domain"
	"nixmox/internal/engine/internal/shared"
)

type issuerState struct {
	CommandID domain.ID      `json:"command_id"`
	Cluster   domain.Cluster `json:"cluster"`
	Key       string         `json:"key"`
	CA        string         `json:"ca"`
}

func (l *Local) load(path string, value any) error {
	root, err := os.OpenRoot(l.directory)
	if err != nil {
		return err
	}
	defer root.Close()
	data, err := root.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, value)
}

func (l *Local) save(path string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	root, err := os.OpenRoot(l.directory)
	if err != nil {
		return err
	}
	defer root.Close()
	return writeFile(root, path, data)
}

func parseCertificate(value string) (*x509.Certificate, error) {
	block, _ := pem.Decode([]byte(value))
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, errors.New("invalid certificate PEM")
	}
	return x509.ParseCertificate(block.Bytes)
}

func certificatePEM(template, parent *x509.Certificate, public any, key ed25519.PrivateKey) (string, error) {
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return "", err
	}
	template.SerialNumber = serial
	der, err := x509.CreateCertificate(rand.Reader, template, parent, public, key)
	if err != nil {
		return "", err
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), nil
}

// Create persists genesis and issuer material together, before any certificate
// escapes. Recovery reuses the same cluster/CA; another command cannot replace it.
func (l *Local) Create(command domain.ID, name string) (domain.Cluster, error) {
	var issuer issuerState
	err := l.load("pki/issuer/genesis.json", &issuer)
	if err == nil {
		if issuer.CommandID != command || issuer.Cluster.Name != name {
			return domain.Cluster{}, &domain.Error{Code: "INVALID_INPUT", Message: "Cluster genesis already belongs to another command"}
		}
		return issuer.Cluster, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return domain.Cluster{}, err
	}
	if l.Trust != nil {
		return domain.Cluster{}, &domain.Error{Code: "INVALID_INPUT", Message: "Node is already enrolled"}
	}
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return domain.Cluster{}, err
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return domain.Cluster{}, err
	}
	now := time.Now()
	template := &x509.Certificate{Subject: pkix.Name{CommonName: "Nixmox cluster CA"}, NotBefore: now.Add(-time.Minute), NotAfter: now.AddDate(10, 0, 0), IsCA: true, BasicConstraintsValid: true, MaxPathLenZero: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign}
	ca, err := certificatePEM(template, template, key.Public(), key)
	if err != nil {
		return domain.Cluster{}, err
	}
	issuer = issuerState{CommandID: command, Key: string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})), CA: ca,
		Cluster: domain.Cluster{Record: domain.Record{SchemaVersion: 1, ID: domain.ID("c_" + uuid.NewString()), Version: 1}, Name: name, Membership: domain.Pending, IssuerNodeID: l.Identity.NodeID, ReplicaNodeIDs: []domain.ID{l.Identity.NodeID}}}
	return issuer.Cluster, l.save("pki/issuer/genesis.json", issuer)
}

func (l *Local) Request(id domain.ID, name string) (domain.EnrollmentSubmission, error) {
	r := domain.EnrollmentSubmission{RequestID: id, NodeID: l.Identity.NodeID, ExpectedClusterName: name}
	for i, cert := range []tls.Certificate{l.Daemon, l.Broker} {
		der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, cert.PrivateKey)
		if err != nil {
			return r, err
		}
		value := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der}))
		if i == 0 {
			r.DaemonCSR = value
		} else {
			r.BrokerCSR = value
		}
	}
	data, err := json.Marshal(r)
	if err != nil {
		return r, err
	}
	r.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(l.Daemon.PrivateKey.(ed25519.PrivateKey), data))
	return r, nil
}

func parseCSR(value string) (*x509.CertificateRequest, error) {
	block, _ := pem.Decode([]byte(value))
	if block == nil || block.Type != "CERTIFICATE REQUEST" || len(value) > 8192 {
		return nil, errors.New("invalid CSR PEM")
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		return nil, err
	}
	if _, ok := csr.PublicKey.(ed25519.PublicKey); !ok {
		return nil, errors.New("CSR requires Ed25519")
	}
	return csr, csr.CheckSignature()
}

func RequestKeys(r domain.EnrollmentSubmission) (domain.Digest, domain.Digest, error) {
	daemon, err := parseCSR(r.DaemonCSR)
	if err != nil {
		return "", "", err
	}
	broker, err := parseCSR(r.BrokerCSR)
	if err != nil {
		return "", "", err
	}
	if bytes.Equal(daemon.RawSubjectPublicKeyInfo, broker.RawSubjectPublicKeyInfo) {
		return "", "", errors.New("daemon and broker require separate keys")
	}
	signature, err := base64.StdEncoding.DecodeString(r.Signature)
	if err != nil {
		return "", "", err
	}
	r.Signature = ""
	data, err := json.Marshal(r)
	if err != nil {
		return "", "", err
	}
	if !ed25519.Verify(daemon.PublicKey.(ed25519.PublicKey), data, signature) {
		return "", "", errors.New("enrollment request does not bind the identity and broker CSR")
	}
	return shared.Fingerprint(daemon.RawSubjectPublicKeyInfo), shared.Fingerprint(broker.RawSubjectPublicKeyInfo), nil
}

type issuedRecord struct {
	Request  domain.EnrollmentSubmission `json:"request"`
	Issuance domain.Issuance             `json:"issuance"`
}

// Issued only retrieves committed public material; losing the issuance ledger
// must never silently create a replacement certificate.
func (l *Local) Issued(r domain.EnrollmentSubmission) (domain.Issuance, error) {
	var record issuedRecord
	if err := l.load("pki/issuer/issued/"+shared.Filename(r.RequestID)+".json", &record); err != nil {
		return domain.Issuance{}, err
	}
	if record.Request != r || record.Issuance.RequestID != r.RequestID {
		return domain.Issuance{}, errors.New("issuance binding mismatch")
	}
	return record.Issuance, nil
}

func (l *Local) Issue(r domain.EnrollmentSubmission, epoch uint64, grants domain.Grants) (domain.Issuance, error) {
	var old issuedRecord
	path := "pki/issuer/issued/" + shared.Filename(r.RequestID) + ".json"
	err := l.load(path, &old)
	if err == nil {
		if old.Request != r || old.Issuance.MembershipEpoch != epoch {
			return domain.Issuance{}, errors.New("issuance request conflict")
		}
		return old.Issuance, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return domain.Issuance{}, err
	}
	if _, _, err = RequestKeys(r); err != nil {
		return domain.Issuance{}, err
	}
	var issuer issuerState
	if err = l.load("pki/issuer/genesis.json", &issuer); err != nil {
		return domain.Issuance{}, err
	}
	ca, err := parseCertificate(issuer.CA)
	if err != nil {
		return domain.Issuance{}, err
	}
	pair, err := tls.X509KeyPair([]byte(issuer.CA), []byte(issuer.Key))
	if err != nil {
		return domain.Issuance{}, err
	}
	key, ok := pair.PrivateKey.(ed25519.PrivateKey)
	if !ok {
		return domain.Issuance{}, errors.New("invalid issuer key")
	}
	issuance := domain.Issuance{ID: shared.ID("issue_", r.RequestID), RequestID: r.RequestID, ClusterID: issuer.Cluster.ID, MembershipEpoch: epoch, ClusterCA: issuer.CA, ClusterFingerprint: shared.Fingerprint(ca.RawSubjectPublicKeyInfo), Grants: grants}
	for i, value := range []string{r.DaemonCSR, r.BrokerCSR} {
		csr, err := parseCSR(value)
		if err != nil {
			return issuance, err
		}
		now := time.Now()
		template := &x509.Certificate{Subject: pkix.Name{CommonName: shared.Token(r.NodeID)}, DNSNames: []string{shared.Token(r.NodeID)}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(domain.CertificateLifetime), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
		if i == 1 {
			template.ExtKeyUsage = append(template.ExtKeyUsage, x509.ExtKeyUsageServerAuth)
		}
		cert, err := certificatePEM(template, ca, csr.PublicKey, key)
		if err != nil {
			return issuance, err
		}
		if i == 0 {
			issuance.DaemonCertificate = cert
		} else {
			issuance.BrokerCertificate = cert
		}
	}
	old.Request, old.Issuance = r, issuance
	return issuance, l.save(path, old)
}

// Install validates all public trust against the locally held keys and commits
// one association manifest. Certificate/identity files are recoverable projections.
func (l *Local) Install(trust domain.ClusterTrust) error {
	if err := l.checkRetiredEpoch(trust); err != nil {
		return err
	}
	if l.Trust != nil && (l.Trust.Cluster.ID != trust.Cluster.ID || l.Trust.Issuance.ClusterFingerprint != trust.Issuance.ClusterFingerprint || l.Trust.CommandID != trust.CommandID) {
		return errors.New("refusing to replace pinned cluster trust")
	}
	ca, err := parseCertificate(trust.Issuance.ClusterCA)
	if err != nil {
		return err
	}
	if !ca.IsCA || trust.Cluster.ID == "" || trust.Cluster.ID != trust.Issuance.ClusterID || trust.Issuance.MembershipEpoch == 0 || shared.Fingerprint(ca.RawSubjectPublicKeyInfo) != trust.Issuance.ClusterFingerprint {
		return errors.New("invalid cluster trust")
	}
	if err := l.validateTopology(trust); err != nil {
		return err
	}
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	certs := []tls.Certificate{l.Daemon, l.Broker}
	for i, value := range []string{trust.Issuance.DaemonCertificate, trust.Issuance.BrokerCertificate} {
		cert, err := parseCertificate(value)
		if err != nil {
			return err
		}
		usages := []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
		if i == 1 {
			usages = append(usages, x509.ExtKeyUsageServerAuth)
		}
		for _, usage := range usages {
			if _, err = cert.Verify(x509.VerifyOptions{Roots: pool, DNSName: shared.Token(l.Identity.NodeID), KeyUsages: []x509.ExtKeyUsage{usage}}); err != nil {
				return err
			}
		}
		if !bytes.Equal(cert.RawSubjectPublicKeyInfo, certs[i].Leaf.RawSubjectPublicKeyInfo) {
			return errors.New("issued certificate key mismatch")
		}
		certs[i].Certificate, certs[i].Leaf = [][]byte{cert.Raw}, cert
	}
	if (l.Identity.ClusterID != nil && *l.Identity.ClusterID != trust.Cluster.ID) || (l.Identity.MembershipEpoch != nil && *l.Identity.MembershipEpoch != trust.Issuance.MembershipEpoch) || (l.Identity.ClusterFingerprint != nil && *l.Identity.ClusterFingerprint != trust.Issuance.ClusterFingerprint) {
		return errors.New("persisted identity trust mismatch")
	}
	if err = l.save("trust/membership.json", trust); err != nil {
		return err
	}
	l.Trust, l.Daemon, l.Broker = &trust, certs[0], certs[1]
	if l.Identity.ClusterID == nil {
		l.Identity.Version++
	}
	l.Identity.ClusterID, l.Identity.MembershipEpoch, l.Identity.ClusterFingerprint = &trust.Cluster.ID, &trust.Issuance.MembershipEpoch, &trust.Issuance.ClusterFingerprint
	root, err := os.OpenRoot(l.directory)
	if err != nil {
		return err
	}
	defer root.Close()
	if err = writeFile(root, "pki/daemon/cert.pem", []byte(trust.Issuance.DaemonCertificate)); err != nil {
		return err
	}
	if err = writeFile(root, "pki/broker/cert.pem", []byte(trust.Issuance.BrokerCertificate)); err != nil {
		return err
	}
	return l.save("identity.json", l.Identity)
}

func CertificateFingerprint(value string) (domain.Digest, error) {
	cert, err := parseCertificate(value)
	if err != nil {
		return "", err
	}
	return shared.Fingerprint(cert.Raw), nil
}
func (l *Local) BrokerCertificate() domain.Digest { return shared.Fingerprint(l.Broker.Leaf.Raw) }
func (l *Local) BrokerKey() domain.Digest {
	return shared.Fingerprint(l.Broker.Leaf.RawSubjectPublicKeyInfo)
}
func (l *Local) PeerTLS(peer domain.ID) *tls.Config {
	cfg := l.ServerTLS()
	cfg.RootCAs, cfg.ServerName = cfg.ClientCAs, shared.Token(peer)
	return cfg
}

// PinEnrollment records the first cluster response before polling. Retries cannot
// follow a different issuer/key/cluster behind the same HTTP address.
func (l *Local) PinEnrollment(id domain.ID, status domain.EnrollmentStatus) error {
	path := "enrollment/" + shared.Filename(id) + ".json"
	var old domain.EnrollmentStatus
	err := l.load(path, &old)
	if err == nil {
		if old.Cluster.ID != status.Cluster.ID || old.ClusterFingerprint != status.ClusterFingerprint || old.BrokerKey != status.BrokerKey || old.Cluster.IssuerNodeID != status.Cluster.IssuerNodeID {
			return &domain.Error{Code: "TRUST_MISMATCH", Message: "Enrollment cluster identity or approved broker key changed; a new review is required"}
		}
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return l.save(path, status)
}
