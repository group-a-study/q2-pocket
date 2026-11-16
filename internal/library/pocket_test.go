package library

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"q2-pocket/internal/clean"
	"q2-pocket/internal/feed"
	"q2-pocket/internal/mp3"
	"q2-pocket/internal/pocketcasts"
	"q2-pocket/internal/tag"
)

func mp3Fixture(t *testing.T, name string) (*mp3.Stream, []byte) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "mp3", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	s, err := mp3.Scan(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	return s, b
}

// withAd is content.mp3 served with an advert after its 200th frame.
func withAd(t *testing.T, ad string) []byte {
	c, cb := mp3Fixture(t, "content.mp3")
	a, ab := mp3Fixture(t, ad)
	out := append([]byte(nil), cb[:c.Frames[200].Off]...)
	out = append(out, ab[a.Frames[a.Info].Off:]...)
	return append(out, cb[c.Frames[200].Off:]...)
}

const (
	podUUID = "a9a362c0-9404-013f-5cfc-02ffffe690b5"
	epUUID  = "7b65ac2a-4b3a-45f2-9bc0-dd9eab66438c"
)

// A Pocket Casts show: its episodes from the cache; one downloaded comes out without the advert a
// second copy doesn't share. With no room on the card for a cut copy beside the download (the user's,
// 2026-11-16: such an episode went in as it came, ads, title and all), it is cut and tagged where it
// is, to the same bytes.
func TestPocketShow(t *testing.T) {
	roomy := pocketShow(t, true)
	if tight := pocketShow(t, false); !bytes.Equal(tight, roomy) || len(tight) == 0 {
		t.Errorf("cut where it is: %d bytes, %d with room for a copy", len(tight), len(roomy))
	}
}

func pocketShow(t *testing.T, room bool) []byte {
	if !room { // the card's folders have no room for a copy; the app's own folder has
		was := freeBytes
		freeBytes = func(p string) (int64, bool) {
			if strings.Contains(p, "Podcasts") {
				return reserve + 1000, true
			}
			return 1 << 40, true
		}
		t.Cleanup(func() { freeBytes = was })
	}
	first, other := withAd(t, "ad1.mp3"), withAd(t, "ad2.mp3")
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/cache/podcast/full/"+podUUID:
			json.NewEncoder(w).Encode(map[string]any{"podcast": map[string]any{"uuid": podUUID, "title": "Machine Gods", "author": "NPR",
				"episodes": []map[string]any{{"uuid": epUUID, "title": "Tsunami", "url": srv.URL + "/ep.mp3",
					"file_type": "audio/mpeg", "duration": 12, "published": "2026-11-06T12:00:00Z"}}}})
		case r.URL.Path == "/cache/mobile/show_notes/full/"+podUUID:
			json.NewEncoder(w).Encode(map[string]any{"podcast": map[string]any{"uuid": podUUID, "episodes": []map[string]any{
				{"uuid": epUUID, "show_notes": "<p>Kevin &amp; Casey.</p><p>Links</p>", "chapters_url": srv.URL + "/chapters.json"}}}})
		case r.URL.Path == "/chapters.json":
			w.Write([]byte(`{"version":"1.2.0","chapters":[{"startTime":0,"title":"Open"},{"startTime":5,"title":"Talk"}]}`))
		case r.URL.Path == "/ep.mp3":
			b := first
			if strings.Contains(r.UserAgent(), "second copy") {
				b = other
			}
			if r.Header.Get("Range") == "bytes=0-0" {
				w.Header().Set("Content-Range", "bytes 0-0/"+strconv.Itoa(len(b)))
				w.WriteHeader(http.StatusPartialContent)
				w.Write(b[:1])
				return
			}
			w.Write(b)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	old := pocketcasts.Cache
	pocketcasts.Cache = srv.URL + "/cache"
	t.Cleanup(func() { pocketcasts.Cache = old })
	dir := t.TempDir()
	pc := pocketcasts.New(srv.Client(), "test", pocketcasts.Token{})
	app := filepath.Join(dir, ".pocket")
	cl := &clean.Cleaner{Dir: app, Fetch: srv.Client(), API: srv.Client(), Agent: "test", PC: pc,
		Settings: func() clean.Settings { return clean.Settings{Ads: true, Sponsors: true, Copy: true} }}
	l, err := Open(Options{Dir: app, Root: filepath.Join(dir, "Podcasts"), Client: srv.Client(), Fetch: srv.Client(),
		Agent: "test", Ready: func() bool { return true }, PC: pc, Clean: cl})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(l.DL.Stop)
	si := l.AdoptPC(pocketcasts.Podcast{UUID: podUUID, Title: "Machine Gods"}, false)
	if err := l.Refresh(context.Background(), si.Key); err != nil {
		t.Fatal(err)
	}
	e, err := l.PCEpisode(podUUID, epUUID)
	if err != nil || e.Title != "Tsunami" || e.PC != podUUID || e.Feed.PC != epUUID {
		t.Fatalf("%+v %v", e, err)
	}
	l.DL.Start()
	if _, err := l.DL.Queue(e.Key, e.EID); err != nil {
		t.Fatal(err)
	}
	for start := time.Now(); !e.OnCard; e, _ = l.PCEpisode(podUUID, epUUID) {
		if time.Since(start) > 10*time.Second {
			t.Fatalf("not downloaded: %+v", l.DL.Jobs())
		}
		time.Sleep(20 * time.Millisecond)
	}
	eps, ok := l.PCEpisodes([]PCID{{podUUID, "missing"}, {podUUID, epUUID}, {"nopod", epUUID}})
	if !slices.Equal(ok, []bool{false, true, false}) || !eps[1].OnCard || eps[1].Path != e.Path || eps[1].Size != e.Size {
		t.Errorf("as a batch: %v %+v", ok, eps)
	}
	got, err := mp3.ScanFile(e.Path)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := mp3Fixture(t, "content.mp3")
	if len(got.Frames) != len(c.Frames) {
		t.Errorf("%d frames on the card, the episode has %d", len(got.Frames), len(c.Frames))
	}
	r := clean.Load(app, e.Key, e.EID)
	if r == nil || len(r.Cuts) != 1 {
		t.Errorf("report %+v", r)
	}
	if b, err := os.ReadFile(clean.NotesPath(e.Path)); err != nil || !strings.Contains(string(b), "Kevin & Casey.\n\nLinks") {
		t.Errorf("notes %q %v", b, err)
	}
	if h, err := tag.Read(e.Path); err != nil || len(h.Chapters()) != 2 || h.Chapters()[1].Title != "Talk" || h.Chapters()[1].Start != 5000 {
		t.Errorf("chapters %v", err)
	}
	// unsubscribed elsewhere, but in Up Next: hidden, out of the lists, its episode still found
	l.Hide(si.Key, true)
	if len(l.Shows()) != 0 || len(l.All()) != 1 {
		t.Errorf("hidden: %d shown, %d in all", len(l.Shows()), len(l.All()))
	}
	if eps, _ := l.Downloaded(); len(eps) != 1 {
		t.Errorf("downloaded: %d", len(eps))
	}
	if left, _ := os.ReadDir(filepath.Join(app, "partial")); len(left) != 0 {
		t.Errorf("partial left: %v", left)
	}
	b, _ := os.ReadFile(e.Path)
	return b
}

// A show subscribed by its feed, then found in Pocket Casts: its episode on the card keeps its file.
func TestAdopted(t *testing.T) {
	prev := []feed.Episode{{GUID: "rss-1", Title: "Pilot", URL: "https://tracker.example/x/host.fm/ep1.mp3?k=1"},
		{GUID: "rss-2", Title: "Second  episode", URL: "https://host.fm/a.mp3"}}
	eps := []feed.Episode{{GUID: "u1", PC: "u1", Title: "Pilot", URL: "https://other.example/x/host.fm/ep1.mp3"},
		{GUID: "u2", PC: "u2", Title: "Second episode", URL: "https://host.fm/b.mp3"},
		{GUID: "u3", PC: "u3", Title: "Third", URL: "https://host.fm/c.mp3"}}
	adopted(prev, eps)
	if eps[0].GUID != "rss-1" || eps[1].GUID != "rss-2" || eps[2].GUID != "u3" || eps[0].PC != "u1" {
		t.Errorf("%+v", eps)
	}
}

// An episode of the account (Up Next) that its show's list lacks (dropped from its feed, or
// published again under a new id) is added from the account's copy, its address and all, and stays
// when the show is fetched again; one with no audio isn't.
func TestAddPC(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/cache/podcast/full/"+podUUID {
			json.NewEncoder(w).Encode(map[string]any{"podcast": map[string]any{"uuid": podUUID, "title": "The Idiot",
				"episodes": []map[string]any{{"uuid": epUUID, "title": "Chapter 5", "url": "https://x.example/new5.mp3",
					"file_type": "audio/mpeg", "published": "2026-03-26T02:05:00Z"}}}})
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	old := pocketcasts.Cache
	pocketcasts.Cache = srv.URL + "/cache"
	t.Cleanup(func() { pocketcasts.Cache = old })
	dir := t.TempDir()
	pc := pocketcasts.New(srv.Client(), "test", pocketcasts.Token{})
	l, err := Open(Options{Dir: filepath.Join(dir, ".pocket"), Root: filepath.Join(dir, "Podcasts"), Client: srv.Client(),
		Fetch: srv.Client(), Agent: "test", Ready: func() bool { return false }, PC: pc})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(l.DL.Stop)
	si := l.AdoptPC(pocketcasts.Podcast{UUID: podUUID, Title: "The Idiot"}, true)
	if err := l.Refresh(context.Background(), si.Key); err != nil {
		t.Fatal(err)
	}
	oldID := "9fef8f10-b055-4ecd-b734-ae9947b08f38" // Up Next's: the chapter before it was published again
	up := pocketcasts.Episode{UUID: oldID, Podcast: podUUID, Title: "Chapter 5", URL: "https://x.example/old5.mp3",
		FileType: "audio/mpeg", Published: "2026-03-26T02:05:00Z"}
	if _, err := l.PCEpisode(podUUID, oldID); err == nil {
		t.Fatal("not in the show's list yet")
	}
	e, err := l.AddPC(up)
	if err != nil || e.Feed.URL != up.URL || !e.Feed.Extra || e.Feed.PC != oldID {
		t.Fatalf("%+v %v", e, err)
	}
	if again, err := l.AddPC(up); err != nil || again.EID != e.EID {
		t.Fatal("added once", again, err)
	}
	l.mu.Lock()
	l.find(si.Key).c.ETag = "" // fetched again in full
	l.mu.Unlock()
	if err := l.Refresh(context.Background(), si.Key); err != nil {
		t.Fatal(err)
	}
	if got, err := l.PCEpisode(podUUID, oldID); err != nil || got.EID != e.EID || got.Path == e.Path && got.Path == "" {
		t.Fatalf("kept on a refresh: %+v %v", got, err)
	}
	if newer, _ := l.PCEpisode(podUUID, epUUID); newer.Path == e.Path {
		t.Error("the show's own Chapter 5 and Up Next's share a file name")
	}
	if _, err := l.AddPC(pocketcasts.Episode{UUID: "u2", Podcast: podUUID, Title: "Video", URL: "https://x.example/v.mp4", FileType: "video/mp4"}); err == nil {
		t.Error("no audio: not added")
	}
}
