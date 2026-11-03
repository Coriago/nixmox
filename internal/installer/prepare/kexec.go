package prepare

import (
	"context"
	"errors"
	"time"

	"nixmox/internal/host"
	"nixmox/internal/installer/installers"
)

type Kexec struct{ Process host.Process }

func (Kexec) Definition() Definition {
	return Definition{ID: "prepare-kexec", Name: "Enter installer using kexec", Method: "kexec"}
}

func (Kexec) Check(target installers.Target, definition installers.Definition) installers.Check {
	switch {
	case target.Environment != "installed":
		return installers.Check{Reason: "host must be in the installed environment"}
	case target.Container:
		return installers.Check{Reason: "kexec is unavailable in a container"}
	case target.System != definition.System:
		return installers.Check{Reason: "kernel architecture differs from host"}
	case !target.KexecAvailable:
		return installers.Check{Reason: "kernel kexec loading is unavailable or locked down"}
	default:
		return installers.Check{Compatible: true, Preference: 100}
	}
}

func (k Kexec) Execute(ctx context.Context, boot Boot) error {
	if err := boot.Validate(); err != nil {
		return err
	}
	_, err := k.Process.Run(ctx, "", nil, "kexec", "--load", boot.Kernel,
		"--initrd="+boot.Initrd, "--command-line="+boot.CommandLine)
	if err != nil {
		return err
	}
	// systemd stops services and unmounts filesystems before executing the kernel.
	_, err = k.Process.Run(ctx, "", nil, "systemctl", "kexec")
	if err != nil {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		_, unloadErr := k.Process.Run(cleanup, "", nil, "kexec", "--unload")
		return errors.Join(err, unloadErr)
	}
	return nil
}
