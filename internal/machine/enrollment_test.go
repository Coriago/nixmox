package machine

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nixmox/internal/domain"
	"nixmox/internal/pki"
	"nixmox/internal/storage"
)

func TestEnrollmentClientPinsServerAndRejectsKeyChange(t *testing.T) {
	serverKeys, clientKeys, otherKeys := keys(t), keys(t), keys(t)
	server := httptest.NewUnstartedServer(New(&ownerEnrollment{}))
	server.TLS = pki.EnrollmentServer(serverKeys.Daemon)
	server.StartTLS()
	defer server.Close()
	address := strings.TrimPrefix(server.URL, "https://")
	cert, err := Probe(context.Background(), address, clientKeys.Daemon)
	if err != nil {
		t.Fatal(err)
	}
	if pki.Fingerprint(cert.PublicKey) != pki.Fingerprint(serverKeys.Daemon.Leaf.PublicKey) {
		t.Fatal("probe did not observe actual server")
	}
	client, err := EnrollmentClient(address, clientKeys.Daemon, pki.Fingerprint(otherKeys.Daemon.Leaf.PublicKey))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	_, err = client.Read(context.Background(), "join_test")
	var problem *domain.Error
	if !errors.As(err, &problem) || problem.Code != "IDENTITY_CONFLICT" {
		t.Fatal("changed server key was accepted", err)
	}
}

type ownerEnrollment struct{ owner string }

func (e *ownerEnrollment) SubmitEnrollment(owner string, r domain.EnrollmentRequest) (any, error) {
	e.owner = owner
	return map[string]string{"owner": owner}, nil
}
func (e *ownerEnrollment) Read(owner, id string) (any, error) {
	return map[string]string{"owner": owner}, nil
}
func (e *ownerEnrollment) Certificates(owner, id string) (any, error) {
	return map[string]string{"owner": owner}, nil
}
func keys(t *testing.T) pki.Keys {
	t.Helper()
	s, err := storage.Open(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	k, err := pki.Open(s)
	if err != nil {
		t.Fatal(err)
	}
	return k
}
func TestMachineTLSRestrictsBootstrapToEnrollment(t *testing.T) {
	serverKeys, clientKeys := keys(t), keys(t)
	e := &ownerEnrollment{}
	s := httptest.NewUnstartedServer(New(e))
	s.TLS = pki.EnrollmentServer(serverKeys.Daemon)
	s.StartTLS()
	defer s.Close()
	config := pki.MutualClient(clientKeys.Daemon, pki.Pool(serverKeys.Daemon.Leaf), serverKeys.Daemon.Leaf.DNSNames[0], pki.OnlyKey(serverKeys.Daemon.Leaf))
	c := &http.Client{Transport: &http.Transport{TLSClientConfig: config}, Timeout: time.Second}
	defer c.CloseIdleConnections()
	r, err := c.Post(s.URL+"/api/v1/enrollment/requests", "application/json", strings.NewReader(`{"id":"join_test"}`))
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, r.Body)
	r.Body.Close()
	if r.StatusCode != 200 || e.owner != pki.Fingerprint(clientKeys.Daemon.Leaf.PublicKey) {
		t.Fatal("owner not bound to TLS key", r.StatusCode)
	}
	for _, route := range []string{"/api/v1/commands", "/api/v1/hosts", "/api/v1/cluster/requests", "/store-cache/nix-cache-info"} {
		r, err = c.Get(s.URL + route)
		if err != nil {
			t.Fatal(err)
		}
		r.Body.Close()
		if r.StatusCode != 404 {
			t.Fatal("bootstrap gained non-enrollment access", route, r.StatusCode)
		}
	}
	noCertificate := config.Clone()
	noCertificate.Certificates = []tls.Certificate{}
	noCertificate.GetClientCertificate = nil
	anonymous := &http.Client{Transport: &http.Transport{TLSClientConfig: noCertificate}, Timeout: time.Second}
	defer anonymous.CloseIdleConnections()
	r, err = anonymous.Get(s.URL + "/api/v1/enrollment/requests/join_test")
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if r.StatusCode != 401 {
		t.Fatal("anonymous enrollment", r.StatusCode)
	}
}
