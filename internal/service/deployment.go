package service

import (
	"context"
	"nixmox/internal/domain"
)

type DeploymentReader interface {
	Candidate(context.Context, string) (domain.Candidate, error)
	Activities(context.Context, string, string, string) (any, error)
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
