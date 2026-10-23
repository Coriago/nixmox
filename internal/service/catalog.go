package service

import "nixmox/internal/domain"

// Catalog contains every v1 mutation. Metadata describes requirements, not readiness.
// Conditional versions/capabilities are validated by the future feature handler.
func Catalog() *Registry {
	r := &Registry{operations: make(map[string]Operation)}
	for _, op := range []Operation{
		operation[domain.Empty]("network.check", domain.NodeLocal, "", nil, nil),
		operation[domain.Empty]("network.wifi.scan", domain.NodeLocal, "", nil, nil),
		operation[domain.NetworkConnect]("network.wifi.connect", domain.NodeLocal, "", nil, nil),
		operation[domain.Empty]("discovery.search", domain.NodeLocal, "", nil, nil),
		operation[domain.IdentityReset]("identity.reset", domain.NodeLocal, "", []string{"identity"}, nil),
		operation[domain.ClusterJoin]("cluster.join", domain.NodeLocal, "join_request", nil, nil),
		operation[domain.ClusterCreate]("cluster.create", domain.NodeLocal, "cluster", nil, nil),
		operation[domain.JoinDecisionPayload]("cluster.request.approve", domain.ClusterScope, "host", []string{"join_request"}, nil),
		operation[domain.JoinDecisionPayload]("cluster.request.reject", domain.ClusterScope, "join_request", []string{"join_request"}, nil),
		operation[domain.ClusterAdd]("cluster.add", domain.ClusterScope, "host", nil, nil),
		operation[domain.ClusterLeave]("cluster.leave", domain.ClusterScope, "host", []string{"host"}, nil),
		operation[domain.HostAssign]("host.assign", domain.ClusterScope, "host", []string{"host", "configuration"}, nil),
		operation[domain.HostAdopt]("host.adopt", domain.ClusterScope, "host", []string{"host"}, nil),
		operation[domain.HostRemove]("host.remove", domain.ClusterScope, "host", []string{"host"}, nil),
		operation[domain.HostBootstrap]("host.bootstrap", domain.ClusterScope, "host", nil, []string{"credential"}),
		operation[domain.HostTarget]("host.refresh", domain.ClusterScope, "host", nil, nil),
		operation[domain.HostDiagnostics]("host.diagnostics", domain.ClusterScope, "diagnostic", nil, nil),
		operation[domain.ForceFullMembership]("host.force_full_membership", domain.ClusterScope, "host", []string{"host"}, nil),
		operation[domain.HostPower]("host.shutdown", domain.ClusterScope, "host", []string{"host"}, []string{"power"}),
		operation[domain.HostPower]("host.reboot", domain.ClusterScope, "host", []string{"host"}, []string{"power"}),
		operation[domain.InstallerEntry]("host.enter_installer", domain.ClusterScope, "host", nil, []string{"install", "power"}),
		operation[domain.RepositoryImport]("repository.import", domain.ClusterScope, "repository", nil, nil),
		operation[domain.RepositoryTarget]("repository.fetch", domain.ClusterScope, "repository", nil, nil),
		operation[domain.RepositoryRemoteSet]("repository.remote.set", domain.ClusterScope, "repository", []string{"repository"}, nil),
		operation[domain.RepositoryCredentialSet]("repository.credential.set", domain.ClusterScope, "repository", nil, nil),
		operation[domain.RepositoryRebase]("repository.rebase", domain.ClusterScope, "repository", []string{"repository"}, nil),
		operation[domain.RebaseResolve]("repository.rebase.resolve", domain.ClusterScope, "repository", []string{"draft", "repository"}, nil),
		operation[domain.TaskTarget]("repository.rebase.abort", domain.ClusterScope, "task", nil, nil),
		operation[domain.RepositoryGCPlan]("repository.gc.plan", domain.ClusterScope, "gc_plan", nil, nil),
		operation[domain.RepositoryGC]("repository.gc", domain.ClusterScope, "gc_plan", []string{"gc_plan", "artifact"}, nil),
		operation[domain.RepositoryPublish]("repository.publish", domain.ClusterScope, "publication", []string{"repository"}, []string{"credential"}),
		operation[domain.PublicationMerge]("publication.merge", domain.ClusterScope, "publication", []string{"publication"}, []string{"credential"}),
		operation[domain.ArtifactDelete]("artifact.delete", domain.ClusterScope, "gc_plan", []string{"gc_plan", "artifact"}, nil),
		operation[domain.ConfigurationGenerate]("configuration.generate", domain.ClusterScope, "draft", nil, nil),
		operation[domain.DraftEdit]("configuration.draft.edit", domain.ClusterScope, "draft", []string{"draft"}, nil),
		operation[domain.ConfigurationValidate]("configuration.validate", domain.ClusterScope, "validation", nil, nil),
		operation[domain.DraftTarget]("configuration.commit", domain.ClusterScope, "configuration", []string{"draft", "repository"}, nil),
		operation[domain.HostTarget]("onboarding.inspect", domain.ClusterScope, "observation", nil, nil),
		operation[domain.OnboardingPrepare]("onboarding.prepare", domain.ClusterScope, "preparation", nil, nil),
		operation[domain.PreparationCommit]("onboarding.commit", domain.ClusterScope, "preparation", []string{"preparation", "repository"}, nil),
		operation[domain.CredentialTarget]("onboarding.sops.credential.set", domain.ClusterScope, "settings", []string{"settings"}, nil),
		operation[domain.DeploymentPreview]("deployment.preview", domain.ClusterScope, "validation", []string{"host"}, nil),
		operation[domain.DeploymentBuild]("deployment.build", domain.ClusterScope, "candidate", []string{"host"}, []string{"build"}),
		operation[domain.DeploymentDiff]("deployment.diff", domain.ClusterScope, "diff", nil, nil),
		operation[domain.DeploymentApply]("deployment.apply", domain.ClusterScope, "deployment", []string{"host"}, []string{"build"}),
		operation[domain.DeploymentApprove]("deployment.approve", domain.ClusterScope, "task", nil, nil),
		operation[domain.DeploymentRollback]("deployment.rollback", domain.ClusterScope, "deployment", []string{"host"}, nil),
		operation[domain.DeploymentInstall]("deployment.install", domain.ClusterScope, "deployment", nil, []string{"install"}),
		operation[domain.MediaBuild]("media.build", domain.ClusterScope, "media", nil, []string{"build"}),
		operation[domain.MediaFetch]("media.fetch", domain.ClusterScope, "media", nil, nil),
		operation[domain.MediaProfileRegister]("media.profile.register", domain.ClusterScope, "media_profile", nil, nil),
		operation[domain.MediaPlanCreate]("media.plan", domain.ClusterScope, "media_plan", nil, []string{"media_write"}),
		operation[domain.MediaWrite]("media.write", domain.ClusterScope, "media", []string{"media_plan"}, []string{"media_write"}),
		operation[domain.CredentialImport]("credential.import", domain.ClusterScope, "credential", nil, []string{"credential"}),
		operation[domain.CredentialCheckPayload]("credential.check", domain.ClusterScope, "credential", nil, []string{"credential"}),
		operation[domain.CredentialTarget]("credential.remove", domain.ClusterScope, "credential", []string{"credential"}, []string{"credential"}),
		operation[domain.CredentialTarget]("cache.credential.set", domain.ClusterScope, "settings", []string{"settings"}, nil),
		operation[domain.TaskTarget]("task.cancel", domain.Inherited, "task", []string{"task"}, nil),
		operation[domain.TaskTarget]("task.retry", domain.Inherited, "task", []string{"task"}, nil),
	} {
		r.operations[op.Name] = op
	}
	return r
}
