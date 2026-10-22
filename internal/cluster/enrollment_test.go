package cluster

import (
	"bytes"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"nixmox/internal/config"
	"nixmox/internal/domain"
	"nixmox/internal/machine"
	"nixmox/internal/network"
	"nixmox/internal/pki"
	"nixmox/internal/service"
	"nixmox/internal/storage"
)

func nodeFixture(t *testing.T, extra ...service.Operation) (*Manager, *service.Service) {
	t.Helper()
	st, err := storage.Open(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	c := config.Default()
	c.DataDir = st.Root
	c.NATS.ClientPort = -1
	c.NATS.RoutePort = -1
	c.NATS.LeafPort = -1
	m, err := Open(st, c)
	if err != nil {
		st.Close()
		t.Fatal(err)
	}
	s, err := service.Open(st, network.Inspector{Config: c}, append(m.Operations(), extra...)...)
	if err != nil {
		m.Close()
		st.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close(); m.Close(); st.Close() })
	if err = m.Attach(s); err != nil {
		t.Fatal(err)
	}
	return m, s
}
func issuerFixture(t *testing.T, extra ...service.Operation) (*Manager, *service.Service) {
	t.Helper()
	m, s := nodeFixture(t, extra...)
	r, err := s.Admit(domain.Command{ID: "cmd_genesis", Type: "cluster.create", TargetID: s.Identity().NodeID, Payload: json.RawMessage(`{"name":"homelab"}`)})
	if err != nil {
		t.Fatal(err)
	}
	waitTask(t, s, r.TaskID, "succeeded")
	return m, s
}
func joiningKeys(t *testing.T) (domain.Identity, pki.Keys) {
	t.Helper()
	st, err := storage.Open(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	keys, err := pki.Open(st)
	if err != nil {
		t.Fatal(err)
	}
	id, err := st.Identity()
	if err != nil {
		t.Fatal(err)
	}
	return id, keys
}
func enrollmentRequest(t *testing.T, id domain.Identity, keys pki.Keys, requestID string) domain.EnrollmentRequest {
	t.Helper()
	daemon, err := pki.CSR(keys.Daemon)
	if err != nil {
		t.Fatal(err)
	}
	broker, err := pki.CSR(keys.Broker)
	if err != nil {
		t.Fatal(err)
	}
	return domain.EnrollmentRequest{ID: requestID, NodeID: id.NodeID, Name: "cedar", ExpectedClusterName: "homelab", DaemonCSR: string(daemon), BrokerCSR: string(broker)}
}
func TestEnrollmentTLSOwnerBindingApprovalAndIssuanceRetries(t *testing.T) {
	m, s := issuerFixture(t)
	server := httptest.NewUnstartedServer(machine.New(m))
	server.TLS = m.TLS()
	server.StartTLS()
	defer server.Close()
	id, keys := joiningKeys(t)
	_, stranger := joiningKeys(t)
	client := func(k pki.Keys) *http.Client {
		c := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{TLSClientConfig: pki.MutualClient(k.Daemon, pki.Pool(m.keys.Daemon.Leaf), pki.ServerName(m.keys.Daemon.Leaf), pki.OnlyKey(m.keys.Daemon.Leaf))}}
		t.Cleanup(c.CloseIdleConnections)
		return c
	}
	owner, other := client(keys), client(stranger)
	request := enrollmentRequest(t, id, keys, "join_cedar")
	post := func(c *http.Client, path string, value any) (int, []byte) {
		b, _ := json.Marshal(value)
		r, err := c.Post(server.URL+path, "application/json", bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		defer r.Body.Close()
		data, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		return r.StatusCode, data
	}
	status, data := post(owner, "/api/v1/enrollment/requests", request)
	if status != 200 {
		t.Fatalf("submit: %d %s", status, data)
	}
	var view domain.JoinRequest
	if json.Unmarshal(data, &view) != nil || view.State != "awaiting_approval" || view.Version != 1 {
		t.Fatal(string(data))
	}
	status, duplicate := post(owner, "/api/v1/enrollment/requests", request)
	if status != 200 || !bytes.Equal(data, duplicate) {
		t.Fatalf("request retry changed binding: %s %s", data, duplicate)
	}
	r, err := other.Get(server.URL + "/api/v1/enrollment/requests/" + request.ID)
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if r.StatusCode != 404 {
		t.Fatal("foreign owner read request", r.StatusCode)
	}
	if status, _ = post(other, "/api/v1/enrollment/requests/"+request.ID+"/certificates", map[string]any{}); status != 404 {
		t.Fatal("foreign owner fetched issuance", status)
	}
	if status, _ = post(other, "/api/v1/enrollment/requests", request); status != 401 {
		t.Fatal("CSR key did not match TLS owner", status)
	}
	changed := request
	changed.Name = "another"
	if status, _ = post(owner, "/api/v1/enrollment/requests", changed); status != 409 {
		t.Fatal("immutable request binding changed", status)
	}
	payload, _ := json.Marshal(decisionPayload{RequestID: request.ID})
	bad := domain.Command{ID: "cmd_invalid_secret", Type: "cluster.request.approve", TargetID: s.Identity().NodeID, ExpectedVersions: map[string]int64{"join_request:" + request.ID: 1}, Payload: json.RawMessage(`{"request_id":"join_cedar","password":"never-store-this"}`)}
	if _, err = s.Admit(bad); err == nil {
		t.Fatal("untyped secret accepted")
	}
	if _, err = m.stores.State.Get("command." + bad.ID); !errors.Is(err, nats.ErrKeyNotFound) {
		t.Fatal("invalid payload persisted", err)
	}
	cmd := domain.Command{ID: "cmd_approve", Type: "cluster.request.approve", TargetID: s.Identity().NodeID, ExpectedVersions: map[string]int64{"join_request:" + request.ID: view.Version}, Payload: payload}
	receipt, err := s.Admit(cmd)
	if err != nil {
		t.Fatal(err)
	}
	waitTask(t, s, receipt.TaskID, "succeeded")
	status, data = post(owner, "/api/v1/enrollment/requests/"+request.ID+"/certificates", map[string]any{})
	if status != 200 {
		t.Fatalf("issuance: %d %s", status, data)
	}
	var issued domain.Issuance
	if json.Unmarshal(data, &issued) != nil {
		t.Fatal(string(data))
	}
	status, duplicate = post(owner, "/api/v1/enrollment/requests/"+request.ID+"/certificates", map[string]any{})
	if status != 200 || !bytes.Equal(data, duplicate) {
		t.Fatal("issuance retry regenerated certificates")
	}
	caBlock, _ := pem.Decode([]byte(issued.CA))
	ca, err := parseCA(caBlock)
	if err != nil {
		t.Fatal(err)
	}
	for role, pair := range map[string]struct {
		pem string
		key string
	}{"daemon": {issued.DaemonCertificate, pki.Fingerprint(keys.Daemon.Leaf.PublicKey)}, "broker": {issued.BrokerCertificate, pki.Fingerprint(keys.Broker.Leaf.PublicKey)}} {
		block, _ := pem.Decode([]byte(pair.pem))
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			t.Fatal(err)
		}
		if !pki.Bound(cert, issued.ClusterID, id.NodeID, role, 1) || pki.Fingerprint(cert.PublicKey) != pair.key {
			t.Fatal("wrong issued identity")
		}
		if _, err = cert.Verify(x509.VerifyOptions{Roots: pki.Pool(ca), DNSName: pki.ServerName(cert)}); err != nil {
			t.Fatal(err)
		}
	}
	if replay, err := s.Admit(cmd); err != nil || replay.TaskID != receipt.TaskID {
		t.Fatal("approval replay lost receipt", replay, err)
	}
	if status, _ = post(other, "/api/v1/enrollment/requests/"+request.ID+"/certificates", map[string]any{}); status != 404 {
		t.Fatal("approved request leaked credentials", status)
	}
	s.Close()
	m.Close()
	restarted, err := Open(m.store, m.config)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	recovered, err := restarted.Certificates(pki.Fingerprint(keys.Daemon.Leaf.PublicKey), request.ID)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(recovered)
	if !bytes.Equal(bytes.TrimSpace(data), encoded) {
		t.Fatal("issuer restart changed persisted issuance")
	}
}

func TestRejectedAndExpiredRequestsCannotBeRevived(t *testing.T) {
	m, s := issuerFixture(t)
	for _, state := range []string{"rejected", "expired"} {
		id, keys := joiningKeys(t)
		owner := pki.Fingerprint(keys.Daemon.Leaf.PublicKey)
		request := enrollmentRequest(t, id, keys, "join_"+state)
		if _, err := m.SubmitEnrollment(owner, request); err != nil {
			t.Fatal(err)
		}
		payload, _ := json.Marshal(decisionPayload{RequestID: request.ID})
		if state == "rejected" {
			r, err := s.Admit(domain.Command{ID: "cmd_reject", Type: "cluster.request.reject", TargetID: s.Identity().NodeID, Payload: payload, ExpectedVersions: map[string]int64{"join_request:" + request.ID: 1}})
			if err != nil {
				t.Fatal(err)
			}
			waitTask(t, s, r.TaskID, "succeeded")
		} else {
			record, revision, err := readJoin(m.stores.State, request.ID)
			if err != nil {
				t.Fatal(err)
			}
			record.View.ExpiresAt = time.Now().Add(-time.Second)
			data, _ := json.Marshal(record)
			if _, err = m.stores.State.Update("join."+request.ID, data, revision); err != nil {
				t.Fatal(err)
			}
		}
		replayed, err := m.SubmitEnrollment(owner, request)
		if err != nil || replayed.(domain.JoinRequest).State != state {
			t.Fatal("request revived", replayed, err)
		}
		view, err := m.JoinRequest(request.ID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = s.Admit(domain.Command{ID: "cmd_approve_" + state, Type: "cluster.request.approve", TargetID: s.Identity().NodeID, Payload: payload, ExpectedVersions: map[string]int64{"join_request:" + request.ID: view.Version}}); err == nil {
			t.Fatal("inactive request approved")
		}
		if _, err = m.Certificates(owner, request.ID); err == nil {
			t.Fatal("inactive request got credentials")
		}
	}
}
