package broker

import (
	"context"
	"crypto/tls"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"nixmox/internal/pki"
	"nixmox/internal/storage"
)

func node(t *testing.T) Config {
	t.Helper()
	st, err := storage.Open(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	keys, err := pki.Open(st)
	if err != nil {
		t.Fatal(err)
	}
	id, err := st.Identity()
	if err != nil {
		t.Fatal(err)
	}
	return Config{NodeID: id.NodeID, DataDir: st.Root, ClientPort: -1, RoutePort: -1, LeafPort: -1, Keys: keys}
}
func TestStandaloneRequiresLocalCertificateAndSubjectGrants(t *testing.T) {
	c := node(t)
	b, err := Start(c)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if err = b.Client.FlushTimeout(time.Second); err != nil {
		t.Fatal(err)
	}
	// No plaintext protocol or anonymous loopback access.
	plain, err := net.DialTimeout("tcp", b.Server.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	plain.SetDeadline(time.Now().Add(200 * time.Millisecond))
	plain.Write([]byte("CONNECT {}\r\nPING\r\n"))
	buf := make([]byte, 1024)
	n, _ := plain.Read(buf)
	plain.Close()
	if strings.Contains(string(buf[:n]), "INFO") || strings.Contains(string(buf[:n]), "PONG") {
		t.Fatal("plaintext broker access")
	}
	for name, config := range map[string]*tls.Config{"anonymous": b.ClientTLS(), "foreign": b.ClientTLS()} {
		config.GetClientCertificate = nil
		if name == "anonymous" {
			config.Certificates = nil
		} else {
			config.Certificates = []tls.Certificate{node(t).Keys.Daemon}
		}
		conn, err := nats.Connect(b.URL(), nats.Secure(config), nats.TLSHandshakeFirst(), nats.Timeout(time.Second), nats.NoReconnect())
		if err == nil {
			conn.Close()
			t.Fatalf("%s client admitted", name)
		}
	}
	denied := make(chan error, 4)
	nc, err := nats.Connect(b.URL(), nats.Secure(b.ClientTLS()), nats.TLSHandshakeFirst(), nats.CustomInboxPrefix("_INBOX."+c.NodeID), nats.NoReconnect(), nats.ErrorHandler(func(_ *nats.Conn, _ *nats.Subscription, err error) { denied <- err }))
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	sub, err := nc.SubscribeSync("nx.local." + c.NodeID + ".test")
	if err != nil {
		t.Fatal(err)
	}
	nc.FlushTimeout(time.Second)
	nc.Publish("nx.local."+c.NodeID+".test", []byte("observed"))
	nc.FlushTimeout(time.Second)
	msg, err := sub.NextMsg(time.Second)
	if err != nil || string(msg.Data) != "observed" {
		t.Fatal(msg, err)
	}
	for _, subject := range []string{"$JS.API.INFO", "$SYS.REQ.SERVER.PING", "nx.other.command.node"} {
		nc.Publish(subject, []byte("{}"))
		nc.FlushTimeout(time.Second)
		select {
		case <-denied:
		case <-time.After(time.Second):
			t.Fatalf("unauthorized publish %s was not rejected", subject)
		}
	}
	status, statusErr := b.Server.Varz(nil)
	if statusErr != nil {
		t.Fatal(statusErr)
	}
	for _, addr := range []string{b.Server.ClusterAddr().String(), net.JoinHostPort("127.0.0.1", strconv.Itoa(status.LeafNode.Port))} {
		// A daemon certificate must not double as a broker route/leaf credential.
		config := b.ClientTLS()
		d := tls.Dialer{Config: config, NetDialer: &net.Dialer{Timeout: time.Second}}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		conn, err := d.DialContext(ctx, "tcp", addr)
		if err == nil {
			conn.SetDeadline(time.Now().Add(time.Second))
			_, err = conn.Read(buf)
			conn.Close()
		}
		cancel()
		if err == nil {
			t.Fatal("daemon key admitted as broker")
		}
	}
}
