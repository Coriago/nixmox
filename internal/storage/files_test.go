package storage

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPrivateDurableIdentityAndExclusion(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state")
	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Open(root); err == nil {
		t.Fatal("second process could open state")
	}
	id, err := s.Identity()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	restored, err := s.Identity()
	if err != nil || id.NodeID != restored.NodeID || id.PublicKey != restored.PublicKey {
		t.Fatal(restored, err)
	}
	for _, p := range []string{"identity.json", "pki/daemon/key.pem", "pki/daemon/cert.pem"} {
		st, err := os.Stat(filepath.Join(root, p))
		if err != nil || st.Mode().Perm() != 0600 {
			t.Fatal(p, err)
		}
	}
	_ = os.Remove(filepath.Join(root, "pki/daemon/key.pem"))
	if _, err := s.Identity(); err == nil {
		t.Fatal("silently replaced missing identity key")
	}
}
func TestInterruptedIdentityInitialization(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state")
	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	id, err := s.Identity()
	if err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(filepath.Join(root, "identity.json"))
	_ = os.Remove(filepath.Join(root, "pki/daemon/cert.pem"))
	resumed, err := s.Identity()
	if err != nil || resumed.PublicKey != id.PublicKey {
		t.Fatal("did not preserve staged key", err)
	}
}
