package telemetry

import (
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"nixmox/internal/domain"
)

func filesystemSample(proc string, interval time.Duration) domain.FilesystemSample {
	s := domain.FilesystemSample{IntervalSeconds: interval.Seconds(), Freshness: "unknown"}
	data, err := readBounded(filepath.Join(proc, "self/mountinfo"))
	if err != nil {
		return s
	}
	items := []domain.FilesystemUsage{}
	seen := map[string]bool{}
	for line := range strings.SplitSeq(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 10 {
			continue
		}
		separator := 0
		for i := 6; i < len(fields); i++ {
			if fields[i] == "-" {
				separator = i
				break
			}
		}
		if separator == 0 || separator+2 >= len(fields) {
			continue
		}
		// Only persistent/local or network filesystems. Pseudo-filesystems do not
		// represent usable host storage; duplicate bind mounts share one device.
		switch fields[separator+1] {
		case "ext2", "ext3", "ext4", "xfs", "btrfs", "zfs", "f2fs", "vfat", "exfat", "ntfs", "ntfs3", "nfs", "nfs4", "cifs", "fuseblk", "overlay":
		default:
			continue
		}
		if seen[fields[2]] {
			continue
		}
		if len(items) >= 256 {
			return s
		}
		mount := strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`).Replace(fields[4])
		var stat syscall.Statfs_t
		if err := syscall.Statfs(mount, &stat); err != nil || stat.Bsize <= 0 || stat.Bfree > stat.Blocks {
			return s
		}
		size := uint64(stat.Bsize)
		if stat.Blocks > ^uint64(0)/size || stat.Bavail > ^uint64(0)/size {
			return s
		}
		seen[fields[2]] = true
		items = append(items, domain.FilesystemUsage{Mount: mount, TotalBytes: stat.Blocks * size, UsedBytes: (stat.Blocks - stat.Bfree) * size, AvailableBytes: stat.Bavail * size})
	}
	now := time.Now().UTC()
	s.ObservedAt = &now
	s.Freshness = "live"
	s.Items = items
	return s
}
