package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"nixmox/internal/host"
	"nixmox/internal/installer"
	"nixmox/internal/installer/finalize"
	"nixmox/internal/task"
)

func (r *Runtime) preserveInstalled(ctx context.Context, execution *task.Execution, root string) error {
	var input installer.FinalizeInput
	if err := json.Unmarshal(execution.Task().Inputs, &input); err != nil {
		return err
	}
	return r.preserveInstallation(ctx, execution, root, input.SystemPath, input.Destination)
}

func (r *Runtime) preserveInstallation(ctx context.Context, execution *task.Execution, root, systemPath string, target installer.Destination) error {
	r.mu.RLock()
	state := r.node.State
	r.mu.RUnlock()
	if state.Status != "pending" || state.Environment != "installer" || state.ClusterID == "" || execution.Task().HostID != state.NodeID {
		return errors.New("only the owning pending installer may preserve installed state")
	}
	if target.StateDir == "" {
		target.StateDir = r.config.StateDir
	}
	if target.SSHHostKey == "" {
		target.SSHHostKey = r.config.SSHHostKey
	}
	stateDir, err := installer.DestinationPath(target.StateDir)
	if err != nil {
		return err
	}
	sshKey, err := installer.DestinationPath(target.SSHHostKey)
	if err != nil {
		return err
	}
	destination, err := finalize.OpenDestination(ctx, host.Process{}, root)
	if err != nil {
		return err
	}
	defer destination.Close()
	snapshot := execution.Task()
	if snapshot.Links.ParentTaskID != "" {
		services, err := r.Services()
		if err != nil {
			return err
		}
		parent, err := services.Tasks.Tasks.Get(ctx, snapshot.Links.ParentTaskID)
		if err != nil {
			return err
		}
		snapshot = parent.Value
	}
	if !snapshot.Persist || snapshot.HostID != state.NodeID || snapshot.Status != "running" {
		return errors.New("running persistent installation task required")
	}
	startupData, err := readPreservedFile(filepath.Join(r.config.StateDir, "cluster.json"))
	if err != nil {
		return err
	}
	defer clear(startupData)
	var saved startup
	if err = json.Unmarshal(startupData, &saved); err != nil {
		return err
	}
	if saved.Cluster.ID != state.ClusterID || saved.Member.NodeID != state.NodeID {
		return errors.New("startup identity differs from installation identity")
	}
	validation, err := r.captureInstallation(ctx, execution, systemPath, target)
	if err != nil {
		return err
	}
	validation.TaskID = snapshot.ID
	if checkpoint, ok := snapshot.Context["installation"]; ok {
		var expected installer.Preservation
		if err = json.Unmarshal(checkpoint, &expected); err != nil {
			return err
		}
		if expected.NodeID != validation.NodeID || expected.TaskID != validation.TaskID || expected.ClusterID != validation.ClusterID || expected.PreviousBootID != validation.PreviousBootID || expected.ExpectedSystem != validation.ExpectedSystem || expected.Destination != validation.Destination || expected.SSHHostKeyHash != validation.SSHHostKeyHash {
			return errors.New("installation preservation changed since planning")
		}
		validation = expected
	}
	preserved := installer.PreservedState{State: state, Startup: startupData, Task: snapshot, Files: map[string][]byte{}, Validation: validation}
	defer func() {
		for _, data := range preserved.Files {
			clear(data)
		}
	}()
	copyFile := func(source, name string, optional bool) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		data, err := readPreservedFile(source)
		if optional && errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		preserved.Files[name] = data
		return nil
	}
	for _, name := range []string{"node.key", "age.key", "enrollment.json"} {
		if err = copyFile(filepath.Join(r.config.StateDir, name), filepath.Join(stateDir, name), name == "enrollment.json"); err != nil {
			return err
		}
	}
	preserved.Files[filepath.Join(stateDir, "node.crt")] = []byte(saved.Certificate)
	preserved.Files[filepath.Join(stateDir, "cluster-ca.crt")] = []byte(saved.Cluster.CA.Certificate)
	if err = copyFile(r.config.SSHHostKey, sshKey, false); err != nil {
		return err
	}
	if err = copyFile(r.config.SSHHostKey+".pub", sshKey+".pub", true); err != nil {
		return err
	}
	for _, spec := range []struct{ pattern, directory string }{
		{"/etc/NetworkManager/system-connections/*", "etc/NetworkManager/system-connections"},
		{"/run/NetworkManager/system-connections/*", "etc/NetworkManager/system-connections"},
		{"/var/lib/iwd/*", "var/lib/iwd"},
		{"/etc/wpa_supplicant.conf", "etc"},
		{"/etc/wpa_supplicant/wpa_supplicant.conf", "etc/wpa_supplicant"},
	} {
		paths, err := filepath.Glob(spec.pattern)
		if err != nil {
			return err
		}
		for _, path := range paths {
			info, err := os.Stat(path)
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() {
				continue
			}
			name := filepath.Join(spec.directory, filepath.Base(path))
			if err = copyFile(path, name, false); err != nil {
				return err
			}
			preserved.Validation.WiFiConfigFiles = append(preserved.Validation.WiFiConfigFiles, name)
		}
	}
	return preserved.Write(destination)
}

func (r *Runtime) captureInstallation(ctx context.Context, execution *task.Execution, systemPath string, target installer.Destination) (installer.Preservation, error) {
	if err := ctx.Err(); err != nil {
		return installer.Preservation{}, err
	}
	r.mu.RLock()
	state := r.node.State
	r.mu.RUnlock()
	if target.StateDir == "" {
		target.StateDir = r.config.StateDir
	}
	if target.SSHHostKey == "" {
		target.SSHHostKey = r.config.SSHHostKey
	}
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return installer.Preservation{}, err
	}
	key, err := readPreservedFile(r.config.SSHHostKey)
	if err != nil {
		return installer.Preservation{}, err
	}
	defer clear(key)
	digest := sha256.Sum256(key)
	return installer.Preservation{NodeID: state.NodeID, ClusterID: state.ClusterID, TaskID: execution.Task().ID, PreviousBootID: strings.TrimSpace(string(boot)), SSHHostKeyHash: hex.EncodeToString(digest[:]), ExpectedSystem: systemPath, ExpectedGeneration: 1, Destination: target}, nil
}

func (r *Runtime) validateInstalled(ctx context.Context, execution *task.Execution, expected installer.Preservation) error {
	if _, err := r.Services(); err != nil {
		return err
	}
	data, err := os.ReadFile(filepath.Join(r.config.StateDir, "install.json"))
	if err != nil {
		return err
	}
	var manifest installer.Preservation
	if err = json.Unmarshal(data, &manifest); err != nil {
		return err
	}
	r.mu.RLock()
	state := r.node.State
	r.mu.RUnlock()
	if r.config.Environment != "installed" || state.NodeID != expected.NodeID || state.ClusterID != expected.ClusterID || expected.TaskID != execution.Task().ID || expected.Destination.StateDir != r.config.StateDir || expected.Destination.SSHHostKey != r.config.SSHHostKey || manifest.NodeID != expected.NodeID || manifest.TaskID != expected.TaskID || manifest.ClusterID != expected.ClusterID || manifest.PreviousBootID != expected.PreviousBootID || manifest.ExpectedSystem != expected.ExpectedSystem || manifest.ExpectedGeneration != expected.ExpectedGeneration || manifest.Destination != expected.Destination || manifest.SSHHostKeyHash != expected.SSHHostKeyHash {
		return errors.New("installed preservation record differs from task contract")
	}
	key, err := readPreservedFile(r.config.SSHHostKey)
	if err != nil {
		return err
	}
	defer clear(key)
	digest := sha256.Sum256(key)
	if hex.EncodeToString(digest[:]) != expected.SSHHostKeyHash {
		return errors.New("SSH host identity changed during installation")
	}
	for _, path := range manifest.WiFiConfigFiles {
		if !filepath.IsLocal(path) {
			return errors.New("invalid preserved Wi-Fi reference")
		}
		info, err := os.Stat(filepath.Join("/", path))
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return errors.New("preserved Wi-Fi file missing")
		}
	}
	return ctx.Err()
}

func readPreservedFile(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return nil, fmt.Errorf("invalid preservation file %s", path)
	}
	data, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
	if err != nil {
		clear(data)
		return nil, err
	}
	if len(data) > 1<<20 {
		clear(data)
		return nil, errors.New("preservation file exceeds 1 MiB")
	}
	return data, nil
}
