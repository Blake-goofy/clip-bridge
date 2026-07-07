package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
)

const encryptedClipboardMIME = "application/vnd.clipbridge.encrypted+json"

type encryptedBox struct {
	Version int    `json:"v"`
	IV      string `json:"iv"`
	CT      string `json:"ct"`
}

type clipboardPayload struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
	MIME string `json:"mime,omitempty"`
	Data string `json:"data,omitempty"`
}

func randomSessionKey() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func encryptPayload(rawKey string, payload clipboardPayload) (string, error) {
	key, err := base64.RawURLEncoding.DecodeString(rawKey)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	iv := make([]byte, 12)
	if _, err := rand.Read(iv); err != nil {
		return "", err
	}
	plain, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	box := encryptedBox{
		Version: 1,
		IV:      base64.RawURLEncoding.EncodeToString(iv),
		CT:      base64.RawURLEncoding.EncodeToString(gcm.Seal(nil, iv, plain, nil)),
	}
	out, err := json.Marshal(box)
	return string(out), err
}

func decryptPayload(rawKey, data string) (clipboardPayload, error) {
	var box encryptedBox
	if err := json.Unmarshal([]byte(data), &box); err != nil {
		return clipboardPayload{}, err
	}
	if box.Version != 1 {
		return clipboardPayload{}, errors.New("unsupported encrypted payload")
	}
	key, err := base64.RawURLEncoding.DecodeString(rawKey)
	if err != nil {
		return clipboardPayload{}, err
	}
	iv, err := base64.RawURLEncoding.DecodeString(box.IV)
	if err != nil {
		return clipboardPayload{}, err
	}
	ct, err := base64.RawURLEncoding.DecodeString(box.CT)
	if err != nil {
		return clipboardPayload{}, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return clipboardPayload{}, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return clipboardPayload{}, err
	}
	plain, err := gcm.Open(nil, iv, ct, nil)
	if err != nil {
		return clipboardPayload{}, err
	}
	var payload clipboardPayload
	if err := json.Unmarshal(plain, &payload); err != nil {
		return clipboardPayload{}, err
	}
	return payload, nil
}
