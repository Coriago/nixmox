# DHCP is a service of this configuration, not a per-target installer override.
{ ... }:
{
  services.dnsmasq = {
    enable = true;
    resolveLocalQueries = false;
    settings = {
      port = 0;
      interface = "cluster0";
      bind-dynamic = true;
      dhcp-range = "192.168.77.100,192.168.77.150,255.255.255.0,1h";
      # Guests retain their separate management-NAT default route and DNS.
      dhcp-option = [
        "option:router"
        "option:dns-server"
      ];
    };
  };
}
