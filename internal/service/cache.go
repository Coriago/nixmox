package service

import (
	"context"
	"net/http"
	"nixmox/internal/domain"
)

type CacheService interface {
	Status(context.Context) (domain.CacheStatus, error)
	Locations(context.Context, string) ([]domain.CacheLocation, error)
	ServeLocal(http.ResponseWriter, *http.Request)
}

func (s *Service) SetCache(c CacheService) { s.mu.Lock(); defer s.mu.Unlock(); s.cache = c }
func (s *Service) Cache() (CacheService, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cache == nil {
		return nil, domain.Err("CACHE_UNAVAILABLE", "Cache service is unavailable.", 503)
	}
	return s.cache, nil
}
