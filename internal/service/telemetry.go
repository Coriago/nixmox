package service

import "nixmox/internal/domain"

type TelemetrySource interface {
	Snapshot() domain.HostTelemetry
	Subscribe() (<-chan struct{}, func())
}
type RemoteTelemetryReader interface {
	RemoteTelemetry(string) (domain.HostTelemetry, error)
}

func (s *Service) SetTelemetry(source TelemetrySource) error {
	s.mu.Lock()
	if s.telemetry != nil || s.ctx.Err() != nil {
		s.mu.Unlock()
		return domain.Err("CONFLICT", "Telemetry source is already configured or the service is closed.", 409)
	}
	s.telemetry = source
	s.workers.Add(1)
	s.mu.Unlock()
	ch, unsubscribe := source.Subscribe()
	go func() {
		defer s.workers.Done()
		defer unsubscribe()
		for {
			select {
			case <-s.ctx.Done():
				return
			case <-ch:
				s.NotifyTelemetry()
			}
		}
	}()
	return nil
}
func (s *Service) LocalTelemetry() domain.HostTelemetry {
	s.mu.Lock()
	source, id := s.telemetry, s.identity
	s.mu.Unlock()
	if source == nil {
		return domain.UnknownTelemetry(id.NodeID)
	}
	return source.Snapshot()
}
func (s *Service) Telemetry(host string) (domain.HostTelemetry, error) {
	s.mu.Lock()
	id, transport := s.identity, s.clusterTransport
	s.mu.Unlock()
	if host == id.NodeID {
		return s.LocalTelemetry(), nil
	}
	if !domain.SafeID.MatchString(host) {
		return domain.HostTelemetry{}, domain.Err("INVALID_QUERY", "Invalid host ID.", 400)
	}
	if reader, ok := transport.(RemoteTelemetryReader); ok {
		return reader.RemoteTelemetry(host)
	}
	return domain.HostTelemetry{}, domain.Err("NOT_FOUND", "Host not found.", 404)
}
func (s *Service) NotifyTelemetry() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for ch := range s.telemetrySubscribers {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}
func (s *Service) SubscribeTelemetry() (<-chan struct{}, func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.telemetrySubscribers) >= 64 {
		return nil, nil, domain.Err("UNAVAILABLE", "Telemetry subscriber capacity reached.", 503)
	}
	if s.telemetrySubscribers == nil {
		s.telemetrySubscribers = map[chan struct{}]struct{}{}
	}
	ch := make(chan struct{}, 1)
	s.telemetrySubscribers[ch] = struct{}{}
	return ch, func() { s.mu.Lock(); delete(s.telemetrySubscribers, ch); s.mu.Unlock() }, nil
}
