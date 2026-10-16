package media

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCandidatesAndIdentity(t *testing.T) {
	mount := "/"
	swap := "[SWAP]"
	base := Device{Path: "/dev/sdb", Type: "disk", Transport: "usb", Serial: "SERIAL", Model: "Flash", Size: 8 << 30, MajorMinor: "8:16"}
	for _, tc := range []struct {
		name     string
		change   func(*Device)
		eligible bool
	}{
		{"unmounted", func(d *Device) {}, true},
		{"mounted partition", func(d *Device) { d.Children = []Device{{Type: "part", Mountpoints: []*string{&mount}}} }, false},
		{"swap partition", func(d *Device) { d.Children = []Device{{Type: "part", Mountpoints: []*string{&swap}}} }, false},
		{"read only", func(d *Device) { d.ReadOnly = true }, false},
		{"no identity", func(d *Device) { d.Serial = "" }, false},
		{"mapping", func(d *Device) { d.Children = []Device{{Type: "crypt"}} }, false},
		{"internal", func(d *Device) { d.Transport = "nvme" }, false},
		{"partition", func(d *Device) { d.Type = "part" }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := base
			tc.change(&d)
			data, _ := json.Marshal(map[string]any{"blockdevices": []Device{d}})
			list, err := Candidates(data)
			if err != nil {
				t.Fatal(err)
			}
			id := ""
			if len(list) > 0 {
				id = list[0].Identity
			}
			_, err = Select(list, d.Path, id)
			if (err == nil) != tc.eligible {
				t.Fatalf("eligible=%v error=%v", tc.eligible, err)
			}
			if _, err = Select(list, d.Path, "stale"); err == nil {
				t.Fatal("accepted stale drive identity")
			}
		})
	}
}

func TestCopyAndVerify(t *testing.T) {
	data := bytes.Repeat([]byte("installer bytes"), 500000)
	src, err := os.CreateTemp(t.TempDir(), "source")
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	if _, err = src.Write(data); err != nil {
		t.Fatal(err)
	}
	src.Seek(0, 0)
	dst, err := os.CreateTemp(t.TempDir(), "usb-test-file")
	if err != nil {
		t.Fatal(err)
	}
	defer dst.Close()
	phases := map[string]bool{}
	err = Copy(context.Background(), dst, src, int64(len(data)), func(phase string, done, total int) error { phases[phase] = true; return nil })
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(dst.Name())
	if !bytes.Equal(got, data) {
		t.Fatal("image bytes differ")
	}
	for _, phase := range []string{"Writing installer", "Flushing to USB", "Verifying installer", "Verified; USB installer ready"} {
		if !phases[phase] {
			t.Fatal("missing phase", phase)
		}
	}
}

func TestCopyCancellationAndCorruption(t *testing.T) {
	for _, cancelled := range []bool{true, false} {
		src, _ := os.CreateTemp(t.TempDir(), "source")
		defer src.Close()
		src.WriteString("installer")
		src.Seek(0, 0)
		dst, _ := os.CreateTemp(t.TempDir(), "destination")
		defer dst.Close()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		if cancelled {
			cancel()
		}
		err := Copy(ctx, dst, src, 9, func(phase string, _, _ int) error {
			if phase == "Flushing to USB" {
				_, e := dst.WriteAt([]byte("bad"), 0)
				return e
			}
			return nil
		})
		if err == nil {
			t.Fatal("expected cancellation or checksum error")
		}
		if !cancelled && !strings.Contains(err.Error(), "checksum") {
			t.Fatal(err)
		}
	}
}

func TestImageRejectsNonStoreISO(t *testing.T) {
	dir := t.TempDir()
	os.Mkdir(filepath.Join(dir, "iso"), 0700)
	os.WriteFile(filepath.Join(dir, "iso", "bad.iso"), []byte("bad"), 0600)
	if _, err := Image(dir); err == nil {
		t.Fatal("accepted mutable image")
	}
}
