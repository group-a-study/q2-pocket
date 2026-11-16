package youtube

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

const answer = `{"playabilityStatus":{"status":"OK"},
"videoDetails":{"videoId":"2hJkprFnzhY","title":"Russia-Ukraine War | Lex Fridman Podcast #504","lengthSeconds":"10429","author":"Lex Fridman","shortDescription":"OUTLINE:\n0:00 - Introduction\n1:00 - State of the war"},
"streamingData":{"adaptiveFormats":[
 {"itag":137,"url":"https://v/137","mimeType":"video/mp4; codecs=\"avc1.640028\"","contentLength":"999"},
 {"itag":139,"url":"https://v/139","mimeType":"audio/mp4; codecs=\"mp4a.40.5\"","contentLength":"63594043","audioSampleRate":"22050"},
 {"itag":140,"url":"https://v/140drc","mimeType":"audio/mp4; codecs=\"mp4a.40.2\"","contentLength":"168781200","audioSampleRate":"44100","isDrc":true},
 {"itag":140,"url":"https://v/140","mimeType":"audio/mp4; codecs=\"mp4a.40.2\"","contentLength":"168781199","audioSampleRate":"44100"},
 {"itag":251,"url":"https://v/251","mimeType":"audio/webm; codecs=\"opus\"","contentLength":"188948672"},
 {"itag":141,"signatureCipher":"s=x&url=y","mimeType":"audio/mp4; codecs=\"mp4a.40.2\""}]},
"captions":{"playerCaptionsTracklistRenderer":{"captionTracks":[
 {"baseUrl":"https://www.youtube.com/api/timedtext?v=2hJkprFnzhY&kind=asr&lang=en","languageCode":"en","kind":"asr","name":{"runs":[{"text":"English (auto-generated)"}]}},
 {"baseUrl":"https://www.youtube.com/api/timedtext?v=2hJkprFnzhY&lang=en","languageCode":"en","name":{"runs":[{"text":"English"}]}}]}}}`

func TestParsePlayer(t *testing.T) {
	v, err := parsePlayer(strings.NewReader(answer), "2hJkprFnzhY")
	if err != nil {
		t.Fatal(err)
	}
	if v.Length != 10429 || v.Channel != "Lex Fridman" || !strings.Contains(v.Description, "OUTLINE") {
		t.Fatalf("details: %+v", v)
	}
	if len(v.Audio) != 2 { // 139 and the plain 140: no video, no DRC copy, no Opus, nothing ciphered
		t.Fatalf("audio %+v", v.Audio)
	}
	f, ok := v.Pick()
	if !ok || f.Itag != 140 || f.URL != "https://v/140" || f.Size != 168781199 {
		t.Fatalf("picked %+v", f)
	}
	v.Length = 8 * 3600 // too many samples for the Q2's player beside Pocket: HE-AAC
	if f, _ := v.Pick(); f.Itag != 139 {
		t.Fatalf("an 8-hour video picked itag %d", f.Itag)
	}
	if tr, ok := v.Caption(); !ok || tr.Auto || tr.Lang != "en" {
		t.Fatalf("caption %+v", tr)
	}
	for status, want := range map[string]error{"LOGIN_REQUIRED": ErrRefused, "UNPLAYABLE": nil} {
		_, err := parsePlayer(strings.NewReader(`{"playabilityStatus":{"status":"`+status+`","reason":"Sign in to confirm you’re not a bot"}}`), "x")
		if err == nil || want != nil && !errors.Is(err, want) || want == nil && errors.Is(err, ErrRefused) {
			t.Fatalf("%s: %v", status, err)
		}
	}
}

func TestIndex(t *testing.T) {
	// a 5-hour episode in AAC-LC fits the Q2's player beside Pocket; a 6-hour one doesn't
	lc := Format{Itag: 140, Codec: "mp4a.40.2", SampleRate: 44100}
	if Index(5*3600, lc) > MaxIndex || Index(6*3600, lc) <= MaxIndex {
		t.Fatalf("index 5 h %d MB, 6 h %d MB", Index(5*3600, lc)>>20, Index(6*3600, lc)>>20)
	}
}

func TestPageVideos(t *testing.T) {
	page := `<a href="https://www.youtube.com/watch?v=AAAAAAAAAAA">his channel</a>
<iframe src="https://www.youtube.com/embed/2hJkprFnzhY?feature=oembed"></iframe>
<a href="https://youtu.be/2hJkprFnzhY">again</a>`
	if got := PageVideos([]byte(page)); fmt.Sprint(got) != "[2hJkprFnzhY AAAAAAAAAAA]" {
		t.Fatalf("got %v", got)
	}
}

// A visitor id from the watch page, kept; the player asked as the visionOS client with it; a
// refusal asked once more with a new id, then not again for a while.
func TestVideo(t *testing.T) {
	var pages, players atomic.Int32
	refuse := atomic.Bool{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/watch":
			pages.Add(1)
			fmt.Fprint(w, strings.Repeat("x", 70000)+`"VISITOR_DATA":"Cgt%3D"`+strings.Repeat("y", 1000))
		case "/youtubei/v1/player":
			players.Add(1)
			b, _ := io.ReadAll(r.Body)
			if r.Header.Get("X-Goog-Visitor-Id") != "Cgt=" || !strings.Contains(string(b), `"clientName":"VISIONOS"`) ||
				r.Header.Get("X-YouTube-Client-Name") != "101" {
				http.Error(w, "bad", 400)
				return
			}
			if refuse.Load() {
				fmt.Fprint(w, `{"playabilityStatus":{"status":"LOGIN_REQUIRED","reason":"Sign in"}}`)
				return
			}
			fmt.Fprint(w, answer)
		}
	}))
	defer srv.Close()
	was := Base
	Base = srv.URL
	defer func() { Base = was }()
	c := &Client{HTTP: srv.Client(), Agent: "test"}
	for i := 0; i < 2; i++ {
		if v, err := c.Video(context.Background(), "2hJkprFnzhY"); err != nil || v.Length != 10429 {
			t.Fatalf("video: %v %v", v, err)
		}
	}
	if pages.Load() != 1 || players.Load() != 2 {
		t.Fatalf("%d pages, %d player asks", pages.Load(), players.Load())
	}
	refuse.Store(true)
	if _, err := c.Video(context.Background(), "2hJkprFnzhY"); !errors.Is(err, ErrRefused) {
		t.Fatal(err)
	}
	if pages.Load() != 2 || players.Load() != 4 {
		t.Fatalf("refused: %d pages, %d player asks", pages.Load(), players.Load())
	}
	if _, err := c.Video(context.Background(), "2hJkprFnzhY"); !errors.Is(err, ErrRefused) || players.Load() != 4 {
		t.Fatalf("asked again straight after a refusal: %v", err)
	}
}
