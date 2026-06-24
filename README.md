# ClipBridge

Small Go relay for sending clipboard text between two paired browser tabs.

## Run

```sh
go run .
```

Open `http://localhost:8080`, scan the QR code with another device, then tap **Send clipboard** from either side.

Pairing links use short word codes and the creating browser remembers its last code locally, so refreshing or reopening the page can resume the same session while the server still has it. Multiple devices can join the same code, and the desktop page can rename or disconnect them.

## Deploy

- Railway can run this as a single Go service.
- Set `PUBLIC_BASE_URL=https://clipbridge.app` if the app is behind a proxy or custom domain.
- Keep one instance for V1. Sessions are intentionally in memory and are deleted when the tab that created them disconnects.

## Security Notes

- No accounts, database, analytics, or third-party frontend assets.
- Clipboard text is relayed in memory and is not logged or persisted.
- Pairing auth tokens are HttpOnly cookies.
- V1 does not support `?text=` shortcut URLs because URLs can leak through logs, history, and referrers.

## Test

```sh
go test ./...
```
