package poc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"github.com/nats-io/jsm.go"
	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	js "github.com/nats-io/nats.go/jetstream"
)

// Run: nix develop -c go test ./poc -run TestBootstrapPeer -v -count=1 -timeout=5m
// Loopback-only servers simulate hosts: A owns both a and bootstrap; B and C
// own b and c. No production engine, store migration, or custom Raft is used.
func TestBootstrapPeer(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	root := t.TempDir()
	names := []string{"a", "bootstrap", "b", "c", "recovery"}
	ports := map[string]int{}
	// Reserve distinct route ports together, then release them before startup.
	var listeners []net.Listener
	for _, name := range names {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		listeners = append(listeners, l)
		ports[name] = l.Addr().(*net.TCPAddr).Port
	}
	for _, l := range listeners {
		l.Close()
	}
	nodes := map[string]*server.Server{}
	stop := func(name string) {
		if s := nodes[name]; s != nil {
			s.Shutdown()
			s.WaitForShutdown()
			delete(nodes, name)
		}
	}
	t.Cleanup(func() {
		for _, name := range names {
			stop(name)
		}
	})
	start := func(name string, peers ...string) {
		t.Helper()
		opts := &server.Options{}
		err := opts.ProcessConfigString(`accounts {
 APP { jetstream: enabled, users: [{user: app, password: poc}] }
 SYS { users: [{user: sys, password: poc}] }
}
system_account: SYS`)
		if err != nil {
			t.Fatal(err)
		}
		opts.ServerName, opts.Host, opts.Port = name, "127.0.0.1", -1
		opts.JetStream, opts.StoreDir = true, filepath.Join(root, name)
		opts.NoSigs, opts.NoLog = true, true
		opts.Cluster = server.ClusterOpts{Name: "poc", Host: "127.0.0.1", Port: ports[name]}
		for _, peer := range peers {
			u, _ := url.Parse(fmt.Sprintf("nats-route://127.0.0.1:%d", ports[peer]))
			opts.Routes = append(opts.Routes, u)
		}
		s, err := server.NewServer(opts)
		if err != nil {
			t.Fatal(err)
		}
		nodes[name] = s
		s.Start()
		if !s.ReadyForConnections(5 * time.Second) {
			t.Fatalf("%s did not start", name)
		}
	}
	connect := func(name, user string) *nats.Conn {
		t.Helper()
		nc, err := nats.Connect(nodes[name].ClientURL(), nats.UserInfo(user, "poc"), nats.NoReconnect(), nats.Timeout(time.Second))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(nc.Close)
		return nc
	}
	apiFor := func(name string) js.JetStream {
		t.Helper()
		api, err := js.New(connect(name, "app"), js.WithDefaultTimeout(2*time.Second))
		if err != nil {
			t.Fatal(err)
		}
		return api
	}
	wait := func(label string, check func() error) {
		t.Helper()
		deadline := time.Now().Add(35 * time.Second)
		var err error
		for time.Now().Before(deadline) && ctx.Err() == nil {
			if err = check(); err == nil {
				return
			}
			time.Sleep(200 * time.Millisecond)
		}
		t.Fatalf("%s: %v", label, err)
	}
	meta := func(observer string, size int) {
		t.Helper()
		wait("metadata membership", func() error {
			info, err := nodes[observer].Jsz(nil)
			if err != nil {
				return err
			}
			if info.Meta == nil || info.Meta.Leader == "" || info.Meta.Size != size {
				return fmt.Errorf("want size %d and leader; got %+v", size, info.Meta)
			}
			return nil
		})
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}

	t.Log("1: bootstrap clustered mode on host A using two persistent servers")
	start("a", "bootstrap")
	start("bootstrap", "a")
	meta("a", 2)
	api := apiFor("a")
	wait("JetStream account ready", func() error { _, err := api.AccountInfo(ctx); return err })
	_, err := api.CreateStream(ctx, js.StreamConfig{Name: "EVENTS", Subjects: []string{"events"}, Storage: js.FileStorage, Replicas: 2})
	must(err)
	_, err = api.CreateKeyValue(ctx, js.KeyValueConfig{Bucket: "STATE", Storage: js.FileStorage, Replicas: 2})
	must(err)
	objects, err := api.CreateObjectStore(ctx, js.ObjectStoreConfig{Bucket: "FILES", Storage: js.FileStorage, Replicas: 2})
	must(err)
	// Larger than the normal NATS message limit, exercising native object chunking.
	payload := bytes.Repeat([]byte("bootstrap-object\n"), 150000)
	_, err = objects.PutBytes(ctx, "recipe", payload)
	must(err)
	streams := []string{"EVENTS", "KV_STATE", "OBJ_FILES"}
	probe := func(api js.JetStream, phase string) {
		t.Helper()
		wait("publish "+phase, func() error { _, err := api.Publish(ctx, "events", []byte(phase)); return err })
		// Each backing stream elects independently; EVENTS readiness does not
		// imply that KV_STATE or OBJ_FILES already has a leader after failure.
		wait("KV "+phase, func() error {
			k, err := api.KeyValue(ctx, "STATE")
			if err != nil {
				return err
			}
			if _, err = k.Put(ctx, "phase", []byte(phase)); err != nil {
				return err
			}
			entry, err := k.Get(ctx, "phase")
			if err != nil {
				return err
			}
			if string(entry.Value()) != phase {
				return fmt.Errorf("KV mismatch")
			}
			return nil
		})
		wait("object "+phase, func() error {
			o, err := api.ObjectStore(ctx, "FILES")
			if err != nil {
				return err
			}
			got, err := o.GetBytes(ctx, "recipe")
			if err != nil {
				return err
			}
			if !bytes.Equal(got, payload) {
				return fmt.Errorf("object mismatch")
			}
			if _, err = o.PutBytes(ctx, "checkpoint", []byte(phase)); err != nil {
				return err
			}
			got, err = o.GetBytes(ctx, "checkpoint")
			if err != nil {
				return err
			}
			if string(got) != phase {
				return fmt.Errorf("object write mismatch")
			}
			return nil
		})
	}
	ready := func(api js.JetStream, replicas int, excluded string) {
		t.Helper()
		wait("replicas current", func() error {
			for _, name := range streams {
				s, err := api.Stream(ctx, name)
				if err != nil {
					return err
				}
				info, err := s.Info(ctx)
				if err != nil {
					return err
				}
				c := info.Cluster
				if info.Config.Replicas != replicas || c == nil || c.Leader == "" || c.Leader == excluded || len(c.Replicas)+1 != replicas || c.Desired != nil {
					detail, _ := json.Marshal(c)
					return fmt.Errorf("%s not ready: %s", name, detail)
				}
				for _, peer := range c.Replicas {
					if !peer.Current || peer.Offline || peer.Name == excluded {
						return fmt.Errorf("%s peer not ready: %+v", name, peer)
					}
				}
			}
			return nil
		})
	}
	ready(api, 2, "")
	probe(api, "initial")

	t.Log("2: add host B and scale all stores to R3 through native stream updates")
	start("b", "a", "bootstrap")
	meta("a", 3)
	for _, name := range streams {
		s, err := api.Stream(ctx, name)
		must(err)
		info, err := s.Info(ctx)
		must(err)
		cfg := info.Config
		cfg.Replicas = 3
		_, err = api.UpdateStream(ctx, cfg)
		must(err)
	}
	ready(api, 3, "")
	probe(apiFor("b"), "host-b")

	t.Log("3: stop B; A + bootstrap must retain metadata and data quorum")
	stop("b")
	meta("a", 3)
	probe(api, "b-offline")
	start("b", "a", "bootstrap")
	ready(api, 3, "")

	t.Log("4: stop host A (both servers); B must NOT acknowledge writes")
	stop("a")
	stop("bootstrap")
	bAPI := apiFor("b")
	failCtx, failCancel := context.WithTimeout(ctx, 3*time.Second)
	_, err = bAPI.Publish(failCtx, "events", []byte("must-not-ack"))
	failCancel()
	if err == nil {
		t.Fatal("write acknowledged without quorum")
	}
	t.Logf("expected quorum failure: %v", err)
	start("a", "bootstrap", "b")
	start("bootstrap", "a", "b")
	api = apiFor("a")
	ready(api, 3, "")
	probe(api, "a-recovered")

	t.Log("5: add C, evacuate bootstrap replicas, then remove its metadata peer")
	start("c", "a", "b")
	meta("a", 4)
	admin, err := jsm.New(connect("a", "app"), jsm.WithTimeout(2*time.Second))
	must(err)
	for _, name := range streams {
		s, err := admin.LoadStream(name)
		must(err)
		must(s.EvacuatePeer("bootstrap"))
	}
	ready(api, 3, "bootstrap")
	// Native peer removal is issued after stopping the retired server so it
	// cannot rejoin; no store files or Raft metadata are edited by this PoC.
	stop("bootstrap")
	sys, err := jsm.New(connect("a", "sys"), jsm.WithTimeout(2*time.Second))
	must(err)
	wait("remove bootstrap metadata peer", func() error { return sys.MetaPeerRemove("bootstrap", "") })
	meta("a", 3)
	probe(apiFor("c"), "bootstrap-removed")

	t.Log("6: stop A; independent hosts B + C must now retain quorum")
	stop("a")
	probe(apiFor("b"), "a-offline-without-bootstrap")
	start("a", "b", "c")
	api = apiFor("a")
	ready(api, 3, "bootstrap")

	t.Log("7: cold restart A/B/C from the SAME directories, with no bootstrap")
	for _, name := range []string{"a", "b", "c"} {
		stop(name)
	}
	start("a", "b", "c")
	start("b", "a", "c")
	start("c", "a", "b")
	api = apiFor("a")
	meta("a", 3)
	ready(api, 3, "bootstrap")
	probe(api, "cold-restart")
	t.Log("8: permanently lose B and C; rescue metadata on A using the native API")
	stop("b")
	stop("c")
	rescueNC := connect("a", "sys")
	wait("metadata rescue", func() error {
		msg, err := rescueNC.Request("$JS.API.META.RESCUE", []byte(`{"quorum_needed":1}`), 2*time.Second)
		if err != nil {
			return err
		}
		var response struct {
			Error *struct {
				Description string `json:"description"`
			} `json:"error"`
		}
		if err := json.Unmarshal(msg.Data, &response); err != nil {
			return err
		}
		if response.Error != nil {
			return fmt.Errorf("rescue: %s", response.Error.Description)
		}
		return nil
	})
	rescueAdmin, err := jsm.New(rescueNC, jsm.WithTimeout(2*time.Second))
	must(err)
	for _, name := range []string{"b", "c"} {
		wait("remove lost peer "+name, func() error { return rescueAdmin.MetaPeerRemove(name, "") })
	}
	meta("a", 1)
	info, err := nodes["a"].Jsz(nil)
	must(err)
	if info.Meta.Rescue {
		t.Fatal("metadata still depends on temporary rescue")
	}
	// Peer removal starts native recovery of each independent data group.
	// Wait for those leaders before requesting another topology change.
	probe(api, "peer-removal-recovered")

	t.Log("9: start fresh recovery bootstrap, settle at R2, then restart both")
	start("recovery", "a")
	meta("a", 2)
	for _, name := range streams {
		s, err := api.Stream(ctx, name)
		must(err)
		info, err := s.Info(ctx)
		must(err)
		cfg := info.Config
		cfg.Replicas = 2
		_, err = api.UpdateStream(ctx, cfg)
		must(err)
	}
	ready(api, 2, "bootstrap")
	probe(apiFor("recovery"), "recovery-bootstrap")
	stop("a")
	stop("recovery")
	start("a", "recovery")
	start("recovery", "a")
	api = apiFor("a")
	meta("a", 2)
	ready(api, 2, "bootstrap")
	probe(api, "emergency-cold-restart")
	// Original stream history survives every membership change and restart.
	s, err := api.Stream(ctx, "EVENTS")
	must(err)
	first, err := s.GetMsg(ctx, 1)
	must(err)
	if string(first.Data) != "initial" {
		t.Fatal("original stream message lost")
	}
	t.Log("PASS: stream, KV and chunked object survived; no standalone mode or snapshot handoff")
}
