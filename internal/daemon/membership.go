package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
	"nixmox/internal/api"
	"nixmox/internal/cluster"
	"nixmox/internal/domain"
	"nixmox/internal/enrollment"
	"nixmox/internal/host"
	"nixmox/internal/identity"
	"nixmox/internal/installer"
	"nixmox/internal/membership"
	"nixmox/internal/task"
)

func (r *Runtime) membershipInfo(ctx context.Context) (membership.Info, error) {
	r.mu.RLock()
	nodeID := r.node.State.NodeID
	info := membership.Info{ServerCount: len(r.servers), CacheReady: r.services != nil && r.services.StoreCache != nil}
	clientPort, routePort := r.config.NATSPort, r.config.RoutePort
	if len(r.servers) > 0 {
		endpoint, _ := url.Parse(r.servers[0].ClientURL())
		clientPort, _ = strconv.Atoi(endpoint.Port())
		routePort = r.servers[0].ClusterAddr().Port
		info.LocalClient = r.client != nil && r.client.ConnectedServerId() == r.servers[0].ID()
	}
	r.mu.RUnlock()
	var err error
	info.Target, err = installer.InspectLocal(ctx, nodeID, r.config.Environment)
	if err != nil {
		return info, err
	}
	endpoint, err := url.Parse(r.config.AdvertiseURL)
	if err != nil {
		return info, err
	}
	info.Endpoint = domain.Endpoint{Network: "lan", HTTP: r.config.AdvertiseURL, NATSClient: "tls://" + net.JoinHostPort(endpoint.Hostname(), strconv.Itoa(clientPort)), NATSRoute: "nats-route://" + net.JoinHostPort(endpoint.Hostname(), strconv.Itoa(routePort))}
	csr, err := r.node.CSR()
	if err != nil {
		return info, err
	}
	info.CSR = string(csr)
	info.PublicKeyThumbprint, err = identity.Thumbprint(r.node.Key.Public())
	if err != nil {
		return info, err
	}
	profile, err := os.Readlink("/nix/var/nix/profiles/system")
	if err == nil {
		generation := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(profile), "system-"), "-link")
		info.Generation, err = strconv.ParseUint(generation, 10, 64)
	}
	if errors.Is(err, os.ErrNotExist) {
		err = nil
	}
	return info, err
}

func (r *Runtime) startMembershipTransport(nc *nats.Conn, svc *api.Services) error {
	if err := r.serveRequests(nc, "host."+svc.Tasks.HostID+".membership", func(ctx context.Context, msg *nats.Msg) (any, error) {
		var change membership.Change
		if err := json.Unmarshal(msg.Data, &change); err != nil {
			return nil, err
		}
		return nil, r.applyMembership(ctx, svc, change)
	}); err != nil {
		return err
	}
	if svc.Pending {
		return nil
	}
	return r.serveRequests(nc, "membership."+svc.Tasks.HostID+".request.*", func(ctx context.Context, msg *nats.Msg) (any, error) {
		var input membership.Input
		if err := json.Unmarshal(msg.Data, &input); err != nil {
			return nil, err
		}
		actor := msg.Subject[strings.LastIndex(msg.Subject, ".")+1:]
		if input.NodeID != actor || input.ToStatus != "full" || input.Force || input.DeploymentTaskID == "" {
			return nil, errors.New("only own validated installation may request automatic promotion")
		}
		return svc.Membership.Submit(ctx, input)
	})
}

func (r *Runtime) applyMembership(ctx context.Context, svc *api.Services, change membership.Change) error {
	record, err := svc.Members.Get(ctx, svc.Tasks.HostID)
	if err != nil {
		return err
	}
	if record.Value.Status != "full" && record.Value.Status != "full-stable" {
		return errors.New("coordinator must grant full membership before activation")
	}
	data, err := os.ReadFile(filepath.Join(r.config.StateDir, "cluster.json"))
	if err != nil {
		return err
	}
	var saved startup
	if err = json.Unmarshal(data, &saved); err != nil {
		return err
	}
	saved.Member = record.Value
	saved.Member.Environment = r.config.Environment
	switch change.Role {
	case "prepare-server":
		if len(change.Routes) == 0 {
			return errors.New("server routes required")
		}
		if err = r.node.VerifyEnrollment([]byte(change.Certificate), []byte(saved.Cluster.CA.Certificate), time.Now()); err != nil {
			return err
		}
		saved.Certificate, saved.PeerRoutes = change.Certificate, change.Routes
		if err = r.saveStartup(saved); err != nil {
			return err
		}
		_, err = r.startPeer(ctx, saved, svc.Members)
		return err
	case "full", "full-stable":
		if saved.Member.Status != change.Role {
			return errors.New("requested role differs from coordinator decision")
		}
		r.mu.RLock()
		already := r.node.State.Status == change.Role && !svc.Pending
		if change.Role == "full-stable" {
			already = already && len(r.servers) == 1 && r.client.ConnectedServerId() == r.servers[0].ID()
		}
		r.mu.RUnlock()
		if already {
			return nil
		}
		if change.Role == "full-stable" && len(saved.PeerRoutes) == 0 {
			return errors.New("server was not prepared")
		}
		if err = r.saveStartup(saved); err != nil {
			return err
		}
		return errRoleRestart
	default:
		return errors.New("unsupported membership activation")
	}
}

func (r *Runtime) promotionCertificate(ctx context.Context, info membership.Info) (string, error) {
	svc, err := r.Services()
	if err != nil {
		return "", err
	}
	member, err := svc.Members.Get(ctx, info.Target.NodeID)
	if err != nil {
		return "", err
	}
	proof, err := svc.Enrollment.Requests.Get(ctx, member.Value.EnrollmentRequestID)
	if err != nil {
		return "", err
	}
	if proof.Value.JoiningNode.PublicKeyThumbprint != info.PublicKeyThumbprint {
		return "", errors.New("promotion would change enrolled identity")
	}
	request := proof.Value
	request.JoiningNode = enrollment.JoiningNode{NodeID: info.Target.NodeID, CSR: info.CSR, PublicKeyThumbprint: info.PublicKeyThumbprint, Endpoint: info.Endpoint.HTTP}
	return r.issueEnrollment(ctx, request)
}

func applyRemoteMembership(ctx context.Context, nc *nats.Conn, nodeID string, change membership.Change) error {
	deadline, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	for {
		attempt, stop := context.WithTimeout(deadline, 2*time.Second)
		err := cluster.RequestJSON(attempt, nc, "host."+nodeID+".membership", change, nil)
		stop()
		if err == nil {
			return nil
		}
		if !errors.Is(err, nats.ErrNoResponders) && !errors.Is(err, nats.ErrTimeout) && !errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		select {
		case <-deadline.Done():
			return deadline.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func (r *Runtime) requestPromotion(ctx context.Context, execution *task.Execution) (domain.Task, error) {
	svc, err := r.Services()
	if err != nil {
		return domain.Task{}, err
	}
	coordinator := ""
	if saved, ok := execution.Task().Context["promotionCoordinator"]; ok {
		if err = json.Unmarshal(saved, &coordinator); err != nil {
			return domain.Task{}, err
		}
	} else {
		members, err := svc.Members.All(ctx)
		if err != nil {
			return domain.Task{}, err
		}
		for _, record := range members {
			if record.Value.Status == "full-stable" {
				coordinator = record.Key
				break
			}
		}
		if coordinator == "" {
			for _, record := range members {
				if record.Value.Status == "full" && record.Key != execution.Task().HostID {
					coordinator = record.Key
					break
				}
			}
		}
		if coordinator == "" {
			return domain.Task{}, errors.New("no full coordinator available")
		}
		if err = execution.Checkpoint(ctx, "promotionCoordinator", coordinator); err != nil {
			return domain.Task{}, err
		}
	}
	deadline, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var result domain.Task
	err = cluster.RequestJSON(deadline, r.client, "membership."+coordinator+".request."+execution.Task().HostID, membership.Input{NodeID: execution.Task().HostID, ToStatus: "full", DeploymentTaskID: execution.Task().ID}, &result)
	return result, err
}

func inspectMember(ctx context.Context, nc *nats.Conn, nodeID string) (membership.Info, error) {
	var info membership.Info
	err := host.Observe(ctx, nc, nodeID, host.ObservationRequest{Kind: "runtime"}, &info)
	if err == nil && info.Target.NodeID != nodeID {
		err = errors.New("observed another node")
	}
	return info, err
}
