package service

import (
	"context"
	"nixmox/internal/domain"
)

type SourceReader interface {
	Repository(context.Context, string) (domain.Repository, error)
	Repositories(context.Context, string, string) (domain.Page[domain.Repository], error)
	Artifact(context.Context, string) (domain.Artifact, error)
	Diff(context.Context, string, string, string) (any, error)
}
type ConfigurationReader interface {
	Templates(context.Context, string, string, string) (any, error)
	Template(context.Context, string, string, string) (any, error)
	Draft(context.Context, string) (any, error)
	DraftDiff(context.Context, string) (any, error)
	Configurations(context.Context, string, string, string, string, string) (domain.Page[domain.Configuration], error)
	Configuration(context.Context, string) (domain.Configuration, error)
}

func (s *Service) SetConfigurations(r ConfigurationReader) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.configurations = r
}
func (s *Service) Configurations() (ConfigurationReader, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.configurations == nil {
		return nil, domain.Err("UNAVAILABLE", "Configuration catalog is unavailable.", 503)
	}
	return s.configurations, nil
}
func (s *Service) SetSources(r SourceReader) { s.mu.Lock(); defer s.mu.Unlock(); s.sources = r }
func (s *Service) Sources() (SourceReader, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sources == nil {
		return nil, domain.Err("UNAVAILABLE", "Source catalog is unavailable.", 503)
	}
	return s.sources, nil
}
