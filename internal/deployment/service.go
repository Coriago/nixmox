// Package deployment coordinates pinned configuration builds, review and activation.
package deployment

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"nixmox/internal/cluster"
	"nixmox/internal/configuration"
	"nixmox/internal/domain"
	"nixmox/internal/host"
	"nixmox/internal/recipe"
	"nixmox/internal/task"
)

type Input struct {
	ConfigurationID      string `json:"configurationId"`
	ConfigurationVersion uint64 `json:"configurationVersion"`
	BuildHostID          string `json:"buildHostId,omitempty"`
	AutoApprove          bool   `json:"autoApprove"`
}

type Service struct {
	HostID         string
	Environment    string
	Configurations *configuration.Service
	Members        cluster.Store[domain.Member]
	Tasks          *task.Manager
	Builder        host.Builder
	Installation   *InstallationRuntime
}

func (s Service) validate(ctx context.Context, id string, input Input) (domain.Member, error) {
	if input.BuildHostID != "" && input.BuildHostID != id {
		return domain.Member{}, errors.New("separate build-host dispatch unavailable")
	}
	member, err := s.Members.Get(ctx, id)
	if err != nil {
		return member.Value, err
	}
	if !(member.Value.Status == "pending" && (member.Value.Environment == "installer" || member.Value.Environment == "installed")) && !(member.Value.Environment == "installed" && (member.Value.Status == "full" || member.Value.Status == "full-stable")) {
		return member.Value, errors.New("pending installer or installed full member required")
	}
	_, err = s.Configurations.Get(ctx, input.ConfigurationID, input.ConfigurationVersion)
	return member.Value, err
}

func (s Service) Assign(ctx context.Context, id string, input Input) (domain.Member, error) {
	if err := s.coordinator(ctx); err != nil {
		return domain.Member{}, err
	}
	if _, err := s.validate(ctx, id, input); err != nil {
		return domain.Member{}, err
	}
	owner := uuid.NewString()
	if err := s.Tasks.Resources.Acquire(ctx, owner, []string{"host:" + id + ":system"}); err != nil {
		return domain.Member{}, err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = s.Tasks.Resources.Release(cleanup, owner)
	}()
	record, err := s.Members.Get(ctx, id)
	if err != nil {
		return record.Value, err
	}
	record.Value.ConfigurationID = input.ConfigurationID
	record.Value.ConfigurationVersion = input.ConfigurationVersion
	_, err = s.Members.Update(ctx, id, record.Value, record.Revision)
	return record.Value, err
}

func (s Service) Submit(ctx context.Context, id string, input Input) (domain.Task, error) {
	if err := s.coordinator(ctx); err != nil {
		return domain.Task{}, err
	}
	member, err := s.validate(ctx, id, input)
	if err != nil {
		return domain.Task{}, err
	}
	if member.ConfigurationID != input.ConfigurationID || member.ConfigurationVersion != input.ConfigurationVersion {
		return domain.Task{}, recipe.Invalid(errors.New("assign this configuration version before deployment"))
	}
	data, err := json.Marshal(input)
	if err != nil {
		return domain.Task{}, err
	}
	kind, persist := "deployment", false
	if member.Status == "pending" {
		kind, persist = InstallationTask, true
	}
	return s.Tasks.Submit(ctx, domain.Task{HostID: id, Type: kind, Persist: persist, Inputs: data, Concurrency: domain.TaskConcurrency{ExclusiveResources: []string{"host:" + id + ":system"}}})
}

func (s Service) Build(ctx context.Context, execution *task.Execution) (any, error) {
	return s.buildConfiguration(ctx, execution, false)
}

func (s Service) coordinator(ctx context.Context) error {
	local, err := s.Members.Get(ctx, s.HostID)
	if err != nil {
		return err
	}
	if local.Value.Status != "full" && local.Value.Status != "full-stable" {
		return errors.New("deployment assignment requires a full coordinator")
	}
	return nil
}

func (s Service) assigned(ctx context.Context, input Input) error {
	member, err := s.validate(ctx, s.HostID, input)
	if err != nil {
		return err
	}
	if member.ConfigurationID != input.ConfigurationID || member.ConfigurationVersion != input.ConfigurationVersion {
		return fmt.Errorf("host assignment changed since task submission")
	}
	return nil
}
