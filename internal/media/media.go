// Package media writes trusted hybrid installer images to explicitly selected USB disks.
package media

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"golang.org/x/sys/unix"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

type Device struct {
	Path        string    `json:"path"`
	Type        string    `json:"type"`
	Transport   string    `json:"tran"`
	Model       string    `json:"model"`
	Serial      string    `json:"serial"`
	Size        uint64    `json:"size"`
	ReadOnly    bool      `json:"ro"`
	Removable   bool      `json:"rm"`
	MajorMinor  string    `json:"maj:min"`
	Mountpoints []*string `json:"mountpoints"`
	Children    []Device  `json:"children,omitempty"`
	Reason      string    `json:"reason,omitempty"`
	Identity    string    `json:"identity"`
}

func blocked(d Device) string {
	if d.ReadOnly {
		return "Read-only device"
	}
	for _, p := range d.Mountpoints {
		if p != nil && *p != "" {
			return "Mounted or active swap; unmount before writing"
		}
	}
	for _, c := range d.Children {
		if c.Type != "part" {
			return "Device has active mappings"
		}
		if reason := blocked(c); reason != "" {
			return reason
		}
	}
	return ""
}

func Candidates(data []byte) ([]Device, error) {
	var result struct {
		Devices []Device `json:"blockdevices"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, err
	}
	devices := []Device{}
	for _, d := range result.Devices {
		if d.Type != "disk" || d.Transport != "usb" {
			continue
		}
		d.Reason = blocked(d)
		if d.Size == 0 || d.Serial == "" {
			d.Reason = "Missing capacity or serial; cannot safely identify this drive"
		}
		sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%s|%s|%s|%d", d.Path, d.MajorMinor, d.Model, d.Serial, d.Size)))
		d.Identity = hex.EncodeToString(sum[:])
		devices = append(devices, d)
	}
	return devices, nil
}

func List(ctx context.Context) ([]Device, error) {
	data, err := exec.CommandContext(ctx, "lsblk", "--json", "--tree", "--bytes", "--output", "PATH,TYPE,TRAN,MODEL,SERIAL,SIZE,RO,RM,MAJ:MIN,MOUNTPOINTS").Output()
	if err != nil {
		return nil, fmt.Errorf("enumerate USB disks: %w", err)
	}
	return Candidates(data)
}

func Select(devices []Device, path, identity string) (Device, error) {
	for _, d := range devices {
		if d.Path == path && d.Identity == identity && d.Reason == "" {
			return d, nil
		}
	}
	return Device{}, fmt.Errorf("drive changed, is busy, or is not an eligible USB disk; refresh drives")
}

func Image(root string) (string, error) {
	matches, err := filepath.Glob(filepath.Join(root, "iso", "*.iso"))
	if err != nil || len(matches) != 1 {
		return "", fmt.Errorf("prepare an installer first (expected exactly one hybrid ISO)")
	}
	path, err := filepath.EvalSymlinks(matches[0])
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(path, "/nix/store/") {
		return "", fmt.Errorf("installer must be in the Nix store")
	}
	return path, nil
}

// Copy verifies the exact bytes written. The caller owns destination validation.
func Copy(ctx context.Context, dst *os.File, src *os.File, size int64, progress func(string, int, int) error) error {
	hash := sha256.New()
	buf := make([]byte, 4<<20)
	var done int64
	for done < size {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, err := io.ReadFull(src, buf[:min(int64(len(buf)), size-done)])
		if err != nil {
			return err
		}
		if _, err = dst.Write(buf[:n]); err != nil {
			return err
		}
		hash.Write(buf[:n])
		done += int64(n)
		if err := progress("Writing installer", int(done>>20), int((size+(1<<20)-1)>>20)); err != nil {
			return err
		}
	}
	if err := progress("Flushing to USB", 0, 0); err != nil {
		return err
	}
	if err := dst.Sync(); err != nil {
		return err
	}
	info, err := dst.Stat()
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeDevice != 0 {
		if err := unix.IoctlSetInt(int(dst.Fd()), unix.BLKFLSBUF, 0); err != nil {
			return fmt.Errorf("flush device cache before verification: %w", err)
		}
	}
	if _, err := dst.Seek(0, io.SeekStart); err != nil {
		return err
	}
	verify := sha256.New()
	done = 0
	for done < size {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, err := io.ReadFull(dst, buf[:min(int64(len(buf)), size-done)])
		if err != nil {
			return err
		}
		verify.Write(buf[:n])
		done += int64(n)
		if err := progress("Verifying installer", int(done>>20), int((size+(1<<20)-1)>>20)); err != nil {
			return err
		}
	}
	if !strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), hex.EncodeToString(verify.Sum(nil))) {
		return fmt.Errorf("USB readback checksum mismatch")
	}
	return progress("Verified; USB installer ready", 1, 1)
}

func Write(ctx context.Context, image, path, identity string, progress func(string, int, int) error) error {
	devices, err := List(ctx)
	if err != nil {
		return err
	}
	d, err := Select(devices, path, identity)
	if err != nil {
		return err
	}
	src, err := os.Open(image)
	if err != nil {
		return err
	}
	defer src.Close()
	info, err := src.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 || uint64(info.Size()) > d.Size {
		return fmt.Errorf("image is empty, invalid, or larger than the drive")
	}
	// O_EXCL on Linux block devices refuses mounted disks/active holders. No create,
	// truncate, shell, automatic unmount, or write to a caller-supplied regular file.
	dst, err := os.OpenFile(path, os.O_RDWR|syscall.O_EXCL, 0)
	if err != nil {
		return err
	}
	defer dst.Close()
	stat, err := dst.Stat()
	if err != nil {
		return err
	}
	if stat.Mode()&os.ModeDevice == 0 || stat.Mode()&os.ModeCharDevice != 0 {
		return fmt.Errorf("target is not a block device")
	}
	// Recheck identity after opening, before the first destructive byte.
	devices, err = List(ctx)
	if err != nil {
		return err
	}
	if _, err = Select(devices, path, identity); err != nil {
		return err
	}
	// Compare the opened device with the pathname after re-enumeration as well.
	now, err := os.Stat(path)
	if err != nil || !os.SameFile(stat, now) {
		return fmt.Errorf("device replaced during admission")
	}
	return Copy(ctx, dst, src, info.Size(), progress)
}
