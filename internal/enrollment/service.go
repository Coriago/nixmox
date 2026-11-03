package enrollment

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"time"

	"filippo.io/age"
	"github.com/google/uuid"
	"github.com/nats-io/nats.go/jetstream"
	"nixmox/internal/client"
	"nixmox/internal/cluster"
	"nixmox/internal/discovery"
	"nixmox/internal/domain"
	"nixmox/internal/identity"
)

type Service struct {
	NodeID    string
	Requests  cluster.Store[Request]
	Members   cluster.Store[domain.Member]
	Clusters  cluster.Store[domain.Cluster]
	Discovery cluster.Store[discovery.Record]
	Issue     func(context.Context, Request) (string, error)
	Authorize func(context.Context) error
}

func Endpoint(address string) error {
	u, err := url.Parse(address)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return errors.New("valid HTTP enrollment endpoint required")
	}
	return nil
}

func full(member domain.Member) bool {
	return member.Status == "full" || member.Status == "full-stable"
}

func (s *Service) Submit(ctx context.Context, input Input) (Request, error) {
	if (input.Address == "") == (input.DiscoveryID == "") || input.ContactHostID == "" {
		return Request{}, errors.New("one discovery ID or address and a contact host are required")
	}
	initiator, err := s.Members.Get(ctx, s.NodeID)
	if err != nil {
		return Request{}, err
	}
	contact, err := s.Members.Get(ctx, input.ContactHostID)
	if err != nil {
		return Request{}, err
	}
	if !full(initiator.Value) || !full(contact.Value) {
		return Request{}, errors.New("enrollment requires full initiator and contact members")
	}
	identityRecord, err := s.Clusters.Get(ctx, "identity")
	if err != nil {
		return Request{}, err
	}
	ca, err := identity.ParseCertificate([]byte(identityRecord.Value.CA.Certificate))
	if err != nil {
		return Request{}, err
	}
	thumbprint, err := identity.Thumbprint(ca.PublicKey)
	if err != nil {
		return Request{}, err
	}
	now := time.Now().UTC()
	request := Request{ID: uuid.NewString(), Status: "pending", CreatedAt: now, Initiator: s.NodeID,
		Decision:    &Decision{Result: "approved", ByNodeID: s.NodeID, DecidedAt: now},
		JoiningNode: JoiningNode{Endpoint: input.Address, DiscoveryID: input.DiscoveryID},
		Cluster:     Cluster{ID: identityRecord.Value.ID, CACertificate: identityRecord.Value.CA.Certificate, CAPublicKeyThumbprint: thumbprint, ContactNode: Contact{NodeID: input.ContactHostID}},
	}
	for _, endpoint := range contact.Value.Endpoints {
		if endpoint.HTTP != "" {
			request.Cluster.ContactNode.Endpoint = endpoint.HTTP
			break
		}
	}
	if err = Endpoint(request.Cluster.ContactNode.Endpoint); err != nil {
		return Request{}, err
	}
	cursor := ""
	for {
		members, next, err := s.Members.List(ctx, cursor, 100)
		if err != nil {
			return Request{}, err
		}
		for _, member := range members {
			if member.Value.Status != "full-stable" {
				continue
			}
			for _, endpoint := range member.Value.Endpoints {
				if endpoint.NATSClient != "" {
					request.Cluster.NATSEndpoints = append(request.Cluster.NATSEndpoints, endpoint.NATSClient)
				}
			}
		}
		if next == "" {
			break
		}
		cursor = next
	}
	if len(request.Cluster.NATSEndpoints) == 0 {
		return Request{}, errors.New("no full-stable NATS endpoints")
	}
	if input.DiscoveryID != "" {
		if s.Discovery.KV == nil {
			return Request{}, errors.New("discovery unavailable")
		}
		observed, err := s.Discovery.Get(ctx, input.DiscoveryID)
		if err != nil {
			return Request{}, err
		}
		candidate := observed.Value
		if candidate.ObservedByHostID != input.ContactHostID || candidate.Advertisement.ClusterID != nil || len(candidate.Advertisement.Endpoints) == 0 || time.Since(candidate.LastSeenAt) >= discovery.Lifetime {
			return Request{}, errors.New("contact must have observed the unjoined node recently")
		}
		request.JoiningNode.CandidateEndpoints = candidate.Advertisement.Endpoints
		request.JoiningNode.Endpoint = candidate.Advertisement.Endpoints[0]
		request.JoiningNode.NodeID = candidate.Advertisement.NodeID
		request.JoiningNode.PublicKeyThumbprint = candidate.Advertisement.Identity.PublicKeyThumbprint
	}
	if err = Endpoint(request.JoiningNode.Endpoint); err != nil {
		return Request{}, err
	}
	if _, err = s.Requests.Create(ctx, request.ID, request); err != nil {
		return Request{}, err
	}
	// Waiting observes durable work; cancelling HTTP does not cancel the contact.
	done := errors.New("enrollment finished")
	err = s.Requests.Watch(ctx, request.ID, func(record *cluster.Record[Request], deleted bool) error {
		if record == nil || deleted {
			return nil
		}
		request = record.Value
		if request.Status == "completed" || request.Status == "failed" {
			return done
		}
		return nil
	})
	if err != nil && !errors.Is(err, done) {
		return request, err
	}
	if request.Status == "failed" {
		return request, errors.New(request.Error)
	}
	return request, nil
}

func (s *Service) Run(ctx context.Context) error {
	return s.Requests.Watch(ctx, ">", func(record *cluster.Record[Request], deleted bool) error {
		if record == nil || deleted {
			return nil
		}
		current, err := s.Requests.Get(ctx, record.Key)
		if err != nil {
			return err
		}
		request := &current.Value
		if request.Cluster.ContactNode.NodeID != s.NodeID || request.Status != "pending" || request.Decision == nil || request.Decision.Result != "approved" {
			return nil
		}
		operation, cancel := context.WithTimeout(ctx, 45*time.Second)
		defer cancel()
		err = s.complete(operation, &current)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		request.Status = "completed"
		if err != nil {
			request.Status, request.Error = "failed", err.Error()
		}
		_, err = s.Requests.Update(ctx, request.ID, *request, current.Revision)
		return err
	})
}

// Try only endpoints captured from the selected discovery record. The recipient
// checks the selected node/key before persisting an invitation; the contact
// independently verifies its CSR before issuing a certificate.
func invite(ctx context.Context, request *Request) (*client.Client, JoiningNode, error) {
	endpoints := request.JoiningNode.CandidateEndpoints
	if len(endpoints) == 0 {
		endpoints = []string{request.JoiningNode.Endpoint}
	}
	var failures []error
	for _, endpoint := range endpoints {
		if err := Endpoint(endpoint); err != nil {
			return nil, JoiningNode{}, err
		}
		peer, err := client.New(endpoint, nil)
		if err != nil {
			return nil, JoiningNode{}, err
		}
		attempt, cancel := context.WithTimeout(ctx, 5*time.Second)
		var joining JoiningNode
		err = peer.Do(attempt, "POST", "/internal/enrollment/add", request, &joining)
		cancel()
		if err == nil {
			request.JoiningNode.Endpoint = endpoint
			return peer, joining, nil
		}
		peer.Close()
		failures = append(failures, err)
	}
	return nil, JoiningNode{}, errors.Join(failures...)
}

func (s *Service) complete(ctx context.Context, record *cluster.Record[Request]) error {
	request := &record.Value
	contact, err := s.Members.Get(ctx, s.NodeID)
	if err != nil {
		return err
	}
	if !full(contact.Value) {
		return errors.New("contact no longer a full member")
	}
	if request.JoiningNode.IssuedCertificate == "" {
		peer, joining, err := invite(ctx, request)
		if err != nil {
			return err
		}
		peer.Close()
		if request.JoiningNode.NodeID != "" && joining.NodeID != request.JoiningNode.NodeID {
			return errors.New("discovered node identity changed")
		}
		if request.JoiningNode.PublicKeyThumbprint != "" && joining.PublicKeyThumbprint != request.JoiningNode.PublicKeyThumbprint {
			return errors.New("discovered node key changed")
		}
		if _, err = uuid.Parse(joining.NodeID); err != nil {
			return errors.New("invalid joining node ID")
		}
		if joining.Name == "" || len(joining.Name) > 128 || (joining.Environment != "installer" && joining.Environment != "installed") {
			return errors.New("invalid joining node metadata")
		}
		if _, err = age.ParseX25519Recipient(joining.AgePublicKey); err != nil {
			return errors.New("invalid joining node age recipient")
		}
		joining.Endpoint, joining.DiscoveryID = request.JoiningNode.Endpoint, request.JoiningNode.DiscoveryID
		request.JoiningNode = joining
		certificate, err := s.Issue(ctx, *request)
		if err != nil {
			return err
		}
		request.JoiningNode.IssuedCertificate = certificate
		// Persist the proof before authorizing membership or notifying the peer.
		record.Revision, err = s.Requests.Update(ctx, request.ID, *request, record.Revision)
		if err != nil {
			return err
		}
	}
	joining := request.JoiningNode
	member := domain.Member{EnrollmentRequestID: request.ID, NodeID: joining.NodeID, Name: joining.Name, ClusterID: request.Cluster.ID, Status: "pending", Environment: joining.Environment, AgePublicKey: joining.AgePublicKey, Daemon: joining.Daemon, Endpoints: []domain.Endpoint{{Network: "lan", HTTP: joining.Endpoint}}}
	_, err = s.Members.Create(ctx, member.NodeID, member)
	if errors.Is(err, jetstream.ErrKeyExists) {
		existing, readErr := s.Members.Get(ctx, member.NodeID)
		if readErr != nil {
			return readErr
		}
		if existing.Value.EnrollmentRequestID != request.ID || existing.Value.ClusterID != member.ClusterID || existing.Value.Status != "pending" || existing.Value.AgePublicKey != member.AgePublicKey {
			return errors.New("existing membership prevents enrollment")
		}
	} else if err != nil {
		return err
	}
	if err = s.Authorize(ctx); err != nil {
		return fmt.Errorf("authorize pending member: %w", err)
	}
	peer, err := client.New(joining.Endpoint, nil)
	if err != nil {
		return err
	}
	defer peer.Close()
	return peer.Do(ctx, "POST", "/internal/enrollment/confirm", Confirmation{RequestID: request.ID, Certificate: joining.IssuedCertificate}, nil)
}
