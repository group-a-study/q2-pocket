package pocketcasts

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fake is Pocket Casts as far as the client goes: the calls it saw, and answers by path.
type fake struct {
	t        *testing.T
	mu       sync.Mutex
	calls    []string
	bodies   map[string]map[string]any
	token    string // the access token it takes
	oldLogin bool   // only /user/login, as before login_pocket_casts
}

func newFake(t *testing.T) (*fake, *Client) {
	f := &fake{t: t, token: "tok1", bodies: map[string]map[string]any{}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	Base, Cache, Lists = srv.URL, srv.URL+"/cache", srv.URL+"/lists"
	return f, New(srv.Client(), "test", Token{})
}

func (f *fake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.calls = append(f.calls, r.Method+" "+r.URL.Path)
	var body map[string]any
	json.NewDecoder(r.Body).Decode(&body)
	f.bodies[r.URL.Path] = body
	f.mu.Unlock()
	authed := r.Header.Get("Authorization") == "Bearer "+f.token
	reply := func(v any) { json.NewEncoder(w).Encode(v) }
	switch {
	case r.URL.Path == "/user/login_pocket_casts" && f.oldLogin:
		http.NotFound(w, r)
	case r.URL.Path == "/user/login_pocket_casts" || r.URL.Path == "/user/login":
		if body["password"] != "right" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Path == "/user/login" {
			reply(map[string]any{"token": f.token, "uuid": "u1"})
			return
		}
		reply(map[string]any{"accessToken": f.token, "refreshToken": "ref1", "expiresIn": 3600, "uuid": "u1"})
	case r.URL.Path == "/user/token":
		if body["refresh_token"] != "ref1" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		reply(map[string]any{"accessToken": f.token, "expiresIn": 3600})
	case !strings.HasPrefix(r.URL.Path, "/cache") && !strings.HasPrefix(r.URL.Path, "/lists") && !authed:
		w.WriteHeader(http.StatusUnauthorized)
	case r.URL.Path == "/user/podcast/list":
		reply(map[string]any{"podcasts": []map[string]any{{"uuid": "p1", "title": "One", "author": "A"}}})
	case r.URL.Path == "/up_next/list":
		reply(map[string]any{"episodes": []map[string]any{
			{"uuid": "e1", "title": "First", "podcast": "p1", "url": "http://x/1.mp3", "published": "2026-11-01T00:00:00Z", "playedUpTo": "125"},
			{"title": "broken, no uuid"},
			{"uuid": "e2", "title": "Second", "podcast": "p2", "duration": 1800.0}}})
	case r.URL.Path == "/user/podcast/episodes":
		reply(map[string]any{"episodes": []map[string]any{{"uuid": "e1", "playedUpTo": 300, "playingStatus": 2}, {"uuid": "e9", "playingStatus": 3}}})
	case r.URL.Path == "/files":
		w.WriteHeader(http.StatusOK) // saving a file's place answers with nothing
	case r.URL.Path == "/cache/podcast/full/p1":
		http.Redirect(w, r, "/cache/v/p1_5.json", http.StatusFound)
	case r.URL.Path == "/cache/v/p1_5.json":
		var b bytes.Buffer
		gz := gzip.NewWriter(&b)
		json.NewEncoder(gz).Encode(map[string]any{"podcast": map[string]any{"uuid": "p1", "title": "One",
			"episodes": []map[string]any{{"uuid": "e1", "title": "First", "url": "http://x/1.mp3", "duration": 2249, "file_size": 1000, "published": "2026-11-01T00:00:00Z"}}}})
		gz.Close()
		w.Write(b.Bytes()) // gzipped, without a Content-Encoding: as the real cache sends it
	case r.URL.Path == "/lists/trending.json":
		reply(map[string]any{"podcasts": []map[string]any{{"uuid": "p3", "title": "Hot", "feed": "http://f"}}})
	default:
		reply(map[string]any{})
	}
}

func (f *fake) saw(call string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if c == call {
			return true
		}
	}
	return false
}

func TestLogin(t *testing.T) {
	f, c := newFake(t)
	ctx := context.Background()
	if _, err := c.Login(ctx, "a@b.c", "wrong"); !errors.Is(err, ErrWrong) {
		t.Fatal(err)
	}
	tok, err := c.Login(ctx, " a@b.c ", "right")
	if err != nil || tok.Access != "tok1" || tok.Refresh != "ref1" || tok.Email != "a@b.c" || tok.Expires.IsZero() {
		t.Fatalf("%+v %v", tok, err)
	}
	f.oldLogin = true
	if tok, err = c.Login(ctx, "a@b.c", "right"); err != nil || tok.Access != "tok1" || !f.saw("POST /user/login") {
		t.Fatalf("older sign-in: %+v %v", tok, err)
	}
}

// A session the server refuses is refreshed once, and the new one kept.
func TestRefresh(t *testing.T) {
	f, c := newFake(t)
	c.tok = Token{Access: "stale", Refresh: "ref1"}
	var saved Token
	c.Saved = func(t Token) { saved = t }
	pods, err := c.Podcasts(context.Background())
	if err != nil || len(pods) != 1 || saved.Access != "tok1" || saved.Refresh != "ref1" {
		t.Fatalf("%v %v %+v", pods, err, saved)
	}
	f.token = "tok2" // and now refused for good
	c.tok = Token{Access: "stale", Refresh: "bad"}
	if _, err := c.Podcasts(context.Background()); !errors.Is(err, ErrSignIn) {
		t.Fatal(err)
	}
}

func TestUpNext(t *testing.T) {
	_, c := newFake(t)
	c.tok = Token{Access: "tok1"}
	ctx := context.Background()
	eps, err := c.UpNext(ctx)
	if err != nil || len(eps) != 2 || eps[0].UUID != "e1" || eps[0].PlayedUpTo != 125 || eps[1].Duration != 1800 {
		t.Fatalf("%+v %v", eps, err)
	}
	// Up Next keeps what it is sent: an episode without a real id or a field isn't
	if err := c.UpNextAdd(ctx, "now", eps[0]); err == nil {
		t.Error("e1 isn't a Pocket Casts id, but went in")
	}
	e := Episode{UUID: "7b65ac2a-4b3a-45f2-9bc0-dd9eab66438c", Podcast: "a9a362c0-9404-013f-5cfc-02ffffe690b5", Title: "T", URL: "http://x", Published: "2026-11-06"}
	if err := c.UpNextAdd(ctx, "next", e); err != nil {
		t.Error(err)
	}
	e.URL = ""
	if err := c.UpNextAdd(ctx, "last", e); err == nil {
		t.Error("no address, but went in")
	}
}

func TestStatesAndFiles(t *testing.T) {
	f, c := newFake(t)
	c.tok = Token{Access: "tok1"}
	ctx := context.Background()
	st, err := c.States(ctx, "p1")
	if err != nil || st["e1"].PlayedUpTo != 300 || st["e9"].Status != Played {
		t.Fatalf("%+v %v", st, err)
	}
	if err := c.SetFilePlace(ctx, "f1", 90, InProgress); err != nil { // an empty answer is fine
		t.Fatal(err)
	}
	if err := c.SetPlace(ctx, "p1", "e1", 61); err != nil || f.bodies["/sync/update_episode"]["position"] != 61.0 {
		t.Fatal(err, f.bodies["/sync/update_episode"])
	}
}

func TestCache(t *testing.T) {
	_, c := newFake(t)
	ctx := context.Background()
	s, tag, same, err := c.Full(ctx, "p1", "")
	if err != nil || same || s.Title != "One" || len(s.Episodes) != 1 || s.Episodes[0].Duration != 2249 || !strings.HasSuffix(tag, "p1_5.json") {
		t.Fatalf("%+v %q %v %v", s, tag, same, err)
	}
	if _, _, same, err = c.Full(ctx, "p1", tag); !same || err != nil {
		t.Errorf("the same version again: %v %v", same, err)
	}
	pods, err := c.List(ctx, "trending")
	if err != nil || len(pods) != 1 || pods[0].Feed != "http://f" {
		t.Fatal(pods, err)
	}
}

func TestFlex(t *testing.T) {
	var e Episode
	json.Unmarshal([]byte(`{"duration":"1806","playedUpTo":12.7,"size":"","playingStatus":null}`), &e)
	if e.Duration != 1806 || e.PlayedUpTo != 12 || e.Size != 0 {
		t.Errorf("%+v", e)
	}
	_ = io.EOF
}
