package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	"nixmox/internal/client"
	"nixmox/internal/discovery"
	"nixmox/internal/enrollment"
)

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
