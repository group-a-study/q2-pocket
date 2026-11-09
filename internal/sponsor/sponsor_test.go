package sponsor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestTranscriptFormats(t *testing.T) {
	vtt := "WEBVTT\n\nNOTE made by hand\n\n00:00.000 --> 00:02.280\nHello <v Bob>there</v>\n\n1\n01:02:03.500 --> 01:02:05.000\nLate line\nwith two rows\n"
	c := Transcript([]byte(vtt), "text/vtt")
	if len(c) != 2 || c[0].Text != "Hello there" || c[1].From != 3723.5 || c[1].Text != "Late line with two rows" {
		t.Fatalf("vtt %+v", c)
	}
	srt := "1\r\n00:00:01,000 --> 00:00:04,500\r\nFirst\r\n\r\n2\r\n00:00:05,000 --> 00:00:06,000\r\nSecond\r\n"
	if c := Transcript([]byte(srt), "application/x-subrip"); len(c) != 2 || c[0].To != 4.5 {
		t.Fatalf("srt %+v", c)
	}
	js := `{"version":"1.0.0","segments":[{"speaker":"A","startTime":0.5,"endTime":2,"body":"Hi"},{"startTime":3,"endTime":4,"body":" "}]}`
	if c := Transcript([]byte(js), "application/json"); len(c) != 1 || c[0].From != 0.5 || End(c) != 2 {
		t.Fatalf("json %+v", c)
	}
}

func cues(lines ...string) []Cue {
	var out []Cue
	for i, l := range lines {
		out = append(out, Cue{float64(i * 5), float64(i*5 + 5), l})
	}
	return out
}

func TestReads(t *testing.T) {
	c := cues(
		"So that's where we were.",                                                // 0
		"This episode is brought to you by Acme Mattresses.",                      // 5: a read starts
		"I've slept on mine for a year and my back has never been better.",        // 10
		"They ship to your door in a box, it's honestly kind of magic.",           // 15
		"Go to acme dot com slash pod and use promo code POD for 20 percent off.", // 20
		"Terms apply.",                                               // 25
		"Anyway, back to the show.",                                  // 30: it ends
		"Where were we? Oh right, the election.",                     // 35
		"Let's take a quick break.",                                  // 40: no offer follows
		"And we're back. My guest is going to go first on this one.", // 45
		"She thinks the policy is bad.",                              // 50
	)
	r := Reads(c)
	if len(r) != 1 || r[0].From != 5 || r[0].To != 35 {
		t.Fatalf("%+v", r)
	}
	// "support for the show comes from", with the offer late and no "back to"
	c = cues("Support for the show comes from Wayfair.", "The holidays are near.", "Maybe some mugs.", "Or pillows.",
		"Or a sideboard, like mine.", "Head to Wayfair.com to shop all things home.", "That's spelled W-A-Y-F-A-I-R dot com.",
		"Philosophy is old.", "Aristotle said things.", "Many things.", "About minds.", "And more.", "Still more.")
	r = Reads(c)
	if len(r) != 1 || r[0].From != 0 || r[0].To != 35 {
		t.Fatalf("%+v", r)
	}
}

func TestSponsorBlock(t *testing.T) {
	id := "dQw4w9WgXcQ"
	sum := sha256.Sum256([]byte(id))
	prefix := hex.EncodeToString(sum[:])[:4]
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/skipSegments/"+prefix || r.URL.Query().Get("service") != "YouTube" {
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode([]map[string]any{
			{"videoID": "other", "segments": []map[string]any{{"segment": []float64{1, 9}, "category": "sponsor", "actionType": "skip"}}},
			{"videoID": id, "segments": []map[string]any{
				{"segment": []float64{60, 120.5}, "category": "sponsor", "actionType": "skip", "videoDuration": 3600.2, "votes": 3},
				{"segment": []float64{100, 130}, "category": "selfpromo", "actionType": "skip", "videoDuration": 3600.2},
				{"segment": []float64{10, 20}, "category": "sponsor", "actionType": "mute"},
				{"segment": []float64{200, 220}, "category": "sponsor", "actionType": "skip", "votes": -2}}}})
	}))
	defer srv.Close()
	SponsorBlockBase = srv.URL
	sb := &SponsorBlock{HTTP: srv.Client(), Agent: "test"}
	segs, length, err := sb.Segments(context.Background(), "YouTube", id, []string{CatSponsor, CatSelfPromo})
	if err != nil || length != 3600.2 || len(segs) != 1 || segs[0].From != 60 || segs[0].To != 130 {
		t.Fatalf("%+v %v %v", segs, length, err)
	}
	if segs, _, err := sb.Segments(context.Background(), "Spotify", id, []string{CatSponsor}); err != nil || len(segs) != 0 {
		t.Fatalf("spotify: %+v %v", segs, err)
	}
}

func TestYouTubeFind(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("key") != "k" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		switch r.URL.Path {
		case "/search":
			fmt.Fprint(w, `{"items":[
				{"id":{"videoId":"clipaaaaaaa"},"snippet":{"title":"Best moments of #414","publishedAt":"2024-02-10T00:00:00Z"}},
				{"id":{"videoId":"fullaaaaaaa"},"snippet":{"title":"Tucker Carlson: Putin, Navalny, Trump | Lex Fridman Podcast #414","publishedAt":"2024-02-16T00:00:00Z"}}]}`)
		case "/videos":
			fmt.Fprint(w, `{"items":[{"id":"fullaaaaaaa","contentDetails":{"duration":"PT3H12M30S"}}]}`)
		}
	}))
	defer srv.Close()
	YouTubeBase = srv.URL
	y := &YouTube{Key: "k", HTTP: srv.Client()}
	m, err := y.Find(context.Background(), "Lex Fridman Podcast", "#414 – Tucker Carlson: Putin, Navalny, Trump", time.Date(2024, 2, 15, 0, 0, 0, 0, time.UTC), 11555)
	if err != nil || m.ID != "fullaaaaaaa" || m.Length != 11550 {
		t.Fatalf("%+v %v", m, err)
	}
}

func TestLinksAlike(t *testing.T) {
	yt, sp := Links(`Watch: <a href="https://www.youtube.com/watch?v=fOCWBhuDdDo&amp;t=5">video</a> https://youtu.be/fOCWBhuDdDo
		Listen https://open.spotify.com/episode/4rOoJ6Egrf8K2IrywzwOMk?si=x`)
	if len(yt) != 1 || yt[0] != "fOCWBhuDdDo" || len(sp) != 1 || sp[0] != "4rOoJ6Egrf8K2IrywzwOMk" {
		t.Errorf("%v %v", yt, sp)
	}
	if a := Alike("#414 – Tucker Carlson: Putin", "Tucker Carlson: Putin | Lex Fridman Podcast #414"); a < 0.99 {
		t.Errorf("alike %v", a)
	}
	if a := Alike("Episode 12: Pizza", "Episode 13: Pizza"); a != 0 {
		t.Errorf("another number is another episode: %v", a)
	}
}

func TestAdChapters(t *testing.T) {
	ch := []Chapter{{0, 0, "Intro"}, {65, 0, "Sponsor: Acme"}, {130, 0, "The interview"}, {900, 960, "Ad break"}, {1200, 0, "Adams' story"}}
	s := AdChapters(ch, 1500)
	if len(s) != 2 || s[0].From != 65 || s[0].To != 130 || s[1].From != 900 || s[1].To != 960 {
		t.Fatalf("%+v", s)
	}
	if !strings.Contains(s[0].Why, "Sponsor: Acme") {
		t.Error(s[0].Why)
	}
}

func TestFit(t *testing.T) {
	for _, c := range []struct {
		ref, clean, orig float64
		tr               bool
		want             int
	}{
		{1806.4, 1806, 1961, false, Clean},
		{1961, 1806, 1961.5, false, Original},
		{1850, 1806, 1961, false, Unplaced},
		{1803.0, 1806, 1961, true, Clean},        // the transcript's last words end before the music
		{1961.8, 1655.4, 2046.1, true, Unplaced}, // a copy with other adverts
		{0, 1806, 1961, false, Unplaced},
	} {
		if got := Fit(c.ref, c.clean, c.orig, c.tr); got != c.want {
			t.Errorf("Fit(%v, %v, %v, %v) = %d, want %d", c.ref, c.clean, c.orig, c.tr, got, c.want)
		}
	}
	if d := isoDuration("PT1H2M3S"); d != 3723 {
		t.Error(d)
	}
}
