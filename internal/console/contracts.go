// Package console contains adapters only; mutations always use Commands.
package console

import (
	"context"
	"net/http"
	"nixmox/internal/domain"
	"nixmox/internal/engine"
	"nixmox/internal/presentation"
	"nixmox/internal/service"
)

type Client interface {
	service.Commands
	service.Reads
	service.SecretIngress
	service.Resolutions
}
type Gateway struct {
	Commands     service.Commands
	Reads        service.Reads
	Secrets      service.SecretIngress
	Resolutions  service.Resolutions
	Enrollment   engine.Enrollment
	Cache        engine.CacheGateway
	Presentation presentation.Presentation
}

func (g Gateway) Handler() (http.Handler, error) {
	// Bind route-specific management/loopback checks, bounded ingress and snapshots
	// before exposing a listener. Never log secret bodies in common middleware.
	return nil, domain.Unavailable("HTTP gateway")
}

type Terminal struct {
	Client       Client
	Presentation presentation.Presentation
	Renderer     presentation.Renderer
}

func (t Terminal) Run(context.Context, bool) error {
	// Charm dependencies stay here; shared preview and services remain platform-free.
	return domain.Unavailable("terminal interface")
}
