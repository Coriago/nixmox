package daemon

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/nats-io/nats.go"

	"nixmox/internal/cluster"
	"nixmox/internal/credentials"
	"nixmox/internal/domain"
	"nixmox/internal/identity"
)

// refreshAuthorization is also called by the enrollment contact before delivering
// a certificate. The shared MEMBERS record, not an HTTP caller, grants access.
func (r *Runtime) refreshAuthorization(ctx context.Context, store cluster.Store[domain.Member]) error {
	r.authorization.Lock()
	defer r.authorization.Unlock()
	records, err := store.All(ctx)
	if err != nil {
		return err
	}
	members := make([]domain.Member, 0, len(records))
	for _, record := range records {
		members = append(members, record.Value)
	}
	key := authorizationKey(members)
	if key == r.authorizedMembers {
		return nil
	}
	r.mu.RLock()
	servers, options := r.servers, r.serverOptions
	r.mu.RUnlock()
	for i, s := range servers {
		updated := options[i].Clone()
		if err := cluster.ConfigureAccounts(updated, members); err != nil {
			return err
		}
		if err := s.ReloadOptions(updated); err != nil {
			return err
		}
	}
	r.authorizedMembers = key
	return nil
}

func authorizationKey(members []domain.Member) string {
	type grant struct{ NodeID, Status string }
	grants := make([]grant, 0, len(members))
	for _, member := range members {
		grants = append(grants, grant{member.NodeID, member.Status})
	}
	sort.Slice(grants, func(i, j int) bool { return grants[i].NodeID < grants[j].NodeID })
	data, _ := json.Marshal(grants)
	return string(data)
}

// nodeTLS is the single validation path for persisted node credentials.
func (r *Runtime) nodeTLS(saved startup) (*tls.Config, error) {
	if err := r.node.VerifyEnrollment([]byte(saved.Certificate), []byte(saved.Cluster.CA.Certificate), time.Now()); err != nil {
		return nil, err
	}
	key, err := os.ReadFile(filepath.Join(r.config.StateDir, "node.key"))
	if err != nil {
		return nil, err
	}
	defer clear(key)
	return cluster.TLSConfig([]byte(saved.Certificate), key, []byte(saved.Cluster.CA.Certificate))
}

func (r *Runtime) issueCertificate(ctx context.Context, caPEM string, csr []byte, subject, thumbprint string, names []string) (string, error) {
	svc, err := r.Services()
	if err != nil {
		return "", err
	}
	record, err := svc.Credentials.Get(ctx, "cluster-ca")
	if err != nil {
		return "", err
	}
	secret, err := (credentials.Cipher{KeyFile: filepath.Join(r.config.StateDir, "age.key")}).Decrypt(ctx, record.Value.Content)
	if err != nil {
		return "", err
	}
	defer clear(secret)
	block, rest := pem.Decode(secret)
	if block == nil || block.Type != "PRIVATE KEY" || len(rest) != 0 {
		return "", errors.New("invalid CA private key")
	}
	defer clear(block.Bytes)
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return "", err
	}
	private, ok := key.(ed25519.PrivateKey)
	if !ok {
		return "", errors.New("CA key must be Ed25519")
	}
	defer clear(private)
	ca, err := identity.ParseCertificate([]byte(caPEM))
	if err != nil {
		return "", err
	}
	if !private.Public().(ed25519.PublicKey).Equal(ca.PublicKey) {
		return "", errors.New("CA signing key mismatch")
	}
	authority := identity.Authority{Certificate: ca, PEM: []byte(caPEM), Key: private}
	cert, err := authority.Issue(csr, subject, thumbprint, names, time.Now())
	return string(cert), err
}

// systemConnection uses an ephemeral management key, never the ordinary node
// certificate or a shared password. The caller owns and closes the connection.
// Authorization reloads remove this SYS principal when full membership ends.
func (r *Runtime) systemConnection(ctx context.Context) (*nats.Conn, error) {
	svc, err := r.Services()
	if err != nil {
		return nil, err
	}
	member, err := svc.Members.Get(ctx, svc.Tasks.HostID)
	if err != nil {
		return nil, err
	}
	if svc.Pending || (member.Value.Status != "full" && member.Value.Status != "full-stable") {
		return nil, errors.New("system management requires full membership")
	}
	record, err := svc.Cluster.Get(ctx, "identity")
	if err != nil {
		return nil, err
	}
	public, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	defer clear(key)
	subject := cluster.SystemIdentity(member.Key)
	csr, err := identity.CreateCSR(key, subject)
	if err != nil {
		return nil, err
	}
	thumbprint, err := identity.Thumbprint(public)
	if err != nil {
		return nil, err
	}
	ca := record.Value.CA.Certificate
	certificate, err := r.issueCertificate(ctx, ca, csr, subject, thumbprint, nil)
	if err != nil {
		return nil, err
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, err
	}
	defer clear(der)
	encoded := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	defer clear(encoded)
	tlsConfig, err := cluster.TLSConfig([]byte(certificate), encoded, []byte(ca))
	if err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.RLock()
	endpoint := r.client.ConnectedUrl()
	r.mu.RUnlock()
	return cluster.Connect([]string{endpoint}, tlsConfig, subject)
}
