package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

const defaultBaseURL = "https://clipbridge.app"

type config struct {
	BaseURL string       `json:"baseUrl"`
	Session savedSession `json:"session"`
}

type savedSession struct {
	SID         string `json:"sid"`
	Key         string `json:"key"`
	PCCookie    string `json:"pcCookie"`
	PCSecret    string `json:"pcSecret"`
	DisplayName string `json:"displayName"`
}

func loadConfig() (config, string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return config{}, "", err
	}
	path := filepath.Join(dir, "ClipBridge", "config.json")
	cfg := config{BaseURL: defaultServerBaseURL()}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return cfg, path, nil
	}
	if err != nil {
		return cfg, path, err
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		return cfg, path, err
	}
	cfg.BaseURL = cleanBaseURL(cfg.BaseURL)
	if cfg.BaseURL == "" {
		cfg.BaseURL = defaultServerBaseURL()
	}
	return cfg, path, nil
}

func defaultServerBaseURL() string {
	if v := cleanBaseURL(os.Getenv("CLIPBRIDGE_SERVER")); v != "" {
		return v
	}
	return defaultBaseURL
}

func saveConfig(path string, cfg config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0600)
}

func cleanBaseURL(value string) string {
	value = strings.TrimRight(strings.TrimSpace(value), "/")
	if value == "" {
		return ""
	}
	u, err := url.Parse(value)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return ""
	}
	return value
}

func restorePCCookie(jar http.CookieJar, base string, s savedSession) {
	if jar == nil || s.PCCookie == "" || s.PCSecret == "" {
		return
	}
	u, err := url.Parse(base)
	if err != nil {
		return
	}
	jar.SetCookies(u, []*http.Cookie{{Name: s.PCCookie, Value: s.PCSecret, Path: "/"}})
}

func pcCookieFromJar(jar http.CookieJar, base, sid string) (name, value string) {
	if jar == nil || sid == "" {
		return "", ""
	}
	u, err := url.Parse(base)
	if err != nil {
		return "", ""
	}
	want := "cb_pc_" + sid
	for _, c := range jar.Cookies(u) {
		if c.Name == want {
			return c.Name, c.Value
		}
	}
	return "", ""
}
