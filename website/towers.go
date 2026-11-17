package main

import (
	"bytes"
	"crypto/subtle"
	"embed"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed static/towers/*
var towersAssets embed.FS

var towersResultsPage = template.Must(template.ParseFS(towersAssets, "static/towers/results.html"))

const (
	defaultTowersFrames = "https://qualtrics.com https://*.qualtrics.com https://qualtrics.eu https://*.qualtrics.eu"
	towerMoveLimit      = 10000
	towerBatchLimit     = 256
)

var (
	towerCodePattern = regexp.MustCompile("^TH-[0-9a-f]{32}$")
	towerKeyPattern  = regexp.MustCompile("^[0-9a-f]{32}$")
	towerFrameOrigin = regexp.MustCompile(`^https://(?:\*\.)?[a-zA-Z0-9.-]+(?::[0-9]+)?$`)
	errTowerMove     = errors.New("invalid move history")
	errTowerConflict = errors.New("attempt code or progress conflict")
)

type towerMove struct {
	From      int `json:"from"`
	To        int `json:"to"`
	ElapsedMs int `json:"elapsedMs"`
}

type towerRecordedMove struct {
	towerMove
	Valid bool `json:"valid"`
}

type towerProgress struct {
	Code     string      `json:"code"`
	WriteKey string      `json:"writeKey,omitempty"`
	Offset   int         `json:"offset"`
	Moves    []towerMove `json:"moves"`
}

type towerEntry struct {
	towerProgress
	At time.Time `json:"at"`
}

type towerAttempt struct {
	Code            string
	StartedAt       time.Time
	LastSavedAt     time.Time
	CompletedAt     time.Time
	ElapsedMs       int
	SuccessfulMoves int
	InvalidMoves    int
	Moves           []towerRecordedMove
	writeKey        string
	board           [6]int
}

type towersStore struct {
	mu       sync.Mutex
	path     string
	frames   string
	attempts map[string]towerAttempt
}

func newTowersStore() *towersStore {
	path := strings.TrimSpace(os.Getenv("TOWERS_RESULTS_PATH"))
	if path == "" {
		switch {
		case os.Getenv("RAILWAY_VOLUME_MOUNT_PATH") != "":
			path = filepath.Join(os.Getenv("RAILWAY_VOLUME_MOUNT_PATH"), "towers-results.jsonl")
		case os.Getenv("RAILWAY_ENVIRONMENT_ID") == "":
			path = "towers-results.jsonl"
			if analyticsPath := os.Getenv("ANALYTICS_PATH"); filepath.IsAbs(analyticsPath) {
				path = filepath.Join(filepath.Dir(analyticsPath), path)
			}
		}
	}
	frames := defaultTowersFrames
	if origins := strings.Fields(os.Getenv("TOWERS_FRAME_ANCESTORS")); len(origins) > 0 {
		frames = "'none'"
		valid := true
		for _, origin := range origins {
			if u, err := url.Parse(origin); err != nil || u.Hostname() == "" || !towerFrameOrigin.MatchString(origin) {
				valid = false
				break
			}
		}
		if valid {
			frames = strings.Join(origins, " ")
		} else {
			log.Print("towers: invalid TOWERS_FRAME_ANCESTORS; embedding disabled")
		}
	}
	return &towersStore{path: path, frames: frames}
}

func (a *app) handleTowers(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'none'")
	switch {
	case r.Method == http.MethodGet && (r.URL.Path == "/towers" || r.URL.Path == "/towers/"):
		w.Header().Set("Content-Security-Policy", "default-src 'none'; base-uri 'none'; frame-ancestors "+a.towers.frames+"; form-action 'none'; script-src 'self'; style-src 'self'; connect-src 'self'")
		w.Header().Del("Cross-Origin-Resource-Policy")
		writeTowersAsset(w, "index.html", "text/html; charset=utf-8")
	case r.Method == http.MethodGet && r.URL.Path == "/towers/game.js":
		writeTowersAsset(w, "game.js", "application/javascript; charset=utf-8")
	case r.Method == http.MethodGet && r.URL.Path == "/towers/styles.css":
		writeTowersAsset(w, "styles.css", "text/css; charset=utf-8")
	case (r.Method == http.MethodGet || r.Method == http.MethodPost) && r.URL.Path == "/towers/results":
		a.handleTowersResults(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/towers/results.csv":
		a.handleTowersCSV(w)
	case r.Method == http.MethodPost && r.URL.Path == "/api/towers/attempts":
		a.handleTowerProgress(w, r)
	default:
		http.NotFound(w, r)
	}
}

func writeTowersAsset(w http.ResponseWriter, name, contentType string) {
	body, err := towersAssets.ReadFile("static/towers/" + name)
	if err != nil {
		http.Error(w, "Could not load page.", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", contentType)
	_, _ = w.Write(body)
}

func (a *app) handleTowerProgress(w http.ResponseWriter, r *http.Request) {
	if mediaType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mediaType != "application/json" {
		writeError(w, http.StatusUnsupportedMediaType, "expected application/json")
		return
	}
	var p towerProgress
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16*1024))
	d.DisallowUnknownFields()
	if d.Decode(&p) != nil || d.Decode(new(any)) != io.EOF || !towerCodePattern.MatchString(p.Code) ||
		!towerKeyPattern.MatchString(p.WriteKey) || p.Offset < 0 || p.Offset > towerMoveLimit ||
		len(p.Moves) > towerBatchLimit || p.Offset+len(p.Moves) > towerMoveLimit {
		writeError(w, http.StatusBadRequest, "invalid attempt data")
		return
	}
	switch err := a.towers.save(p); {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, errTowerConflict):
		writeError(w, http.StatusConflict, errTowerConflict.Error())
	case errors.Is(err, errTowerMove):
		writeError(w, http.StatusBadRequest, errTowerMove.Error())
	default:
		log.Printf("towers: save: %v", err)
		writeError(w, http.StatusServiceUnavailable, "could not save attempt")
	}
}

func (s *towersStore) save(p towerProgress) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.load(); err != nil {
		return err
	}
	previous, exists := s.attempts[p.Code]
	if exists && subtle.ConstantTimeCompare([]byte(previous.writeKey), []byte(p.WriteKey)) != 1 {
		return errTowerConflict
	}
	entry := towerEntry{towerProgress: p, At: time.Now().UTC()}
	next, err := applyTowerEntry(previous, entry)
	if err != nil {
		return err
	}
	if exists {
		saved := len(previous.Moves)
		if p.Offset+len(p.Moves) <= saved {
			return nil
		}
		entry.Moves = p.Moves[saved-p.Offset:]
		entry.Offset = saved
		entry.WriteKey = ""
	}
	if err := s.append(entry); err != nil {
		return err
	}
	s.attempts[p.Code] = next
	return nil
}

func applyTowerEntry(attempt towerAttempt, entry towerEntry) (towerAttempt, error) {
	if entry.Offset < 0 || entry.Offset > len(attempt.Moves) {
		return attempt, errTowerConflict
	}
	if attempt.Code == "" {
		attempt.Code = entry.Code
		attempt.writeKey = entry.WriteKey
		attempt.StartedAt = entry.At
		attempt.board = [6]int{1, 1, 1, 1, 1, 1}
	}
	for i, move := range entry.Moves {
		if index := entry.Offset + i; index < len(attempt.Moves) {
			if attempt.Moves[index].towerMove != move {
				return attempt, errTowerConflict
			}
			continue
		}
		if !attempt.CompletedAt.IsZero() || move.From < 1 || move.From > 3 || move.To < 1 || move.To > 3 ||
			move.From == move.To || move.ElapsedMs < attempt.ElapsedMs || move.ElapsedMs > 2147483647 {
			return attempt, errTowerMove
		}
		from, to := len(attempt.board), len(attempt.board)
		for disc, peg := range attempt.board {
			if peg == move.From && from == len(attempt.board) {
				from = disc
			}
			if peg == move.To && to == len(attempt.board) {
				to = disc
			}
		}
		if from == len(attempt.board) {
			return attempt, errTowerMove
		}
		valid := from < to
		if valid {
			attempt.board[from] = move.To
			attempt.SuccessfulMoves++
		} else {
			attempt.InvalidMoves++
		}
		attempt.Moves = append(attempt.Moves, towerRecordedMove{towerMove: move, Valid: valid})
		attempt.ElapsedMs = move.ElapsedMs
		if attempt.board == [6]int{3, 3, 3, 3, 3, 3} {
			attempt.CompletedAt = attempt.StartedAt.Add(time.Duration(move.ElapsedMs) * time.Millisecond)
		}
	}
	attempt.LastSavedAt = entry.At
	return attempt, nil
}

// ponytail: replay the small study journal once per process. If it outgrows
// memory or needs multiple replicas, migrate to a shared indexed database.
func (s *towersStore) load() error {
	if s.attempts != nil {
		return nil
	}
	if s.path == "" {
		return errors.New("attach a Railway volume or configure TOWERS_RESULTS_PATH")
	}
	data, err := os.ReadFile(s.path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	attempts := make(map[string]towerAttempt)
	// An interrupted append can leave a partial final line. Only replay complete lines.
	end := bytes.LastIndexByte(data, '\n') + 1
	for i, line := range bytes.Split(data[:end], []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		var entry towerEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			return fmt.Errorf("invalid journal line %d: %w", i+1, err)
		}
		previous, exists := attempts[entry.Code]
		if !towerCodePattern.MatchString(entry.Code) || entry.At.IsZero() || entry.Offset != len(previous.Moves) ||
			(!exists && !towerKeyPattern.MatchString(entry.WriteKey)) || (exists && entry.WriteKey != "") {
			return fmt.Errorf("invalid journal line %d", i+1)
		}
		next, err := applyTowerEntry(previous, entry)
		if err != nil {
			return fmt.Errorf("invalid journal line %d: %w", i+1, err)
		}
		attempts[entry.Code] = next
	}
	if end < len(data) {
		if err := os.Truncate(s.path, int64(end)); err != nil {
			return err
		}
		log.Print("towers: discarded interrupted final journal line")
	}
	s.attempts = attempts
	return nil
}

func (s *towersStore) append(entry towerEntry) error {
	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(s.path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if _, err = f.Write(append(data, '\n')); err == nil {
		err = f.Sync()
	}
	if err != nil {
		s.attempts = nil
		return errors.Join(err, f.Truncate(info.Size()), f.Sync())
	}
	return nil
}

func (a *app) handleTowersResults(w http.ResponseWriter, r *http.Request) {
	page := struct {
		Code    string
		Message string
		Attempt *towerAttempt
	}{}
	status := http.StatusOK
	if r.Method == http.MethodPost {
		r.Body = http.MaxBytesReader(w, r.Body, 512)
		if r.ParseForm() != nil {
			http.Error(w, "Invalid lookup.", http.StatusBadRequest)
			return
		}
		code := strings.ToLower(strings.TrimSpace(r.PostForm.Get("code")))
		if strings.HasPrefix(code, "th-") {
			code = "TH-" + code[3:]
		}
		if !towerCodePattern.MatchString(code) {
			status, page.Message = http.StatusBadRequest, "Enter a valid attempt code."
		} else {
			page.Code = code
			a.towers.mu.Lock()
			err := a.towers.load()
			attempt, found := a.towers.attempts[code]
			a.towers.mu.Unlock()
			switch {
			case err != nil:
				log.Printf("towers: lookup: %v", err)
				status, page.Message = http.StatusServiceUnavailable, "Results are temporarily unavailable."
			case !found:
				status, page.Message = http.StatusNotFound, "No saved attempt matches that code."
			default:
				page.Attempt = &attempt
			}
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'; style-src 'self'")
	w.WriteHeader(status)
	if err := towersResultsPage.Execute(w, page); err != nil {
		log.Printf("towers: results template: %v", err)
	}
}

func (a *app) handleTowersCSV(w http.ResponseWriter) {
	a.towers.mu.Lock()
	err := a.towers.load()
	attempts := make([]towerAttempt, 0, len(a.towers.attempts))
	for _, attempt := range a.towers.attempts {
		attempts = append(attempts, attempt)
	}
	a.towers.mu.Unlock()
	if err != nil {
		log.Printf("towers: CSV export: %v", err)
		http.Error(w, "Results are temporarily unavailable.", http.StatusServiceUnavailable)
		return
	}
	sort.Slice(attempts, func(i, j int) bool {
		if attempts[i].StartedAt.Equal(attempts[j].StartedAt) {
			return attempts[i].Code < attempts[j].Code
		}
		return attempts[i].StartedAt.Before(attempts[j].StartedAt)
	})
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="towers-results.csv"`)
	out := csv.NewWriter(w)
	header := []string{"attempt_code", "status", "started_at_utc", "last_saved_at_utc", "completed_at_utc", "elapsed_ms", "successful_moves", "invalid_moves", "total_attempts", "moves_json"}
	if err := out.Write(header); err != nil {
		return
	}
	for _, attempt := range attempts {
		state, completedAt := "incomplete", ""
		if !attempt.CompletedAt.IsZero() {
			state, completedAt = "completed", attempt.CompletedAt.Format(time.RFC3339Nano)
		}
		moves, _ := json.Marshal(attempt.Moves)
		if attempt.Moves == nil {
			moves = []byte("[]")
		}
		if err := out.Write([]string{attempt.Code, state, attempt.StartedAt.Format(time.RFC3339Nano),
			attempt.LastSavedAt.Format(time.RFC3339Nano), completedAt, strconv.Itoa(attempt.ElapsedMs),
			strconv.Itoa(attempt.SuccessfulMoves), strconv.Itoa(attempt.InvalidMoves),
			strconv.Itoa(len(attempt.Moves)), string(moves)}); err != nil {
			return
		}
	}
	out.Flush()
	if err := out.Error(); err != nil {
		log.Printf("towers: writing CSV: %v", err)
	}
}
