package cluster

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
	"nixmox/internal/hostinfo"
	"nixmox/internal/natsx"
)

const telemetryEvery = 2 * time.Second
const telemetryPrefix = "nixmox.telemetry."

type telemetryPacket struct {
	Info      hostinfo.HostInfo `json:"info"`
	Inventory bool              `json:"inventory"`
}

type telemetrySample struct {
	info     hostinfo.HostInfo
	observed time.Time
}

// Telemetry is replaceable live data, not durable command history. It does not
// refresh membership liveness: only the host KV heartbeat can do that.
func (v *View) updateTelemetry(packet telemetryPacket) {
	info := packet.Info
	v.mu.Lock()
	defer v.mu.Unlock()
	host, known := v.hosts[info.Hostname]
	if !known {
		return
	}
	if v.telemetry == nil {
		v.telemetry = map[string]telemetrySample{}
	}
	if !packet.Inventory {
		base := host.Resources
		if previous, ok := v.telemetry[info.Hostname]; ok && !previous.info.Resources.InventoryAt.Before(base.InventoryAt) {
			base = previous.info.Resources
		}
		info.Resources = hostinfo.MergeActivity(base, info.Resources)
	}
	v.telemetry[info.Hostname] = telemetrySample{info, time.Now()}
	if v.Changed != nil {
		v.Changed()
	}
}

func (rt *Runtime) runTelemetry(ctx context.Context, emb *natsx.EmbeddedServer) {
	if rt.Telemetry == nil {
		return
	}
	sub, err := emb.NC.Subscribe(telemetryPrefix+">", func(msg *nats.Msg) {
		var packet telemetryPacket
		if json.Unmarshal(msg.Data, &packet) == nil && packet.Info.Hostname != "" &&
			strings.TrimPrefix(msg.Subject, telemetryPrefix) == hex.EncodeToString([]byte(packet.Info.Hostname)) {
			rt.View.updateTelemetry(packet)
		}
	})
	if err != nil {
		return
	}
	defer sub.Unsubscribe()
	tick := time.NewTicker(telemetryEvery)
	defer tick.Stop()
	var inventoryAt time.Time
	for {
		info := rt.Telemetry()
		inventory := info.Resources.InventoryAt != inventoryAt
		inventoryAt = info.Resources.InventoryAt
		if !inventory {
			info.Resources = hostinfo.ActivityOnly(info.Resources)
		}
		if data, err := json.Marshal(telemetryPacket{Info: info, Inventory: inventory}); err == nil {
			emb.NC.Publish(telemetryPrefix+hex.EncodeToString([]byte(info.Hostname)), data)
		}
		if rt.TelemetryChanged != nil {
			rt.TelemetryChanged()
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}
