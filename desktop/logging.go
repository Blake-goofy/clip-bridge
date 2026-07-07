package main

import (
	"log"
	"os"
	"path/filepath"
)

func initLogging() func() {
	path := logPath()
	if path == "" {
		return func() {}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return func() {}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return func() {}
	}
	log.SetOutput(f)
	log.Printf("ClipBridge starting")
	return func() {
		log.Printf("ClipBridge stopping")
		_ = f.Close()
	}
}

func logPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "ClipBridge", "clipbridge.log")
}
