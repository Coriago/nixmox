# Imported by a deployment recipe, never by the baseline VM configuration.
{ pkgs, ... }:
{
  environment.systemPackages = [
    (pkgs.writeShellApplication {
      name = "nixmox-e2e-testpkg";
      text = ''
        printf '%s\n' 'nixmox-e2e-testpkg-v1'
      '';
    })
  ];
}
