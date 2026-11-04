// Package membership owns serialized, full-member-coordinated role transitions.
package membership

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"nixmox/internal/cluster"
	"nixmox/internal/credentials"
	"nixmox/internal/domain"
	"nixmox/internal/installer"
	"nixmox/internal/installer/installers"
	"nixmox/internal/task"
)

const PromotionTask = "node-promotion"

type Input struct {
	NodeID           string `json:"nodeId"`
	ToStatus         string `json:"toStatus"`
	Force            bool   `json:"force"`
	DeploymentTaskID string `json:"deploymentTaskId,omitempty"`
}

type Info struct {
	Target              installers.Target `json:"target"`
	Endpoint            domain.Endpoint   `json:"endpoint"`
	CSR                 string            `json:"csr"`
	PublicKeyThumbprint string            `json:"publicKeyThumbprint"`
	Generation          uint64            `json:"generation"`
	CacheReady          bool              `json:"cacheReady"`
	ServerCount         int               `json:"serverCount"`
	LocalClient         bool              `json:"localClient"`
}

type Change struct {
	Role        string   `json:"role"`
	Certificate string   `json:"certificate,omitempty"`
	Routes      []string `json:"routes,omitempty"`
}

type Service struct {
	HostID      string
	ClusterID   string
	Members     cluster.Store[domain.Member]
	Credentials cluster.Store[credentials.Record]
	Recipients  cluster.Store[credentials.Recipients]
	Tasks       *task.Manager
	Cipher      credentials.Cipher
	Inspect     func(context.Context, string) (Info, error)
	Apply       func(context.Context, string, Change) error
	Certificate func(context.Context, Info) (string, error)
	Authorize   func(context.Context) error
	Replicate   func(context.Context, int) error
}

func (s *Service) Submit(ctx context.Context, input Input) (domain.Task, error) {
	local, err := s.Members.Get(ctx, s.HostID)
	if err != nil {
		return domain.Task{}, err
	}
	if local.Value.Status != "full" && local.Value.Status != "full-stable" {
		return domain.Task{}, errors.New("promotion requires a full coordinator")
	}
	if input.ToStatus != "full" && input.ToStatus != "full-stable" {
		return domain.Task{}, errors.New("unsupported membership destination")
	}
	target, err := s.Members.Get(ctx, input.NodeID)
	if err != nil {
		return domain.Task{}, err
	}
	if target.Value.Status != input.ToStatus {
		members, err := s.Members.All(ctx)
		if err != nil {
			return domain.Task{}, err
		}
		stable := 0
		for _, member := range members {
			if member.Value.Status == "full-stable" {
				stable++
			}
		}
		if err = cluster.ValidateTransition(target.Value.Status, input.ToStatus, stable); err != nil {
			return domain.Task{}, err
		}
	}
	data, err := json.Marshal(input)
	if err != nil {
		return domain.Task{}, err
	}
	work := domain.Task{HostID: s.HostID, Type: PromotionTask, Persist: true, Inputs: data, Concurrency: domain.TaskConcurrency{ExclusiveResources: []string{"cluster:" + s.ClusterID + ":membership", "host:" + input.NodeID + ":system"}}}
	if input.DeploymentTaskID != "" {
		if err = s.validateDeployment(ctx, input); err != nil {
			return domain.Task{}, err
		}
		work.Links.ParentTaskID = input.DeploymentTaskID
		return s.Tasks.SubmitChild(ctx, work, "promotion")
	}
	return s.Tasks.Submit(ctx, work)
}

func (s *Service) validateDeployment(ctx context.Context, input Input) error {
	parent, err := s.Tasks.Tasks.Get(ctx, input.DeploymentTaskID)
	if err != nil {
		return err
	}
	if input.Force || input.ToStatus != "full" || parent.Value.HostID != input.NodeID || parent.Value.Type != "fresh-deployment" || !parent.Value.Persist || parent.Value.Status != domain.Running || len(parent.Value.Context["validatedInstallation"]) == 0 || len(parent.Value.Concurrency.ExclusiveResources) != 0 {
		return errors.New("verified installation with released host ownership required")
	}
	var pin struct {
		ConfigurationID      string `json:"configurationId"`
		ConfigurationVersion uint64 `json:"configurationVersion"`
	}
	if err = json.Unmarshal(parent.Value.Inputs, &pin); err != nil {
		return err
	}
	member, err := s.Members.Get(ctx, input.NodeID)
	if err != nil {
		return err
	}
	if pin.ConfigurationID != member.Value.ConfigurationID || pin.ConfigurationVersion != member.Value.ConfigurationVersion {
		return errors.New("deployment assignment changed before promotion")
	}
	var expected installer.Preservation
	if err = json.Unmarshal(parent.Value.Context["installation"], &expected); err != nil {
		return err
	}
	observed, err := s.Inspect(ctx, input.NodeID)
	if err != nil {
		return err
	}
	if observed.Target.NodeID != input.NodeID || observed.Target.Environment != "installed" || observed.Target.BootID == expected.PreviousBootID || observed.Target.SystemPath != expected.ExpectedSystem || observed.Generation != expected.ExpectedGeneration {
		return errors.New("coordinator could not verify installed boot")
	}
	return nil
}

func (s *Service) Runner(ctx context.Context, execution *task.Execution) (any, error) {
	var input Input
	if err := json.Unmarshal(execution.Task().Inputs, &input); err != nil {
		return nil, err
	}
	record, err := s.Members.Get(ctx, input.NodeID)
	if err != nil {
		return nil, err
	}
	if _, ok := execution.Task().Context["previousStatus"]; !ok {
		if err = execution.Checkpoint(ctx, "previousStatus", record.Value.Status); err != nil {
			return nil, err
		}
	}
	replicas := 0
	switch input.ToStatus {
	case "full":
		if record.Value.Status != "pending" && record.Value.Status != "full" {
			return nil, errors.New("full promotion requires pending/full membership")
		}
		if record.Value.Status == "pending" && !input.Force {
			if input.DeploymentTaskID == "" {
				return nil, errors.New("automatic full promotion requires deployment proof")
			}
			if err = s.validateDeployment(ctx, input); err != nil {
				return nil, err
			}
		}
		if err = s.grantCredentials(ctx, record.Value); err != nil {
			return nil, err
		}
		observed, err := s.Inspect(ctx, input.NodeID)
		if err != nil {
			return nil, err
		}
		record.Value.Status, record.Value.Environment = "full", observed.Target.Environment
		record.Value.Endpoints = []domain.Endpoint{observed.Endpoint}
		record.Value.Endpoints[0].NATSClient, record.Value.Endpoints[0].NATSRoute = "", ""
		if _, err = s.Members.Update(ctx, record.Key, record.Value, record.Revision); err != nil {
			return nil, err
		}
		if err = s.Authorize(ctx); err != nil {
			return nil, err
		}
		if err = s.Apply(ctx, input.NodeID, Change{Role: "full"}); err != nil {
			return nil, err
		}
		err = s.wait(ctx, input.NodeID, func(info Info) bool { return info.CacheReady && info.ServerCount == 0 })
	case "full-stable":
		if record.Value.Status != "full" && record.Value.Status != "full-stable" {
			return nil, errors.New("server promotion requires a full member")
		}
		observed, err := s.Inspect(ctx, input.NodeID)
		if err != nil {
			return nil, err
		}
		if record.Value.Status == "full-stable" && observed.CacheReady && observed.ServerCount > 0 && observed.LocalClient {
			return map[string]string{"membershipStatus": "full-stable"}, nil
		}
		certificate, err := s.Certificate(ctx, observed)
		if err != nil {
			return nil, err
		}
		members, err := s.Members.All(ctx)
		if err != nil {
			return nil, err
		}
		routes := []string{}
		stable := 1
		for _, member := range members {
			if member.Value.Status != "full-stable" || member.Key == input.NodeID {
				continue
			}
			stable++
			for _, endpoint := range member.Value.Endpoints {
				if endpoint.NATSRoute != "" {
					routes = append(routes, endpoint.NATSRoute)
				}
			}
		}
		if len(routes) == 0 {
			return nil, errors.New("existing full-stable routes required")
		}
		// Phase2 keeps the existing bootstrap; three full-stable hosts require the
		// separate bootstrap retirement transition before this path can extend further.
		plan, err := cluster.Plan(stable)
		if err != nil {
			return nil, err
		}
		if !plan.Bootstrap {
			return nil, errors.New("bootstrap retirement must precede a third full-stable promotion")
		}
		if err = s.Apply(ctx, input.NodeID, Change{Role: "prepare-server", Certificate: certificate, Routes: routes}); err != nil {
			return nil, err
		}
		observed, err = s.Inspect(ctx, input.NodeID)
		if err != nil {
			return nil, err
		}
		if err = s.Replicate(ctx, plan.Replicas); err != nil {
			return nil, err
		}
		record, err = s.Members.Get(ctx, input.NodeID)
		if err != nil {
			return nil, err
		}
		record.Value.Status, record.Value.Endpoints = "full-stable", []domain.Endpoint{observed.Endpoint}
		if _, err = s.Members.Update(ctx, record.Key, record.Value, record.Revision); err != nil {
			return nil, err
		}
		if err = s.Authorize(ctx); err != nil {
			return nil, err
		}
		if err = s.Apply(ctx, input.NodeID, Change{Role: "full-stable"}); err != nil {
			return nil, err
		}
		err = s.wait(ctx, input.NodeID, func(info Info) bool { return info.CacheReady && info.ServerCount == 1 && info.LocalClient })
		if err == nil {
			err = s.Replicate(ctx, plan.Replicas)
			replicas = plan.Replicas
		}
	default:
		return nil, errors.New("unsupported promotion")
	}
	if err != nil {
		return nil, err
	}
	return struct {
		MembershipStatus string `json:"membershipStatus"`
		Replicas         int    `json:"replicas,omitempty"`
	}{input.ToStatus, replicas}, nil
}

func (s *Service) grantCredentials(ctx context.Context, member domain.Member) error {
	recipients, err := s.Recipients.Get(ctx, "recipients")
	if err != nil {
		return err
	}
	found := false
	for _, recipient := range recipients.Value.Recipients {
		if recipient.NodeID == member.NodeID {
			if recipient.AgePublicKey != member.AgePublicKey {
				return errors.New("recipient identity changed")
			}
			found = true
		}
	}
	if found {
		return nil
	}
	recipients.Value.Recipients = append(recipients.Value.Recipients, credentials.Recipient{NodeID: member.NodeID, AgePublicKey: member.AgePublicKey})
	cursor := ""
	for {
		records, next, err := s.Credentials.List(ctx, cursor, 100)
		if err != nil {
			return err
		}
		for _, record := range records {
			record.Value.Content, err = s.Cipher.Rekey(ctx, record.Value.Content, recipients.Value.Recipients)
			if err != nil {
				return err
			}
			if _, err = s.Credentials.Update(ctx, record.Key, record.Value, record.Revision); err != nil {
				return err
			}
		}
		if next == "" {
			break
		}
		cursor = next
	}
	_, err = s.Recipients.Update(ctx, "recipients", recipients.Value, recipients.Revision)
	return err
}

func (s *Service) wait(ctx context.Context, nodeID string, ready func(Info) bool) error {
	deadline, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	for {
		probe, stop := context.WithTimeout(deadline, 2*time.Second)
		observed, err := s.Inspect(probe, nodeID)
		stop()
		if err == nil && ready(observed) {
			return nil
		}
		select {
		case <-deadline.Done():
			return deadline.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}
