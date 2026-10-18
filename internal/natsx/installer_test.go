package natsx

import (
	"context"
	"fmt"
	"github.com/nats-io/nats.go/jetstream"
	"testing"
	"time"
)

func TestInstallerLeafUsesCoordinatorJetStream(t *testing.T) {
	hubOpts, _ := NewOptions("hub", 0, 0, "127.0.0.1", t.TempDir(), ClusterSpec{})
	hub, err := Start(hubOpts)
	if err != nil {
		t.Fatal(err)
	}
	defer hub.Stop()
	leafOpts, _ := NewOptions("installer", 0, 0, "127.0.0.1", t.TempDir(), ClusterSpec{})
	leafOpts.JetStream = false
	leafOpts.StoreDir = ""
	leafOpts.LeafNode.Host = "127.0.0.1"
	leafOpts.LeafNode.Port = -1
	leaf, err := Start(leafOpts)
	if err != nil {
		t.Fatal(err)
	}
	defer leaf.Stop()
	info, err := leaf.Srv.Varz(nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := hub.ConnectInstaller(fmt.Sprintf("127.0.0.1:%d", info.LeafNode.Port)); err != nil {
		t.Fatal(err)
	}
	if err := WaitReady(leaf.JS, 10*time.Second); err != nil {
		t.Fatal(err)
	}
	if leaf.Srv.JetStreamEnabled() {
		t.Fatal("temporary node became a JetStream voter")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stream, err := hub.JS.CreateStream(ctx, jetstream.StreamConfig{Name: "test", Subjects: []string{"test"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := leaf.JS.Publish(ctx, "test", []byte("over NATS")); err != nil {
		t.Fatal(err)
	}
	msg, err := stream.GetMsg(ctx, 1)
	if err != nil || string(msg.Data) != "over NATS" {
		t.Fatalf("hub did not store installer message: %v", err)
	}
}
