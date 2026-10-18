{ self, pkgs, ... }:
pkgs.testers.runNixOSTest {
  name = "installer-wifi";
  nodes.machine = { lib, ... }: {
    imports = [ ../installer-wifi.nix self.nixosModules.default ];
    services.nixmox = {
      enable = true;
      discovery = true;
      package = self.packages.${pkgs.stdenv.hostPlatform.system}.default;
    };
    networking.useDHCP = false;
    networking.wireless.enable = lib.mkOverride 0 true;
    networking.wireless.interfaces = [ "wlan1" ];
    networking.firewall.enable = false;
    networking.networkmanager.unmanaged = [ "interface-name:eth*" "interface-name:wlan0" ];
    boot.kernelModules = [ "mac80211_hwsim" ];
    boot.extraModprobeConfig = "options mac80211_hwsim radios=2";
    networking.interfaces.wlan0.ipv4.addresses = [ { address = "192.168.60.1"; prefixLength = 24; } ];
    services.hostapd = {
      enable = true;
      radios.wlan0 = {
        band = "2g";
        channel = 6;
        countryCode = "US";
        networks.wlan0 = {
          ssid = "Onboarding-test";
          authentication = {
            mode = "wpa2-sha256";
            wpaPassword = "onboarding-test-password";
          };
        };
      };
    };
    services.dnsmasq = {
      enable = true;
      settings = {
        interface = "wlan0";
        bind-interfaces = true;
        dhcp-range = "192.168.60.10,192.168.60.20,255.255.255.0,1h";
      };
    };
  };
  testScript = ''
    machine.start()
    machine.wait_for_unit("nixmox.service")
    machine.wait_for_unit("nixmox-installer.service")
    assert "Wired connection ready" in machine.succeed("nixmox installer --auto")

    # Drop wired carrier, leaving only the simulated radio pair. The console
    # selector must not block nixmox even while waiting for a password.
    machine.succeed("ip link set eth1 down")
    machine.wait_for_unit("hostapd.service")
    machine.succeed("systemctl stop getty@tty1.service; systemctl restart --no-block nixmox-installer")
    machine.wait_until_tty_matches("1", "Onboarding-test")
    machine.succeed("systemctl is-active nixmox.service")
    machine.screenshot("wifi-selector")
    machine.send_chars("1\n")
    machine.wait_until_tty_matches("1", "Password")
    machine.send_chars("onboarding-test-password")
    machine.send_key("ret")
    machine.wait_until_succeeds("nmcli -g GENERAL.STATE device show wlan1 | grep '^100'")
    machine.wait_until_succeeds("ip -4 address show wlan1 | grep '192.168.60.'")
    # NetworkManager must save a system connection for the later install task.
    machine.succeed("test -n \"$(find /etc/NetworkManager/system-connections -name '*.nmconnection' -perm 0600)\"")
    machine.screenshot("wifi-connected")
    machine.send_chars("q\n")
    machine.wait_for_unit("nixmox-installer.service")
    machine.succeed("systemctl start getty@tty1.service")
    machine.wait_for_unit("getty@tty1.service")
  '';
}
