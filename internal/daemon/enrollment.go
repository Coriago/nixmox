package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"

	"nixmox/internal/client"
	"nixmox/internal/credentials"
	"nixmox/internal/discovery"
	"nixmox/internal/domain"
	"nixmox/internal/enrollment"
	"nixmox/internal/identity"
)

func (r *Runtime) issueEnrollment(ctx context.Context, request enrollment.Request) (string, error) {
	endpoint, err := url.Parse(request.JoiningNode.Endpoint)
	if err != nil {
		return "", err
	}
	joining := request.JoiningNode
	return r.issueCertificate(ctx, request.Cluster.CACertificate, []byte(joining.CSR), joining.NodeID, joining.PublicKeyThumbprint, []string{endpoint.Hostname(), "localhost", "127.0.0.1", "::1"})
}

func (r *Runtime) inviteEnrollment(ctx context.Context, request enrollment.Request) (enrollment.JoiningNode, error) {
	r.transition.Lock()
	defer r.transition.Unlock()
	return r.acceptEnrollment(ctx, request)
}

// acceptEnrollment runs under the transition lock for both invitation directions.
func (r *Runtime) acceptEnrollment(ctx context.Context, request enrollment.Request) (enrollment.JoiningNode, error) {
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
	joining, err := r.joiningIdentity(ctx)
	if err != nil {
		return enrollment.JoiningNode{}, err
	}
	request.JoiningNode = joining
	data, err := json.Marshal(request)
	if err != nil {
		return joining, err
	}
	return joining, identity.WritePrivate(path, data)
}

// joiningIdentity is called with the transition lock held.
func (r *Runtime) joiningIdentity(ctx context.Context) (enrollment.JoiningNode, error) {
	publicAge, err := credentials.DeriveIdentity(ctx, r.config.SSHHostKey, filepath.Join(r.config.StateDir, "age.key"))
	if err != nil {
		return enrollment.JoiningNode{}, err
	}
	csr, err := r.node.CSR()
	if err != nil {
		return enrollment.JoiningNode{}, err
	}
	fingerprint, err := identity.Thumbprint(r.node.Key.Public())
	if err != nil {
		return enrollment.JoiningNode{}, err
	}
	r.mu.RLock()
	state := r.node.State
	r.mu.RUnlock()
	return enrollment.JoiningNode{NodeID: state.NodeID, Name: state.Name, Endpoint: r.config.AdvertiseURL, CSR: string(csr), PublicKeyThumbprint: fingerprint, Environment: state.Environment, AgePublicKey: publicAge, Daemon: domain.DaemonInfo{Version: Version, StartedAt: r.started}}, nil
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
	return r.startCluster(saved)
}

func (r *Runtime) requestEnrollment(ctx context.Context, input enrollment.Input) (enrollment.Request, error) {
	r.transition.Lock()
	defer r.transition.Unlock()
	r.mu.RLock()
	joined := r.node.State.ClusterID != ""
	r.mu.RUnlock()
	if joined {
		return enrollment.Request{}, errors.New("node already joined")
	}
	if (input.Address == "") == (input.DiscoveryID == "") || input.ContactHostID != "" {
		return enrollment.Request{}, errors.New("one cluster discovery ID or address is required")
	}
	endpoints := []string{input.Address}
	clusterID, fingerprint := "", ""
	if input.DiscoveryID != "" {
		if r.discovery == nil {
			return enrollment.Request{}, errors.New("discovery unavailable")
		}
		record, err := r.discovery.Get(ctx, input.DiscoveryID)
		if err != nil {
			return enrollment.Request{}, err
		}
		ad := record.Advertisement
		if ad.ClusterID == nil || ad.Identity.Kind != "cluster-ca" || time.Since(record.LastSeenAt) >= discovery.Lifetime || len(ad.Endpoints) == 0 {
			return enrollment.Request{}, errors.New("recent cluster discovery required")
		}
		clusterID, fingerprint = *ad.ClusterID, ad.Identity.PublicKeyThumbprint
		endpoints = ad.Endpoints
	}
	if data, err := os.ReadFile(filepath.Join(r.config.StateDir, "enrollment.json")); err == nil {
		var saved enrollment.Request
		if err = json.Unmarshal(data, &saved); err != nil {
			return enrollment.Request{}, err
		}
		if (clusterID != "" && saved.Cluster.ID == clusterID && saved.Cluster.CAPublicKeyThumbprint == fingerprint) || (input.Address != "" && input.Address == saved.Cluster.ContactNode.Endpoint) {
			return saved, nil
		}
		return enrollment.Request{}, errors.New("another enrollment is already in progress")
	} else if !errors.Is(err, os.ErrNotExist) {
		return enrollment.Request{}, err
	}
	joining, err := r.joiningIdentity(ctx)
	if err != nil {
		return enrollment.Request{}, err
	}
	if err = enrollment.Endpoint(joining.Endpoint); err != nil {
		return enrollment.Request{}, errors.New("reachable advertised HTTP endpoint required for enrollment")
	}
	var failures []error
	for _, endpoint := range endpoints {
		if err = enrollment.Endpoint(endpoint); err != nil {
			return enrollment.Request{}, err
		}
		peer, err := client.New(endpoint, nil)
		if err != nil {
			return enrollment.Request{}, err
		}
		attempt, cancel := context.WithTimeout(ctx, 10*time.Second)
		var request enrollment.Request
		err = peer.Do(attempt, "POST", "/internal/enrollment/request", joining, &request)
		cancel()
		peer.Close()
		if err != nil {
			failures = append(failures, err)
			continue
		}
		if _, err = uuid.Parse(request.ID); err != nil {
			return enrollment.Request{}, errors.New("invalid enrollment request ID")
		}
		if request.Initiator != joining.NodeID || request.JoiningNode.NodeID != joining.NodeID || request.JoiningNode.PublicKeyThumbprint != joining.PublicKeyThumbprint {
			return enrollment.Request{}, errors.New("enrollment response changed joining identity")
		}
		if clusterID != "" && (request.Cluster.ID != clusterID || request.Cluster.CAPublicKeyThumbprint != fingerprint) {
			failures = append(failures, errors.New("discovered cluster identity changed"))
			continue
		}
		if _, err = r.acceptEnrollment(ctx, request); err != nil {
			return enrollment.Request{}, err
		}
		return request, nil
	}
	return enrollment.Request{}, errors.Join(failures...)
}

// Local status is restricted to the persisted invitation, including after the
// node joins as pending. The saved contact/CA cannot be replaced by a response.
func (r *Runtime) enrollmentStatus(ctx context.Context, id string) (enrollment.Request, error) {
	data, err := os.ReadFile(filepath.Join(r.config.StateDir, "enrollment.json"))
	if err != nil {
		return enrollment.Request{}, err
	}
	var saved enrollment.Request
	if err = json.Unmarshal(data, &saved); err != nil {
		return enrollment.Request{}, err
	}
	if id == "" || id != saved.ID {
		return enrollment.Request{}, errors.New("enrollment request identity mismatch")
	}
	peer, err := client.New(saved.Cluster.ContactNode.Endpoint, nil)
	if err != nil {
		return enrollment.Request{}, err
	}
	defer peer.Close()
	var current enrollment.Request
	if err = peer.Do(ctx, "GET", "/enrollment/"+id, nil, &current); err != nil {
		return enrollment.Request{}, err
	}
	if current.ID != saved.ID || current.Cluster.ID != saved.Cluster.ID || current.Cluster.CACertificate != saved.Cluster.CACertificate || current.Cluster.ContactNode != saved.Cluster.ContactNode || current.JoiningNode.NodeID != saved.JoiningNode.NodeID || current.JoiningNode.PublicKeyThumbprint != saved.JoiningNode.PublicKeyThumbprint {
		return enrollment.Request{}, errors.New("enrollment status changed saved trust")
	}
	return current, nil
}
