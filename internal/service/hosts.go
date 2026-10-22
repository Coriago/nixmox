package service

import (
	"context"
	"nixmox/internal/domain"
)

type HostReader interface {
	Host(context.Context, string) (domain.Host, error)
	Hosts(context.Context, string, string) (domain.Page[domain.Host], error)
}

func (s *Service) SetHosts(r HostReader) { s.mu.Lock(); defer s.mu.Unlock(); s.hosts = r }
func (s *Service) Hosts() (HostReader, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.hosts == nil {
		return nil, domain.Err("UNAVAILABLE", "Host catalog unavailable.", 503)
	}
	return s.hosts, nil
}
