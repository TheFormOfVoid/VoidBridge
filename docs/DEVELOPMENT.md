# Developing VoidBridge

## Project layout

```
cmd/voidbridge/          Windows app (Go + Wails; UI in frontend/, tray, adb phone setup)
cmd/voidbridge-server/   Self-hosted server
internal/protocol        Wire format and crypto (docs/PROTOCOL.md)
internal/node            Sync engine: each copy passed to every device, newest wins
internal/peer            Direct links: Wi-Fi discovery, Tailscale, peer exchange
internal/relay, server   Server client and server
internal/clipboard       Windows clipboard (text, images, password-manager detection)
android/core             Kotlin port of protocol/node/peer/relay (plain JVM, unit-tested)
android/app              Android app
tools/interop            Go devices + server that the Kotlin tests run against
deploy/                  systemd unit and install script for the server
```

The Go and Kotlin implementations must stay byte-compatible.
[PROTOCOL.md](PROTOCOL.md) is the reference, and both sides check the same
known-answer vectors.

## Building and testing

Requirements: Go (see `go.mod` for the version), and JDK 17 plus the Android
SDK for the Android app (Android Studio includes both).

- **Go tests:** `go vet ./... && go test -race ./...`. They cover multi-device
  sync, images, offline catch-up, the server and the Wi-Fi fallback.
- **Windows app:**
  `GOOS=windows go build -tags desktop,production -ldflags "-H windowsgui" ./cmd/voidbridge`.
  This works from Linux or macOS too; no C compiler is needed. The icon and
  manifest come from `cmd/voidbridge/winres/`, compiled into the committed
  `rsrc_windows_*.syso` files with
  `go run github.com/tc-hib/go-winres@latest make --in cmd/voidbridge/winres/winres.json --out cmd/voidbridge/rsrc --arch amd64,arm64`.
- **Server:** `go build ./cmd/voidbridge-server`. For a Raspberry Pi, cross-compile
  with `GOOS=linux GOARCH=arm64` (or `GOARCH=arm GOARM=7` for 32-bit).
- **Android app:** `cd android && ./gradlew :core:test :app:assembleDebug`.
- **Interop tests:** start `go run ./tools/interop`, then run the Kotlin tests
  with `VOIDBRIDGE_INTEROP_PEER=127.0.0.1:47829`,
  `VOIDBRIDGE_INTEROP_CODE=ABCD-EFGH-IJKL-MNOP` and
  `VOIDBRIDGE_INTEROP_SERVER=127.0.0.1:47831` set. CI does this on every push.

## Releases

Go to **Actions → build → Run workflow**, pick `main`, and enter a tag such
as `v0.3.0`. A tag with a suffix, like `v0.3.0-beta.1`, is published as a
pre-release. Entering an existing tag replaces that release. Pushing a `v*`
tag from your own machine also publishes a release.

The Pi install script downloads the latest **stable** release, so it ignores
pre-releases.

## APK signing

Android only installs an update if it's signed with the same key as the
installed app. The phone-setup permissions also survive updates only then.
CI signs release APKs with a key stored in repository secrets. Without them,
it falls back to a throwaway key and prints a warning.

To create a key without Java, use Git Bash on Windows or any Linux/macOS
terminal:

1. Generate a random password and save it in a password manager:
   ```
   openssl rand -base64 24
   ```
2. Create the key. openssl asks for the password when needed:
   ```
   cd ~
   MSYS_NO_PATHCONV=1 openssl req -x509 -newkey rsa:4096 -nodes -keyout vb.key -out vb.crt -days 36500 -subj "/CN=VoidBridge"
   winpty openssl pkcs12 -export -in vb.crt -inkey vb.key -name voidbridge -out voidbridge.p12
   rm vb.key vb.crt
   base64 -w0 voidbridge.p12 > voidbridge-base64.txt
   ```
   `MSYS_NO_PATHCONV=1` and `winpty` are only needed in Git Bash. Leave them
   out elsewhere.
3. Add these repository secrets under **Settings → Secrets and variables →
   Actions**:

   | Secret | Value |
   |---|---|
   | `VOIDBRIDGE_KEYSTORE_BASE64` | the contents of `voidbridge-base64.txt` |
   | `VOIDBRIDGE_KEYSTORE_PASSWORD` | the password |
   | `VOIDBRIDGE_KEY_ALIAS` | `voidbridge` |
   | `VOIDBRIDGE_KEY_PASSWORD` | the same password |

4. Back up `voidbridge.p12` and the password, keeping them in separate places.
   Then delete `voidbridge-base64.txt`. Never commit either file. If the key
   is lost, existing installs can't be updated and everyone has to reinstall.

A fork that publishes its own builds needs its own key. Its APKs can't update
the official app, and the reverse is true too.
