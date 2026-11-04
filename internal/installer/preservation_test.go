package installer_test

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/google/uuid"
	"nixmox/internal/domain"
	"nixmox/internal/identity"
	"nixmox/internal/installer"
)

func ExamplePreservedState() {
	// --- Test setup ---
	// ------------------
	directory, err := os.MkdirTemp("", "nixmox-preservation-")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(directory)
	source, err := identity.Open(filepath.Join(directory, "source"), "installer")
	if err != nil {
		panic(err)
	}
	defer source.Close()
	source.State.ClusterID, source.State.Status = "cluster", "pending"
	key, err := os.ReadFile(filepath.Join(directory, "source", "node.key"))
	if err != nil {
		panic(err)
	}
	destinationPath := filepath.Join(directory, "installed")
	if err = os.Mkdir(destinationPath, 0700); err != nil {
		panic(err)
	}
	root, err := os.OpenRoot(destinationPath)
	if err != nil {
		panic(err)
	}
	defer root.Close()
	work := domain.Task{ID: source.State.NodeID + "." + uuid.NewString(), HostID: source.State.NodeID, Type: "fresh-deployment", Status: domain.Running, Persist: true, Inputs: json.RawMessage(`{}`)}
	preserved := installer.PreservedState{
		State: source.State, Startup: json.RawMessage(`{"member":{"status":"pending"},"natsEndpoints":["tls://contact:4222"]}`), Task: work,
		Validation: installer.Preservation{NodeID: source.State.NodeID, ClusterID: "cluster", TaskID: work.ID, PreviousBootID: "installer-boot", ExpectedSystem: "/nix/store/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-nixos-system", ExpectedGeneration: 1, Destination: installer.Destination{StateDir: "/var/lib/nixmox", SSHHostKey: "/etc/ssh/ssh_host_ed25519_key"}},
		Files:      map[string][]byte{"var/lib/nixmox/node.crt": []byte("certificate fixture"), "var/lib/nixmox/cluster-ca.crt": []byte("CA fixture"), "var/lib/nixmox/node.key": key, "var/lib/nixmox/age.key": []byte("age identity fixture"), "etc/ssh/ssh_host_ed25519_key": []byte("SSH identity fixture"), "etc/NetworkManager/system-connections/wifi": []byte("Wi-Fi fixture")},
	}

	digest := sha256.Sum256(preserved.Files["etc/ssh/ssh_host_ed25519_key"])
	preserved.Validation.SSHHostKeyHash = fmt.Sprintf("%x", digest)

	// --- Usage ---
	// -------------
	if err = preserved.Write(root); err != nil {
		panic(err)
	}
	restored, err := identity.Open(filepath.Join(destinationPath, "var/lib/nixmox"), "ignored-name")
	if err != nil {
		panic(err)
	}
	defer restored.Close()
	fmt.Println("identity retained:", restored.State.NodeID == source.State.NodeID && restored.Key.Equal(source.Key))
	fmt.Println("installed resume:", restored.State.Environment == "installed" && len(restored.State.ResumeTaskIDs) == 1 && restored.State.ResumeTaskIDs[0] == work.ID)
	info, err := root.Stat("etc/ssh/ssh_host_ed25519_key")
	if err != nil {
		panic(err)
	}
	fmt.Println("private SSH key:", info.Mode().Perm() == 0600)
	data, err := root.ReadFile("var/lib/nixmox/install.json")
	if err != nil {
		panic(err)
	}
	var record installer.PreservationRecord
	if err = json.Unmarshal(data, &record); err != nil {
		panic(err)
	}
	copied, err := root.ReadFile(filepath.Join("var/lib/nixmox", record.Identity.SSHHostKeyFile))
	if err != nil {
		panic(err)
	}
	fmt.Println("relative recovery identity:", filepath.IsLocal(record.Identity.SSHHostKeyFile) && string(copied) == "SSH identity fixture" && record.Status == "pending" && record.Environment == "installed")
	if err = root.Symlink(directory, "escape"); err != nil {
		panic(err)
	}
	preserved.Files["escape/outside"] = []byte("must not be written")
	fmt.Println("destination escape refused:", preserved.Write(root) != nil)
	_, err = os.Stat(filepath.Join(directory, "outside"))
	fmt.Println("outside untouched:", os.IsNotExist(err))

	// --- Assertions ---
	// ------------------
	// Output:
	// identity retained: true
	// installed resume: true
	// private SSH key: true
	// relative recovery identity: true
	// destination escape refused: true
	// outside untouched: true
}
