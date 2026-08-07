package cluster

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

var (
	ErrNotClustered        = errors.New("node is not in a cluster")
	ErrClusterNameMismatch = errors.New("cluster name mismatch")
)

type JoinRequest struct {
	NodeID            string `json:"node_id"`
	Hostname          string `json:"hostname"`
	ClusterAddr       string `json:"cluster_addr"`
	TargetClusterName string `json:"target_cluster_name"`
}

type JoinResponse struct {
	ClusterName string   `json:"cluster_name"`
	KnownPeers  []string `json:"known_peers"`
}

func normalizeHTTPAddr(addr string) string {
	addr = strings.TrimPrefix(strings.TrimPrefix(addr, "http://"), "https://")
	return "http://" + strings.TrimSuffix(addr, "/")
}

func FetchBootstrap(ctx context.Context, httpAddr string) (ClusterState, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", normalizeHTTPAddr(httpAddr)+"/api/v1/bootstrap", nil)
	if err != nil {
		return ClusterState{}, err
	}
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return ClusterState{}, err
	}
	defer resp.Body.Close()
	var st ClusterState
	return st, json.NewDecoder(resp.Body).Decode(&st)
}

func RequestJoin(ctx context.Context, httpAddr string, joinReq JoinRequest) (JoinResponse, error) {
	body, err := json.Marshal(joinReq)
	if err != nil {
		return JoinResponse{}, err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", normalizeHTTPAddr(httpAddr)+"/api/v1/cluster/join", bytes.NewReader(body))
	if err != nil {
		return JoinResponse{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return JoinResponse{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusConflict {
		return JoinResponse{}, ErrClusterNameMismatch
	}
	if resp.StatusCode != http.StatusOK {
		return JoinResponse{}, fmt.Errorf("join rejected: %s", resp.Status)
	}
	var jr JoinResponse
	return jr, json.NewDecoder(resp.Body).Decode(&jr)
}
