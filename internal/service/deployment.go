package service

import (
	"context"
	"nixmox/internal/domain"
)

type DeploymentReader interface {
	Candidate(context.Context, string) (domain.Candidate, error)
	Activities(context.Context, string, string, string) (any, error)
	Diff(context.Context, string) (domain.PackageDiff, error)
	DiffChanges(context.Context, string, string, string, string, string) (domain.Page[domain.PackageChange], error)
	Deployment(context.Context, string) (domain.Deployment, error)
	Generations(context.Context, string, string, string) (domain.Page[domain.Generation], error)
}

func (s *Service) SetDeployments(r DeploymentReader) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deployments = r
}
func (s *Service) Deployments() (DeploymentReader, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.deployments == nil {
		return nil, domain.Err("UNAVAILABLE", "Deployment service unavailable.", 503)
	}
	return s.deployments, nil
}
