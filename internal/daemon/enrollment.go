package daemon

import (
	"context"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"nixmox/internal/credentials"
	"nixmox/internal/domain"
	"nixmox/internal/enrollment"
	"nixmox/internal/identity"
)

func (r *Runtime) issueEnrollment(ctx context.Context, request enrollment.Request) (string, error) {
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
	ca, err := identity.ParseCertificate([]byte(request.Cluster.CACertificate))
	if err != nil {
		return "", err
	}
	if !private.Public().(ed25519.PublicKey).Equal(ca.PublicKey) {
		return "", errors.New("CA signing key mismatch")
	}
	authority := identity.Authority{Certificate: ca, PEM: []byte(request.Cluster.CACertificate), Key: private}
	endpoint, err := url.Parse(request.JoiningNode.Endpoint)
	if err != nil {
		return "", err
	}
	cert, err := authority.Issue([]byte(request.JoiningNode.CSR), request.JoiningNode.NodeID, request.JoiningNode.PublicKeyThumbprint, []string{endpoint.Hostname(), "localhost", "127.0.0.1", "::1"}, time.Now())
	return string(cert), err
}

func (r *Runtime) inviteEnrollment(ctx context.Context, request enrollment.Request) (enrollment.JoiningNode, error) {
	r.transition.Lock()
	defer r.transition.Unlock()
	r.mu.RLock()
	state := r.node.State
	r.mu.RUnlock()
	if state.ClusterID != "" {
		return enrollment.JoiningNode{}, errors.New("node already joined")
	}
	if request.ID == "" || request.Cluster.ID == "" || len(request.Cluster.NATSEndpoints) == 0 {
		return enrollment.JoiningNode{}, errors.New("incomplete enrollment invitation")
	}
	if err := enrollment.Endpoint(request.Cluster.ContactNode.Endpoint); err != nil {
		return enrollment.JoiningNode{}, err
	}
	ca, err := identity.ParseCertificate([]byte(request.Cluster.CACertificate))
	if err != nil {
		return enrollment.JoiningNode{}, err
	}
	if !ca.IsCA || ca.Subject.CommonName != request.Cluster.ID {
		return enrollment.JoiningNode{}, errors.New("cluster CA identity mismatch")
	}
	thumbprint, err := identity.Thumbprint(ca.PublicKey)
	if err != nil || thumbprint != request.Cluster.CAPublicKeyThumbprint {
		return enrollment.JoiningNode{}, errors.New("cluster CA thumbprint mismatch")
	}
	nodeFingerprint, err := identity.Thumbprint(r.node.Key.Public())
	if err != nil {
		return enrollment.JoiningNode{}, err
	}
	if request.JoiningNode.NodeID != "" && request.JoiningNode.NodeID != state.NodeID {
		return enrollment.JoiningNode{}, errors.New("invitation targets a different node")
	}
	if request.JoiningNode.PublicKeyThumbprint != "" && request.JoiningNode.PublicKeyThumbprint != nodeFingerprint {
		return enrollment.JoiningNode{}, errors.New("invitation targets a different node key")
	}
	path := filepath.Join(r.config.StateDir, "enrollment.json")
	if data, readErr := os.ReadFile(path); readErr == nil {
		var saved enrollment.Request
		if err = json.Unmarshal(data, &saved); err != nil {
			return enrollment.JoiningNode{}, err
		}
		if saved.ID != request.ID || saved.Cluster.CACertificate != request.Cluster.CACertificate || saved.Cluster.ContactNode != request.Cluster.ContactNode {
			return enrollment.JoiningNode{}, errors.New("another enrollment is already in progress")
		}
		request = saved // Retries cannot replace the originally accepted trust/endpoints.
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return enrollment.JoiningNode{}, readErr
	}
	publicAge, err := credentials.DeriveIdentity(ctx, r.config.SSHHostKey, filepath.Join(r.config.StateDir, "age.key"))
	if err != nil {
		return enrollment.JoiningNode{}, err
	}
	csr, err := r.node.CSR()
	if err != nil {
		return enrollment.JoiningNode{}, err
	}
	joining := enrollment.JoiningNode{NodeID: state.NodeID, Name: state.Name, CSR: string(csr), PublicKeyThumbprint: nodeFingerprint, Environment: state.Environment, AgePublicKey: publicAge, Daemon: domain.DaemonInfo{Version: Version, StartedAt: r.started}}
	request.JoiningNode = joining
	data, err := json.Marshal(request)
	if err != nil {
		return joining, err
	}
	return joining, identity.WritePrivate(path, data)
}

func (r *Runtime) confirmEnrollment(ctx context.Context, confirmation enrollment.Confirmation) error {
	r.transition.Lock()
	defer r.transition.Unlock()
	data, err := os.ReadFile(filepath.Join(r.config.StateDir, "enrollment.json"))
	if err != nil {
		return err
	}
	var request enrollment.Request
	if err = json.Unmarshal(data, &request); err != nil {
		return err
	}
	if confirmation.RequestID == "" || confirmation.RequestID != request.ID {
		return errors.New("enrollment request identity mismatch")
	}
	if err = r.node.VerifyEnrollment([]byte(confirmation.Certificate), []byte(request.Cluster.CACertificate), time.Now()); err != nil {
		return err
	}
	r.mu.RLock()
	joined := r.node.State.ClusterID
	r.mu.RUnlock()
	if joined != "" {
		if joined != request.Cluster.ID {
			return errors.New("already joined another cluster")
		}
		_, err = r.Services()
		return err
	}
	saved := startup{Cluster: domain.Cluster{ID: request.Cluster.ID, CA: domain.ClusterCA{Certificate: request.Cluster.CACertificate, CredentialID: "cluster-ca"}},
		Member:      domain.Member{EnrollmentRequestID: request.ID, NodeID: r.node.State.NodeID, ClusterID: request.Cluster.ID, Status: "pending", Environment: r.config.Environment},
		Certificate: confirmation.Certificate, NATSEndpoints: request.Cluster.NATSEndpoints}
	if err = r.saveStartup(saved); err != nil {
		return err
	}
	return r.start(saved)
}
