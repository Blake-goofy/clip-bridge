package main

import (
	"encoding/csv"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTowerStudy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "attempts.jsonl")
	t.Setenv("TOWERS_RESULTS_PATH", path)
	t.Setenv("TOWERS_FRAME_ANCESTORS", "https://survey.example.test")
	a := newApp()
	request := func(method, path, contentType, body string, want int) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", contentType)
		rec := httptest.NewRecorder()
		a.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Fatalf("%s %s: got %d, want %d: %s", method, path, rec.Code, want, rec.Body)
		}
		return rec
	}
	game := request("GET", "/towers", "", "", 200)
	if !strings.Contains(game.Body.String(), "Copy code") || !strings.Contains(
		game.Header().Get("Content-Security-Policy"), "frame-ancestors https://survey.example.test;") {
		t.Fatal("game missing its copy control or custom frame policy")
	}
	request("GET", "/towers/game.js", "", "", 200)
	request("GET", "/towers/styles.css", "", "", 200)
	for _, route := range []string{"/", "/towers/results", "/towers/results.csv"} {
		rec := request("GET", route, "", "", 200)
		if !strings.Contains(rec.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'") {
			t.Fatalf("%s is missing anti-framing policy", route)
		}
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("viewing pages should not save attempts: %v", err)
	}

	code := "TH-" + strings.Repeat("a", 32)
	secondCode := "TH-" + strings.Repeat("b", 32)
	key := strings.Repeat("c", 32)
	submit := func(p towerProgress, want int) {
		t.Helper()
		raw, err := json.Marshal(p)
		if err != nil {
			t.Fatal(err)
		}
		request(http.MethodPost, "/api/towers/attempts", "application/json", string(raw), want)
	}
	lookup := func(code string, want int) string {
		t.Helper()
		form := url.Values{"code": {code}}.Encode()
		return request(http.MethodPost, "/towers/results", "application/x-www-form-urlencoded", form, want).Body.String()
	}
	lookup(code, 404)
	lookup("<invalid>", 400)
	submit(towerProgress{Code: code, WriteKey: key}, 204)
	if body := lookup(code, 200); !strings.Contains(body, "Incomplete") || !strings.Contains(body, "Total attempts</dt><dd>0") {
		t.Fatal("starting without a move should persist an incomplete attempt")
	}
	partial := towerProgress{
		Code: code, WriteKey: key,
		Moves: []towerMove{{From: 1, To: 2, ElapsedMs: 100}, {From: 1, To: 2, ElapsedMs: 200}},
	}
	submit(partial, 204)
	submit(partial, 204)
	submit(towerProgress{Code: code, WriteKey: strings.Repeat("d", 32)}, 409)
	submit(towerProgress{Code: code, WriteKey: key, Offset: 3}, 409)
	submit(towerProgress{Code: code, WriteKey: key, Offset: 2, Moves: []towerMove{{From: 3, To: 2, ElapsedMs: 300}}}, 400)
	request(http.MethodPost, "/api/towers/attempts", "application/json", `{} {}`, 400)

	var solve func(n, from, spare, to int)
	var moves []towerMove
	solve = func(n, from, spare, to int) {
		if n == 0 {
			return
		}
		solve(n-1, from, to, spare)
		moves = append(moves, towerMove{From: from, To: to, ElapsedMs: (len(moves) + 1) * 100})
		solve(n-1, spare, from, to)
	}
	solve(6, 1, 2, 3)
	submit(towerProgress{Code: secondCode, WriteKey: key}, 204)
	submit(towerProgress{Code: secondCode, WriteKey: key, Moves: moves[:10]}, 204)
	submit(towerProgress{Code: secondCode, WriteKey: key, Offset: 9, Moves: moves[9:]}, 204)
	submit(towerProgress{Code: secondCode, WriteKey: key, Offset: 63, Moves: []towerMove{{From: 3, To: 2, ElapsedMs: 6400}}}, 400)

	// Model an interrupted final write and a new process using the same volume.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteString(`{"code":`)
	if closeErr := f.Close(); err != nil || closeErr != nil {
		t.Fatalf("writing interrupted line: %v, closing: %v", err, closeErr)
	}
	a = newApp()
	if body := lookup(strings.ToUpper(code), 200); !strings.Contains(body, "Invalid moves</dt><dd>1") ||
		!strings.Contains(body, "Incomplete") || strings.Contains(body, key) {
		t.Fatal("incomplete progress or the private write key was rendered incorrectly")
	}
	if body := lookup(secondCode, 200); !strings.Contains(body, ">Completed<") || !strings.Contains(body, "6300 ms") {
		t.Fatal("completed progress was not recovered")
	}
	rec := request("GET", "/towers/results.csv", "", "", 200)
	rows, err := csv.NewReader(rec.Body).ReadAll()
	if err != nil || len(rows) != 3 {
		t.Fatalf("CSV should have a header and two attempts: %v, %v", rows, err)
	}
	if rows[0][9] != "moves_json" || rows[1][0] != code || rows[1][1] != "incomplete" ||
		rows[1][4] != "" || rows[1][5] != "200" || rows[1][6] != "1" || rows[1][7] != "1" || rows[1][8] != "2" ||
		rows[2][0] != secondCode || rows[2][1] != "completed" || rows[2][4] == "" || rows[2][8] != "63" {
		t.Fatalf("CSV snapshot mismatch: %v", rows)
	}
	var loggedMoves []towerRecordedMove
	if err := json.Unmarshal([]byte(rows[1][9]), &loggedMoves); err != nil || len(loggedMoves) != 2 ||
		!loggedMoves[0].Valid || loggedMoves[1].Valid || strings.Contains(strings.Join(rows[1], ","), key) {
		t.Fatalf("CSV move history or write-key handling is incorrect: %v, %v", loggedMoves, err)
	}
}
