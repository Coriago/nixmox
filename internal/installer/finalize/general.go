package finalize

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/nix-community/go-nix/pkg/storepath"

	"nixmox/internal/host"
	"nixmox/internal/installer/installers"
)

type General struct{ Process host.Process }

func (General) Definition() Definition {
	return Definition{ID: "finalize-general", Name: "Finalize a NixOS installation", Method: "general"}
}

func (General) Check(installation Installation) installers.Check {
	target := installation.Target
	switch {
	case target.Environment != "installer":
		return installers.Check{Reason: "finalization requires an installer environment"}
	case target.Container:
		return installers.Check{Reason: "cannot finalize from a container"}
	case target.System != "x86_64-linux" && target.System != "aarch64-linux":
		return installers.Check{Reason: "unsupported system"}
	case target.System == "aarch64-linux" && target.Firmware != "uefi":
		return installers.Check{Reason: "board-specific finalizer required"}
	case !filepath.IsAbs(installation.Root) || filepath.Clean(installation.Root) == "/":
		return installers.Check{Reason: "a separate installed root is required"}
	case !storepath.PathRe.MatchString(installation.SystemPath):
		return installers.Check{Reason: "installed NixOS system store path required"}
	default:
		return installers.Check{Compatible: true, Preference: 10}
	}
}

func (g General) Execute(ctx context.Context, installation Installation) error {
	if check := g.Check(installation); !check.Compatible {
		return errors.New(check.Reason)
	}
	if installation.Preserve == nil {
		return errors.New("identity/network/task preservation is not configured")
	}
	root, err := filepath.EvalSymlinks(installation.Root)
	if err != nil {
		return err
	}
	destination, err := os.Stat(root)
	if err != nil {
		return err
	}
	running, err := os.Stat("/")
	if err != nil {
		return err
	}
	if root == "/" || os.SameFile(destination, running) {
		return errors.New("refusing to finalize the running root or a bind mount of it")
	}
	// Require a mounted destination, not an arbitrary directory on the live root.
	if _, err = g.Process.Run(ctx, "", nil, "mountpoint", "--quiet", root); err != nil {
		return fmt.Errorf("installed root is not a mountpoint: %w", err)
	}
	if err = installation.Preserve(ctx, root); err != nil {
		return err
	}
	// Delegate profile setup, activation and bootloader installation to NixOS.
	// --system selects the already built closure; it does not evaluate a flake.
	_, err = g.Process.Run(ctx, "", nil, "nixos-install", "--root", root, "--system",
		installation.SystemPath, "--no-root-password", "--no-channel-copy")
	return err
}
