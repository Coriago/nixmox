# Invoked by writeShellApplication (strict shell options and PATH supplied there).
# Never print connection profiles or credentials: nmtui owns password entry.
if [[ "${1:-}" == --auto ]]; then
  # Allow DHCP a bounded opportunity to finish without delaying nixmox/discovery.
  nm-online --quiet --wait-for-startup --timeout=20 || true
  while IFS=: read -r device kind; do
    [[ "$kind" == ethernet ]] || continue
    [[ "$(cat "/sys/class/net/$device/carrier" 2>/dev/null || true)" == 1 ]] || continue
    if ip -j address show dev "$device" | jq -e '
      any(.[].addr_info[]; .scope == "global" and
        (.local | startswith("169.254.") | not))
    ' >/dev/null; then
      echo "Wired connection ready; nixmox discovery is running."
      exit 0
    fi
  done < <(nmcli --terse --fields DEVICE,TYPE device status)

  if ! nmcli --terse --fields TYPE device status | grep -qx wifi; then
    echo "No Wi-Fi adapter found. Connect Ethernet or check the Wi-Fi adapter."
    echo "Run nixmox-wifi to open network setup again."
    exit 0
  fi
fi

echo "Select a Wi-Fi network and enter its password to connect this installer."
echo "After connecting, this machine can be discovered in the nixmox cluster UI."
echo "Quit returns to the console. Run nixmox-wifi to open setup again."
nmtui-connect
