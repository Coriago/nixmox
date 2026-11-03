package daemon

import (
	"context"
	"errors"
	"net"

	"nixmox/internal/cluster"
	"nixmox/internal/discovery"
	"nixmox/internal/identity"
)

func (r *Runtime) startDiscovery() error {
	if r.config.DiscoveryPort == 0 {
		return nil
	}
	connection, err := net.ListenUDP("udp4", &net.UDPAddr{Port: r.config.DiscoveryPort})
	if err != nil {
		return err
	}
	r.discovery = &discovery.Service{Observer: r.node.State.NodeID, Store: func() (cluster.Store[discovery.Record], bool) {
		r.mu.RLock()
		defer r.mu.RUnlock()
		if r.services != nil {
			return r.services.Discovery, true
		}
		return cluster.Store[discovery.Record]{}, r.node.State.ClusterID != ""
	}}
	r.workers.Add(1)
	go func() {
		defer r.workers.Done()
		if err := r.discovery.Serve(r.lifetime, connection, r.advertisement); err != nil {
			select {
			case r.failures <- err:
			default:
			}
		}
	}()
	return nil
}

func (r *Runtime) advertisement(ctx context.Context, ips []net.IP) (discovery.Advertisement, error) {
	r.mu.RLock()
	state, services := r.node.State, r.services
	thumbprint, err := identity.Thumbprint(r.node.Key.Public())
	r.mu.RUnlock()
	if err != nil {
		return discovery.Advertisement{}, err
	}
	result := discovery.Advertisement{NodeID: state.NodeID, Name: state.Name, Identity: discovery.Identity{Kind: "node", PublicKeyThumbprint: thumbprint}}
	if state.ClusterID != "" {
		if services == nil || (state.Status != "full" && state.Status != "full-stable") {
			return result, errors.New("not an enrollment contact")
		}
		record, err := services.Cluster.Get(ctx, "identity")
		if err != nil {
			return result, err
		}
		certificate, err := identity.ParseCertificate([]byte(record.Value.CA.Certificate))
		if err != nil {
			return result, err
		}
		thumbprint, err = identity.Thumbprint(certificate.PublicKey)
		if err != nil {
			return result, err
		}
		result.ClusterID = &state.ClusterID
		result.Name = record.Value.Name
		result.Identity = discovery.Identity{Kind: "cluster-ca", PublicKeyThumbprint: thumbprint}
	}
	bind, port, err := net.SplitHostPort(r.config.Listen)
	if err != nil {
		return result, err
	}
	for _, ip := range ips {
		if bind == "" || bind == "0.0.0.0" || bind == "::" || bind == ip.String() {
			result.Endpoints = append(result.Endpoints, "http://"+net.JoinHostPort(ip.String(), port))
		}
	}
	return result, nil
}
