package main

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"q2-pocket/internal/pocketcasts"
	"q2-pocket/internal/q2"
)

const (
	podA = "11111111-1111-1111-1111-111111111111"
	epA1 = "aaaaaaaa-0000-0000-0000-000000000001"
	epA2 = "aaaaaaaa-0000-0000-0000-000000000002"
	epA3 = "aaaaaaaa-0000-0000-0000-000000000003"
)

// pcFake is Pocket Casts: an account subscribed to one show, two of its episodes in Up Next, and
// the calls it was sent.
type pcFake struct {
	mu      sync.Mutex
	inprog  []map[string]any
	calls   []string
	bodies  []map[string]any
	upnext  []string
	removed []string
}

func (f *pcFake) saw(prefix string) []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []map[string]any
	for i, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			out = append(out, f.bodies[i])
		}
	}
	return out
}

func newPC(t *testing.T) (*pcFake, *httptest.Server) {
	audio, err := os.ReadFile(filepath.Join("..", "..", "internal", "mp3", "testdata", "content.mp3"))
	if err != nil {
		t.Fatal(err)
	}
	f := &pcFake{upnext: []string{epA2, epA1}}
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.calls = append(f.calls, r.URL.Path)
		f.bodies = append(f.bodies, body)
		f.mu.Unlock()
		ep := func(id, title string) map[string]any {
			return map[string]any{"uuid": id, "podcast": podA, "title": title, "url": srv.URL + "/audio/" + id + ".mp3",
				"published": "2026-11-0" + id[len(id)-1:] + "T00:00:00Z"}
		}
		reply := func(v any) { json.NewEncoder(w).Encode(v) }
		if strings.HasPrefix(r.URL.Path, "/audio/") {
			w.Write(audio)
			return
		}
		if !strings.HasPrefix(r.URL.Path, "/cache") && r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		switch r.URL.Path {
		case "/user/podcast/list":
			reply(map[string]any{"podcasts": []map[string]any{{"uuid": podA, "title": "Night Shift", "author": "Nora"}}})
		case "/up_next/list":
			var eps []map[string]any
			for _, id := range f.upnext {
				eps = append(eps, ep(id, "Episode "+id[len(id)-1:]))
			}
			reply(map[string]any{"episodes": eps})
		case "/up_next/remove":
			for _, u := range body["uuids"].([]any) {
				f.removed = append(f.removed, u.(string))
			}
			reply(map[string]any{})
		case "/user/in_progress":
			reply(map[string]any{"episodes": f.inprog})
		case "/cache/podcast/full/" + podA:
			eps := []map[string]any{}
			for i, id := range []string{epA3, epA2, epA1} {
				e := ep(id, "Episode "+id[len(id)-1:])
				e["duration"], e["file_type"] = 12, "audio/mpeg"
				_ = i
				eps = append(eps, e)
			}
			reply(map[string]any{"podcast": map[string]any{"uuid": podA, "title": "Night Shift", "author": "Nora", "episodes": eps}})
		default:
			reply(map[string]any{})
		}
	}))
	t.Cleanup(srv.Close)
	oldB, oldC := pocketcasts.Base, pocketcasts.Cache
	pocketcasts.Base, pocketcasts.Cache = srv.URL, srv.URL+"/cache"
	t.Cleanup(func() { pocketcasts.Base, pocketcasts.Cache = oldB, oldC })
	return f, srv
}

// startSigned is startApp signed in to the fake Pocket Casts, with a Q2 Pod that takes places and
// plays playlists.
func startSigned(t *testing.T) (*app, string, *pcFake) {
	f, _ := newPC(t)
	card := t.TempDir()
	socks, err := os.MkdirTemp("/tmp", "q2p")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(socks) })
	q2.CapsFile = filepath.Join(socks, "caps")
	os.WriteFile(q2.CapsFile, []byte("playlist progress pages library sleep places\n"), 0o644)
	dir := filepath.Join(card, ".pocket")
	os.MkdirAll(dir, 0o755)
	saveAccount(dir, pocketcasts.Token{Access: "tok", Email: "a@b.c"})
	// a sync each time the test asks, not on the loop's clock
	o := options{dir: dir, card: card, statusFile: filepath.Join(socks, "state"), places: filepath.Join(socks, "places"),
		play: filepath.Join(socks, "play"), browse: filepath.Join(socks, "b.sock"), control: filepath.Join(socks, "c.sock"),
		port: 18200, resume: filepath.Join(card, "ringnav-resume"), sleep: filepath.Join(socks, "sleep"), ready: func() bool { return true }}
	a, err := start(o)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { a.run(); close(done) }()
	t.Cleanup(func() { a.shutdown(); <-done })
	for end := time.Now().Add(3 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if c, err := net.Dial("unix", o.browse); err == nil {
			c.Close()
			break
		}
		if time.Now().After(end) {
			t.Fatal("no browse socket")
		}
	}
	return a, card, f
}

// ring writes Q2 Pod's resume ring: places by path, the first the most recent.
func ring(t *testing.T, card string, places ...any) {
	var b []byte
	for i := 0; i+1 < len(places); i += 2 {
		var slot [8]byte
		binary.LittleEndian.PutUint32(slot[:], placeKey(places[i].(string)))
		binary.LittleEndian.PutUint32(slot[4:], uint32(places[i+1].(int)))
		b = append(b, slot[:]...)
	}
	b = append(b, make([]byte, 64)...)
	if err := os.WriteFile(filepath.Join(card, "ringnav-resume"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	time.Sleep(15 * time.Millisecond) // a new mtime for places()
}

func TestSync(t *testing.T) {
	a, card, f := startSigned(t)
	ctx := context.Background()
	a.sy.once(ctx) // the show adopted, Up Next fetched, its episodes downloading
	si, ok := a.lib.ByPC(podA)
	if !ok || si.Hidden || si.Title != "Night Shift" {
		t.Fatalf("show %+v %v", si, ok)
	}
	for start := time.Now(); ; time.Sleep(20 * time.Millisecond) {
		a.sy.once(ctx)
		e1, _ := a.lib.PCEpisode(podA, epA1)
		e2, _ := a.lib.PCEpisode(podA, epA2)
		if e1.OnCard && e2.OnCard {
			break
		}
		if time.Since(start) > 15*time.Second {
			t.Fatalf("Up Next not downloaded: %+v", a.lib.DL.Jobs())
		}
	}
	e1, _ := a.lib.PCEpisode(podA, epA1)
	e2, _ := a.lib.PCEpisode(podA, epA2)
	if e3, _ := a.lib.PCEpisode(podA, epA3); e3.OnCard || e3.Job != nil {
		t.Error("an episode not in Up Next downloaded")
	}

	// Played on the Q2: its place sent, and it becomes Up Next's current, and the history's
	ring(t, card, e2.Path, 7)
	a.sy.once(ctx)
	sent := f.saw("/sync/update_episode")
	if len(sent) == 0 || sent[len(sent)-1]["uuid"] != epA2 || sent[len(sent)-1]["position"] != 7.0 {
		t.Fatalf("place sent: %v", sent)
	}
	if now := f.saw("/up_next/play_now"); len(now) != 1 || now[0]["episode"].(map[string]any)["uuid"] != epA2 {
		t.Errorf("play_now: %v", now)
	}
	if h := f.saw("/history/do"); len(h) != 1 || h[0]["episode"] != epA2 {
		t.Errorf("history: %v", h)
	}

	// Moved on another device: put in the ring
	f.mu.Lock()
	f.inprog = []map[string]any{{"uuid": epA1, "podcast": podA, "playedUpTo": 300, "playingStatus": 2}}
	f.mu.Unlock()
	a.sy.once(ctx)
	if b, _ := os.ReadFile(a.opts.places); !strings.Contains(string(b), "300\t"+e1.Path) {
		t.Errorf("places for Q2 Pod: %q", b)
	}

	// Q2 Pod takes it: its newest place, which isn't the Q2 playing it
	ring(t, card, e1.Path, 300, e2.Path, 7)
	a.sy.once(ctx)
	if now := f.saw("/up_next/play_now"); len(now) != 1 {
		t.Errorf("a place put in the ring taken for playing: %v", now)
	}

	// Finished on the Q2 (its place gone near the end): played, and out of Up Next
	ring(t, card, e1.Path, 300)
	a.sy.once(ctx)
	if p := f.saw("/sync/update_episode"); p[len(p)-1]["status"] != 3.0 || p[len(p)-1]["uuid"] != epA2 {
		t.Errorf("played: %v", p[len(p)-1])
	}
	f.mu.Lock()
	removed := strings.Join(f.removed, ",")
	f.mu.Unlock()
	if removed != epA2 {
		t.Errorf("removed from Up Next: %q", removed)
	}

	// the pages
	up := spot(t, a, "upnext")
	if len(up.rows) != 2 || up.rows[0][0] != "action" || up.rows[0][1] != "upplay" || up.rows[1][4] != "1" || up.rows[0][5] != "unq:"+podA+":"+epA2 {
		t.Errorf("Up Next page: %v", up.rows)
	}
	// pressed: Up Next from there, an .m3u on the card for Q2 Pod to play in order
	if r := spot(t, a, "upplay", "0"); r.message != "Playing Up Next" {
		t.Errorf("upplay: %+v", r)
	}
	m3u := filepath.Join(a.opts.dir, "upnext.m3u")
	if b, _ := os.ReadFile(m3u); string(b) != "#EXTM3U\n"+e2.Path+"\n"+e1.Path+"\n" {
		t.Errorf("the playlist: %q", b)
	}
	if b, _ := os.ReadFile(a.opts.play); !strings.HasSuffix(string(b), " "+m3u+"\n") {
		t.Errorf(".play: %q", b)
	}
	if r := spot(t, a, "upq:"+podA+":"+epA1); r.message != "It's in Up Next already" {
		t.Errorf("add again: %+v", r)
	}
	if r := spot(t, a, "unq:"+podA+":"+epA1); r.message != "Hold again to take it out of Up Next" {
		t.Errorf("remove: %+v", r)
	}
	menu := spot(t, a, "menu")
	if titles(menu)[0] != "Up Next" || !strings.Contains(strings.Join(titles(menu), "|"), "Sync Now") {
		t.Errorf("menu %v", titles(menu))
	}
}

func TestAdfreePage(t *testing.T) {
	a, _ := startApp(t)
	r := spot(t, a, "adfree")
	if row := findRow(t, r, "Remove Ads: On"); row[1] != "set" || row[4] != "ads" {
		t.Fatalf("%v", row)
	}
	if r := spot(t, a, "set", "ads"); r.message != "Ads: Off" || a.settings().Ads {
		t.Errorf("%+v", r)
	}
	b, _ := os.ReadFile(filepath.Join(a.opts.dir, "config.json"))
	if !strings.Contains(string(b), `"remove_ads": false`) {
		t.Errorf("saved %s", b)
	}
	findRow(t, spot(t, a, "adfree"), "Remove Ads: Off")
}

// The phone page's Pocket Casts calls: sign in and out, settings (keys never read back), Up Next.
func TestPhoneAccount(t *testing.T) {
	a, _, _ := startSigned(t)
	web := httptest.NewServer(newWeb(a))
	defer web.Close()
	c := web.Client()
	cookie := "q2pocket=" + a.settings().Key
	call := func(method, path, body string) (int, map[string]any) {
		req, _ := http.NewRequest(method, web.URL+"/api/"+path, strings.NewReader(body))
		req.Header.Set("Cookie", cookie)
		req.Header.Set("X-Q2", "1")
		resp, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]any
		json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}
	if code, out := call("GET", "pc/account", ""); code != 200 || out["signedIn"] != true || out["email"] != "a@b.c" {
		t.Fatalf("%d %v", code, out)
	}
	if code, _ := call("POST", "pc/settings", `{"ads":false,"upNext":3,"youTubeKey":" K1 "}`); code != 200 {
		t.Fatal(code)
	}
	_, s := call("GET", "pc/settings", "")
	if s["ads"] != false || s["upNext"] != 3.0 || s["youtube"] != true || s["youTubeKey"] != nil {
		t.Errorf("settings %v", s)
	}
	if a.settings().YouTubeKey != "K1" {
		t.Errorf("key %q", a.settings().YouTubeKey)
	}
	a.sy.once(context.Background())
	if _, up := call("GET", "pc/upnext", ""); len(up["episodes"].([]any)) != 2 {
		t.Errorf("up next %v", up)
	}
	if code, _ := call("POST", "pc/logout", "{}"); code != 200 || a.pc.SignedIn() {
		t.Fatal("still signed in")
	}
	if _, err := os.Stat(filepath.Join(a.opts.dir, "account.json")); err == nil {
		t.Error("the session stayed on the card")
	}
	if code, out := call("POST", "pc/login", `{"email":"a@b.c","password":""}`); code != 400 {
		t.Errorf("empty password: %d %v", code, out)
	}
}

func TestNotesText(t *testing.T) {
	got := notesText(`<p>Guest:  <b>Ana</b> &amp; friends.</p><p>Links:<br/><a href="x">site</a></p>`)
	if want := "Guest: Ana & friends.\n\nLinks:\nsite"; got != want {
		t.Errorf("%q, want %q", got, want)
	}
}
