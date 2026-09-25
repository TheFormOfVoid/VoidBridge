# Hosting a VoidBridge server (e.g. on a Raspberry Pi)

The server gives your devices a meeting point that works from anywhere. It
also lets other people you invite have their own accounts. Each account's
clipboard is separate and end-to-end encrypted, so the server can't read it.

It's one small program with no database to install. Any Raspberry Pi 3 or
newer (or any Linux machine) is plenty.

## 1. Install Tailscale on the Pi

Tailscale lets your devices reach the Pi from anywhere, without opening
anything to the internet:

```
curl -fsSL https://tailscale.com/install.sh | sh
sudo tailscale up
```

Also install Tailscale on your PC, laptop, phone and tablet, and sign in with
the same Tailscale account.

## 2. Install the VoidBridge server

```
curl -fsSL https://raw.githubusercontent.com/TheFormOfVoid/VoidBridge/main/deploy/install-server.sh | sudo sh
```

The script:

1. Downloads the right binary for the Pi.
2. Installs it as a systemd service (`voidbridge-server`) that starts at boot.
3. Prints the address to use in the apps, e.g. `raspberrypi.tail1234.ts.net`.
   The Pi's Tailscale IP (`100.x.y.z`) or its short Tailscale name
   (`raspberrypi`) also works.

Run the same command again to update.

Manual install: download `voidbridge-server-linux-arm64` (64-bit Pi OS) or
`-armv7` (32-bit) from Releases and run `voidbridge-server serve`. It listens
on port 47831 and stores its data in `./voidbridge-data`. Run
`voidbridge-server serve -h` for options.

## 3. Create your account

In the Windows app, open **Connections → Use a VoidBridge server**:

1. Enter the server address.
2. Choose **Create account**. The first account needs no invite code and
   becomes the **admin**.
3. Sign in with the same account on all your other devices.

## 4. Let other people in

Accounts can only be created with an invite code. As the admin, either:

- open **Server** in the Windows app and click **Create invite**. You choose
  how many uses and when it expires. The same page lists accounts, which you
  can disable or delete, and your devices, which you can remove if one is lost;
- or, on the Pi, run `voidbridge-server invite` (options: `-uses 3 -days 30
  -note "for Sam"`).

Send the code to the person. They pick **Create account** in their app and
enter it.

Other commands on the Pi:

```
voidbridge-server users          # list accounts
voidbridge-server disable NAME   # block an account (signs out its devices)
voidbridge-server enable NAME
voidbridge-server admin NAME     # make someone an admin
```

To let anyone who can reach the server sign up without an invite, add
`-signup open` to `ExecStart` in `/etc/systemd/system/voidbridge-server.service`.

## Good to know

- **Passwords:** a password also encrypts that account's clipboard. The server
  never sees it, so there's no password reset. If a password is forgotten, the
  admin deletes the account and the person creates a new one.
- **Backups:** everything the server knows is in
  `/var/lib/voidbridge/voidbridge-server.json` (accounts, invites, device
  sign-ins). Clipboard contents aren't stored on disk.
- **Without Tailscale:** the server also works on a plain home network, or
  exposed to the internet behind a reverse proxy with HTTPS (Caddy, Cloudflare
  Tunnel), or with `-tls-cert`/`-tls-key`. On the open internet, always use
  HTTPS.
- **Logs:** `journalctl -u voidbridge-server -f`.
