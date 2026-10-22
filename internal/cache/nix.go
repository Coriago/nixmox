package cache

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Nix verifies imports; existing external substituters and trust remain intact.
func (s *Service) NixOptions(ctx context.Context) []string {
	if !s.Config.Enable {
		return nil
	}
	args := []string{"--option", "extra-substituters", s.LocalURL() + "?priority=10", "--option", "require-sigs", "true"}
	keys, err := s.trustedKeys(ctx)
	if err != nil {
		return args
	}
	public := make([]string, 0, len(keys))
	for _, key := range keys {
		public = append(public, key.String())
	}
	if len(public) != 0 {
		args = append(args, "--option", "extra-trusted-public-keys", strings.Join(public, " "))
	}
	return args
}

// The module opts in to a public runtime include. Private material is never
// written here; standalone task invocations use NixOptions directly.
func (s *Service) syncTrust(ctx context.Context) error {
	if s.TrustFile == "" {
		return nil
	}
	keys, err := s.trustedKeys(ctx)
	public := []string{}
	if err == nil {
		for _, key := range keys {
			public = append(public, key.String())
		}
	}
	sort.Strings(public)
	data := []byte("extra-trusted-public-keys = " + strings.Join(public, " ") + "\n")
	if old, err := os.ReadFile(s.TrustFile); err == nil && bytes.Equal(old, data) {
		return nil
	}
	file, err := os.CreateTemp(filepath.Dir(s.TrustFile), ".cache-trust-")
	if err != nil {
		return err
	}
	name := file.Name()
	defer os.Remove(name)
	defer file.Close()
	if _, err = file.Write(data); err != nil {
		return err
	}
	if err = file.Chmod(0644); err != nil {
		return err
	}
	if err = file.Sync(); err != nil {
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	return os.Rename(name, s.TrustFile)
}
