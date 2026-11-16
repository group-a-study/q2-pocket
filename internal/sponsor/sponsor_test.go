package sponsor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
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

// A show's notes' timestamps (Huberman Lab's, the user's episode, 2026-11-15): the sponsor reads they
// name as segments, each to the next timestamp; a list that doesn't rise, has under three, or starts
// late is none.
func TestNotesChapters(t *testing.T) {
	notes := "Thank you to our sponsors\nAG1: https://drinkag1.com/huberman\n\nTimestamps\n(00:00:00) Arthur Brooks\n" +
		"(00:02:40) Public Science Education\n(00:17:35) Sponsors: OurPlace & Helix Sleep\n" +
		"(00:20:41) Positive Self-Reflection\n(00:48:50) Sponsor: AG1\n(00:50:08) #2: Satisfaction, Struggle & Purpose\n"
	ch := NotesChapters(notes)
	if len(ch) != 6 || ch[2].From != 1055 || ch[2].Title != "Sponsors: OurPlace & Helix Sleep" {
		t.Fatalf("chapters %+v", ch)
	}
	segs := AdChapters(ch, 6000)
	if len(segs) != 2 || segs[0].From != 1055 || segs[0].To != 1241 || segs[1].From != 2930 || segs[1].To != 3008 {
		t.Errorf("segments %+v", segs)
	}
	// on one line, as a feed's description reads once its tags are gone; a bare 17:35 too
	if ch := NotesChapters("Timestamps 0:00 Intro 05:10 Sponsor: Acme 7:00 The rest"); len(ch) != 3 || ch[1].From != 310 || ch[1].Title != "Sponsor: Acme" {
		t.Errorf("one line: %+v", ch)
	}
	for _, s := range []string{"(00:00:00) A\n(00:10:00) B", "(00:00:00) A\n(00:10:00) B\n(00:05:00) C", "(00:05:00) A\n(00:10:00) B\n(00:15:00) C"} {
		if ch := NotesChapters(s); ch != nil {
			t.Errorf("%q: %+v", s, ch)
		}
	}
}

func TestMentions(t *testing.T) {
	// Lex Fridman Podcast #504's YouTube captions
	vtt := `WEBVTT

00:00:36.280 --> 00:00:41.440
War on the Rocks platform. Please
follow and support their work.

00:00:42.480 --> 00:00:46.360
This is the Lex Fridman Podcast. To support
it, please check out our sponsors in

00:00:46.400 --> 00:00:49.940
the description, where you
can also find links to

00:00:50.000 --> 00:00:53.940
contact me, ask questions,
give feedback, and so on.

00:00:54.600 --> 00:00:59.120
And now, dear friends, here's
Preston Stewart and Michael Kofman.
`
	got := Mentions(Transcript([]byte(vtt), "text/vtt"))
	if len(got) != 1 {
		t.Fatalf("got %v", got)
	}
	if g := got[0]; g.From < 43.5 || g.From > 44.5 || g.To < 53.8 || g.To > 54.0 {
		t.Fatalf("got %.2f-%.2f, want about 44-53.9", g.From, g.To)
	}
	none := Transcript([]byte("WEBVTT\n\n00:00:01.000 --> 00:00:05.000\nWe talked about how sponsors shape media.\n"), "text/vtt")
	if m := Mentions(none); len(m) != 0 {
		t.Fatalf("a talk about sponsors taken for a pointer: %v", m)
	}
}

// YouTube's speech recognition of Lex Fridman Podcast #277 (json3: words, no punctuation): its
// pointers to the sponsors cut to the word, the Emerson quote after the second kept.
func TestMentionsASR(t *testing.T) {
	asr := `{"wireMagic":"pb3","events":[
{"tStartMs":64400,"dDurationMs":5079,"segs":[{"utf8":"friend"},{"utf8":" this","tOffsetMs":1000},{"utf8":" is","tOffsetMs":1200},{"utf8":" leex","tOffsetMs":1400},{"utf8":" Freedman","tOffsetMs":1700},{"utf8":" podcast","tOffsetMs":2200},{"utf8":" to","tOffsetMs":2800}]},
{"tStartMs":69400,"dDurationMs":20,"aAppend":1,"segs":[{"utf8":"\n"}]},
{"tStartMs":67479,"dDurationMs":5161,"segs":[{"utf8":"support"},{"utf8":" it","tOffsetMs":300},{"utf8":" please","tOffsetMs":800},{"utf8":" check","tOffsetMs":1000},{"utf8":" out","tOffsetMs":1200},{"utf8":" our","tOffsetMs":1300},{"utf8":" sponsors","tOffsetMs":1500}]},
{"tStartMs":69479,"dDurationMs":5161,"segs":[{"utf8":"in"},{"utf8":" the","tOffsetMs":200},{"utf8":" description","tOffsetMs":300},{"utf8":" and","tOffsetMs":1600},{"utf8":" now","tOffsetMs":1900},{"utf8":" dear","tOffsetMs":2400},{"utf8":" friends","tOffsetMs":2700}]},
{"tStartMs":12287520,"dDurationMs":4040,"segs":[{"utf8":"Andrew"},{"utf8":" huberman","tOffsetMs":300},{"utf8":" to","tOffsetMs":1100},{"utf8":" support","tOffsetMs":1300},{"utf8":" this","tOffsetMs":1600},{"utf8":" podcast","tOffsetMs":1800}]},
{"tStartMs":12289920,"dDurationMs":3680,"segs":[{"utf8":"please"},{"utf8":" check","tOffsetMs":200},{"utf8":" out","tOffsetMs":400},{"utf8":" our","tOffsetMs":500},{"utf8":" sponsors","tOffsetMs":700},{"utf8":" in","tOffsetMs":1300},{"utf8":" the","tOffsetMs":1400}]},
{"tStartMs":12291560,"dDurationMs":5360,"segs":[{"utf8":"description"},{"utf8":" and","tOffsetMs":1000},{"utf8":" now","tOffsetMs":1200},{"utf8":" let","tOffsetMs":1500},{"utf8":" me","tOffsetMs":1600},{"utf8":" leave","tOffsetMs":1700},{"utf8":" you","tOffsetMs":1900}]},
{"tStartMs":12293600,"dDurationMs":5040,"segs":[{"utf8":"with"},{"utf8":" some","tOffsetMs":100},{"utf8":" words","tOffsetMs":300},{"utf8":" from","tOffsetMs":700},{"utf8":" Ralph","tOffsetMs":900},{"utf8":" Waldo","tOffsetMs":1500},{"utf8":" Emerson","tOffsetMs":2300}]}]}`
	lines := Transcript([]byte(asr), "")
	if len(lines) != 7 || lines[1].Text != "support it please check out our sponsors" {
		t.Fatalf("lines %+v", lines)
	}
	got := Mentions(Words([]byte(asr)))
	if len(got) != 2 {
		t.Fatalf("got %+v", got)
	}
	// "to support it ... description": from "to" (67.2) to "and" (71.08)
	if g := got[0]; math.Abs(g.From-67.2) > 0.01 || math.Abs(g.To-71.079) > 0.01 {
		t.Errorf("first %.3f-%.3f", g.From, g.To)
	}
	if g := got[1]; math.Abs(g.From-12288.62) > 0.01 || math.Abs(g.To-12292.56) > 0.01 {
		t.Errorf("second %.3f-%.3f", g.From, g.To)
	}
}
