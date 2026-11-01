# VoidBridge Server

## Install

1. In Home Assistant, open **Settings → Add-ons → Add-on store**.
2. Click **⋮ → Repositories**, add
   `https://github.com/TheFormOfVoid/VoidBridge` and close the dialog.
3. Find **VoidBridge Server** in the store, click it, then **Install**.
4. Click **Start**. The **Log** tab should show
   `VoidBridge server ... listening on :47831`.

On the home network (or over Tailscale) that's all you need. In the apps,
enter the Pi's address, e.g. `homeassistant.local` or `192.168.1.20`, and pick
**Create account**. The first account needs no invite and becomes the admin.

To use the server from anywhere without Tailscale, set up HTTPS as below.

## Options

| Option | Default | What it does |
| --- | --- | --- |
| `port` | `47831` | Port the server listens on, on the Pi itself. |
| `domain` | | Get a free Let's Encrypt certificate for this domain. The server does it itself, which only works if **port 443** on the internet reaches it. |
| `email` | | Optional contact email for Let's Encrypt (with `domain`). |
| `certfile` | | Use this certificate instead, e.g. `fullchain.pem` from the DuckDNS add-on. A plain file name is looked up in `/ssl`. The server picks up renewals by itself. |
| `keyfile` | | The key that goes with `certfile`, e.g. `privkey.pem`. |
| `signup` | `invite` | `invite`: after the first account, new accounts need an invite code. `open`: anyone who can reach the server can sign up. |

Use either `domain` or `certfile`/`keyfile`, not both. Optional options are
under **Show unused optional configuration options** on the
**Configuration** tab.

Without a certificate the server works only on the home network and over
Tailscale. With one, it still accepts plain connections from those, and
requires HTTPS from everywhere else.

## Reaching it from the internet with DuckDNS

You need a name for your home connection and a port opened on the router.

### 1. Get a DuckDNS name

1. Sign in at [duckdns.org](https://www.duckdns.org) and add a subdomain,
   e.g. `myvoid` for `myvoid.duckdns.org`. Copy your **token**.
2. In the add-on store, install the official **Duck DNS** add-on.
3. On its **Configuration** tab set:

   ```yaml
   domains:
     - myvoid.duckdns.org
   token: <your token>
   lets_encrypt:
     accept_terms: true
     certfile: fullchain.pem
     keyfile: privkey.pem
   ```

4. Start it and turn on **Start on boot**. It keeps the name pointing at your
   home IP. It also gets a Let's Encrypt certificate into `/ssl` (over DNS, so
   no port is needed for that).

### 2. Forward port 443 on the router

1. Give the Pi a fixed address on the home network: in the router, add a
   DHCP reservation for it (e.g. `192.168.1.20`).
2. Add a port forwarding rule:

   | | |
   | --- | --- |
   | Protocol | TCP |
   | External (WAN) port | `443` |
   | Internal IP | the Pi, e.g. `192.168.1.20` |
   | Internal port | `47831` |

Some providers put homes behind CGNAT, where forwarding can't work: if the
router's WAN address differs from what
[whatismyip.com](https://www.whatismyip.com) shows, ask the provider for a
public IP, or use Tailscale or a Cloudflare Tunnel instead.

### 3. Give the VoidBridge add-on the certificate

On the VoidBridge add-on's **Configuration** tab set:

```yaml
certfile: fullchain.pem
keyfile: privkey.pem
```

Save and restart the add-on. The log shows
`HTTPS with the certificate in /ssl/fullchain.pem`.

Instead of the DuckDNS certificate you can leave `certfile`/`keyfile` empty
and set `domain: myvoid.duckdns.org`. The server then gets its own
certificate through port 443 the first time someone connects. The Duck DNS
add-on is still needed to keep the name up to date (set
`accept_terms: false` there if you don't want its certificate).

### 4. Connect the apps

In the apps, enter the server address as `https://myvoid.duckdns.org`. It
works both at home and away.

Some routers can't send home-network traffic to their own public address
("NAT loopback" or "hairpin NAT", often a setting you can turn on). Without
it, devices at home can't reach the server through the `https://` address.
They still sync with each other directly over the Wi-Fi, but not with devices
that are away.

### Port 443 is already in use

If 443 already goes to Home Assistant itself (remote access on port 8123),
forward another port to the Pi instead, e.g. external `8443` → `47831`. Use
`certfile`/`keyfile` (the `domain` option needs port 443) and connect to
`https://myvoid.duckdns.org:8443`.

## Managing accounts

The add-on runs without the server's console commands
(`voidbridge-server invite`, `users`, ...). Do the same from the Windows app
instead: as the admin, open **Server** to create invites and to disable or
delete accounts and devices.

## Data and backups

Accounts, invites and device sign-ins are in the add-on's `/data` folder
(`voidbridge-server.json`, plus `certs/` when `domain` is used). Home
Assistant backups include it. Clipboard contents aren't stored on disk.
