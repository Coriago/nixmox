{ self, pkgs, ... }:
let
  image = pkgs.runCommand "nixmox-test-installer" {} ''
    mkdir -p $out/iso
    printf 'nixmox test installer payload\n' > $out/iso/test.iso
    truncate -s 1048576 $out/iso/test.iso
  '';
in pkgs.testers.runNixOSTest {
  name = "usb-installer";
  nodes.machine = { ... }: {
    imports = [ self.nixosModules.default ];
    services.nixmox = {
      enable = true;
      enableMedia = true;
      package = self.packages.${pkgs.stdenv.hostPlatform.system}.default;
    };
    environment.systemPackages = [ pkgs.jq pkgs.e2fsprogs ];
    virtualisation.additionalPaths = [ image ];
    virtualisation.qemu.options = [
      "-device qemu-xhci"
      "-drive if=none,id=testusb,format=raw,file=/tmp/nixmox-test-usb.raw"
      "-device usb-storage,drive=testusb,serial=NIXMOX-TEST-USB"
    ];
  };
  testScript = ''
    import json

    # This is a disposable virtual USB disk, never a physical host drive.
    with open("/tmp/nixmox-test-usb.raw", "wb") as disk:
        disk.truncate(64 * 1024 * 1024)
    machine.start()
    machine.wait_for_unit("nixmox.service")
    machine.wait_for_open_port(8080)
    machine.succeed("ln -s ${image} /var/lib/nixmox/installer")
    status = json.loads(machine.succeed("curl -sf localhost:8080/api/v1/media"))
    disks = status["devices"]
    assert len(disks) == 1, disks
    disk = disks[0]
    assert disk["serial"] == "NIXMOX-TEST-USB", disk
    assert not disk.get("reason"), disk
    path = disk["path"]

    # A mounted device must never be admitted, even with correct confirmation.
    machine.succeed(f"mkfs.ext4 -F {path} && mkdir -p /mnt/stick && mount {path} /mnt/stick")
    request = json.dumps({"device": path, "identity": disk["identity"], "confirm": path})
    command = f"curl -s -o /tmp/write-response -w '%{{http_code}}' -H 'Content-Type: application/json' -d '{request}' -XPOST localhost:8080/api/v1/media/write"
    assert machine.succeed(command).strip() == "409"
    machine.succeed("umount /mnt/stick")

    assert machine.succeed(command).strip() == "202"
    job_id = json.loads(machine.succeed("cat /tmp/write-response"))["id"]
    job_url = f"http://localhost:8080/api/v1/jobs/{job_id}"
    machine.wait_until_succeeds(f"curl -sf {job_url} | jq -e '.state == \"applied\" or .state == \"failed\"'", timeout=60)
    job = json.loads(machine.succeed(f"curl -sf {job_url}"))
    assert job["state"] == "applied", job
    assert job["phase"] == "Verified; USB installer ready", job
    machine.succeed(f"cmp -n 1048576 ${image}/iso/test.iso {path}")
    machine.succeed("curl -sf localhost:8080/partials/hosts/machine/jobs | grep installer-write")
  '';
}
