package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAllowLocalRequestRequiresConfiguredOriginAndToken(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/status.json", nil)
	req.Header.Set("Origin", "https://clipbridge.cc")
	req.Header.Set("X-ClipBridge-Token", "secret")
	rec := httptest.NewRecorder()

	if !allowLocalRequest(rec, req, "https://clipbridge.cc", "secret") {
		t.Fatal("valid local bridge request was rejected")
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://clipbridge.cc" {
		t.Fatalf("allow origin = %q", got)
	}

	req = httptest.NewRequest(http.MethodGet, "/status.json", nil)
	req.Header.Set("Origin", "https://evil.example")
	req.Header.Set("X-ClipBridge-Token", "secret")
	if allowLocalRequest(httptest.NewRecorder(), req, "https://clipbridge.cc", "secret") {
		t.Fatal("wrong origin was accepted")
	}

	req = httptest.NewRequest(http.MethodGet, "/status.json", nil)
	req.Header.Set("Origin", "https://clipbridge.cc")
	req.Header.Set("X-ClipBridge-Token", "wrong")
	if allowLocalRequest(httptest.NewRecorder(), req, "https://clipbridge.cc", "secret") {
		t.Fatal("wrong token was accepted")
	}
}
