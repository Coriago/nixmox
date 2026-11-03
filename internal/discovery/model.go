// Package discovery exchanges untrusted LAN hints; enrollment must verify the
// advertised public-key thumbprint before granting membership or installing trust.
package discovery

import (
	"encoding/hex"
	"errors"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	DefaultPort = 47890
	Lifetime    = time.Minute
	MaxPacket   = 8192
)

type Identity struct {
	Kind                string `json:"kind"`
	PublicKeyThumbprint string `json:"publicKeyThumbprint"`
}

type Advertisement struct {
	NodeID    string   `json:"nodeId"`
	ClusterID *string  `json:"clusterId"`
	Name      string   `json:"name"`
	Endpoints []string `json:"endpoints"`
	Identity  Identity `json:"identity"`
}

type Record struct {
	ID               string        `json:"id"`
	ObservedByHostID string        `json:"observedByHostId"`
	LastSeenAt       time.Time     `json:"lastSeenAt"`
	Advertisement    Advertisement `json:"advertisement"`
}

func (a Advertisement) Validate() error {
	if _, err := uuid.Parse(a.NodeID); err != nil {
		return err
	}
	kind := "node"
	if a.ClusterID != nil {
		if _, err := uuid.Parse(*a.ClusterID); err != nil {
			return err
		}
		kind = "cluster-ca"
	}
	key, err := hex.DecodeString(strings.TrimPrefix(a.Identity.PublicKeyThumbprint, "sha256:"))
	if err != nil || len(key) != 32 || !strings.HasPrefix(a.Identity.PublicKeyThumbprint, "sha256:") || a.Identity.Kind != kind {
		return errors.New("invalid discovery identity")
	}
	if a.Name == "" || len(a.Name) > 128 || len(a.Endpoints) == 0 || len(a.Endpoints) > 16 {
		return errors.New("invalid discovery advertisement")
	}
	for _, endpoint := range a.Endpoints {
		u, err := url.Parse(endpoint)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
			return errors.New("invalid discovery HTTP endpoint")
		}
		ip, err := netip.ParseAddr(u.Hostname())
		if u.Hostname() == "" || (err == nil && (!ip.IsGlobalUnicast() || ip.IsLoopback())) {
			return errors.New("invalid discovery host")
		}
	}
	return nil
}

func recordID(observer string, a Advertisement) string {
	cluster := ""
	if a.ClusterID != nil {
		cluster = *a.ClusterID
	}
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte(observer+"\x00"+a.NodeID+"\x00"+cluster+"\x00"+a.Identity.PublicKeyThumbprint)).String()
}
