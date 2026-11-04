package enrollment

import (
	"context"
	"errors"
	"net"
	"net/url"
	"strings"
	"time"

	"filippo.io/age"
	"github.com/google/uuid"
	"github.com/nats-io/nats.go/jetstream"
	"nixmox/internal/identity"
)

// Receive records an unjoined node's request without granting membership or
// issuing a certificate. Only the contact's existing worker performs enrollment.
func (s *Service) Receive(ctx context.Context, joining JoiningNode) (Request, error) {
	if err := Endpoint(joining.Endpoint); err != nil {
		return Request{}, err
	}
	if err := validateJoining(joining); err != nil {
		return Request{}, err
	}
	request, err := s.prepareRequest(ctx, Input{Address: joining.Endpoint, ContactHostID: s.NodeID})
	if err != nil {
		return Request{}, err
	}
	previous, err := s.Requests.All(ctx)
	if err != nil {
		return Request{}, err
	}
	var existing *Request
	for _, record := range previous {
		saved := record.Value
		sameNode := saved.JoiningNode.NodeID == joining.NodeID
		sameKey := saved.JoiningNode.PublicKeyThumbprint == joining.PublicKeyThumbprint
		sameEndpoint := endpointIdentity(saved.JoiningNode.Endpoint) == endpointIdentity(joining.Endpoint)
		if saved.Decision != nil && saved.Decision.Result == "rejected" && (sameNode || sameKey || sameEndpoint) {
			return Request{}, errors.New("matching enrollment rejection must be cleared first")
		}
		if sameNode && sameKey && sameEndpoint && saved.Status != "failed" {
			existing = &saved
		}
	}
	if existing != nil {
		return *existing, nil
	}
	if _, err = s.Members.Get(ctx, joining.NodeID); err == nil {
		return Request{}, errors.New("existing membership prevents enrollment")
	} else if !errors.Is(err, jetstream.ErrKeyNotFound) && !errors.Is(err, jetstream.ErrKeyDeleted) {
		return Request{}, err
	}
	joining.CandidateEndpoints = nil
	joining.IssuedCertificate = ""
	joining.DiscoveryID = ""
	request.JoiningNode = joining
	request.Initiator = joining.NodeID
	_, err = s.Requests.Create(ctx, request.ID, request)
	return request, err
}

// Decide is cross-host: it records a full member's decision using CAS, while
// retaining the original contact as the only issuer and enrollment worker.
func (s *Service) Decide(ctx context.Context, id, result string) (Request, error) {
	if result != "approved" && result != "rejected" {
		return Request{}, errors.New("invalid enrollment decision")
	}
	member, err := s.Members.Get(ctx, s.NodeID)
	if err != nil {
		return Request{}, err
	}
	if !full(member.Value) {
		return Request{}, errors.New("enrollment decisions require a full member")
	}
	record, err := s.Requests.Get(ctx, id)
	if err != nil {
		return Request{}, err
	}
	if record.Value.Decision != nil {
		if record.Value.Decision.Result == result {
			return record.Value, nil
		}
		return Request{}, errors.New("enrollment decision already recorded")
	}
	if record.Value.Status != "pending" {
		return Request{}, errors.New("enrollment is not pending")
	}
	record.Value.Decision = &Decision{Result: result, ByNodeID: s.NodeID, DecidedAt: time.Now().UTC()}
	if result == "rejected" {
		record.Value.Status = "rejected"
	}
	_, err = s.Requests.Update(ctx, id, record.Value, record.Revision)
	return record.Value, err
}

func validateJoining(joining JoiningNode) error {
	if _, err := uuid.Parse(joining.NodeID); err != nil {
		return errors.New("invalid joining node ID")
	}
	if joining.Name == "" || len(joining.Name) > 128 || (joining.Environment != "installer" && joining.Environment != "installed") {
		return errors.New("invalid joining node metadata")
	}
	if _, err := age.ParseX25519Recipient(joining.AgePublicKey); err != nil {
		return errors.New("invalid joining node age recipient")
	}
	_, err := identity.ParseCSR([]byte(joining.CSR), joining.NodeID, joining.PublicKeyThumbprint)
	return err
}

func endpointIdentity(endpoint string) string {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return endpoint
	}
	port := parsed.Port()
	if port == "" {
		if parsed.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	return parsed.Scheme + "://" + net.JoinHostPort(strings.ToLower(parsed.Hostname()), port)
}
