package identity_test

import (
	"fmt"
	"os"
	"time"

	"nixmox/internal/identity"
)

func ExampleNode() {
	// --- Test setup ---
	// ------------------
	dir, err := os.MkdirTemp("", "nixmox-identity-")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)
	now := time.Now()
	node, err := identity.Open(dir, "cedar")
	if err != nil {
		panic(err)
	}
	id := node.State.NodeID
	ca, err := identity.NewAuthority("cluster", now)
	if err != nil {
		panic(err)
	}

	// --- Usage ---
	// -------------
	csr, err := node.CSR()
	if err != nil {
		panic(err)
	}
	thumbprint, err := identity.Thumbprint(node.Key.Public())
	if err != nil {
		panic(err)
	}
	certificate, err := ca.Issue(csr, id, thumbprint, []string{"localhost", "127.0.0.1"}, now)
	if err != nil {
		panic(err)
	}
	fmt.Println("enrollment verified:", node.VerifyEnrollment(certificate, ca.PEM, now) == nil)
	_, err = identity.Open(dir, "second process")
	fmt.Println("concurrent process refused:", err != nil)
	if err := node.Close(); err != nil {
		panic(err)
	}
	node, err = identity.Open(dir, "renamed")
	if err != nil {
		panic(err)
	}
	defer node.Close()
	fmt.Println("identity retained:", node.State.NodeID == id, node.State.Name == "cedar")
	fmt.Println("certificate retained:", node.VerifyEnrollment(certificate, ca.PEM, now) == nil)

	// --- Assertions ---
	// ------------------

	// Output:
	// enrollment verified: true
	// concurrent process refused: true
	// identity retained: true true
	// certificate retained: true
}
