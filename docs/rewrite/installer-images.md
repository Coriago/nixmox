# Installer images

## Profile model

Select a named **installer profile**, not architecture and format independently.
A profile combines target system, board/boot support, image layout, and the shared
Nixmox installer module. All profiles provide the same TUI, browser, networking,
discovery, and pending-node flow described in [onboarding](host-onboarding.md).
An installer profile is reusable; it does not assign the installed host's configuration.
Built-in profiles are complete recipes: users select one without writing Nix.

| Layer | Defaults supplied |
| --- | --- |
| Shared installer module | Daemon, automatic installer TUI, browser, wired/Wi-Fi setup, discovery/enrollment, installation tools; fresh identity at boot; join settings submit a request awaiting cluster approval |
| Hardware/image profile | Architecture, kernel, drivers, firmware, bootloader, partition layout, image format |

Images contain no assigned permanent host configuration, shared machine identity,
or private credentials. Customization uses ordinary Nix modules and exported image
derivations, not a separate hardware configuration language.
Fresh boots generate ephemeral identity; authorized media/kexec handoffs restore
the host identity before agent startup. Follow the
[state inventory](identity-and-trust.md#generated-and-preserved-state) and
[installer identity rules](identity-and-trust.md#reusable-installer-identity).

| Initial profile | Target system | Image | Boot media |
| --- | --- | --- | --- |
| `pc-x86_64` | `x86_64-linux` | Hybrid ISO | USB; declare tested UEFI/BIOS modes |
| `raspberry-pi-5` | `aarch64-linux` | Raw disk image, optionally compressed | SD initially; USB only with a validated boot recipe |
| Custom | Declared by recipe | ISO or raw disk image | Explicitly declared and tested |

Architecture is insufficient to establish board compatibility. Each recipe pins
Nixpkgs, hardware modules, kernel, firmware, bootloader, and Nixmox through its
source/lock file. Record board models, supported boot media, firmware prerequisites,
and test evidence with the profile. Label untested custom profiles accordingly.

Pi profiles declare one of two [reboot adapters](deployment-and-rollback.md#reboot-adapters):
`rpi-uboot-sd` keeps firmware/U-Boot and both systems' boot files on SD, with NVMe
root for the installed system; `rpi-eeprom` provides independent SD/NVMe boot chains
and changes firmware boot priority. An NVMe root-only layout cannot use the latter.
Do not assume the nixos-hardware U-Boot recipe supports boot-file reads from NVMe
or USB on Pi 5. Custom profiles explicitly declare their supported boot media.
[Pi hardware/boot integration](https://github.com/NixOS/nixos-hardware/blob/master/raspberry-pi/README.md)

## Build and write

Profiles with tested hardware support also supply a kexec kernel/initrd payload
using the same installer module. Record its digest and advertise unsupported
variants explicitly; an ISO/raw image is not a kexec payload. Entry follows the
[deployment contract](deployment-and-rollback.md#reboot-into-installer-and-fresh-install).

The normal workflow builds on the machine with the destination device attached,
then writes there. Builder selection is an advanced override, not a required step.
USB drives and SD cards are equally supported write targets, including SD cards
in built-in or USB readers. Show model, capacity, and device identity; the reader's
USB connection does not establish whether the image can boot on the chosen board.

```sh
nixmox media profiles
# On alder, with the destination device attached: build and write locally.
nixmox media build --profile pc-x86_64 --host alder --wait
nixmox media plan --image media_01 --host alder --device /dev/disk/by-id/usb-EXAMPLE --wait
nixmox media write --plan media_plan_01 --confirm-device /dev/disk/by-id/usb-EXAMPLE --wait
# Optional: build a Pi image elsewhere, then transfer it to alder for writing.
nixmox media build --profile raspberry-pi-5 --host alder --builder arm-builder --wait
nixmox media plan --image media_02 --host alder --device /dev/disk/by-id/usb-SD_READER --wait
nixmox media write --plan media_plan_02 --confirm-device /dev/disk/by-id/usb-SD_READER --wait
```

`--host` identifies the intended writing node and may be omitted when using its
local daemon; remote-console CLI usage requires an explicit host. It supplies
builder preference, not permission to write. Device selection and confirmation
remain separate. Build-only/download use follows the same preference without
requiring an attached device.

Browser/TUI: Media → Create installer → profile + writing node/device → Build →
review → Write. Preselect the current console's node as writer, allow changing it,
and show the selected builder without requiring a separate builder screen.
Download-only skips device selection. Show architecture, board, supported boot
media, and prerequisites before building. One available profile skips selection;
multiple profiles prompt, or require `--profile` for noninteractive commands.
The writer can be x86 even when the image is ARM; writing bytes does not execute
the target system. SD readers use the same device identity/confirmation rules as USB.
A pending bootstrap agent may be the writer for its own installation media, with
scoped media-write authorization and device confirmation. No permanent agent or
assigned host configuration is required to prepare the media.

Builder selection: honor an explicit `--builder`; otherwise prefer the writer
when it can execute the recipe's required build system, natively or through already
configured, capability-checked emulation. Label emulated builds as potentially
slower. Only fall back to another eligible node when the writer cannot build or is
unavailable, and show the reason and selected node. Do not silently replace an
explicit builder. Changing the writer later does not rebuild a valid image.
Do not automatically configure emulation or assume cross-compilation works for an
arbitrary NixOS image. Explicitly supported cross-build recipes must declare their
build and target systems separately. No compatible builder returns
`BUILDER_UNAVAILABLE` with the missing system/capability.

When builder and writer differ, apply the
[cache readiness rules](cache-gateway.md#serving-readiness-and-cross-host-builds).
Show unavailable remote builders disabled with the cache reason; same-host builds
and verified local image writes do not require a signing key.

For a remote build, the writer pulls the completed image's Nix store output through
its local [cache gateway](cache-gateway.md) and verifies its digest before writing.
Build, transfer, write, and readback have separate task
phases/progress. Build failure or interrupted transfer must not start a device write.
JetStream carries metadata and progress, not image bytes. There is no transfer step
when the verified artifact is already available on the writer.

Built-in profiles should offer release images with verified digests/provenance for
Download/Write without requiring an ARM builder to bootstrap the first Pi. Building
customizations still requires a compatible builder. Image bytes are downloaded or
transferred directly, not stored as Git changes in JetStream.
CLI: `nixmox media fetch --profile raspberry-pi-5 --release latest --wait`.
`--release` is optional and defaults to `latest`; `--profile` uses the same
single-profile/interactive-selection rule as Build. Custom profiles without release
manifests return `RELEASE_UNAVAILABLE` and offer Build, never a different profile.
`media.fetch` resolves the release selector once, records its immutable manifest
and verified digests, and returns the same media resource used by Plan/Write.

`media.build` takes `profile_id`, optional `writer_id` (placement preference), and
optional `builder_id` (explicit override). The CLI resolves `--host` to `writer_id`;
the API never infers it from the gateway. With no writer or builder, API requests
use any eligible builder. Pin recipe and selected builder before execution.
`/media/profiles` lists profiles. A media record extends normal
task/source evidence with:

```json
{
  "id": "media_02",
  "profile_id": "raspberry-pi-5",
  "target_system": "aarch64-linux",
  "format": "raw",
  "compression": "zstd",
  "supported_boards": ["raspberry-pi-5"],
  "boot_media": ["sd"],
  "builder_id": "n_arm",
  "recipe_commit": "1111111111111111111111111111111111111111",
  "download_size_bytes": 1073741824,
  "expanded_size_bytes": 4294967296,
  "image_digest": "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
}
```

Sizes/digests are fixtures. Record both compressed and expanded content digests;
device capacity uses expanded size. Verify the downloaded artifact, decompress
while writing, and verify readback against expanded bytes. Resolve a recipe's
exact output file, never pick the first file in a build directory.

## Media API

| Command type | Payload |
| --- | --- |
| media.build | `profile_id`; optional `writer_id`, `builder_id` |
| media.fetch | `profile_id`; optional `release` (default `latest`); record immutable release manifest before download |
| media.profile.register | `profile` using the custom-profile schema below; unique profile ID |
| media.plan | `image_id`, `writer_id`, `device_path`; service observes device identity and eligibility |
| media.write | `plan_id`, `typed_device_path`; expected plan version; recheck image/device binding |

HTTP callers supply a resolved profile ID; the CLI performs sole-profile selection
or prompts before submission. Device listing reads `/hosts/{id}/disks` using the
writer ID. CLI `media devices` and `media plan` require `--host` at remote consoles;
otherwise resolve the local daemon's ID, as for media build.

`GET /api/v1/media/plans/{id}` returns
`{id, version, image_id, writer_id, image_digest, device_id, device_path,
device_size_bytes, observation_id, review}`. The embedded review binds the device,
expanded image digest, and writing effect. `media write --plan` supplies the plan
version; `--confirm-device` provides the typed acknowledgement. Refresh device
observations and recheck eligibility before writing. Changed identity, image, or
write effects require Plan again; matching refreshed facts do not expire approval.
Device facts always come from the writer, not the submitted path alone.

`GET /api/v1/media/{id}/download` streams the verified image file with its recorded
compression and digest, filename, and Content-Length. Media/task records expose
`download_url` and content/expanded digests; bytes come from the owner or a verified
holder through direct HTTP streaming, never JetStream. Unavailable holders return
503; an intentionally deleted image returns 410. Hold output pins for the stream.

## Custom profiles

Two extension paths share the same registration contract:

- Extend a built-in profile with additional modules, retaining its hardware/boot
  defaults. Proposed exports are `nixosModules.installer-pc-x86_64` and
  `nixosModules.installer-raspberry-pi-5`; both include the shared installer module.
- Supply a different hardware/image recipe and import `nixosModules.installer`
  for the shared Nixmox experience.

Example module extending the PC recipe, exported by the user's Git-backed flake:

```nix
{ inputs, ... }: {
  imports = [
    inputs.nixmox.nixosModules.installer-pc-x86_64
    ./extra-network-drivers.nix
  ];
}
```

For a different board, imports instead combine `inputs.nixmox.nixosModules.installer`,
`./board-support.nix`, and `./image-layout.nix`. The user's flake supplies `inputs`
to these modules and exports the resulting image derivation. Register that output
to make it selectable in Create installer; registration does not generate a recipe.

Register Git-backed image recipes through UI or the NixOS module. Proposed module
contract (the repository must export this image derivation and exact output path):

```nix
{
  services.nixmox.media.profiles.edge = {
    label = "Custom ARM appliance";
    system = "aarch64-linux";
    format = "raw";
    compression = "zstd";
    supportedBoards = [ "edge-v1" ];
    bootMedia = [ "sd" ];
    source = {
      repository = "lab";
      ref = "main";
      flakeAttribute = "nixosConfigurations.edge-installer.config.system.build.sdImage";
      artifactPath = "sd-image/edge-installer.img.zst";
    };
  };
}
```

Browser/TUI: Media → Profiles → Add → repository/ref → image attribute and metadata.
CLI: `nixmox media profiles add --file edge-profile.json --wait` submits
`media.profile.register`; JSON uses the module fields above in snake_case.
Use the normal GitOps source pinning and unpublished-change mechanism for recipes.
Do not embed host identity or private credentials in a reusable image.

## Retained installer and boot handoff

Profiles may provide a retained recovery system for
[Reboot into installer](deployment-and-rollback.md#reboot-into-installer-and-fresh-install).
For Pi SD boot + NVMe root, design the SD image with firmware, installed-system boot,
and installer-root storage; the NVMe holds the installed root. Keep installer kernel,
initrd, boot entry, and its complete runtime available independently of the NVMe.
Retaining a partition without a usable boot entry is insufficient.

x86 profiles can use prepared USB media or reserved recovery storage with a tested
bootloader adapter. Staging may populate already reserved storage, but cannot
silently repartition an installed disk. Protect recovery files/entries from normal
generation updates, rollback cleanup, and GC. Record immutable image identity and
provide an offline recovery-selection procedure when the installed daemon is down.
Do not promise automatic failure fallback until tested for that bootloader.

The installed configuration independently supplies its board's boot/hardware
support. Preparation verifies the handoff, not a copy of the installer's disk layout.
Preserved SD boot partitions can be mounted and updated without erasing the running
installer partition. Reject any erase plan overlapping the active installer,
recovery dependencies, or handoff state. Recovery on the same physical disk requires
a partition-preserving plan; whole-disk erasure requires a separate boot source.
Initial supported adapters and recovery-selection mechanisms must be pinned and
physically validated per profile before advertising readiness.
For running-OS targets, Install can [prepare spare reboot media](deployment-and-rollback.md#preparing-reboot-media)
before entry; media writing and installation erasure require separate confirmation.

Advertise board/boot support only after physical validation: x86 USB and Pi 5 SD,
Ethernet/Wi-Fi, enrollment, separate-target installation, and reboot. Pi USB/NVMe
boot claims need separate evidence. Test custom and compressed images, capacity/
readback, both SD reader types, writer preference/emulation, explicit builders,
remote transfer failure, and identity reuse rules across media/kexec variants.
