package enrollment_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/nats-io/nats.go/jetstream"
	"nixmox/internal/cluster"
	"nixmox/internal/domain"
	"nixmox/internal/enrollment"
	"nixmox/internal/identity"
	"nixmox/internal/testutil"
)

// A joining node supplies proof of its own key, while a different full member
// can approve the request without replacing its contact or creating membership.
func TestRequestedEnrollmentDecision(t *testing.T) {
	// --- Test setup ---
	// ------------------
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	js := testutil.JetStream(t)
	for _, name := range []string{"ENROLLMENT", "MEMBERS", "CLUSTER"} {
		if _, err := js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: name}); err != nil {
			t.Fatal(err)
		}
	}
	requests, err := cluster.OpenStore[enrollment.Request](ctx, js, "ENROLLMENT")
	if err != nil {
		t.Fatal(err)
	}
	members, err := cluster.OpenStore[domain.Member](ctx, js, "MEMBERS")
	if err != nil {
		t.Fatal(err)
	}
	clusters, err := cluster.OpenStore[domain.Cluster](ctx, js, "CLUSTER")
	if err != nil {
		t.Fatal(err)
	}
	ca, err := identity.NewAuthority("cluster", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = clusters.Create(ctx, "identity", domain.Cluster{ID: "cluster", CA: domain.ClusterCA{Certificate: string(ca.PEM)}}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"contact", "approver"} {
		if _, err = members.Create(ctx, id, domain.Member{NodeID: id, ClusterID: "cluster", Status: "full-stable", Endpoints: []domain.Endpoint{{HTTP: "http://127.0.0.1:8080", NATSClient: "tls://127.0.0.1:4222"}}}); err != nil {
			t.Fatal(err)
		}
	}
	node, err := identity.Open(t.TempDir(), "joining")
	if err != nil {
		t.Fatal(err)
	}
	defer node.Close()
	csr, err := node.CSR()
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, err := identity.Thumbprint(node.Key.Public())
	if err != nil {
		t.Fatal(err)
	}
	key, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	contact := &enrollment.Service{NodeID: "contact", Requests: requests, Members: members, Clusters: clusters}
	approver := &enrollment.Service{NodeID: "approver", Requests: requests, Members: members, Clusters: clusters}
	joining := enrollment.JoiningNode{NodeID: node.State.NodeID, Name: "joining", Endpoint: "http://127.0.0.1:8081", Environment: "installed", CSR: string(csr), PublicKeyThumbprint: fingerprint, AgePublicKey: key.Recipient().String()}

	// --- Usage ---
	// -------------
	request, err := contact.Receive(ctx, joining)
	if err != nil {
		t.Fatal(err)
	}
	repeated, err := contact.Receive(ctx, joining)
	if err != nil {
		t.Fatal(err)
	}
	approved, err := approver.Decide(ctx, request.ID, "approved")
	if err != nil {
		t.Fatal(err)
	}
	_, memberErr := members.Get(ctx, joining.NodeID)

	// --- Assertions ---
	// ------------------
	if request.Status != "pending" || request.Decision != nil || request.JoiningNode.IssuedCertificate != "" {
		t.Fatal("request was authorized before approval")
	}
	if repeated.ID != request.ID {
		t.Fatal("repeated request did not retain its identity")
	}
	if approved.Decision.ByNodeID != "approver" || approved.Decision.Result != "approved" || approved.Cluster.ContactNode.NodeID != "contact" {
		t.Fatal("approval changed contact ownership")
	}
	if !errors.Is(memberErr, jetstream.ErrKeyNotFound) {
		t.Fatalf("decision must not create membership: %v", memberErr)
	}
}
