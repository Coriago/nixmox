package engine

import (
	"context"
	"crypto"
	"io"

	"nixmox/internal/domain"
)

type Identity interface {
	Load(context.Context) (domain.Identity, error)
	Initialize(context.Context) (domain.Identity, error)
	// Receipt and replacement identity form one durable recovery boundary.
	Reset(context.Context, domain.Envelope) (domain.Identity, error)
	Sign(context.Context, []byte) ([]byte, error)
}
type Enrollment interface {
	Request(context.Context, domain.JoinRequest) (domain.JoinRequest, error)
	Decide(context.Context, domain.ID, domain.Version, bool) (domain.JoinRequest, error)
	Issue(context.Context, domain.ID) (domain.Issuance, error)
	Pin(context.Context, domain.Issuance) error
	VerifyRemoval(context.Context, domain.SignedRemoval) error
}
type Secrets interface {
	Stage(context.Context, SecretIngress, io.Reader) (domain.SecretStaging, error)
	Consume(context.Context, domain.ID, domain.ID) error
	// Never return plaintext through a feature result. Close erases bounded buffers.
	Open(context.Context, domain.ID) (io.ReadCloser, error)
	Remove(context.Context, domain.ID) error
}
type CertificateIssuer interface {
	Issue(context.Context, domain.JoinRequest, domain.JoinDecision) (domain.Issuance, error)
	PublicKey() crypto.PublicKey
	SignRemoval(context.Context, domain.Removal) (domain.SignedRemoval, error)
}
