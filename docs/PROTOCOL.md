# VoidBridge protocol, version 2

Reference implementations:

- Go: `internal/protocol`, `internal/node`, `internal/peer`, `internal/relay`
  and `internal/server`.
- Kotlin: `android/core`.

Both sides check the same known-answer vectors (`TestKnownAnswers` and
`knownAnswersMatchGo`). CI runs the Kotlin client against the Go devices and
server (`tools/interop`).

## Groups and keys

Devices that sync together share a 32-byte **master key**:

- **Sync code:** 80 random bits shown as `XXXX-XXXX-XXXX-XXXX` (base32).
  Normalise the code: uppercase it, map `0→O`, `1→I` and `8→B`, and drop
  anything else outside `A–Z2–7`. Then
  `master = PBKDF2-SHA256(code, "voidbridge-pairing-v1", 200000, 32)`.
- **Server account:**
  `master = PBKDF2-SHA256(password, "voidbridge-account-v1:" + lowercase(trim(username)), 200000, 32)`.

Each subkey is derived with HKDF-SHA256 (empty salt, 32 bytes) and an info
string:

| key | HKDF info | used for |
|---|---|---|
| link | `voidbridge-link-v2` | authenticating and encrypting direct links |
| content | `voidbridge-content-v2` | end-to-end encryption of clip contents |
| auth | `voidbridge-auth-v2` | logging in to a server (the server stores SHA-256(auth)) |

The beacon fingerprint is the first 8 bytes of
`HMAC-SHA256(link, "voidbridge-beacon-v2")`, in hex.

## Frames

A message is `uint32be headerLen || header JSON || body`. The header carries
`t` (the type) and the fields below. The body is raw bytes and is used only
for encrypted clip content. The maximum is 40 MiB.

| t | fields | meaning |
|---|---|---|
| `hello` | `v`=2, `id`, `name`, `kind`, `nonce` (base64, 16 B), `addrs` | first frame of a direct link (plaintext) |
| `ready` | | first encrypted frame; proves the sender has the link key |
| `ping` / `pong` | | keepalive; 25 s of silence means the link is dead |
| `clip` | `id`, `origin`, `origin_name`, `time` (unix ms), `ctype` (`text`/`image`), `mime` | body = nonce(12) ‖ AES-256-GCM(content key, plaintext, AAD) |
| `peers` | `peers: [{id,name,kind,addrs}]` | peer exchange on direct links |
| `devices` | `peers: [{id,name,kind}]` | server → device: the account's online devices |

The clip AAD is
`"voidbridge-clip-v2|" + id + "|" + origin + "|" + time + "|" + ctype + "|" + mime`,
so none of those header fields can be altered.

## Direct links (TCP 47829)

Every device listens on TCP 47829. Frames go over the wire as
`uint32be len ‖ payload`.

1. Each side sends its `hello` in plaintext.
2. Both sides compute
   `session = HMAC-SHA256(link, "voidbridge-session-v2" ‖ dialerNonce ‖ listenerNonce)`.
3. Every later frame is AES-256-GCM(session) with a 96-bit nonce made of
   `uint32 direction ‖ uint64 counter`. The direction is `0x76620001` for the
   dialer and `0x76620002` for the listener. The receiver requires the exact
   next counter.
4. Both sides send `ready`. If it doesn't decrypt, the other device is in a
   different group.

Discovery works in three ways:

- **Beacons:** every 3 s each device sends UDP broadcasts to port 47830:
  `{"app":"voidbridge","v":2,"id","name","kind","port","fp"}`. A device can
  also send `{"type":"probe"}`, and every device that hears it answers with
  its beacon.
- **Peer exchange:** `peers` messages spread the addresses of every known
  device, so one working link is enough to find the rest. That covers
  Tailscale, where broadcasts don't travel.
- **Other sources:** addresses typed in by the user, and Tailscale peers
  (desktop only).

## Server (HTTP 47831)

| endpoint | notes |
|---|---|
| `GET /api/info` | `{server:"voidbridge", protocol, version, name, signup, has_users}` |
| `POST /api/register` | `{username, auth_key(base64), invite, device_id, device_name, device_kind}` → `{token, username, admin}` |
| `POST /api/login` | same fields without `invite` |
| `POST /api/logout`, `GET /api/me`, `DELETE /api/me/devices/{id}` | bearer token |
| `GET/POST/DELETE /api/admin/invites…`, `GET/PATCH/DELETE /api/admin/users…` | admin only |
| `GET /api/sync` | WebSocket (bearer token). Each binary message is one frame. |

The first account needs no invite and becomes the admin. After that an invite
is required, unless the server was started with `-signup open`. After 10
failed attempts, logins from that IP or for that username are blocked for 15
minutes.

The hub keeps each account's newest clip in memory. It forwards newer clips to
the account's other devices and sends the newest clip to a device when it
connects. It can't decrypt clips.

## Sync rules

- A device keeps its newest clip (**current**). Clips are ordered by
  `(time, id)`.
- On a local copy, the device stamps the clip with `max(now, current.time+1)`,
  seals it, and sends it to every link.
- On receiving a clip:
  - drop it if its id was already seen or it isn't newer than current;
  - drop it if it doesn't decrypt;
  - otherwise make it current, forward it to every other link, and write it to
    the clipboard.
- When a link comes up, each side sends its current clip. That's how offline
  devices catch up, and the newest clip wins everywhere.
- Echo suppression: each device remembers the hash of the last content it saw
  or wrote, so writing a received clip is not mistaken for a new copy.
- Whatever is on the clipboard at startup is not sent. Clips marked sensitive
  by a password manager are never sent: Windows
  `ExcludeClipboardContentFromMonitorProcessing`,
  `CanIncludeInClipboardHistory=0` or `CanUploadToCloudClipboard=0`; Android
  `EXTRA_IS_SENSITIVE`.
