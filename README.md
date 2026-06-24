# ClipBridge

Small Go clipboard relay for handing text and PNG images between browsers in the same private session.

## Run

```sh
go run .
```

Open `http://localhost:8080`, then scan the QR code or copy the join link on another device. Any joined device can tap **Send clipboard** to relay its current clipboard text or PNG image to the other connected devices.

## Features

- Short word-code join links and QR codes.
- Multi-device sessions. The first browser is `Device1`; later devices are `Device2`, `Device3`, and so on.
- Shared device manager on every device. Desktop shows the pane on the right; mobile has a small arrow toggle.
- Device rename and delete actions sync to all connected devices.
- Join links can be rotated without kicking out existing devices.
- The original browser remembers its last session locally and can resume while the server still has it.
- Clipboard images are relayed as PNG payloads with a 5 MiB decoded size limit.

## Deploy

- Railway can run this as a single Go service.
- Set `PUBLIC_BASE_URL=https://clipbridge.app` if the app is behind a proxy or custom domain.
- Keep one instance for now. Sessions, device names, and join-link aliases are intentionally in memory and disappear when the process restarts or the session expires.

## Security Notes

- No accounts, database, analytics, or third-party frontend assets.
- Clipboard text and images are relayed in memory and are not logged or persisted.
- Pairing auth tokens are HttpOnly cookies.
- Rotating a join link only creates a new invite URL for the same session. Existing devices stay connected.
- `?text=` shortcut URLs are not supported because URLs can leak through logs, history, and referrers.

## Test

```sh
go test ./...
```
