package comms

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"testing"
	"time"

	"github.com/nats-io/nats.go"

	"nixmox/internal/domain"
	"nixmox/internal/engine/internal/shared"
)

func testTLS(t *testing.T) (*tls.Config, *tls.Config, domain.Digest, *tls.Config) {
	t.Helper()
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, ca, ca, pub, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	roots := x509.NewCertPool()
	roots.AddCert(cert)
	issue := func(serial int64) (tls.Certificate, domain.Digest) {
		p, k, _ := ed25519.GenerateKey(rand.Reader)
		leaf := &x509.Certificate{SerialNumber: big.NewInt(serial), DNSNames: []string{"localhost"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, NotBefore: ca.NotBefore, NotAfter: ca.NotAfter, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}}
		raw, e := x509.CreateCertificate(rand.Reader, leaf, cert, p, key)
		if e != nil {
			t.Fatal(e)
		}
		priv, _ := x509.MarshalPKCS8PrivateKey(k)
		pair, e := tls.X509KeyPair(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: raw}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: priv}))
		if e != nil {
			t.Fatal(e)
		}
		parsed, _ := x509.ParseCertificate(raw)
		return pair, shared.Fingerprint(parsed.RawSubjectPublicKeyInfo)
	}
	serverPair, _ := issue(2)
	clientPair, fingerprint := issue(3)
	rogue, _ := issue(4)
	serverTLS := &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{serverPair}, ClientCAs: roots, RootCAs: roots, ClientAuth: tls.RequireAndVerifyClientCert}
	clientTLS := &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{clientPair}, RootCAs: roots, ServerName: "localhost"}
	rogueTLS := clientTLS.Clone()
	rogueTLS.Certificates = []tls.Certificate{rogue}
	return serverTLS, clientTLS, fingerprint, rogueTLS
}

func brokerFixture(t *testing.T) (*nats.Conn, *JetStream, string, *tls.Config) {
	t.Helper()
	st, ct, key, rogue := testTLS(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	broker, err := StartBroker(context.Background(), BrokerOptions{NodeID: "node", ClusterID: "cluster", DataDir: t.TempDir(), ClientAddress: address, ClientTLS: st, DaemonKeys: []domain.Digest{key}, MaxStoreBytes: 128 << 20})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(broker.Close)
	nc, err := ConnectBroker("tls://"+address, "node", ct)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(nc.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	j, err := OpenJetStream(ctx, nc, "cluster", "node", StorageLimits{Commands: 4 << 20, Results: 4 << 20, State: 4 << 20, Objects: 8 << 20, Replicas: 1})
	if err != nil {
		t.Fatal(err)
	}
	return nc, j, address, rogue
}

func TestJetStreamDurability(t *testing.T) {
	nc, j, _, _ := brokerFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cluster := domain.ID("cluster")
	e := domain.Envelope{SchemaVersion: 1, Scope: domain.ClusterScope, ClusterID: &cluster, OwnerNodeID: "node", Command: domain.Command{ID: "cmd", Type: "example", TargetID: "node", Payload: []byte(`{}`)}, ExpiresAt: time.Now().Add(time.Minute)}
	if err := j.Submit(ctx, e); err != nil {
		t.Fatal(err)
	}
	sub, err := j.Commands(ctx, "node")
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	delivery, err := sub.Next(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if delivery.Envelope().Command.ID != "cmd" {
		t.Fatal("wrong envelope")
	}
	if err = delivery.Retry(ctx); err != nil {
		t.Fatal(err)
	}
	delivery, err = sub.Next(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = delivery.Ack(ctx); err != nil {
		t.Fatal(err)
	}
	rev, err := j.CompareAndSwap(ctx, "host.node", 0, []byte(`{"version":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = j.CompareAndSwap(ctx, "host.node", 0, []byte(`{}`)); err == nil {
		t.Fatal("CAS create overwrote state")
	}
	if _, err = j.CompareAndSwap(ctx, "host.node", rev, []byte(`{"version":2}`)); err != nil {
		t.Fatal(err)
	}
	watch, err := j.Watch(ctx, "host.*")
	if err != nil {
		t.Fatal(err)
	}
	defer watch.Close()
	if e, err := watch.Next(ctx); err != nil || e.Key != "host.node" {
		t.Fatalf("snapshot: %+v %v", e, err)
	}
	if e, err := watch.Next(ctx); err != nil || !e.SnapshotComplete {
		t.Fatalf("watch boundary: %+v %v", e, err)
	}
	data := []byte("immutable recipe version fixture")
	digest := sha256.Sum256(data)
	object := domain.StoredObject{ObjectKey: "recipe/1", SHA256: domain.Digest(hex.EncodeToString(digest[:])), Bytes: int64(len(data))}
	if err = j.Put(ctx, object, bytes.NewReader(data)); err != nil {
		t.Fatal(err)
	}
	r, err := j.Open(ctx, object)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(r)
	r.Close()
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("object: %q %v", got, err)
	}
	if err = j.Put(ctx, object, bytes.NewReader([]byte("bad"))); err == nil {
		t.Fatal("bad digest accepted")
	}
	// Opening storage is safe across reconnects/restarts and must preserve policy.
	if _, err = OpenJetStream(ctx, nc, "cluster", "node", StorageLimits{Commands: 4 << 20, Results: 4 << 20, State: 4 << 20, Objects: 8 << 20, Replicas: 1}); err != nil {
		t.Fatal(err)
	}
	q, err := j.Quorum(ctx)
	if err != nil || !q.Available {
		t.Fatalf("quorum: %+v %v", q, err)
	}
}

func TestBrokerRejectsUnapprovedKeysAndPeerWrites(t *testing.T) {
	nc, _, address, rogue := brokerFixture(t)
	if conn, err := ConnectBroker("tls://"+address, "rogue", rogue); err == nil {
		conn.Close()
		t.Fatal("unapproved CA-signed key connected")
	}
	anonymous := rogue.Clone()
	anonymous.Certificates = nil
	if conn, err := nats.Connect("tls://"+address, nats.Secure(anonymous), nats.TLSHandshakeFirst(), nats.Timeout(time.Second)); err == nil {
		conn.Close()
		t.Fatal("anonymous client connected")
	}
	failures := make(chan error, 1)
	nc.SetErrorHandler(func(_ *nats.Conn, _ *nats.Subscription, err error) {
		select {
		case failures <- err:
		default:
		}
	})
	if err := nc.Publish(Subject("cluster", "log", "another")+".task", []byte("forged")); err != nil {
		t.Fatal(err)
	}
	nc.Flush()
	select {
	case <-failures:
	case <-time.After(time.Second):
		t.Fatal("cross-owner publication allowed")
	}
	if shared.Token("a.b") == shared.Token("a_b") {
		t.Fatal("subject collision")
	}
}
