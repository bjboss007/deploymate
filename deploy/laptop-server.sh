#!/usr/bin/env bash
# Laptop-as-server power configuration for Ubuntu 24.04 LTS.
# Run once, as root, after installing Ubuntu:
#   sudo bash deploy/laptop-server.sh
#
# A laptop defaults to mobile behavior: closing the lid suspends the OS
# and kills your server. This script makes it behave like a headless box:
#   - lid close does nothing
#   - suspend/hibernate are disabled (masked)
#   - console blanking after 5 min (screen off, server on)
# Idempotent: safe to re-run.
set -euo pipefail

if [[ $EUID -ne 0 ]]; then
  echo "run as root: sudo bash deploy/laptop-server.sh" >&2
  exit 1
fi

echo "==> lid close does nothing"
LOGIND=/etc/systemd/logind.conf
sed -i 's/^#\?HandleLidSwitch=.*/HandleLidSwitch=ignore/' "$LOGIND"
sed -i 's/^#\?HandleLidSwitchExternalPower=.*/HandleLidSwitchExternalPower=ignore/' "$LOGIND"
grep -q '^HandleLidSwitch=ignore' "$LOGIND" || echo 'HandleLidSwitch=ignore' >> "$LOGIND"
grep -q '^HandleLidSwitchExternalPower=ignore' "$LOGIND" || echo 'HandleLidSwitchExternalPower=ignore' >> "$LOGIND"

echo "==> suspend/hibernate disabled"
systemctl mask sleep.target suspend.target hibernate.target hybrid-sleep.target

echo "==> console blanking after 5 minutes (screen off, server on)"
cat > /etc/systemd/system/console-blank.service <<'UNIT'
[Unit]
Description=Blank laptop console after boot

[Service]
Type=oneshot
ExecStart=/usr/bin/setterm --blank 5 --powersave powerdown
StandardOutput=tty

[Install]
WantedBy=multi-user.target
UNIT
systemctl daemon-reload
systemctl enable --now console-blank.service

echo "==> restarting logind"
systemctl restart systemd-logind

echo
echo "done. Verify with:  systemctl is-enabled console-blank.service"
echo "Next: connect ethernet, set a DHCP reservation in your router,"
echo "port-forward 80/443 to this machine, then run bootstrap.sh."
