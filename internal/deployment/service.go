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
	Configurations *configuration.Service
	Members        cluster.Store[domain.Member]
	Tasks          *task.Manager
	Builder        host.Builder
}

func (s Service) validate(ctx context.Context, id string, input Input) (domain.Member, error) {
	if id != s.HostID || (input.BuildHostID != "" && input.BuildHostID != id) {
		return domain.Member{}, errors.New("remote deployment/build dispatch unavailable")
	}
	member, err := s.Members.Get(ctx, id)
	if err != nil {
		return member.Value, err
	}
	if member.Value.Environment != "installed" || member.Value.Status != "full-stable" {
		return member.Value, errors.New("local installed full-stable member required; client-only promotion remains unavailable")
	}
	_, err = s.Configurations.Get(ctx, input.ConfigurationID, input.ConfigurationVersion)
	return member.Value, err
}

func (s Service) Assign(ctx context.Context, id string, input Input) (domain.Member, error) {
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
	return s.Tasks.Submit(ctx, domain.Task{HostID: id, Type: "deployment", Inputs: data, Concurrency: domain.TaskConcurrency{ExclusiveResources: []string{"host:" + id + ":system"}}})
}

func (s Service) Build(ctx context.Context, execution *task.Execution) (any, error) {
	var input Input
	if err := json.Unmarshal(execution.Task().Inputs, &input); err != nil {
		return nil, err
	}
	work, directory, name, err := s.Configurations.Materialize(ctx, input.ConfigurationID, input.ConfigurationVersion)
	if err != nil {
		return nil, err
	}
	defer work.Close()
	log := execution.LogWriter(ctx)
	builder := s.Builder
	builder.Process.Log = log
	result, err := builder.BuildPath(ctx, directory, "configurations.nixos."+name+".config.system.build.toplevel")
	return result, errors.Join(err, log.Close())
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
