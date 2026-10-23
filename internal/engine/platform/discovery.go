package platform

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"sort"
	"sync"
	"time"

	"golang.org/x/net/ipv4"
	"golang.org/x/sync/errgroup"
	"golang.org/x/time/rate"

	"nixmox/internal/domain"
)

var discoveryGroup = net.IPv4(239, 255, 63, 174)

type discoveryPacket struct {
	SchemaVersion int                `json:"schema_version"`
	Search        bool               `json:"search"`
	Records       []domain.Discovery `json:"records"`
}
type discoverySocket struct {
	connection *ipv4.PacketConn
	iface      net.Interface
}
type Discovery struct {
	mu       sync.Mutex
	sockets  []discoverySocket
	address  *net.UDPAddr
	records  map[domain.ID]domain.Discovery
	announce func() []domain.Discovery
	limit    *rate.Limiter
}

// OpenDiscovery uses the standard UDP socket and x/net multicast controls.
// Empty management interfaces means loopback only, matching the HTTP boundary.
func OpenDiscovery(port int, names []string, announce func() []domain.Discovery) (d *Discovery, err error) {
	d = &Discovery{address: &net.UDPAddr{IP: discoveryGroup, Port: port}, records: map[domain.ID]domain.Discovery{}, announce: announce, limit: rate.NewLimiter(25, 50)}
	opened := d
	defer func() {
		if err != nil {
			opened.Close()
		}
	}()
	if len(names) == 0 {
		interfaces, err := net.Interfaces()
		if err != nil {
			return nil, err
		}
		for _, iface := range interfaces {
			if iface.Flags&net.FlagLoopback != 0 {
				names = append(names, iface.Name)
			}
		}
	}
	for _, name := range names {
		iface, err := net.InterfaceByName(name)
		if err != nil {
			return nil, err
		}
		connection, err := net.ListenMulticastUDP("udp4", iface, d.address)
		if err != nil {
			return nil, err
		}
		packet := ipv4.NewPacketConn(connection)
		d.sockets = append(d.sockets, discoverySocket{packet, *iface})
		for _, configure := range []func() error{
			func() error { return packet.SetMulticastInterface(iface) },
			func() error { return packet.SetMulticastLoopback(true) },
			func() error { return packet.SetMulticastTTL(1) },
			func() error { return packet.SetControlMessage(ipv4.FlagInterface|ipv4.FlagDst, true) },
		} {
			if err = configure(); err != nil {
				return nil, err
			}
		}
	}
	if len(d.sockets) == 0 {
		return nil, errors.New("no allowed discovery interface")
	}
	return d, nil
}
func (d *Discovery) Close() {
	for _, socket := range d.sockets {
		socket.connection.Close()
	}
}
func (d *Discovery) send(search bool) error {
	records := d.announce()
	var result error
	for _, socket := range d.sockets {
		packet := discoveryPacket{SchemaVersion: 1, Search: search}
		for _, record := range records {
			record.ExpiresAt = time.Now().UTC().Add(domain.DiscoveryTTL)
			record.Endpoints = append([]domain.Endpoint(nil), record.Endpoints...)
			filtered := record.Endpoints[:0]
			for _, endpoint := range record.Endpoints {
				host, _, err := net.SplitHostPort(endpoint.Address)
				if err != nil {
					continue
				}
				if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() && socket.iface.Flags&net.FlagLoopback == 0 {
					continue
				}
				filtered = append(filtered, endpoint)
			}
			record.Endpoints = filtered
			if len(filtered) > 0 {
				packet.Records = append(packet.Records, record)
			}
		}
		data, err := json.Marshal(packet)
		if err != nil {
			return err
		}
		if len(data) > 8192 {
			return errors.New("discovery announcement exceeds size limit")
		}
		_, err = socket.connection.WriteTo(data, nil, d.address)
		result = errors.Join(result, err)
	}
	return result
}
func (d *Discovery) Run(ctx context.Context) error {
	group, life := errgroup.WithContext(ctx)
	stop := context.AfterFunc(life, d.Close)
	defer stop()
	for _, socket := range d.sockets {
		group.Go(func() error {
			buffer := make([]byte, 8193)
			for {
				n, control, _, err := socket.connection.ReadFrom(buffer)
				if err != nil {
					if life.Err() != nil {
						return life.Err()
					}
					return err
				}
				if n > 8192 || control == nil || control.IfIndex != socket.iface.Index || !control.Dst.Equal(discoveryGroup) || !d.limit.Allow() {
					continue
				}
				var packet discoveryPacket
				if json.Unmarshal(buffer[:n], &packet) != nil || packet.SchemaVersion != 1 || len(packet.Records) > 4 {
					continue
				}
				now := time.Now().UTC()
				d.mu.Lock()
				for id, record := range d.records {
					if now.After(record.ExpiresAt) {
						delete(d.records, id)
					}
				}
				for _, record := range packet.Records {
					if record.ID == "" || len(record.ID) > 256 || len(record.Name) > 128 || len(record.Endpoints) > 16 || (record.Kind != "node" && record.Kind != "cluster") {
						continue
					}
					if len(d.records) >= 256 {
						if _, known := d.records[record.ID]; !known {
							continue
						}
					}
					record.ExpiresAt = now.Add(domain.DiscoveryTTL)
					for i := range record.Endpoints {
						record.Endpoints[i].Verified = false
					}
					d.records[record.ID] = record
				}
				d.mu.Unlock()
				if packet.Search {
					_ = d.send(false)
				}
			}
		})
	}
	group.Go(func() error {
		ticker := time.NewTicker(domain.DiscoveryInterval)
		defer ticker.Stop()
		for {
			_ = d.send(false) // LAN hints are best effort, never durable admission.
			select {
			case <-life.Done():
				return life.Err()
			case <-ticker.C:
			}
		}
	})
	return group.Wait()
}
func (d *Discovery) Snapshot() []domain.Discovery {
	d.mu.Lock()
	defer d.mu.Unlock()
	items := []domain.Discovery{}
	for _, record := range d.records {
		if time.Now().Before(record.ExpiresAt) {
			items = append(items, record)
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	return items
}
func (d *Discovery) Search(ctx context.Context) ([]domain.Discovery, error) {
	if err := d.send(true); err != nil {
		return nil, err
	}
	timer := time.NewTimer(domain.DiscoverySearchWindow)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timer.C:
		return d.Snapshot(), nil
	}
}
