// Package storage owns durable local files and process exclusion. It never chooses task policy.
package storage

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

type Store struct {
	Root string
	lock *os.File
}

func Open(root string) (*Store, error) {
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(root)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("data directory must be a private directory (0700)")
	}
	f, err := os.OpenFile(filepath.Join(root, ".lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, errors.New("data directory is already in use")
	}
	s := &Store{Root: root, lock: f}
	for _, d := range []string{"receipts", "jobs", "pki", "pki/daemon"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0700); err != nil {
			s.Close()
			return nil, err
		}
	}
	if err := syncDir(root); err != nil {
		s.Close()
		return nil, err
	}
	if err := syncDir(filepath.Dir(root)); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}
func (s *Store) Close() error { return s.lock.Close() }
func syncDir(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
func (s *Store) Write(path string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return s.WriteBytes(path, append(b, '\n'))
}
func (s *Store) WriteBytes(path string, b []byte) error {
	dest := filepath.Join(s.Root, path)
	dir := filepath.Dir(dest)
	f, err := os.CreateTemp(dir, ".pending-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(f.Name(), dest); err != nil {
		return err
	}
	return syncDir(dir)
}
func (s *Store) Read(path string, v any) error {
	b, err := os.ReadFile(filepath.Join(s.Root, path))
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("corrupt state in %s", path)
	}
	return nil
}
func (s *Store) Names(dir string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(s.Root, dir))
	if err != nil {
		return nil, err
	}
	names := []string{}
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".json" && e.Type().IsRegular() {
			names = append(names, e.Name())
		}
	}
	return names, nil
}
