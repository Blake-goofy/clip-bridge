package main

import (
	"context"
	"strings"
	"testing"
)

func TestEncryptDecryptPayload(t *testing.T) {
	key, err := randomSessionKey()
	if err != nil {
		t.Fatal(err)
	}
	want := clipboardPayload{Type: "text", Text: "hello from windows"}
	box, err := encryptPayload(key, want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := decryptPayload(key, box)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("payload = %#v, want %#v", got, want)
	}
}

func TestJoinLinkKeepsKeyInFragment(t *testing.T) {
	link := joinLink("https://clipbridge.app/", "abc123", "secret-key")
	if strings.Contains(link, "?") {
		t.Fatalf("join link leaked key into query: %s", link)
	}
	if !strings.Contains(link, "/s/abc123#k=secret-key") {
		t.Fatalf("join link = %s", link)
	}
}

func TestHandleEncryptedTextWritesClipboard(t *testing.T) {
	key, err := randomSessionKey()
	if err != nil {
		t.Fatal(err)
	}
	box, err := encryptPayload(key, clipboardPayload{Type: "text", Text: "from phone"})
	if err != nil {
		t.Fatal(err)
	}

	oldWriteText := writeClipboardTextFunc
	oldNotify := showNotificationFunc
	defer func() {
		writeClipboardTextFunc = oldWriteText
		showNotificationFunc = oldNotify
	}()

	var copied string
	var notices []string
	writeClipboardTextFunc = func(text string) error {
		copied = text
		return nil
	}
	showNotificationFunc = func(message string) {
		notices = append(notices, message)
	}

	app := &desktopApp{session: savedSession{Key: key}}
	app.handleEvent(context.Background(), relayEvent{Type: "clipboard.encrypted", Data: box})

	if copied != "from phone" {
		t.Fatalf("copied = %q", copied)
	}
	if len(notices) != 1 || notices[0] != "Text copied to clipboard" {
		t.Fatalf("notices = %#v", notices)
	}
}
