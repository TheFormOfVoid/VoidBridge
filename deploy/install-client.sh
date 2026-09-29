#!/bin/sh
# Installs or updates the VoidBridge client for your user on a Linux desktop
# such as Raspberry Pi OS. Run it as yourself (not with sudo):
#   curl -fsSL https://raw.githubusercontent.com/TheFormOfVoid/VoidBridge/main/deploy/install-client.sh | sh
set -eu

REPO="TheFormOfVoid/VoidBridge"
case "$(uname -m)" in
  aarch64|arm64) ARCH=arm64 ;;
  armv7l|armv6l) ARCH=armv7 ;;
  x86_64|amd64)  ARCH=amd64 ;;
  *) echo "Unsupported CPU: $(uname -m)"; exit 1 ;;
esac

if [ "$(id -u)" = 0 ]; then
  echo "Run this as your normal user, without sudo: the clipboard belongs to your desktop session."
  exit 1
fi

# The clipboard tools: wl-clipboard for Wayland (Raspberry Pi OS default), xclip for X11.
if ! command -v wl-paste >/dev/null 2>&1 || ! command -v xclip >/dev/null 2>&1; then
  echo "Installing clipboard tools (wl-clipboard, xclip); this may ask for your password."
  sudo apt-get install -y wl-clipboard xclip >/dev/null || echo "Couldn't install them; install wl-clipboard (or xclip on X11) yourself."
fi

BIN="$HOME/.local/bin"
mkdir -p "$BIN"
URL="https://github.com/$REPO/releases/latest/download/voidbridge-linux-$ARCH"
echo "Downloading $URL"
curl -fL "$URL" -o "$BIN/voidbridge.new"
chmod 755 "$BIN/voidbridge.new"
mv "$BIN/voidbridge.new" "$BIN/voidbridge"

"$BIN/voidbridge" setup
# Keep running when you're not logged in, so files still arrive (best effort).
loginctl enable-linger "$(id -un)" >/dev/null 2>&1 || true

echo
case ":$PATH:" in
  *":$BIN:"*) VB=voidbridge ;;
  *) VB="$BIN/voidbridge"; echo "(Open a new terminal or log in again to use plain 'voidbridge'.)" ;;
esac
echo "Next, sign in with the same account as your other devices:"
echo "  $VB login YOUR-SERVER YOUR-USERNAME"
echo "or join with a sync code:  $VB join ABCD-EFGH-IJKL-MNOP"
echo "Then check it with:        $VB status"
