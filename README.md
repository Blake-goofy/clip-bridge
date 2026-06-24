# ClipBridge

Small Go relay for sending clipboard text between two paired browser tabs.

## Run

```sh
go run .
```

Open `http://localhost:8080`, scan the QR code with another device, then tap **Send** from either side.

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
