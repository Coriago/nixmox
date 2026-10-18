# Onboarding: first implementation

Goal: boot the existing installer, discover it from the cluster UI, generate a host
configuration, push it to Git, and install it from the bootable drive.

The form, generator, browser orchestration, Git/SOPS publication, and local installer
tasks are implemented. Destructive installation, reboot/join, and Wi-Fi reconnection
still require VM or hardware acceptance. Focused tests are not proof of those steps.

## Objectives

1. **Wi-Fi setup:** keep the installer and LAN discovery as they are. Add a Wi-Fi
   selection TUI when a usable wired connection is unavailable.
2. **Host setup form:** the discovered installer runs nixmox as a temporary,
   non-voting cluster member. Select it and click **Set up host**.
   Enter hostname, username, optional modules, and installation drive. Show disk
   model, size, and serial, and require confirmation before erasure.
3. **Generate and push:** addHost orchestration requests the SOPS public recipient,
   drive inventory, and generated hardware configuration from the installer's nixmox.
   A cluster node with Git write access and an authorized age identity renders the
   template, updates SOPS recipients, and commits and pushes the changes. The
   installer does not have those write/decryption credentials. Stop on failure.
4. **Pull and install:** the temporary member's own nixmox pulls the latest GitOps
   repository changes. Once the pull succeeds, orchestration sends a nixmox command
   to run nixos-anywhere locally against itself with the specific host configuration.
   Evaluation and building happen
   through nixos-anywhere. Show progress and logs through the cluster UI.
5. **Reboot and join:** nixos-anywhere reboots into the installed configuration,
   which includes nixmox and the cluster join settings. The temporary installer
   member disappears and is removed from the temporary-member list. The installed
   host joins under its configured hostname; verify that it is reachable.

Temporary installer members do not participate in Raft. Use nixmox communication
for orchestration and information collection; do not add SSH public keys to the
bootable image for cluster access.

Discovery listens and broadcasts on all eligible IPv4 interfaces and rechecks
them every five seconds. Selecting an installer establishes a NATS leaf link from
the coordinator to the installer's LAN address on TCP 7422. The installer has no
JetStream store or Raft vote; its commands use the coordinator's durable queue.
There is no direct HTTP installer-control fallback. Temporary inventory is marked
explicitly, excluded from replica counts, and removed after the installed host joins.

## Form and template

The Nix module supplies the form, destination path, and template below by default
when `services.nixmox.addHost = {};` is set. The repository owner can override
them; setting `form` replaces the default field set. Git credentials and SOPS
settings are configured separately. No profile framework is needed.

```nix
services.nixmox.addHost = {
  form = {
    Name = { type = "hostname"; default = "newhost"; };
    Username = { type = "username"; default = "helios"; };
    StateVersion = { type = "text"; default = "26.05"; };
    Modules = {
      type = "multiselect";
      values = [ "workstation" "development" "nvidia" "gaming" ];
      default = [ "workstation" "development" ];
    };
    Drive = { type = "disk"; required = true; };
  };
  path = "modules/hosts/{{ .Name }}/configuration.nix";
  template = ./config.gotmpl;
  ageKeyFile = "/run/keys/sops-age"; # Existing authorized identity on the Git writer.
  sops = {
    rulesFile = ".sops.yaml";
    files = [ "secrets/secrets.yaml" ];
    rulePathRegex = "secrets/secrets.yaml";
  };
};
```

Example `config.gotmpl` for nix-config:

```gotemplate
{ config, ... }: {
  meta.hosts.{{ .Name }} = {
    username = {{ .Username }};
    stateVersion = {{ .StateVersion }};
  };

  configurations.nixos.{{ .Name }} = {
    imports = [
      config.flake.modules.nixos.base
      config.flake.modules.nixos.boot
      config.flake.modules.nixos.diskLayout
{{- range .Modules }}
      config.flake.modules.nixos.{{ . }}
{{- end }}
      ({{ .Hardware }})
    ];
    diskLayout.device = {{ .Drive }};
    services.nixmox.joinSeed = {{ .ClusterAddress }};
  };
}
```

Process form values before rendering: validate hostnames, usernames, and module
choices, then serialize string values as quoted, escaped Nix literals (including
escaping Nix interpolation). Serialize each selected module name the same way.
The template uses `{{ .Name }}`, `{{ .Drive }}`, and other values directly, without
helpers or added quotes. `.Hardware` remains generated Nix source.
Orchestration supplies `.ClusterAddress` as a serialized Nix string containing a
reachable cluster member's HTTP host:port. The chosen modules must enable nixmox.

Resolve the destination path separately using validated plain form values, so Nix
quotes never enter filenames. Check that the path stays inside the repository and
does not overwrite an existing host configuration. The disk picker supplies the
selected whole-drive device path and excludes the installer drive; verify the
selected disk again before installation.

## Hardware and disks

Collect hardware on the installer with:

```sh
nixos-generate-config --show-hardware-config --no-filesystems
```

Expose the complete generated module expression as `{{ .Hardware }}`. Insert it
inline in `imports`, preserving its arguments and internal imports. Generate one
host configuration file, with no separate hardware file or wrapper settings.
Templates can omit `.Hardware` if they do not need generated hardware configuration.

Disko belongs to the repository. For nix-config, add a shared `diskLayout` module
that accepts `diskLayout.device` and defines the desired partitions and filesystems.
The form supplies only the drive.

## Git, SOPS, and installation

- Route repository preparation to a cluster node with Git push access and an age
  identity authorized to update the selected encrypted files, regardless of which
  node serves the UI. Request installer information through nixmox.
- Obtain the installer's SSH host public age recipient through nixmox, add it to the selected SOPS
  rule, and update the encrypted files with `sops updatekeys`. Preserve that SSH
  private host key into the installed system so the new host can decrypt its secrets.
- Render in an isolated checkout. Commit the generated configuration and SOPS changes
  together, then push to Git. The temporary member pulls the latest changes through
  its own nixmox GitOps workflow and records the pulled revision for the install.
  Do not put private keys or plaintext credentials in Git or logs.
- The temporary member uses the cluster's configured GitOps repository and its
  assigned host configuration name. It needs repository read access. Pulling and
  installation are local nixmox tasks, with status and logs visible in the cluster UI.
- After the pull succeeds, send the install command through nixmox with the selected
  host configuration. The installer's nixmox runs nixos-anywhere against itself.
  Prove this local invocation in the first spike. Any local transport setup required
  by nixos-anywhere stays inside the installer task; no cluster SSH keys are embedded
  in the image or used to orchestrate the installer.
- Before reboot, copy the bootable system's working Wi-Fi connection and credentials
  into the installed system when compatible with its configured network manager.
  Transfer them locally with appropriate file permissions, never through Git or task
  logs. The selected repository modules must enable the corresponding networking.
  If the connection cannot be transferred, show that clearly before installation
  and require an alternative connection plan so the host can reconnect after reboot.
- Installation failures retain logs and show a failed state. Retrying uses the same
  recorded pulled revision and requires explicit confirmation before any repeated disk erasure.

## Implementation order and done criteria

1. Add the Wi-Fi TUI; verify wired boot still works and wireless boot can connect
   and become discoverable through the existing discovery mechanism.
2. Prove nixos-anywhere can run on the live installer against itself, install a
   known configuration onto a selected disk, and reboot successfully.
3. Add the form, hardware template value, SOPS update, and Git commit/push workflow.
4. Connect information collection, GitOps pull, and the local install command through
   nixmox. Complete one install from discovery through reboot and installed-host join
   without terminal commands. Verify the temporary installer membership is gone.
5. Verify a Wi-Fi installation reconnects after reboot using the copied credentials,
   without asking the user to enter them again. Cover unsupported transfer explicitly.

For application changes, extend `e2e/run.py` and run both parts of the comprehensive
three-node E2E against the changed source, as required by AGENTS.md. Record source
mode, tested SHA, final checkpoint, and artifact directory. Report Wi-Fi and the
local installer execution checks separately; do not claim coverage not exercised.
