package installer

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/nix-community/go-nix/pkg/storepath"
	"nixmox/internal/domain"
	"nixmox/internal/identity"
	"nixmox/internal/task"
)

// Preservation is the post-install validation contract, not proof of success.
// The returning daemon must verify the new boot, system, identity and connection.
type Preservation struct {
	NodeID             string      `json:"nodeId"`
	ClusterID          string      `json:"clusterId"`
	TaskID             string      `json:"taskId"`
	PreviousBootID     string      `json:"previousBootId"`
	SSHHostKeyHash     string      `json:"sshHostKeyHash"`
	ExpectedSystem     string      `json:"expectedSystem"`
	ExpectedGeneration uint64      `json:"expectedGeneration"`
	Destination        Destination `json:"destination"`
	WiFiConfigFiles    []string    `json:"wifiConfigFiles"`
}

// PreservationRecord adds installation-relative recovery references to the
// expected boot contract. References are relative to its private state directory.
type PreservationRecord struct {
	Preservation
	Status        string   `json:"status"`
	Environment   string   `json:"environment"`
	ResumeTaskIDs []string `json:"resumeTaskIds"`
	Identity      struct {
		PrivateKeyFile string `json:"privateKeyFile"`
		SSHHostKeyFile string `json:"sshHostKeyFile"`
		AgeKeyFile     string `json:"ageKeyFile"`
	} `json:"identity"`
	Cluster struct {
		ID              string   `json:"id"`
		CertificateFile string   `json:"certificateFile"`
		CAFile          string   `json:"caFile"`
		NATSEndpoints   []string `json:"natsEndpoints"`
	} `json:"cluster"`
	Network struct {
		WiFiConfigFiles []string `json:"wifiConfigFiles"`
	} `json:"network"`
}

type PreservedState struct {
	State      identity.State
	Startup    json.RawMessage
	Task       domain.Task
	Validation Preservation
	// Files contains installation-relative private identity/network files only.
	// It deliberately excludes server databases, worktrees and identity.lock.
	Files map[string][]byte
}

func DestinationPath(path string) (string, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" {
		return "", errors.New("clean absolute destination path required")
	}
	return strings.TrimPrefix(path, "/"), nil
}

// Write is called only after the finalizer verifies a separate mounted root.
// os.Root confines every write, including symlink resolution, to that root.
// The validation manifest is committed last, after all private files are synced.
func (p PreservedState) Write(root *os.Root) error {
	if p.State.NodeID == "" || p.State.ClusterID == "" || p.State.Status != "pending" || !p.Task.Persist || p.Task.HostID != p.State.NodeID || p.Task.ID != p.Validation.TaskID || p.Validation.NodeID != p.State.NodeID || p.Validation.ClusterID != p.State.ClusterID {
		return errors.New("inconsistent installation preservation identity/task")
	}
	if p.Validation.PreviousBootID == "" || p.Validation.ExpectedGeneration == 0 || !storepath.PathRe.MatchString(p.Validation.ExpectedSystem) {
		return errors.New("complete post-install validation contract required")
	}
	snapshotName, err := task.SnapshotName(p.Task)
	if err != nil {
		return err
	}
	stateDir, err := DestinationPath(p.Validation.Destination.StateDir)
	if err != nil {
		return err
	}
	keyPath, err := DestinationPath(p.Validation.Destination.SSHHostKey)
	if err != nil {
		return err
	}
	if len(p.Files[filepath.Join(stateDir, "node.key")]) == 0 || len(p.Files[filepath.Join(stateDir, "age.key")]) == 0 || len(p.Files[keyPath]) == 0 {
		return errors.New("node, SSH and age identities must all be preserved")
	}
	digest := sha256.Sum256(p.Files[keyPath])
	if hex.EncodeToString(digest[:]) != p.Validation.SSHHostKeyHash {
		return errors.New("preserved SSH key differs from validation contract")
	}
	if existing, readErr := root.ReadFile(filepath.Join(stateDir, "state.json")); readErr == nil {
		var previous identity.State
		if err = json.Unmarshal(existing, &previous); err != nil {
			return err
		}
		if previous.NodeID != p.State.NodeID || previous.ClusterID != p.State.ClusterID {
			return errors.New("destination contains another node identity")
		}
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return readErr
	}
	if err = root.MkdirAll(stateDir, 0755); err != nil {
		return err
	}
	private, err := root.Open(stateDir)
	if err != nil {
		return err
	}
	err = private.Chmod(0700)
	private.Close()
	if err != nil {
		return err
	}
	names := make([]string, 0, len(p.Files))
	for name := range p.Files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if !filepath.IsLocal(name) || filepath.Clean(name) != name {
			return errors.New("preserved file must be installation-relative")
		}
		if err = root.MkdirAll(filepath.Dir(name), 0755); err != nil {
			return err
		}
		if err = identity.WritePrivateAt(root, name, p.Files[name]); err != nil {
			return err
		}
	}
	p.State.Environment = "installed"
	p.State.ResumeTaskIDs = []string{p.Task.ID}
	state, err := json.Marshal(p.State)
	if err != nil {
		return err
	}
	snapshot, err := json.Marshal(p.Task)
	if err != nil {
		return err
	}
	var startup struct {
		NATSEndpoints []string `json:"natsEndpoints"`
	}
	if err = json.Unmarshal(p.Startup, &startup); err != nil {
		return err
	}
	if len(startup.NATSEndpoints) == 0 {
		return errors.New("preserved cluster endpoints required")
	}
	record := PreservationRecord{Preservation: p.Validation, Status: p.State.Status, Environment: p.State.Environment, ResumeTaskIDs: p.State.ResumeTaskIDs}
	record.Identity.PrivateKeyFile = "node.key"
	record.Identity.SSHHostKeyFile = "ssh_host_ed25519_key"
	record.Identity.AgeKeyFile = "age.key"
	record.Cluster.ID = p.State.ClusterID
	record.Cluster.CertificateFile = "node.crt"
	record.Cluster.CAFile = "cluster-ca.crt"
	record.Cluster.NATSEndpoints = startup.NATSEndpoints
	if err = identity.WritePrivateAt(root, filepath.Join(stateDir, record.Identity.SSHHostKeyFile), p.Files[keyPath]); err != nil {
		return err
	}
	for _, name := range []string{record.Cluster.CertificateFile, record.Cluster.CAFile} {
		if len(p.Files[filepath.Join(stateDir, name)]) == 0 {
			return errors.New("preserved cluster certificates required")
		}
	}
	for _, active := range p.Validation.WiFiConfigFiles {
		if !filepath.IsLocal(active) || len(p.Files[active]) == 0 {
			return errors.New("preserved Wi-Fi reference has no copied file")
		}
		name := filepath.Join("wifi", active)
		if err = root.MkdirAll(filepath.Join(stateDir, filepath.Dir(name)), 0700); err != nil {
			return err
		}
		if err = identity.WritePrivateAt(root, filepath.Join(stateDir, name), p.Files[active]); err != nil {
			return err
		}
		record.Network.WiFiConfigFiles = append(record.Network.WiFiConfigFiles, name)
	}
	validation, err := json.Marshal(record)
	if err != nil {
		return err
	}
	if err = root.MkdirAll(filepath.Join(stateDir, "tasks"), 0700); err != nil {
		return err
	}
	for _, file := range []struct {
		name string
		data []byte
	}{
		{"cluster.json", p.Startup},
		{"state.json", state},
		{filepath.Join("tasks", snapshotName), snapshot},
		{"install.json", validation},
	} {
		if err = identity.WritePrivateAt(root, filepath.Join(stateDir, file.name), file.data); err != nil {
			return err
		}
	}
	return nil
}
