//go:build !windows

package main

import "errors"

func readClipboardPayload() (clipboardPayload, error) {
	return clipboardPayload{}, errors.New("windows only")
}
func readClipboardText() (string, error)     { return "", errors.New("windows only") }
func writeClipboardText(string) error        { return errors.New("windows only") }
func readClipboardImagePNG() (string, error) { return "", errors.New("windows only") }
func writeClipboardImagePNG(string) error    { return errors.New("windows only") }
func openBrowser(string) error               { return nil }
