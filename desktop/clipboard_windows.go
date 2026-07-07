//go:build windows

package main

import (
	"bytes"
	"encoding/base64"
	"errors"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

func readClipboardPayload() (clipboardPayload, error) {
	if image, err := readClipboardImagePNG(); err == nil && image != "" {
		return clipboardPayload{Type: "image", MIME: "image/png", Data: image}, nil
	}
	text, err := readClipboardText()
	if err != nil {
		return clipboardPayload{}, err
	}
	return clipboardPayload{Type: "text", Text: text}, nil
}

func readClipboardText() (string, error) {
	cmd := powershell(`Get-Clipboard -Raw -Format Text`)
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return "", err
	}
	return strings.TrimSuffix(out.String(), "\r\n"), nil
}

func writeClipboardText(text string) error {
	cmd := powershell(`$input | Set-Clipboard`)
	cmd.Stdin = strings.NewReader(text)
	return cmd.Run()
}

func readClipboardImagePNG() (string, error) {
	path := filepath.Join(os.TempDir(), "clipbridge-clipboard.png")
	defer os.Remove(path)
	script := `
Add-Type -AssemblyName System.Windows.Forms,System.Drawing
$img = [System.Windows.Forms.Clipboard]::GetImage()
if ($null -eq $img) { exit 2 }
$img.Save($env:CLIPBRIDGE_IMAGE, [System.Drawing.Imaging.ImageFormat]::Png)
$img.Dispose()
`
	cmd := powershell(script)
	cmd.Env = append(os.Environ(), "CLIPBRIDGE_IMAGE="+path)
	if err := cmd.Run(); err != nil {
		return "", err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if _, err := png.DecodeConfig(bytes.NewReader(b)); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(b), nil
}

func writeClipboardImagePNG(data string) error {
	b, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return err
	}
	if _, err := png.DecodeConfig(bytes.NewReader(b)); err != nil {
		return err
	}
	f, err := os.CreateTemp("", "clipbridge-*.png")
	if err != nil {
		return err
	}
	path := f.Name()
	if _, err := f.Write(b); err != nil {
		f.Close()
		os.Remove(path)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(path)
		return err
	}
	defer os.Remove(path)
	// ponytail: PowerShell clipboard image handling is enough for PNG v1; move to direct Win32 calls if large images need lower latency.
	script := `
Add-Type -AssemblyName System.Windows.Forms,System.Drawing
$image = [System.Drawing.Image]::FromFile($env:CLIPBRIDGE_IMAGE)
[System.Windows.Forms.Clipboard]::SetImage($image)
$image.Dispose()
`
	cmd := powershell(script)
	cmd.Env = append(os.Environ(), "CLIPBRIDGE_IMAGE="+path)
	if err := cmd.Run(); err != nil {
		return err
	}
	return nil
}

func powershell(script string) *exec.Cmd {
	cmd := exec.Command("powershell", "-NoProfile", "-WindowStyle", "Hidden", "-Command", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	return cmd
}

func openBrowser(target string) error {
	if target == "" {
		return errors.New("empty url")
	}
	cmd := exec.Command("rundll32.exe", "url.dll,FileProtocolHandler", target)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if err := cmd.Start(); err == nil {
		return nil
	}
	cmd = exec.Command("explorer.exe", target)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	return cmd.Start()
}

func openAppWindow(target string) error {
	if target == "" {
		return errors.New("empty url")
	}
	for _, candidate := range edgeCandidates() {
		if path, err := exec.LookPath(candidate); err == nil {
			cmd := exec.Command(path, "--app="+target, "--new-window")
			cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
			return cmd.Start()
		}
	}
	return openBrowser(target)
}

func edgeCandidates() []string {
	var out []string
	for _, base := range []string{os.Getenv("ProgramFiles(x86)"), os.Getenv("ProgramFiles"), os.Getenv("LocalAppData")} {
		base = strings.TrimSpace(base)
		if base == "" {
			continue
		}
		out = append(out, filepath.Join(base, "Microsoft", "Edge", "Application", "msedge.exe"))
	}
	return append(out, "msedge.exe")
}
