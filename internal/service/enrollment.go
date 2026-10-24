package service

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	js "github.com/nats-io/nats.go/jetstream"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"nixmox/internal/client"
	"nixmox/internal/domain"
	"nixmox/internal/engine"
	"nixmox/internal/engine/comms"
	"nixmox/internal/engine/identity"
	"nixmox/internal/engine/telemetry"
	"nixmox/internal/logging"
)

var enrollmentID = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,128}$`)

func invalid(message string) error { return &domain.Error{Code: "INVALID_INPUT", Message: message} }
func submission(r domain.JoinRequest) domain.EnrollmentSubmission {
	return domain.EnrollmentSubmission{Signature: r.Signature, RequestID: r.RequestID, NodeID: r.NodeID, ExpectedClusterName: r.ExpectedClusterName, DaemonCSR: r.DaemonCSR, BrokerCSR: r.BrokerCSR}
}
func (c *ClusterNode) status(r domain.JoinRequest) domain.EnrollmentStatus {
	if r.State == "awaiting_approval" && time.Now().After(r.ExpiresAt) {
		r.State = "expired"
	}
	return domain.EnrollmentStatus{Topology: c.state.Topology, Request: r, Cluster: c.state.Cluster, ClusterFingerprint: c.keys.Trust.Issuance.ClusterFingerprint, BrokerKey: c.keys.BrokerKey(), LeafAddress: c.leafAddress()}
}

func (c *ClusterNode) Enroll(ctx context.Context, r domain.EnrollmentSubmission) (domain.EnrollmentStatus, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.quorum(ctx); err != nil {
		return domain.EnrollmentStatus{}, err
	}
	if !enrollmentID.MatchString(string(r.RequestID)) || !enrollmentID.MatchString(string(r.NodeID)) || r.ExpectedClusterName != c.state.Cluster.Name {
		return domain.EnrollmentStatus{}, invalid("Request identity or expected cluster name is invalid")
	}
	if old, ok := c.state.Requests[r.RequestID]; ok {
		if submission(old) != r {
			return domain.EnrollmentStatus{}, &domain.Error{Code: "IDEMPOTENCY_CONFLICT", Message: "Enrollment request ID is bound to different keys or identity"}
		}
		return c.status(old), nil
	}
	if len(c.state.Requests) >= 64 || !c.limiter.Allow() {
		return domain.EnrollmentStatus{}, &domain.Error{Code: "REQUEST_LIMIT", Message: "Enrollment capacity or rate limit reached"}
	}
	if member, ok := c.state.Members[r.NodeID]; ok && !member.Removed {
		return domain.EnrollmentStatus{}, invalid("Node ID is already enrolled")
	}
	for _, old := range c.state.Requests {
		if old.NodeID == r.NodeID && old.State == "awaiting_approval" && time.Now().Before(old.ExpiresAt) {
			return domain.EnrollmentStatus{}, invalid("Node already has an active join request")
		}
	}
	key, _, err := identity.RequestKeys(r)
	if err != nil {
		return domain.EnrollmentStatus{}, invalid("Signed Ed25519 daemon and broker CSRs are required")
	}
	request := domain.JoinRequest{Trace: telemetry.Inject(ctx), Signature: r.Signature, Record: domain.Record{SchemaVersion: 1, ID: r.RequestID, Version: 1}, RequestID: r.RequestID, NodeID: r.NodeID, ExpectedClusterName: r.ExpectedClusterName, DaemonCSR: r.DaemonCSR, BrokerCSR: r.BrokerCSR, KeyFingerprint: key, State: "awaiting_approval", ExpiresAt: time.Now().UTC().Add(domain.EnrollmentTTL)}
	state := *c.state
	state.Requests = maps.Clone(state.Requests)
	state.Requests[r.RequestID] = request
	if err = c.saveState(ctx, state); err != nil {
		return domain.EnrollmentStatus{}, err
	}
	return c.status(request), nil
}

func (c *ClusterNode) Enrollment(ctx context.Context, id domain.ID) (domain.EnrollmentStatus, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.quorum(ctx); err != nil {
		return domain.EnrollmentStatus{}, err
	}
	r, ok := c.state.Requests[id]
	if !ok {
		return domain.EnrollmentStatus{}, notFound()
	}
	return c.status(r), nil
}
func (c *ClusterNode) Requests(ctx context.Context, page domain.PageRequest) (domain.Page[domain.JoinRequest], error) {
	state, err := enumFilter(page.Filters["state"], "awaiting_approval", "approved", "rejected", "expired")
	if err != nil {
		return domain.Page[domain.JoinRequest]{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.quorum(ctx); err != nil {
		return domain.Page[domain.JoinRequest]{}, err
	}
	items := make([]domain.JoinRequest, 0, len(c.state.Requests))
	for _, r := range c.state.Requests {
		r = c.status(r).Request
		if state == "" || r.State == state {
			items = append(items, r)
		}
	}
	slices.SortFunc(items, func(a, b domain.JoinRequest) int { return cmp.Compare(a.ID, b.ID) })
	return pageItems(items, page, "cluster/requests", func(r domain.JoinRequest) []string { return []string{string(r.ID)} })
}
func (c *ClusterNode) Certificates(ctx context.Context, id domain.ID) (domain.Issuance, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.quorum(ctx); err != nil {
		return domain.Issuance{}, err
	}
	r, ok := c.state.Requests[id]
	if !ok {
		return domain.Issuance{}, notFound()
	}
	if r.State != "approved" || r.Decision == nil || !r.Decision.Approved {
		return domain.Issuance{}, invalid("Enrollment has not been approved")
	}
	member := c.state.Members[r.NodeID]
	issued, err := c.keys.Issued(submission(r))
	if err == nil && (issued.MembershipEpoch != member.Epoch || issued.ClusterID != c.state.Cluster.ID || r.Decision.IssuanceID == nil || issued.ID != *r.Decision.IssuanceID) {
		err = errors.New("committed issuance identity mismatch")
	}
	return issued, err
}

func (c *ClusterNode) Validate(ctx context.Context, command domain.Command, value any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch p := value.(type) {
	case domain.CredentialImport, domain.CredentialCheckPayload, domain.CredentialTarget:
		if !credentialOperation(command.Type) {
			return domain.Unavailable(command.Type)
		}
		_, err := c.credentialInput(ctx, command, value)
		return err
	case domain.RepositoryImport, domain.RepositoryTarget, domain.RepositoryRemoteSet, domain.RepositoryCredentialSet:
		if !repositoryOperation(command.Type) {
			return domain.Unavailable(command.Type)
		}
		_, err := c.repositoryInput(ctx, command, value)
		return err
	case domain.HostAssign:
		return c.validateAssignment(ctx, command, p)
	case domain.ClusterCreate:
		if strings.TrimSpace(p.Name) != p.Name || p.Name == "" || len(p.Name) > 128 {
			return invalid("Cluster name must contain 1–128 characters without surrounding whitespace")
		}
		if c.keys.Trust != nil {
			return invalid("Leave the current cluster before creating another")
		}
	case domain.ClusterJoin:
		if c.keys.Trust != nil {
			return invalid("Leave the current cluster before joining another")
		}
		if p.ExpectedClusterName == "" {
			return invalid("Expected cluster name is required")
		}
		if _, err := enrollmentClient(p.Address); err != nil {
			return err
		}
	case domain.ClusterLeave:
		if err := c.quorum(ctx); err != nil {
			return err
		}
		return c.validateDeparture(command, p.HostID, p.MembershipEpoch, false, "")
	case domain.HostRemove:
		if err := c.quorum(ctx); err != nil {
			return err
		}
		return c.validateDeparture(command, p.HostID, p.MembershipEpoch, p.Force, p.ConfirmHost)
	case domain.ForceFullMembership:
		return c.validatePromotion(ctx, command, p)
	case domain.HostPower:
		_, err := c.validatePower(ctx, command, p)
		return err
	case domain.HostTarget:
		if command.Type != "host.refresh" {
			return domain.Unavailable(command.Type)
		}
		if err := c.quorum(ctx); err != nil {
			return err
		}
		if p.HostID != command.TargetID || c.state.Members[p.HostID].NodeID == "" || c.state.Members[p.HostID].Removed {
			return invalid("Target is not an enrolled host")
		}
		if len(command.ExpectedVersions) != 0 {
			return invalid("host.refresh has no mutable version prerequisites")
		}
	case domain.JoinDecisionPayload:
		if err := c.quorum(ctx); err != nil {
			return err
		}
		r, ok := c.state.Requests[p.RequestID]
		if !ok {
			return notFound()
		}
		if len(command.ExpectedVersions) != 1 || command.ExpectedVersions["join_request:"+string(p.RequestID)] != r.Version {
			return &domain.Error{Code: "VERSION_CONFLICT", Message: "Review the current join request version and keys"}
		}
		if c.status(r).Request.State != "awaiting_approval" {
			return invalid("Join request is no longer awaiting approval")
		}
	default:
		return domain.Unavailable(command.Type)
	}
	if command.Type == "cluster.create" || command.Type == "cluster.join" {
		if len(command.ExpectedVersions) != 0 {
			return invalid("This command has no version prerequisites")
		}
	}
	return nil
}

func enrollmentClient(address string) (*client.Client, error) {
	if !strings.Contains(address, "://") {
		address = "http://" + address
	}
	u, err := url.Parse(address)
	if err != nil || u.Scheme != "http" {
		return nil, invalid("Enrollment address must use HTTP on the management network")
	}
	return client.New(address, nil)
}

func (c *ClusterNode) create(ctx context.Context, command domain.Command) (any, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var p domain.ClusterCreate
	if err := json.Unmarshal(command.Payload, &p); err != nil {
		return nil, err
	}
	cluster, err := c.keys.Create(command.ID, p.Name)
	if err != nil {
		return nil, err
	}
	if c.keys.Trust == nil {
		cluster.GatewayEndpoints = c.host.Endpoints
		request, err := c.keys.Request("genesis_"+command.ID, p.Name)
		if err != nil {
			return nil, err
		}
		issued, err := c.keys.Issue(request, 1, comms.FullGrants(cluster.ID, c.host.ID))
		if err != nil {
			return nil, err
		}
		topology, err := c.initialTopology(cluster, request, issued)
		if err != nil {
			return nil, err
		}
		if err = c.keys.Install(domain.ClusterTrust{Topology: &topology, CommandID: command.ID, Cluster: cluster, Issuance: issued, BrokerKey: c.keys.BrokerKey(), LeafAddress: c.leafAddress()}); err != nil {
			return nil, err
		}
	}
	if err = c.configure(ctx); err != nil {
		return nil, err
	}
	return c.keys.Trust.Cluster, nil
}

func (c *ClusterNode) decide(ctx context.Context, command domain.Command) (_ any, resultErr error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.quorum(ctx); err != nil {
		return nil, err
	}
	var p domain.JoinDecisionPayload
	if err := json.Unmarshal(command.Payload, &p); err != nil {
		return nil, err
	}
	r, ok := c.state.Requests[p.RequestID]
	if !ok {
		return nil, notFound()
	}
	origin := trace.SpanContextFromContext(telemetry.Extract(context.Background(), r.Trace))
	ctx, span := telemetry.Start(ctx, "enrollment.decision", trace.WithLinks(trace.Link{SpanContext: origin}), trace.WithAttributes(attribute.String("nixmox.enrollment.id", string(p.RequestID))))
	defer func() { telemetry.End(span, resultErr) }()
	approved := command.Type == "cluster.request.approve"
	version := command.ExpectedVersions["join_request:"+string(p.RequestID)]
	if r.Decision != nil {
		if r.Decision.RequestVersion != version || r.Decision.Approved != approved {
			return nil, invalid("Join request already has a different decision")
		}
		if approved {
			if err := c.configure(ctx); err != nil {
				return nil, err
			}
		}
		return r, nil
	}
	if r.Version != version {
		return nil, &domain.Error{Code: "VERSION_CONFLICT", Message: "Join request changed"}
	}
	if time.Now().After(r.ExpiresAt) {
		return nil, invalid("Join request expired")
	}
	state := *c.state
	state.Requests = maps.Clone(state.Requests)
	state.Members = maps.Clone(state.Members)
	r.Decision = &domain.JoinDecision{Approved: approved, RequestVersion: r.Version, DecidedAt: time.Now().UTC()}
	r.Version++
	r.State = "rejected"
	if approved {
		previous, exists := state.Members[r.NodeID]
		if exists && !previous.Removed {
			return nil, invalid("Node identity is already enrolled")
		}
		daemon, broker, err := identity.RequestKeys(submission(r))
		if err != nil {
			return nil, err
		}
		if exists && (previous.DaemonKey != daemon || previous.BrokerKey != broker) {
			return nil, invalid("Rejoin cannot replace this identity's approved keys")
		}
		for id, member := range state.Members {
			if id != r.NodeID && (member.DaemonKey == daemon || member.BrokerKey == broker) {
				return nil, invalid("Machine keys already belong to another node")
			}
		}
		epoch, version := previous.Epoch+1, previous.Version+1
		issued, err := c.keys.Issue(submission(r), epoch, comms.PendingGrants(state.Cluster.ID, r.NodeID))
		if err != nil {
			return nil, err
		}
		r.State, r.Decision.IssuanceID = "approved", &issued.ID
		pin, err := identity.CertificateFingerprint(issued.BrokerCertificate)
		if err != nil {
			return nil, err
		}
		state.Members[r.NodeID] = domain.Member{NodeID: r.NodeID, Version: version, Epoch: epoch, DaemonKey: daemon, BrokerKey: broker, BrokerCertificate: pin, RequestID: r.ID}
	}
	state.Requests[r.ID] = r
	if err := c.saveState(ctx, state); err != nil {
		return nil, err
	}
	if approved {
		if err := c.configure(ctx); err != nil {
			return nil, err
		}
	}
	return r, nil
}

func (c *ClusterNode) join(ctx context.Context, command domain.Command, log engine.Execution) (_ any, resultErr error) {
	ctx, span := telemetry.Start(ctx, "enrollment.join")
	defer func() { telemetry.End(span, resultErr) }()
	var p domain.ClusterJoin
	if err := json.Unmarshal(command.Payload, &p); err != nil {
		return nil, err
	}
	api, err := enrollmentClient(p.Address)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	if c.keys.Trust != nil {
		trust := *c.keys.Trust
		if trust.CommandID != command.ID {
			c.mu.Unlock()
			return nil, invalid("Node is already enrolled")
		}
		err = c.configure(ctx)
		c.mu.Unlock()
		if err == nil {
			err = c.awaitLeaf(ctx)
		}
		return trust, err
	}
	id := domain.ID("join_" + uuid.NewSHA1(uuid.NameSpaceOID, []byte(command.ID)).String())
	span.SetAttributes(attribute.String("nixmox.enrollment.id", string(id)))
	request, err := c.keys.Request(id, p.ExpectedClusterName)
	c.mu.Unlock()
	if err != nil {
		return nil, err
	}
	span.AddEvent("enrollment.awaiting_approval")
	fmt.Fprintln(log.Log(), "Enrollment request:", id, "(awaiting administrator approval)")
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		status, err := api.Enroll(ctx, request)
		if err == nil {
			if status.Request.NodeID != request.NodeID || submission(status.Request) != request || status.Cluster.Name != p.ExpectedClusterName || status.Cluster.ID == "" || status.ClusterFingerprint == "" || status.BrokerKey == "" {
				return nil, invalid("Enrollment response identity mismatch")
			}
			c.mu.Lock()
			err = c.keys.PinEnrollment(id, status)
			c.mu.Unlock()
			if err != nil {
				return nil, err
			}
			switch status.Request.State {
			case "rejected", "expired":
				return status, invalid("Enrollment " + status.Request.State + "; explicitly start a new join attempt")
			case "approved":
				span.AddEvent("enrollment.approved")
				issued, err := api.Certificates(ctx, id)
				if err != nil {
					break
				}
				if issued.RequestID != id || issued.ClusterID != status.Cluster.ID || issued.ClusterFingerprint != status.ClusterFingerprint {
					return nil, invalid("Issued cluster trust differs from the pinned enrollment")
				}
				c.mu.Lock()
				err = c.keys.Install(domain.ClusterTrust{CommandID: command.ID, Cluster: status.Cluster, Issuance: issued, BrokerKey: status.BrokerKey, LeafAddress: status.LeafAddress, AuthorityAddress: p.Address, Topology: status.Topology})
				if err == nil {
					err = c.configure(ctx)
				}
				c.mu.Unlock()
				if err != nil {
					return nil, err
				}
				span.AddEvent("enrollment.credentials_installed")
				fmt.Fprintln(log.Log(), "Scoped cluster credentials installed; connecting pending leaf")
				return status, c.awaitLeaf(ctx)
			}
		} else {
			var apiErr *client.APIError
			if errors.As(err, &apiErr) && apiErr.StatusCode >= 400 && apiErr.StatusCode < 500 && apiErr.StatusCode != 429 {
				return nil, apiErr
			}
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}

func (c *ClusterNode) awaitLeaf(ctx context.Context) (resultErr error) {
	ctx, span := telemetry.Start(ctx, "enrollment.connect")
	defer func() { telemetry.End(span, resultErr) }()
	wait, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		c.mu.Lock()
		connected := c.transport.Broker != nil && c.transport.Broker.LeafConnections() > 0
		c.mu.Unlock()
		if connected {
			span.AddEvent("enrollment.authenticated")
			return nil
		}
		select {
		case <-wait.Done():
			return &domain.Error{Code: "TARGET_UNREACHABLE", Message: "Credentials installed but authenticated cluster leaf is not connected", Retryable: true}
		case <-ticker.C:
		}
	}
}

func (c *ClusterNode) Definitions(journal engine.LocalStore) []engine.TaskDefinition {
	definitions := c.repositoryDefinitions(journal)
	definitions = append(definitions, c.credentialDefinitions(journal)...)
	definitions = append(definitions, c.assignmentDefinition(journal))
	for _, kind := range []string{"cluster.create", "cluster.join", "cluster.request.approve", "cluster.request.reject", "cluster.leave", "host.remove", "host.force_full_membership", "host.demote"} {
		run := func(ctx context.Context, e engine.Execution) (resultErr error) {
			record, err := journal.Receipt(ctx, e.Task().CommandID)
			if err != nil {
				return err
			}
			ctx, span := telemetry.Start(ctx, kind)
			defer func() { telemetry.End(span, resultErr) }()
			logging.From(ctx).Info("membership operation", "operation", kind, "command_id", string(record.Envelope.Command.ID))
			var result any
			switch kind {
			case "cluster.create":
				result, err = c.create(ctx, record.Envelope.Command)
			case "cluster.join":
				result, err = c.join(ctx, record.Envelope.Command, e)
			case "host.force_full_membership", "host.demote":
				result, err = c.promote(ctx, record.Envelope.Command)
			case "cluster.leave", "host.remove":
				result, err = c.depart(ctx, record.Envelope.Command)
			default:
				result, err = c.decide(ctx, record.Envelope.Command)
			}
			if err != nil {
				fmt.Fprintf(e.Log(), "Membership reconciliation: %v\n", err)
				logging.From(ctx).Error("membership reconciliation", "error", err)
			}
			var failure *domain.Error
			// Definitive rejected/expired/version outcomes are observed failures.
			// Transport/storage uncertainty retains the intent for reconciliation.
			if err == nil || (errors.As(err, &failure) && (failure.Code == "INVALID_INPUT" || failure.Code == "VERSION_CONFLICT" || failure.Code == "TRUST_MISMATCH")) {
				evidence, _ := json.Marshal(struct {
					Result any           `json:"result"`
					Error  *domain.Error `json:"error"`
				}{result, failure})
				if observeErr := e.Observed(ctx, domain.Checkpoint{Phase: "membership", Evidence: evidence}); observeErr != nil {
					return observeErr
				}
			}
			return err
		}
		definitions = append(definitions, engine.TaskDefinition{Kind: kind, Phases: []engine.Phase{{Name: "membership", Interruptible: true, Resources: []engine.Resource{{Key: "membership", Capacity: 1}}, Run: run, Reconcile: run}}})
	}
	return definitions
}

func (c *ClusterNode) PublishCommand(ctx context.Context, envelope domain.Envelope) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.quorum(ctx); err != nil {
		return err
	}
	envelope.Scope, envelope.ClusterID = domain.ClusterScope, &c.state.Cluster.ID
	if envelope.Command.Type == "credential.check" {
		var p domain.CredentialCheckPayload
		if err := json.Unmarshal(envelope.Command.Payload, &p); err != nil {
			return err
		}
		record, _, err := c.credentialLocked(ctx, p.CredentialID)
		if err != nil {
			return err
		}
		holder, err := c.credentialHolderLocked(record)
		if err != nil {
			return err
		}
		envelope.OwnerNodeID = holder
		input := credentialLocalRequest{CredentialID: p.CredentialID}
		if p.RepositoryID != nil {
			state, _, err := c.repositoryLocked(ctx, *p.RepositoryID)
			if err != nil {
				return err
			}
			input.Repository = &state.Repository
		}
		envelope.Resolved, err = json.Marshal(input)
		if err != nil {
			return err
		}
	}
	if envelope.Command.Type == "repository.import" || envelope.Command.Type == "repository.fetch" {
		_, value, err := Catalog().Decode(envelope.Command)
		if err != nil {
			return err
		}
		state, err := c.repositoryInput(ctx, envelope.Command, value)
		if err != nil {
			return err
		}
		if ref := state.Repository.Remote.CredentialRef; ref != nil {
			record, _, err := c.credentialLocked(ctx, *ref)
			if err != nil {
				return err
			}
			envelope.OwnerNodeID, err = c.credentialHolderLocked(record)
			if err != nil {
				return err
			}
		}
	}
	member, exists := c.state.Members[envelope.OwnerNodeID]
	if !exists || member.Removed {
		return invalid("Executor is not an active member")
	}
	envelope.MembershipEpoch = member.Epoch
	if isPower(envelope.Command.Type) {
		if err := c.reservePower(ctx, envelope.Command); err != nil {
			return err
		}
	}
	// Expired unclaimed staging is fenced by a durable rejected receipt before
	// its private bytes are erased. Do not admit a late replay behind that fence.
	priorReceipt, _, receiptErr := readRecord[domain.AdmissionRecord](ctx, c.transport.Store, recordKey("receipt", envelope.Command.ID))
	if receiptErr == nil && priorReceipt.Error != nil {
		return priorReceipt.Error
	}
	if receiptErr != nil && !errors.Is(receiptErr, js.ErrKeyNotFound) {
		return receiptErr
	}
	// A durable claim binds the ID before JetStream's time-bounded deduplication.
	key := fmt.Sprintf("command.%x", sha256.Sum256([]byte(envelope.Command.ID)))
	entry, err := c.transport.Store.Get(ctx, key)
	if err == nil {
		var old domain.Envelope
		if err = json.Unmarshal(entry.Value, &old); err != nil {
			return err
		}
		if old.RequestHash != envelope.RequestHash {
			return &domain.Error{Code: "IDEMPOTENCY_CONFLICT", Message: "Command ID already used"}
		}
		envelope = old
	} else if errors.Is(err, js.ErrKeyNotFound) {
		data, err := json.Marshal(envelope)
		if err != nil {
			return err
		}
		if _, err = c.transport.Store.CompareAndSwap(ctx, key, 0, data); err != nil {
			return err
		}
	} else {
		return err
	}
	return c.transport.Store.Submit(ctx, envelope)
}
