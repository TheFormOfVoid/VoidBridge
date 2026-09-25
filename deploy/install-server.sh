#!/bin/sh
# Installs or updates the VoidBridge server on a Raspberry Pi (or any Linux box
# with systemd). Run:  curl -fsSL https://raw.githubusercontent.com/TheFormOfVoid/VoidBridge/main/deploy/install-server.sh | sudo sh
set -eu

REPO="TheFormOfVoid/VoidBridge"
case "$(uname -m)" in
  aarch64|arm64) ARCH=arm64 ;;
  armv7l|armv6l) ARCH=armv7 ;;
  x86_64|amd64)  ARCH=amd64 ;;
  *) echo "Unsupported CPU: $(uname -m)"; exit 1 ;;
esac

if [ "$(id -u)" != 0 ]; then echo "Please run with sudo."; exit 1; fi

URL="https://github.com/$REPO/releases/latest/download/voidbridge-server-linux-$ARCH"
echo "Downloading $URL"
curl -fL "$URL" -o /usr/local/bin/voidbridge-server.new
chmod 755 /usr/local/bin/voidbridge-server.new
mv /usr/local/bin/voidbridge-server.new /usr/local/bin/voidbridge-server

curl -fsSL "https://raw.githubusercontent.com/$REPO/main/deploy/voidbridge-server.service" -o /etc/systemd/system/voidbridge-server.service
systemctl daemon-reload
systemctl enable voidbridge-server >/dev/null 2>&1
systemctl restart voidbridge-server

echo
echo "VoidBridge server is running on port 47831."
if command -v tailscale >/dev/null 2>&1; then
  TS=$(tailscale status --json 2>/dev/null | sed -n 's/.*"DNSName": *"\([^"]*\)\.".*/\1/p' | head -n1)
  [ -n "$TS" ] && echo "Server address for the apps (over Tailscale): $TS"
else
  echo "Tip: install Tailscale (https://tailscale.com/download/linux) to reach it from anywhere."
fi
echo "The first account you create from an app becomes the admin."
echo "Make an invite code for someone else:  voidbridge-server invite"
