# VoidBridge protocol, version 1

The PC is the server and phones are clients. Everything happens on the local
network. There are no cloud servers or accounts.

Reference implementations: `pc/internal/protocol` (Go) and
`android/core/.../Protocol.kt` (Kotlin). Both test against the same
known-answer vectors (`TestKnownAnswers`, `knownAnswersMatchGo`).

## Discovery (UDP 47830)

Every 2 s the PC broadcasts this JSON to `255.255.255.255:47830` and to each
interface's directed broadcast address:

```json
{"app":"voidbridge","id":"pc-…","name":"DESKTOP-1","port":47829,"fp":"<16 hex>"}
```

`fp` is the first 8 bytes of `HMAC-SHA256(pairingKey, "voidbridge-beacon-v1")`,
in hex. A phone ignores beacons whose `fp` doesn't match its key. A phone may
send `{"app":"voidbridge","type":"probe"}` to port 47830, and the PC answers it
with the beacon right away.

## Pairing

The PC generates 80 random bits and shows them as 16 base32 characters,
`XXXX-XXXX-XXXX-XXXX`. Both sides normalise the code the same way: uppercase it,
map `0→O`, `1→I` and `8→B`, and drop anything outside `A–Z2–7`. Then:

```
pairingKey = PBKDF2-HMAC-SHA256(normalisedCode, salt="voidbridge-pairing-v1", iterations=200000, len=32)
```

## Connection (TCP 47829)

Frames are `uint32 big-endian length || payload`, with a maximum of 4 MiB.

1. Each side sends a plaintext hello:
   `{"t":"hello","v":1,"id":"…","name":"…","nonce":"<base64 16 bytes>","time":<unix ms>}`
2. Both sides derive
   `sessionKey = HMAC-SHA256(pairingKey, "voidbridge-session-v1" || clientNonce || serverNonce)`.
3. Every later frame is `AES-256-GCM(sessionKey, nonce, JSON)` with no AAD. The
   96-bit nonce is `uint32 direction || uint64 counter`. The direction is
   `0x76620001` from the client and `0x76620002` from the server. Counters start
   at 0 for each direction, and the receiver requires the exact next value. This
   rejects replayed, reordered and reflected frames.
4. Each side sends `{"t":"ready"}` as its first encrypted frame. If it fails to
   decrypt, the peer used a different pairing code.

`time` in the hello gives each side a rough clock offset
(`peerTime - localTime`). The offset converts clip timestamps into the local
clock.

## Messages

| message | meaning |
|---|---|
| `{"t":"ping"}` / `{"t":"pong"}` | The PC pings every 10 s. Either side treats 25 s of silence as a dead link. |
| `{"t":"clip","id":"…","time":ms,"text":"…"}` | New clipboard text. `time` is the sender's clock. |
| `{"t":"ack","id":"…"}` | The clip with this id arrived. It's sent even when the clip was ignored. |

## Sync rules

- Only changes are sent. Whatever is on the clipboard at startup isn't pushed.
- Each side keeps its newest local clip until it's acked, and resends it after
  every reconnect. Nothing is lost when the link drops mid-copy.
- On receiving a clip, a side ignores it if it has a newer local copy
  (last writer wins after clock-offset correction). Otherwise it writes the
  clip to the clipboard.
- Echo suppression: each side remembers the SHA-256 of the last text it saw or
  wrote, and doesn't send a "change" whose hash matches.
- The PC relays clips between multiple phones.
