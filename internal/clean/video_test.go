package clean

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"q2-pocket/internal/pocketcasts"
	"q2-pocket/internal/sponsor"
	"q2-pocket/internal/youtube"
)

const lexVTT = `WEBVTT

00:00:00.140 --> 00:00:03.760
The following is a conversation with Preston Stewart and Michael Kofman

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

// A Lex Fridman episode: its page (its link in Pocket Casts' show notes) embeds its video, which
// YouTube gives, with captions saying where the sponsors are; the video is as long as the episode
// less its sponsor block. Found, checked, and the pointer to the sponsors to be cut.
func TestFindVideo(t *testing.T) {
	var srv *httptest.Server
	cues := lexVTT
	for i := 0; i < 30; i++ {
		cues += fmt.Sprintf("\n00:%02d:%02d.000 --> 00:%02d:%02d.500\nline %d of the conversation\n", 2+i/60, i%60, 2+i/60, i%60, i)
	}
	captions := toJSON3(sponsor.Transcript([]byte(cues), "text/vtt"))
	var noCaptions atomic.Bool
	fallback := false
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/cache/mobile/show_notes/full/"):
			json.NewEncoder(w).Encode(map[string]any{"podcast": map[string]any{"episodes": []any{map[string]any{
				"uuid": "ep-uuid", "show_notes": "Check out our sponsors: https://lexfridman.com/sponsors/ep504-sc",
				"link": srv.URL + "/ukraine-war/?utm_source=rss"}}}})
		case r.URL.Path == "/ukraine-war/":
			fmt.Fprint(w, `<a href="https://youtube.com/@prestonstewart">his</a><iframe src="https://www.youtube.com/embed/2hJkprFnzhY?feature=oembed"></iframe>`)
		case r.URL.Path == "/watch":
			fmt.Fprint(w, `"VISITOR_DATA":"v1"`)
		case r.URL.Path == "/youtubei/v1/player":
			fmt.Fprintf(w, `{"playabilityStatus":{"status":"OK"},"videoDetails":{"videoId":"2hJkprFnzhY","title":"Russia-Ukraine War | Lex Fridman Podcast #504","lengthSeconds":"10429","author":"Lex Fridman","shortDescription":"OUTLINE:\n0:00 - Introduction\n1:00 - State of the war\n14:39 - Drone warfare"},
"streamingData":{"adaptiveFormats":[{"itag":140,"url":"https://v/140","mimeType":"audio/mp4; codecs=\"mp4a.40.2\"","contentLength":"168781199","audioSampleRate":"44100"}]},
"captions":{"playerCaptionsTracklistRenderer":{"captionTracks":[{"baseUrl":"%s/api/timedtext?v=2hJkprFnzhY&lang=en","languageCode":"en"}]}}}`, srv.URL)
		case r.URL.Path == "/api/timedtext":
			if noCaptions.Load() {
				http.NotFound(w, r)
				return
			}
			if r.URL.Query().Get("fmt") != "json3" {
				http.Error(w, "fmt", 400)
				return
			}
			w.Write(captions)
		case strings.HasPrefix(r.URL.Path, "/api/skipSegments/"):
			http.NotFound(w, r) // SponsorBlock has nothing for it
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	defer func(c, y, s string) { pocketcasts.Cache, youtube.Base, sponsor.SponsorBlockBase = c, y, s }(pocketcasts.Cache, youtube.Base, sponsor.SponsorBlockBase)
	pocketcasts.Cache, youtube.Base, sponsor.SponsorBlockBase = srv.URL+"/cache", srv.URL, srv.URL
	dir := t.TempDir()
	c := &Cleaner{Dir: dir, API: srv.Client(), Agent: "test", PC: pocketcasts.New(srv.Client(), "test", pocketcasts.Token{}),
		Settings: func() Settings {
			return Settings{Ads: true, Sponsors: true, SelfPromo: true, YouTubeFallback: fallback}
		},
		YouTube: &youtube.Client{HTTP: srv.Client(), Agent: "test"}}
	path := filepath.Join(dir, "Lex Fridman Podcast", "2026-11-10 - #504.m4a")
	e := Episode{Key: "lex", EID: "504", Path: path, Show: "Lex Fridman Podcast", Title: "#504 – Russia-Ukraine War",
		Duration: 10875, Podcast: "lex-uuid", UUID: "ep-uuid"}
	vc, why := c.FindVideo(context.Background(), e)
	if vc == nil {
		t.Fatalf("no video: %s", why)
	}
	if vc.Video.ID != "2hJkprFnzhY" || vc.Found != "its page" || vc.Format.Itag != 140 || vc.Shift != 446 {
		t.Fatalf("%+v", vc)
	}
	if len(vc.Cuts) != 1 || math.Abs(vc.Cuts[0].From-44) > 0.5 || math.Abs(vc.Cuts[0].To-53.9) > 0.2 {
		t.Fatalf("cuts %+v", vc.Cuts)
	}
	rep, chapters := c.VideoDone(context.Background(), e, vc, 10428.9)
	if rep.Video != "2hJkprFnzhY" || len(rep.Cuts) != 1 || rep.Summary() != "From YouTube: 1 sponsor read cut (10 s)" {
		t.Fatalf("%+v %q", rep, rep.Summary())
	}
	if cut := vc.Cuts[0].To - vc.Cuts[0].From; len(chapters) != 3 || math.Abs(float64(chapters[1].Start)-(60-cut)*1000) > 50 {
		t.Fatalf("chapters %+v", chapters)
	}
	lrc, err := os.ReadFile(TranscriptPath(path))
	if err != nil || strings.Contains(string(lrc), "description") || !strings.Contains(string(lrc), "And now, dear friends") {
		t.Fatalf("transcript %v:\n%s", err, lrc)
	}
	// Pocket Casts' places: the feed's copy runs 446 s longer (its sponsor block), then the cut
	if got := rep.FromFeed(3600); math.Abs(got-(3600-446-9.8)) > 0.2 {
		t.Fatalf("FromFeed(3600) = %.1f", got)
	}
	if got := rep.ToFeed(rep.FromFeed(3600)); math.Abs(got-3600) > 0.01 {
		t.Fatalf("ToFeed back = %.1f", got)
	}

	// nothing to check the video with (no captions, nothing in SponsorBlock): it comes all the same,
	// not marked Ads Not Removed, for a Remove Ads Again once SponsorBlock has its segments; with
	// the fallback set, the feed's copy comes instead
	noCaptions.Store(true)
	vc, why = c.FindVideo(context.Background(), e)
	if vc == nil || vc.Unchecked == "" || len(vc.Cuts) != 0 {
		t.Fatalf("unchecked video: %+v %s", vc, why)
	}
	rep, _ = c.VideoDone(context.Background(), e, vc, 10428.9)
	if rep.Unchecked || rep.Summary() != "From YouTube: not checked (no captions, nothing in SponsorBlock)" || Load(dir, "lex", "504").Unchecked {
		t.Fatalf("unchecked report %+v %q", rep, rep.Summary())
	}
	fallback = true
	if vc, why := c.FindVideo(context.Background(), e); vc != nil || !strings.Contains(why, "nothing could check") {
		t.Fatalf("with the fallback: %+v %q", vc, why)
	}
	fallback = false
	noCaptions.Store(false)

	// a video only its notes link to, titled otherwise and of another length: not the episode
	e.Podcast, e.UUID = "", ""
	e.Links = []string{"yt:AAAAAAAAAAA"}
	if vc, _ := c.FindVideo(context.Background(), e); vc != nil {
		t.Fatalf("took the notes' video: %+v", vc.Video)
	}
}

func TestSameEpisode(t *testing.T) {
	v := &youtube.Video{Title: "Russia-Ukraine War | Lex Fridman Podcast #504", Length: 10429}
	for _, c := range []struct {
		dur  int
		page bool
		want bool
	}{
		{10875, true, true},   // the podcast has a 7-minute sponsor block
		{10875, false, false}, // linked from the notes, another title
		{0, true, true},       // length unknown
		{20000, true, false},  // far longer: another video
		{9000, true, false},   // shorter than the video by much
	} {
		if ok, why := sameEpisode(Episode{Title: "#504 – Preston Stewart", Duration: c.dur}, v, c.page); ok != c.want {
			t.Errorf("%+v: %v %s", c, ok, why)
		}
	}
	if ok, _ := sameEpisode(Episode{Title: "Russia-Ukraine War", Duration: 10875}, v, false); !ok {
		t.Error("titled alike, from the notes")
	}
}

// toJSON3 is cues as an uploader's json3 track: an event a line.
func toJSON3(cues []sponsor.Cue) []byte {
	type seg struct {
		Text string `json:"utf8"`
	}
	type event struct {
		Start int64 `json:"tStartMs"`
		Dur   int64 `json:"dDurationMs"`
		Segs  []seg `json:"segs"`
	}
	var evs []event
	for _, c := range cues {
		evs = append(evs, event{int64(c.From * 1000), int64((c.To - c.From) * 1000), []seg{{c.Text}}})
	}
	b, _ := json.Marshal(map[string]any{"wireMagic": "pb3", "events": evs})
	return b
}
