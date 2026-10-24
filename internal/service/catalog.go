package service

import (
	"context"

	"nixmox/internal/domain"
	"nixmox/internal/engine"
)

// Catalog contains implemented mutations and the retained deployment contracts.
// Deployment entries describe requirements, not executable readiness.
func Catalog() *Registry {
	network := operationBehavior{validate: func(_ *ClusterNode, _ context.Context, command domain.Command, _ any) error {
		if len(command.ExpectedVersions) != 0 {
			return invalid("This command has no version prerequisites")
		}
		return nil
	}, define: (*Service).networkDefinition}
	discovery := network
	discovery.validate = func(c *ClusterNode, ctx context.Context, command domain.Command, value any) error {
		if !c.config.Discovery.Enable {
			return invalid("Discovery is disabled")
		}
		return network.validate(c, ctx, command, value)
	}
	discovery.define = func(s *Service) engine.TaskDefinition { return s.discoveryDefinition() }
	refresh := operationBehavior{validate: (*ClusterNode).validateMembership, resolve: targetExecutor, define: (*Service).networkDefinition}
	repository := operationBehavior{resolve: (*ClusterNode).resolveRepository,
		validate: func(c *ClusterNode, ctx context.Context, command domain.Command, value any) error {
			_, err := c.repositoryInput(ctx, command, value)
			return err
		}, define: func(s *Service) engine.TaskDefinition { return s.Cluster.repositoryDefinition() }}
	credential := operationBehavior{resolve: (*ClusterNode).resolveCredential,
		validate: func(c *ClusterNode, ctx context.Context, command domain.Command, value any) error {
			_, err := c.credentialInput(ctx, command, value)
			return err
		}, define: func(s *Service) engine.TaskDefinition { return s.Cluster.credentialDefinition() }}
	assignment := operationBehavior{
		validate: func(c *ClusterNode, ctx context.Context, command domain.Command, value any) error {
			return c.validateAssignment(ctx, command, value.(domain.HostAssign))
		}, define: func(s *Service) engine.TaskDefinition { return s.Cluster.assignmentDefinition() }}
	power := operationBehavior{
		validate: func(c *ClusterNode, ctx context.Context, command domain.Command, value any) error {
			_, err := c.validatePower(ctx, command, value.(domain.HostPower))
			return err
		}, resolve: func(c *ClusterNode, ctx context.Context, e *domain.Envelope, _ any) error {
			e.OwnerNodeID = e.Command.TargetID
			return c.reservePower(ctx, e.Command)
		}, define: (*Service).powerDefinition}
	r := &Registry{operations: make(map[string]Operation)}
	for _, op := range []Operation{
		operation[domain.Empty]("network.check", domain.NodeLocal, "", nil, nil, network),
		operation[domain.Empty]("discovery.search", domain.NodeLocal, "", nil, nil, discovery),
		operation[domain.ClusterJoin]("cluster.join", domain.NodeLocal, "join_request", nil, nil, membership((*ClusterNode).join)),
		operation[domain.ClusterCreate]("cluster.create", domain.NodeLocal, "cluster", nil, nil, membership((*ClusterNode).create)),
		operation[domain.JoinDecisionPayload]("cluster.request.approve", domain.ClusterScope, "host", []string{"join_request"}, nil, membership((*ClusterNode).decide)),
		operation[domain.JoinDecisionPayload]("cluster.request.reject", domain.ClusterScope, "join_request", []string{"join_request"}, nil, membership((*ClusterNode).decide)),
		operation[domain.ClusterLeave]("cluster.leave", domain.ClusterScope, "host", []string{"host"}, nil, membership((*ClusterNode).depart)),
		operation[domain.HostAssign]("host.assign", domain.ClusterScope, "host", []string{"host", "configuration"}, nil, assignment),
		operation[domain.HostAdopt]("host.adopt", domain.ClusterScope, "host", []string{"host"}, nil),
		operation[domain.HostRemove]("host.remove", domain.ClusterScope, "host", []string{"host"}, nil, membership((*ClusterNode).depart)),
		operation[domain.HostTarget]("host.refresh", domain.ClusterScope, "host", nil, nil, refresh),
		operation[domain.ForceFullMembership]("host.demote", domain.ClusterScope, "host", []string{"host"}, nil, membership((*ClusterNode).promote)),
		operation[domain.ForceFullMembership]("host.force_full_membership", domain.ClusterScope, "host", []string{"host"}, nil, membership((*ClusterNode).promote)),
		operation[domain.HostPower]("host.shutdown", domain.ClusterScope, "host", []string{"host"}, []string{"power"}, power),
		operation[domain.HostPower]("host.reboot", domain.ClusterScope, "host", []string{"host"}, []string{"power"}, power),
		operation[domain.InstallerEntry]("host.enter_installer", domain.ClusterScope, "host", nil, []string{"install", "power"}),
		operation[domain.RepositoryImport]("repository.import", domain.ClusterScope, "repository", nil, nil, repository),
		operation[domain.RepositoryTarget]("repository.fetch", domain.ClusterScope, "repository", nil, nil, repository),
		operation[domain.RepositoryRemoteSet]("repository.remote.set", domain.ClusterScope, "repository", []string{"repository"}, nil, repository),
		operation[domain.RepositoryCredentialSet]("repository.credential.set", domain.ClusterScope, "repository", nil, nil, repository),
		operation[domain.DeploymentPreview]("deployment.preview", domain.ClusterScope, "validation", []string{"host"}, nil),
		operation[domain.DeploymentBuild]("deployment.build", domain.ClusterScope, "candidate", []string{"host"}, []string{"build"}),
		operation[domain.DeploymentDiff]("deployment.diff", domain.ClusterScope, "diff", nil, nil),
		operation[domain.DeploymentApply]("deployment.apply", domain.ClusterScope, "deployment", []string{"host"}, []string{"build"}),
		operation[domain.DeploymentApprove]("deployment.approve", domain.ClusterScope, "task", nil, nil),
		operation[domain.DeploymentRollback]("deployment.rollback", domain.ClusterScope, "deployment", []string{"host"}, nil),
		operation[domain.DeploymentInstall]("deployment.install", domain.ClusterScope, "deployment", nil, []string{"install"}),
		operation[domain.CredentialImport]("credential.import", domain.ClusterScope, "credential", nil, []string{"credential"}, credential),
		operation[domain.CredentialCheckPayload]("credential.check", domain.ClusterScope, "credential", nil, []string{"credential"}, credential),
		operation[domain.CredentialTarget]("credential.remove", domain.ClusterScope, "credential", []string{"credential"}, []string{"credential"}, credential),
		operation[domain.TaskTarget]("task.cancel", domain.Inherited, "task", []string{"task"}, nil),
		operation[domain.TaskTarget]("task.retry", domain.Inherited, "task", []string{"task"}, nil),
	} {
		r.operations[op.Name] = op
	}
	return r
}

type membershipAction func(*ClusterNode, context.Context, domain.Command, engine.Execution) (any, error)

func membership(action membershipAction) operationBehavior {
	return operationBehavior{validate: (*ClusterNode).validateMembership,
		define: func(s *Service) engine.TaskDefinition {
			return s.Cluster.membershipDefinition(action)
		}}
}

func targetExecutor(_ *ClusterNode, _ context.Context, envelope *domain.Envelope, _ any) error {
	envelope.OwnerNodeID = envelope.Command.TargetID
	return nil
}
