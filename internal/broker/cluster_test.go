package broker

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"net"
	"net/url"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"nixmox/internal/pki"
	"nixmox/internal/storage"
)

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}
func issued(t *testing.T, a *pki.Authority, pair tls.Certificate, node, role string) tls.Certificate {
	t.Helper()
	csr, err := pki.CSR(pair)
	if err != nil {
		t.Fatal(err)
	}
	request, err := pki.ParseCSR(csr)
	if err != nil {
		t.Fatal(err)
	}
	b, err := a.Issue(request, node, role, 1)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(b)
	pair.Certificate = [][]byte{block.Bytes}
	pair.Leaf, err = x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return pair
}
func TestThreeBrokerQuorumFailureRecoveryAndRouteAllowlist(t *testing.T) {
	st, err := storage.Open(filepath.Join(t.TempDir(), "issuer"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	a, err := pki.OpenAuthority(st, "c_cluster", true)
	if err != nil {
		t.Fatal(err)
	}
	configs := []Config{node(t), node(t), node(t), node(t)}
	allowed := map[string]bool{}
	for i := range configs {
		c := &configs[i]
		c.ClusterID = "c_cluster"
		c.Roots = pki.Pool(a.Certificate)
		c.ControlPlane = true
		c.RoutePort = freePort(t)
		c.Keys.Daemon = issued(t, a, c.Keys.Daemon, c.NodeID, "daemon")
		c.Keys.Broker = issued(t, a, c.Keys.Broker, c.NodeID, "broker")
		if i < 3 {
			allowed[pki.Fingerprint(c.Keys.Broker.Leaf.PublicKey)] = true
		}
	}
	for i := range configs {
		configs[i].FullBrokers = allowed
		for j := 0; j < 3; j++ {
			if i == j {
				continue
			}
			u, _ := url.Parse("nats-route://127.0.0.1:" + strconv.Itoa(configs[j].RoutePort))
			configs[i].Routes = append(configs[i].Routes, u)
		}
	}
	brokers := make([]*Broker, 4)
	defer func() {
		for _, b := range brokers {
			if b != nil {
				b.Close()
			}
		}
	}()
	for i := range brokers {
		brokers[i], err = Start(configs[i])
		if err != nil {
			t.Fatalf("start %d: %v", i, err)
		}
	}
	js, err := brokers[0].Client.JetStream(nats.MaxWait(2 * time.Second))
	if err != nil {
		t.Fatal(err)
	}
	var kv nats.KeyValue
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		kv, err = js.CreateKeyValue(&nats.KeyValueConfig{Bucket: "nx_state", Replicas: 3, MaxBytes: 16 << 20, Storage: nats.FileStorage, History: 1})
		if err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err != nil {
		t.Fatal("replicated KV unavailable", err)
	}
	revision, err := kv.Create("test", []byte("pinned"))
	if err != nil {
		t.Fatal(err)
	}
	if brokers[3].Server.NumRoutes() != 0 {
		t.Fatal("CA-signed, unadmitted broker joined routes")
	}
	brokers[1].Close()
	brokers[1] = nil
	brokers[2].Close()
	brokers[2] = nil
	if _, err = kv.Update("test", []byte("uncommitted"), revision); err == nil {
		t.Fatal("write accepted without majority")
	}
	for i := 1; i < 3; i++ {
		brokers[i], err = Start(configs[i])
		if err != nil {
			t.Fatal(err)
		}
	}
	deadline = time.Now().Add(15 * time.Second)
	var recoveredRevision uint64
	for time.Now().Before(deadline) {
		recoveredRevision, err = kv.Update("test", []byte("recovered"), revision)
		if err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err != nil {
		// A timed-out write can commit during recovery. Reconcile its value and
		// revision before issuing another CAS; never infer absence from timeout.
		entry, getErr := kv.Get("test")
		if getErr != nil || string(entry.Value()) != "uncommitted" {
			t.Fatalf("recovery: %v %v", err, getErr)
		}
		if recoveredRevision, err = kv.Update("test", []byte("recovered"), entry.Revision()); err != nil {
			t.Fatal(err)
		}
	}
	// KV direct reads may reach a recovering follower. The acknowledged CAS
	// revision is the fence; wait for that replica to observe it without issuing
	// another mutation or treating an older value as a lost committed write.
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		entry, getErr := kv.Get("test")
		if getErr == nil && entry.Revision() == recoveredRevision && string(entry.Value()) == "recovered" {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("replica did not observe committed revision %d", recoveredRevision)
}
