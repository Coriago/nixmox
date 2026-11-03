package discovery

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"time"

	"nixmox/internal/util"
)

// Serve exchanges bounded JSON datagrams on each active IPv4 broadcast LAN.
// It owns conn until cancellation and never uses advertised endpoints for traffic.
func (s *Service) Serve(ctx context.Context, conn *net.UDPConn, advertise func(context.Context, []net.IP) (Advertisement, error)) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	publisherDone := make(chan struct{})
	go func() {
		defer close(publisherDone)
		ticker := time.NewTicker(3 * time.Second)
		defer ticker.Stop()
		for {
			send(ctx, conn, advertise)
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	// Closing the listener must also stop its publisher if a read fails.
	defer func() { cancel(); conn.Close(); <-publisherDone }()
	var lastWarning time.Time
	buffer := make([]byte, MaxPacket+1)
	for {
		n, _, err := conn.ReadFromUDP(buffer)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		if n > MaxPacket {
			continue
		}
		var advertisement Advertisement
		if json.Unmarshal(buffer[:n], &advertisement) != nil || advertisement.Validate() != nil {
			continue
		}
		operation, cancel := context.WithTimeout(ctx, time.Second)
		err = s.Observe(operation, advertisement)
		cancel()
		if err != nil && ctx.Err() != nil {
			return nil
		}
		if err != nil && time.Since(lastWarning) > 30*time.Second {
			util.From(ctx).Warn("cannot retain discovery advertisement", "error", util.Error(err))
			lastWarning = time.Now()
		}
		// Invalid/flooding peers and unavailable shared storage cannot kill the daemon.
	}
}

func send(ctx context.Context, conn *net.UDPConn, advertise func(context.Context, []net.IP) (Advertisement, error)) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return
	}
	var ips []net.IP
	var broadcasts []net.IP
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagBroadcast == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addresses, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, address := range addresses {
			network, ok := address.(*net.IPNet)
			if !ok || network.IP.To4() == nil || !network.IP.IsGlobalUnicast() {
				continue
			}
			bits, total := network.Mask.Size()
			if total != 32 || bits > 30 || len(ips) >= 16 {
				continue
			}
			ip := network.IP.To4()
			broadcast := append(net.IP(nil), ip...)
			for i := range broadcast {
				broadcast[i] |= ^network.Mask[i]
			}
			ips = append(ips, ip)
			broadcasts = append(broadcasts, broadcast)
		}
	}
	operation, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	advertisement, err := advertise(operation, ips)
	if err != nil || advertisement.Validate() != nil {
		return
	}
	data, err := json.Marshal(advertisement)
	if err != nil || len(data) > MaxPacket {
		return
	}
	port := conn.LocalAddr().(*net.UDPAddr).Port
	if err = conn.SetWriteDeadline(time.Now().Add(time.Second)); err != nil {
		return
	}
	for _, broadcast := range broadcasts {
		_, err = conn.WriteToUDP(data, &net.UDPAddr{IP: broadcast, Port: port})
		if errors.Is(err, net.ErrClosed) {
			return
		}
	}
}
