package service

import (
	"context"
	"io"
	"nixmox/internal/domain"
)

type CredentialService interface {
	Credentials(context.Context, string, string) (domain.Page[domain.Credential], error)
	Credential(context.Context, string) (domain.Credential, error)
	Import(context.Context, domain.SecretImport, io.Reader) (domain.Receipt, error)
}

func (s *Service) SetCredentials(c CredentialService) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.credentials = c
}
func (s *Service) Credentials() (CredentialService, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.credentials == nil {
		return nil, domain.Err("CREDENTIAL_UNAVAILABLE", "Credential service is unavailable.", 503)
	}
	return s.credentials, nil
}
