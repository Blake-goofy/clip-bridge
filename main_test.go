package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "clipbridge-test-*")
	if err != nil {
		os.Exit(m.Run())
	}
	os.Setenv("ANALYTICS_PATH", filepath.Join(dir, "analytics.jsonl"))
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

func TestSessionIDUsesURLSafeToken(t *testing.T) {
	h := newHub()
	sid, _, err := h.createSession()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := base64.RawURLEncoding.DecodeString(sid)
	if err != nil || len(raw) != 9 {
		t.Fatalf("session id is not a 9-byte URL-safe token: sid=%q len=%d err=%v", sid, len(raw), err)
	}
}

func TestMultipleMobilesAndCookieReuse(t *testing.T) {
	h := newHub()
	sid, _, err := h.createSession()
	if err != nil {
		t.Fatal(err)
	}
	token, setCookie, err := h.joinMobile(sid, "", "iPhone")
	if err != nil {
		t.Fatal(err)
	}
	if !setCookie || token == "" {
		t.Fatal("first join did not issue a mobile token")
	}
	if _, setCookie, err := h.joinMobile(sid, token, "iPhone"); err != nil || setCookie {
		t.Fatalf("same mobile should rejoin without a new cookie: setCookie=%v err=%v", setCookie, err)
	}
	secondToken, setCookie, err := h.joinMobile(sid, "wrong", "Other")
	if err != nil {
		t.Fatal(err)
	}
	if !setCookie || secondToken == "" || secondToken == token {
		t.Fatalf("second mobile should receive a distinct cookie: setCookie=%v token=%q", setCookie, secondToken)
	}
}

func TestDefaultDeviceNamesAndMobileRenameBroadcast(t *testing.T) {
	h := newHub()
	sid, _, err := h.createSession()
	if err != nil {
		t.Fatal(err)
	}
	firstToken, _, err := h.joinMobile(sid, "", "")
	if err != nil {
		t.Fatal(err)
	}
	secondToken, _, err := h.joinMobile(sid, "", "")
	if err != nil {
		t.Fatal(err)
	}
	secondCh, err := h.connectMobile(sid, secondToken)
	if err != nil {
		t.Fatal(err)
	}

	initial := readDevicesEvent(t, "second", secondCh)
	names := map[string]string{}
	var firstID string
	for _, d := range initial.Devices {
		names[d.Name] = d.ID
		if d.Name == "Device2" {
			firstID = d.ID
		}
	}
	for _, want := range []string{"Device1", "Device2", "Device3"} {
		if names[want] == "" {
			t.Fatalf("missing default device name %q in %#v", want, initial.Devices)
		}
	}
	if err := h.renameDevice(sid, firstToken, firstID, "Blake phone"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		updated := readDevicesEvent(t, "second", secondCh)
		for _, d := range updated.Devices {
			if d.ID == firstID && d.Name == "Blake phone" {
				return
			}
		}
	}
	t.Fatal("renamed device was not broadcast")
}

func TestSessionRenameBroadcastsToConnectedAndNewDevices(t *testing.T) {
	h := newHub()
	sid, pcToken, err := h.createSession()
	if err != nil {
		t.Fatal(err)
	}
	pcCh, err := h.connectPC(sid, pcToken)
	if err != nil {
		t.Fatal(err)
	}
	mobileToken, _, err := h.joinMobile(sid, "", "Phone")
	if err != nil {
		t.Fatal(err)
	}
	mobileCh, err := h.connectMobile(sid, mobileToken)
	if err != nil {
		t.Fatal(err)
	}

	if err := h.renameSession(sid, pcToken, "Personal"); err != nil {
		t.Fatal(err)
	}
	readSessionNameEvent(t, "pc", pcCh, "Personal")
	readSessionNameEvent(t, "mobile", mobileCh, "Personal")

	tabletToken, _, err := h.joinMobile(sid, "", "Tablet")
	if err != nil {
		t.Fatal(err)
	}
	tabletCh, err := h.connectMobile(sid, tabletToken)
	if err != nil {
		t.Fatal(err)
	}
	readSessionNameEvent(t, "tablet", tabletCh, "Personal")
}

func TestRotateJoinLinkDropsOtherDevicesAndStaleLink(t *testing.T) {
	h := newHub()
	sid, pcToken, err := h.createSession()
	if err != nil {
		t.Fatal(err)
	}
	pcCh, err := h.connectPC(sid, pcToken)
	if err != nil {
		t.Fatal(err)
	}
	<-pcCh
	mobileToken, _, err := h.joinMobile(sid, "", "")
	if err != nil {
		t.Fatal(err)
	}
	readDevicesEvent(t, "pc", pcCh)
	mobileCh, err := h.connectMobile(sid, mobileToken)
	if err != nil {
		t.Fatal(err)
	}
	readDevicesEvent(t, "mobile", mobileCh)

	newSID, err := h.rotateJoinLink(sid, pcToken)
	if err != nil {
		t.Fatal(err)
	}
	if newSID == sid {
		t.Fatal("rotated join link reused original sid")
	}
	if !h.exists(newSID) {
		t.Fatal("rotated join link does not resolve")
	}
	if h.joinLinkExists(sid) {
		t.Fatal("old join link should be stale")
	}
	linkEvent := readLinkEvent(t, "pc", pcCh)
	if linkEvent.SID != newSID {
		t.Fatalf("link event sid = %q, want %q", linkEvent.SID, newSID)
	}
	devices := readDevicesEvent(t, "pc", pcCh)
	if len(devices.Devices) != 1 || devices.Devices[0].Name != "Device1" {
		t.Fatalf("devices after rotated join = %#v, want only requester", devices.Devices)
	}
	select {
	case _, ok := <-mobileCh:
		if ok {
			t.Fatal("stale mobile channel should be closed")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("stale mobile channel should be closed")
	}
	if err := h.verifyMobile(sid, mobileToken); err == nil {
		t.Fatal("stale mobile token should not verify")
	}
	if err := h.relayClipboard(sid, mobileToken, "hello"); err != errUnauthorized {
		t.Fatalf("stale mobile send err = %v, want unauthorized", err)
	}
	if _, _, err := h.joinMobile(sid, "", ""); err == nil {
		t.Fatal("old join link should not accept new devices")
	}
	if _, _, err := h.joinMobile(newSID, "", ""); err != nil {
		t.Fatal(err)
	}
	devices = readDevicesEvent(t, "pc", pcCh)
	if len(devices.Devices) != 2 {
		t.Fatalf("devices after new link join = %d, want 2: %#v", len(devices.Devices), devices.Devices)
	}
}

func TestExpiryCleanup(t *testing.T) {
	h := newHub()
	now := time.Date(2026, 6, 23, 12, 0, 0, 0, time.UTC)
	h.now = func() time.Time { return now }
	sid, _, err := h.createSession()
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(unpairedTTL + time.Second)
	if h.exists(sid) {
		t.Fatal("unpaired session should expire")
	}
}

func TestNoDeliveryWhenPCGone(t *testing.T) {
	a := newApp()
	sid, _, err := a.hub.createSession()
	if err != nil {
		t.Fatal(err)
	}
	mobileToken, _, err := a.hub.joinMobile(sid, "", "iPhone")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.hub.relayClipboardToPC(sid, mobileToken, "hello"); err != errPCGone {
		t.Fatalf("expected errPCGone, got %v", err)
	}
}

func TestPCDisconnectRetainsSession(t *testing.T) {
	h := newHub()
	sid, pcToken, err := h.createSession()
	if err != nil {
		t.Fatal(err)
	}
	ch, err := h.connectPC(sid, pcToken)
	if err != nil {
		t.Fatal(err)
	}
	h.disconnectPC(sid, ch)
	if err := h.verifyPC(sid, pcToken); err != nil {
		t.Fatalf("pc session should survive disconnect: %v", err)
	}
}

func TestPCBroadcastsClipboardToConnectedMobiles(t *testing.T) {
	h := newHub()
	sid, pcToken, err := h.createSession()
	if err != nil {
		t.Fatal(err)
	}
	firstToken, _, err := h.joinMobile(sid, "", "First")
	if err != nil {
		t.Fatal(err)
	}
	secondToken, _, err := h.joinMobile(sid, "", "Second")
	if err != nil {
		t.Fatal(err)
	}
	firstCh, err := h.connectMobile(sid, firstToken)
	if err != nil {
		t.Fatal(err)
	}
	secondCh, err := h.connectMobile(sid, secondToken)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.relayClipboardToMobile(sid, pcToken, "hello all"); err != nil {
		t.Fatal(err)
	}
	for name, ch := range map[string]chan event{"first": firstCh, "second": secondCh} {
		got := readClipboardEvent(t, name, ch)
		if got.Text != "hello all" {
			t.Fatalf("%s got wrong text %q", name, got.Text)
		}
	}
}

func TestPCReceivesDeviceListUpdates(t *testing.T) {
	h := newHub()
	sid, pcToken, err := h.createSession()
	if err != nil {
		t.Fatal(err)
	}
	pcCh, err := h.connectPC(sid, pcToken)
	if err != nil {
		t.Fatal(err)
	}
	<-pcCh
	firstToken, _, err := h.joinMobile(sid, "", "First")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.connectMobile(sid, firstToken); err != nil {
		t.Fatal(err)
	}
	secondToken, _, err := h.joinMobile(sid, "", "Second")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.connectMobile(sid, secondToken); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 6; i++ {
		latest := readDevicesEvent(t, "pc", pcCh)
		connected := 0
		for _, d := range latest.Devices {
			if d.Connected {
				connected++
			}
		}
		if connected == 3 {
			return
		}
	}
	t.Fatal("pc did not receive all connected devices")
}

func TestDeviceListTracksLiveConnections(t *testing.T) {
	h := newHub()
	sid, pcToken, err := h.createSession()
	if err != nil {
		t.Fatal(err)
	}
	pcCh, err := h.connectPC(sid, pcToken)
	if err != nil {
		t.Fatal(err)
	}
	<-pcCh
	mobileToken, _, err := h.joinMobile(sid, "", "Phone")
	if err != nil {
		t.Fatal(err)
	}
	mobileCh, err := h.connectMobile(sid, mobileToken)
	if err != nil {
		t.Fatal(err)
	}
	readDevicesEvent(t, "mobile", mobileCh)

	h.disconnectPC(sid, pcCh)
	devices := readDevicesEvent(t, "mobile", mobileCh)
	got := map[string]bool{}
	for _, d := range devices.Devices {
		got[d.Name] = d.Connected
	}
	if got["Device1"] {
		t.Fatalf("pc should be offline after websocket disconnect: %#v", devices.Devices)
	}
	if !got["Phone"] {
		t.Fatalf("phone should stay connected: %#v", devices.Devices)
	}
}

func TestMobileCanSendAfterPCReconnect(t *testing.T) {
	h := newHub()
	sid, pcToken, err := h.createSession()
	if err != nil {
		t.Fatal(err)
	}
	pcCh, err := h.connectPC(sid, pcToken)
	if err != nil {
		t.Fatal(err)
	}
	<-pcCh
	mobileToken, _, err := h.joinMobile(sid, "", "Phone")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.connectMobile(sid, mobileToken); err != nil {
		t.Fatal(err)
	}
	h.disconnectPC(sid, pcCh)
	if err := h.relayClipboard(sid, mobileToken, "miss"); err != errPCGone {
		t.Fatalf("send after pc disconnect = %v, want pc gone", err)
	}
	pcCh, err = h.connectPC(sid, pcToken)
	if err != nil {
		t.Fatal(err)
	}
	readDevicesEvent(t, "pc", pcCh)
	if err := h.relayClipboard(sid, mobileToken, "hit"); err != nil {
		t.Fatal(err)
	}
	if got := readClipboardEvent(t, "pc", pcCh); got.Text != "hit" {
		t.Fatalf("pc got %q", got.Text)
	}
}

func TestResumeRequiresPCCookie(t *testing.T) {
	a := newApp()
	ts := httptest.NewServer(a)
	defer ts.Close()

	res, err := http.Post(ts.URL+"/api/session", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var created struct {
		SID string `json:"sid"`
	}
	if err := json.NewDecoder(res.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	pcCookie := findCookie(t, res.Cookies(), pcCookieName(created.SID))

	unauthorized, err := http.Post(ts.URL+"/api/session/"+created.SID+"/resume", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer unauthorized.Body.Close()
	if unauthorized.StatusCode != http.StatusUnauthorized {
		t.Fatalf("resume without pc cookie status %d", unauthorized.StatusCode)
	}

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/session/"+created.SID+"/resume", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.AddCookie(pcCookie)
	resumed, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resumed.Body.Close()
	if resumed.StatusCode != http.StatusOK {
		t.Fatalf("resume with pc cookie status %d", resumed.StatusCode)
	}
}

func TestDesktopCanRenameAndDisconnectDevice(t *testing.T) {
	a := newApp()
	sid, pcToken, err := a.hub.createSession()
	if err != nil {
		t.Fatal(err)
	}
	mobileToken, _, err := a.hub.joinMobile(sid, "", "Mobile")
	if err != nil {
		t.Fatal(err)
	}
	a.hub.mu.Lock()
	deviceID := findDeviceByTokenLocked(a.hub.sessions[sid], mobileToken).id
	a.hub.mu.Unlock()
	if deviceID == "" {
		t.Fatal("missing joined device")
	}

	if err := a.hub.renameDevice(sid, pcToken, deviceID, "Blake phone"); err != nil {
		t.Fatal(err)
	}
	a.hub.mu.Lock()
	got := a.hub.sessions[sid].devices[deviceID].name
	a.hub.mu.Unlock()
	if got != "Blake phone" {
		t.Fatalf("device name = %q", got)
	}
	if err := a.hub.removeDevice(sid, pcToken, deviceID); err != nil {
		t.Fatal(err)
	}
	if err := a.hub.verifyMobile(sid, mobileToken); err == nil {
		t.Fatal("removed device token should no longer verify")
	}
}

func TestCannotDeleteActiveDevice(t *testing.T) {
	h := newHub()
	sid, pcToken, err := h.createSession()
	if err != nil {
		t.Fatal(err)
	}
	h.mu.Lock()
	pcID := findDeviceByTokenLocked(h.sessions[sid], pcToken).id
	h.mu.Unlock()
	if err := h.removeDevice(sid, pcToken, pcID); err != errActiveDevice {
		t.Fatalf("delete active device err = %v, want %v", err, errActiveDevice)
	}
	if err := h.verifyPC(sid, pcToken); err != nil {
		t.Fatalf("active device should remain valid: %v", err)
	}
}

func TestCloseSessionRequiresPCToken(t *testing.T) {
	h := newHub()
	sid, pcToken, err := h.createSession()
	if err != nil {
		t.Fatal(err)
	}
	mobileToken, _, err := h.joinMobile(sid, "", "Phone")
	if err != nil {
		t.Fatal(err)
	}
	if err := h.closeSession(sid, mobileToken); err != errUnauthorized {
		t.Fatalf("mobile close err = %v, want unauthorized", err)
	}
	pcCh, err := h.connectPC(sid, pcToken)
	if err != nil {
		t.Fatal(err)
	}
	readDevicesEvent(t, "pc", pcCh)
	if err := h.closeSession(sid, pcToken); err != nil {
		t.Fatal(err)
	}
	select {
	case _, ok := <-pcCh:
		if ok {
			t.Fatal("closed session should close pc channel")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("closed session should close pc channel")
	}
	if h.exists(sid) {
		t.Fatal("closed session should not exist")
	}
}

func TestEncryptedClipboardPayloadRelay(t *testing.T) {
	a := newApp()
	sid, pcToken, err := a.hub.createSession()
	if err != nil {
		t.Fatal(err)
	}
	pcCh, err := a.hub.connectPC(sid, pcToken)
	if err != nil {
		t.Fatal(err)
	}
	mobileToken, _, err := a.hub.joinMobile(sid, "", "Phone")
	if err != nil {
		t.Fatal(err)
	}

	box := `{"v":1,"iv":"abc","ct":"def"}`
	req := httptest.NewRequest(http.MethodPost, "/api/session/"+sid+"/clipboard", strings.NewReader(`{"mime":"`+encryptedClipboardMIME+`","data":`+strconv.Quote(box)+`}`))
	req.AddCookie(&http.Cookie{Name: mobileCookieName(sid), Value: mobileToken})
	w := httptest.NewRecorder()
	a.ServeHTTP(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("encrypted send status = %d, body %s", w.Code, w.Body.String())
	}
	got := readEncryptedEvent(t, "pc", pcCh)
	if got.MIME != encryptedClipboardMIME || got.Data != box {
		t.Fatalf("wrong encrypted event %#v", got)
	}
}

func TestOversizedPayloadRejected(t *testing.T) {
	a := newApp()
	sid, _, err := a.hub.createSession()
	if err != nil {
		t.Fatal(err)
	}
	mobileToken, _, err := a.hub.joinMobile(sid, "", "iPhone")
	if err != nil {
		t.Fatal(err)
	}
	body := `{"text":"` + strings.Repeat("a", maxTextBytes+1) + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api/session/"+sid+"/clipboard", strings.NewReader(body))
	req.AddCookie(&http.Cookie{Name: mobileCookieName(sid), Value: mobileToken})
	w := httptest.NewRecorder()
	a.ServeHTTP(w, req)
	if w.Code != http.StatusRequestEntityTooLarge && w.Code != http.StatusBadRequest {
		t.Fatalf("expected payload rejection, got %d", w.Code)
	}
}

func TestImageClipboardPayloadRelayAndValidation(t *testing.T) {
	a := newApp()
	sid, pcToken, err := a.hub.createSession()
	if err != nil {
		t.Fatal(err)
	}
	pcCh, err := a.hub.connectPC(sid, pcToken)
	if err != nil {
		t.Fatal(err)
	}
	mobileToken, _, err := a.hub.joinMobile(sid, "", "Phone")
	if err != nil {
		t.Fatal(err)
	}

	pngData := "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+/p9sAAAAASUVORK5CYII="
	req := httptest.NewRequest(http.MethodPost, "/api/session/"+sid+"/clipboard", strings.NewReader(`{"mime":"image/png","data":"`+pngData+`"}`))
	req.AddCookie(&http.Cookie{Name: mobileCookieName(sid), Value: mobileToken})
	w := httptest.NewRecorder()
	a.ServeHTTP(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("image send status = %d, body %s", w.Code, w.Body.String())
	}
	got := readImageEvent(t, "pc", pcCh)
	if got.MIME != "image/png" || got.Data != pngData {
		t.Fatalf("wrong image event %#v", got)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/session/"+sid+"/clipboard", strings.NewReader(`{"mime":"image/jpeg","data":"/9j/"}`))
	req.AddCookie(&http.Cookie{Name: mobileCookieName(sid), Value: mobileToken})
	w = httptest.NewRecorder()
	a.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("jpeg send status = %d, want 400", w.Code)
	}
}

func TestAnalyticsTracksOnlyJoinsAndClipboardShares(t *testing.T) {
	a := newApp()
	a.analytics.path = filepath.Join(t.TempDir(), "analytics.jsonl")
	fixedNow := time.Date(2026, 6, 29, 12, 0, 0, 0, time.UTC)
	a.hub.now = func() time.Time { return fixedNow }

	sid, pcToken, err := a.hub.createSession()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.hub.connectPC(sid, pcToken); err != nil {
		t.Fatal(err)
	}
	mobileToken, setCookie, err := a.hub.joinMobile(sid, "", "Device2")
	if err != nil {
		t.Fatal(err)
	}
	if !setCookie {
		t.Fatal("mobile join should issue a token")
	}
	if _, err := a.hub.connectMobile(sid, mobileToken); err != nil {
		t.Fatal(err)
	}
	a.recordAnalytics("device_joined")

	secret := "secret clipboard text"
	req := httptest.NewRequest(http.MethodPost, "/api/session/"+sid+"/clipboard", strings.NewReader(`{"text":`+strconv.Quote(secret)+`}`))
	req.AddCookie(&http.Cookie{Name: mobileCookieName(sid), Value: mobileToken})
	w := httptest.NewRecorder()
	a.ServeHTTP(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("clipboard status = %d", w.Code)
	}

	summary, err := a.analyticsSummary("30")
	if err != nil {
		t.Fatal(err)
	}
	if summary.ClipboardShares != 1 || summary.DevicesJoined != 1 {
		t.Fatalf("summary = %+v, want one share and one joined device", summary)
	}
	if len(summary.Daily) != 1 || summary.Daily[0].ClipboardShares != 1 || summary.Daily[0].DevicesJoined != 1 {
		t.Fatalf("daily summary = %+v, want one share and one joined device today", summary.Daily)
	}
	b, err := os.ReadFile(a.analytics.path)
	if err != nil {
		t.Fatal(err)
	}
	logged := string(b)
	if !strings.Contains(logged, "device_joined") || !strings.Contains(logged, "clipboard_shared") {
		t.Fatalf("analytics log missing expected events: %s", logged)
	}
	for _, forbidden := range []string{secret, sid, mobileToken, "clipboard.text", "visitorHash", "sessionHash", `"t"`, `"event"`} {
		if strings.Contains(logged, forbidden) {
			t.Fatalf("analytics log stored %q: %s", forbidden, logged)
		}
	}
	for _, line := range strings.Split(strings.TrimSpace(logged), "\n") {
		var e analyticsLogEvent
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("invalid analytics json line %q: %v", line, err)
		}
		if e.Date != "2026-06-29" || (e.Event != "device_joined" && e.Event != "clipboard_shared") {
			t.Fatalf("unexpected analytics event: %+v", e)
		}
	}
}

func TestAnalyticsSummaryRangeFiltersAndBackfillsDays(t *testing.T) {
	a := newApp()
	a.analytics.path = filepath.Join(t.TempDir(), "analytics.jsonl")
	a.hub.now = func() time.Time { return time.Date(2026, 6, 29, 12, 0, 0, 0, time.UTC) }
	log := strings.Join([]string{
		`{"d":"2026-06-20","e":"clipboard_shared"}`,
		`{"t":"2026-06-25T09:00:00Z","event":"clipboard_shared"}`,
		`{"d":"2026-06-29","e":"device_joined"}`,
		`{"d":"2026-06-29","e":"ignored"}`,
	}, "\n") + "\n"
	if err := os.WriteFile(a.analytics.path, []byte(log), 0600); err != nil {
		t.Fatal(err)
	}

	summary, err := a.analyticsSummary("7")
	if err != nil {
		t.Fatal(err)
	}
	if summary.Range != "7" || summary.RangeLabel != "Last 7 days" {
		t.Fatalf("range = %q %q, want 7 / Last 7 days", summary.Range, summary.RangeLabel)
	}
	if summary.ClipboardShares != 1 || summary.DevicesJoined != 1 {
		t.Fatalf("summary = %+v, want one recent share and join", summary)
	}
	if len(summary.ChartDays) != 7 {
		t.Fatalf("chart days = %d, want 7", len(summary.ChartDays))
	}
	if summary.ChartDays[0].Date != "2026-06-23" || summary.ChartDays[6].Date != "2026-06-29" {
		t.Fatalf("chart range = %s..%s, want 2026-06-23..2026-06-29", summary.ChartDays[0].Date, summary.ChartDays[6].Date)
	}
	if !summary.Chart.HasData || summary.Chart.SharesLine == "" || summary.Chart.JoinsLine == "" {
		t.Fatalf("chart = %+v, want line data", summary.Chart)
	}

	all, err := a.analyticsSummary("all")
	if err != nil {
		t.Fatal(err)
	}
	if all.ClipboardShares != 2 || all.DevicesJoined != 1 {
		t.Fatalf("all summary = %+v, want all tracked events", all)
	}
	if all.ChartDays[0].Date != "2026-06-20" {
		t.Fatalf("all chart starts %s, want first event day", all.ChartDays[0].Date)
	}
}

func TestAnalyticsPageRendersDashboard(t *testing.T) {
	a := newApp()
	a.analytics.path = filepath.Join(t.TempDir(), "analytics.jsonl")
	a.hub.now = func() time.Time { return time.Date(2026, 6, 29, 12, 0, 0, 0, time.UTC) }
	if err := os.WriteFile(a.analytics.path, []byte(`{"d":"2026-06-29","e":"clipboard_shared"}`+"\n"), 0600); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/analytics?range=7", nil)
	w := httptest.NewRecorder()
	a.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("analytics status = %d", w.Code)
	}
	body := w.Body.String()
	for _, want := range []string{
		`class="analytics-dashboard"`,
		`<a class="home-link" href="/"><img class="brand-icon" src="/favicon.svg" alt="">ClipBridge</a>`,
		`class="dashboard-toolbar"`,
		`Analytics trend`,
		`href="/analytics?range=7" aria-current="page"`,
		"Clipboard shares",
		"Devices joined",
		"color: #fff;",
		`<footer class="site-footer">`,
		`<a href="/">Home</a> | <a href="/privacy">Privacy</a> | <a href="/terms">Terms</a>`,
		"<polyline",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("analytics page missing %q", want)
		}
	}
	if strings.Contains(body, "htmx") {
		t.Fatal("analytics page should not add htmx for a static dashboard")
	}
	if strings.Contains(body, "Successful joins and clipboard shares. Nothing user-identifying.") {
		t.Fatal("analytics page should not render the old subtitle")
	}
	if strings.Contains(body, "Blake Becker") {
		t.Fatal("analytics footer should not include author text")
	}
	csp := w.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "style-src 'nonce-") || !strings.Contains(csp, "img-src 'self'") {
		t.Fatalf("analytics CSP = %q", csp)
	}
}

func TestDocumentPagesUseDarkThemeAndFooter(t *testing.T) {
	a := newApp()
	for _, tt := range []struct {
		path          string
		title         string
		wantFooter    string
		forbidSelfRef string
	}{
		{
			path:          "/privacy",
			title:         "Privacy Policy",
			wantFooter:    `<a href="/">Home</a> | <a href="/analytics">Analytics</a> | <a href="/terms">Terms</a> |`,
			forbidSelfRef: `<a href="/privacy">Privacy</a>`,
		},
		{
			path:          "/terms",
			title:         "Terms of Service",
			wantFooter:    `<a href="/">Home</a> | <a href="/analytics">Analytics</a> | <a href="/privacy">Privacy</a> |`,
			forbidSelfRef: `<a href="/terms">Terms</a>`,
		},
	} {
		req := httptest.NewRequest(http.MethodGet, tt.path, nil)
		w := httptest.NewRecorder()
		a.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("%s status = %d", tt.path, w.Code)
		}
		body := w.Body.String()
		for _, want := range []string{
			"<title>" + tt.title + " - ClipBridge</title>",
			`<main class="document-page">`,
			`<a class="page-brand" href="/"><img class="brand-icon" src="/favicon.svg" alt="">ClipBridge</a>`,
			"background: #181a17;",
			"color: #fff;",
			"justify-content: center;",
			tt.wantFooter,
			"Source code",
		} {
			if !strings.Contains(body, want) {
				t.Fatalf("%s missing %q", tt.path, want)
			}
		}
		if strings.Contains(body, tt.forbidSelfRef) {
			t.Fatalf("%s footer links to itself", tt.path)
		}
		if strings.Contains(body, "Blake Becker") {
			t.Fatalf("%s footer should not include author text", tt.path)
		}
		csp := w.Header().Get("Content-Security-Policy")
		if !strings.Contains(csp, "style-src 'nonce-") || !strings.Contains(csp, "img-src 'self'") {
			t.Fatalf("%s CSP = %q", tt.path, csp)
		}
	}
}

func TestIndexUsesNeutralClipboardUI(t *testing.T) {
	ui := indexHTML + appCSS + appJS
	for _, old := range []string{"Windows", "iPhone", "Safari", "No phone", "Connected to PC", "Send Clipboard", "<title>Clip Bridge</title>", "Waiting.", "receiver", "sender", "New Session", "Add link", "border-radius: 24px", "box-shadow", "notif-overlay", "qrWrap.classList.toggle(\"hidden\", event.connected)", "transform: translateX(100%)", "syncJoinedPaneLayout", "copyConnectURL", "connectURL", "copy-link", "?fragment=", "qrQuery", "session-select", "session-delete", "reveal-delete", "swipeStartX", "readClipboardImage"} {
		if strings.Contains(ui, old) {
			t.Fatalf("index still contains platform-specific or removed UI text %q", old)
		}
	}
	for _, want := range []string{"<title>ClipBridge</title>", `<link rel="stylesheet" href="/app.css">`, `<script src="/qrcode.js"></script>`, `<script src="/app.js"></script>`, `<link rel="icon" href="/favicon.svg" type="image/svg+xml">`, `<h1><img class="brand-icon" src="/favicon.svg" alt="">ClipBridge</h1>`, "Secure clipboard handoff", "Send clipboard", "Peek clipboard", `class="site-footer"`, "grid-template-rows: minmax(0, 1fr) auto", "min-height: 0", ".app-layout.pc-mode .site-footer", ".site-footer a:hover", "text-decoration: underline", "Analytics", "Privacy", "Terms", "Source code", "localStorage", "/resume", "sessionPane", "sessionPaneToggle", "devicePane", "devicePaneToggle", "body.pc-mode .desktop-pane", "syncPaneLayout", "mobileQr", "toggleQR", "drawQRCode", "ClipBridgeQRCode", "addSession", "Add session", "sessionList", "sessionModal", "sessionNameInput", "defaultSessionName", "connectedDeviceCount", "edit-session-button", "notice.peek", "notice.peek .notif-status", "notif-image", "readClipboardContent", "readClipboardPreview", "text/plain", "showClipboardPeek", "setupPeekButton", "peekAutoHideMs = 2500", "noticeHiddenQR", "dataset.noticeHidden", "setTimeout(hideNotice, 2500)", "(hover: none) and (pointer: coarse)", "position: fixed", "padding-right: 0", "encryptedClipboardMIME", "copySelectedLink", "sessionLink", "/name", "event.type === \"session\"", "updateSessionName", "deviceCount", "/disconnect", "pcActions", "pcMessages", "mobileMessages", "navigator.clipboard.writeText(text || \"\")", "position: sticky", "onMiddleClick", "onauxclick", "deleteDevice(device)", "width: 100vw", "border-radius: 8px 8px 0 0"} {
		if !strings.Contains(ui, want) {
			t.Fatalf("index is missing chat UI marker %q", want)
		}
	}
	if strings.Contains(indexHTML, "Blake Becker") {
		t.Fatal("index footer should not include author text")
	}
}

func TestIndexCSPAllowsRenderedImagePreviews(t *testing.T) {
	a := newApp()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	a.ServeHTTP(w, req)
	got := w.Header().Get("Content-Security-Policy")
	for _, want := range []string{"img-src 'self' data:", "style-src 'self'", "script-src 'self'"} {
		if !strings.Contains(got, want) {
			t.Fatalf("CSP = %q, want %q", got, want)
		}
	}
}

func TestFaviconServed(t *testing.T) {
	a := newApp()
	req := httptest.NewRequest(http.MethodGet, "/favicon.svg", nil)
	w := httptest.NewRecorder()
	a.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("favicon status = %d", w.Code)
	}
	if got := w.Header().Get("Content-Type"); got != "image/svg+xml; charset=utf-8" {
		t.Fatalf("favicon content type = %q", got)
	}
	if !strings.Contains(w.Body.String(), `stroke="currentColor"`) {
		t.Fatal("favicon does not use theme-aware currentColor stroke")
	}
}

func TestQRCodeJSServed(t *testing.T) {
	a := newApp()
	req := httptest.NewRequest(http.MethodGet, "/qrcode.js", nil)
	w := httptest.NewRecorder()
	a.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("qrcode js status = %d", w.Code)
	}
	if got := w.Header().Get("Content-Type"); got != "application/javascript; charset=utf-8" {
		t.Fatalf("qrcode js content type = %q", got)
	}
	if body := w.Body.String(); !strings.Contains(body, "ClipBridgeQRCode") || strings.Contains(body, "?fragment=") {
		t.Fatal("qrcode js should expose local QR renderer without fragment transport")
	}
}

func TestAppAssetsServed(t *testing.T) {
	a := newApp()
	tests := []struct {
		path        string
		contentType string
		markers     []string
	}{
		{path: "/app.css", contentType: "text/css; charset=utf-8", markers: []string{".app-layout", ".notice.peek", "body.pc-mode .desktop-pane"}},
		{path: "/app.js", contentType: "application/javascript; charset=utf-8", markers: []string{"encryptedClipboardMIME", "connectPCSession", "readClipboardContent"}},
	}
	for _, tt := range tests {
		req := httptest.NewRequest(http.MethodGet, tt.path, nil)
		w := httptest.NewRecorder()
		a.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("%s status = %d", tt.path, w.Code)
		}
		if got := w.Header().Get("Content-Type"); got != tt.contentType {
			t.Fatalf("%s content type = %q", tt.path, got)
		}
		body := w.Body.String()
		for _, marker := range tt.markers {
			if !strings.Contains(body, marker) {
				t.Fatalf("%s is missing marker %q", tt.path, marker)
			}
		}
	}
}

func TestWebSocketClipboardRelayFromPCToMobile(t *testing.T) {
	a := newApp()
	ts := httptest.NewServer(a)
	defer ts.Close()

	res, err := http.Post(ts.URL+"/api/session", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var created struct {
		SID string `json:"sid"`
	}
	if err := json.NewDecoder(res.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	pcCookie := findCookie(t, res.Cookies(), pcCookieName(created.SID))

	pcWSURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws/" + created.SID + "/pc"
	pcConn, _, err := websocket.Dial(context.Background(), pcWSURL, &websocket.DialOptions{
		HTTPHeader: http.Header{"Cookie": []string{pcCookie.String()}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer pcConn.Close(websocket.StatusNormalClosure, "")

	mobileCookie := joinMobileWithApprovalHTTP(t, ts.URL, created.SID, pcCookie, pcConn, "Device")

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws/" + created.SID + "/mobile"
	conn, _, err := websocket.Dial(context.Background(), wsURL, &websocket.DialOptions{
		HTTPHeader: http.Header{"Cookie": []string{mobileCookie.String()}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/session/"+created.SID+"/clipboard", bytes.NewBufferString(`{"text":"hello mobile"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(pcCookie)
	sendRes, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer sendRes.Body.Close()
	if sendRes.StatusCode != http.StatusAccepted {
		t.Fatalf("send status %d", sendRes.StatusCode)
	}

	msg := readClipboardWS(t, conn)
	if msg.Text != "hello mobile" {
		t.Fatalf("wrong message %#v", msg)
	}
}

func TestWebSocketClipboardRelay(t *testing.T) {
	a := newApp()
	ts := httptest.NewServer(a)
	defer ts.Close()

	res, err := http.Post(ts.URL+"/api/session", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var created struct {
		SID string `json:"sid"`
	}
	if err := json.NewDecoder(res.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	if created.SID == "" {
		t.Fatal("missing session id")
	}
	pcCookie := findCookie(t, res.Cookies(), pcCookieName(created.SID))

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws/" + created.SID + "/pc"
	conn, _, err := websocket.Dial(context.Background(), wsURL, &websocket.DialOptions{
		HTTPHeader: http.Header{"Cookie": []string{pcCookie.String()}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	mobileCookie := joinMobileWithApprovalHTTP(t, ts.URL, created.SID, pcCookie, conn, "iPhone")

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/session/"+created.SID+"/clipboard", bytes.NewBufferString(`{"text":"hello pc"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(mobileCookie)
	sendRes, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer sendRes.Body.Close()
	if sendRes.StatusCode != http.StatusAccepted {
		t.Fatalf("send status %d", sendRes.StatusCode)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	for {
		_, b, err := conn.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var msg event
		if err := json.Unmarshal(b, &msg); err != nil {
			t.Fatal(err)
		}
		if msg.Type == "clipboard.text" {
			if msg.Text != "hello pc" {
				t.Fatalf("wrong text %q", msg.Text)
			}
			return
		}
	}
}

func readClipboardEvent(t *testing.T, name string, ch chan event) event {
	t.Helper()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for {
		select {
		case got := <-ch:
			if got.Type == "clipboard.text" {
				return got
			}
		case <-timer.C:
			t.Fatalf("%s mobile did not receive clipboard event", name)
		}
	}
}

func readImageEvent(t *testing.T, name string, ch chan event) event {
	t.Helper()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for {
		select {
		case got := <-ch:
			if got.Type == "clipboard.image" {
				return got
			}
		case <-timer.C:
			t.Fatalf("%s did not receive clipboard image event", name)
		}
	}
}

func readEncryptedEvent(t *testing.T, name string, ch chan event) event {
	t.Helper()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for {
		select {
		case got := <-ch:
			if got.Type == "clipboard.encrypted" {
				return got
			}
		case <-timer.C:
			t.Fatalf("%s did not receive encrypted clipboard event", name)
		}
	}
}

func readDevicesEvent(t *testing.T, name string, ch chan event) event {
	t.Helper()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for {
		select {
		case got := <-ch:
			if got.Type == "devices" {
				return got
			}
		case <-timer.C:
			t.Fatalf("%s did not receive devices event", name)
		}
	}
}

func readSessionNameEvent(t *testing.T, name string, ch chan event, want string) event {
	t.Helper()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for {
		select {
		case got := <-ch:
			if got.Type == "session" && got.Name == want {
				return got
			}
		case <-timer.C:
			t.Fatalf("%s did not receive session name %q", name, want)
		}
	}
}

func readLinkEvent(t *testing.T, name string, ch chan event) event {
	t.Helper()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for {
		select {
		case got := <-ch:
			if got.Type == "link" {
				return got
			}
		case <-timer.C:
			t.Fatalf("%s did not receive link event", name)
		}
	}
}

func readClipboardWS(t *testing.T, conn *websocket.Conn) event {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	for {
		_, b, err := conn.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var msg event
		if err := json.Unmarshal(b, &msg); err != nil {
			t.Fatal(err)
		}
		if msg.Type == "clipboard.text" {
			return msg
		}
	}
}

func joinMobileWithApprovalHTTP(t *testing.T, baseURL, sid string, approverCookie *http.Cookie, events *websocket.Conn, device string) *http.Cookie {
	t.Helper()
	joinReq, err := http.NewRequest(http.MethodPost, baseURL+"/api/session/"+sid+"/join", strings.NewReader(`{"device":`+strconv.Quote(device)+`}`))
	if err != nil {
		t.Fatal(err)
	}
	joinReq.Header.Set("Content-Type", "application/json")
	joinRes, err := http.DefaultClient.Do(joinReq)
	if err != nil {
		t.Fatal(err)
	}
	defer joinRes.Body.Close()
	if joinRes.StatusCode != http.StatusAccepted {
		t.Fatalf("join status %d", joinRes.StatusCode)
	}
	pendingCookie := findCookie(t, joinRes.Cookies(), pendingJoinCookieName(sid))
	request := readJoinRequestWS(t, events)

	approveReq, err := http.NewRequest(http.MethodPost, baseURL+"/api/session/"+sid+"/joins/"+request.ID+"/approve", nil)
	if err != nil {
		t.Fatal(err)
	}
	approveReq.AddCookie(approverCookie)
	approveRes, err := http.DefaultClient.Do(approveReq)
	if err != nil {
		t.Fatal(err)
	}
	defer approveRes.Body.Close()
	if approveRes.StatusCode != http.StatusOK {
		t.Fatalf("approve status %d", approveRes.StatusCode)
	}

	claimReq, err := http.NewRequest(http.MethodPost, baseURL+"/api/session/"+sid+"/join", strings.NewReader(`{"device":`+strconv.Quote(device)+`}`))
	if err != nil {
		t.Fatal(err)
	}
	claimReq.Header.Set("Content-Type", "application/json")
	claimReq.AddCookie(pendingCookie)
	claimRes, err := http.DefaultClient.Do(claimReq)
	if err != nil {
		t.Fatal(err)
	}
	defer claimRes.Body.Close()
	if claimRes.StatusCode != http.StatusOK {
		t.Fatalf("claim status %d", claimRes.StatusCode)
	}
	return findCookie(t, claimRes.Cookies(), mobileCookieName(sid))
}

func readJoinRequestWS(t *testing.T, conn *websocket.Conn) joinRequestView {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	for {
		_, b, err := conn.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var msg event
		if err := json.Unmarshal(b, &msg); err != nil {
			t.Fatal(err)
		}
		if len(msg.JoinRequests) > 0 {
			return msg.JoinRequests[0]
		}
	}
}

func findCookie(t *testing.T, cookies []*http.Cookie, name string) *http.Cookie {
	t.Helper()
	for _, c := range cookies {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("missing cookie %s", name)
	return nil
}
