package main

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"q2-pocket/internal/clean"
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
		case "/user/new_releases":
			reply(map[string]any{"episodes": []map[string]any{ep(epA3, "Episode 3")}})
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
	return startSignedWith(t, func() bool { return true })
}

// startSignedWith is startSigned with ready saying whether the Q2 is online (syncs by themselves,
// downloads).
func startSignedWith(t *testing.T, ready func() bool) (*app, string, *pcFake) {
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
		star: filepath.Join(socks, "star"), starred: filepath.Join(socks, "starred"), done: filepath.Join(socks, "ringnav-done"),
		port: 18200, resume: filepath.Join(card, "ringnav-resume"), sleep: filepath.Join(socks, "sleep"), ready: ready}
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

	// Played on the Q2: nothing sent (the user's, 2026-11-15: no two-way sync), not its place, not Up
	// Next's current, not the history; it's the Q2's playing episode
	ring(t, card, e2.Path, 7)
	a.sy.once(ctx)
	if sent := f.saw("/sync/update_episode"); len(sent) != 0 {
		t.Errorf("a place sent: %v", sent)
	}
	if now, h := f.saw("/up_next/play_now"), f.saw("/history/do"); len(now)+len(h) != 0 {
		t.Errorf("play_now %v, history %v", now, h)
	}
	if a.sy.playingUUID() != epA2 {
		t.Errorf("playing: %q", a.sy.playingUUID())
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
	if a.sy.playingUUID() != epA2 {
		t.Errorf("a place put in the ring taken for playing: %q", a.sy.playingUUID())
	}

	// Finished on the Q2 (its place gone near the end): out of the Q2's Up Next, the account's as it was,
	// nothing sent
	ring(t, card, e1.Path, 300)
	a.sy.once(ctx)
	f.mu.Lock()
	removed, served := strings.Join(f.removed, ","), strings.Join(f.upnext, ",")
	f.mu.Unlock()
	if p := f.saw("/sync/update_episode"); len(p) != 0 || removed != "" || served != epA2+","+epA1 {
		t.Errorf("sent %v, removed %q, the account's Up Next %q", p, removed, served)
	}
	if up, _ := a.sy.upNext(); len(up) != 1 || up[0].UUID != epA1 {
		t.Errorf("the Q2's Up Next: %v", up)
	}

	// Now Playing's star: the episode starred in Pocket Casts
	os.WriteFile(a.opts.star, []byte("1\t"+e1.Path+"\n"), 0o644)
	a.sy.star()
	for start := time.Now(); len(f.saw("/sync/update_episode_star")) == 0; time.Sleep(10 * time.Millisecond) {
		if time.Since(start) > 5*time.Second {
			t.Fatal("no star sent")
		}
	}
	if st := f.saw("/sync/update_episode_star")[0]; st["uuid"] != epA1 || st["star"] != true {
		t.Errorf("star %v", st)
	}
	if _, err := os.Stat(a.opts.star); err == nil {
		t.Error("the request stayed")
	}

	// the pages
	up := spot(t, a, "upnext")
	if len(up.rows) != 3 || up.rows[0][1] != "dlall" || up.rows[0][2] != "Download All" || up.rows[0][3] != "Every episode is on the Q2" ||
		up.rows[1][1] != "dlnext" || up.rows[1][2] != "Download Next 5" || up.rows[1][5] != "nextn" {
		t.Fatalf("Up Next page: %v", up.rows)
	}
	up.rows = up.rows[2:] // Download All and Download Next first, then the episodes: the Q2's, the finished one gone
	if up.rows[0][0] != "action" || up.rows[0][1] != "upplay" || up.rows[0][4] != "0" || up.rows[0][5] != "unq:"+podA+":"+epA1 {
		t.Errorf("Up Next page: %v", up.rows)
	}
	// Download All: a second press turns it on, a sync through the whole of Up Next ends it, a press
	// while it runs stops it
	if r := spot(t, a, "dlall"); r.message != "Press again to download all of Up Next" || a.sy.allPending() {
		t.Errorf("dlall once: %+v", r)
	}
	if r := spot(t, a, "dlall"); r.message != "Downloading all of Up Next" || !a.sy.allPending() {
		t.Errorf("dlall twice: %+v", r)
	}
	if up := spot(t, a, "upnext"); up.rows[0][2] != "Downloading All of Up Next" {
		t.Errorf("Up Next page while all: %v", up.rows[0])
	}
	if r := spot(t, a, "dlall"); r.message != "Stopped downloading all of Up Next" || a.sy.allPending() {
		t.Errorf("dlall stop: %+v", r)
	}
	a.sy.downloadAll(true)
	a.sy.once(a.ctx)
	if a.sy.allPending() {
		t.Error("a sync through all of Up Next left Download All on")
	}
	// Stop After This One: the waiting downloads cancelled and Up Next held, until Resume Downloads (or
	// Download All)
	if r := spot(t, a, "stopafter"); !strings.HasPrefix(r.message, "Stopping after this one") || !a.sy.held() {
		t.Errorf("stopafter: %+v", r)
	}
	if d := spot(t, a, "downloads"); len(d.rows) == 0 || d.rows[0][1] != "resume" {
		t.Errorf("downloads while held: %v", d.rows)
	}
	if r := spot(t, a, "resume"); r.message != "Up Next downloads again" || a.sy.held() {
		t.Errorf("resume: %+v", r)
	}
	a.sy.hold(true)
	a.sy.downloadAll(true)
	if a.sy.held() {
		t.Error("Download All left Up Next held")
	}
	a.sy.downloadAll(false)
	// The Q2's page opened (its menu asked after openGap): the menu at once, no wait for a sync (the
	// user's, 2026-11-15, as before 1.1); one runs behind it
	a.mu.Lock()
	a.pageAt = time.Time{}
	a.mu.Unlock()
	if m := spot(t, a, "menu"); m.rows[0][1] != "upnext" {
		t.Errorf("menu as it opens: %+v", m)
	}
	// Q2 Podcast notes each opening of the page in /tmp/q2-pocket.open: a new note is an opening at once,
	// however soon; the same note again (the menu shown again) isn't
	a.opts.open = filepath.Join(t.TempDir(), "open")
	for i, note := range []string{"100\n", "100\n", "200\n"} {
		os.WriteFile(a.opts.open, []byte(note), 0o644)
		if opened, want := a.opening("menu"), i != 1; opened != want {
			t.Errorf("note %d %q: opening %v", i, note, opened)
		}
	}
	// pressed: Up Next from there, an .m3u on the card for Q2 Pod to play in order
	if r := spot(t, a, "upplay", "0"); r.message != "Playing Up Next" {
		t.Errorf("upplay: %+v", r)
	}
	m3u := filepath.Join(a.opts.dir, "upnext.m3u")
	if b, _ := os.ReadFile(m3u); string(b) != "#EXTM3U\n"+e1.Path+"\n" {
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
	// Download Next from the phone: its count kept as the Q2's (the sync it asks for runs at once), then stopped with 0
	if code, out := call("POST", "pc/downloadnext", `{"count":3}`); code != 200 || out["message"] != "Downloading the next 3 episodes" ||
		a.settings().nextCount() != 3 {
		t.Errorf("downloadnext: %d %v (%d)", code, out, a.sy.nextPending())
	}
	if _, up := call("GET", "pc/upnext", ""); up["nextCount"] != 3.0 {
		t.Errorf("upnext's next count: %v", up["nextCount"])
	}
	if code, _ := call("POST", "pc/downloadnext", `{"count":0}`); code != 200 || a.sy.nextPending() != 0 {
		t.Errorf("downloadnext stop: %d", code)
	}
	if a.settings().YouTubeKey != "K1" {
		t.Errorf("key %q", a.settings().YouTubeKey)
	}
	a.sy.once(context.Background())
	if _, up := call("GET", "pc/upnext", ""); len(up["episodes"].([]any)) != 2 {
		t.Errorf("up next %v", up)
	}
	// signed in, nothing starred: the list is there, empty, so Now Playing's heart is the star
	if b, err := os.ReadFile(a.opts.starred); err != nil || len(b) != 0 {
		t.Errorf("starred list %q %v", b, err)
	}
	if code, _ := call("POST", "pc/logout", "{}"); code != 200 || a.pc.SignedIn() {
		t.Fatal("still signed in")
	}
	if _, err := os.Stat(filepath.Join(a.opts.dir, "account.json")); err == nil {
		t.Error("the session stayed on the card")
	}
	if _, err := os.Stat(a.opts.starred); err == nil {
		t.Error("the starred list stayed: the heart would still be the star")
	}
	if code, out := call("POST", "pc/login", `{"email":"a@b.c","password":""}`); code != 400 {
		t.Errorf("empty password: %d %v", code, out)
	}
}

func TestNotesText(t *testing.T) {
	got := clean.NotesText(`<p>Guest:  <b>Ana</b> &amp; friends.</p><p>Links:<br/><a href="x">site</a></p>`)
	if want := "Guest: Ana & friends.\n\nLinks:\nsite"; got != want {
		t.Errorf("%q, want %q", got, want)
	}
}

// A download Up Next asked for is cancelled once its episode leaves Up Next (taken off in Pocket
// Casts): one that crashes the app among them. The queue an older version left counts as Up Next's;
// one asked for on the Q2 after that is kept. Downloads wait for the start's first sync.
func TestLeftUpNext(t *testing.T) {
	a, _, f := startSignedWith(t, func() bool { return false }) // offline: nothing syncs or comes down by itself
	if !a.sy.holding() {
		t.Fatal("downloads go before the first sync")
	}
	ctx := context.Background()
	e3, err := a.sy.episode(ctx, pocketcasts.Episode{UUID: epA3, Podcast: podA})
	if err != nil {
		t.Fatal(err)
	}
	a.lib.DL.Queue(e3.Key, e3.EID) // an older version's queue: Up Next's then
	a.sy.once(ctx)
	if a.sy.holding() {
		t.Fatal("still held after the sync")
	}
	eids := func() (out []string) {
		for _, j := range a.lib.DL.Jobs() {
			e, _ := a.lib.Episode(j.Key, j.EID)
			out = append(out, e.Feed.PC)
		}
		sort.Strings(out)
		return
	}
	if got := strings.Join(eids(), ","); got != epA1+","+epA2 {
		t.Fatalf("after the first sync: %s", got)
	}
	f.mu.Lock()
	f.upnext = []string{epA1} // the second taken off Up Next in Pocket Casts
	f.mu.Unlock()
	a.lib.DL.Queue(e3.Key, e3.EID) // asked for on the Q2
	a.sy.once(ctx)
	if got := strings.Join(eids(), ","); got != epA1+","+epA3 {
		t.Fatalf("after it left Up Next: %s", got)
	}
}

// New Releases: how many, and Play/Pause held on one puts it first in Up Next (play_next).
func TestNewReleasesFirst(t *testing.T) {
	a, _, f := startSigned(t)
	var page answer
	for end := time.Now().Add(5 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if page = spot(t, a, "new"); len(page.rows) == 2 || time.Now().After(end) {
			break
		}
	}
	if len(page.rows) != 2 || page.rows[0][2] != "1 new episode" || page.rows[1][2] != "Episode 3" {
		t.Fatalf("New Releases: %v", page.rows)
	}
	spot(t, a, "upf:"+podA+":"+epA3)
	if put := f.saw("/up_next/play_next"); len(put) != 1 || put[0]["episode"].(map[string]any)["uuid"] != epA3 {
		t.Fatalf("play_next: %v", put)
	}
}

// Q2 Podcast's notes, a cancel and Download Next (the user's, 2026-11-15): an episode played to its
// end on the Q2 leaves the Q2's Up Next, though Pocket wasn't running near its end, and nothing is
// sent (no two-way sync); one deleted there, or cancelled here, isn't fetched by Up Next again;
// Download Next asks for the next few not on the Q2, after those Up Next keeps downloaded.
func TestNotesKeptOffAndDownloadNext(t *testing.T) {
	a, _, f := startSigned(t)
	ctx := context.Background()
	waiting := func(id string) bool {
		e, _ := a.lib.PCEpisode(podA, id)
		return e.OnCard || e.Job != nil
	}
	onCard := func(ids ...string) {
		t.Helper()
		for start := time.Now(); ; time.Sleep(20 * time.Millisecond) {
			a.sy.once(ctx)
			all := true
			for _, id := range ids {
				e, _ := a.lib.PCEpisode(podA, id)
				all = all && e.OnCard
			}
			if all {
				return
			}
			if time.Since(start) > 15*time.Second {
				t.Fatalf("not downloaded %v: %+v", ids, a.lib.DL.Jobs())
			}
		}
	}
	onCard(epA2, epA1) // Up Next's two, kept downloaded
	// a third in Up Next, put last from the Q2 (so last in the Q2's too), past the two kept: Download
	// Next 1 asks for it
	f.mu.Lock()
	f.upnext = []string{epA2, epA1, epA3}
	f.mu.Unlock()
	if r := spot(t, a, "upq:"+podA+":"+epA3); r.message != "Added to Up Next" {
		t.Fatalf("upq: %+v", r)
	}
	a.change(func(s *settings) { s.UpNext, s.NextCount = 2, 1 })
	a.sy.forget()
	a.sy.once(ctx)
	if waiting(epA3) {
		t.Fatal("past the two kept, fetched by itself")
	}
	if r := spot(t, a, "dlnext"); r.message != "Downloading the next 1 episode" {
		t.Errorf("dlnext: %+v", r)
	}
	a.sy.once(ctx)
	if !waiting(epA3) || a.sy.nextPending() != 0 {
		t.Errorf("Download Next didn't ask for the third, or stayed on (%d)", a.sy.nextPending())
	}
	// its Play/Pause hold: the next count
	if r := spot(t, a, "nextn"); r.message != "Download Next: 3 episodes" || a.settings().nextCount() != 3 {
		t.Errorf("nextn: %+v", r)
	}
	// deleted (Play/Pause held twice), it's kept off: not fetched again by Up Next, even all of it; a
	// cancel keeps one off too
	onCard(epA3)
	e3, _ := a.lib.PCEpisode(podA, epA3)
	spot(t, a, "del:"+e3.Key+":"+e3.EID)
	if r := spot(t, a, "del:"+e3.Key+":"+e3.EID); r.message != "Deleted" {
		t.Fatalf("del: %+v", r)
	}
	spot(t, a, "cancel", "x:y")
	if !a.sy.keptOff("x:y") {
		t.Error("a cancel doesn't keep it off")
	}
	a.change(func(s *settings) { s.UpNext = -1 })
	a.sy.once(ctx)
	if waiting(epA3) {
		t.Error("a cancelled episode fetched again by Up Next")
	}
	if up := spot(t, a, "upnext"); !strings.Contains(fmt.Sprint(up.rows), "Kept off the Q2 · Download") {
		t.Errorf("Up Next page doesn't say it's kept off: %v", up.rows)
	}
	// Q2 Podcast's notes: the second finished on the Q2, the first deleted there
	e1, _ := a.lib.PCEpisode(podA, epA1)
	e2, _ := a.lib.PCEpisode(podA, epA2)
	os.WriteFile(a.opts.done, []byte("F\t"+e2.Path+"\nD\t"+e1.Path+"\n"), 0o644)
	a.sy.once(ctx)
	f.mu.Lock()
	removed := strings.Join(f.removed, ",")
	f.mu.Unlock()
	if p := f.saw("/sync/update_episode"); len(p) != 0 || removed != "" {
		t.Errorf("finished on the Q2: sent %v, removed from the account's Up Next %q", p, removed)
	}
	for _, e := range func() []pocketcasts.Episode { up, _ := a.sy.upNext(); return up }() {
		if e.UUID == epA2 {
			t.Error("finished on the Q2, but still in the Q2's Up Next")
		}
	}
	if !a.sy.keptOff(e1.Key + ":" + e1.EID) {
		t.Error("deleted on the Q2, but not kept off")
	}
	if _, err := os.Stat(a.opts.done); err == nil {
		t.Error("the notes stayed")
	}
	// a press fetches a kept-off episode, and it isn't kept off any more
	if r := spot(t, a, "get", e3.Key+":"+e3.EID); r.message == "" || a.sy.keptOff(e3.Key+":"+e3.EID) {
		t.Errorf("get: %+v, kept off %v", r, a.sy.keptOff(e3.Key+":"+e3.EID))
	}
}

// The Q2's own Up Next (the user's, 2026-11-15: no two-way sync, the Q2 takes the account's changes,
// an episode new to Up Next at the top of the Q2's): the account's order at first; the episode the Q2
// starts goes first; one added elsewhere goes in after it; one taken out elsewhere leaves; one
// finished on the Q2 leaves the Q2's only; one added again is new again. Nothing is sent.
func TestQ2UpNextOrder(t *testing.T) {
	a, card, f := startSigned(t)
	ctx := context.Background()
	order := func() string {
		up, _ := a.sy.upNext()
		var ids []string
		for _, e := range up {
			ids = append(ids, e.UUID[len(e.UUID)-1:])
		}
		return strings.Join(ids, "")
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
	set := func(ids ...string) {
		f.mu.Lock()
		f.upnext = ids
		f.mu.Unlock()
		a.sy.once(ctx)
	}
	if got := order(); got != "21" {
		t.Fatalf("the account's order to start with: %q", got)
	}
	e1, _ := a.lib.PCEpisode(podA, epA1)
	ring(t, card, e1.Path, 40) // the Q2 starts the second: first in the Q2's
	a.sy.once(ctx)
	if got := order(); got != "12" {
		t.Errorf("started on the Q2: %q", got)
	}
	if set(epA2, epA1, epA3); order() != "132" { // added on the phone, last in the account's
		t.Errorf("a new one, after the one playing: %q", order())
	}
	if set(epA1, epA3); order() != "13" { // taken out on the phone
		t.Errorf("one taken out elsewhere: %q", order())
	}
	os.WriteFile(a.opts.done, []byte("F\t"+e1.Path+"\n"), 0o644) // finished on the Q2
	if a.sy.once(ctx); order() != "3" {
		t.Errorf("finished on the Q2: %q", order())
	}
	if set(epA1, epA3); order() != "3" { // still in the account's: not back on the Q2
		t.Errorf("a finished one came back: %q", order())
	}
	if set(epA1, epA3, epA2); order() != "23" { // added again: new, at the top
		t.Errorf("added again: %q", order())
	}
	f.mu.Lock()
	removed := len(f.removed)
	f.mu.Unlock()
	if p, now, h := f.saw("/sync/update_episode"), f.saw("/up_next/play_now"), f.saw("/history/do"); len(p)+len(now)+len(h)+removed != 0 {
		t.Errorf("sent: places %v, play_now %v, history %v, %d removed", p, now, h, removed)
	}
	if b, _ := os.ReadFile(filepath.Join(a.opts.dir, "sync.json")); !strings.Contains(string(b), `"q2_up_next":["`+epA2+`","`+epA3+`"]`) {
		t.Errorf("sync.json: %s", b)
	}
}

// Sync: Two-Way (the user's, 2026-11-15: a setting, one-way unless chosen): the Q2's own go back too, a
// place it saves sent, the episode it starts first in the account's Up Next and in the history, one it
// finished marked played and out of the account's Up Next; the Q2's own Up Next goes on as one-way's.
func TestTwoWaySync(t *testing.T) {
	a, card, f := startSigned(t)
	ctx := context.Background()
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
	if r := spot(t, a, "set", "twoway"); r.message != "Sync: Two-Way" || !a.settings().TwoWay {
		t.Fatalf("twoway: %+v", r)
	}
	if row := findRow(t, spot(t, a, "adfree"), "Sync: Two-Way"); row[1] != "set" || row[4] != "twoway" {
		t.Errorf("setting row %v", row)
	}
	e1, _ := a.lib.PCEpisode(podA, epA1)
	ring(t, card, e1.Path, 7)
	a.sy.once(ctx)
	if sent := f.saw("/sync/update_episode"); len(sent) == 0 || sent[len(sent)-1]["uuid"] != epA1 || sent[len(sent)-1]["position"] != 7.0 {
		t.Fatalf("place sent: %v", sent)
	}
	if now := f.saw("/up_next/play_now"); len(now) != 1 || now[0]["episode"].(map[string]any)["uuid"] != epA1 {
		t.Errorf("play_now: %v", now)
	}
	if h := f.saw("/history/do"); len(h) != 1 || h[0]["episode"] != epA1 {
		t.Errorf("history: %v", h)
	}
	ring(t, card) // its place forgotten at its end, as Q2 Pod does, and noted finished
	os.WriteFile(a.opts.done, []byte("F\t"+e1.Path+"\n"), 0o644)
	a.sy.once(ctx)
	f.mu.Lock()
	removed := strings.Join(f.removed, ",")
	f.mu.Unlock()
	if p := f.saw("/sync/update_episode"); p[len(p)-1]["status"] != 3.0 || p[len(p)-1]["uuid"] != epA1 || removed != epA1 {
		t.Errorf("finished: %v, removed %q", p[len(p)-1], removed)
	}
	if up, _ := a.sy.upNext(); len(up) != 1 || up[0].UUID != epA2 {
		t.Errorf("the Q2's Up Next: %v", up)
	}
	if r := spot(t, a, "set", "twoway"); r.message != "Sync: One-Way" || a.settings().TwoWay {
		t.Errorf("back to one-way: %+v", r)
	}
}
