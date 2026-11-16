package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"q2-pocket/internal/feed"
	"q2-pocket/internal/library"
	"q2-pocket/internal/pocketcasts"
)

// An episode gone at its address is found elsewhere: the original of a rebroadcast in its own show,
// a trailer in another show's feed (Apple's directory), a copy only its Libsyn site still links (not
// another show's in its sidebar); a copy that doesn't answer, or another episode, isn't taken.
func TestElsewhere(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/cache/podcast/full/show1":
			json.NewEncoder(w).Encode(map[string]any{"podcast": map[string]any{"uuid": "show1", "title": "This Is Actually Happening",
				"episodes": []map[string]any{
					{"uuid": "orig", "title": "238: What if you were tortured in an Iranian prison?", "url": srv.URL + "/audio/orig.mp3", "published": "2022-06-07T10:00:00Z"},
					{"uuid": "dead", "title": "What if you were tortured in an Iranian prison?", "url": srv.URL + "/gone.mp3", "published": "2022-06-01T10:00:00Z"},
				}}})
		case r.URL.Path == "/apple":
			term := r.URL.Query().Get("term")
			var res []map[string]any
			if strings.Contains(term, "Nice To Meet You") {
				res = []map[string]any{
					{"trackName": "Introducing NICE TO MEET YOU", "episodeUrl": srv.URL + "/gone.mp3", "collectionName": "The Big Fib"},
					{"trackName": "Introducing: Nice to Meet You", "episodeUrl": srv.URL + "/audio/trailer.mp3", "collectionName": "Nice to Meet You", "trackTimeMillis": 120000},
					{"trackName": "Introducing: Nice to Meet You Again", "episodeUrl": srv.URL + "/audio/other.mp3"},
				}
			}
			json.NewEncoder(w).Encode(map[string]any{"results": res})
		case r.URL.Path == "/libsyn/writeaboutnow/life-lessons-from-a-military-cyber-spy":
			io.WriteString(w, `<a href="https://writeaboutnow.libsyn.com/x">x</a><div class="sidebar"><a href="https://traffic.libsyn.com/secure/othershow/b.mp3">b</a></div>`+
				`<a href="https://traffic.libsyn.com/secure/writeaboutnow/WAN-KennethEarl-FINAL2.mp3?dest-id=575049">Download</a>`)
		case strings.HasPrefix(r.URL.Path, "/audio/"):
			w.Header().Set("Content-Type", "audio/mpeg")
			w.WriteHeader(http.StatusPartialContent)
			w.Write([]byte{0xff})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	oldCache, oldApple, oldLib := pocketcasts.Cache, appleSearch, libsynSite
	pocketcasts.Cache, appleSearch, libsynSite = srv.URL+"/cache", srv.URL+"/apple", srv.URL+"/libsyn/%s/%s"
	defer func() { pocketcasts.Cache, appleSearch, libsynSite = oldCache, oldApple, oldLib }()
	// Libsyn's audio host answered by the test server
	viaTest := roundTrip(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host == "traffic.libsyn.com" {
			r = r.Clone(r.Context())
			r.URL.Scheme, r.URL.Host, r.URL.Path = "http", strings.TrimPrefix(srv.URL, "http://"), "/audio"+r.URL.Path
		}
		return http.DefaultTransport.RoundTrip(r)
	})
	hc := &http.Client{Transport: viaTest}
	a := &app{client: hc, pc: pocketcasts.New(hc, "test", pocketcasts.Token{})}
	day := func(s string) time.Time { t, _ := time.Parse("2006-01-02", s); return t }
	for _, c := range []struct {
		e    library.EpisodeInfo
		want string
	}{
		{library.EpisodeInfo{Title: "What if you were tortured in an Iranian prison? - [Rebroadcast - #238]", Show: "This Is Actually Happening",
			PC: "show1", Date: day("2022-12-27"), Feed: feed.Episode{PC: "rb", URL: srv.URL + "/gone.mp3"}}, "/audio/orig.mp3"},
		{library.EpisodeInfo{Title: "Introducing: Nice To Meet You", Show: "Young Ben Franklin", Date: day("2021-09-22"),
			Feed: feed.Episode{URL: srv.URL + "/gone.mp3"}}, "/audio/trailer.mp3"},
		{library.EpisodeInfo{Title: "Life Lessons From a Military Cyber Spy", Show: "Write About Now", Duration: 2758,
			Date: day("2022-03-01"), Feed: feed.Episode{URL: "https://traffic.libsyn.com/secure/writeaboutnow/WAN-KennethEarl-FINAL.mp3?dest-id=575049"}},
			"https://traffic.libsyn.com/secure/writeaboutnow/WAN-KennethEarl-FINAL2.mp3?dest-id=575049"},
		{library.EpisodeInfo{Title: "Nowhere To Be Found", Show: "X", Date: day("2022-03-01")}, ""},
	} {
		got := strings.TrimPrefix(a.elsewhere(context.Background(), c.e), srv.URL)
		if got != c.want {
			t.Errorf("%q: %q, want %q", c.e.Title, got, c.want)
		}
	}
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
