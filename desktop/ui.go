package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"log"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type localBridge struct {
	BaseURL string
	Token   string
}

type activateRequest struct {
	SID string `json:"sid"`
	Key string `json:"key"`
}

type approveRequest struct {
	ID string `json:"id"`
}

func startControlServer(ctx context.Context, app *desktopApp) (localBridge, error) {
	token, err := localBridgeToken()
	if err != nil {
		return localBridge{}, err
	}

	mux := http.NewServeMux()
	handle := func(fn func(http.ResponseWriter, *http.Request)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if !allowLocalRequest(w, r, app.cfg.BaseURL, token) {
				return
			}
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			fn(w, r)
		}
	}
	mux.HandleFunc("/status.json", handle(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		writeLocalJSON(w, http.StatusOK, app.snapshot())
	}))
	mux.HandleFunc("/session", handle(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		s, err := app.createSession(r.Context())
		if err != nil {
			writeLocalError(w, http.StatusBadGateway, err.Error())
			return
		}
		app.activateSession(s, "Ready.")
		app.restartRelay()
		writeLocalJSON(w, http.StatusCreated, map[string]string{"sid": s.SID, "key": s.Key, "name": s.DisplayName})
	}))
	mux.HandleFunc("/activate", handle(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		var req activateRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
			writeLocalError(w, http.StatusBadRequest, "invalid json")
			return
		}
		if err := app.activateKnownSession(r.Context(), req.SID, req.Key); err != nil {
			writeLocalError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeLocalJSON(w, http.StatusOK, app.snapshot())
	}))
	mux.HandleFunc("/send", handle(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		if err := app.sendClipboard(r.Context()); err != nil {
			log.Printf("send clipboard: %v", err)
			writeLocalError(w, http.StatusBadGateway, err.Error())
			return
		}
		writeLocalJSON(w, http.StatusAccepted, map[string]bool{"ok": true})
	}))
	mux.HandleFunc("/approve", handle(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		var req approveRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
			writeLocalError(w, http.StatusBadRequest, "invalid json")
			return
		}
		if err := app.approveJoin(r.Context(), req.ID); err != nil {
			log.Printf("allow join: %v", err)
			writeLocalError(w, http.StatusBadGateway, err.Error())
			return
		}
		writeLocalJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}))
	mux.HandleFunc("/quit", handle(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		go app.cancel()
		writeLocalJSON(w, http.StatusAccepted, map[string]bool{"ok": true})
	}))

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return localBridge{}, err
	}
	server := &http.Server{Handler: mux}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	go func() {
		if err := server.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Printf("control server: %v", err)
			app.cancel()
		}
	}()
	port := ln.Addr().(*net.TCPAddr).Port
	return localBridge{BaseURL: "http://127.0.0.1:" + strconv.Itoa(port), Token: token}, nil
}

func allowLocalRequest(w http.ResponseWriter, r *http.Request, siteBaseURL, token string) bool {
	origin := r.Header.Get("Origin")
	if origin != "" {
		site, err := url.Parse(siteBaseURL)
		if err != nil || origin != site.Scheme+"://"+site.Host {
			http.Error(w, "forbidden", http.StatusForbidden)
			return false
		}
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Vary", "Origin")
	}
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-ClipBridge-Token")
	if r.Method == http.MethodOptions {
		return true
	}
	got := r.Header.Get("X-ClipBridge-Token")
	if got == "" {
		got = r.URL.Query().Get("token")
	}
	if got == "" || got != token {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return false
	}
	return true
}

func writeLocalJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeLocalError(w http.ResponseWriter, status int, message string) {
	writeLocalJSON(w, status, map[string]string{"error": message})
}

func localBridgeToken() (string, error) {
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return strings.TrimRight(base64.URLEncoding.EncodeToString(b[:]), "="), nil
}
