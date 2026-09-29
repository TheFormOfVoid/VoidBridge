# VoidBridge on Linux (Raspberry Pi OS)

The Linux client syncs the desktop's clipboard (text and images) with your
other devices and sends and receives files. It's handy on a Raspberry Pi you
use through Pi Connect or VNC: copy on the Pi, paste on your PC or phone, and
the other way round. It can run on the same Pi as the VoidBridge server.

It runs in the background as your user and is controlled from the terminal.
There's no window.

## Install

Run this as your normal user (not with `sudo`):

```
curl -fsSL https://raw.githubusercontent.com/TheFormOfVoid/VoidBridge/main/deploy/install-client.sh | sh
```

It installs `voidbridge` to `~/.local/bin`, installs the clipboard tools
(`wl-clipboard` and `xclip`) if they're missing, and starts VoidBridge with
your login. Then sign in with the same account as your other devices:

```
voidbridge login YOUR-SERVER YOUR-USERNAME
```

`YOUR-SERVER` is the same address the apps use, e.g. `raspberrypi` or its
Tailscale IP. On the Pi that runs the server, `localhost` works too. To use a
sync code instead of a server, run `voidbridge join ABCD-EFGH-IJKL-MNOP`.

Check that everything works:

```
voidbridge status
```

## Using it

- **Clipboard:** copy on the Pi's desktop and paste on your other devices, and
  the other way round. This needs the desktop session to be running, which it
  is whenever you're using Pi Connect's screen sharing. Wayland (the default on
  Raspberry Pi OS) and X11 both work.
- **Send files:** right-click files in the file manager → **Send with
  VoidBridge** → a device. From the terminal, run
  `voidbridge send "Pixel 8" photo.jpg` (the device's name or id).
  `voidbridge devices` lists the devices you can send to.
- **Receive files:** they're saved to `~/Downloads/VoidBridge`, with a desktop
  notification. Change the folder with `voidbridge set folder ~/Documents/Inbox`.

The right-click menu lists devices once they've connected at least once. If a
new device doesn't show up, log out and back in; the file manager reads the
menu when it starts.

## Settings

```
voidbridge set name "Living room Pi"     # how this computer appears elsewhere
voidbridge set menu off                  # remove the right-click menu
voidbridge set auto-updates off          # don't install new versions by themselves
voidbridge set sensitive off             # also sync copies a password manager marks secret
voidbridge forget "Old phone"            # remove a device from the menu
voidbridge logout                        # stop syncing
```

## Updates

With automatic updates on (the default), VoidBridge installs new stable
releases by itself, never betas, and restarts in the background. `voidbridge
update` checks right away. Running the install command again also updates it.

## Troubleshooting

- `voidbridge status` shows the connection, the clipboard and the devices.
- Logs: `journalctl --user -u voidbridge -f`
- "Clipboard: no desktop session": nobody is logged in to the desktop right
  now. Files still work.
- Uninstall: `systemctl --user disable --now voidbridge`, then delete
  `~/.local/bin/voidbridge`, `~/.config/VoidBridge` and
  `~/.local/share/file-manager/actions/voidbridge*`.
