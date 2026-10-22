# Configuration generation

Generate a named configuration without connecting a machine. Machine observations,
disk binding, and optional secret updates belong to [onboarding](host-onboarding.md).
Forms and output paths remain repository-defined; no flake-parts layout is required.

## Repository template contract

Proposed location: `.nixmox/templates/server.json` plus template files in the same
repository. Load these from a pinned repository commit, not an arbitrary web URL.

```json
{
  "schema_version": 1,
  "id": "server",
  "kind": "configuration",
  "label": "NixOS server",
  "fields": [
    {"name": "Name", "type": "hostname", "required": true},
    {"name": "Username", "type": "text", "required": true, "default": "admin"},
    {"name": "Modules", "type": "multiselect", "required": false,
     "choices": ["base", "development"], "default": ["base"]}
  ],
  "outputs": [
    {"path": "hosts/{{ .Name }}/configuration.nix", "template": "server.nix.gotmpl"}
  ],
  "configuration": {
    "name": "{{ .Name }}",
    "flake_directory": ".",
    "flake_attribute": "nixosConfigurations.{{ .Name }}"
  },
  "preparation_templates": {
    "hardware": ["linux-hardware"],
    "disk": ["ext4-layout", "existing-layout"]
  }
}
```

Supported field types: `text`, `hostname`, `boolean`, `integer`, `select`,
`multiselect`, plus `disk` and `partition` in target-preparation forms. Ordered fields preserve form
order. Select values must be from `choices`; unknown values/fields are rejected.
A `disk` field is a selector populated from the target's eligible observed disks,
not a free-text field or a requirement for generating an unassigned configuration.

Template kinds are `configuration`, `hardware`, and `disk`. Lists in
`preparation_templates` select compatible templates for later target preparation;
they do not run during configuration generation. Existing configurations can supply
the same lists in their configuration catalog entry.

## Template selection

Resolve templates from the selected repository revision and stage:

| Available templates | Browser and TUI | CLI/API without template_id |
| --- | --- | --- |
| One | Skip selection and open its form; show template name in review | Select it automatically |
| More than one | Show the selector | Interactive CLI prompts; noninteractive/API returns `TEMPLATE_SELECTION_REQUIRED` with choices |
| None | Show how to configure a template or choose an existing configuration | Return `TEMPLATE_UNAVAILABLE` |

An explicitly supplied ID must be available for that stage. Record the resolved ID
and source commit in the draft/preparation, even when selected automatically.
A later catalog change must not switch the template for an in-progress task.
Counting only configuration templates means one configuration template plus several
hardware/disk templates still skips selection on Generate.

Template proposal: Go-style templates with one `nix` serializer for strings,
booleans, integers, and lists. Values are raw typed values until serialized.
Paths use validated values and must remain relative to the repository; reject
`..`, absolute paths, symlink escapes, and duplicate output paths.
Output `format` defaults to `nix`; `json` and `text` are also supported. Validate
the declared syntax and use the `json` serializer for JSON output.

```gotemplate
{ ... }: {
  imports = map (name: ../../modules + "/${name}.nix") {{ nix .Modules }};
  networking.hostName = {{ nix .Name }};
  users.users.{{ nix .Username }}.isNormalUser = true;
}
```

The Username field becomes a quoted Nix attribute, not raw code. Do not treat
arbitrary field values as Nix code. The serializer must escape Nix interpolation `${...}`.
The example emits a host module; the repository's flake must export the declared
attribute. Templates may emit additional files to provide that wiring.
Here the repository supplies `modules/base.nix` and `modules/development.nix`;
enumerated Modules values select those imports.

## Selecting configurations without generation

By default, enumerate names under `nixosConfigurations` at the selected commit.
Repositories with other layouts provide `.nixmox/configurations.json`:

```json
{
  "schema_version": 1,
  "configurations": [
    {
      "name": "cedar",
      "flake_directory": ".",
      "flake_attribute": "configurations.nixos.cedar"
    }
  ]
}
```

These descriptors replace automatic enumeration for that repository, so display
names remain stable across internal layout changes. Generation must update the
descriptor when needed and validate the resulting exported attribute. The files
describe how to find a configuration; they do not make Nixmox depend on flake-parts.
`nixmox configurations list --repository lab` and the browser/TUI configuration
picker use the same catalog, with the inspected commit displayed.

`configurations list` lists across repositories; `--repository` filters it.
Each configuration defaults to its tracked ref; names found only in the upstream
catalog use the repository's accepted upstream head. Reads never fetch implicitly.
`--ref` or `--commit` selects another revision and requires `--repository`; they
are mutually exclusive. HTTP uses `repository_id`, `ref_id`, and `commit` with the
same rule. Return `inspected_commit` per item and bind pagination to those revisions.
Templates use `/repositories/{id}/templates` with optional `ref_id` or `commit`,
defaulting to the accepted upstream head. CLI `configurations templates <repository>`
accepts the corresponding `--ref`/`--commit`. Missing cached source is an explicit
error, not a substituted revision.

## Draft and configuration objects

```json
{
  "schema_version": 1,
  "id": "draft_01",
  "version": 2,
  "repository_id": "r_lab",
  "base_commit": "0000000000000000000000000000000000000001",
  "template_id": "server",
  "values": {"Name": "cedar", "Username": "admin", "Modules": ["base"]},
  "configuration_name": "cedar",
  "files": [{"path": "hosts/cedar/configuration.nix", "change": "add"}],
  "validation": {"state": "incomplete", "missing": ["hardware", "disk_binding"]},
  "assigned_host_id": null
}
```

Draft bytes live in a private local worktree; persist the draft record and report
its owning node. Until committed/replicated, a draft is not highly available.
A lost owner makes the draft unavailable, never an empty successful draft.

```json
{
  "schema_version": 1,
  "id": "cfg_cedar",
  "version": 1,
  "repository_id": "r_lab",
  "name": "cedar",
  "flake_directory": ".",
  "flake_attribute": "nixosConfigurations.cedar",
  "ref_id": "ref_cedar",
  "readiness": "needs_preparation"
}
```

No assigned host is required. Multiple hosts may refer to one named configuration;
application-specific uniqueness constraints must be validated separately.

## Create and commit

```sh
nixmox repositories import lab --url https://github.com/example/nixos-config.git --upstream main --wait
nixmox configurations templates lab
nixmox configurations generate lab --set Name=cedar --set Username=admin --wait
nixmox configurations draft show draft_01 --diff
nixmox configurations draft edit draft_01 --set Username=operator --expected-version 2 --wait
nixmox configurations draft validate draft_01 --wait
nixmox configurations draft commit draft_01 --expected-version 3 --wait
```

Generation returns `draft_01`; commit returns configuration, branch, commit, and
artifact IDs. Edits to an already tracked upstream configuration create a change
branch only when committed changes exist. Never create one branch for every host.
The example automatically selects the sole configuration template, `server`.
With multiple choices, add `--template server` for noninteractive use.
Generation accepts `--ref` or `--commit` (mutually exclusive); default to the
repository's accepted upstream head. Resolve and persist `base_commit` before
rendering. API callers may supply `ref_id` or `base_commit`; omission uses that
same default. No implicit fetch occurs. Form values use defaults, then explicit
`--set` overrides; interactive clients prompt for missing required values,
noninteractive clients return field validation errors.

| Operation | Command type / payload |
| --- | --- |
| Generate | `configuration.generate` / repository_id, values; optional template_id and mutually exclusive ref_id/base_commit |
| Edit draft | `configuration.draft.edit` / draft_id, values (typed field patch); expected draft version |
| Validate | `configuration.validate` / draft_id or pinned source |
| Commit | `configuration.commit` / draft_id; expected draft and repository versions |

Reads: `/repositories/{id}/templates`, `/drafts/{id}`, `/drafts/{id}/diff`,
`/configurations/{id}`. Committing hands file changes to GitOps; it does not build,
install, update secrets, or publish.

Draft detail/diff return the reviewed `expected_versions` for draft and repository;
commit copies that map and checks `--expected-version` against its draft entry.
An intervening repository edit requires explicit re-render/review, never automatic
substitution of its new version. Browser/TUI form edits use the same field-patch
operation and invalidate validation. Rebase drafts share these read routes:
`GET /api/v1/drafts/{id}/files` lists `{path, bytes, sha256}` and
`GET /api/v1/drafts/{id}/files/content?path=<relative-path>` streams one file at the
optional `version` (defaults to current). Reject unsafe paths; file-list pages and
file responses report the exact draft version so clients can pin subsequent reads.
File entries are manifest rows, ordered by path instead of resource ID; use common
cursor/limit bounds with the requested draft version pinned for the whole listing.

## Browser and TUI

| Step | Browser | TUI |
| --- | --- | --- |
| Generate | Configurations → New → lab → form directly when Server is the only template | Configurations → New → lab → same form without a template prompt |
| Enter values | Name=cedar, Username=admin, Modules=base | Same fields; Tab through controls, Space toggles choices |
| Review | Preview files and validation panel | Review → files list → Enter opens diff |
| Save | Commit locally | Commit locally → Enter |

Both show “Saved locally; needs hardware preparation” rather than “Ready to install”.
A saved unassigned configuration remains selectable from any console once replicated.

## Validation and failures

Validation levels: `invalid`, `incomplete`, `ready`. Check field types, safe paths,
Nix syntax, declared configuration export, and available evaluation prerequisites.
No system build is performed. Missing hardware may produce `incomplete`, which
allows local draft commit; syntax/path errors do not. Installation requires `ready`.
Validation records the exact draft version/commit; editing invalidates it.
Disable import-from-derivation during lightweight evaluation so validation cannot
silently start builds. Report configurations requiring it as unsupported by this
validation mode; offer an explicitly separate build operation.

Never overwrite a file silently. Conflicting repository updates return a version
conflict and retain the draft. Template/schema errors identify a field or file.

Hardware and disk rendering use dedicated templates, specified below. Onboarding
owns observations, user selections, and execution; these templates own file layout
and formatting.

Acceptance: generate without a host or credentials; render the same values in all
interfaces; reject traversal/interpolation mistakes; commit incomplete hardware
state; preserve a draft after validation/conflict failure; later prepare that exact
configuration through onboarding; skip selection only when exactly one compatible
template exists; preserve the selected template when the catalog changes.

## Dedicated hardware template

A hardware template declares collection settings separately from its rendering.
It receives preserved configuration form values plus service-collected hardware.
The repository controls whether the result is a standalone module, a module wrapped
in its own configuration namespace, or another explicitly declared output format.
Hardware/disk templates may declare `fields` using the same schema as configuration
templates. Preserve prior form values when available; for existing configurations,
prompt for missing template values rather than guessing them from a hostname.
Reserve `.Hardware` for service-owned observations; fields cannot override it.
Disk fields resolve to service-owned device records before rendering.

`.nixmox/templates/linux-hardware.json`:

```json
{
  "schema_version": 1,
  "id": "linux-hardware",
  "kind": "hardware",
  "label": "Hardware without filesystem declarations",
  "collection": {
    "method": "nixos-generate-config",
    "include_filesystems": false,
    "root": "target"
  },
  "outputs": [{
    "path": "generated/hosts/{{ .Name }}/hardware.nix",
    "template": "hardware.nix.gotmpl",
    "format": "nix"
  }]
}
```

`hardware.nix.gotmpl`, for a file explicitly imported into this host only:

```gotemplate
{{ module .Hardware.Module }}
```

The `module` helper emits a parsed, service-produced Nix module expression. It does
not accept arbitrary strings from form fields or API clients. Do not pass that
expression through `nix`, which would turn it into a quoted string. Also expose
structured observations under `.Hardware.Facts` for repository templates that emit
only selected settings, for example:

```gotemplate
{ ... }: {
  boot.initrd.availableKernelModules = {{ nix .Hardware.Facts.InitrdAvailableKernelModules }};
  boot.kernelModules = {{ nix .Hardware.Facts.KernelModules }};
}
```

Facts are typed lists collected from the target; absent facts are unknown, not an
empty list. A template referencing an unavailable fact fails with its field name.
Additional facts require versioned schema fields; templates cannot execute collectors.

`include_filesystems = false` omits filesystem and swap declarations from the
collected module. With `true`, collect them from the verified target root. A live
installer's own mounts must never become the installed host's filesystem config.
If the future root is not available, mark this template incomplete and offer a
no-filesystems template or repository-defined layout. Do not partition disks just
to make configuration generation succeed. With disko managing filesystems, default
to no-filesystems; conflicting declarations must fail evaluation explicitly.
The `root` policy applies to filesystem collection; no-filesystems hardware
collection can run on the live machine before an installed root exists.

## Dedicated disk templates

Disk selection uses the same `fields` form contract. A field named `SystemDisk`
with `type: "disk"` displays a device selector. Its submitted value is an observed
disk ID; the service resolves it to `.SystemDisk.Path`, `.Serial`, `.Model`, and
`.SizeBytes` before rendering. There is no separate role mapping: the template
decides which disko option uses each field.

For preserved boot filesystems, a `partition` field selects an observed existing
partition, not an erase target. Resolve `.Path` to its stable filesystem/partition
identifier, and expose `.FsType` and `.ParentDiskId` for compatibility checks. For
example, Pi SD/NVMe preparation uses `RootDisk` (`disk`) plus `BootPartition` and
`FirmwarePartition` (`partition`); only `RootDisk` enters Disko's destructive layout.
Render preserved mounts through `fileSystems` and authorize their boot-file updates
separately in installation review. Reject stale IDs, incorrect filesystem types,
duplicate partitions, and overlap with any erase target. Multiple preserved
partitions on one SD card are valid; installer-root partitions remain ineligible.

Show path, model, serial, and capacity for every choice. Do not automatically select
a physical disk, even when only one is eligible. Reject missing required selections,
ineligible/stale IDs, and the same device selected in multiple disk fields. Validate
again before destructive execution. Two rendering modes are supported.

### Generate from disko templates

The repository opts into a named template from a pinned disko-templates revision.
The source below is a structural fixture: replace the sample revision with a real
immutable commit during setup. Review the selected source before installing.

```json
{
  "schema_version": 1,
  "id": "ext4-layout",
  "kind": "disk",
  "label": "Generate single disk ext4 layout",
  "mode": "generate",
  "source": {
    "repository": "https://github.com/nix-community/disko-templates.git",
    "revision": "2222222222222222222222222222222222222222",
    "template": "single-disk-ext4"
  },
  "fields": [
    {"name": "SystemDisk", "label": "System disk", "type": "disk", "required": true}
  ],
  "assets": [{
    "source_path": "single-disk-ext4/disko-config.nix",
    "path": "vendor/disko/single-disk-ext4.nix"
  }],
  "outputs": [{
    "path": "generated/hosts/{{ .Name }}/disk.nix",
    "template": "ext4-disk.nix.gotmpl",
    "format": "nix"
  }]
}
```

`ext4-disk.nix.gotmpl`:

```gotemplate
{ ... }: {
  imports = [ ../../../vendor/disko/single-disk-ext4.nix ];
  disko.devices.disk.main.device = {{ nix .SystemDisk.Path }};
}
```

Copy only explicitly listed assets, record source commit/digests in preparation,
and commit them with the rendered binding. Reuse identical assets; changed or
conflicting vendor files require review rather than overwrite. No floating fetch
or arbitrary source command runs during rendering. The selected NixOS configuration
must import the disko NixOS module separately.

The single-disk-ext4 template supplies a layout whose `main.device` can be set by
the importing configuration. Other layouts may use different disk names and require
a renderer that assigns the selected fields to those names.
[Disko template example](https://github.com/nix-community/disko-templates/blob/main/single-disk-ext4/disko-config.nix)

### Bind a disk to a repository layout

The repository already supplies/imports its disko module; only the selected device
needs to be rendered. Do not copy or regenerate its partition layout.

```json
{
  "schema_version": 1,
  "id": "existing-layout",
  "kind": "disk",
  "label": "Use repository disk layout",
  "mode": "bind",
  "fields": [
    {"name": "SystemDisk", "label": "System disk", "type": "disk", "required": true}
  ],
  "outputs": [{
    "path": "generated/hosts/{{ .Name }}/disk.nix",
    "template": "disk-binding.nix.gotmpl",
    "format": "nix"
  }]
}
```

`disk-binding.nix.gotmpl`:

```gotemplate
{ ... }: {
  disko.devices.disk.main.device = {{ nix .SystemDisk.Path }};
}
```

Authors can instead emit their repository's custom disk option or a host-scoped
wrapper. Validate the final evaluated `disko.devices`, not just the generated
binding: every destructive disk must resolve to a selected observed device.
If another module hard-codes a conflicting disk, show that conflict; never insert
`mkForce` or silently modify the repository's layout. The disk step is independent
of whether the configuration was generated or selected from an existing repository.

### Two disk example

A repository layout defines `main` for the operating system and `data` for storage.
The form asks for both devices; the renderer supplies only their bindings.

```json
{
  "schema_version": 1,
  "id": "system-and-data",
  "kind": "disk",
  "label": "System and data disks",
  "mode": "bind",
  "fields": [
    {"name": "SystemDisk", "label": "System disk", "type": "disk", "required": true},
    {"name": "DataDisk", "label": "Data disk", "type": "disk", "required": true}
  ],
  "outputs": [{
    "path": "generated/hosts/{{ .Name }}/disk.nix",
    "template": "two-disks.nix.gotmpl",
    "format": "nix"
  }]
}
```

`two-disks.nix.gotmpl`:

```gotemplate
{ ... }: {
  disko.devices.disk.main.device = {{ nix .SystemDisk.Path }};
  disko.devices.disk.data.device = {{ nix .DataDisk.Path }};
}
```

CLI:

```sh
nixmox onboarding prepare cedar --configuration cfg_cedar --observation obs_01 \
  --disk-template system-and-data \
  --set disk.SystemDisk=disk_01 --set disk.DataDisk=disk_02 --wait
```

Browser/TUI: Preparation → Disks → System and data disks → select System disk and
Data disk → Review. If this is the only compatible disk template, open the two
fields directly. Include `system-and-data` in the configuration's compatible disk
template list to make it available.

The corresponding `onboarding.prepare` payload includes:

```json
{
  "host_id": "n_cedar",
  "configuration_id": "cfg_cedar",
  "observation_id": "obs_01",
  "disk_template_id": "system-and-data",
  "template_values": {
    "disk": {"SystemDisk": "disk_01", "DataDisk": "disk_02"}
  }
}
```

Given observed paths `/dev/disk/by-id/nvme-EXAMPLE` and
`/dev/disk/by-id/ata-DATA`, the rendered assignments are:

```nix
{ ... }: {
  disko.devices.disk.main.device = "/dev/disk/by-id/nvme-EXAMPLE";
  disko.devices.disk.data.device = "/dev/disk/by-id/ata-DATA";
}
```

The selected repository layout supplies partitions/filesystems for both named
disks. Review and confirm both devices before installation. The same field pattern
can bind two members of a mirror when the chosen layout defines one.

## Output paths and imports

Templates specify output paths. If hardware and disk files are not automatically
imported by the repository, the configuration template should include imports with
`lib.optional` so it can exist before those files are generated. Example fragment:

```nix
{ lib, ... }: {
  imports =
    lib.optional (builtins.pathExists ../../generated/hosts/cedar/hardware.nix)
      ../../generated/hosts/cedar/hardware.nix
    ++ lib.optional (builtins.pathExists ../../generated/hosts/cedar/disk.nix)
      ../../generated/hosts/cedar/disk.nix;
}
```

Missing required files still mean `needs_preparation`, not ready to install.

## Regeneration

Record per-output path, template revision, and generated-content digest in
preparation history. On regeneration, compare against both the previous generated
content and the file at the selected commit. Manual edits require explicit merge
or replacement review. Path changes show additions and proposed removals together;
never delete an obsolete generated file or leave a stale import silently.

Acceptance: hardware with/without filesystems; imported vendor layout versus
binding-only; single- and two-disk forms; duplicate/stale disk selection rejection;
conflicting disk option; optional imports before preparation; repeated preparation
preserving manual edits.
