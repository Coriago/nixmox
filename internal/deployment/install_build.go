package deployment

import (
	"context"
	"encoding/json"
	"errors"

	"nixmox/internal/installer"
	"nixmox/internal/task"
)

func (s Service) InstallationBuild(ctx context.Context, execution *task.Execution) (any, error) {
	return s.buildConfiguration(ctx, execution, true)
}

func (s Service) buildConfiguration(ctx context.Context, execution *task.Execution, install bool) (any, error) {
	var input Input
	if err := json.Unmarshal(execution.Task().Inputs, &input); err != nil {
		return nil, err
	}
	work, directory, name, err := s.Configurations.Materialize(ctx, input.ConfigurationID, input.ConfigurationVersion)
	if err != nil {
		return nil, err
	}
	defer work.Close()
	log := execution.LogWriter(ctx)
	defer log.Close()
	builder := s.Builder
	builder.Process.Log = log
	attribute := "configurations.nixos." + name + ".config"
	if !install {
		return builder.BuildPath(ctx, directory, attribute+".system.build.toplevel")
	}
	// Evaluate only the installation contract from the exact rendered version.
	// NixOS/disko own device layout, scripts, service configuration and bootloader.
	const projection = `c: {
  root = c.disko.rootMountPoint;
  devices = map (disk: disk.device) (builtins.attrValues c.disko.devices.disk);
  destination = { stateDir = c.services.nixmox.config."state-dir"; sshHostKey = c.services.nixmox.config."ssh-host-key"; };
  recoverable = c.services.nixmox.enable
    && c.services.nixmox.config.environment == "installed"
    && c.services.openssh.enable
    && c.services.openssh.settings.PermitRootLogin != "no"
    && c.users.users.root.openssh.authorizedKeys.keys != []
    && builtins.elem c.services.nixmox.config."ssh-host-key" (map (key: key.path) c.services.openssh.hostKeys)
    && (c.boot.loader.grub.enable || c.boot.loader.systemd-boot.enable);
 }`
	data, err := builder.Process.Run(ctx, directory, nil, "nix", "eval", "--json", "--apply", projection, "path:"+directory+"#"+attribute)
	if err != nil {
		return nil, err
	}
	var evaluated struct {
		Installation
		Recoverable bool `json:"recoverable"`
	}
	if err = json.Unmarshal(data, &evaluated); err != nil {
		return nil, err
	}
	if !evaluated.Recoverable {
		return nil, errors.New("installed Nixmox, preserved SSH host key, root authorized key, and bootloader are required")
	}
	if _, err = installer.DestinationPath(evaluated.Destination.StateDir); err != nil {
		return nil, err
	}
	if _, err = installer.DestinationPath(evaluated.Destination.SSHHostKey); err != nil {
		return nil, err
	}
	plan := evaluated.Installation
	for _, output := range []struct {
		attribute   string
		destination *string
	}{
		{"system.build.toplevel", &plan.SystemPath},
		{"system.build.diskoScript", &plan.DiskoScript},
		{"system.build.mountScript", &plan.MountScript},
	} {
		built, err := builder.BuildPath(ctx, directory, attribute+"."+output.attribute)
		if err != nil {
			return nil, err
		}
		if len(built.StorePaths) != 1 {
			return nil, errors.New("installation build requires one store output per artifact")
		}
		*output.destination = built.StorePaths[0]
	}
	return plan, nil
}
