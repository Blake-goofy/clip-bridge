package main

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"sort"
	"strconv"
	"sync"
	"time"
)

type hub struct {
	mu       sync.Mutex
	now      func() time.Time
	sessions map[string]*session
	aliases  map[string]string
}

type session struct {
	id         string
	joinID     string
	name       string
	pcHash     [32]byte
	createdAt  time.Time
	lastSeen   time.Time
	pcSend     chan event
	nextDevice int
	devices    map[string]*device
	pending    map[string]*pendingJoin
}

type device struct {
	id          string
	tokenHash   [32]byte
	name        string
	connectedAt time.Time
	lastSeen    time.Time
	send        chan event
}

type pendingJoin struct {
	id          string
	tokenHash   [32]byte
	name        string
	requestedAt time.Time
	lastSeen    time.Time
	approved    bool
}

type deviceView struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Connected   bool   `json:"connected"`
	Active      bool   `json:"active"`
	ConnectedAt string `json:"connectedAt"`
}

type joinRequestView struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	RequestedAt string `json:"requestedAt"`
}

func newHub() *hub {
	return &hub{
		now:      time.Now,
		sessions: make(map[string]*session),
		aliases:  make(map[string]string),
	}
}

type joinResult struct {
	mobileToken      string
	pendingToken     string
	setMobileCookie  bool
	setPendingCookie bool
	pending          bool
}

func (h *hub) createSession() (string, string, error) {
	pcToken, err := randomToken(32)
	if err != nil {
		return "", "", err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.now()
	h.cleanupLocked(now)
	for i := 0; i < 5; i++ {
		sid, err := randomToken(9)
		if err != nil {
			return "", "", err
		}
		if _, ok := h.sessions[sid]; ok {
			continue
		}
		if _, ok := h.aliases[sid]; ok {
			continue
		}
		s := &session{
			id:         sid,
			joinID:     sid,
			pcHash:     tokenHash(pcToken),
			createdAt:  now,
			lastSeen:   now,
			nextDevice: 2,
			devices:    make(map[string]*device),
			pending:    make(map[string]*pendingJoin),
		}
		deviceID, err := randomToken(9)
		if err != nil {
			return "", "", err
		}
		s.devices[deviceID] = &device{
			id:          deviceID,
			tokenHash:   tokenHash(pcToken),
			name:        "Device1",
			connectedAt: now,
			lastSeen:    now,
		}
		h.sessions[sid] = s
		h.aliases[sid] = sid
		return sid, pcToken, nil
	}
	return "", "", errors.New("could not allocate session id")
}

func (h *hub) exists(sid string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.now()
	h.cleanupLocked(now)
	_, ok := h.sessionLocked(sid)
	return ok
}

func (h *hub) joinLinkExists(sid string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.now()
	h.cleanupLocked(now)
	s, ok := h.sessionLocked(sid)
	return ok && sid == s.joinID
}

func (h *hub) verifyPC(sid, token string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.now()
	h.cleanupLocked(now)
	s, ok := h.sessionLocked(sid)
	if !ok {
		return errNotFound
	}
	if !sameToken(s.pcHash, token) {
		return errUnauthorized
	}
	if findDeviceByTokenLocked(s, token) == nil {
		return errUnauthorized
	}
	return nil
}

func (h *hub) verifyMobile(sid, token string) error {
	return h.verifyDevice(sid, token)
}

func (h *hub) verifyDevice(sid, token string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.now()
	h.cleanupLocked(now)
	s, ok := h.sessionLocked(sid)
	if !ok {
		return errNotFound
	}
	if findDeviceByTokenLocked(s, token) == nil {
		return errUnauthorized
	}
	return nil
}

func (h *hub) connectPC(sid, token string) (chan event, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.now()
	h.cleanupLocked(now)
	s, ok := h.sessionLocked(sid)
	if !ok {
		return nil, errNotFound
	}
	if !sameToken(s.pcHash, token) {
		return nil, errUnauthorized
	}
	if s.pcSend != nil {
		return nil, errConflict
	}
	d := findDeviceByTokenLocked(s, token)
	if d == nil {
		return nil, errUnauthorized
	}
	ch := make(chan event, 16)
	s.pcSend = ch
	d.send = ch
	d.lastSeen = now
	s.lastSeen = now
	_ = sendLocked(ch, sessionEventLocked(s))
	_ = broadcastDevicesLocked(s)
	return ch, nil
}

func (h *hub) disconnectPC(sid string, ch chan event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	s, ok := h.sessionLocked(sid)
	if !ok || s.pcSend != ch {
		return
	}
	s.pcSend = nil
	if d := findDeviceByChannelLocked(s, ch); d != nil {
		d.send = nil
		d.lastSeen = h.now()
	}
	s.lastSeen = h.now()
	close(ch)
	_ = broadcastDevicesLocked(s)
}

func (h *hub) requestMobileJoin(sid, existingToken, pendingToken, deviceName string) (joinResult, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.now()
	h.cleanupLocked(now)
	s, ok := h.sessionLocked(sid)
	if !ok {
		return joinResult{}, errNotFound
	}
	if sid != s.joinID {
		return joinResult{}, errNotFound
	}
	if d := findDeviceByTokenLocked(s, existingToken); d != nil {
		d.lastSeen = now
		if deviceName != "" {
			d.name = deviceName
		}
		s.lastSeen = now
		_ = broadcastDevicesLocked(s)
		return joinResult{}, nil
	}
	if p := findPendingJoinByTokenLocked(s, pendingToken); p != nil {
		p.lastSeen = now
		if deviceName != "" {
			p.name = deviceName
		}
		s.lastSeen = now
		if !p.approved {
			return joinResult{pending: true}, nil
		}
		token, err := randomToken(32)
		if err != nil {
			return joinResult{}, err
		}
		if _, err := addMobileDeviceLocked(s, token, p.name, now); err != nil {
			return joinResult{}, err
		}
		delete(s.pending, p.id)
		_ = broadcastDevicesLocked(s)
		return joinResult{mobileToken: token, setMobileCookie: true}, nil
	}
	if !hasConnectedDeviceLocked(s) {
		return joinResult{}, errPCGone
	}
	pendingID, err := randomToken(9)
	if err != nil {
		return joinResult{}, err
	}
	pendingSecret, err := randomToken(32)
	if err != nil {
		return joinResult{}, err
	}
	if deviceName == "" {
		deviceName = nextDeviceNameLocked(s)
	}
	s.pending[pendingID] = &pendingJoin{
		id:          pendingID,
		tokenHash:   tokenHash(pendingSecret),
		name:        deviceName,
		requestedAt: now,
		lastSeen:    now,
	}
	s.lastSeen = now
	_ = broadcastDevicesLocked(s)
	return joinResult{pendingToken: pendingSecret, setPendingCookie: true, pending: true}, nil
}

func (h *hub) joinMobile(sid, existingToken, deviceName string) (mobileToken string, setCookie bool, err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.now()
	h.cleanupLocked(now)
	s, ok := h.sessionLocked(sid)
	if !ok {
		return "", false, errNotFound
	}
	if sid != s.joinID {
		return "", false, errNotFound
	}
	if d := findDeviceByTokenLocked(s, existingToken); d != nil {
		d.lastSeen = now
		if deviceName != "" {
			d.name = deviceName
		}
		s.lastSeen = now
		_ = broadcastDevicesLocked(s)
		return "", false, nil
	}
	token, err := randomToken(32)
	if err != nil {
		return "", false, err
	}
	if _, err := addMobileDeviceLocked(s, token, deviceName, now); err != nil {
		return "", false, err
	}
	s.lastSeen = now
	_ = broadcastDevicesLocked(s)
	return token, true, nil
}

func (h *hub) approveJoin(sid, token, requestID string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.now()
	h.cleanupLocked(now)
	s, ok := h.sessionLocked(sid)
	if !ok {
		return errNotFound
	}
	if findDeviceByTokenLocked(s, token) == nil {
		return errUnauthorized
	}
	p, ok := s.pending[requestID]
	if !ok || now.Sub(p.requestedAt) > pendingJoinTTL {
		return errNotFound
	}
	p.approved = true
	p.lastSeen = now
	s.lastSeen = now
	_ = broadcastDevicesLocked(s)
	return nil
}

func (h *hub) connectMobile(sid, token string) (chan event, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.now()
	h.cleanupLocked(now)
	s, ok := h.sessionLocked(sid)
	if !ok {
		return nil, errNotFound
	}
	d := findDeviceByTokenLocked(s, token)
	if d == nil {
		return nil, errUnauthorized
	}
	if d.send != nil {
		return nil, errConflict
	}
	ch := make(chan event, 16)
	d.send = ch
	d.lastSeen = now
	s.lastSeen = now
	_ = sendLocked(ch, sessionEventLocked(s))
	_ = broadcastDevicesLocked(s)
	return ch, nil
}

func (h *hub) disconnectMobile(sid string, ch chan event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	s, ok := h.sessionLocked(sid)
	if !ok {
		return
	}
	for _, d := range s.devices {
		if d.send == ch {
			d.send = nil
			d.lastSeen = h.now()
			close(ch)
			_ = broadcastDevicesLocked(s)
			return
		}
	}
}

func (h *hub) relayClipboardToPC(sid, mobileToken, text string) error {
	return h.relayClipboard(sid, mobileToken, text)
}

func (h *hub) relayClipboardToMobile(sid, pcToken, text string) error {
	return h.relayClipboard(sid, pcToken, text)
}

func (h *hub) relayClipboard(sid, token, text string) error {
	return h.relayClipboardEvent(sid, token, event{Type: "clipboard.text", Text: text})
}

func (h *hub) relayClipboardEvent(sid, token string, msg event) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.now()
	h.cleanupLocked(now)
	s, ok := h.sessionLocked(sid)
	if !ok {
		return errNotFound
	}
	d := findDeviceByTokenLocked(s, token)
	if d == nil {
		return errUnauthorized
	}
	d.lastSeen = now
	s.lastSeen = now
	return sendToOtherDevicesLocked(s, d, msg)
}

func (h *hub) renameSession(sid, token, name string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.now()
	h.cleanupLocked(now)
	s, ok := h.sessionLocked(sid)
	if !ok {
		return errNotFound
	}
	if findDeviceByTokenLocked(s, token) == nil {
		return errUnauthorized
	}
	s.name = name
	s.lastSeen = now
	_ = broadcastLocked(s, sessionEventLocked(s))
	return nil
}

func (h *hub) renameDevice(sid, token, deviceID, name string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.now()
	h.cleanupLocked(now)
	s, ok := h.sessionLocked(sid)
	if !ok {
		return errNotFound
	}
	if findDeviceByTokenLocked(s, token) == nil {
		return errUnauthorized
	}
	d, ok := s.devices[deviceID]
	if !ok {
		return errNotFound
	}
	d.name = name
	d.lastSeen = now
	s.lastSeen = now
	_ = broadcastDevicesLocked(s)
	return nil
}

func (h *hub) removeDevice(sid, token, deviceID string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.now()
	h.cleanupLocked(now)
	s, ok := h.sessionLocked(sid)
	if !ok {
		return errNotFound
	}
	requester := findDeviceByTokenLocked(s, token)
	if requester == nil {
		return errUnauthorized
	}
	d, ok := s.devices[deviceID]
	if !ok {
		return errNotFound
	}
	if d == requester {
		return errActiveDevice
	}
	delete(s.devices, deviceID)
	if d.send != nil {
		if s.pcSend == d.send {
			s.pcSend = nil
		}
		close(d.send)
		d.send = nil
	}
	s.lastSeen = now
	_ = broadcastDevicesLocked(s)
	return nil
}

func (h *hub) closeSession(sid, pcToken string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.now()
	h.cleanupLocked(now)
	s, ok := h.sessionLocked(sid)
	if !ok {
		return errNotFound
	}
	if !sameToken(s.pcHash, pcToken) {
		return errUnauthorized
	}
	delete(h.sessions, s.id)
	for alias, target := range h.aliases {
		if target == s.id {
			delete(h.aliases, alias)
		}
	}
	closed := make(map[chan event]bool)
	closeChannel := func(ch chan event) {
		if ch == nil || closed[ch] {
			return
		}
		close(ch)
		closed[ch] = true
	}
	closeChannel(s.pcSend)
	s.pcSend = nil
	for _, d := range s.devices {
		closeChannel(d.send)
		d.send = nil
	}
	return nil
}

func (h *hub) rotateJoinLink(sid, token string) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.now()
	h.cleanupLocked(now)
	s, ok := h.sessionLocked(sid)
	if !ok {
		return "", errNotFound
	}
	requester := findDeviceByTokenLocked(s, token)
	if requester == nil {
		return "", errUnauthorized
	}
	for i := 0; i < 5; i++ {
		newSID, err := randomToken(9)
		if err != nil {
			return "", err
		}
		if _, ok := h.sessions[newSID]; ok {
			continue
		}
		if _, ok := h.aliases[newSID]; ok {
			continue
		}
		for alias, target := range h.aliases {
			if target == s.id {
				delete(h.aliases, alias)
			}
		}
		h.aliases[newSID] = s.id
		s.joinID = newSID
		// ponytail: rotating a join link is a hard device reset; use per-device grants if partial revocation is needed later.
		s.pending = make(map[string]*pendingJoin)
		for id, d := range s.devices {
			if d == requester {
				continue
			}
			delete(s.devices, id)
			if d.send != nil {
				if s.pcSend == d.send {
					s.pcSend = nil
				}
				close(d.send)
				d.send = nil
			}
		}
		s.lastSeen = now
		_ = broadcastLocked(s, event{Type: "link", SID: newSID})
		_ = broadcastDevicesLocked(s)
		return newSID, nil
	}
	return "", errors.New("could not allocate session id")
}

func (h *hub) cleanupLocked(now time.Time) {
	// ponytail: in-memory sessions are single-process only; use Redis/pubsub if Railway needs multiple instances.
	for sid, s := range h.sessions {
		for id, p := range s.pending {
			if now.Sub(p.lastSeen) > pendingJoinTTL {
				delete(s.pending, id)
			}
		}
		unpairedExpired := len(s.devices) == 0 && now.Sub(s.createdAt) > unpairedTTL
		idleExpired := now.Sub(s.lastSeen) > idleTTL
		if unpairedExpired || idleExpired {
			delete(h.sessions, sid)
			for alias, target := range h.aliases {
				if target == sid {
					delete(h.aliases, alias)
				}
			}
			closed := make(map[chan event]bool)
			closeChannel := func(ch chan event) {
				if ch == nil || closed[ch] {
					return
				}
				close(ch)
				closed[ch] = true
			}
			closeChannel(s.pcSend)
			for _, d := range s.devices {
				closeChannel(d.send)
			}
		}
	}
}

func (h *hub) sessionLocked(sid string) (*session, bool) {
	if s, ok := h.sessions[sid]; ok {
		return s, true
	}
	canonical, ok := h.aliases[sid]
	if !ok {
		return nil, false
	}
	s, ok := h.sessions[canonical]
	return s, ok
}

func findDeviceByTokenLocked(s *session, token string) *device {
	if token == "" {
		return nil
	}
	for _, d := range s.devices {
		if sameToken(d.tokenHash, token) {
			return d
		}
	}
	return nil
}

func findDeviceByChannelLocked(s *session, ch chan event) *device {
	for _, d := range s.devices {
		if d.send == ch {
			return d
		}
	}
	return nil
}

func findPendingJoinByTokenLocked(s *session, token string) *pendingJoin {
	if token == "" {
		return nil
	}
	for _, p := range s.pending {
		if sameToken(p.tokenHash, token) {
			return p
		}
	}
	return nil
}

func hasConnectedDeviceLocked(s *session) bool {
	for _, d := range s.devices {
		if d.send != nil {
			return true
		}
	}
	return false
}

func addMobileDeviceLocked(s *session, token, deviceName string, now time.Time) (*device, error) {
	deviceID, err := randomToken(9)
	if err != nil {
		return nil, err
	}
	if deviceName == "" {
		deviceName = nextDeviceNameLocked(s)
	}
	d := &device{
		id:          deviceID,
		tokenHash:   tokenHash(token),
		name:        deviceName,
		connectedAt: now,
		lastSeen:    now,
	}
	s.devices[deviceID] = d
	return d, nil
}

func nextDeviceNameLocked(s *session) string {
	name := "Device" + strconv.Itoa(s.nextDevice)
	s.nextDevice++
	return name
}

func sessionEventLocked(s *session) event {
	return event{Type: "session", SID: s.id, Name: s.name}
}

func devicesEventLocked(s *session, active *device) event {
	return event{Type: "devices", Devices: deviceViewsLocked(s, active), JoinRequests: joinRequestViewsLocked(s)}
}

func deviceViewsLocked(s *session, active *device) []deviceView {
	devices := make([]deviceView, 0, len(s.devices))
	for _, d := range s.devices {
		devices = append(devices, deviceView{
			ID:          d.id,
			Name:        d.name,
			Connected:   d.send != nil,
			Active:      d == active,
			ConnectedAt: d.connectedAt.Format(time.RFC3339),
		})
	}
	sort.Slice(devices, func(i, j int) bool {
		return devices[i].ConnectedAt < devices[j].ConnectedAt
	})
	return devices
}

func joinRequestViewsLocked(s *session) []joinRequestView {
	requests := make([]joinRequestView, 0, len(s.pending))
	for _, p := range s.pending {
		if p.approved {
			continue
		}
		requests = append(requests, joinRequestView{
			ID:          p.id,
			Name:        p.name,
			RequestedAt: p.requestedAt.Format(time.RFC3339),
		})
	}
	sort.Slice(requests, func(i, j int) bool {
		return requests[i].RequestedAt < requests[j].RequestedAt
	})
	return requests
}

func broadcastDevicesLocked(s *session) error {
	sent := 0
	busy := 0
	for _, d := range s.devices {
		if d.send == nil {
			continue
		}
		if err := sendLocked(d.send, devicesEventLocked(s, d)); err != nil {
			busy++
			continue
		}
		sent++
	}
	if sent > 0 {
		return nil
	}
	if busy > 0 {
		return errBusy
	}
	return errPCGone
}

func broadcastLocked(s *session, msg event) error {
	sent := 0
	busy := 0
	for _, d := range s.devices {
		if d.send == nil {
			continue
		}
		if err := sendLocked(d.send, msg); err != nil {
			busy++
			continue
		}
		sent++
	}
	if sent > 0 {
		return nil
	}
	if busy > 0 {
		return errBusy
	}
	return errPCGone
}

func sendToOtherDevicesLocked(s *session, sender *device, msg event) error {
	sent := 0
	busy := 0
	for _, d := range s.devices {
		if d == sender || d.send == nil {
			continue
		}
		if err := sendLocked(d.send, msg); err != nil {
			busy++
			continue
		}
		sent++
	}
	if sent > 0 {
		return nil
	}
	if busy > 0 {
		return errBusy
	}
	return errPCGone
}

func sendLocked(ch chan event, msg event) error {
	select {
	case ch <- msg:
		return nil
	default:
		return errBusy
	}
}

func sendToPCLocked(s *session, msg event) error {
	if s.pcSend == nil {
		return errPCGone
	}
	return sendLocked(s.pcSend, msg)
}

func sendToMobilesLocked(s *session, msg event) error {
	return broadcastLocked(s, msg)
}

func randomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func tokenHash(token string) [32]byte {
	return sha256.Sum256([]byte(token))
}

func sameToken(hash [32]byte, token string) bool {
	got := tokenHash(token)
	return subtle.ConstantTimeCompare(hash[:], got[:]) == 1
}
